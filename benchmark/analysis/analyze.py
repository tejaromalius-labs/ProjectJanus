#!/usr/bin/env python3
"""Paired analysis of Janus against each baseline. Trials, not requests, are the units.

A run whose recovery phase is longer than the frozen protocol's 3 s is labelled
an exploratory recovery study (PROTOCOL-recovery.md): the frozen gates are
re-checked, and recovery metrics are reported without a decision rule.

usage: analyze.py RESULTS_DIRECTORY   (writes analysis.json and report.md there)
Exit status: 0 complete and valid, 2 incomplete or validity findings.
"""
import csv
import gzip
import json
import math
import pathlib
import random
import statistics
import sys

JANUS = "janus"
BASELINES = ("rr", "lc", "p2c", "pewma")
NAMES = {"janus": "Janus", "rr": "Round robin", "lc": "Least connections",
         "p2c": "Power of two choices", "pewma": "Peak EWMA"}
SCENARIOS = ("healthy", "latency", "errors", "cpu")
PRIMARY = ("latency", "cpu")          # scenarios in the primary latency endpoint
LATENCY_MARGIN = 0.85                 # upper interval bound must be below this
FAILURE_MARGIN = 0.001                # max allowed failure-rate increase
THROUGHPUT_FLOOR = 0.97               # min healthy achieved-rate ratio
DRAWS = 20000
FAULTS = ("latency", "errors", "cpu")  # scenarios with a recovery phase
FROZEN_RECOVERY_S = 3                 # recovery phase length in the frozen protocol
READMIT_SHARE = 0.20                  # 80% of the fair 1/4 share
WINDOW_S, STEP_S = 1.0, 0.1           # sliding window for the recovered pod's share

folder = pathlib.Path(sys.argv[1])
rows = [json.loads(line) for line in (folder / "trials.jsonl").read_text().splitlines()]
design = json.loads((folder / "design.json").read_text())
seeds = sorted({d["seed"] for d in design})
policies = (JANUS, *BASELINES)
expected = {(s, p, c) for s in seeds for p in policies for c in SCENARIOS}
actual = {(r["trial"]["seed"], r["trial"]["policy"], r["trial"]["scenario"]) for r in rows}
grid = len(expected)

validity = []
complete = actual == expected and len(rows) == grid and len(design) == grid
if not complete:
    validity.append("Incomplete or duplicate trial grid")
if not (folder / "completion.json").exists():
    validity.append("Campaign did not complete")
for row in rows:
    for reason in row["invalid"]:
        validity.append(f'trial {row["trial"]["index"]}: {reason}')
    for phase in [row["warmup"], *row["phases"]]:
        path = folder / phase["raw"]
        if not path.exists():
            validity.append(f'Missing raw data: {phase["raw"]}')
            continue
        # Recheck request counts against the raw observations.
        with gzip.open(path, "rt") as stream:
            count = sum(1 for _ in csv.DictReader(stream))
        if count != phase["planned"] or count != phase["completed"]:
            validity.append(f'Raw request count mismatch: {phase["raw"]}')

lookup = {(r["trial"]["seed"], r["trial"]["policy"], r["trial"]["scenario"]): r for r in rows}
recovery_lengths = [p["planned"] / p["rps"] for r in rows for p in r["phases"] if p["name"] == "recovery"]
recovery_seconds = statistics.median(recovery_lengths) if recovery_lengths else 0
exploratory = recovery_seconds > FROZEN_RECOVERY_S
protocol_file = "protocol.md (PROTOCOL-recovery.md)" if exploratory else "PROTOCOL.md"


def measurement(seed, policy, scenario):
    row = lookup[(seed, policy, scenario)]
    name = "healthy" if scenario == "healthy" else "fault"
    return next(p for p in row["phases"] if p["name"] == name)


def recovery(seed, policy, scenario):
    return next(p for p in lookup[(seed, policy, scenario)]["phases"] if p["name"] == "recovery")


def nearest_rank(values, q):
    ordered = sorted(values)
    return ordered[max(0, math.ceil(q * len(ordered)) - 1)]


