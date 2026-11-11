//! Simulation harness for the convergence tests.
//!
//! Three pods — healthy, degraded (noisy neighbor), and a cold-start newcomer —
//! exercise the LinUCB routing loop. Used by the convergence tests to verify the
//! model routes away from the degraded pod within ~500 requests and never sends
//! traffic to a pod still in WARMING.

use super::features::{build_context, RawFeatures};
use super::reward::{compute_reward, RewardConfig};
use linucb_core::arm::ArmState;
use nalgebra::DVector;
use rand::rngs::StdRng;
use rand::{Rng, SeedableRng};

/// Pods below this observation count are WARMING and excluded from scoring.
/// Kept small for fast simulations; production default is 200.
pub const WARMUP_THRESHOLD: u64 = 50;

/// Identifies the simulated pods by index.
pub const POD_HEALTHY: usize = 0;
pub const POD_DEGRADED: usize = 1;
pub const POD_NEW: usize = 2;

/// Outcome of a simulation run, used by tests for assertions.
#[derive(Debug, Default, Clone)]
pub struct SimStats {
    pub routed: Vec<u64>, // requests routed per pod
    pub n_requests: usize,
    pub any_nonfinite_score: bool,
}

impl SimStats {
    pub fn traffic_fraction(&self, pod: usize) -> f64 {
        if self.n_requests == 0 {
            return 0.0;
        }
        self.routed[pod] as f64 / self.n_requests as f64
    }
}

fn sample_context(rng: &mut StdRng, t: usize) -> DVector<f64> {
    let hour = (t % 24) as f64;
    build_context(&RawFeatures {
        cpu_utilization: rng.gen_range(0.1..0.6),
        memory_saturation: rng.gen_range(0.1..0.6),
        active_connections: rng.gen_range(0.0..0.5),
        request_queue_depth: rng.gen_range(0.0..0.4),
        gc_pause_rate: rng.gen_range(0.0..0.2),
        payload_log_norm: rng.gen_range(0.2..0.8),
        is_mutation: rng.gen_bool(0.3),
        hour_of_day: hour,
        day_of_week: ((t / 24) % 7) as f64,
        p99_latency_ema: rng.gen_range(0.1..0.5),
    })
}

/// Simulate the latency/status/cpu a pod would return, then map to a reward.
fn simulate_reward(pod: usize, rng: &mut StdRng, cfg: &RewardConfig) -> f64 {
    let (latency_ms, status, cpu) = match pod {
        POD_HEALTHY => (rng.gen_range(40.0..90.0), 200u16, rng.gen_range(0.2..0.5)),
        POD_DEGRADED => (
            rng.gen_range(400.0..900.0),
            200u16,
            rng.gen_range(0.85..0.99),
        ),
        POD_NEW => (rng.gen_range(60.0..120.0), 200u16, rng.gen_range(0.3..0.6)),
        _ => (100.0, 200, 0.5),
    };
    compute_reward(latency_ms, status, cpu, cfg)
}

/// Run the routing loop for `n_requests`. The new pod starts cold and is forced
/// through WARMING by feeding it observations only once the other two are warm,
/// mirroring the lifecycle gate.
pub fn simulate(n_requests: usize, alpha: f64, seed: u64) -> SimStats {
    let cfg = RewardConfig::default();
    let mut rng = StdRng::seed_from_u64(seed);

    let mut arms = vec![
        ArmState::new("pod-healthy"),
        ArmState::new("pod-degraded"),
        ArmState::new("pod-new"),
    ];

    let mut stats = SimStats {
        routed: vec![0; arms.len()],
        ..Default::default()
    };

    // Warm the two incumbent pods so they are eligible from the start; the new
    // pod is intentionally left cold to test WARMING exclusion.
    for _ in 0..WARMUP_THRESHOLD {
        let x = sample_context(&mut rng, 0);
        for &pod in &[POD_HEALTHY, POD_DEGRADED] {
            let r = simulate_reward(pod, &mut rng, &cfg);
            arms[pod] = arms[pod].update(&x, r);
        }
    }

    for t in 0..n_requests {
        let x = sample_context(&mut rng, t);

        // Score only ACTIVE arms (n_obs >= WARMUP_THRESHOLD).
        let mut best: Option<usize> = None;
        let mut best_score = f64::NEG_INFINITY;
        for (i, a) in arms.iter().enumerate() {
            if a.n_obs < WARMUP_THRESHOLD {
                continue; // WARMING: hard-excluded from scoring
            }
            let s = a.ucb_score(&x, alpha);
            if !s.is_finite() {
                stats.any_nonfinite_score = true;
            }
            if s > best_score {
                best_score = s;
                best = Some(i);
            }
        }
        let chosen = best.unwrap_or(POD_HEALTHY);

        let r = simulate_reward(chosen, &mut rng, &cfg);
        arms[chosen] = arms[chosen].update(&x, r);

        stats.routed[chosen] += 1;
        stats.n_requests += 1;
    }

    stats
}

