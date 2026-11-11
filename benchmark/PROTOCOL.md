# Protocol: Janus versus RR, LC, P2C and Peak EWMA

**Status: FROZEN 2026-11-10.** No algorithm, parameter or workload changes are
made from here on. Failed, overloaded or flagged trials stay in the dataset.

## Question

Does Janus, as shipped, deliver a useful tail-latency advantage over each of four
classic load-balancing policies, without a material failure-rate or throughput
regression?

## Policies

| Name | Policy | Implementation |
| --- | --- | --- |
| `janus` | LinUCB scoring; lowest (in flight + 1) · 2^⌊score gap/0.05⌋, random ties; circuit breakers; pod lifecycle | Unmodified production binary `linucb-proxy/cmd/proxy` |
| `rr` | Round robin | `benchmark/internal/policy` |
| `lc` | Least connections: fewest in flight across all backends, random ties | same |
| `p2c` | Power of two choices: two distinct random backends, fewer in flight | same |
| `pewma` | Peak EWMA cost × (in flight + 1), 10 s decay, chosen by two random choices (Finagle-style) | same |

Janus changes made after local smoke trials and before freezing (defect fixes,
not tuning against comparative results). In the first smoke run Janus never got
all four pods to ACTIVE, and healthy p99 was about 2 s:

1. Cold start lasts until a pod is ACTIVE; until then every pod is scored.
2. The SHADOW→ACTIVE latency gate compares against the median of ACTIVE pods only.
3. SHADOW pods are always scored alongside ACTIVE pods.
4. Each pod gets its first 20 requests before competing on score.
5. The discounted engine update restores the prior in directions the context
   never varies in (found by the γ = 0.999 stability test; scores reached 1e5).
6. A lower score raises a pod's routing cost instead of excluding it: the hard
   0.05 band herded traffic onto one pod when scores were noisy (local seed:
   healthy p99 2 s).
7. An unchosen arm's exploration bonus grows as γ^(−k/2) over the k engine
   updates since its last one, capped at 1; before, an excluded pod's score
   stayed frozen and it never got traffic again.
8. WARMING pods are scored with the others; the 1% warmup budget and
   `-warmup-pct` are removed. A pod still WARMING when another became ACTIVE
   needed about 50 s at the budget to reach SHADOW (2 of 4 Janus trials flagged).
9. The score gap counts in whole 0.05 steps and equal costs are broken at
   random. Ties used to go to the higher score, so with a smooth gap the score
   was a fixed priority order: the top pod took every request that found it
   idle, about 260 req/s against 250 req/s of capacity, and was CPU-throttled
   (local seed: healthy p99 44–53 ms vs 4–31 ms).
10. Updates train on the pod features captured at dispatch, not features
    refreshed while the request ran.

Changes 9 and 10 came from reading the code and a simulation of this testbed
(healthy p99 27 ms → 2 ms, matching RR/LC). They were motivated by Janus's
healthy p99 next to the baselines' in local smoke runs, which is comparative
data, so they are not purely blind defect fixes. A one-seed smoke after them
(2026-11-10) gave Janus a healthy p99 of 4.3 ms vs 3.9–8.2 ms. The frozen
campaign uses fresh trials only; smoke data is not part of it.

The baselines run in `benchmark/cmd/baseline`, a minimal reverse proxy with the
same upstream transport settings as Janus (5 s response-header timeout, 3 s dial,
256 idle connections per host) and **no circuit breakers or retries**. This
compares Janus as shipped against the classic algorithms as they are, not
against Envoy, NGINX or Finagle binaries.

## Environment

- One dedicated Multipass VM, Ubuntu 24.04, 4 vCPUs, 2 GiB memory, 8 GB disk
  (`multipass launch 24.04 --name janus-bench --cpus 4 --memory 2G --disk 8G`),
  with no other VMs running on the host. cgroup v2, systemd. CPU 0: agent and
  system; CPU 1: proxy under test (1.0 CPU, 512 MiB, `GOMAXPROCS=1`);
  CPUs 2–3: four backends at 0.25 CPU and 128 MiB each.
- Load generation runs on the host, outside the VM.
- Janus reads backend CPU, memory, active connections, queue depth and GC from
  real cgroup and backend counters, served to its normal Prometheus feature
  source by `benchmark/cmd/agent` every 500 ms.

## Workload

- Backends: each request uses 1 ms of thread CPU plus 1 ms sleep, at most 16
  concurrently. GET returns 128 B; POST returns 4 KiB.
- Open-loop constant arrival rate; every tenth request is a 4 KiB POST.
  Latency is measured from each request's **scheduled** arrival to completion,
  with a 2 s client timeout; failures stay in latency statistics.
- Measured rate: **600 RPS**, about 73% of backend CPU quota (measured in the
  2026-11-10 smoke; 70–78% across policies). Study 2's 900 RPS healthy load was
  saturated, so it is not reused.

## Trial procedure

Every trial starts a fresh proxy process, so no state carries between trials.

1. Reset all backends to healthy; start the policy under test.
2. Warmup: **30 s at 600 RPS**, identical for all policies. Warming up at the
   measured rate means measurement starts at steady state, with no rate step
   inside the measured window; 30 s is 18,000 requests, about 4.5 of Janus's
   ~1,000-update memory windows per pod. For Janus, every pod must be `ACTIVE`
   at the end of warmup; if not, the trial is flagged and kept.
3. Scenario:
   - `healthy`: 10 s at 600 RPS.
   - `latency`, `errors`, `cpu`: 3 s healthy → 10 s fault on one backend → 3 s recovery, all at 600 RPS.
     Faults: +200 ms per request; 503 on every fifth request; 50 ms CPU burn every 100 ms.
4. Stop the proxy.

Grid: 5 policies × 4 scenarios × **6 seeds** = 120 trials. Scenario and policy
order are shuffled per seed with fixed RNG seed 20261109; the faulted backend
rotates with the seed.

## Primary endpoint and decision rule

For each baseline separately (`benchmark/analysis/analyze.py`):

- **Latency gate:** the per-seed geometric mean of Janus/baseline fault-phase p99
  over the latency and CPU scenarios; the paired bootstrap (20,000 draws,
  Bonferroni-adjusted for four baselines) upper bound must be **below 0.85**.
- **Failure gate:** error-scenario failure rate may rise by at most **0.1 points**.
- **Throughput gate:** healthy achieved rate ratio must be at least **0.97**.

A baseline is beaten only if all three gates pass and the run has no validity
findings. Not beating a baseline is not proof of inferiority or equivalence.

## Validity checks (flag, never filter)

Incomplete arrivals; generator p99 schedule delay over 10 ms; missing or stale
backend resource samples; Janus telemetry drops; CPU fault that did not increase
throttling; Janus pods not all ACTIVE after warmup; raw request counts that do
not match the summaries.

## Limits

Single VM, synthetic backends, short fault windows, six seeds. Not evidence about
multi-host networking, Kubernetes discovery, long-running traffic or production
capacity.
