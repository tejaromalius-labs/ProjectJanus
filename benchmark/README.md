# Benchmark: Janus vs RR, LC, P2C and Peak EWMA

A black-box harness. It runs the unmodified Janus proxy binary and a small
baseline proxy under identical CPU limits, sends the same traffic to both, and
compares tail latency, failures and throughput. It does not import any Janus
code. [PROTOCOL.md](PROTOCOL.md) is the frozen design and decision rule (run
2026-11-10); [PROTOCOL-recovery.md](PROTOCOL-recovery.md) is the exploratory
recovery study that `run.sh` now runs.

## Layout

| Path | Role |
| --- | --- |
| `internal/policy/` | The four baselines: `rr`, `lc`, `p2c`, `pewma` |
| `cmd/baseline/` | Reverse proxy running one baseline policy |
| `cmd/backend/` | Synthetic CPU-limited backend with fault modes |
| `cmd/agent/` | In-VM control: starts/stops the proxy under test, serves backend metrics to Janus through a Prometheus-compatible API, reports cgroup snapshots |
| `cmd/driver/` | Host-side load generator and trial loop |
| `vm/` | Start/stop the backend and agent units inside the VM |
| `run.sh` | Build, copy to the VM, preflight, campaign, analysis |
| `analysis/` | `analyze.py` (verdict per baseline, recovery metrics), `plot.py` (figures, needs matplotlib) |

## Run

Requires a dedicated Multipass VM with cgroup v2, systemd and curl, and the
build prerequisites in the root README. The frozen protocol uses:

```bash
multipass launch 24.04 --name janus-bench --cpus 4 --memory 2G --disk 8G
```

```bash
benchmark/run.sh janus-bench /absolute/path/to/NEW_RESULTS_DIRECTORY
python3 benchmark/analysis/plot.py /absolute/path/to/NEW_RESULTS_DIRECTORY/canonical
```

`run.sh` refuses to overwrite an existing directory, runs one short preflight
trial and stops if it is flagged, then runs the full grid with a 30 s recovery
phase and copies `PROTOCOL-recovery.md` to `protocol.md`. To reproduce the
frozen campaign (2026-11-10) used a 3 s recovery phase (`-recovery=3s`) and the
Janus code from before the recovery changes. Set
`JANUS_BENCH_TIMEOUT` (default `180m`) to change the campaign limit. Analysis
exits with status 2 when the grid is incomplete or any trial is flagged; it
still writes `analysis.json` and `report.md`. A run with a recovery phase longer
than 3 s is labelled exploratory: the frozen gates are reported as a
non-regression check, followed by the recovery metrics.

## Results

Raw data and analysis are under `runs/`: `runs/20261110/` is the frozen campaign
(3 s recovery) and `runs/20261111/` is the exploratory recovery study (30 s).
Each holds `canonical/trials.jsonl`, per-phase CSVs, `analysis.json` and
`report.md`. Summary tables and limits are in the root README and
[history, part 2](../docs/history/04-frozen-campaign-and-recovery.md).

## Tests

```bash
cd benchmark && go vet ./... && go test ./...
```
