# 13. Reference

**What you'll find here:** a map of the source files, and a glossary of every term used
in these docs. Use it when you meet a word you don't recognise.

---

## Where do I look for…?

| I want to change or understand… | Start in |
| --- | --- |
| The score formula and the update | `linucb-core/src/arm.rs` |
| Lock-free reads and writes | `linucb-core/src/rcu.rs` |
| The C interface Go calls | `linucb-core/src/ffi.rs`, `linucb-core/include/linucb.h` |
| How a request is routed | `linucb-proxy/internal/proxy/proxy.go` (`choose`, `linucbBest`) |
| The reward | `linucb-proxy/internal/proxy/reward.go` (and its copy in `linucb-core/tests/common/reward.rs`) |
| The 12 features | `linucb-proxy/internal/proxy/context.go` |
| Pod states and promotion | `linucb-proxy/internal/registry/lifecycle.go` |
| Kubernetes discovery | `linucb-proxy/internal/registry/discovery_k8s.go` |
| Learning from responses | `linucb-proxy/internal/telemetry/collector.go`, `worker.go` |
| The latency feature and Prometheus inputs | `linucb-proxy/internal/featurestore/` |
| The circuit breaker | `linucb-proxy/internal/circuitbreaker/cb.go` |
| The benchmark | `benchmark/` (see [page 12](12-testing-and-benchmarking.md)) |

## File reference

| File | Key concepts |
|------|-------------|
| `linucb-core/src/arm.rs` | ArmState, Sherman-Morrison, UCB score, nalgebra, ridge regression |
| `linucb-core/src/rcu.rs` | RCU, crossbeam-epoch, Atomic, Owned, Acquire/Release |
| `linucb-core/src/features.rs` | Dimension constants D and MAX_D, compile-time assert |
| `linucb-core/src/ffi.rs` | C ABI, #[no_mangle], catch_unwind, opaque Engine struct handle, Box::into_raw |
| `linucb-core/build.rs` | cbindgen, build script, rerun triggers |
| `linucb-core/include/linucb.h` | Generated C header, D constant, FFI function signatures |
| `linucb-core/Cargo.toml` | nalgebra, crossbeam-epoch; dev: rand; cdylib+staticlib |
| `linucb-core/tests/common/` | Simulator (`sim.rs`), its context vector and reward copies |
| `linucb-core/tests/convergence.rs` | Simulator convergence criterion over fixed seeds |
| `linucb-core/tests/numerical_stability.rs` | `|A_inv*A - I| < 1e-6`, Sherman-Morrison accuracy |
| `linucb-core/tests/arm.rs`, `rcu.rs`, `ffi.rs` | Engine unit-level tests through the public API |
| `linucb-proxy/cmd/proxy/main.go` | Main entrypoint, flag parsing, goroutine wiring, signal handling, slog |
| `linucb-proxy/internal/linucb/binding.go` | CGO, #cgo LDFLAGS, unsafe.Pointer, runtime.SetFinalizer, NaN error |
| `linucb-proxy/internal/proxy/proxy.go` | httputil.ReverseProxy, Rewrite, timingTransport, routing safety chain, atomic counters |
| `linucb-proxy/internal/proxy/context.go` | RequestInfo, BuildContextVector, sinusoidal encoding (Go), log normalization (Go) |
| `linucb-proxy/internal/proxy/reward.go` | ComputeReward (Go), RewardConfig, parity with Rust |
| `linucb-proxy/internal/registry/registry.go` | PodEntry, PodState, sync.Map, atomic.Int32, math.Float64bits |
| `linucb-proxy/internal/registry/lifecycle.go` | Promotion loop, WARMING→SHADOW→ACTIVE, latency graduation gate |
| `linucb-proxy/internal/registry/discovery.go` | Discovery interface |
| `linucb-proxy/internal/registry/discovery_mock.go` | MockDiscovery, PodEvent |
| `linucb-proxy/internal/registry/discovery_k8s.go` | K8s EndpointSlice informer, pod add/remove events (build tag: k8s) |
| `linucb-proxy/internal/registry/discovery_k8s_stub.go` | Stub for non-k8s builds (build tag: !k8s) |
| `linucb-proxy/internal/registry/discovery_k8s_test.go` | Tests using fake K8s client |
| `linucb-proxy/internal/featurestore/store.go` | sync.Map, async refresh, Source interface, stale tolerance |
| `linucb-proxy/internal/featurestore/source_mock.go` | MockSource |
| `linucb-proxy/internal/fallback/leastconn.go` | LeastConnections, sync.Mutex, Acquire/Release, in-flight tracking |
| `linucb-proxy/internal/circuitbreaker/cb.go` | Closed/Open/HalfOpen, atomic.Int32, CAS, injectable clock |
| `linucb-proxy/internal/telemetry/collector.go` | Non-blocking channel send, backpressure, reward computation |
| `linucb-proxy/internal/telemetry/worker.go` | Single-writer serialization, RecordObservation, context reconstruction |
| `linucb-proxy/internal/telemetry/ebpf_ebpf.go` | eBPF, tracepoint, ringbuf, CO-RE, bpf2go, CAP_BPF, rlimit (build tag: ebpf) |
| `linucb-proxy/internal/telemetry/ebpf_stub.go` | Build tag stub, ErrEBPFDisabled (build tag: !ebpf) |
| `linucb-proxy/internal/bpf/gen.go` | go:generate, bpf2go directive |
| `linucb-proxy/internal/bpf/socket_latency.c` | eBPF C program, inet_sock_set_state tracepoint, BPF ringbuf |
| `helm-chart/Chart.yaml` | Helm chart metadata, apiVersion v2 |
| `helm-chart/values.yaml` | Default values, configuration surface |
| `helm-chart/templates/deployment.yaml` | Container spec, resource limits, probes |
| `helm-chart/templates/service.yaml` | ClusterIP service |
| `helm-chart/templates/rbac.yaml` | ClusterRole, ClusterRoleBinding, least privilege |
| `helm-chart/templates/serviceaccount.yaml` | ServiceAccount |
| `Dockerfile` | Multi-stage build: Rust core + Go proxy (with -tags k8s) |
| `.dockerignore` | Excludes build artifacts and dev files from Docker context |
| `docs/` | This documentation |

