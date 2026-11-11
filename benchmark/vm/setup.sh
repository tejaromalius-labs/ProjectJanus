#!/usr/bin/env bash
set -euo pipefail
# Run inside the dedicated VM as root. CPU 0: agent and system, CPU 1: proxy
# under test (started per trial by the agent), CPUs 2-3: four backends at 0.25 CPU each.
work=${1:?usage: setup.sh WORK_DIRECTORY}
mkdir -p "$work/samples"
for port in 19001 19002 19003 19004; do
 rm -f -- "$work/samples/backend-$port.json" "$work/samples/backend-$port.json.tmp"
 systemd-run --quiet --unit="janus-bench-backend-$port" --property=CPUQuota=25% --property=AllowedCPUs=2,3 \
  --property=MemoryMax=128M --property=Environment=GOMAXPROCS=1 --property=Environment=GOMEMLIMIT=96MiB \
  -- "$work/backend" -port "$port" -dir "$work/samples"
done
systemd-run --quiet --unit=janus-bench-agent --property=AllowedCPUs=0 \
 -- "$work/agent" -dir "$work/samples" -bin "$work"
for ((i=0; i<40; i++)); do
 ready=true
 for port in 19001 19002 19003 19004; do
  if ! curl -fsS "http://127.0.0.1:$port/ready" >/dev/null 2>&1 || [[ ! -s "$work/samples/backend-$port.json" ]]; then ready=false; fi
 done
 curl -fsS http://127.0.0.1:18090/snapshot >/dev/null 2>&1 || ready=false
 if [[ $ready == true ]]; then exit 0; fi
 sleep .25
done
exit 1
