#!/usr/bin/env bash
set -euo pipefail
# Builds everything, copies it to a dedicated four-vCPU Multipass VM (cgroup v2,
# systemd, curl), runs a preflight trial, then the full campaign and analysis.
# usage: run.sh VM NEW_OUTPUT_DIRECTORY
bench=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(dirname "$bench")
vm=${1:?usage: run.sh VM NEW_OUTPUT_DIRECTORY}
output=${2:?usage: run.sh VM NEW_OUTPUT_DIRECTORY}
if [[ -e "$output" ]]; then printf 'Refusing to overwrite existing results: %s\n' "$output" >&2; exit 1; fi
mkdir -p "$output"
output=$(cd "$output" && pwd)
# Snap-confined Multipass can read neither /tmp nor hidden home directories.
staging=$(mktemp -d "$HOME/janus-bench-staging.XXXXXX")
remote=/home/ubuntu/janus-bench
cleanup() {
 multipass exec "$vm" -- sudo "$remote/vm/stop.sh" >/dev/null 2>&1 || true
 rm -rf -- "$staging"
}
trap cleanup EXIT

multipass info "$vm" > "$output/vm-info.txt"
address=$(multipass exec "$vm" -- hostname -I)
address=${address%% *}
# This run follows the exploratory recovery study; the frozen protocol
# (PROTOCOL.md, 3 s recovery) was run on 2026-11-10 with `-recovery=3s`.
cp "$bench/PROTOCOL-recovery.md" "$output/protocol.md"

# Janus is the production proxy binary built from the working tree.
(cd "$root/linucb-core" && cargo build --release --offline)
(cd "$root/linucb-proxy" && go test ./... && go build -o "$staging/proxy" ./cmd/proxy)
(cd "$bench" && go test ./... && for cmd in backend baseline agent driver; do go build -o "$staging/$cmd" "./cmd/$cmd"; done)
tar -C "$bench" -czf "$staging/vm.tar.gz" vm

multipass exec "$vm" -- mkdir -p "$remote"
for name in proxy baseline backend agent vm.tar.gz; do
 multipass transfer "$staging/$name" "$vm:$remote/$name"
 expected=$(sha256sum "$staging/$name"); expected=${expected%% *}
 actual=$(multipass exec "$vm" -- sha256sum "$remote/$name"); actual=${actual%% *}
 if [[ $expected != "$actual" ]]; then printf 'Checksum mismatch: %s\n' "$name" >&2; exit 1; fi
done
(cd "$staging" && sha256sum proxy baseline backend agent driver vm.tar.gz) > "$output/binary-sha256.txt"
multipass exec "$vm" -- bash -lc "set -e; cd $remote; tar -xzf vm.tar.gz; chmod +x proxy baseline backend agent vm/*.sh; sudo vm/stop.sh; sudo vm/setup.sh $remote"

timeout --signal=TERM --kill-after=5s 3m "$staging/driver" -host "$address" -out "$output/preflight" -preflight > "$output/preflight.log" 2>&1
python3 - "$output/preflight/trials.jsonl" <<'PY'
import json, sys
for line in open(sys.argv[1]):
    row = json.loads(line)
    if row["invalid"]:
        raise SystemExit(f"Preflight failed: {row['invalid']}")
PY

run_status=0
timeout --signal=TERM --kill-after=10s "${JANUS_BENCH_TIMEOUT:-180m}" "$staging/driver" -host "$address" -out "$output/canonical" -recovery=30s > "$output/campaign.log" 2>&1 || run_status=$?
# Analyze incomplete runs too; failures stay in the evidence.
analysis_status=0
python3 "$bench/analysis/analyze.py" "$output/canonical" > "$output/analysis.log" || analysis_status=$?
if ((run_status != 0)); then exit "$run_status"; fi
exit "$analysis_status"
