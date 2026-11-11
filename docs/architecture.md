# Janus technical reference: map

The technical reference used to be one 2,400-line file. It is now a set of short,
step-by-step pages in this folder. This page maps the **old section numbers** to the
**new pages**, so existing links and references still land in the right place.

Start at the [documentation index](README.md) if you are new.

| Old section | Topic | Now |
| --- | --- | --- |
| §1 Executive summary | What Janus is | [1. The big picture](01-the-big-picture.md) |
| §2 Why static load balancing fails | The problem and why LinUCB | [1. The big picture](01-the-big-picture.md) |
| §3 Mathematical framework | Score, update, Sherman-Morrison | [2. How Janus learns](02-how-janus-learns.md) |
| §4 Feature engineering | The 12 features | [3. What Janus measures](03-what-janus-measures.md) |
| §5 Reward function | Reward formula | [3. What Janus measures](03-what-janus-measures.md) |
| §6 Architecture overview | The request loop | [4. The life of a request](04-life-of-a-request.md) |
| §7 Startup flow | Flags and initialisation | [4. The life of a request](04-life-of-a-request.md) |
| §8 Pod discovery | Kubernetes and mock discovery | [5. Pods and their lifecycle](05-pods-and-lifecycle.md) |
| §9 Request routing | Candidate list, costs, fallback | [4. The life of a request](04-life-of-a-request.md) |
| §10 Learning and observation | Collector and worker | [4. The life of a request](04-life-of-a-request.md) |
| §11 Pod lifecycle state machine | WARMING, SHADOW, ACTIVE | [5. Pods and their lifecycle](05-pods-and-lifecycle.md) |
| §12 Non-stationarity and discounted LinUCB | Forgetting, idle growth, recovery fixes | [6. Adapting to change](06-adapting-to-change.md) |
| §13 Concurrency: RCU and epoch GC | Lock-free reads | [8. The Rust core and FFI](08-rust-core-and-ffi.md) |
| §14 FFI: Rust, C, Go | The CGO bridge | [8. The Rust core and FFI](08-rust-core-and-ffi.md) |
| §15 The Go proxy internals | ReverseProxy, timing, shutdown | [9. The Go proxy](09-go-proxy-internals.md) |
| §16 Telemetry pipeline | Collector, queue, worker, eBPF | [9. The Go proxy](09-go-proxy-internals.md) |
| §17 Circuit breaker | Three states, CAS | [7. Safety nets](07-safety-nets.md) |
| §18 Fallback policy | Least connections | [7. Safety nets](07-safety-nets.md) |
| §19 Feature store | Async cache, Prometheus inputs | [9. The Go proxy](09-go-proxy-internals.md) |
| §20 Kubernetes integration | Informer, RBAC | [5. Pods and their lifecycle](05-pods-and-lifecycle.md) |
| §21 Helm chart | Values, probes, resources | [10. Running Janus](10-running-janus.md) |
| §22 Testing | Rust and Go test suites | [12. Testing and benchmarking](12-testing-and-benchmarking.md) |
| §23 Quick start | Local, Kubernetes, Docker | [10. Running Janus](10-running-janus.md) |
| §24 Deployment guide | Helm, scaling, state, shutdown | [10. Running Janus](10-running-janus.md) |
| §25 Operational runbooks | Troubleshooting | [11. Troubleshooting](11-troubleshooting.md) |
| §26 Performance benchmarks and criteria | Targets and measured status | [12. Testing and benchmarking](12-testing-and-benchmarking.md) |
| §27 Risk register | Design risks | [Known issues and risks](known-issues.md) |
| §28 File reference | Source map | [13. Reference](13-reference.md) |
| §29 Glossary | Terms | [13. Reference](13-reference.md) |

## What changed when it was split

Besides the new tone, the rewrite corrected several things in the old text:

- **`/stats` is plain text, not JSON.** The JSON view is `/model`.
- **The readiness probe uses `/readyz`**, not `/healthz`.
- **The request-flow diagram** no longer says "select arm with max score"; routing is a soft cost, not an argmax.
- **The circuit breaker** description now includes its leaky-bucket error score.
- **The chart sets no `terminationGracePeriodSeconds` or `preStop` hook**; the Kubernetes default applies.
- **The retired `LinUCBLoadBalancer` CRD and reconciler** discussion was dropped; that code no longer exists.
- **Measured benchmark results** and their limits were added next to the acceptance targets.

The history of past studies is in [history.md](history.md), and the unresolved problems
are in [known-issues.md](known-issues.md).
