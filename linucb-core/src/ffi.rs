//! C ABI for the Go proxy (CGO). Opaque-handle pattern.
//!
//! Footguns guarded here:
//!   1. Panics must NOT unwind across the FFI boundary (UB) — every entry point
//!      wraps its body in `catch_unwind`.
//!   2. `&slice[0]` panics on an empty slice — length is validated before deref.
//!   3. Ownership: Rust frees Rust-allocated memory (`engine_free`); the caller
//!      must never `free()` the handle from C.

use crate::rcu::ArmSlot;
use nalgebra::DVector;
use std::os::raw::{c_char, c_double, c_int};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::slice;
use std::sync::atomic::{AtomicU64, Ordering::Relaxed};

/// Opaque engine handle. Owns one `ArmSlot` per arm, indexed by `arm_id`.
pub struct Engine {
    arms: Vec<ArmSlot>,
    /// Engine-wide update count, and its value at each arm's last update. The
    /// difference is how long an arm has gone unchosen.
    rounds: AtomicU64,
    last_round: Vec<AtomicU64>,
    dim: usize,
    alpha: f64,
    /// Forgetting factor γ ∈ (0,1]. 1.0 = no forgetting; 0.999 ≈ 1000-obs window.
    gamma: f64,
}

/// Create a new engine for feature dimension `dim` and exploration weight
/// `alpha`. Returns a heap-allocated handle, or null on invalid input/panic.
/// The caller owns the handle and must release it with `linucb_engine_free`.
///
/// # Safety
/// The returned pointer must only be passed back to this crate's functions.
#[no_mangle]
pub extern "C" fn linucb_engine_new(dim: usize, alpha: c_double) -> *mut Engine {
    linucb_engine_new_with_gamma(dim, alpha, 1.0)
}

/// Like `linucb_engine_new` but with an explicit forgetting factor gamma ∈ (0,1].
/// gamma=1.0 is identical to `linucb_engine_new`. gamma=0.999 gives an effective
/// memory window of ~1000 observations, letting the model adapt to environment changes.
#[no_mangle]
pub extern "C" fn linucb_engine_new_with_gamma(
    dim: usize,
    alpha: c_double,
    gamma: c_double,
) -> *mut Engine {
    let result = catch_unwind(|| {
        // gamma < 0.99 causes (1/γ)^n overflow after ~O(1000) updates — reject.
        if dim == 0
            || dim > crate::features::MAX_D
            || !alpha.is_finite()
            || alpha < 0.0
            || !gamma.is_finite()
            || gamma < 0.99
            || gamma > 1.0
        {
            return std::ptr::null_mut();
        }
        Box::into_raw(Box::new(Engine {
            arms: Vec::new(),
            rounds: AtomicU64::new(0),
            last_round: Vec::new(),
            dim,
            alpha,
            gamma,
        }))
    });
    result.unwrap_or(std::ptr::null_mut())
}

/// Free an engine created by `linucb_engine_new`. Null is a no-op.
///
/// # Safety
/// `engine` must be a pointer returned by `linucb_engine_new` and not freed
/// before. After this call the pointer is dangling and must not be reused.
#[no_mangle]
pub unsafe extern "C" fn linucb_engine_free(engine: *mut Engine) {
    if engine.is_null() {
        return;
    }
    let _ = catch_unwind(AssertUnwindSafe(|| {
        drop(Box::from_raw(engine));
    }));
}

/// Register a new arm (pod). Returns its `arm_id` (>= 0) or -1 on error.
///
/// # Safety
/// `engine` must be valid; `pod_id` must be a valid NUL-terminated C string.
#[no_mangle]
pub unsafe extern "C" fn linucb_add_arm(engine: *mut Engine, pod_id: *const c_char) -> c_int {
    let result = catch_unwind(AssertUnwindSafe(|| {
        let engine = match engine.as_mut() {
            Some(e) => e,
            None => return -1,
        };
        let id = if pod_id.is_null() {
            String::new()
        } else {
            std::ffi::CStr::from_ptr(pod_id)
                .to_string_lossy()
                .into_owned()
        };
        engine.arms.push(ArmSlot::new(&id));
        engine
            .last_round
            .push(AtomicU64::new(engine.rounds.load(Relaxed)));
        (engine.arms.len() - 1) as c_int
    }));
    result.unwrap_or(-1)
}

