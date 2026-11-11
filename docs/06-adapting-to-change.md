# 6. Adapting to change

**What you'll learn:** why a model that learns can also get stuck in its ways, how
Janus forgets on purpose, and the three recovery fixes that let a healed pod earn
its traffic back. This is the page where most of the bugs were found.

---

## The problem: a model with a long memory

Standard LinUCB assumes the world doesn't change. Real pods do. Consider:

1. A pod serves 70,000 fast requests. Its model is now very confident, so its
   exploration bonus is almost zero.
2. Something makes it slow (say, 500 ms of added delay).
3. Each new bad reward nudges the estimate only a tiny amount, because 70,000 good
   observations outweigh it. It takes thousands of bad ones to change the model's mind.

This was seen directly in testing: after minutes of 500 ms delay, the model was still
sending over 99% of traffic to the degraded pod. This is called **model inertia**.

## The fix: forget on purpose (discounted LinUCB)

Introduce a **forgetting factor** `γ` ("gamma"), a number just under 1. On every update,
old evidence is multiplied by `γ` before the new data is added:

```
A_new = γ · A + x xᵀ
b_new = γ · b + r · x
```

An observation from `k` updates ago now carries weight `γᵏ`. The effective memory is:

```
effective window ≈ 1 / (1 − γ) observations
```

| γ | Window | Behaviour |
| --- | --- | --- |
| 1.0 | forever | Never forgets (inertia returns) |
| 0.9999 | ~10,000 | Very stable, slow to adapt |
| **0.999** | **~1,000** | **Default.** Adapts in seconds at 200 req/s |
| 0.995 | ~200 | Quick, may oscillate |
| 0.99 | ~100 | Very reactive, may oscillate |

Memory in *time* depends on traffic: `window / requests-per-second`. At 1,000 req/s
and γ = 0.999 that is about one second; at 10 req/s it is 100 seconds.

**Starting advice:** keep 0.999. If routing wobbles when nothing is wrong, move toward
0.9999. If Janus is slow to react to real faults, move toward 0.995.

### The matching update

Discounting changes Sherman-Morrison's denominator from `1 + xᵀA⁻¹x` to
`γ + xᵀA⁻¹x`:

```rust
let v = &self.a_inv * x;
let denom = gamma + x.dot(&v);          // γ + xᵀA⁻¹x, not 1 + xᵀA⁻¹x
// ... guard: if denom <= 1e-10, reset A⁻¹ to the identity
let a_inv_new = (1.0 / gamma) * (&self.a_inv - (&v * v.transpose()) / denom);
let b_new = gamma * &self.b + reward * x;
```

**Allowed range:** `γ ∈ [0.99, 1.0]`. Below 0.99 the `(1/γ)ⁿ` growth overflows after
about a thousand updates, so the engine refuses to start (it returns a null handle).

## Side effects of forgetting, and their cures

Forgetting solves inertia but introduces three new problems. Each has a fix.

### Problem 1: the prior fades too ("windup")

Discounting scales away the starting identity matrix as well. In directions where the
context never varies (the hour features within a single hour, a feature that stays
zero) `A⁻¹` therefore grows like `γ⁻ⁿ`: about 10¹³ after 30,000 updates at γ = 0.999.
The result was UCB scores of 19 within the same hour, and 130,000 when the hour changed.

**Fix:** each discounted update adds a second tiny term, `d·(1−γ)·e_k·e_kᵀ`, cycling
through the directions (`k = n_obs mod d`). On average this restores `(1−γ)·I` and
keeps `A⁻¹` below about 1. `tests/numerical_stability.rs` checks it.

### Problem 2: an unchosen pod's score freezes ("idle growth")

A pod is only updated when it is chosen. If its score drops, it stops being chosen,
so it never updates, so its score never changes. It is stuck.

**Fix:** at scoring time, the exploration bonus of a pod idle for `k` engine updates is
widened by `γ^(−k/2)` (capped at 1, the reward range). This is what discounting every
arm on every update would have done. The estimate `θ` is unchanged; only the "benefit
of the doubt" grows. At γ = 0.999 the width grows e-fold every 2,000 updates, so a pod
that is 0.5 below the best gets looked at again after about 8,300 updates.

