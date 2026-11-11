# September: the first benchmark (Janus vs round robin and least connections)

**In one sentence:** Janus won on isolated faults, but showed a capacity problem and
shaky error handling that we had to fix before it could be trusted.

**Why this chapter matters:** this is where most of the safety features (the soft
score penalty, the circuit breaker, "any 5xx gives reward 0") came from. Each one
exists because something broke here.

## September: Janus vs RR and LC

Single Multipass VM running kind, two Python backends at 0.5 CPU each, proxy at
1 CPU, k6 load. 105 trials, 2.08 M measured requests. Backends were forced
ACTIVE after 1,000 round-robin training requests each.

- **Latency fault (+200 ms on one backend):** fault p99 4.0 ms vs RR 204 ms and
  LC 202 ms; Janus sent 0.3% of traffic to the slow backend. Won 5/5 blocks.
- **CPU fault:** p99 7.3 ms vs RR 1,057 ms and LC 38 ms (median), but one block
  reached 1,481 ms, worse than RR. Mean reduction vs RR 71%, 95% interval 16–99%.
- **503 fault:** inconsistent. Janus failures per block 1,376 / 2,439 / 2 / 112 /
  585 vs RR 1,500 every block; only 1/5 blocks converged.
- **Capacity:** Janus passed 600–700 RPS; a plain reverse proxy and RR inside
  the same Janus proxy both passed 1,200 RPS. The "<3% throughput loss" target failed.
- **Lifecycle:** a new pod took ~55 s to reach SHADOW and ~95 s to reach ACTIVE
  at 390 RPS. The 1% warmup budget contradicted a documented "0% to WARMING" goal.
- Sherman–Morrison update: 436 ns (target <50 µs); numerical stability held over 100k updates.

**Decisions that followed:**

| Finding | Cause found | Change |
| --- | --- | --- |
| Capacity collapse | Healthy pods scored almost identically; argmax sent 68–89% of traffic to one pod until it saturated, and rewards only arrive after completion | Equivalence band: pods within 0.05 of the best score share traffic by fewest in flight |
| Erratic 503 handling | A 5xx still earned +0.10 (CPU term); the circuit breaker was not wired in and reset on every success | Any 5xx gives reward 0; leaky-bucket breaker wired into every routing path; single-probe half-open |
| 0% vs 1% warmup | The 1% budget is what breaks the cold-start deadlock | Kept 1%; documentation changed to "≤ warmup budget" |

Left open: the CPU-fault outlier block, an unexplained +139% Criterion change
against an old local baseline, and 7,296 requests in one phase that the proxy's
hit counters did not attribute.

---

← [History index](../history.md) · [Next →](02-october-studies.md)