## Glossary

**A⁻¹ (Inverse Covariance Matrix):** d×d matrix maintained per arm. Encodes how much the model knows about each context direction. Large A⁻¹ = high uncertainty (few observations). Small A⁻¹ = high confidence. Never computed by direct inversion — maintained via Sherman-Morrison.

**Alpha (α, Exploration Weight):** Parameter controlling exploration-exploitation balance. Higher α → wider confidence intervals → more exploration. Default 0.15. Set via `-alpha` flag.

**Arm:** LinUCB term for a choice (here: a backend pod). Each arm has learned parameters (A⁻¹, b, θ). Arm ID is the positional index into the engine's arm vector.

**b (Reward Accumulator):** d-dimensional vector per arm. Accumulates reward-weighted context vectors: b = Σ rᵢxᵢ. Together with A⁻¹, determines θ = A⁻¹b.

**Bandit Algorithm:** Decision-making algorithm that balances exploration (trying new options) and exploitation (using known good options). "Multi-armed bandit" refers to multiple choices.

**cbindgen:** Rust tool that generates C header files from `#[no_mangle] extern "C"` Rust functions, enabling CGO to call them.

**CGO:** Go's facility for calling C code, using special comments in Go source files.

**Circuit Breaker:** Concurrency pattern that detects repeated failures and stops routing traffic through the failing path. Three states: Closed → Open → HalfOpen.

**CO-RE (Compile Once, Run Everywhere):** eBPF technique using BTF (BPF Type Format) to relocate kernel data structure field accesses at load time, allowing a single eBPF binary to run across kernel versions.

**Cold Start:** The initial period when a new arm (pod) has no observations and LinUCB has no learned parameters for it. Mitigated by scoring every pod until one is ACTIVE, and by minimum exploration (arms with fewer than 20 observations are routed first).

**Context (x):** d-dimensional feature vector describing the current state. Indices 0-10: pod features. Index 11: normalized rolling p99 latency. Used to predict which arm is best.

**Contextual Bandit:** Bandit algorithm where the arm choice depends on context (unlike standard bandits, which ignore context).

