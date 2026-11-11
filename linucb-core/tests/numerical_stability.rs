//! Sherman-Morrison numerical stability, including the discounted update the
//! proxy runs with (gamma = 0.999).

use linucb_core::arm::ArmState;
use linucb_core::features::D;
use nalgebra::{DMatrix, DVector};
use rand::rngs::StdRng;
use rand::{Rng, SeedableRng};

/// The proxy's default forgetting factor (`-gamma`).
const PROD_GAMMA: f64 = 0.999;
const ALPHA: f64 = 0.15;

/// Reconstruct A from the observations and check A_inv · A ≈ I over 100k
/// updates with contexts that vary in every direction. A₀ = I and
/// A = γ·A + x·xᵀ, plus the prior refresh c·e_k·e_kᵀ (c = d·(1−γ),
/// k = update index mod d) when γ < 1.
#[test]
fn inverse_stays_accurate_over_100k_updates() {
    for gamma in [1.0, PROD_GAMMA] {
        let mut rng = StdRng::seed_from_u64(123);
        let mut arm = ArmState::new("pod");
        let mut a = DMatrix::<f64>::identity(D, D);

        for t in 1..=100_000 {
            let x = DVector::from_fn(D, |_, _| rng.gen_range(-1.0..1.0));
            arm = arm.update_with_gamma(&x, rng.gen_range(0.0..1.0), gamma);
            a = gamma * a + &x * x.transpose();
            if gamma < 1.0 {
                let k = (t - 1) % D;
                a[(k, k)] += D as f64 * (1.0 - gamma);
            }

            if t % 5_000 == 0 {
                let max_err = (&arm.a_inv * &a - DMatrix::<f64>::identity(D, D))
                    .abs()
                    .max();
                assert!(
                    max_err < 1e-6,
                    "gamma={gamma} t={t}: max|A_inv*A - I| = {max_err:e} (want < 1e-6)"
                );
            }
        }
    }
}

/// Context shaped like the proxy's: resource and request features vary, while
/// the hour and weekday encodings (indices 7-10) stay fixed within an hour.
fn context(rng: &mut StdRng, hour: u32, weekday: u32) -> DVector<f64> {
    let h = 2.0 * std::f64::consts::PI * hour as f64 / 24.0;
    let w = 2.0 * std::f64::consts::PI * weekday as f64 / 7.0;
    DVector::from_fn(D, |i, _| match i {
        7 => h.sin(),
        8 => h.cos(),
        9 => w.sin(),
        10 => w.cos(),
        6 => f64::from(u8::from(rng.gen_bool(0.1))),
        _ => rng.gen_range(0.0..1.0),
    })
}

/// 30k updates at gamma = 0.999 within one hour (about two minutes for one
/// backend at 250 req/s), then score a request in the same hour and in the next.
/// Rewards are in [0,1], so a healthy UCB score stays small and finite.
#[test]
fn production_gamma_stays_bounded_when_the_hour_changes() {
    let mut rng = StdRng::seed_from_u64(7);
    let mut arm = ArmState::new("pod");
    for _ in 0..30_000 {
        let x = context(&mut rng, 10, 2);
        arm = arm.update_with_gamma(&x, rng.gen_range(0.6..0.9), PROD_GAMMA);
    }

    let max_entry = arm.a_inv.abs().max();
    assert!(
        arm.a_inv.iter().all(|v| v.is_finite()),
        "A_inv has non-finite entries"
    );

    let same_hour = arm.ucb_score(&context(&mut rng, 10, 2), ALPHA);
    let next_hour = arm.ucb_score(&context(&mut rng, 11, 2), ALPHA);
    assert!(
        same_hour.is_finite() && same_hour < 2.0,
        "same-hour UCB score = {same_hour:e} (max |A_inv| = {max_entry:e})"
    );
    assert!(
        next_hour.is_finite() && next_hour < 2.0,
        "next-hour UCB score = {next_hour:e} (max |A_inv| = {max_entry:e})"
    );
}

/// The same workload, but as one of four arms with occasional long idle gaps
/// (a pod routed around during a fault): the γ^(−idle) catch-up must not wind
/// A⁻¹ up in directions the contexts never span.
#[test]
fn idle_catch_up_stays_bounded() {
    let mut rng = StdRng::seed_from_u64(11);
    let mut arm = ArmState::new("pod");
    for i in 0..30_000 {
        let x = context(&mut rng, 10, 2);
        let idle = if i % 1_000 == 999 {
            20_000
        } else {
            rng.gen_range(0..6)
        };
        arm = arm.update_after_idle(&x, rng.gen_range(0.6..0.9), PROD_GAMMA, idle);
    }

    let max_entry = arm.a_inv.abs().max();
    assert!(
        arm.a_inv.iter().all(|v| v.is_finite()),
        "A_inv has non-finite entries"
    );
    let score = arm.ucb_score(&context(&mut rng, 11, 2), ALPHA);
    assert!(
        score.is_finite() && score < 2.0,
        "UCB score = {score:e} (max |A_inv| = {max_entry:e})"
    );
}

#[test]
fn ucb_scores_finite_over_10k_requests() {
    for gamma in [1.0, PROD_GAMMA] {
        let mut rng = StdRng::seed_from_u64(99);
        let mut arm = ArmState::new("pod");
        for _ in 0..10_000 {
            let x = DVector::from_fn(D, |_, _| rng.gen_range(-1.0..1.0));
            let s = arm.ucb_score(&x, ALPHA);
            assert!(s.is_finite(), "gamma={gamma}: non-finite UCB score: {s}");
            arm = arm.update_with_gamma(&x, rng.gen_range(0.0..1.0), gamma);
        }
    }
}
