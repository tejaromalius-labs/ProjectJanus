# Exploratory recovery study: Janus versus RR, LC, P2C and Peak EWMA

**Frozen gates re-checked in an exploratory run (not confirmatory): Janus beat all four baselines in this profile.**

Recovery phase: 30 s (frozen protocol: 3 s). Janus is the
working-tree build with the recovery changes, not the frozen binary, so these gates are a
non-regression check, not a confirmatory result.

A baseline counts as beaten only if all three gates pass: the simultaneous upper bound of the
Janus/baseline p99 ratio is below 0.85 (a 15% advantage), the error-scenario failure
rate rises by at most 0.1 points, and healthy achieved rate is at least
97% of the baseline's. Not beating a baseline is not proof of inferiority.

## Integrity

Completed trials: 120 / 120.
Measured requests: 2,502,000; including warmup: 4,662,000.
Validity findings: 0.

Execution: 7829.9 seconds (130.5 minutes).

## Primary paired comparison

Ratios below one favor Janus. Intervals bootstrap the 6 paired seeds (latency and CPU
faults kept together), Bonferroni-adjusted for 4 baselines.

| Baseline | Janus/baseline p99 ratio | Simultaneous interval | Failure-rate difference (pp) | Healthy rate ratio | Beaten |
| --- | --- | --- | --- | --- | --- |
| Round robin | 0.011 | 0.009–0.013 | -4.933 | 1.000 | yes |
| Least connections | 0.212 | 0.181–0.245 | -4.983 | 1.000 | yes |
| Power of two choices | 0.056 | 0.047–0.065 | -4.967 | 1.000 | yes |
| Peak EWMA | 0.254 | 0.217–0.302 | -4.811 | 1.000 | yes |

## Scenario results

Medians over trials. Failed requests remain in latency statistics. Proxy CPU is milliseconds
per 1,000 completed requests; memory is end-of-phase current memory.

| Scenario | Policy | p50 ms | p95 ms | p99 ms | Failures / requests | Proxy CPU ms / 1k | Memory MiB |
| --- | --- | --- | --- | --- | --- | --- | --- |
| healthy | Janus | 3.10 | 3.76 | 4.12 | 0 / 36000 | 190.7 | 6.8 |
| healthy | Round robin | 3.05 | 3.67 | 4.00 | 0 / 36000 | 140.4 | 5.9 |
| healthy | Least connections | 3.05 | 3.65 | 3.94 | 0 / 36000 | 140.6 | 5.9 |
| healthy | Power of two choices | 3.05 | 3.67 | 3.96 | 0 / 36000 | 139.9 | 6.0 |
| healthy | Peak EWMA | 3.07 | 3.74 | 6.51 | 0 / 36000 | 148.8 | 6.3 |
| latency | Janus | 3.08 | 3.80 | 8.43 | 0 / 36000 | 185.6 | 6.8 |
| latency | Round robin | 3.42 | 2001.35 | 2001.84 | 7176 / 36000 | 246.5 | 28.0 |
| latency | Least connections | 3.04 | 3.67 | 14.14 | 0 / 36000 | 133.1 | 6.1 |
| latency | Power of two choices | 3.11 | 4.20 | 203.46 | 0 / 36000 | 141.9 | 6.3 |
| latency | Peak EWMA | 3.13 | 12.21 | 29.38 | 0 / 36000 | 139.3 | 6.5 |
| errors | Janus | 3.10 | 3.79 | 9.49 | 24 / 36000 | 191.5 | 6.8 |
| errors | Round robin | 3.04 | 3.64 | 3.96 | 1800 / 36000 | 138.8 | 5.9 |
| errors | Least connections | 3.07 | 3.69 | 3.99 | 1818 / 36000 | 143.6 | 6.0 |
| errors | Power of two choices | 3.06 | 3.71 | 4.02 | 1812 / 36000 | 146.6 | 6.0 |
| errors | Peak EWMA | 3.08 | 3.78 | 5.87 | 1756 / 36000 | 147.8 | 6.3 |
| cpu | Janus | 3.12 | 3.92 | 6.96 | 0 / 36000 | 176.7 | 6.8 |
| cpu | Round robin | 3.40 | 160.89 | 246.39 | 0 / 36000 | 178.5 | 7.2 |
| cpu | Least connections | 3.08 | 3.97 | 95.36 | 0 / 36000 | 134.1 | 6.2 |
| cpu | Power of two choices | 3.11 | 5.11 | 98.19 | 0 / 36000 | 132.6 | 6.4 |
| cpu | Peak EWMA | 3.17 | 10.13 | 31.14 | 0 / 36000 | 143.0 | 6.5 |

## Recovery

Reported without a decision rule. Ratios are Janus/baseline recovery-phase p99, geometric over
the latency, errors and CPU scenarios per seed, with the same paired bootstrap as above.

| Baseline | Recovery p99 ratio | Simultaneous interval | latency | errors | cpu |
| --- | --- | --- | --- | --- | --- |
| Round robin | 1.006 | 0.993–1.023 | 1.005 | 1.020 | 0.994 |
| Least connections | 1.015 | 1.000–1.028 | 1.013 | 1.026 | 1.006 |
| Power of two choices | 1.015 | 1.003–1.023 | 0.998 | 1.024 | 1.021 |
| Peak EWMA | 0.320 | 0.291–0.346 | 0.202 | 0.645 | 0.252 |

Readmission: end of the first 1 s window in which the recovered pod got at least
20% of requests (fair share 25%); medians over seeds that reached it. Settled share:
the recovered pod's share over the second half of the recovery phase. First 3 s p99
is comparable with the frozen protocol's recovery phase.

| Scenario | Policy | Readmission s | Never readmitted | Settled share | Recovery p99 ms | First 3 s p99 ms |
| --- | --- | --- | --- | --- | --- | --- |
| latency | Janus | 2.2 | 0 / 6 | 25.0% | 4.02 | 4.51 |
| latency | Round robin | 1.0 | 0 / 6 | 25.0% | 4.02 | 4.00 |
| latency | Least connections | 1.0 | 0 / 6 | 25.0% | 3.99 | 3.97 |
| latency | Power of two choices | 1.0 | 0 / 6 | 24.9% | 4.04 | 4.16 |
| latency | Peak EWMA | 24.4 | 0 / 6 | 17.6% | 19.62 | 31.35 |
| errors | Janus | 1.0 | 0 / 6 | 25.0% | 4.08 | 4.10 |
| errors | Round robin | 1.0 | 0 / 6 | 25.0% | 3.97 | 3.90 |
| errors | Least connections | 1.0 | 0 / 6 | 24.8% | 3.94 | 3.92 |
| errors | Power of two choices | 1.0 | 0 / 6 | 25.0% | 3.95 | 3.92 |
| errors | Peak EWMA | 1.0 | 0 / 6 | 25.0% | 6.11 | 4.38 |
| cpu | Janus | 1.6 | 0 / 6 | 24.9% | 3.98 | 4.09 |
| cpu | Round robin | 1.0 | 0 / 6 | 25.0% | 4.05 | 55.71 |
| cpu | Least connections | 1.0 | 0 / 6 | 25.2% | 4.00 | 4.01 |
| cpu | Power of two choices | 1.0 | 0 / 6 | 25.0% | 3.93 | 4.02 |
| cpu | Peak EWMA | 21.8 | 0 / 6 | 21.0% | 16.36 | 25.84 |

See protocol.md (PROTOCOL-recovery.md) for the workload, estimator and decision rule.
