#!/usr/bin/env bash
set -euo pipefail
# Run inside the VM as root. Stops only the benchmark's own units.
for name in janus-bench-proxy janus-bench-agent janus-bench-backend-19001 janus-bench-backend-19002 janus-bench-backend-19003 janus-bench-backend-19004; do
 systemctl stop "$name.service" 2>/dev/null || true
 systemctl reset-failed "$name.service" 2>/dev/null || true
done
