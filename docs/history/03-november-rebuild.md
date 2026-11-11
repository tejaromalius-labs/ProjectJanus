# November, part 1: fixing what October exposed

**In one sentence:** a new reward, a rebuilt routing rule and a separate black-box
benchmark, all driven by local smoke runs that kept finding real bugs.

**How to read this:** the bullets are in the order we found things. Each one is a
bug, its cause, and the change. The smoke runs were local and are *not* valid
evidence; they are recorded because they explain why the code looks the way it does.

## November: current work

- **Reward:** latency score now falls linearly from 1.0 at 0 ms to 0.5 at the
  100 ms SLA, then decays exponentially, so 5 ms and 90 ms responses differ.
- **Separation:** research hooks removed from the proxy; simulator and tests
  moved out of the engine source. The comparison is a separate module,
  [`benchmark/`](../../benchmark/), that runs the unmodified Janus binary against
  standalone RR, LC, P2C and Peak EWMA proxies with no breakers. Janus reads
  real cgroup metrics through its normal Prometheus path.
- **Cold-start defects found in local smoke runs and fixed:** two pods reached
  SHADOW first, carried all load and failed the latency gate, so no pod ever
  became ACTIVE (healthy p99 ~2 s). Fixes: score every pod until one is ACTIVE;
  compare the gate against ACTIVE pods only; always score SHADOW pods; give
  each new pod 20 requests before it competes on score (a new arm's estimate
  starts at zero and otherwise scores below busy pods).
- **Smoke result after the fixes (one local trial, not valid evidence):** all
  four pods ACTIVE within the 30 s warmup, healthy p99 42 ms, but recovery after
  a CPU fault reached the 2 s timeout. Earlier local baseline runs had healthy
  p99 around 4 ms.
- **Full local seed after the γ-windup fix (20 trials, not valid evidence):**
  Janus lost every scenario, including healthy load (p99 2,002 ms with 986
  failures vs 4–15 ms). Per-pod scores of identical healthy pods spread over
  0.1–0.8, so the 0.05 band usually held one pod (about 76% of decisions at that
  spread), which took all traffic and overloaded. A pod outside the band was
  never updated again (one stayed at 1,543 observations for 15 s), so it never
  recovered. **Changes:** a lower score now raises a pod's cost,
  (in flight + 1) · 2^(gap/0.05), instead of excluding it, which bounds how far
  load can concentrate. An unchosen arm's bonus grows as γ^(−k/2) over the k
  engine updates since its last one (capped at 1), so excluded pods are
  revisited. Why the scores are that noisy is still open.
- **Warmup budget removed:** 2 of the 4 Janus trials in that seed ended warmup with a pod
  still WARMING. Once a pod was ACTIVE, WARMING pods got only the 1% budget, so
  a pod at ~125 observations needed ~19 s more to reach SHADOW at 400 RPS. The
  budget was kept in September to break the cold-start deadlock; scoring every
  pod plus the 20-request minimum now does that, so WARMING pods are scored like
  the others and lifecycle state gates promotion only.
- **Local seed after those three changes (20 trials, not valid evidence):** all
  Janus pods ACTIVE after warmup in every trial; no client timeouts. Fault p99:
  latency 81 ms (baselines 204 ms, RR 2 s), CPU 81 ms (92–378 ms). In the 503
  fault Janus had 4 failures (baselines 264–300) but p99 203 ms, because the
  other three backends then ran at ~100% CPU. Healthy p99 was 44–53 ms vs
  4–31 ms, with 15–65 throttled periods per backend vs 0–2 for RR; recovery p99
  66–85 ms vs 4–53 ms. At 600 RPS backends ran at ~80% of quota, not the 60%
  assumed in the protocol.
- **Cause of the healthy-tail gap (2026-11-09, code review plus a simulation,
  not a benchmark run):** equal costs went to the higher score. With a smooth
  gap the score acted as a fixed priority order, so the top pod took every
  request that found it idle: about 260 req/s per 100 ms window (43% of
  600 RPS) on a pod that serves 250. That pod was CPU-throttled, though long-run
  shares looked even. The simulation reproduced the gap (Janus 27 ms vs RR/LC
  2 ms, ~40 of 100 periods throttled per backend). Randomizing only exact ties
  changed nothing. Rounding the gap down to whole 0.05 steps and breaking ties
  at random gave 2.0 ms with no throttling. A second defect: updates rebuilt the
  context from features refreshed after dispatch, so arms trained on contexts
  they were not routed at. The reward's CPU term and routing-dependent load
  features widen the score gaps through feedback; they were left unchanged.
  **Changes:** cost (in flight + 1) · 2^⌊gap/0.05⌋, random ties; updates use the
  features captured at dispatch.
- **Local seed after those fixes (2026-11-10, 20 trials, 0 flagged, not valid
  evidence):** all Janus pods ACTIVE after warmup in every trial. Healthy p99
  4.3 ms (RR 4.7, LC 4.4, P2C 3.9, Peak EWMA 8.2) with 0–2 throttled periods
  per backend, down from 44–53 ms and 15–65. Fault p99: latency 20.7 ms (LC
  19.1, Peak EWMA 34.2, P2C 204, RR 2 s); CPU 24.7 ms (Peak EWMA 26.3, LC 96,
  P2C 99, RR 263). Errors fault: 4 failures vs 296–343, but p99 57 ms vs
  4–11 ms, as the three remaining backends throttled. Recovery p99 after the
  CPU fault 37.5 ms and after the errors fault 14.6 ms, vs about 4 ms for
  LC/P2C. With one seed the analysis's "all four beaten" verdict has no
  interval. Healthy backends ran at ~73% of CPU quota, not 60%.
- **Protocol frozen (2026-11-10):** warmup set to 30 s at the measured 600 RPS,
  so measurement starts at steady state. The benchmark VM is `janus-bench`
  (4 vCPUs, 2 GiB). `run.sh` stages builds under `$HOME`, because snap-confined
  Multipass cannot read `/tmp` or hidden home directories.

---

← [Previous](02-october-studies.md) · [History index](../history.md) · [Next →](04-frozen-campaign-and-recovery.md)
