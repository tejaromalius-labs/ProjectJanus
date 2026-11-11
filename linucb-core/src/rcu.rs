//! Lock-free RCU state management for per-arm model state.
//!
//! The hot path (routing) reads an immutable `ArmState` through a single atomic
//! pointer load; background workers publish updates by swapping in a freshly
//! built state and deferring reclamation of the old one via epoch GC. This
//! guarantees readers never observe a torn (A_inv, b) pair and
//! never block writers.

use crate::arm::ArmState;
use crossbeam_epoch::{self as epoch, Atomic, Owned};
use nalgebra::DVector;
use std::sync::atomic::Ordering::{Acquire, Release};

/// A single arm's atomically-swappable state cell.
pub struct ArmSlot {
    current: Atomic<ArmState>,
}

impl ArmSlot {
    pub fn new(pod_id: &str) -> Self {
        Self {
            current: Atomic::new(ArmState::new(pod_id)),
        }
    }

    /// HOT PATH — score this arm with zero locks. Acquire load guarantees we
    /// observe a fully-initialized `ArmState` published by `update`. `idle` is
    /// the number of engine updates since this arm's last one
    /// (see `ArmState::ucb_score_idle`).
    pub fn score(&self, x: &DVector<f64>, alpha: f64, gamma: f64, idle: u64) -> f64 {
        let guard = &epoch::pin();
        // Safety: the pointer is non-null (constructed with Atomic::new) and is
        // kept alive for the guard's lifetime by epoch protection.
        let state = unsafe { self.current.load(Acquire, guard).deref() };
        state.ucb_score_idle(x, alpha, gamma, idle)
    }

    /// Current observation count for this arm (lifecycle promotion).
    pub fn n_obs(&self) -> u64 {
        let guard = &epoch::pin();
        let state = unsafe { self.current.load(Acquire, guard).deref() };
        state.n_obs
    }

    /// BACKGROUND WORKER — serialized per arm. Builds a new state by copying the
    /// current one, applies the Sherman-Morrison update, and atomically swaps it
    /// in. The old state is reclaimed only once no reader can hold it.
    ///
    /// Callers MUST serialize concurrent `update` calls to the same slot (the
    /// design runs one worker goroutine/thread per arm); writes to different
    /// arms are fully independent.
    ///
    /// `idle` is the number of engine updates since this arm's last one
    /// (see `ArmState::update_after_idle`).
    pub fn update(&self, x: &DVector<f64>, reward: f64, gamma: f64, idle: u64) {
        let guard = &epoch::pin();
        let old_shared = self.current.load(Acquire, guard);
        let old = unsafe { old_shared.deref() };

        let new_state = old.update_after_idle(x, reward, gamma, idle);
        let new = Owned::new(new_state);

        let prev = self.current.swap(new, Release, guard);
        // Defer drop until no thread holds a reference (NEVER free immediately —
        // readers acquired before the swap may still be using it).
        unsafe {
            guard.defer_destroy(prev);
        }
    }

    /// Reclaim this arm's learned state and reinstall a cold-start prior, keeping
    /// the slot — and therefore its `arm_id` index — allocated and valid. Used
    /// when a pod is removed: the heavy `ArmState` (d×d A_inv + b) is freed via
    /// epoch GC without shifting any later arm index.
    ///
    /// Uses the identical `swap(Release)` + `defer_destroy` protocol as `update`,
    /// so concurrent hot-path `score`/`n_obs` readers never observe a freed
    /// pointer — the old state is reclaimed only once no reader can hold it.
    ///
    /// Callers SHOULD serialize `reset` against `update` on the same slot under
    /// the single-writer-per-arm discipline. If a late `update` races a `reset`
    /// (e.g. an in-flight reward arriving after pod removal), both swaps are
    /// individually atomic so no state is torn; the only effect is the slot may
    /// be momentarily re-dirtied. That is harmless because removed indices are
    /// never reused by a different pod.
    pub fn reset(&self, pod_id: &str) {
        let guard = &epoch::pin();
        let new = Owned::new(ArmState::new(pod_id));
        let prev = self.current.swap(new, Release, guard);
        unsafe {
            guard.defer_destroy(prev);
        }
    }
}

impl Drop for ArmSlot {
    fn drop(&mut self) {
        // Reclaim the live state. Safe: &mut self means no concurrent access.
        let guard = &epoch::pin();
        let shared = self.current.swap(Owned::new(ArmState::new("")), Release, guard);
        unsafe {
            guard.defer_destroy(shared);
        }
    }
}
