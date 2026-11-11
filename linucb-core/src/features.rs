//! Feature-dimension constants. The Go proxy builds the context vector
//! (`linucb-proxy/internal/proxy/context.go`) and passes it across the C ABI.

/// Feature dimension. Change here only. Sherman-Morrison update cost is O(d²);
/// it is capped at d ≤ 20.
pub const D: usize = 12;

/// Hard dimension cap. Asserted at
/// compile time so a careless bump of `D` fails the build rather than silently
/// blowing the 1.5ms routing budget.
pub const MAX_D: usize = 20;
const _: () = assert!(D <= MAX_D, "feature dimension D exceeds the hard cap MAX_D");
