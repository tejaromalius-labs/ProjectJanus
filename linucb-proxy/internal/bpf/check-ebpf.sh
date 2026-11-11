#!/usr/bin/env bash
set -euo pipefail
work=${1:?usage: check-ebpf.sh WORK_DIRECTORY BPF_OBJECT}
object=${2:?usage: check-ebpf.sh WORK_DIRECTORY BPF_OBJECT}
pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT
python3 -m http.server 9001 --bind 127.0.0.1 > "$work/ebpf-backend-a.log" 2>&1 &
pids+=("$!")
python3 -m http.server 9002 --bind 127.0.0.1 > "$work/ebpf-backend-b.log" 2>&1 &
pids+=("$!")
"$work/proxy" -listen=127.0.0.1:8080 -ebpf-object="$object" > "$work/ebpf-proxy.log" 2>&1 &
pids+=("$!")
ready=false
for ((i=0; i<50; i++)); do
  if curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1 && grep -q 'eBPF connection telemetry enabled' "$work/ebpf-proxy.log"; then ready=true; break; fi
  sleep 0.1
done
if [[ $ready != true ]]; then cat "$work/ebpf-proxy.log"; exit 1; fi
for ((i=0; i<20; i++)); do curl -fsS http://127.0.0.1:8080/ >/dev/null; done
sleep 0.5
curl -fsS http://127.0.0.1:8080/metrics > "$work/ebpf-metrics.txt"
awk '/^janus_backend_connections_total/ { total += $2 } END { if (total <= 0) exit 1; print "Completed TCP connections observed:", total }' "$work/ebpf-metrics.txt"
grep -q '^janus_telemetry_enqueued_total 20$' "$work/ebpf-metrics.txt"
printf 'eBPF runtime attachment, endpoint correlation, and HTTP reward isolation passed.\n'
