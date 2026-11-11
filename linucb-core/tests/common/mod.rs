//! Simulation support for the integration tests. Not part of the engine: the
//! Go proxy builds the context vector and computes rewards at runtime.
#![allow(dead_code)]

pub mod features;
pub mod reward;
pub mod sim;
