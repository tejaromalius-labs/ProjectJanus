// Command agent runs inside the test VM as root. It
//   - serves a Prometheus-compatible /api/v1/query endpoint so the unmodified
//     Janus proxy can read backend CPU, memory, connections, queue and GC
//     through its normal Prometheus feature source;
//   - starts and stops the proxy under test as a CPU-limited systemd unit;
//   - reports cgroup snapshots of the proxy and backends to the driver.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tejaromalius-labs/ProjectJanus/benchmark/internal/cgroup"
)

const unit = "janus-bench-proxy"

type features struct {
	CPU, Memory, Active, Queue, GC float64
}

type agent struct {
	dir, bin, backends string
	proxyPort          int
	ports              map[string]string // backend id -> port
	ids                []string

	mu       sync.Mutex
	previous map[string]cgroup.Resources
	latest   map[string]cgroup.Resources
	features map[string]features
	errors   atomic.Uint64
	policy   string
}

// sample refreshes per-backend features from the backend sample files.
func (a *agent) sample() {
	a.mu.Lock()
	defer a.mu.Unlock()
	clamp := func(v float64) float64 { return math.Max(0, math.Min(1, v)) }
	for _, id := range a.ids {
		b, err := os.ReadFile(filepath.Join(a.dir, "backend-"+a.ports[id]+".json"))
		var r cgroup.Resources
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err != nil || r.Error != "" || time.Now().UnixNano()-r.Stamp > int64(2*time.Second) || r.Quota <= 0 {
			a.errors.Add(1)
			delete(a.features, id)
			continue
		}
		old, ok := a.previous[id]
		a.latest[id] = r
		if ok && r.Stamp <= old.Stamp {
			continue
		}
		a.previous[id] = r
		if !ok {
			continue
		}
		dt := float64(r.Stamp-old.Stamp) / 1e9
		a.features[id] = features{
			CPU:    clamp(float64(r.Usage-old.Usage) / 1e6 / dt / r.Quota),
			Memory: clamp(float64(r.Memory) / (128 << 20)),
			Active: clamp(float64(r.Active) / 16),
			Queue:  clamp(float64(r.Queued) / 16),
			GC:     clamp(float64(r.GCPause-old.GCPause) / 1e9 / dt),
		}
	}
}

// pick maps a Janus Prometheus query to the matching feature.
func pick(query string) func(features) float64 {
	switch {
	case strings.Contains(query, "container_cpu_usage_seconds_total"):
		return func(f features) float64 { return f.CPU }
	case strings.Contains(query, "container_memory_working_set_bytes"):
		return func(f features) float64 { return f.Memory }
	case strings.Contains(query, "janus_app_active_connections_ratio"):
		return func(f features) float64 { return f.Active }
	case strings.Contains(query, "janus_app_queue_depth_ratio"):
		return func(f features) float64 { return f.Queue }
	case strings.Contains(query, "go_gc_duration_seconds_sum"):
		return func(f features) float64 { return f.GC }
	}
	return nil
}

// writeVector writes a Prometheus instant-vector response with one sample per pod.
func writeVector(w io.Writer, values map[string]float64) {
	type item struct {
		Metric map[string]string `json:"metric"`
		Value  [2]any            `json:"value"`
	}
	result := []item{}
	now := float64(time.Now().UnixMilli()) / 1000
	for pod, v := range values {
		result = append(result, item{Metric: map[string]string{"pod": pod}, Value: [2]any{now, fmt.Sprintf("%g", v)}})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
}

func (a *agent) query(w http.ResponseWriter, r *http.Request) {
	get := pick(r.URL.Query().Get("query"))
	values := map[string]float64{}
	if get != nil {
		a.mu.Lock()
		for id, f := range a.features {
			values[id] = get(f)
		}
		a.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	writeVector(w, values)
}

func systemctl(args ...string) { _ = exec.Command("systemctl", args...).Run() }

func (a *agent) stop() {
	systemctl("stop", unit+".service")
	systemctl("reset-failed", unit+".service")
}

func (a *agent) start(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("policy")
	listen := fmt.Sprintf("-listen=:%d", a.proxyPort)
	var command []string
	switch name {
	case "janus":
		command = []string{filepath.Join(a.bin, "proxy"), listen, "-backends=" + a.backends,
			fmt.Sprintf("-prometheus-url=http://127.0.0.1%s", *addr), "-feature-interval=500ms"}
	case "rr", "lc", "p2c", "pewma":
		command = []string{filepath.Join(a.bin, "baseline"), listen, "-policy=" + name, "-backends=" + a.backends}
	default:
		http.Error(w, "unknown policy", http.StatusBadRequest)
		return
	}
	a.stop()
	args := append([]string{"--quiet", "--unit=" + unit,
		"--property=CPUQuota=100%", "--property=AllowedCPUs=1", "--property=MemoryMax=512M",
		"--property=Environment=GOMAXPROCS=1", "--property=Environment=GOMEMLIMIT=384MiB", "--"}, command...)
	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		http.Error(w, fmt.Sprintf("systemd-run: %v: %s", err, out), http.StatusInternalServerError)
		return
	}
	health := fmt.Sprintf("http://127.0.0.1:%d/healthz", a.proxyPort)
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(health); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				a.mu.Lock()
				a.policy = name
				a.mu.Unlock()
				a.errors.Store(0)
				fmt.Fprint(w, "ok")
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	http.Error(w, "proxy did not become healthy", http.StatusInternalServerError)
}

func (a *agent) fetch(path string) string {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", a.proxyPort, path))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func (a *agent) snapshot(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	backends := make(map[string]cgroup.Resources, len(a.latest))
	for id, res := range a.latest {
		backends[id] = res
	}
	name := a.policy
	a.mu.Unlock()
	out := map[string]any{
		"policy":         name,
		"proxy":          cgroup.Read("/sys/fs/cgroup/system.slice/" + unit + ".service"),
		"backends":       backends,
		"feature_errors": a.errors.Load(),
	}
	if name == "janus" {
		out["metrics"] = a.fetch("/metrics")
		out["stats"] = a.fetch("/stats")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

var addr = flag.String("listen", ":18090", "agent listen address")

func main() {
	dir := flag.String("dir", "/tmp/janus-bench/samples", "backend sample directory")
	bin := flag.String("bin", "/tmp/janus-bench", "directory holding the proxy and baseline binaries")
	backends := flag.String("backends", "backend-0=127.0.0.1:19001,backend-1=127.0.0.1:19002,backend-2=127.0.0.1:19003,backend-3=127.0.0.1:19004", "id=host:port,...")
	proxyPort := flag.Int("proxy-port", 18080, "port of the proxy under test")
	flag.Parse()

	a := &agent{dir: *dir, bin: *bin, backends: *backends, proxyPort: *proxyPort,
		ports: map[string]string{}, previous: map[string]cgroup.Resources{},
		latest: map[string]cgroup.Resources{}, features: map[string]features{}}
	for _, entry := range strings.Split(*backends, ",") {
		id, hostport, _ := strings.Cut(entry, "=")
		_, port, err := net.SplitHostPort(hostport)
		if err != nil {
			log.Fatalf("invalid backend %q: %v", entry, err)
		}
		a.ports[id] = port
		a.ids = append(a.ids, id)
	}
	go func() {
		for range time.Tick(500 * time.Millisecond) {
			a.sample()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", a.query)
	mux.HandleFunc("/start", a.start)
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) { a.stop(); fmt.Fprint(w, "ok") })
	mux.HandleFunc("/snapshot", a.snapshot)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
