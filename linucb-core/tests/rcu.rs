//! ArmSlot: RCU publication, reset and concurrent readers.

use linucb_core::features::D;
use linucb_core::rcu::ArmSlot;
use nalgebra::DVector;
use std::sync::atomic::Ordering::{Acquire, Release};
use std::sync::Arc;
use std::thread;

fn unit_context(i: usize) -> DVector<f64> {
    let mut x = DVector::zeros(D);
    x[i] = 1.0;
    x
}

#[test]
fn update_increments_observations() {
    let slot = ArmSlot::new("pod-x");
    assert_eq!(slot.n_obs(), 0);
    slot.update(&unit_context(0), 1.0, 1.0, 0);
    assert_eq!(slot.n_obs(), 1);
    slot.update(&unit_context(1), 0.5, 1.0, 0);
    assert_eq!(slot.n_obs(), 2);
}

#[test]
fn reset_reinstalls_cold_start() {
    let slot = ArmSlot::new("pod-x");
    slot.update(&unit_context(0), 1.0, 1.0, 0);
    slot.update(&unit_context(1), 0.8, 1.0, 0);
    assert_eq!(slot.n_obs(), 2);

    slot.reset("pod-y");
    assert_eq!(slot.n_obs(), 0, "reset must clear observation count");
    // Slot is still usable and scores a finite cold-start value.
    let score = slot.score(&unit_context(0), 0.15, 1.0, 0);
    assert!(score.is_finite());
    // And it can learn again after reset.
    slot.update(&unit_context(0), 0.5, 1.0, 0);
    assert_eq!(slot.n_obs(), 1);
}

#[test]
fn concurrent_readers_survive_interleaved_update_and_reset() {
    // One writer interleaving update/reset, many readers. Readers must never
    // panic and never observe a non-finite (torn / freed) score — this is the
    // epoch-GC-under-concurrent-readers guarantee for reset.
    let slot = Arc::new(ArmSlot::new("pod-x"));
    let stop = Arc::new(std::sync::atomic::AtomicBool::new(false));

    let mut readers = Vec::new();
    for _ in 0..4 {
        let s = Arc::clone(&slot);
        let stop = Arc::clone(&stop);
        readers.push(thread::spawn(move || {
            let x = unit_context(0);
            while !stop.load(Acquire) {
                let score = s.score(&x, 0.15, 1.0, 0);
                assert!(score.is_finite());
            }
        }));
    }

    let writer = {
        let s = Arc::clone(&slot);
        thread::spawn(move || {
            for t in 0..2000u64 {
                s.update(&unit_context((t as usize) % D), 0.5, 1.0, 0);
                // Periodically reset mid-stream to race readers against reclamation.
                if t % 200 == 0 {
                    s.reset("pod-x");
                }
            }
        })
    };

    writer.join().unwrap();
    stop.store(true, Release);
    for r in readers {
        r.join().unwrap();
    }
}

#[test]
fn concurrent_readers_and_serialized_writer_stay_consistent() {
    // One writer thread, many reader threads. Readers must never panic,
    // never see a non-finite score (torn read would corrupt the math).
    let slot = Arc::new(ArmSlot::new("pod-x"));
    let stop = Arc::new(std::sync::atomic::AtomicBool::new(false));

    let mut readers = Vec::new();
    for _ in 0..4 {
        let s = Arc::clone(&slot);
        let stop = Arc::clone(&stop);
        readers.push(thread::spawn(move || {
            let x = unit_context(0);
            while !stop.load(Acquire) {
                let score = s.score(&x, 0.15, 1.0, 0);
                assert!(score.is_finite());
            }
        }));
    }

    let writer = {
        let s = Arc::clone(&slot);
        thread::spawn(move || {
            for t in 0..2000u64 {
                s.update(&unit_context((t as usize) % D), 0.5, 1.0, 0);
            }
        })
    };

    writer.join().unwrap();
    stop.store(true, Release);
    for r in readers {
        r.join().unwrap();
    }
    assert_eq!(slot.n_obs(), 2000);
}
