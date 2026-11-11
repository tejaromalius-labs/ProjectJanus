# Known issues and risks

This page lists what is still wrong or unproven, in the order we would worry about
it. Nothing here is hidden elsewhere: if you are deciding whether to rely on Janus,
read this page next to the [benchmark results](../README.md#does-it-work).

## What to worry about first

1. **Evidence, not code.** The benchmark favours Janus's design (see the limits in
   the README), and the recovery fix has been tested once, after being written
   with knowledge of the first results.
2. **Reproducibility.** The recovery study used a binary built from the working tree on
   2026-11-11, and the benchmark runs themselves were not repeated.
3. **Production hazards.** Unsynchronised arm growth, unauthenticated admin
   endpoints on the client port, no TLS and no retries. These are listed below
   and none of them is exercised by the benchmark.

## Open issues

- Recovery after faults (see [history part 2](history/04-frozen-campaign-and-recovery.md)): fixed in the 2026-11-11 exploratory run, but not
  confirmed. It needs a protocol with a decision rule fixed in advance and a rerun. The latency gate covers only the latency and CPU fault
  phases, so the errors-fault p99 (about 9.5 ms vs 4 ms) is ungated.
- Latency fault readmission: Janus takes a median 2.25 s to give a recovered
  pod 20% of traffic; RR, LC and P2C take 1.0 s.
- The benchmark compares Janus, which has circuit breakers, with baselines that
  have none, on evenly spaced arrivals at 600 RPS under tight CPU quotas. No
  breaker-disabled Janus, Poisson or bursty arrivals, or looser-quota variants
  have been run.
- Why healthy pods' scores spread over 0.1–0.8: endogenous feedback loops.
  The context vector features ($x[0]$ CPU, $x[11]$ rolling latency) and reward
  penalties depend on metrics that the proxy's own routing actions degrade,
  violating the exogenous context assumption and creating artificial score spreads.
- Concurrency hazard in Rust FFI: `linucb_add_arm` appends to `Engine.arms` via
  `Vec::push` from the K8s discovery goroutine without synchronization against
  hot-path reads in `linucb_score`. Vector reallocation during concurrent requests
  risks data races and undefined behavior.
- Monotonic arm memory leak on pod churn: `AddArm` appends to `engine.arms`, but
  `RemovePod` only resets state in place to preserve positional indexing. Over
  rolling updates or HPA scaling, arm slots leak monotonically.
- Multi-replica state fragmentation: each proxy pod maintains an isolated,
  in-memory LinUCB model with zero cross-instance synchronization. Scaling or
  rolling restarts erase learned state. A single replica avoids fragmentation but
  creates an unmitigated single point of failure (SPOF).
- Telemetry lock contention and single-worker bottleneck: on every request,
  `Collector.updateLatencyFeature` acquires a global mutex and runs `sort.Float64s`
  over a 256-sample window. Updates funnel into a single `UpdateWorker` goroutine,
  causing observation drops when burst traffic exceeds the 4,096-item buffer.
- Prometheus telemetry lag and feature starvation: `PrometheusSource.Scrape` runs
  5 sequential HTTP queries with 15–60 s scrape/rate lag, too slow to track
  sub-second spikes. Disabling Prometheus (the default) leaves $x[0..4]$ permanently
  zero, reducing the model to a 1D latency tracker.
- Admin path collision: internal endpoints (`/stats`, `/model`, `/readyz`,
  `/healthz`, `/metrics`) are served unauthenticated on the client-facing proxy
  port (`:8080`), leaking internal model state and hijacking matching upstream paths.
- Missing L7 capabilities: no TLS/mTLS termination, no request retries/hedging
  on upstream 5xx or connection drops, and standard reverse proxy transport.
- Lifecycle state machine contradictions: requiring 200 warmup and 1,000 shadow
  observations (within 20% of fleet median p99) risks deadlocks during rolling
  restarts, while `Proxy.choose` routes to warming pods anyway via score.
- With γ = 0.999 an estimate needs ~693 updates to move halfway to a new mean;
  the 3 s recovery window gives a pod ~450, so recovery is limited by design.
- ~~γ windup~~ fixed 2026-11-09: each discounted update restores the prior
  along one axis (d·(1−γ)·e_k·e_kᵀ). Before the fix, 30k updates in one hour
  gave max |A⁻¹| ≈ 1e13 and UCB scores of 19 (same hour) and 1.3e5 (next hour).
- The Rust simulator keeps a copy of the reward that must match Go by hand.

## Risk register (design risks and how each was handled)

| Risk | Root Cause | Consequence | Mitigation | Status |
|------|-----------|-------------|------------|--------|
| **Matrix inversion O(d³)** | Naively inverting Aₐ on each reward | Decision latency blooms exponentially | Sherman-Morrison O(d²) incremental update. Store A⁻¹ directly. Hard cap d ≤ 20. | **Fixed** |
| **Reward instability (heavy tails)** | Raw ms latency fed into bₐ | θ estimates diverge, model degrades | Normalized reward with exponential latency decay, all inputs clipped to [0,1] | **Fixed** |
| **RCU torn read** | A⁻¹ and b from different time points | Silent model corruption, wrong routing | Epoch-based RCU: single atomic pointer to immutable ArmState struct. Defer free via crossbeam-epoch | **Fixed** |
| **Cold-start SLA violation** | LinUCB explores new pods aggressively | Real traffic hits un-warmed pods | Score-weighted cost after 20 exploration requests, circuit breakers; lifecycle gates promotion only. Hard exclusion of WARMING pods was removed (it starved late pods) | **Mitigated** |
| **Feature dimension creep** | Arbitrary feature addition | O(d²) update cost silently blows budget | Compile-time assertion `D ≤ MAX_D` | **Fixed** |
| **Metric fetch latency** | Synchronous Prometheus queries on hot path | 50ms+ network round trips per request | Async background refresh loop with local shared-memory cache. Hot path reads in-process | **Fixed** |
| **Model inertia** | Standard LinUCB forgets nothing | Cannot adapt to changing pod performance | Discounted LinUCB with configurable γ (forgetting factor). Default γ=0.999 ≈ 1000-obs window | **Fixed** |
| **kind image cache** | kind has separate containerd from Docker | Pod keeps running old binary after rebuild | Must `kind load docker-image` after every Docker build for kind clusters | **Documented** |
| **Multi-region model split-brain** | Syncing reward matrices over WAN | Out-of-date parameters, inconsistent routing | Localized learning loops per region. Kafka/Redpanda for offline batch sync only (not implemented) | **Maintained** |

---

[← History index](history.md) · [Documentation index](README.md)