**crossbeam-epoch:** Rust library providing epoch-based garbage collection for RCU (Read-Copy-Update) patterns. Allows safe deferred destruction of objects that may still be referenced by concurrent readers.

**Discovery:** Process of finding available backend pods. Kubernetes: EndpointSlice informer. Local: hardcoded mock pods.

**eBPF (extended Berkeley Packet Filter):** Linux kernel technology for running sandboxed programs in kernel space without modifying kernel source code. Verified by the kernel for safety and JIT-compiled to native code.

**EndpointSlice:** Kubernetes API object (`discovery.k8s.io/v1`) that lists IP addresses and ports of pods in a Service. Shards up to 100 endpoints per slice, more efficient than the older Endpoints API.

**EMA (Exponential Moving Average):** Running weighted average where recent values are weighted more heavily. `EMA_t = α·xₜ + (1−α)·EMA_{t-1}`. O(1) to update.

**Epoch-Based GC:** Memory reclamation strategy where deallocation is deferred until all threads that could have seen the pointer have passed through an epoch boundary.

**Exploitation:** Selecting the arm predicted to be best based on learned parameters (xᵀθ term in UCB).

**Exploration:** Selecting uncertain arms to gather more information (α·√(xᵀA⁻¹x) term in UCB).

**Forgetting Factor (γ, Gamma):** Parameter in (0, 1] controlling how fast the model forgets old observations. Lower = forget faster. Default 0.999 ≈ 1000-observation effective window. Set via `-gamma` flag.

**Informer:** Kubernetes client-go pattern for efficiently watching API objects. Maintains a local cache backed by a long-polling watch connection. Callbacks fire on Add/Update/Delete events.

**LinUCB:** Linear Upper Confidence Bound algorithm. Uses linear regression (θ·x) with analytically exact uncertainty quantification (A⁻¹). O(d²) per update. The Rust core implements this.

**nalgebra:** Rust linear algebra library. Provides `DMatrix<f64>` (dynamically-sized dense matrix) and `DVector<f64>` (dynamically-sized column vector) with SIMD-optimized operations.

**Observation:** One (context, reward) training example. Updates the model via Sherman-Morrison.

**Opaque Handle Pattern:** The C caller receives a pointer to an opaque struct (no fields visible), hiding the implementation while providing a stable API.

**RCU (Read-Copy-Update):** Concurrency pattern enabling lock-free reads. Writes create a new copy and atomically swap in the pointer. Old copy is garbage collected after all readers finish.

**Reward:** Scalar feedback in [0, 1] computed from latency (70%), HTTP status (20%), and CPU (10%). Positive feedback trains the model to prefer high-reward arms.

**Score:** Predicted expected reward for an arm given context. LinUCB score = xᵀθ + α·√(xᵀA⁻¹x).

**Sherman-Morrison Update:** Efficient rank-1 update formula for A⁻¹. O(d²) instead of O(d³) direct inversion. Core of the LinUCB learning algorithm.

**Static Library (.a):** Library linked into the executable at compile time. The resulting binary contains all Rust code with no external runtime dependencies.

**θ (Theta, Weight Vector):** d-dimensional learned parameter vector per arm. θ = A⁻¹b. Represents how reward is predicted from context. Computed on demand during scoring.

**WARMING/SHADOW/ACTIVE/DRAINING:** Pod lifecycle states regulating traffic ramp-up. Prevents cold-start poor decisions from reaching production traffic.

**ScoreDelta (0.05):** the score gap that the router treats as one "step". Gaps under one step are noise; each further step roughly doubles a pod's expected load.

**Dead zone:** the range of score gaps below `ScoreDelta`, where pods are considered equal and compete only on fewest requests in flight.

**p99:** the latency that 99% of requests beat. The slowest 1% of requests are slower than this.

**Gate:** a pass/fail check. Pod promotion has an observation-count gate and a latency gate; the benchmark has latency, failure and throughput gates.

**Readmission time:** in the recovery benchmark, how long after a fault ends until the recovered pod receives at least 20% of requests (the fair share is 25%).

**Shadow traffic:** the roughly 5% of requests deliberately sent to SHADOW pods, so they have a stream to be judged on.

---

[← Previous: Testing and benchmarking](12-testing-and-benchmarking.md) · [Documentation index](README.md) · [Known issues and risks →](known-issues.md)
