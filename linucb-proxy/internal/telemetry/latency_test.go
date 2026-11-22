package telemetry

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/featurestore"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/proxy"
)

func TestRollingP99AndWindowRecovery(t *testing.T) {
	fs := featurestore.New(featurestore.NewMockSource())
	c := NewCollector(proxy.DefaultRewardConfig(), fs, make(chan ArmUpdate, 300))
	for i := 0; i < 99; i++ {
		c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 10, HTTPStatus: 200})
	}
	c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 1000, HTTPStatus: 200})
	f, _ := fs.Get("p1")
	if f.P99LatencyMs != 10 {
		t.Fatalf("p99=%g want 10", f.P99LatencyMs)
	}
	c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 1000, HTTPStatus: 200})
	f, _ = fs.Get("p1")
	if f.P99LatencyMs != 1000 {
		t.Fatalf("p99=%g want 1000", f.P99LatencyMs)
	}
	for i := 0; i < 256; i++ {
		c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 20, HTTPStatus: 200})
	}
	f, _ = fs.Get("p1")
	if f.P99LatencyMs != 20 {
		t.Fatalf("old outliers retained: p99=%g", f.P99LatencyMs)
	}
}

// TestStaleSamplesDoNotOutvoteFreshOnes: slow samples from before a traffic gap
// (a fault the pod has recovered from) must not hold its p99 high once a fresh
// response arrives; FIFO eviction alone needed ~250 new responses.
func TestStaleSamplesDoNotOutvoteFreshOnes(t *testing.T) {
	fs := featurestore.New(featurestore.NewMockSource())
	c := NewCollector(proxy.DefaultRewardConfig(), fs, make(chan ArmUpdate, 300))
	for i := 0; i < 240; i++ {
		c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 3, HTTPStatus: 200})
	}
	for i := 0; i < 10; i++ {
		c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 200, HTTPStatus: 200})
	}
	old := time.Now().Add(-2 * featurestore.LatencyMaxAge)
	w := c.latencies["p1"]
	for i := 0; i < w.count; i++ {
		w.at[i] = old
	}
	c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 4, HTTPStatus: 200})
	if f, _ := fs.Get("p1"); f.P99LatencyMs != 4 {
		t.Fatalf("stale samples retained: p99=%g want 4", f.P99LatencyMs)
	}
}

func TestConnectionEventLayoutAndRewardIsolation(t *testing.T) {
	b := make([]byte, 40)
	binary.LittleEndian.PutUint64(b[:8], 100)
	binary.LittleEndian.PutUint64(b[8:16], 200)
	copy(b[16:20], []byte{127, 0, 0, 1})
	binary.LittleEndian.PutUint16(b[32:34], 9001)
	binary.LittleEndian.PutUint16(b[34:36], 2)
	e, ok := parseConnectionEvent(b)
	if !ok || e.endpoint != "127.0.0.1:9001" {
		t.Fatalf("event=%+v ok=%v", e, ok)
	}
	if _, ok := parseConnectionEvent(b[:20]); ok {
		t.Fatal("accepted truncated event")
	}
	binary.LittleEndian.PutUint64(b[8:16], 99)
	if _, ok := parseConnectionEvent(b); ok {
		t.Fatal("accepted negative duration")
	}
	c := NewCollector(proxy.DefaultRewardConfig(), featurestore.New(featurestore.NewMockSource()), make(chan ArmUpdate, 1))
	c.ObserveConnection("p1", 0.1)
	if c.Enqueued() != 0 || c.Connections()["p1"].Count != 1 {
		t.Fatal("connection contaminated HTTP rewards")
	}
}
