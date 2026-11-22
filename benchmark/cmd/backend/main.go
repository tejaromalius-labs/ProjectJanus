// Command backend is a synthetic CPU-limited HTTP service with fault modes.
// Each request burns 1 ms of thread CPU and sleeps 1 ms; at most 16 run at once.
// Faults (set via /control?mode=): latency adds 200 ms, errors returns 503 on
// every fifth request, cpu burns 50 ms of CPU every 100 ms in the background.
// Every 250 ms it writes its cgroup reading to <dir>/backend-<port>.json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/tejaromalius-labs/ProjectJanus/benchmark/internal/cgroup"
	"golang.org/x/sys/unix"
)

var sink atomic.Uint64

// cpuWork spins until this OS thread has used the given CPU time.
func cpuWork(duration time.Duration) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var stamp unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_THREAD_CPUTIME_ID, &stamp); err != nil {
		panic(err)
	}
	end := stamp.Nano() + int64(duration)
	x := uint64(7)
	for {
		for i := 0; i < 1000; i++ {
			x = x*6364136223846793005 + 1
		}
		if err := unix.ClockGettime(unix.CLOCK_THREAD_CPUTIME_ID, &stamp); err != nil {
			panic(err)
		}
		if stamp.Nano() >= end {
			break
		}
	}
	sink.Store(x)
}

func main() {
	port := flag.Int("port", 19001, "listen port")
	dir := flag.String("dir", "/tmp/janus-bench", "resource sample directory")
	flag.Parse()

	group := cgroup.Self()
	var mode atomic.Value
	mode.Store("healthy")
	var count atomic.Uint64
	var active, queued atomic.Int64
	slots := make(chan struct{}, 16)
	small, large := make([]byte, 128), make([]byte, 4096)

	go func() {
		for range time.Tick(100 * time.Millisecond) {
			if mode.Load() == "cpu" {
				cpuWork(50 * time.Millisecond)
			}
		}
	}()
	go func() {
		name := filepath.Join(*dir, fmt.Sprintf("backend-%d.json", *port))
		for range time.Tick(250 * time.Millisecond) {
			r := cgroup.Read(group)
			r.Active, r.Queued = active.Load(), queued.Load()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			r.GCPause = m.PauseTotalNs
			data, _ := json.Marshal(r)
			if os.WriteFile(name+".tmp", data, 0o644) == nil {
				_ = os.Rename(name+".tmp", name)
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		switch m := r.URL.Query().Get("mode"); m {
		case "healthy", "latency", "errors", "cpu":
			mode.Store(m)
			count.Store(0)
			fmt.Fprint(w, "ok")
		default:
			http.Error(w, "unknown mode", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		queued.Add(1)
		select {
		case slots <- struct{}{}:
		case <-r.Context().Done():
			queued.Add(-1)
			return
		}
		queued.Add(-1)
		active.Add(1)
		defer func() { active.Add(-1); <-slots }()
		_, _ = io.Copy(io.Discard, r.Body)
		n := count.Add(1)
		current := mode.Load()
		cpuWork(time.Millisecond)
		time.Sleep(time.Millisecond)
		if current == "latency" {
			time.Sleep(200 * time.Millisecond)
		}
		w.Header().Set("X-Backend", strconv.Itoa(*port))
		if current == "errors" && n%5 == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		if r.Method == http.MethodPost {
			_, _ = w.Write(large)
		} else {
			_, _ = w.Write(small)
		}
	})
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), mux))
}
