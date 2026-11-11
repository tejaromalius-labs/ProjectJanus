//go:build ignore

// eBPF socket-lifecycle latency monitor for Project Janus.
//
// Hooks TCP state transitions to measure connection-level latency without
// per-request syscall overhead, emitting events to a BPF ring buffer that the
// Go userspace collector consumes (preferred over a perf buffer on kernels
// >= 5.8; this project targets 6.17).
//
// CO-RE: compile against the generated vmlinux.h with `clang -g -O2 -target bpf`.
// Generated via bpf2go (see gen.go). Requires CAP_BPF / privileged runtime;
// deployment is gated behind the `ebpf` build tag.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>

char LICENSE[] SEC("license") = "GPL";

struct event_t {
    __u64 start_ns;
    __u64 end_ns;
    __u8 daddr[16];
    __u16 dport;
    __u16 family;
    __u32 padding;
};

// Ring buffer for events → userspace (BPF_MAP_TYPE_RINGBUF, kernel >= 5.8).
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20); // 1 MiB
} events SEC(".maps");

// Per-socket start timestamp, keyed by socket cookie.
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 65536);
    __type(key, __u64);
    __type(value, __u64);
} start_ts SEC(".maps");

// Record connection start when a socket enters ESTABLISHED.
SEC("tracepoint/sock/inet_sock_set_state")
int handle_set_state(struct trace_event_raw_inet_sock_set_state *ctx) {
    if (ctx->newstate == TCP_ESTABLISHED) {
        __u64 cookie = (__u64)ctx->skaddr;
        __u64 ts = bpf_ktime_get_ns();
        bpf_map_update_elem(&start_ts, &cookie, &ts, BPF_ANY);
    } else if (ctx->newstate == TCP_CLOSE) {
        __u64 cookie = (__u64)ctx->skaddr;
        __u64 *start = bpf_map_lookup_elem(&start_ts, &cookie);
        if (!start)
            return 0;

        struct event_t *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
        if (!e) {
            bpf_map_delete_elem(&start_ts, &cookie);
            return 0;
        }
        e->start_ns = *start;
        e->end_ns = bpf_ktime_get_ns();
        e->dport = ctx->dport;
        e->family = ctx->family;
        e->padding = 0;
        __builtin_memset(e->daddr, 0, sizeof(e->daddr));
        if (ctx->family == 2)
            bpf_probe_read_kernel(e->daddr, 4, ctx->daddr);
        else if (ctx->family == 10)
            bpf_probe_read_kernel(e->daddr, 16, ctx->daddr_v6);
        bpf_ringbuf_submit(e, 0);
        bpf_map_delete_elem(&start_ts, &cookie);
    }
    return 0;
}
