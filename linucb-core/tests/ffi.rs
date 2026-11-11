//! C ABI: lifecycle, scoring, reset and input validation.

use linucb_core::ffi::*;
use std::ffi::CString;

#[test]
fn engine_lifecycle_and_scoring() {
    let eng = linucb_engine_new(linucb_core::features::D, 0.15);
    assert!(!eng.is_null());

    let pod = CString::new("pod-a").unwrap();
    let arm = unsafe { linucb_add_arm(eng, pod.as_ptr()) };
    assert_eq!(arm, 0);

    let x = vec![0.1_f64; linucb_core::features::D];
    let score = unsafe { linucb_score(eng, arm, x.as_ptr(), x.len()) };
    assert!(score.is_finite());

    let rc = unsafe { linucb_update(eng, arm, x.as_ptr(), x.len(), 0.8) };
    assert_eq!(rc, 0);
    assert_eq!(unsafe { linucb_n_obs(eng, arm) }, 1);

    unsafe { linucb_engine_free(eng) };
}

#[test]
fn reset_arm_clears_state() {
    let eng = linucb_engine_new(linucb_core::features::D, 0.15);
    let pod = CString::new("pod-a").unwrap();
    let arm = unsafe { linucb_add_arm(eng, pod.as_ptr()) };

    let x = vec![0.1_f64; linucb_core::features::D];
    unsafe { linucb_update(eng, arm, x.as_ptr(), x.len(), 0.8) };
    unsafe { linucb_update(eng, arm, x.as_ptr(), x.len(), 0.8) };
    assert_eq!(unsafe { linucb_n_obs(eng, arm) }, 2);

    // Reset with a new pod id; the arm index stays valid and observations clear.
    let pod_b = CString::new("pod-b").unwrap();
    let rc = unsafe { linucb_reset_arm(eng, arm, pod_b.as_ptr()) };
    assert_eq!(rc, 0);
    assert_eq!(unsafe { linucb_n_obs(eng, arm) }, 0);
    // Still scorable after reset (slot remains allocated).
    let score = unsafe { linucb_score(eng, arm, x.as_ptr(), x.len()) };
    assert!(score.is_finite());

    unsafe { linucb_engine_free(eng) };
}

#[test]
fn reset_arm_rejects_bad_inputs() {
    let eng = linucb_engine_new(linucb_core::features::D, 0.15);
    let pod = CString::new("pod-a").unwrap();
    // Unknown arm -> -1.
    assert_eq!(unsafe { linucb_reset_arm(eng, 99, pod.as_ptr()) }, -1);
    // Null engine -> -1, not a crash.
    assert_eq!(
        unsafe { linucb_reset_arm(std::ptr::null(), 0, pod.as_ptr()) },
        -1
    );
    // Null pod_id is accepted (treated as empty) on a valid arm.
    let arm = unsafe { linucb_add_arm(eng, pod.as_ptr()) };
    assert_eq!(unsafe { linucb_reset_arm(eng, arm, std::ptr::null()) }, 0);
    unsafe { linucb_engine_free(eng) };
}

#[test]
fn rejects_bad_inputs() {
    assert!(linucb_engine_new(0, 0.15).is_null());
    assert!(linucb_engine_new(linucb_core::features::MAX_D + 1, 0.15).is_null());

    let eng = linucb_engine_new(linucb_core::features::D, 0.15);
    // dimension mismatch -> NaN
    let short = vec![0.1_f64; 3];
    let s = unsafe { linucb_score(eng, 0, short.as_ptr(), short.len()) };
    assert!(s.is_nan());
    // unknown arm -> NaN
    let x = vec![0.1_f64; linucb_core::features::D];
    let s2 = unsafe { linucb_score(eng, 99, x.as_ptr(), x.len()) };
    assert!(s2.is_nan());
    unsafe { linucb_engine_free(eng) };

    // null engine is a no-op / error, not a crash
    unsafe { linucb_engine_free(std::ptr::null_mut()) };
}

#[test]
fn unchosen_arm_regains_score_while_others_are_updated() {
    let eng = linucb_engine_new_with_gamma(linucb_core::features::D, 0.15, 0.999);
    let (a, b) = unsafe {
        (
            linucb_add_arm(eng, CString::new("a").unwrap().as_ptr()),
            linucb_add_arm(eng, CString::new("b").unwrap().as_ptr()),
        )
    };
    let x = vec![0.1_f64; linucb_core::features::D];
    let score = |arm| unsafe { linucb_score(eng, arm, x.as_ptr(), x.len()) };
    for _ in 0..200 {
        unsafe { linucb_update(eng, a, x.as_ptr(), x.len(), 0.9) };
        unsafe { linucb_update(eng, b, x.as_ptr(), x.len(), 0.2) };
    }
    assert!(score(b) < score(a) - 0.5);

    // Only `a` is chosen from now on; `b` must eventually outscore it.
    let mut rounds = 0;
    while score(b) <= score(a) {
        unsafe { linucb_update(eng, a, x.as_ptr(), x.len(), 0.9) };
        rounds += 1;
        assert!(rounds < 20_000, "idle arm never caught up");
    }
    println!("idle arm caught up after {rounds} updates of the other arm");
    unsafe { linucb_engine_free(eng) };
}
