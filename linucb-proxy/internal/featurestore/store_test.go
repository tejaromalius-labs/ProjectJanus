package featurestore

import (
	"context"
	"testing"
	"time"
)

func TestRefreshLoopPopulatesCache(t *testing.T) {
	src := NewMockSource()
	src.Set("p1", PodFeatures{CPUUtilization: 0.42})
	store := New(src)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go store.StartRefreshLoop(ctx, 5*time.Millisecond)

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if f, ok := store.Get("p1"); ok && f.CPUUtilization == 0.42 {
			if f.UpdatedAt.IsZero() {
				t.Fatal("UpdatedAt not set on refresh")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cache was not populated by refresh loop")
}

func TestGetMissingReturnsFalse(t *testing.T) {
	store := New(NewMockSource())
	if _, ok := store.Get("nope"); ok {
		t.Fatal("expected miss for unknown pod")
	}
}

// TestStaleLatencyClearedOnRefresh: a pod with no response for LatencyMaxAge
// reverts to the cold-cache latency (0); a fresh value survives the refresh.
func TestStaleLatencyClearedOnRefresh(t *testing.T) {
	src := NewMockSource()
	src.Set("stale", PodFeatures{CPUUtilization: 0.5})
	src.Set("fresh", PodFeatures{CPUUtilization: 0.5})
	store := New(src)
	store.Set("stale", PodFeatures{P99LatencyMs: 200, P99LatencyEMA: 0.2, LatencyAt: time.Now().Add(-2 * LatencyMaxAge)})
	store.SetLatency("fresh", 3, 0.003)
	store.refresh(context.Background())

	if f, _ := store.Get("stale"); f.P99LatencyMs != 0 || f.P99LatencyEMA != 0 || f.CPUUtilization != 0.5 {
		t.Fatalf("stale latency kept: %+v", f)
	}
	if f, _ := store.Get("fresh"); f.P99LatencyMs != 3 || f.P99LatencyEMA != 0.003 {
		t.Fatalf("fresh latency lost: %+v", f)
	}
}
