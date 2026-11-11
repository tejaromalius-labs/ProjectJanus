#!/usr/bin/env python3
"""Figures for a completed analysis (requires matplotlib). usage: plot.py RESULTS_DIRECTORY"""
import json
import pathlib
import statistics
import sys

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

POLICIES = ("janus", "rr", "lc", "p2c", "pewma")
LABELS = {"janus": "Janus", "rr": "RR", "lc": "LC", "p2c": "P2C", "pewma": "Peak EWMA"}
COLORS = {"janus": "#2463a6", "rr": "#777777", "lc": "#805ad5", "p2c": "#a54f24", "pewma": "#2f855a"}

root = pathlib.Path(sys.argv[1])
analysis = json.loads((root / "analysis.json").read_text())
rows = [json.loads(line) for line in (root / "trials.jsonl").read_text().splitlines()]
figures = root / "figures"
figures.mkdir(exist_ok=True)
plt.rcParams.update({"font.size": 10, "svg.fonttype": "none", "pdf.fonttype": 42})

fig, ax = plt.subplots(figsize=(7, 3.6), layout="constrained")
baselines = list(analysis["contrasts"])
for i, baseline in enumerate(baselines):
    c = analysis["contrasts"][baseline]
    value = c["primary_p99_ratio"]
    low, high = c["simultaneous_ratio_interval"]
    ax.errorbar(value, i, xerr=[[value - low], [high - value]], fmt="o", capsize=5, color=COLORS["janus"])
    per_seed = c["per_seed_primary_p99_ratios"]
    ax.scatter(per_seed, [i + .13] * len(per_seed), s=16, color="#777777", alpha=.7)
ax.axvline(1, color="black", linewidth=1, label="Equal p99")
ax.axvline(.85, color="#a54f24", linestyle="--", label="15% advantage margin")
ax.set_yticks(range(len(baselines)), [f"Janus / {LABELS[b]}" for b in baselines])
ax.set_xscale("log")
ax.set_xlabel("Paired geometric-mean p99 ratio (log scale; lower favors Janus)")
ax.set_title("Latency + CPU faults")
ax.grid(axis="x", alpha=.2)
ax.legend(loc="best", fontsize=8)
for extension in ("svg", "pdf", "png"):
    fig.savefig(figures / f"primary-comparison.{extension}", dpi=180)
plt.close(fig)

fig, axes = plt.subplots(1, 4, figsize=(12, 3.6), layout="constrained", sharey=True)
for ax, scenario in zip(axes, ("healthy", "latency", "errors", "cpu")):
    for i, policy in enumerate(POLICIES):
        values, flags = [], []
        for row in rows:
            if row["trial"]["policy"] != policy or row["trial"]["scenario"] != scenario:
                continue
            name = "healthy" if scenario == "healthy" else "fault"
            values.append(next(p for p in row["phases"] if p["name"] == name)["p99_ms"])
            flags.append(bool(row["invalid"]))
        if not values:
            continue
        ax.scatter([i + (j - len(values) / 2) * .035 for j in range(len(values))], values, color=COLORS[policy],
                   edgecolors=["#c53030" if f else COLORS[policy] for f in flags], linewidths=1.2, s=28)
        ax.plot([i - .22, i + .22], [statistics.median(values)] * 2, color=COLORS[policy], linewidth=2)
    ax.set_title(scenario.title())
    ax.set_xticks(range(len(POLICIES)), [LABELS[p] for p in POLICIES], rotation=30)
    ax.set_yscale("log")
    ax.grid(axis="y", alpha=.2)
axes[0].set_ylabel("Scheduled-arrival p99 latency (ms, log)")
fig.suptitle("Every trial; lines = medians; red outlines = retained validity flags")
for extension in ("svg", "pdf", "png"):
    fig.savefig(figures / f"scenario-tail-latencies.{extension}", dpi=180)
plt.close(fig)