def recovery_detail(seed, policy, scenario):
    """Readmission time, settled share and first-3 s p99 for the recovered pod.

    Arrivals are on a fixed schedule, so request i is due at i / rate seconds.
    Readmission is the end of the first 1 s window (0.1 s steps) in which the
    recovered pod got at least READMIT_SHARE of requests; None if never.
    """
    row = lookup[(seed, policy, scenario)]
    phase = recovery(seed, policy, scenario)
    port = str(row["trial"]["fault_port"])
    with gzip.open(folder / phase["raw"], "rt") as stream:
        requests = [(r["backend"] == port, float(r["scheduled_latency_ms"])) for r in csv.DictReader(stream)]
    rate, n = phase["rps"], len(requests)
    width, step = round(rate * WINDOW_S), max(1, round(rate * STEP_S))
    hits = [0]
    for hit, _ in requests:
        hits.append(hits[-1] + hit)
    readmit = next(((start + width) / rate for start in range(0, n - width + 1, step)
                    if hits[start + width] - hits[start] >= READMIT_SHARE * width), None)
    half = n // 2
    first = [ms for _, ms in requests[:round(rate * FROZEN_RECOVERY_S)]]
    return {"readmission_s": readmit, "settled_share": (hits[n] - hits[half]) / (n - half),
            "first_3s_p99_ms": nearest_rank(first, 0.99), "p99_ms": phase["p99_ms"]}


def geometric(values):
    return math.exp(statistics.mean(math.log(max(v, 1e-12)) for v in values))


def percentile(values, q):
    ordered = sorted(values)
    pos = (len(ordered) - 1) * q
    lo = int(pos)
    hi = min(lo + 1, len(ordered) - 1)
    return ordered[lo] + (ordered[hi] - ordered[lo]) * (pos - lo)


# Bonferroni across the four contrasts: two-sided 95% family-wise.
tail = 0.05 / len(BASELINES) / 2


def paired_interval(per_seed):
    rng = random.Random(20261109)
    n = len(per_seed)
    bootstrap = [geometric([per_seed[rng.randrange(n)] for _ in range(n)]) for _ in range(DRAWS)]
    return geometric(per_seed), [percentile(bootstrap, tail), percentile(bootstrap, 1 - tail)]


contrasts = {}
if complete:
    for baseline in BASELINES:
        per_seed = [geometric([measurement(s, JANUS, c)["p99_ms"] / measurement(s, baseline, c)["p99_ms"]
                               for c in PRIMARY]) for s in seeds]
        ratio, interval = paired_interval(per_seed)
        rates = {}
        for policy in (JANUS, baseline):
            phases = [measurement(s, policy, "errors") for s in seeds]
            rates[policy] = sum(p["failures"] for p in phases) / sum(p["completed"] for p in phases)
        failure_diff = rates[JANUS] - rates[baseline]
        throughput = geometric([measurement(s, JANUS, "healthy")["achieved_rps"] /
                                measurement(s, baseline, "healthy")["achieved_rps"] for s in seeds])
        contrasts[baseline] = {
            "per_seed_primary_p99_ratios": per_seed,
            "primary_p99_ratio": ratio,
            "simultaneous_ratio_interval": interval,
            "failure_rate_difference": failure_diff,
            "healthy_achieved_rate_ratio": throughput,
            "latency_gate": interval[1] < LATENCY_MARGIN,
            "failure_gate": failure_diff <= FAILURE_MARGIN,
            "throughput_gate": throughput >= THROUGHPUT_FLOOR,
        }
        c = contrasts[baseline]
        c["passed"] = complete and not validity and c["latency_gate"] and c["failure_gate"] and c["throughput_gate"]

passed_all = complete and bool(contrasts) and all(c["passed"] for c in contrasts.values())
beaten = [b for b, c in contrasts.items() if c["passed"]]
if passed_all:
    verdict = "Janus beat all four baselines in this profile"
elif beaten:
    verdict = "Janus beat " + ", ".join(NAMES[b] for b in beaten) + " but not every baseline in this profile"
else:
    verdict = "Janus did not demonstrate superiority over any baseline in this profile"
