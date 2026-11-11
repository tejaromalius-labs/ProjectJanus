# 1. The big picture

**What you'll learn:** what problem Janus tackles, why the simple alternatives fall
short, and what a "contextual bandit" actually is, in plain words.

---

## The problem in one story

Imagine three checkout lanes at a supermarket and one person at the front deciding
which lane each shopper joins.

- **Round robin** says: lane 1, lane 2, lane 3, lane 1, and so on, whatever happens.
- **Least connections** says: join the lane with the fewest people in it.

Both are sensible. Both break the same way: one cashier gets slow. Perhaps they are
new, or their till keeps freezing. Round robin keeps sending them a third of the
shoppers. Least connections notices only when the queue grows, and a cashier who is
slow *but steady* can look perfectly fine by that measure.

In a Kubernetes cluster the "cashier" is a backend pod, and "slow" has many causes:
a noisy neighbour stealing CPU, a garbage-collection pause, a saturated thread pool,
a flaky network path, or a half-failed dependency.

## What the simple policies can't see

| Policy | Good at | Blind to |
| --- | --- | --- |
| **Round robin** | Fairness, zero overhead | Everything about backend health |
| **Least connections** | Reacting to queue build-up | Latency, errors, CPU pressure; a pod that is slow but steady looks fine |
| **P2C / Peak EWMA** | Reacting to load and recent latency | Request type, error rate and pod resource state |

All of these are close to **memoryless**: they can't tell a pod that is
consistently 5 ms from one that is *occasionally* 500 ms, and they react late to
gradual degradation.

## What Janus does differently

Janus keeps a small statistical model for every backend. After each response it
asks, "how did that go?", and updates that pod's model. Before each request it asks
each pod's model, "how well do you expect to do right now?", and steers traffic
accordingly.

Two words you'll see everywhere:

- **Arm.** A choice you can make. Here, one backend pod. (The name comes from a
  slot machine with several arms; more below.)
- **Reward.** A number from 0 to 1 saying how good an outcome was. Fast and
  successful is high; a server error is 0.

## What is a "bandit"?

You walk into a casino with several slot machines. Each pays out differently and you
don't know which is best. You face a tension:

- **Exploit:** keep pulling the machine that has paid best so far.
- **Explore:** try the others, because one of them might be better and you
  haven't seen enough yet.

That is the **multi-armed bandit** problem, and algorithms for it are very good at
balancing the two. Load balancing has the same shape: each request is a pull, and
each pod is an arm.

A **contextual** bandit adds one thing: the right choice depends on the situation. A
pod might be fast for reads and slow for writes, or fast at 3 a.m. and slow at
noon. "Context" is the set of facts about the situation (CPU load, payload size,
time of day) that the model uses to predict the reward.

## Why LinUCB?

Several bandit algorithms exist. We chose **LinUCB** (Linear Upper Confidence Bound)
because it fits the constraints of a load balancer:

| Alternative | Why we didn't use it |
| --- | --- |
| ε-greedy | The exploration rate is fixed and blind; it isn't tied to how uncertain we actually are |
| Thompson sampling | Needs a full Bayesian posterior per arm; costly and harder to audit |
| Neural bandits | Need heavy inference, which doesn't fit a 1.5 ms routing budget |
| **LinUCB** | A tiny update (a few hundred operations at 12 features), an exact measure of its own uncertainty, one tuning knob, and an entire model per pod that fits in about a kilobyte |

The next page shows how it works.

## What Janus is made of

| Piece | Language | Role |
| --- | --- | --- |
| `linucb-core/` | Rust | The maths: scoring, updates, lock-free state |
| `linucb-proxy/` | Go | The HTTP reverse proxy, pod discovery, telemetry |
| `helm-chart/` | Helm | Kubernetes deployment |
| `benchmark/` | Go | The test harness that compares Janus with four baselines |

Rust and Go are linked into **one binary** using CGO. You deploy one container; it
sits in front of your backends, receives requests and decides where each goes.

## What this is not

Janus is a **single-proxy router**, not a service mesh. Each replica learns on its
own and nothing is shared between replicas or saved across restarts. It has no TLS
termination and no retries. See [known issues](known-issues.md) for the full list.

---

[← Documentation index](README.md) · [Next: How Janus learns →](02-how-janus-learns.md)
