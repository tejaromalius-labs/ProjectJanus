//! after 500 requests the degraded pod must
//! receive < 10% of traffic, and the cold-start pod must receive none while it
//! is still WARMING.

mod common;

use common::sim::{simulate, simulate_error_fault, POD_DEGRADED, POD_HEALTHY, POD_NEW};

#[test]
fn degraded_pod_starved_within_500_requests() {
    // Multiple seeds to guard against a lucky/unlucky RNG draw.
    for seed in [1u64, 7, 42, 100, 2024] {
        let stats = simulate(500, 0.15, seed);
        let degraded = stats.traffic_fraction(POD_DEGRADED);
        assert!(
            degraded < 0.10,
            "seed {seed}: degraded pod got {:.1}% of traffic (want < 10%)",
            degraded * 100.0
        );
        // The healthy pod should dominate.
        assert!(
            stats.traffic_fraction(POD_HEALTHY) > 0.80,
            "seed {seed}: healthy pod only got {:.1}%",
            stats.traffic_fraction(POD_HEALTHY) * 100.0
        );
    }
}

#[test]
fn degraded_pod_with_503_fault_starved_within_500_requests() {
    // JANUS-02 verification: 10 randomized seeds under 1-in-5 503 error injection.
    for seed in [1u64, 7, 42, 100, 2024, 11, 22, 33, 44, 55] {
        let stats = simulate_error_fault(500, 0.15, seed, 5);
        let degraded = stats.traffic_fraction(POD_DEGRADED);
        assert!(
            degraded < 0.10,
            "seed {seed}: degraded pod with 503 fault got {:.1}% of traffic (want < 10%)",
            degraded * 100.0
        );
        assert!(
            stats.traffic_fraction(POD_HEALTHY) > 0.80,
            "seed {seed}: healthy pod only got {:.1}%",
            stats.traffic_fraction(POD_HEALTHY) * 100.0
        );
    }
}

#[test]
fn warming_pod_excluded() {
    let stats = simulate(500, 0.15, 7);
    assert_eq!(stats.routed[POD_NEW], 0);
}

#[test]
fn all_scores_finite() {
    let stats = simulate(10_000, 0.15, 1);
    assert!(!stats.any_nonfinite_score);
}
