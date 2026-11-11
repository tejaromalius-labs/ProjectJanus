//! The d=12 context vector the simulator feeds the engine. Mirrors
//! `buildContext` in `linucb-proxy/internal/proxy/context.go`.

use nalgebra::DVector;

/// Raw pod + request signals used to build the normalized context vector.
/// Every field is expected to already be in its documented range.
#[derive(Debug, Clone, Copy)]
pub struct RawFeatures {
    pub cpu_utilization: f64,     // [0,1] EMA over 5s
    pub memory_saturation: f64,   // [0,1] used/allocatable
    pub active_connections: f64,  // [0,1] normalized by max_connections
    pub request_queue_depth: f64, // [0,1] normalized by queue capacity
    pub gc_pause_rate: f64,       // [0,1] JVM-relevant
    pub payload_log_norm: f64,    // [0,1] log(bytes)/log(max_bytes)
    pub is_mutation: bool,        // POST/PUT/PATCH/DELETE
    pub hour_of_day: f64,         // [0,24)
    pub day_of_week: f64,         // [0,7)
    pub p99_latency_ema: f64,     // [0,1] normalized by SLA target
}

/// Build the baseline d=12 context vector with sinusoidal temporal encoding.
/// Index order matches the Go proxy's buildContext.
pub fn build_context(f: &RawFeatures) -> DVector<f64> {
    use std::f64::consts::PI;
    let hour_angle = 2.0 * PI * f.hour_of_day / 24.0;
    let dow_angle = 2.0 * PI * f.day_of_week / 7.0;

    DVector::from_vec(vec![
        f.cpu_utilization,                     // 0
        f.memory_saturation,                   // 1
        f.active_connections,                  // 2
        f.request_queue_depth,                 // 3
        f.gc_pause_rate,                       // 4
        f.payload_log_norm,                    // 5
        if f.is_mutation { 1.0 } else { 0.0 }, // 6
        hour_angle.sin(),                      // 7
        hour_angle.cos(),                      // 8
        dow_angle.sin(),                       // 9
        dow_angle.cos(),                       // 10
        f.p99_latency_ema,                     // 11
    ])
}
