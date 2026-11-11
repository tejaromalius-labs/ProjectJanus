# October: tougher opponents, and an honest loss

**In one sentence:** against the smarter baselines (P2C and Peak EWMA) Janus lost
every gate by a wide margin, and a follow-up experiment was cut short.

**Why this chapter matters:** September only compared Janus with two weak
baselines. October asked the harder question and the answer was no. The post-hoc
analysis at the end explains *why* (mostly CPU throttling), and that is what shaped
the November test design.

## October: smoke validation vs RR

Added before the main October study: Prometheus resource ingestion, a rolling
p99 over the last 256 requests, `/readyz` and `/metrics`, connection-failure
feedback, and optional eBPF connection diagnostics (kept out of the reward).
Two blocks of 8-second phases at 300–1,200 RPS against RR: all gates passed,
no unexpected failures (Janus p99 under 2 ms in every phase; RR 202 ms in the
latency fault, 240 failures per block in the 503 fault). Short phases, a model
kept across faults and a Prometheus fixture make this a functional check only.

## October: preregistered study vs Peak EWMA and P2C

**Why this study:** RR and LC are weak baselines; September could not show
whether learning beats simple adaptive rules. The protocol was frozen before
any comparative traffic, and no trial was filtered.

Setup: one 4-vCPU Multipass VM; four backends at 0.25 CPU (real cgroup quotas),
proxy at 1 CPU, load from the host. Every policy shared the Janus proxy,
breakers and resource polling. 6 seeds × 3 policies × 4 scenarios = 72 trials,
680,400 requests. Scenarios: healthy at 900 RPS; latency, 503 and CPU faults at
600 RPS. Gates: p99 ratio upper bound <0.85 vs both baselines, failure-rate
increase ≤0.1 point, throughput ratio ≥0.97.

| | vs Peak EWMA | vs P2C | Gate |
| --- | --- | --- | --- |
| Fault-phase p99 ratio (Janus/baseline) | 25.9 (18.3–36.0) | 13.4 (12.2–14.2) | <0.85 |
| 503-fault failure rate | 16.9% vs 0.07% | 16.9% vs 0.07% | +≤0.1 pt |
| Healthy throughput ratio | 0.953 | 0.924 | ≥0.97 |

Median fault p99: Janus ~2,000 ms (client timeouts) vs 57–204 ms for the
baselines. Janus also used 1.3–2× the proxy CPU per completed request. Six Janus trials
had stale resource samples (flagged, kept). **Verdict: useful superiority not
demonstrated; every gate failed against both baselines.** The study did not
isolate which part of Janus caused the regression.

## October: request-aware Peak EWMA follow-up

**Why:** test whether a small learned correction on top of Peak EWMA helps when
backends specialise (two fast at GET, two fast at POST). Not LinUCB. A
"learning disabled" control isolated the effect of using the model.

The VM's backend control stopped responding after 28 of 80 trials, and the VM
then became unreachable over SSH; testing was ended. In the completed trials
the correction changed the EWMA ranking only twice, and results tracked plain
EWMA. **No learning benefit shown; hypothesis unresolved.** The code was later
removed from the proxy.

## Analytical notes

- **Routing math:** in an explicit model where backends specialise by request
  type, request-aware routing can raise capacity by 50% over any class-blind
  policy (1,500 vs 1,000 RPS) — but only with accurate predictions (>77%
  accuracy for a 15% gain at 50 µs overhead), and naive specialist routing can
  be worse than class-blind routing under a skewed mix.
- **LinUCB opportunity:** a constructed problem exists where LinUCB provably
  beats policies missing request or resource context (≤1.25 + 64/N ms vs
  ≥1.5 ms). It requires rewards that distinguish small latencies — which the
  reward at the time did not (flat below 100 ms). This motivated the reward change in [November, part 1](03-november-rebuild.md).

Neither shows that Janus, as built, can win.

## Re-analysis of the October data (post hoc)

Done after the study closed; directional only.

- Baseline tails came largely from CFS throttling: at ~95% of each backend's
  quota, P2C and EWMA fault phases had 67–78% of 100 ms periods throttled vs 6%
  for round-robin warmup.
- The 900 RPS healthy load saturated the backends (97–98% of periods throttled),
  so it measured overload rather than routing. The new benchmark uses 600 RPS.
- Janus's collapse matched the equivalence band excluding pods: one backend
  took 333 of 600 req/s before the fault.
- With γ = 0.999, the inverse covariance grows without bound in context
  directions that rarely vary (~1e13 after 30k updates). Fixed in [November, part 1](03-november-rebuild.md).

---

← [Previous](01-september-first-benchmark.md) · [History index](../history.md) · [Next →](03-november-rebuild.md)
