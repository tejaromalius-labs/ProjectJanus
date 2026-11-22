package registry

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/linucb"
)

func testRegistry(t *testing.T, cfg Config) (*Registry, *linucb.Engine) {
	t.Helper()
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, eng, log), eng
}

func TestRemovePodResetsArm(t *testing.T) {
	reg, eng := testRegistry(t, DefaultConfig())
	defer eng.Close()

	pod, err := reg.AddPod("p1", "127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	// Accumulate observations on the pod's arm.
	x := make([]float64, linucb.D)
	for i := range x {
		x[i] = 0.1
	}
	for i := 0; i < 3; i++ {
		if err := eng.Update(pod.ArmID, x, 0.5); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	if got := eng.NObs(pod.ArmID); got != 3 {
		t.Fatalf("NObs before remove = %d, want 3", got)
	}

	armID := pod.ArmID
	reg.RemovePod("p1")

	if _, ok := reg.Get("p1"); ok {
		t.Fatal("pod still present after RemovePod")
	}
	if got := eng.NObs(armID); got != 0 {
		t.Fatalf("arm n_obs after remove = %d, want 0 (reset)", got)
	}
}

func TestLifecyclePromotion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WarmupObservations = 5
	cfg.ShadowObservations = 10
	cfg.PromotionPollInterval = 2 * time.Millisecond
	reg, eng := testRegistry(t, cfg)
	defer eng.Close()

	pod, err := reg.AddPod("p1", "127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if pod.State() != StateWarming {
		t.Fatalf("new pod state = %v, want WARMING", pod.State())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reg.StartPromotionLoop(ctx)

	// Cross warmup threshold → SHADOW.
	for i := 0; i < 5; i++ {
		pod.NObs.Add(1)
	}
	if !waitForState(pod, StateShadow, 200*time.Millisecond) {
		t.Fatalf("pod did not reach SHADOW, state=%v", pod.State())
	}

	// Cross shadow threshold (latency baseline is zero → tolerance passes) → ACTIVE.
	for i := 0; i < 5; i++ {
		pod.NObs.Add(1)
	}
	if !waitForState(pod, StateActive, 200*time.Millisecond) {
		t.Fatalf("pod did not reach ACTIVE, state=%v", pod.State())
	}
}

func TestShadowGraduationBlockedByLatency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WarmupObservations = 1
	cfg.ShadowObservations = 2
	cfg.ShadowLatencyTolerancePct = 20
	cfg.PromotionPollInterval = 2 * time.Millisecond
	reg, eng := testRegistry(t, cfg)
	defer eng.Close()

	pod, _ := reg.AddPod("slow", "127.0.0.1:8080")
	reg.SetFleetMedianP99(100) // fleet median 100ms; tolerance → 120ms
	pod.SetPodP99(500)         // way over tolerance

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reg.StartPromotionLoop(ctx)

	pod.NObs.Add(3) // crosses both thresholds
	// Should reach SHADOW but NOT ACTIVE (latency blocks graduation).
	if !waitForState(pod, StateShadow, 200*time.Millisecond) {
		t.Fatalf("expected SHADOW, got %v", pod.State())
	}
	time.Sleep(40 * time.Millisecond)
	if pod.State() == StateActive {
		t.Fatal("pod graduated to ACTIVE despite high latency")
	}

	// Bring latency within tolerance → should now graduate.
	pod.SetPodP99(110)
	if !waitForState(pod, StateActive, 200*time.Millisecond) {
		t.Fatalf("expected ACTIVE after latency recovered, got %v", pod.State())
	}
}

func waitForState(p *PodEntry, want PodState, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.State() == want {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return p.State() == want
}