if exploratory:
    verdict = "Frozen gates re-checked in an exploratory run (not confirmatory): " + verdict

# Recovery metrics: reported, never gated.
recovery_contrasts, recovery_table = {}, {}
if complete:
    details = {(s, p, c): recovery_detail(s, p, c) for s in seeds for p in policies for c in FAULTS}
    for baseline in BASELINES:
        per_seed = [geometric([details[(s, JANUS, c)]["p99_ms"] / details[(s, baseline, c)]["p99_ms"]
                               for c in FAULTS]) for s in seeds]
        ratio, interval = paired_interval(per_seed)
        recovery_contrasts[baseline] = {
            "per_seed_recovery_p99_ratios": per_seed, "recovery_p99_ratio": ratio,
            "simultaneous_ratio_interval": interval,
            "per_scenario_ratio": {c: geometric([details[(s, JANUS, c)]["p99_ms"] / details[(s, baseline, c)]["p99_ms"]
                                                 for s in seeds]) for c in FAULTS},
        }
    for scenario in FAULTS:
        for policy in policies:
            d = [details[(s, policy, scenario)] for s in seeds]
            times = [x["readmission_s"] for x in d if x["readmission_s"] is not None]
            recovery_table[f"{scenario}/{policy}"] = {
                "median_readmission_s": statistics.median(times) if times else None,
                "never_readmitted": len(d) - len(times),
                "median_settled_share": statistics.median(x["settled_share"] for x in d),
                "median_p99_ms": statistics.median(x["p99_ms"] for x in d),
                "median_first_3s_p99_ms": statistics.median(x["first_3s_p99_ms"] for x in d),
            }
summary = {
    "verdict": verdict, "study": "exploratory recovery" if exploratory else "frozen",
    "recovery_seconds": recovery_seconds, "trials": len(rows), "expected_trials": grid, "seeds": len(seeds),
    "validity_findings": validity, "contrasts": contrasts,
    "recovery_contrasts": recovery_contrasts, "recovery": recovery_table,
    "measured_requests": sum(p["completed"] for r in rows for p in r["phases"]),
    "requests_including_warmup": sum(p["completed"] for r in rows for p in [r["warmup"], *r["phases"]]),
}
(folder / "analysis.json").write_text(json.dumps(summary, indent=2) + "\n")
print(json.dumps(summary, indent=2))

title = "Exploratory recovery study" if exploratory else "Benchmark result"
lines = [f"# {title}: Janus versus RR, LC, P2C and Peak EWMA", "", f"**{verdict}.**", ""]
if exploratory:
    lines += [f"Recovery phase: {recovery_seconds:g} s (frozen protocol: {FROZEN_RECOVERY_S} s). Janus is the",
              "working-tree build with the recovery changes, not the frozen binary, so these gates are a",
              "non-regression check, not a confirmatory result.", ""]
lines += [
         f"A baseline counts as beaten only if all three gates pass: the simultaneous upper bound of the",
         f"Janus/baseline p99 ratio is below {LATENCY_MARGIN} (a 15% advantage), the error-scenario failure",
         f"rate rises by at most {100 * FAILURE_MARGIN:.1f} points, and healthy achieved rate is at least",
         f"{THROUGHPUT_FLOOR:.0%} of the baseline's. Not beating a baseline is not proof of inferiority.", "",
         "## Integrity", "", f"Completed trials: {len(rows)} / {grid}.",
         f'Measured requests: {summary["measured_requests"]:,}; including warmup: {summary["requests_including_warmup"]:,}.',
         f"Validity findings: {len(validity)}.", ""]
if (folder / "completion.json").exists():
    duration = json.loads((folder / "completion.json").read_text())["elapsed_seconds"]
    lines += [f"Execution: {duration:.1f} seconds ({duration / 60:.1f} minutes).", ""]
if validity:
    lines += ["Findings, retained without filtering trials:", "", *[f"- {v}" for v in validity], ""]
