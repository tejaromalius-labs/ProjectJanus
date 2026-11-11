package proxy

import "math"

// RewardConfig holds the reward parameters.
// Weights must sum to 1.0.
type RewardConfig struct {
	LatencyWeight      float64
	ErrorWeight        float64
	ResourceWeight     float64
	SLATargetMs        float64
	LatencyDecayLambda float64
}

// DefaultRewardConfig returns the defaults.
func DefaultRewardConfig() RewardConfig {
	return RewardConfig{
		LatencyWeight:      0.70,
		ErrorWeight:        0.20,
		ResourceWeight:     0.10,
		SLATargetMs:        100,
		LatencyDecayLambda: 0.01,
	}
}

// ComputeReward returns the normalized composite reward in [0,1].
// This is the production reward; linucb-core/tests/common/reward.rs is a copy
// for the engine's simulator and should be kept in step with it.
func ComputeReward(observedMs float64, httpStatus int, cpuPct float64, cfg RewardConfig) float64 {
	if httpStatus >= 500 {
		return 0.0
	}

	// Linear from 1.0 at 0 ms to 0.5 at the SLA target, then exponential decay
	// from 0.5 (continuous at the target).
	var latencyScore float64
	if observedMs <= cfg.SLATargetMs {
		latencyScore = 1.0 - 0.5*math.Max(0, observedMs)/cfg.SLATargetMs
	} else {
		latencyScore = 0.5 * math.Exp(-cfg.LatencyDecayLambda*(observedMs-cfg.SLATargetMs))
	}

	errorOK := 1.0
	cpuScore := math.Max(0, math.Min(1, 1.0-math.Max(0, cpuPct-0.85)/0.15))

	r := cfg.LatencyWeight*latencyScore*errorOK +
		cfg.ErrorWeight*errorOK +
		cfg.ResourceWeight*cpuScore

	// Clamp defensively against float drift; the formula is provably in [0,1].
	return math.Max(0, math.Min(1, r))
}
