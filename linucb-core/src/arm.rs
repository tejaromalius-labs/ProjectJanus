//! Per-arm LinUCB state and the O(d²) Sherman-Morrison update.

use crate::features::D;
use nalgebra::{DMatrix, DVector};

/// Largest idle catch-up factor γ^(−idle) applied on an update. It bounds how
/// far A⁻¹ can grow in directions the contexts never span (the windup the
/// prior-restoring term guards against); 100 is ≈4,600 idle updates at γ=0.999.
pub const MAX_IDLE_SCALE: f64 = 100.0;

/// Immutable per-arm model state. Updates produce a *new* `ArmState` so the
/// value can be published via an atomic pointer swap (see `rcu::ArmSlot`) —
/// readers always observe a consistent (A_inv, b) pair.
#[derive(Debug, Clone)]
pub struct ArmState {
    /// A⁻¹, the d×d inverse covariance. Stored directly and updated via
    /// Sherman-Morrison — A is never formed or reinverted.
    pub a_inv: DMatrix<f64>,
    /// b, the reward-weighted context accumulator.
    pub b: DVector<f64>,
    pub n_obs: u64,
    pub pod_id: String,
}

impl ArmState {
    /// Cold-start state. A_inv is the identity (NOT zeros) — a unit-variance
    /// i.i.d. prior; zeros would be singular and the first Sherman-Morrison
    /// update would divide by zero.
    pub fn new(pod_id: &str) -> Self {
        Self {
            a_inv: DMatrix::identity(D, D),
            b: DVector::zeros(D),
            n_obs: 0,
            pod_id: pod_id.to_string(),
        }
    }

    /// Apply one observation via the Sherman-Morrison rank-one update — O(d²).
    /// Returns a new immutable state; never mutates in place.
    ///
    /// A_new⁻¹ = A⁻¹ − (v vᵀ) / (1 + xᵀ v),  where v = A⁻¹ x
    pub fn update(&self, x: &DVector<f64>, reward: f64) -> ArmState {
        self.update_with_gamma(x, reward, 1.0)
    }

    /// Discounted update with forgetting factor gamma ∈ (0, 1].
    ///
    /// Models A_new = γ·A + x·xᵀ,  b_new = γ·b + r·x.
    ///
    /// The exact Sherman-Morrison inverse update is:
    ///   v       = A⁻¹ x
    ///   A_new⁻¹ = (1/γ) · (A⁻¹ − v·vᵀ / (γ + xᵀv))
    ///
    /// Effective memory window ≈ 1/(1−γ) observations.
    /// γ=1.0 is the standard non-discounted update.
    ///
    /// With γ < 1, discounting also forgets the prior, so A decays toward zero in
    /// any direction the contexts never span and A⁻¹ grows as γ⁻ⁿ. Each update
    /// therefore adds c·e_k·e_kᵀ with c = d·(1−γ), cycling k = n_obs mod d: on
    /// average (1−γ)·I per update, which holds A near the unit prior in unused
    /// directions. This is one more rank-one Sherman-Morrison step.
    pub fn update_with_gamma(&self, x: &DVector<f64>, reward: f64, gamma: f64) -> ArmState {
        debug_assert!(gamma > 0.0 && gamma <= 1.0, "gamma must be in (0,1]");
        let d = x.len();
        let v = &self.a_inv * x;
        let denom = gamma + x.dot(&v);

        // If denom ≤ 0, A⁻¹ has lost positive definiteness due to accumulated
        // floating-point error from repeated (1/γ) scaling. Reset to identity
        // (cold-start prior) so the arm re-learns rather than staying broken.
        if denom <= 1e-10 {
            let b_new = gamma * &self.b + reward * x;
            return ArmState {
                a_inv: DMatrix::identity(d, d),
                b: b_new,
                n_obs: self.n_obs + 1,
                pod_id: self.pod_id.clone(),
            };
        }

        let mut a_inv_new = (1.0 / gamma) * (&self.a_inv - (&v * v.transpose()) / denom);
        if gamma < 1.0 {
            let k = (self.n_obs % d as u64) as usize;
            let c = d as f64 * (1.0 - gamma);
            let col = a_inv_new.column(k).into_owned();
            a_inv_new -= (c / (1.0 + c * col[k])) * (&col * col.transpose());
        }
        let b_new = gamma * &self.b + reward * x;

        ArmState {
            a_inv: a_inv_new,
            b: b_new,
            n_obs: self.n_obs + 1,
            pod_id: self.pod_id.clone(),
        }
    }

    /// Discounted update for an arm that was not updated during the last `idle`
    /// engine updates. Discounted LinUCB discounts every arm on every update,
    /// which scales A and b by γ^idle while the arm goes unchosen: θ = A⁻¹b is
    /// unchanged (as `ucb_score_idle` assumes) and the next observation weighs
    /// γ^(−idle) more. Applying γ only once would leave a recovered pod's stale
    /// estimate as heavy as if it had never been idle. The factor is capped at
    /// `MAX_IDLE_SCALE`; idle = 0 is exactly `update_with_gamma`.
    pub fn update_after_idle(
        &self,
        x: &DVector<f64>,
        reward: f64,
        gamma: f64,
        idle: u64,
    ) -> ArmState {
        let scale = if gamma < 1.0 && idle > 0 {
            (idle as f64 * -gamma.ln()).exp().min(MAX_IDLE_SCALE)
        } else {
            1.0
        };
        if scale == 1.0 {
            return self.update_with_gamma(x, reward, gamma);
        }
        ArmState {
            a_inv: scale * &self.a_inv,
            b: &self.b / scale,
            n_obs: self.n_obs,
            pod_id: self.pod_id.clone(),
        }
        .update_with_gamma(x, reward, gamma)
    }

    /// Upper Confidence Bound score — O(d²) for the variance term, runs on the
    /// hot path.
    ///
    /// score = xᵀθ + α·√(xᵀ A⁻¹ x),  where θ = A⁻¹ b
    pub fn ucb_score(&self, x: &DVector<f64>, alpha: f64) -> f64 {
        self.ucb_score_idle(x, alpha, 1.0, 0)
    }

    /// UCB score for an arm that was not updated during the last `idle` engine
    /// updates. Discounting every arm on every update, as discounted LinUCB
    /// does, would scale both A and b by γ^idle: θ = A⁻¹b is unchanged and the
    /// width grows by γ^(−idle/2). Without this an unchosen arm's estimate and
    /// width stay frozen, so it can be excluded forever. The width is capped at
    /// 1 (the reward range), enough for an idle arm to outscore any arm whose
    /// estimate is at most 1 higher.
    pub fn ucb_score_idle(&self, x: &DVector<f64>, alpha: f64, gamma: f64, idle: u64) -> f64 {
        let theta = &self.a_inv * &self.b;
        let exploit = x.dot(&theta);
        let variance = x.dot(&(&self.a_inv * x));
        // variance is mathematically >= 0; clamp guards tiny negative float error.
        let width = alpha * variance.max(0.0).sqrt();
        let growth = if width > 0.0 {
            (idle as f64 * -gamma.ln() / 2.0)
                .exp()
                .min(1.0 / width.min(1.0))
        } else {
            1.0
        };
        exploit + width * growth
    }
}
