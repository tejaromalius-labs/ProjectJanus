//! Project Janus — LinUCB contextual-bandit core engine.
//!
//! This crate implements the performance-critical math hot path of the
//! intelligent load balancer: per-arm ridge-regression state with
//! O(d²) Sherman-Morrison updates, lock-free RCU
//! state management, and a C ABI for the Go proxy to call via CGO.

pub mod arm;
pub mod features;
pub mod ffi;
pub mod rcu;

pub use arm::ArmState;
pub use features::{D, MAX_D};
pub use rcu::ArmSlot;