lines += ["## Primary paired comparison", "",
          f"Ratios below one favor Janus. Intervals bootstrap the {len(seeds)} paired seeds (latency and CPU",
          f"faults kept together), Bonferroni-adjusted for {len(BASELINES)} baselines.", "",
          "| Baseline | Janus/baseline p99 ratio | Simultaneous interval | Failure-rate difference (pp) | Healthy rate ratio | Beaten |",
          "| --- | --- | --- | --- | --- | --- |"]
for b, c in contrasts.items():
    lo, hi = c["simultaneous_ratio_interval"]
    lines.append(f'| {NAMES[b]} | {c["primary_p99_ratio"]:.3f} | {lo:.3f}–{hi:.3f} | '
                 f'{100 * c["failure_rate_difference"]:+.3f} | {c["healthy_achieved_rate_ratio"]:.3f} | '
                 f'{"yes" if c["passed"] else "no"} |')
lines += ["", "## Scenario results", "",
          "Medians over trials. Failed requests remain in latency statistics. Proxy CPU is milliseconds",
          "per 1,000 completed requests; memory is end-of-phase current memory.", "",
          "| Scenario | Policy | p50 ms | p95 ms | p99 ms | Failures / requests | Proxy CPU ms / 1k | Memory MiB |",
          "| --- | --- | --- | --- | --- | --- | --- | --- |"]
if complete:
    for scenario in SCENARIOS:
        for policy in policies:
            phases = [measurement(s, policy, scenario) for s in seeds]
            med = lambda k: statistics.median(p[k] for p in phases)
            cpu = statistics.median((p["after"]["proxy"]["usage_us"] - p["before"]["proxy"]["usage_us"]) /
                                    p["completed"] for p in phases)
            memory = statistics.median(p["after"]["proxy"]["memory_bytes"] / 2**20 for p in phases)
            failures = sum(p["failures"] for p in phases)
            count = sum(p["completed"] for p in phases)
            lines.append(f'| {scenario} | {NAMES[policy]} | {med("p50_ms"):.2f} | {med("p95_ms"):.2f} | '
                         f'{med("p99_ms"):.2f} | {failures} / {count} | {cpu:.1f} | {memory:.1f} |')
lines += ["", "## Recovery", "",
          f"Reported without a decision rule. Ratios are Janus/baseline recovery-phase p99, geometric over",
          f"the latency, errors and CPU scenarios per seed, with the same paired bootstrap as above.", "",
          "| Baseline | Recovery p99 ratio | Simultaneous interval | latency | errors | cpu |",
          "| --- | --- | --- | --- | --- | --- |"]
for b, c in recovery_contrasts.items():
    lo, hi = c["simultaneous_ratio_interval"]
    per = c["per_scenario_ratio"]
    lines.append(f'| {NAMES[b]} | {c["recovery_p99_ratio"]:.3f} | {lo:.3f}–{hi:.3f} | '
                 f'{per["latency"]:.3f} | {per["errors"]:.3f} | {per["cpu"]:.3f} |')
lines += ["", f"Readmission: end of the first {WINDOW_S:g} s window in which the recovered pod got at least",
          f"{READMIT_SHARE:.0%} of requests (fair share 25%); medians over seeds that reached it. Settled share:",
          f"the recovered pod's share over the second half of the recovery phase. First {FROZEN_RECOVERY_S} s p99",
          "is comparable with the frozen protocol's recovery phase.", "",
          "| Scenario | Policy | Readmission s | Never readmitted | Settled share | Recovery p99 ms | First 3 s p99 ms |",
          "| --- | --- | --- | --- | --- | --- | --- |"]
for key, r in recovery_table.items():
    scenario, policy = key.split("/")
    readmit = "—" if r["median_readmission_s"] is None else f'{r["median_readmission_s"]:.1f}'
    lines.append(f'| {scenario} | {NAMES[policy]} | {readmit} | {r["never_readmitted"]} / {len(seeds)} | '
                 f'{r["median_settled_share"]:.1%} | {r["median_p99_ms"]:.2f} | {r["median_first_3s_p99_ms"]:.2f} |')
lines += ["", f"See {protocol_file} for the workload, estimator and decision rule.", ""]
(folder / "report.md").write_text("\n".join(lines))
sys.exit(0 if complete and not validity else 2)