### Problem 3: the recovery lockout (three bugs, fixed 2026-11-10)

The first frozen campaign showed that after a fault ended, the recovered pod got
**0% of traffic for the whole 3-second recovery window** (latency fault) or swung
between 0% and 33% every half second (CPU fault). Three separate causes were found:

| # | Cause | Fix |
| --- | --- | --- |
| 1 | **Stale latency feature.** The rolling p99 only changes when a pod *answers*. Ten slow samples in a 256-sample window need about 254 fresh responses to flush. A pod with a low score gets no traffic, so it gets no responses: a lockout | Ignore samples older than **1 s** (`featurestore.LatencyMaxAge`), and reset the feature to 0 after 1 s without a response. Staleness is bounded by 1 s plus the refresh interval |
| 2 | **Idle growth only widened the score.** The next update applied `γ` once, so a fresh observation after a long idle gap weighed no more than if there had been no gap | **Idle catch-up:** before the next update, scale `A⁻¹` by `min(γ^(−k), 100)` and `b` by the inverse (so `θ` is unchanged), then apply the normal update. The cap bounds windup |
| 3 | **Step-function penalty.** The `2^⌊gap/0.05⌋` rule made a pod's share jump whenever its gap crossed a step edge, so a recovering pod's share oscillated | **Dead zone plus random rounding** (see [page 4](04-life-of-a-request.md)) |

The first fix has a **cost** worth knowing: a pod that is *still slow* now gets a
probe request roughly every 1.5 seconds on the benchmark (up to about 6 s at the 5 s
default refresh interval). If probes grew past about 1% of requests, fault-phase p99
would rise. In the benchmark it did not.

### Did it work?

In the exploratory 2026-11-11 run, with a 30-second recovery phase:

| | Janus | RR / LC / P2C | Peak EWMA |
| --- | --- | --- | --- |
| Time for the recovered pod to reach 20% of traffic (latency fault) | **2.25 s** | 1.0 s | 24.4 s |
| Same, CPU fault | **1.55 s** | 1.0 s | 21.8 s |
| Same, errors fault | 1.0 s | 1.0 s | 1.0 s |
| Settled share of the recovered pod | 0.249–0.250 | 0.248–0.252 | 0.18 (latency), 0.21 (CPU) |

So Janus now recovers to a fair share, but still more slowly than the simple policies
at first. Recovery p99 matches RR, LC and P2C (about 4 ms). **This run is exploratory:**
the fixes were written after seeing the first campaign's results and have been tested
once. See [history, part 2](history/04-frozen-campaign-and-recovery.md).

### Things we considered and didn't do

- **A minimum exploration share per pod**, to guarantee some traffic always reaches a pod.
- **A change-detection reset** (M-UCB or GLR) that restarts a pod's model when a shift is detected.
- **Lowering γ:** rejected, because it adds variance to every healthy decision.

### The limit set by γ itself

With γ = 0.999 an estimate needs about 693 updates to move halfway to a new mean. In
a 3-second window at 150 requests per second per pod that is about 450 updates, so
recovery speed is limited by design. The 30 s window used in the second study
doesn't have that limitation.

## How γ is wired through the system

The forgetting factor passes through five layers, so you know where to look:

1. `linucb-core/src/arm.rs`: `ArmState::update_with_gamma`
2. `linucb-core/src/rcu.rs`: `ArmSlot::update`
3. `linucb-core/src/ffi.rs`: `linucb_engine_new_with_gamma` (validates `0.99 ≤ γ ≤ 1.0`)
4. `linucb-proxy/internal/linucb/binding.go`: `NewEngineWithGamma`
5. `linucb-proxy/cmd/proxy/main.go`: the `-gamma` flag

---

[← Previous: Pods and their lifecycle](05-pods-and-lifecycle.md) · [Documentation index](README.md) · [Next: Safety nets →](07-safety-nets.md)
