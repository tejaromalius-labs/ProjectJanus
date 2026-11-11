// Package featurestore holds a local, in-memory cache of per-pod telemetry
// features. Features are refreshed asynchronously off the request hot path;
// the hot path only does microsecond map reads.
package featurestore

import (
	"context"
	"sync"
	"time"
)

// PodFeatures is the raw per-pod telemetry snapshot used to build a context
// vector. Fields mirror the d=12 baseline.
type PodFeatures struct {
	CPUUtilization   float64
	MemorySaturation float64
	ActiveConns      float64
	QueueDepth       float64
	GCPauseRate      float64
	P99LatencyMs     float64   // rolling nearest-rank p99 in milliseconds
	P99LatencyEMA    float64   // normalized rolling p99 (legacy field name)
	LatencyAt        time.Time // when the latency fields were last set
	UpdatedAt        time.Time
}

// LatencyMaxAge bounds how long latency fields outlive a pod's last response.
// A refresh clears older values back to the cold-cache zero, so a pod that got
// no traffic after a fault is probed again instead of being scored on the
// fault forever. Staleness is at most LatencyMaxAge plus the refresh interval.
const LatencyMaxAge = time.Second

// Source provides feature snapshots for all pods. The real implementation
// scrapes Prometheus; the mock returns synthetic values for local runs.
type Source interface {
	Scrape(ctx context.Context) (map[string]PodFeatures, error)
}

// Store is the concurrent feature cache.
type Store struct {
	features sync.Map // map[string]PodFeatures
	source   Source
}

// New creates a feature store backed by the given source.
func New(source Source) *Store {
	return &Store{source: source}
}

// Get returns the cached features for a pod. Hot-path safe (microsecond read).
func (s *Store) Get(podID string) (PodFeatures, bool) {
	v, ok := s.features.Load(podID)
	if !ok {
		return PodFeatures{}, false
	}
	return v.(PodFeatures), true
}

// Set overwrites the cached features for a pod (used by tests and the refresh
// loop).
func (s *Store) Set(podID string, f PodFeatures) {
	s.features.Store(podID, f)
}

// StartRefreshLoop periodically scrapes the source and updates the cache. It
// runs in a goroutine and is NEVER called on the request hot path.
func (s *Store) StartRefreshLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Prime once immediately so the cache isn't empty on first requests.
	s.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refresh(ctx)
		}
	}
}

func (s *Store) refresh(ctx context.Context) {
	snap, err := s.source.Scrape(ctx)
	if err != nil {
		return // keep serving stale features; scrape errors are non-fatal
	}
	now := time.Now()
	for id, f := range snap {
		f.UpdatedAt = now
		// Resource refreshes and response observations own different fields.
		for {
			old, exists := s.features.Load(id)
			if !exists {
				if _, loaded := s.features.LoadOrStore(id, f); !loaded {
					break
				}
				continue
			}
			previous := old.(PodFeatures)
			if now.Sub(previous.LatencyAt) <= LatencyMaxAge {
				f.P99LatencyEMA = previous.P99LatencyEMA
				f.P99LatencyMs = previous.P99LatencyMs
				f.LatencyAt = previous.LatencyAt
			}
			if s.features.CompareAndSwap(id, old, f) {
				break
			}
		}
	}
}

// SetLatency updates only response-derived fields, preserving concurrent scrapes.
func (s *Store) SetLatency(podID string, p99Ms, normalized float64) {
	now := time.Now()
	for {
		old, exists := s.features.Load(podID)
		if !exists {
			if _, loaded := s.features.LoadOrStore(podID, PodFeatures{P99LatencyMs: p99Ms, P99LatencyEMA: normalized, LatencyAt: now}); !loaded {
				return
			}
			continue
		}
		f := old.(PodFeatures)
		f.P99LatencyMs, f.P99LatencyEMA, f.LatencyAt = p99Ms, normalized, now
		if s.features.CompareAndSwap(podID, old, f) {
			return
		}
	}
}
