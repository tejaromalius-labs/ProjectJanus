//! ArmState: cold start and the Sherman-Morrison update.

use linucb_core::arm::{ArmState, MAX_IDLE_SCALE};
use linucb_core::features::D;
use nalgebra::{DMatrix, DVector};

fn unit_context(i: usize) -> DVector<f64> {
    let mut x = DVector::zeros(D);
    x[i] = 1.0;
    x
}

#[test]
fn cold_start_is_identity() {
    let a = ArmState::new("pod-x");
    assert_eq!(a.a_inv, DMatrix::identity(D, D));
    assert_eq!(a.b, DVector::zeros(D));
    assert_eq!(a.n_obs, 0);
}

#[test]
fn first_update_does_not_divide_by_zero() {
    let a = ArmState::new("pod-x");
    let x = unit_context(0);
    let b = a.update(&x, 1.0);
    assert_eq!(b.n_obs, 1);
    assert!(b.a_inv.iter().all(|v| v.is_finite()));
}

#[test]
fn sherman_morrison_matches_direct_inverse() {
    // After one update with context e0, A = I + e0 e0ᵀ, so A⁻¹ should be
    // I with the (0,0) entry = 1/2.
    let a = ArmState::new("pod-x");
    let x = unit_context(0);
    let updated = a.update(&x, 0.0);
    assert!((updated.a_inv[(0, 0)] - 0.5).abs() < 1e-12);
    for i in 1..D {
        assert!((updated.a_inv[(i, i)] - 1.0).abs() < 1e-12);
    }
}

#[test]
fn reward_pushes_theta_toward_context() {
    let a = ArmState::new("pod-x");
    let x = unit_context(0);
    let updated = a.update(&x, 1.0);
    let theta = &updated.a_inv * &updated.b;
    assert!(theta[0] > 0.0);
}

#[test]
fn idle_growth_widens_bonus_without_moving_the_estimate() {
    let mut a = ArmState::new("pod-x");
    let x = DVector::from_element(D, 0.1);
    for _ in 0..200 {
        a = a.update_with_gamma(&x, 0.5, 0.999);
    }
    let exploit = x.dot(&(&a.a_inv * &a.b));
    let bonus = |idle| a.ucb_score_idle(&x, 0.15, 0.999, idle) - exploit;

    assert_eq!(a.ucb_score_idle(&x, 0.15, 0.999, 0), a.ucb_score(&x, 0.15));
    assert_eq!(
        a.ucb_score_idle(&x, 0.15, 1.0, 1_000_000),
        a.ucb_score(&x, 0.15)
    );
    let ratio = bonus(1_000) / bonus(0);
    assert!(
        (ratio - 0.999_f64.powf(-500.0)).abs() < 1e-9,
        "ratio {ratio}"
    );
    // Capped at the reward range, even when γ^(−idle/2) overflows.
    assert!((bonus(u64::MAX) - 1.0).abs() < 1e-12);
}

fn trained(reward: f64) -> (ArmState, DVector<f64>) {
    let mut a = ArmState::new("pod-x");
    let x = DVector::from_element(D, 0.1);
    for _ in 0..2_000 {
        a = a.update_with_gamma(&x, reward, 0.999);
    }
    (a, x)
}

#[test]
fn no_idle_update_is_the_plain_discounted_update() {
    let (a, x) = trained(0.4);
    let plain = a.update_with_gamma(&x, 1.0, 0.999);
    let idle0 = a.update_after_idle(&x, 1.0, 0.999, 0);
    assert_eq!(plain.a_inv, idle0.a_inv);
    assert_eq!(plain.b, idle0.b);
    let undiscounted = a.update_after_idle(&x, 1.0, 1.0, 5_000);
    assert_eq!(a.update(&x, 1.0).a_inv, undiscounted.a_inv);
}

/// After an idle gap, a fresh observation weighs γ^(−idle) more, so a pod that
/// recovered while unchosen corrects its stale low estimate faster.
#[test]
fn fresh_reward_after_idle_moves_the_estimate_more() {
    let (a, x) = trained(0.4);
    let estimate = |s: &ArmState| x.dot(&(&s.a_inv * &s.b));
    let before = estimate(&a);
    let plain = estimate(&a.update_after_idle(&x, 1.0, 0.999, 0)) - before;
    let idle = estimate(&a.update_after_idle(&x, 1.0, 0.999, 2_000)) - before;
    assert!(
        plain > 0.0 && idle > 5.0 * plain,
        "plain {plain}, idle {idle}"
    );
}

#[test]
fn idle_catch_up_is_capped() {
    let (a, x) = trained(0.4);
    let at_cap = (MAX_IDLE_SCALE.ln() / -(0.999f64).ln()).ceil() as u64;
    let capped = a.update_after_idle(&x, 1.0, 0.999, at_cap);
    let far = a.update_after_idle(&x, 1.0, 0.999, 1_000_000);
    assert_eq!(capped.a_inv, far.a_inv);
    assert_eq!(capped.b, far.b);
}