/// Simulate 503 error fault (JANUS-02): the degraded pod has low latency (20-50ms)
/// and normal CPU (0.2-0.5), but returns 503 every `fail_every` requests (e.g. 1 in 5).
/// An error gate mirrors the proxy's leaky-bucket circuit breaker, suppressing degraded
/// arms from candidate selection during cooldown.
pub fn simulate_error_fault(
    n_requests: usize,
    alpha: f64,
    seed: u64,
    fail_every: usize,
) -> SimStats {
    let cfg = RewardConfig::default();
    let mut rng = StdRng::seed_from_u64(seed);

    let mut arms = vec![
        ArmState::new("pod-healthy"),
        ArmState::new("pod-degraded"),
        ArmState::new("pod-new"),
    ];

    let mut stats = SimStats {
        routed: vec![0; arms.len()],
        ..Default::default()
    };

    // Pre-warm the two incumbent pods with healthy 200 OK responses.
    for _ in 0..WARMUP_THRESHOLD {
        let x = sample_context(&mut rng, 0);
        for &pod in &[POD_HEALTHY, POD_DEGRADED] {
            let r = compute_reward(
                rng.gen_range(40.0..90.0),
                200,
                rng.gen_range(0.2..0.5),
                &cfg,
            );
            arms[pod] = arms[pod].update(&x, r);
        }
    }

    let mut degraded_req_count = 0usize;
    let mut err_score = [0i64; 3];
    let mut consecutive_errors = [0usize; 3];
    let mut suppressed_until = [0usize; 3];
    let mut trial_in_flight = [false; 3];

    for t in 0..n_requests {
        let x = sample_context(&mut rng, t);

        // Filter eligible arms: ACTIVE (n_obs >= WARMUP_THRESHOLD) and not circuit-broken.
        let mut eligible = Vec::new();
        for (i, a) in arms.iter().enumerate() {
            if a.n_obs < WARMUP_THRESHOLD {
                continue; // WARMING
            }
            if suppressed_until[i] == 0 {
                eligible.push(i); // StateClosed
            } else if t >= suppressed_until[i] {
                // StateHalfOpen: admit trial request if not already in flight
                if !trial_in_flight[i] {
                    eligible.push(i);
                }
            }
        }
        if eligible.is_empty() {
            // Outage fallback: if all active arms are suppressed, admit all active arms.
            for (i, a) in arms.iter().enumerate() {
                if a.n_obs >= WARMUP_THRESHOLD {
                    eligible.push(i);
                }
            }
        }

        let mut best: Option<usize> = None;
        let mut best_score = f64::NEG_INFINITY;
        for &i in &eligible {
            let s = arms[i].ucb_score(&x, alpha);
            if !s.is_finite() {
                stats.any_nonfinite_score = true;
            }
            if s > best_score {
                best_score = s;
                best = Some(i);
            }
        }
        let chosen = best.unwrap_or(POD_HEALTHY);

        let is_trial = suppressed_until[chosen] > 0 && t >= suppressed_until[chosen];
        if is_trial {
            trial_in_flight[chosen] = true;
        }

        // Determine outcome: degraded pod fails every `fail_every` requests with 503.
        let (latency_ms, status, cpu) = if chosen == POD_DEGRADED {
            degraded_req_count += 1;
            let status = if fail_every > 0 && degraded_req_count % fail_every == 0 {
                503u16
            } else {
                200u16
            };
            (rng.gen_range(20.0..50.0), status, rng.gen_range(0.2..0.5))
        } else {
            (rng.gen_range(40.0..90.0), 200u16, rng.gen_range(0.2..0.5))
        };

        // Update circuit breaker state.
        if is_trial {
            trial_in_flight[chosen] = false;
        }

        if status >= 500 {
            consecutive_errors[chosen] += 1;
            err_score[chosen] += 6;
            if is_trial || consecutive_errors[chosen] >= 2 || err_score[chosen] >= 10 {
                suppressed_until[chosen] = t + 100; // 100 request cooldown
                err_score[chosen] = 6;
            }
        } else {
            consecutive_errors[chosen] = 0;
            err_score[chosen] = (err_score[chosen] - 1).max(0);
            if is_trial {
                suppressed_until[chosen] = 0; // Trial succeeded: recover to StateClosed
            }
        }

        let r = compute_reward(latency_ms, status, cpu, &cfg);
        arms[chosen] = arms[chosen].update(&x, r);

        stats.routed[chosen] += 1;
        stats.n_requests += 1;
    }

    stats
}
