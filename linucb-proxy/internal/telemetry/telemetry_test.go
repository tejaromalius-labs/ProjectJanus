package telemetry

import (
	"context"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/featurestore"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/linucb"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/proxy"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/registry"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCollectorComputesAndEnqueues(t *testing.T) {
	fs := featurestore.New(featurestore.NewMockSource())
	fs.Set("p1", featurestore.PodFeatures{CPUUtilization: 0.2})
	ch := make(chan ArmUpdate, 4)
	c := NewCollector(proxy.DefaultRewardConfig(), fs, ch)

	c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 50, HTTPStatus: 200})
	if c.Enqueued() != 1 {
		t.Fatalf("enqueued = %d, want 1", c.Enqueued())
	}
	u := <-ch
	if u.PodID != "p1" {
		t.Fatalf("podID = %q", u.PodID)
	}
	// 50 ms, 200, low CPU → 0.7*0.75 + 0.2 + 0.1.
	if math.Abs(u.Reward-0.825) > 1e-9 {
		t.Fatalf("reward = %v, want 0.825", u.Reward)
	}
}

// TestObservePopulatesLatencyFeature is the regression guard for the bug where
// the per-pod context vector was identical across pods (P99LatencyEMA always 0),
// leaving LinUCB unable to distinguish a fast backend from a slow one. Observe
// must feed observed latency back into the feature store.
func TestObservePopulatesLatencyFeature(t *testing.T) {
	fs := featurestore.New(featurestore.NewMockSource())
	ch := make(chan ArmUpdate, 4)
	cfg := proxy.DefaultRewardConfig()
	c := NewCollector(cfg, fs, ch)

	// First observation seeds the EMA at the raw value.
	c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 200, HTTPStatus: 200})
	f, ok := fs.Get("p1")
	if !ok {
		t.Fatal("feature store has no entry for p1 after Observe")
	}
	want := latencyNorm(200, cfg.SLATargetMs)
	if f.P99LatencyEMA != want {
		t.Fatalf("P99LatencyEMA = %v, want %v (normalized 200ms)", f.P99LatencyEMA, want)
	}
	if f.P99LatencyEMA <= 0 {
		t.Fatalf("P99LatencyEMA = %v, want > 0", f.P99LatencyEMA)
	}

	// A second, higher-latency observation must move the EMA upward (it folds,
	// not overwrites).
	c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 1000, HTTPStatus: 200})
	f2, _ := fs.Get("p1")
	if f2.P99LatencyEMA <= f.P99LatencyEMA {
		t.Fatalf("EMA did not rise after a slower observation: %v -> %v", f.P99LatencyEMA, f2.P99LatencyEMA)
	}
}

func TestCollectorDropsOnBackpressure(t *testing.T) {
	fs := featurestore.New(featurestore.NewMockSource())
	ch := make(chan ArmUpdate) // unbuffered, no reader → always full
	c := NewCollector(proxy.DefaultRewardConfig(), fs, ch)

	c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 10, HTTPStatus: 200})
	if c.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", c.Dropped())
	}
	if c.Enqueued() != 0 {
		t.Fatalf("enqueued = %d, want 0", c.Enqueued())
	}
}

func TestServerErrorYieldsZeroReward(t *testing.T) {
	fs := featurestore.New(featurestore.NewMockSource())
	fs.Set("p1", featurestore.PodFeatures{CPUUtilization: 0.2})
	ch := make(chan ArmUpdate, 1)
	c := NewCollector(proxy.DefaultRewardConfig(), fs, ch)

	c.Observe(RequestOutcome{PodID: "p1", LatencyMs: 10, HTTPStatus: 503})
	u := <-ch
	// A failed HTTP request receives no reward, including the resource term.
	if u.Reward != 0 {
		t.Fatalf("reward = %v, want 0 for 5xx", u.Reward)
	}
}

func TestWorkerAppliesUpdatesThroughEngine(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	regCfg := registry.DefaultConfig()
	reg := registry.New(regCfg, eng, discard())
	pod, _ := reg.AddPod("p1", "127.0.0.1:1")

	fs := featurestore.New(featurestore.NewMockSource())
	fs.Set("p1", featurestore.PodFeatures{CPUUtilization: 0.2})

	ch := make(chan ArmUpdate, 8)
	w := NewUpdateWorker(eng, reg, fs, ch, discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	for i := 0; i < 5; i++ {
		ch <- ArmUpdate{PodID: "p1", Reward: 0.8, Req: proxy.RequestInfo{Now: time.Now()}}
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if eng.NObs(pod.ArmID) == 5 && pod.NObs.Load() == 5 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("worker did not apply 5 updates: engine n_obs=%d registry n_obs=%d",
		eng.NObs(pod.ArmID), pod.NObs.Load())
}

// TestWorkerTrainsOnDispatchFeatures: the update must use the features the pod
// was routed at, not features refreshed while the request ran.
func TestWorkerTrainsOnDispatchFeatures(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discard())
	viaWorker, _ := reg.AddPod("p1", "127.0.0.1:1")
	direct, _ := reg.AddPod("p2", "127.0.0.1:2")

	fs := featurestore.New(featurestore.NewMockSource())
	fs.Set("p1", featurestore.PodFeatures{CPUUtilization: 0.95, ActiveConns: 0.9})
	dispatched := featurestore.PodFeatures{CPUUtilization: 0.2, ActiveConns: 0.1}
	req := proxy.RequestInfo{Now: time.Unix(100, 0), Features: dispatched}

	w := NewUpdateWorker(eng, reg, fs, nil, discard())
	w.apply(ArmUpdate{PodID: "p1", Reward: 0.8, Req: req})
	if err := eng.Update(direct.ArmID, proxy.BuildContextVector(dispatched, req), 0.8); err != nil {
		t.Fatal(err)
	}

	probe := proxy.BuildContextVector(featurestore.PodFeatures{CPUUtilization: 0.5, ActiveConns: 0.5}, req)
	got, _ := eng.Score(viaWorker.ArmID, probe)
	want, _ := eng.Score(direct.ArmID, probe)
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("worker trained on refreshed features: score %v, want %v", got, want)
	}
}

// TestFleetMedianUsesActivePodsOnly: idle WARMING pods with tiny p99 values must
// not pull the graduation reference below the established ACTIVE fleet. Over all
// pods the median here would be 50.5 ms and block the 110 ms SHADOW pod.
func TestFleetMedianUsesActivePodsOnly(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	regCfg := registry.DefaultConfig()
	regCfg.WarmupObservations = 1
	regCfg.ShadowObservations = 1
	regCfg.PromotionPollInterval = 2 * time.Millisecond
	reg := registry.New(regCfg, eng, discard())

	active, _ := reg.AddPod("active", "127.0.0.1:1")
	reg.ForceState("active", registry.StateActive)
	active.SetPodP99(100)
	shadow, _ := reg.AddPod("shadow", "127.0.0.1:2")
	reg.ForceState("shadow", registry.StateShadow)
	shadow.SetPodP99(110)
	shadow.NObs.Add(1)
	for i, id := range []string{"idle-1", "idle-2"} {
		p, _ := reg.AddPod(id, "127.0.0.1:"+string(rune('3'+i)))
		p.SetPodP99(1)
	}

	fs := featurestore.New(featurestore.NewMockSource())
	w := NewUpdateWorker(eng, reg, fs, make(chan ArmUpdate), discard())
	w.refreshFleetMedianP99()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reg.StartPromotionLoop(ctx)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if shadow.State() == registry.StateActive {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("SHADOW pod within 20%% of the ACTIVE median stayed %v", shadow.State())
}
