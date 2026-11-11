# Project history

This is the story of how Janus got here, including the experiments it lost.
We keep the failures on purpose: almost every safety feature in the code exists
because something broke in one of these studies, and the honest record is more
useful than a highlight reel.

The individual study reports and their data were removed on 2026-11-09 and
replaced by this write-up. The October data was never archived.

## The question we keep asking

Does Janus (a LinUCB contextual-bandit router) beat classic load balancing, namely
round robin (RR), least connections (LC), power of two choices (P2C) and Peak
EWMA, on tail latency, without losing on errors or throughput?

## Where things stand today

**Not yet proven in general, but promising on one testbed.**

- In September Janus beat RR and LC on isolated faults, with some serious problems.
- In October it lost clearly to P2C and Peak EWMA.
- After a rebuild, a campaign frozen in advance ([`benchmark/PROTOCOL.md`](../benchmark/PROTOCOL.md))
  and run on 2026-11-10 was passed by Janus against all four baselines. It lost the
  post-fault recovery phase, which no gate covered.
- Recovery fixes were then benchmarked in an exploratory 120-trial run on 2026-11-11:
  the gates still passed and recovery p99 matched RR, LC and P2C. That run is not
  confirmatory.

## Timeline

| Date | Study | Outcome |
| --- | --- | --- |
| 2026-09-27 | Controlled benchmark vs RR and LC (kind cluster, 2 backends) | Won on latency/CPU faults; capacity collapse, erratic 503 handling |
| 2026-10-19 | Smoke validation vs RR (new telemetry) | All smoke gates passed; not comparative evidence |
| 2026-10-19 | Preregistered study vs Peak EWMA and P2C | Lost every gate by a wide margin |
| 2026-10-19 | Follow-up: request-aware correction to Peak EWMA | Stopped at 28/80 trials; no learning benefit shown |
| 2026-10-19 | Analytical notes (routing math, LinUCB opportunity) | Conditional advantages only |
| 2026-11-08 | Re-analysis of the October data | Explained the baseline tails; post hoc |
| 2026-11-09 | Reward change, black-box harness, cold-start fixes | Smoke-tested locally |
| 2026-11-10 | Tie-break and update-context fixes; one-seed smoke; protocol frozen | Healthy tail fixed; campaign pending |
| 2026-11-10 | Frozen campaign vs RR, LC, P2C, Peak EWMA (120 trials) | All gates passed; recovery phase lost 2–6.5× |
| 2026-11-10 | Recovery fixes: stale latency feature, idle catch-up, randomized steps | Unit-tested |
| 2026-11-11 | Exploratory recovery study (120 trials, 30 s recovery phase) | Gates still passed; recovery p99 on par with RR, LC, P2C; Janus readmits a recovered pod 0.5–1.25 s later |

## Read the chapters in order

| # | Chapter | What you will learn |
| --- | --- | --- |
| 1 | [September: the first benchmark](history/01-september-first-benchmark.md) | Janus beats weak baselines; where the circuit breaker and the soft score penalty came from |
| 2 | [October: tougher opponents](history/02-october-studies.md) | Why Janus lost to P2C and Peak EWMA, and what the post-hoc analysis explained |
| 3 | [November, part 1: the rebuild](history/03-november-rebuild.md) | New reward, cold-start bugs, the tie-break fix and why the benchmark is a separate module |
| 4 | [November, part 2: frozen campaign and recovery](history/04-frozen-campaign-and-recovery.md) | The numbers, the recovery failure, the fixes and the exploratory re-test |
| 5 | [Known issues and risks](known-issues.md) | What is still open, in plain terms |

## A note on trust

Three habits run through every chapter, and they are worth knowing before you
quote a number:

1. **Freeze first, then run.** A protocol with a decision rule is committed before
   the trials start, and no trial is filtered out afterwards.
2. **Label what is and isn't evidence.** Local smoke runs are "not valid evidence".
   Post-hoc analysis is "directional only". The 2026-11-11 recovery study is
   "exploratory".
3. **Say what would change our mind.** Each study lists its limits. The benchmark's
   limits are summarised in the [README](../README.md#does-it-work).
