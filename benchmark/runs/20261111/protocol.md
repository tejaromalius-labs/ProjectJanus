# Protocol: exploratory recovery study

**Status: EXPLORATORY, 2026-11-11.** Not confirmatory. The frozen protocol
([PROTOCOL.md](PROTOCOL.md), run 2026-11-10 as `bench-20261110`) is unchanged
and used a 3 s recovery phase.

## Question

After the recovery changes, how fast does Janus send traffic back to a pod that
has recovered from a fault, and what does recovery cost in tail latency, next
to RR, LC, P2C and Peak EWMA? Do the frozen gates still pass?

## Why a new study

In `bench-20261110` Janus passed every frozen gate, but the 3 s recovery phase,
which no gate covers, showed:

- after the latency fault, 0% of traffic to the recovered pod for all 3 s;
- after the CPU fault, its share swinging between about 0% and 33% every
  500 ms, with the slow requests mostly on the other pods;
- recovery p99 1.9–6.5× RR, LC and P2C.

## Janus changes under test

Made after reading the `bench-20261110` results, so they are tuned against
comparative data:

1. **Latency feature expires.** p99 uses only samples from the last 1 s; a pod
   with no response for 1 s has its latency feature reset to 0.
2. **Idle catch-up on update.** An arm idle for k engine updates has A⁻¹ scaled
   by min(γ^(−k), 100) and b by the inverse before its next update.
3. **Dead zone plus random rounding.** Penalty steps are 0 for a score gap under
   0.05, otherwise ⌊gap/0.05 − 1 + U⌋ with U uniform in [0, 1).

The Janus binary is built from the working tree, not the code frozen on 2026-11-10.

## Design

Identical to the frozen protocol (environment, workload, 600 RPS, 30 s warmup,
10 s fault, policies, 6 seeds, shuffling, validity checks) except:

- **Recovery phase: 30 s** (`driver -recovery=30s`), up from 3 s.

## Metrics (reported, no decision rule)

`benchmark/analysis/analyze.py` labels any run with a recovery phase longer
than 3 s as this study and reports:

- **Frozen gates, re-checked** as a non-regression check: fault-phase p99
  ratio, error-scenario failure rate, healthy achieved rate.
- **Recovery p99:** per-seed geometric mean of Janus/baseline recovery-phase p99
  over the latency, errors and CPU scenarios, with the frozen paired bootstrap;
  also per scenario.
- **Readmission time:** end of the first 1 s window (0.1 s steps) in which the
  recovered pod got at least 20% of requests (80% of the fair 25%); trials that
  never reach it are counted.
- **Settled share:** the recovered pod's share over the second half of the
  recovery phase.
- **First 3 s p99:** recovery p99 over the first 3 s, comparable with
  `bench-20261110`.

## Risk to check

The latency-feature reset sends a pod that is still slow a probe about every
1.5 s while the fault is active. If those probes exceed about 1% of requests,
fault-phase p99 rises; the re-checked latency gate shows it.

## Limits

As in the frozen protocol, plus: one exploratory run, metrics chosen after
seeing the frozen results, and no correction for the extra comparisons.
