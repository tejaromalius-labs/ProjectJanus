# Benchmark result: Janus versus RR, LC, P2C and Peak EWMA

**Janus beat all four baselines in this profile.**

A baseline counts as beaten only if all three gates pass: the simultaneous upper bound of the
Janus/baseline p99 ratio is below 0.85 (a 15% advantage), the error-scenario failure
rate rises by at most 0.1 points, and healthy achieved rate is at least
97% of the baseline's. Not beating a baseline is not proof of inferiority.

## Integrity

Completed trials: 120 / 120.
Measured requests: 1,044,000; including warmup: 3,204,000.
Validity findings: 0.

Execution: 5393.7 seconds (89.9 minutes).

## Primary paired comparison

Ratios below one favor Janus. Intervals bootstrap the 6 paired seeds (latency and CPU
faults kept together), Bonferroni-adjusted for 4 baselines.

| Baseline | Janus/baseline p99 ratio | Simultaneous interval | Failure-rate difference (pp) | Healthy rate ratio | Beaten |
| --- | --- | --- | --- | --- | --- |
| Round robin | 0.014 | 0.009–0.020 | -4.933 | 1.000 | yes |
| Least connections | 0.201 | 0.133–0.300 | -4.964 | 1.000 | yes |
| Power of two choices | 0.066 | 0.046–0.096 | -4.919 | 1.000 | yes |
| Peak EWMA | 0.308 | 0.210–0.447 | -4.656 | 1.000 | yes |

## Scenario results

Medians over trials. Failed requests remain in latency statistics. Proxy CPU is milliseconds
per 1,000 completed requests; memory is end-of-phase current memory.

| Scenario | Policy | p50 ms | p95 ms | p99 ms | Failures / requests | Proxy CPU ms / 1k | Memory MiB |
| --- | --- | --- | --- | --- | --- | --- | --- |
| healthy | Janus | 3.06 | 3.66 | 3.96 | 0 / 36000 | 183.9 | 6.7 |
| healthy | Round robin | 3.03 | 3.61 | 3.89 | 0 / 36000 | 135.1 | 5.9 |
| healthy | Least connections | 3.03 | 3.63 | 3.93 | 0 / 36000 | 135.1 | 5.9 |
| healthy | Power of two choices | 3.04 | 3.64 | 3.92 | 0 / 36000 | 140.3 | 6.1 |
| healthy | Peak EWMA | 3.03 | 3.65 | 5.00 | 0 / 36000 | 137.7 | 6.3 |
| latency | Janus | 3.10 | 3.81 | 11.17 | 0 / 36000 | 194.4 | 6.8 |
| latency | Round robin | 3.40 | 2001.33 | 2001.83 | 7176 / 36000 | 246.5 | 27.8 |
| latency | Least connections | 3.04 | 3.68 | 15.31 | 0 / 36000 | 133.3 | 6.2 |
| latency | Power of two choices | 3.07 | 3.98 | 203.43 | 0 / 36000 | 136.4 | 6.4 |
| latency | Peak EWMA | 3.14 | 14.23 | 32.37 | 0 / 36000 | 147.1 | 6.5 |
| errors | Janus | 3.10 | 3.80 | 13.05 | 24 / 36000 | 194.0 | 6.8 |
| errors | Round robin | 3.05 | 3.68 | 4.01 | 1800 / 36000 | 146.5 | 6.0 |
| errors | Least connections | 3.03 | 3.61 | 3.90 | 1811 / 36000 | 135.6 | 5.9 |
| errors | Power of two choices | 3.06 | 3.70 | 4.05 | 1795 / 36000 | 150.3 | 6.2 |
| errors | Peak EWMA | 3.04 | 3.67 | 5.21 | 1700 / 36000 | 138.7 | 6.4 |
| cpu | Janus | 3.13 | 3.99 | 7.95 | 0 / 36000 | 186.7 | 6.8 |
| cpu | Round robin | 3.39 | 162.57 | 241.37 | 0 / 36000 | 179.3 | 7.0 |
| cpu | Least connections | 3.08 | 3.95 | 95.95 | 0 / 36000 | 135.2 | 6.0 |
| cpu | Power of two choices | 3.11 | 4.95 | 98.32 | 0 / 36000 | 135.4 | 6.5 |
| cpu | Peak EWMA | 3.13 | 8.07 | 27.45 | 0 / 36000 | 138.4 | 6.5 |

See PROTOCOL.md for the workload, estimator and decision rule.