/// Reset `arm_id` to cold-start state, reclaiming its learned model (the d×d
/// A_inv matrix + b vector). The slot — and therefore its `arm_id` — stays
/// valid: this NEVER shifts other arm indices, so cached arm IDs on the caller
/// side remain correct. Used when a pod is removed (the engine grows to peak
/// concurrent arms and reset-in-place reclaims memory without index reuse).
/// Returns 0 on success, -1 on error (bad handle or unknown arm).
/// Caller must serialize this against updates to the same arm.
///
/// # Safety
/// `engine` must be valid; `pod_id` may be null (treated as empty) or a valid
/// NUL-terminated C string.
#[no_mangle]
pub unsafe extern "C" fn linucb_reset_arm(
    engine: *const Engine,
    arm_id: c_int,
    pod_id: *const c_char,
) -> c_int {
    let result = catch_unwind(AssertUnwindSafe(|| {
        let engine = match engine.as_ref() {
            Some(e) => e,
            None => return -1,
        };
        let id = if pod_id.is_null() {
            String::new()
        } else {
            std::ffi::CStr::from_ptr(pod_id)
                .to_string_lossy()
                .into_owned()
        };
        match arm_index(engine, arm_id) {
            Some(i) => {
                engine.arms[i].reset(&id);
                engine.last_round[i].store(engine.rounds.load(Relaxed), Relaxed);
                0
            }
            None => -1,
        }
    }));
    result.unwrap_or(-1)
}

/// Score `arm_id` against context vector `x` (length `len`). Returns the UCB
/// score, or NaN on error (bad handle, dimension mismatch, panic).
///
/// # Safety
/// `engine` must be valid; `x` must point to `len` readable `f64`s.
#[no_mangle]
pub unsafe extern "C" fn linucb_score(
    engine: *const Engine,
    arm_id: c_int,
    x: *const c_double,
    len: usize,
) -> c_double {
    let result = catch_unwind(AssertUnwindSafe(|| {
        let engine = match engine.as_ref() {
            Some(e) => e,
            None => return f64::NAN,
        };
        let xv = match read_context(engine, x, len) {
            Some(v) => v,
            None => return f64::NAN,
        };
        match arm_index(engine, arm_id) {
            Some(i) => {
                let idle = engine
                    .rounds
                    .load(Relaxed)
                    .saturating_sub(engine.last_round[i].load(Relaxed));
                engine.arms[i].score(&xv, engine.alpha, engine.gamma, idle)
            }
            None => f64::NAN,
        }
    }));
    result.unwrap_or(f64::NAN)
}

/// Apply a reward observation to `arm_id` (Sherman-Morrison update). Returns 0
/// on success, -1 on error. Caller must serialize updates to the same arm.
///
/// # Safety
/// `engine` must be valid; `x` must point to `len` readable `f64`s.
#[no_mangle]
pub unsafe extern "C" fn linucb_update(
    engine: *const Engine,
    arm_id: c_int,
    x: *const c_double,
    len: usize,
    reward: c_double,
) -> c_int {
    let result = catch_unwind(AssertUnwindSafe(|| {
        let engine = match engine.as_ref() {
            Some(e) => e,
            None => return -1,
        };
        if !reward.is_finite() {
            return -1;
        }
        let xv = match read_context(engine, x, len) {
            Some(v) => v,
            None => return -1,
        };
        match arm_index(engine, arm_id) {
            Some(i) => {
                let idle = engine
                    .rounds
                    .load(Relaxed)
                    .saturating_sub(engine.last_round[i].load(Relaxed));
                engine.arms[i].update(&xv, reward, engine.gamma, idle);
                let round = engine.rounds.fetch_add(1, Relaxed) + 1;
                engine.last_round[i].store(round, Relaxed);
                0
            }
            None => -1,
        }
    }));
    result.unwrap_or(-1)
}

/// Observation count for `arm_id`, or 0 on error.
///
/// # Safety
/// `engine` must be valid.
#[no_mangle]
pub unsafe extern "C" fn linucb_n_obs(engine: *const Engine, arm_id: c_int) -> u64 {
    let result = catch_unwind(AssertUnwindSafe(|| {
        let engine = match engine.as_ref() {
            Some(e) => e,
            None => return 0,
        };
        match arm_index(engine, arm_id) {
            Some(i) => engine.arms[i].n_obs(),
            None => 0,
        }
    }));
    result.unwrap_or(0)
}

// --- helpers (not exported) ---

unsafe fn read_context(engine: &Engine, x: *const c_double, len: usize) -> Option<DVector<f64>> {
    // Footgun #2: guard empty/null before any deref.
    if x.is_null() || len == 0 || len != engine.dim {
        return None;
    }
    let s = slice::from_raw_parts(x, len);
    if s.iter().any(|v| !v.is_finite()) {
        return None;
    }
    Some(DVector::from_column_slice(s))
}

fn arm_index(engine: &Engine, arm_id: c_int) -> Option<usize> {
    if arm_id < 0 {
        return None;
    }
    let i = arm_id as usize;
    if i < engine.arms.len() {
        Some(i)
    } else {
        None
    }
}
