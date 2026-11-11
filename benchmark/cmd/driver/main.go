// Command driver runs the benchmark campaign from the host: for every trial it
// asks the in-VM agent to start a fresh proxy (Janus or a baseline), warms it
// up, injects one fault scenario on one backend, and records every request.
// Trials, not requests, are the statistical units (see PROTOCOL.md).
package main

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	host      = flag.String("host", "", "VM IPv4 address")
	output    = flag.String("out", "", "new results directory")
	preflight = flag.Bool("preflight", false, "run one short Janus trial to validate the setup")
	seeds     = flag.Int("seeds", 6, "independent repetitions of the full grid")
	warmup    = flag.Duration("warmup", 30*time.Second, "warmup duration for every trial")
	warmupRPS = flag.Int("warmup-rps", 600, "warmup request rate")
	rps       = flag.Int("rps", 600, "measured request rate")
	recovery  = flag.Duration("recovery", 3*time.Second, "recovery phase after each fault (3s in the frozen protocol)")
)

var (
	policies  = []string{"janus", "rr", "lc", "p2c", "pewma"}
	scenarios = []string{"healthy", "latency", "errors", "cpu"}
	ports     = []int{19001, 19002, 19003, 19004}
	admin     = &http.Client{Timeout: 15 * time.Second}
	largeBody = strings.Repeat("x", 4096)
)

type specification struct {
	Index    int    `json:"index"`
	Seed     int    `json:"seed"`
	Policy   string `json:"policy"`
	Scenario string `json:"scenario"`
	Port     int    `json:"fault_port"`
}
type resource struct {
	Stamp     int64   `json:"stamp_ns"`
	Usage     uint64  `json:"usage_us"`
	Throttled uint64  `json:"throttled_us"`
	Periods   uint64  `json:"throttled_periods"`
	Memory    uint64  `json:"memory_bytes"`
	Quota     float64 `json:"quota_cores"`
	Error     string  `json:"error,omitempty"`
}
type snapshot struct {
	Policy        string              `json:"policy"`
	Proxy         resource            `json:"proxy"`
	Backends      map[string]resource `json:"backends"`
	FeatureErrors uint64              `json:"feature_errors"`
	Metrics       string              `json:"metrics,omitempty"`
	Stats         string              `json:"stats,omitempty"`
}
type phase struct {
	Name              string   `json:"name"`
	RPS               int      `json:"rps"`
	Planned           int      `json:"planned"`
	Completed         int      `json:"completed"`
	Failures          int      `json:"failures"`
	TransportFailures int      `json:"transport_failures"`
	Unattributed      int      `json:"unattributed"`
	FaultHits         int      `json:"fault_hits"`
	LateFaultHits     int      `json:"late_fault_hits"`
	LateRequests      int      `json:"late_requests"`
	P50               float64  `json:"p50_ms"`
	P95               float64  `json:"p95_ms"`
	P99               float64  `json:"p99_ms"`
	ServiceP99        float64  `json:"service_p99_ms"`
	ScheduleP99       float64  `json:"schedule_p99_ms"`
	Seconds           float64  `json:"seconds"`
	AchievedRPS       float64  `json:"achieved_rps"`
	Raw               string   `json:"raw"`
	Before            snapshot `json:"before"`
	After             snapshot `json:"after"`
}
type trial struct {
	Specification specification `json:"trial"`
	Started       string        `json:"started_utc"`
	Warmup        phase         `json:"warmup"`
	Phases        []phase       `json:"phases"`
	Invalid       []string      `json:"invalid"`
}
type sample struct {
	Index                   int
	Scheduled               int64
	Delay, Service, Elapsed float64
	Status, Backend         int
	Transport               bool
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func save(name string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(*output, name), append(b, '\n'), 0o644))
}

func get(ctx context.Context, url string) []byte {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	check(err)
	resp, err := admin.Do(req)
	check(err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	check(err)
	if resp.StatusCode != http.StatusOK {
		panic(fmt.Sprintf("%s: HTTP %d %s", url, resp.StatusCode, b))
	}
	return b
}

func agent(ctx context.Context, path string) []byte {
	return get(ctx, "http://"+*host+":18090"+path)
}

func snapshotNow(ctx context.Context) snapshot {
	var s snapshot
	check(json.Unmarshal(agent(ctx, "/snapshot"), &s))
	return s
}

func control(ctx context.Context, port int, mode string) {
	get(ctx, fmt.Sprintf("http://%s:%d/control?mode=%s", *host, port, mode))
}

func quantile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	return values[int(math.Ceil(q*float64(len(values))))-1]
}

