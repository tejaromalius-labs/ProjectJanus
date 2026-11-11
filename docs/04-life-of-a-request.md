# 4. The life of a request

**What you'll learn:** what happens when Janus starts up, then how a single request
is routed, answered and learned from, step by step. This is the most useful page for
understanding the code.

---

## The picture first

```
 request arrives
      │
      ▼
 1. Who is available?       (every pod whose circuit breaker is closed)
      │
      ▼
 2. Is any pod brand new?   (fewer than 20 observations → try it first)
      │
      ▼
 3. Score each pod          (the LinUCB formula)
      │
      ▼
 4. Turn scores into costs  (lower score = "looks busier")
      │   pick the cheapest pod; break ties at random
      ▼
 5. Too slow or nothing to choose?  → fall back to least connections
      │
      ▼
 6. Occasionally (~5%) send to a SHADOW pod instead
      │
      ▼
 forward the request ──▶ pod answers
      │
      ▼
 7. Measure it, compute the reward, queue a model update (never blocks the response)
      │
      ▼
 8. Background worker updates that pod's model
```

## Before any request: startup

When you run `./proxy -listen=:8080 -alpha=0.15 -gamma=0.999`, Janus does six things
in order:

1. **Parse flags.** The full list is on [page 10](10-running-janus.md).
2. **Create the LinUCB engine** (Rust). Each pod will get an identity-matrix `A⁻¹` and a zero `b`.
3. **Create the registry** (every known pod, its arm, its state) **and the feature store** (the cache of per-pod context numbers).
4. **Create the telemetry pipeline.** A collector turns responses into rewards and drops them in a 4,096-item queue; a worker drains it. If the queue ever fills, new observations are dropped rather than slowing a request. Losing 1% of updates costs negligible accuracy.
5. **Start background loops.** A promotion check every second, a feature refresh every 5 seconds (by default), and the update worker.
6. **Start discovery.** With `-service`, Janus watches Kubernetes for pods. Without it, it uses mock backends from `-backends` (default: `backend-a` on `127.0.0.1:9001` and `backend-b` on `:9002`).

## Step by step: one request

Code: `linucb-proxy/internal/proxy/proxy.go`.

### Step 1: gather the candidates

Janus lists every pod whose circuit breaker is not open. It scores **all** of them,
whatever their lifecycle state (WARMING, SHADOW or ACTIVE). Lifecycle state controls
*promotion*, not who may receive traffic. [Page 5](05-pods-and-lifecycle.md) explains why.

### Step 2: give new pods their first 20

If any candidate has fewer than 20 observations, Janus picks the least-observed one
(random ties) and stops. This is the "minimum exploration" rule from page 2.

### Step 3: score everyone

For each remaining pod, compute `score = xᵀθ + α·√(xᵀA⁻¹x)` using that pod's context.
This reads the model without locking anything (details on [page 8](08-rust-core-and-ffi.md)).

### Step 4: turn scores into costs and pick

This is the part that changed most over the project's history, so it is worth
reading slowly.

A lower score is a **load penalty**, not an exclusion. For each pod:

```
gap   = (best score − this pod's score) / 0.05          (0.05 is "ScoreDelta")
steps = 0                                  if gap < 1
steps = ⌊ gap − 1 + U ⌋   (U random in [0,1))   otherwise
cost  = (requests in flight + 1) · 2^steps
```

Janus picks the pod with the **lowest cost**, breaking ties at random.

In words:

- A pod within 0.05 of the best is treated as just as good, so it competes purely on
  **fewest requests in flight**.
- Each further 0.05 of gap roughly **doubles** how busy the pod looks, so it gets
  about half as much traffic. A pod with a gap like a +200 ms fault (about 0.57)
  looks over 1,000 times as loaded and gets almost nothing.
- Because it is only a penalty, noisy scores can't herd all traffic onto one pod.

### Why this exact rule? Three lessons learned the hard way

1. **Soft penalty, not a hard cutoff.** An earlier version only allowed pods within
   0.05 of the best. When healthy pods' scores were noisy, it picked one pod and
   overloaded it, and the excluded pods never got updated again.
2. **Round down, and break ties at random.** With smooth gaps, equally loaded pods
   almost never tie, so the best-scoring pod took every request that found it idle:
   about 260 req/s on a pod that can serve 250, which then got CPU-throttled. Treating
   small gaps as noise fixed that.
3. **Random rounding beyond the first step.** Plain rounding made a pod's share jump
   whenever its gap crossed a step edge. A recovering pod swung between about 0% and
   33% of traffic every half second. Random rounding makes the *expected* penalty
   smooth. The cost is that every penalty is one step (2×) weaker. The dead zone
   under 0.05 stays, because randomising from zero would split idle traffic 70/30
   between a pod 0.02 below the best and the best one, undoing lesson 2.

### Step 5: fall back if needed

Two cases send the request to a plain **least-connections** fallback instead:

- The LinUCB decision took longer than **1.5 ms**.
- Nothing qualified to be chosen.

The fallback is counted in `/stats`, so you can see how often it happens.

### Step 6: shadow traffic

About 5% of requests (`ShadowTrafficCapPct`) are sent to a SHADOW pod via
least-connections, if there is one. This gives proving-ground pods a steady stream
of traffic to be judged on.

### Step 7: forward, then observe

The request is proxied as-is to the chosen pod. When the response headers come back,
Janus records:

- the HTTP status,
- the latency (from just before sending upstream to when the response headers arrive; client-side network time is excluded),
- which pod answered.

It hands that to the collector and returns the response to the client straight away.

### Step 8: learn, in the background

The collector does three quick things:

1. **Update the pod's rolling p99** (the last 256 samples that are no older than 1 s) and write it to the feature store.
2. **Compute the reward** with the formula from page 3.
3. **Enqueue an update** without blocking. A full queue drops the observation.

A single background worker then takes each update and:

1. Finds the pod's arm,
2. Rebuilds the context from the **features captured when the request was dispatched** (not the current ones, since the model should learn from the situation it actually chose in),
3. Calls the Rust engine's discounted Sherman-Morrison update,
4. Increments the pod's observation count,
5. Copies the pod's p99 into the registry and refreshes the fleet median, which the promotion gate on page 5 relies on.

Only this one worker writes model state. That is how Janus avoids a lock around every
arm's update.

## What it looks like from outside

```bash
curl localhost:8080/stats    # example output below; your numbers will differ
```

```text
linucb=145632 fallback=12 shadow=7280
pod=backend-a state=ACTIVE nobs=4521 hits=145632
pod=backend-b state=ACTIVE nobs=4389 hits=3521
requests=149000 completed=149000 errors=0 telemetry_enqueued=148990 telemetry_dropped=10 telemetry_queued=0
```

- `linucb` / `fallback` / `shadow`: how many decisions each path made.
- `nobs`: how many observations trained that pod's model.
- `hits`: how many requests it was routed.
- `telemetry_dropped`: observations lost to a full queue. If this grows, the worker is falling behind.

`/model` returns the same pods as JSON with each pod's current UCB score.

---

[← Previous: What Janus measures](03-what-janus-measures.md) · [Documentation index](README.md) · [Next: Pods and their lifecycle →](05-pods-and-lifecycle.md)
