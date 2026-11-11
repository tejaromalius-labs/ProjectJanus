package proxy

import (
	"math"
	"math/rand"
	"testing"
)

func TestComputeRewardLatencyCurve(t *testing.T) {
	cfg := DefaultRewardConfig()
	cases := []struct{ ms, want float64 }{
		{0, 1.0},
		{50, 0.825},
		{100, 0.65},
		{200, 0.3 + 0.35*math.Exp(-1)},
	}
	for _, c := range cases {
		if got := ComputeReward(c.ms, 200, 0.10, cfg); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%v ms: got %v, want %v", c.ms, got, c.want)
		}
	}
}

func TestDefaultRewardWeightsSumToOne(t *testing.T) {
	cfg := DefaultRewardConfig()
	if sum := cfg.LatencyWeight + cfg.ErrorWeight + cfg.ResourceWeight; math.Abs(sum-1) > 1e-9 {
		t.Fatalf("weights sum to %v", sum)
	}
}

func TestComputeRewardCPUAboveThresholdReducesReward(t *testing.T) {
	cfg := DefaultRewardConfig()
	if low, high := ComputeReward(50, 200, 0.5, cfg), ComputeReward(50, 200, 0.95, cfg); high >= low {
		t.Fatalf("cpu 0.95 reward %v not below cpu 0.5 reward %v", high, low)
	}
}

// TestComputeRewardProperties checks invariants over random inputs: the reward
// stays in [0,1], any 5xx gives 0, and higher latency never raises the reward.
func TestComputeRewardProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	def := DefaultRewardConfig()
	for i := 0; i < 5000; i++ {
		cfg := def
		cfg.SLATargetMs = 1 + rng.Float64()*999
		cfg.LatencyDecayLambda = 0.0001 + rng.Float64()
		ms, cpu, status := rng.Float64()*100_000, rng.Float64(), 100+rng.Intn(500)
		if r := ComputeReward(ms, status, cpu, cfg); r < 0 || r > 1 || math.IsNaN(r) {
			t.Fatalf("reward %v out of [0,1] for ms=%v status=%d cpu=%v cfg=%+v", r, ms, status, cpu, cfg)
		}
		if r := ComputeReward(ms, 500+rng.Intn(100), cpu, def); r != 0 {
			t.Fatalf("5xx reward = %v, want 0", r)
		}
		a, b := rng.Float64()*10_000, rng.Float64()*10_000
		lo, hi := math.Min(a, b), math.Max(a, b)
		if ComputeReward(hi, 200, cpu, def) > ComputeReward(lo, 200, cpu, def)+1e-12 {
			t.Fatalf("reward rose with latency: %v ms vs %v ms", lo, hi)
		}
	}
}
