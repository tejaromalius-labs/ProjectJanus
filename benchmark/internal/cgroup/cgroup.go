// Package cgroup reads cgroup v2 CPU and memory counters.
package cgroup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Resources is one cgroup reading plus backend-reported gauges.
type Resources struct {
	Stamp     int64   `json:"stamp_ns"`
	Usage     uint64  `json:"usage_us"`
	Throttled uint64  `json:"throttled_us"`
	Periods   uint64  `json:"throttled_periods"`
	Memory    uint64  `json:"memory_bytes"`
	Quota     float64 `json:"quota_cores"`
	Active    int64   `json:"active"`
	Queued    int64   `json:"queued"`
	GCPause   uint64  `json:"gc_pause_ns"`
	Error     string  `json:"error,omitempty"`
}

// Self returns the cgroup directory of the current process.
func Self() string {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		panic(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "0::") {
			return filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"))
		}
	}
	panic("cgroup v2 unavailable")
}

// Read returns CPU usage, throttling, quota and current memory for a cgroup.
func Read(path string) Resources {
	r := Resources{Stamp: time.Now().UnixNano()}
	b, err := os.ReadFile(filepath.Join(path, "cpu.stat"))
	if err != nil {
		r.Error = err.Error()
		return r
	}
	fields := strings.Fields(string(b))
	for i := 0; i+1 < len(fields); i += 2 {
		v, _ := strconv.ParseUint(fields[i+1], 10, 64)
		switch fields[i] {
		case "usage_usec":
			r.Usage = v
		case "nr_throttled":
			r.Periods = v
		case "throttled_usec":
			r.Throttled = v
		}
	}
	b, err = os.ReadFile(filepath.Join(path, "cpu.max"))
	if err != nil {
		r.Error = err.Error()
		return r
	}
	fields = strings.Fields(string(b))
	if len(fields) != 2 || fields[0] == "max" {
		r.Error = "finite CPU quota required"
		return r
	}
	q, _ := strconv.ParseFloat(fields[0], 64)
	period, _ := strconv.ParseFloat(fields[1], 64)
	r.Quota = q / period
	b, _ = os.ReadFile(filepath.Join(path, "memory.current"))
	r.Memory, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return r
}
