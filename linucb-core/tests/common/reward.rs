//! Reward function used by the simulator. Mirrors `ComputeReward` in
//! `linucb-proxy/internal/proxy/reward.go`, which is the production version
//! and holds the reward tests.
//!
//! Raw millisecond latency must never be fed into the model: latency
//! distributions are heavy-tailed and unnormalized inputs destabilize ridge
//! regression. Every component here is bounded to [0,1] and the composite
//! reward is provably in [0,1].

/// Tunables for the reward function, mirroring the CRD `rewardDefinition`.
/// Weights must sum to 1.0.
#[derive(Debug, Clone, Copy)]
pub struct RewardConfig {
    pub latency_weight: f64,       // 0.70
    pub error_weight: f64,         // 0.20
    pub resource_weight: f64,      // 0.10
    pub sla_target_ms: f64,        // e.g. 100
    pub latency_decay_lambda: f64, // e.g. 0.01
}

impl Default for RewardConfig {
    fn default() -> Self {
        Self {
            latency_weight: 0.70,
            error_weight: 0.20,
            resource_weight: 0.10,
            sla_target_ms: 100.0,
            latency_decay_lambda: 0.01,
        }
    }
}

/// Compute the normalized composite reward in [0,1].
///
/// - `observed_ms`: measured upstream latency.
/// - `http_status`: response status; >= 500 is a hard error.
/// - `cpu_pct`: pod CPU utilization in [0,1].
pub fn compute_reward(observed_ms: f64, http_status: u16, cpu_pct: f64, cfg: &RewardConfig) -> f64 {
    // Step 1: latency score. Linear from 1.0 at 0 ms to 0.5 at the SLA target,
    // then exponential decay from 0.5 (continuous at the target).
    let latency_score = if observed_ms <= cfg.sla_target_ms {
        1.0 - 0.5 * observed_ms.max(0.0) / cfg.sla_target_ms
    } else {
        0.5 * (-cfg.latency_decay_lambda * (observed_ms - cfg.sla_target_ms)).exp()
    };

    // Step 2: binary error gate.
    let error_ok = if http_status < 500 { 1.0 } else { 0.0 };

    // Step 3: CPU headroom score, linear penalty from 0.85→1.0 CPU.
    let cpu_score = (1.0 - (cpu_pct - 0.85).max(0.0) / 0.15).clamp(0.0, 1.0);

    // Step 4: composite. Server errors (>= 500) collapse the entire reward to 0.0.
    let r = if error_ok == 0.0 {
        0.0
    } else {
        cfg.latency_weight * latency_score
            + cfg.error_weight * error_ok
            + cfg.resource_weight * cpu_score
    };

    debug_assert!((0.0..=1.0).contains(&r), "reward out of [0,1]: {r}");
    r
}
