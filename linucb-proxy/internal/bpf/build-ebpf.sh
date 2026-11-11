#!/usr/bin/env bash
set -euo pipefail
# Requires clang, bpftool and libbpf-dev. Keep kernel-generated files outside Git.
dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
output=${1:-/tmp/janus-bpf}
mkdir -p "$output"
"${BPFTOOL:-bpftool}" btf dump file /sys/kernel/btf/vmlinux format c > "$output/vmlinux.h"
clang -g -O2 -target bpfel -I"$output" -c "$dir/socket_latency.c" -o "$output/socket_latency.o"
printf '%s\n' "$output/socket_latency.o"
