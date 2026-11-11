package linucb

import (
	"math"
	"testing"
)

func ctx(v float64) []float64 {
	x := make([]float64, D)
	for i := range x {
		x[i] = v
	}
	return x
}

func TestEngineRoundTrip(t *testing.T) {
	eng, err := NewEngine(D, 0.15)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	arm, err := eng.AddArm("pod-a")
	if err != nil {
		t.Fatalf("AddArm: %v", err)
	}
	if arm != 0 {
		t.Fatalf("first arm id = %d, want 0", arm)
	}

	x := ctx(0.1)
	score, err := eng.Score(arm, x)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if math.IsNaN(score) || math.IsInf(score, 0) {
		t.Fatalf("score not finite: %v", score)
	}

	if got := eng.NObs(arm); got != 0 {
		t.Fatalf("NObs before update = %d, want 0", got)
	}
	if err := eng.Update(arm, x, 0.8); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := eng.NObs(arm); got != 1 {
		t.Fatalf("NObs after update = %d, want 1", got)
	}
}

func TestResetArm(t *testing.T) {
	eng, err := NewEngine(D, 0.15)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	arm, err := eng.AddArm("pod-a")
	if err != nil {
		t.Fatalf("AddArm: %v", err)
	}
	x := ctx(0.1)
	if err := eng.Update(arm, x, 0.8); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := eng.NObs(arm); got != 1 {
		t.Fatalf("NObs before reset = %d, want 1", got)
	}

	if err := eng.ResetArm(arm, "pod-a"); err != nil {
		t.Fatalf("ResetArm: %v", err)
	}
	if got := eng.NObs(arm); got != 0 {
		t.Fatalf("NObs after reset = %d, want 0", got)
	}
	// Arm index still valid and scorable after reset.
	if _, err := eng.Score(arm, x); err != nil {
		t.Fatalf("Score after reset: %v", err)
	}
}

func TestScoreParityAcrossUpdates(t *testing.T) {
	// Build a deterministic sequence of updates and confirm the score moves in
	// the expected direction — proves the FFI passes vectors correctly and the
	// Rust math runs end-to-end from Go.
	eng, err := NewEngine(D, 0.0) // alpha=0 → pure exploitation, deterministic
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	arm, _ := eng.AddArm("pod-a")
	x := ctx(0.2)

	before, _ := eng.Score(arm, x)
	for i := 0; i < 10; i++ {
		if err := eng.Update(arm, x, 1.0); err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
	}
	after, _ := eng.Score(arm, x)

	// Positive rewards on this context should raise the exploitation score.
	if !(after > before) {
		t.Fatalf("expected score to rise after positive rewards: before=%v after=%v", before, after)
	}
	if eng.NObs(arm) != 10 {
		t.Fatalf("NObs = %d, want 10", eng.NObs(arm))
	}
}

func TestRejectsBadInputs(t *testing.T) {
	if _, err := NewEngine(0, 0.15); err == nil {
		t.Fatal("expected error for dim=0")
	}
	if _, err := NewEngine(MaxDPlusOne(), 0.15); err == nil {
		t.Fatal("expected error for dim > MAX_D")
	}

	eng, _ := NewEngine(D, 0.15)
	defer eng.Close()
	arm, _ := eng.AddArm("pod-a")

	// Dimension mismatch.
	if _, err := eng.Score(arm, []float64{0.1, 0.2}); err == nil {
		t.Fatal("expected error for short context")
	}
	// Unknown arm.
	if _, err := eng.Score(99, ctx(0.1)); err == nil {
		t.Fatal("expected error for unknown arm")
	}
}

func MaxDPlusOne() int { return 21 }
