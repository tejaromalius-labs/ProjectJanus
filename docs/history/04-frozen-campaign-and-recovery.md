# November, part 2: the frozen campaign and the recovery fixes

**In one sentence:** with the protocol frozen first, Janus beat all four baselines
on tail latency during faults, then lost the recovery phase (which no gate
covered), and a second, exploratory run showed the recovery fixes work.

**Read this before quoting any number:** the 2026-11-10 campaign is confirmatory.
The 2026-11-11 recovery study is not (see the limits at the end of this page).

## November: frozen campaign and recovery fixes

**Campaign (2026-11-10, `benchmark/runs/20261110/`):** 120/120 trials,
6 seeds, 0 validity findings, 1.04M measured requests. The protocol was frozen
on 2026-11-10 at 21:23 UTC, before the first trial (21:25 UTC). Verdict: Janus
beat all four baselines.

| Baseline | Janus/baseline fault p99 | Simultaneous interval |
| --- | --- | --- |
| RR | 0.014 | 0.009–0.020 |
| LC | 0.201 | 0.133–0.300 |
| P2C | 0.066 | 0.046–0.096 |
| Peak EWMA | 0.308 | 0.210–0.447 |

- Fault p99 per seed: latency fault 6–23 ms (LC 12–204, P2C 203, Peak EWMA
  28–39, RR 2 s timeouts); CPU fault 6–9 ms (LC/P2C 93–101, Peak EWMA 27–35,
  RR 225–268). Healthy p99 3.96 ms vs 3.89–3.93 (Peak EWMA 5.0).
- Caveats: the RR ratio is set by RR's 2 s client timeouts, not a measured
  tail. The failure gate (24 failures vs ~1,800) compares Janus's circuit
  breaker with baselines that have none; Janus's errors-fault p99 was 3× worse.
  Janus used ~36% more proxy CPU per request (184 vs 135 ms per 1k).
- **Recovery lost (not a gate):** recovery p99 was 1.9–6.5× RR/LC/P2C. In 500 ms
  bins from the raw CSVs: after the latency fault the recovered pod got 0% of
  traffic for the whole 3 s; after the CPU fault its share swung 0 → 33 → 4 →
  33%, and the slow requests landed mostly on the *other* pods.

**Causes (code review plus the raw CSVs):**
1. The p99 feature only changes when the pod answers. Ten slow samples at the
   head of the 256-sample FIFO needed ~254 fresh responses to flush, and a
   pod with a low score got none: a lockout.
2. Idle growth widened only the score; the next update applied γ once, so a
   fresh sample after a long idle gap weighed as little as if there had been
   no gap. This is not the D-LinUCB the idle bonus is modelled on.
3. The 2^⌊gap/0.05⌋ step made a pod's share jump whenever its gap crossed an
   edge, so a recovering pod's share oscillated.

**Changes (2026-11-10, unit-tested; benchmarked 2026-11-11 below):**
- **Latency feature expires:** samples older than `featurestore.LatencyMaxAge`
  (1 s) are ignored, and a refresh resets the feature to the cold-cache 0 after
  1 s with no response. Staleness is bounded by 1 s plus the refresh interval
  (1.5 s on the bench, up to 6 s at the 5 s default).
- **Idle catch-up on update:** `update_after_idle` scales A⁻¹ by
  min(γ^(−idle), 100) and b by the inverse (θ unchanged) before the normal
  update; idle 0 is the old update exactly. The cap bounds windup in unused
  directions (stability test with 20,000-update gaps).
- **Dead zone plus random rounding:** steps = 0 for a gap under 0.05, else
  ⌊gap/0.05 − 1 + U⌋. The expected multiplier is continuous and monotone, and
  every penalty is one step (2×) weaker. Plain random rounding from 0 was
  rejected: a pod 0.02 below the best would get a 70/30 split of idle traffic,
  undoing the 2026-11-09 tie-break fix.
- Considered but not done: a minimum per-pod exploration share, and a
  change-detection reset (M-UCB / GLR). Lowering γ was rejected: it adds
  variance to every healthy decision.

Any of these changes the frozen algorithm, so their effect needs a new,
separately labelled study: a 30 s recovery window, recovery p99, time until the
recovered pod is back to a 25% share, and healthy and fault p99 as
non-regression checks. Risk to check there: the feature reset sends a pod
that is still slow a probe every ~1.5 s during a fault.

**Exploratory recovery study (2026-11-11, `benchmark/runs/20261111/`):** protocol [`benchmark/PROTOCOL-recovery.md`](../../benchmark/PROTOCOL-recovery.md)
(written 00:25, before the 01:39 start; no decision rule; the binary was
built from the working tree as it stood that night). Identical to the frozen design
except a 30 s recovery phase. 120/120 trials, 6 seeds, 0 validity findings,
2.50M measured requests, 2 h 10 min.

| Baseline | Fault p99 ratio (gate) | Interval | Recovery p99 ratio | Interval |
| --- | --- | --- | --- | --- |
| RR | 0.011 | 0.009–0.013 | 1.006 | 0.993–1.023 |
| LC | 0.212 | 0.181–0.245 | 1.015 | 1.000–1.028 |
| P2C | 0.056 | 0.047–0.065 | 1.015 | 1.003–1.023 |
| Peak EWMA | 0.254 | 0.217–0.302 | 0.320 | 0.291–0.346 |

Median over 6 seeds, p99 in ms (fault phase / recovery phase):

| Scenario | Janus | RR | LC | P2C | Peak EWMA |
| --- | --- | --- | --- | --- | --- |
| Healthy | 4.12 | 4.00 | 3.94 | 3.96 | 6.51 |
| Latency fault | 8.43 / 4.02 | 2001.8 / 4.02 | 14.14 / 3.99 | 203.5 / 4.04 | 29.38 / 19.62 |
| Errors fault | 9.49 / 4.08 | 3.96 / 3.97 | 3.99 / 3.94 | 4.02 / 3.95 | 5.87 / 6.11 |
| CPU fault | 6.96 / 3.98 | 246.4 / 4.05 | 95.4 / 4.00 | 98.2 / 3.93 | 31.14 / 16.36 |

- Failures in the errors fault (6,000 requests): Janus 4 (0.07%) vs 300 (5%)
  for RR, LC and P2C. Latency fault: RR failed 19.9% (2 s timeouts).
- Readmission (first 1 s window with ≥20% of traffic to the recovered pod,
  median): Janus 2.25 s (latency), 1.0 s (errors), 1.55 s (CPU); RR, LC, P2C
  1.0 s in all three; Peak EWMA 24.4 s, 1.0 s, 21.8 s. Settled share (second
  half of recovery): Janus 0.249–0.250; Peak EWMA 0.18 (latency), 0.21 (CPU).
- Janus used ~25–33% more proxy CPU per request (184 vs 138–148 ms per 1k).
- Recovery p99 is ~4 ms for every policy except Peak EWMA, which is the
  testbed floor, so ratios near 1.0 show no penalty and have little power to
  show an advantage. Against LC and P2C the recovery interval touches 1.0.
- Not a clean confirmation: the three changes were written after reading
  `bench-20261110`, the question was designed around a known failure, no
  correction was made for the added comparisons, and there is no repeat run.
  Janus's errors-fault p99 (9.5 ms vs ~4 ms) is worse than RR, LC and P2C and
  outside the gates.

---

← [Previous](03-november-rebuild.md) · [History index](../history.md) · [Known issues and risks →](../known-issues.md)