// load sends an open-loop constant-rate stream (10% 4 KiB POSTs) and records
// latency from each request's scheduled arrival, so queueing is not hidden.
func load(ctx context.Context, client *http.Client, spec specification, name string, rate int, duration time.Duration) phase {
	p := phase{Name: name, RPS: rate, Planned: int(duration.Seconds() * float64(rate)), Before: snapshotNow(ctx)}
	records := make([]sample, p.Planned)
	var wg sync.WaitGroup
	started := time.Now()
	launched := 0
	for i := 0; i < p.Planned; i++ {
		scheduled := started.Add(time.Duration(float64(i) * float64(time.Second) / float64(rate)))
		timer := time.NewTimer(time.Until(scheduled))
		select {
		case <-ctx.Done():
			timer.Stop()
			wg.Wait()
			panic(ctx.Err())
		case <-timer.C:
		}
		wg.Add(1)
		launched++
		go func(i int, scheduled time.Time) {
			defer wg.Done()
			start := time.Now()
			rec := sample{Index: i, Scheduled: scheduled.UnixNano(), Delay: float64(start.Sub(scheduled).Nanoseconds()) / 1e6}
			method, body := http.MethodGet, io.Reader(nil)
			if i%10 == 0 {
				method, body = http.MethodPost, strings.NewReader(largeBody)
			}
			req, err := http.NewRequestWithContext(ctx, method, "http://"+*host+":18080/work", body)
			if err == nil {
				var resp *http.Response
				resp, err = client.Do(req)
				if err == nil {
					rec.Status = resp.StatusCode
					rec.Backend, _ = strconv.Atoi(resp.Header.Get("X-Backend"))
					_, err = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
			end := time.Now()
			rec.Service = float64(end.Sub(start).Nanoseconds()) / 1e6
			rec.Elapsed = float64(end.Sub(scheduled).Nanoseconds()) / 1e6
			rec.Transport = err != nil
			records[i] = rec
		}(i, scheduled)
	}
	wg.Wait()
	p.Seconds = time.Since(started).Seconds()
	p.Completed = launched
	p.AchievedRPS = float64(launched) / p.Seconds
	p.After = snapshotNow(ctx)

	latencies, service, delay := make([]float64, 0, launched), make([]float64, 0, launched), make([]float64, 0, launched)
	p.Raw = fmt.Sprintf("trial-%03d-%s.csv.gz", spec.Index, name)
	file, err := os.Create(filepath.Join(*output, p.Raw))
	check(err)
	compressed, err := gzip.NewWriterLevel(file, gzip.BestSpeed)
	check(err)
	writer := csv.NewWriter(compressed)
	check(writer.Write([]string{"index", "scheduled_unix_ns", "schedule_delay_ms", "service_ms", "scheduled_latency_ms", "status", "backend", "transport_failure"}))
	for _, r := range records {
		latencies = append(latencies, r.Elapsed)
		service = append(service, r.Service)
		delay = append(delay, r.Delay)
		if r.Status >= 500 || r.Transport {
			p.Failures++
		}
		if r.Transport {
			p.TransportFailures++
		}
		if r.Backend == 0 {
			p.Unattributed++
		}
		late := r.Index >= p.Planned/2
		if late {
			p.LateRequests++
		}
		if r.Backend == spec.Port {
			p.FaultHits++
			if late {
				p.LateFaultHits++
			}
		}
		check(writer.Write([]string{strconv.Itoa(r.Index), strconv.FormatInt(r.Scheduled, 10), fmt.Sprintf("%.6f", r.Delay), fmt.Sprintf("%.6f", r.Service), fmt.Sprintf("%.6f", r.Elapsed), strconv.Itoa(r.Status), strconv.Itoa(r.Backend), strconv.FormatBool(r.Transport)}))
	}
	writer.Flush()
	check(writer.Error())
	check(compressed.Close())
	check(file.Close())
	p.P50 = quantile(append([]float64(nil), latencies...), .5)
	p.P95 = quantile(append([]float64(nil), latencies...), .95)
	p.P99 = quantile(latencies, .99)
	p.ServiceP99 = quantile(service, .99)
	p.ScheduleP99 = quantile(delay, .99)
	return p
}

// allActive reports whether every Janus pod in /stats output is ACTIVE.
func allActive(stats string) bool {
	active := 0
	for _, line := range strings.Split(stats, "\n") {
		if strings.HasPrefix(line, "pod=") {
			if !strings.Contains(line, "state=ACTIVE") {
				return false
			}
			active++
		}
	}
	return active == len(ports)
}

func design() []specification {
	if *preflight {
		return []specification{{0, 0, "janus", "cpu", ports[0]}}
	}
	out := []specification{}
	rng := rand.New(rand.NewSource(20261109))
	for seed := 0; seed < *seeds; seed++ {
		order := append([]string(nil), scenarios...)
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, scenario := range order {
			names := append([]string(nil), policies...)
			rng.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
			for _, policy := range names {
				out = append(out, specification{len(out), seed, policy, scenario, ports[seed%len(ports)]})
			}
		}
	}
	return out
}

func main() {
	flag.Parse()
	if *host == "" || *output == "" {
		panic("-host and -out required")
	}
	check(os.MkdirAll(*output, 0o755))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	plan := design()
	save("design.json", plan)
	results, err := os.OpenFile(filepath.Join(*output, "trials.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	check(err)
	defer results.Close()
	encoder := json.NewEncoder(results)
	campaignStart := time.Now()
	faultDuration := 10 * time.Second
	if *preflight {
		faultDuration = 2 * time.Second
	}

	for _, spec := range plan {
		if ctx.Err() != nil {
			panic(ctx.Err())
		}
		for _, port := range ports {
			control(ctx, port, "healthy")
		}
		agent(ctx, "/start?policy="+spec.Policy)
		transport := &http.Transport{MaxIdleConns: 1024, MaxIdleConnsPerHost: 256}
		client := &http.Client{Timeout: 2 * time.Second, Transport: transport}
		record := trial{Specification: spec, Started: time.Now().UTC().Format(time.RFC3339Nano), Invalid: []string{}}

		record.Warmup = load(ctx, client, spec, "warmup", *warmupRPS, *warmup)
		if record.Warmup.Failures != 0 {
			record.Invalid = append(record.Invalid, "warmup requests failed")
		}
		if spec.Policy == "janus" && !allActive(record.Warmup.After.Stats) {
			record.Invalid = append(record.Invalid, "Janus pods not all ACTIVE after warmup")
		}

		if spec.Scenario == "healthy" {
			record.Phases = append(record.Phases, load(ctx, client, spec, "healthy", *rps, 10*time.Second))
		} else {
			record.Phases = append(record.Phases, load(ctx, client, spec, "healthy", *rps, 3*time.Second))
			control(ctx, spec.Port, spec.Scenario)
			fault := load(ctx, client, spec, "fault", *rps, faultDuration)
			record.Phases = append(record.Phases, fault)
			if spec.Scenario == "cpu" {
				id := fmt.Sprintf("backend-%d", spec.Port-ports[0])
				before, after := fault.Before.Backends[id], fault.After.Backends[id]
				if after.Throttled <= before.Throttled || after.Periods <= before.Periods {
					record.Invalid = append(record.Invalid, "CPU stimulus did not increase throttling")
				}
			}
			control(ctx, spec.Port, "healthy")
			record.Phases = append(record.Phases, load(ctx, client, spec, "recovery", *rps, *recovery))
		}

		for _, p := range append([]phase{record.Warmup}, record.Phases...) {
			if p.Completed != p.Planned {
				record.Invalid = append(record.Invalid, p.Name+": incomplete arrivals")
			}
			if p.ScheduleP99 > 10 {
				record.Invalid = append(record.Invalid, p.Name+": generator p99 schedule delay exceeds 10ms")
			}
			if p.After.FeatureErrors != 0 {
				record.Invalid = append(record.Invalid, p.Name+": missing or stale backend resource samples")
			}
			if spec.Policy == "janus" && !strings.Contains(p.After.Metrics, "janus_telemetry_dropped_total 0\n") {
				record.Invalid = append(record.Invalid, p.Name+": Janus telemetry drops")
			}
		}
		agent(ctx, "/stop")
		check(encoder.Encode(record))
		check(results.Sync())
		transport.CloseIdleConnections()
		fmt.Printf("trial=%03d seed=%d policy=%s scenario=%s invalid=%v elapsed=%.1fs\n", spec.Index, spec.Seed, spec.Policy, spec.Scenario, record.Invalid, time.Since(campaignStart).Seconds())
	}
	save("completion.json", map[string]any{"trials": len(plan), "elapsed_seconds": time.Since(campaignStart).Seconds(), "completed_utc": time.Now().UTC().Format(time.RFC3339Nano)})
}
