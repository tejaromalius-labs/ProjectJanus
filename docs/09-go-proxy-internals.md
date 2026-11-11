# 9. The Go proxy

**What you'll learn:** how the Go side is built: the reverse proxy and its hooks, how
latency is timed, how telemetry flows without blocking requests, and how the feature
store keeps slow lookups off the hot path.

---

## The reverse proxy

Janus is built on Go's `net/http/httputil.ReverseProxy`, which already handles
receiving the request, rewriting it for the upstream, forwarding it, copying the
response back and handling upstream errors. Janus plugs into four hooks:

| Hook | What Janus uses it for |
| --- | --- |
| `Rewrite(*ProxyRequest)` | Points the outbound request at the chosen pod. It sees `.In` (the original) and `.Out` (a mutable copy), so changes to one don't leak into the other |
| `Transport` | A custom `timingTransport` that measures upstream time |
| `ModifyResponse` | Fires after the upstream answers; emits the request outcome to telemetry |
| `ErrorHandler` | On upstream errors, returns `502 Bad Gateway` |

**Why `Rewrite` and not `Director`?** `Director` (now deprecated) mutates the request in
place, which causes subtle bugs when the same request is both sent upstream and
inspected later in `ModifyResponse`.

## Timing a request

```go
type timingTransport struct{ base http.RoundTripper }

func (t *timingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
    start := time.Now()
    resp, err := t.base.RoundTrip(r)
    if resp != nil {
        if v, ok := r.Context().Value(requestStartKey).(*time.Duration); ok {
            *v = time.Since(start)
        }
    }
    return resp, err
}
```

**What it measures:** from just before the upstream call until the **response headers**
arrive. That covers connecting, sending, the server's processing and receiving the
headers. It excludes client-to-proxy time and the body.

**Why a pointer in the context?** `ModifyResponse` runs after `RoundTrip` returns. A
duration is pre-allocated in the request context; `RoundTrip` writes it and
`ModifyResponse` reads it. That avoids a global map lookup on every request.

### Carrying data through the request

```go
ctx := context.WithValue(r.Context(), selectedPodKey, pod)
ctx = context.WithValue(ctx, requestStartKey, &upstreamDur)
p.rp.ServeHTTP(w, r.WithContext(ctx))
```

The context keys are private types (`ctxKey int`), so they can't collide with keys from
other packages. The chosen pod and the timer flow from `ServeHTTP` through `rewrite`,
`RoundTrip` and `modifyResponse` with no global state.

## Connection pooling

Go's default `MaxIdleConnsPerHost` is 2. Under load that forces new TCP connections
(about 1 ms each) and creates p99 spikes. Janus uses:

```go
base := &http.Transport{
    MaxIdleConns:        1024,
    MaxIdleConnsPerHost: 256,
    IdleConnTimeout:     60 * time.Second,
    ForceAttemptHTTP2:   true,
}
```

`ForceAttemptHTTP2` enables HTTP/2 when the backend supports it, which multiplexes many
requests over one connection. The benchmark's baseline proxies are configured with the
same pooling so the comparison is fair.

## Graceful shutdown

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

go func() {
    <-ctx.Done()
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    _ = srv.Shutdown(shutdownCtx)
}()
```

Kubernetes sends `SIGTERM`. `signal.NotifyContext` cancels a context, all background
goroutines select on `ctx.Done()` and exit, and `srv.Shutdown` stops accepting new
connections and waits up to 5 seconds for in-flight requests. Rolling deployments
shouldn't drop requests.

## `sync.Map` or a mutex?

`sync.Map` is optimised for keys written once and read many times, which describes the
pod registry and feature store: pods come and go rarely but are read on every routing
decision. Its read path is lock-free.

It's the wrong tool for the least-connections counter, which must scan all pods for the
minimum. That needs a `map` with a `sync.Mutex`.

---

## The telemetry pipeline

### 1. The reward path

After each upstream response:

```go
func (p *Proxy) modifyResponse(resp *http.Response) error {
    ctx := resp.Request.Context()
    pod, _ := ctx.Value(selectedPodKey).(*registry.PodEntry)
    if d, ok := ctx.Value(requestStartKey).(*time.Duration); ok {
        latencyMs = float64(d.Microseconds()) / 1000.0
    }
    p.observe(Outcome{PodID: pod.ID, LatencyMs: latencyMs, HTTPStatus: resp.StatusCode})
    return nil
}
```

### 2. The collector: a non-blocking send

```go
select {
case c.updates <- ArmUpdate{PodID: o.PodID, Reward: reward}:
    c.enq.Add(1)
default:
    c.dropped.Add(1)      // backpressure: drop rather than slow the request
}
```

The queue holds 4,096 updates. If it ever fills, the observation is dropped and
`telemetry_dropped` in `/stats` goes up. That counter is your alarm that the worker is
falling behind.

### 3. The update worker: one writer

A single goroutine applies all updates in order. The Rust engine allows concurrent
reads but wants writes to each arm serialised; one worker guarantees that with no
per-arm mutex:

```go
func (w *UpdateWorker) apply(u ArmUpdate) {
    entry, ok := w.reg.Get(u.PodID)
    if !ok { return }                       // pod left between request and reward

    f, _ := w.features.Get(u.PodID)
    x := proxy.BuildContextVector(f, u.Req)  // (uses the dispatch-time features)

    if err := w.engine.Update(entry.ArmID, x, u.Reward); err != nil { ... }
    w.reg.RecordObservation(u.PodID)         // drives lifecycle promotion
}
```

> **Trade-off:** one worker is simple and correct, but it is also a single point of
> serialisation, and the rolling-p99 calculation takes a global lock and sorts 256
> samples on every request. Under extreme bursts the queue can fill. See
> [known issues](known-issues.md).

### Optional: eBPF connection diagnostics

An optional `ebpf` build loads a BPF object (`-ebpf-object`), attaches to
`sock:inet_sock_set_state` and correlates TCP destinations with registered pods.
Completed-connection counts and lifetimes appear in `/metrics`. Connection lifetimes
**never** enter the reward, because they can't supply HTTP latency or status. Build the
object with `linucb-proxy/internal/bpf/build-ebpf.sh` (needs clang, bpftool and
libbpf-dev).

---

## The feature store

### The problem it solves

Prometheus scrapes take 100–500 ms. If routing waited for one, every request would pay
that. So the **hot path only reads a local cache**, and a background loop refreshes it.

```go
// hot path: microseconds
func (s *Store) Get(podID string) (PodFeatures, bool) {
    v, ok := s.features.Load(podID)
    if !ok { return PodFeatures{}, false }
    return v.(PodFeatures), true
}

// background, every 5 s by default
func (s *Store) StartRefreshLoop(ctx context.Context, interval time.Duration) {
    ticker := time.NewTicker(interval)
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            results, err := s.source.Scrape(ctx)
            if err != nil { continue }      // keep stale data on error
            for id, f := range results { s.features.Store(id, f) }
        }
    }
}
```

### Stale beats blocked

If a scrape fails, the store keeps the previous values. Slightly old inputs are better
than blocking on telemetry, and the model is robust to the odd stale context because
it converges from any sufficient stream of rewards.

### Where the numbers come from

```go
type Source interface {
    Scrape(ctx context.Context) (map[string]PodFeatures, error)
}
```

- `MockSource`: synthetic values for local development and tests.
- `PrometheusSource`: queries per-pod resource vectors through the Prometheus HTTP API.

Enable it with `-prometheus-url=http://prometheus:9090` (Helm: `proxy.prometheusURL`) and
optionally `-feature-interval=5s` (`proxy.featureInterval`). Without it, requests still
teach the model from latency and status, and the proxy logs a warning that resource
ingestion is off.

| Feature | Prometheus input |
| --- | --- |
| CPU utilisation | 1-minute CPU usage rate ÷ CPU limit (kube-state-metrics) |
| Memory saturation | Working-set bytes ÷ memory limit (kube-state-metrics) |
| Active connections | `janus_app_active_connections_ratio` (exported by your app) |
| Queue depth | `janus_app_queue_depth_ratio` (exported by your app) |
| GC pause fraction | 1-minute rate of `go_gc_duration_seconds_sum` |

Requirements and behaviour:

- Series must carry `pod` and `namespace` labels. Your app normalises its own ratios to its capacity.
- Missing series count as zero; `NaN` and `Inf` are ignored; finite values are clamped to `[0, 1]`.
- CPU and memory need configured limits plus cAdvisor and kube-state-metrics, **which Janus does not install**.
- Rolling p99: the collector keeps a nearest-rank p99 over the latest 256 samples no older than 1 s per pod. `P99LatencyMs` (in ms) drives promotion. `P99LatencyEMA` keeps its old name for compatibility but now carries that p99 divided by 10× the SLA, clamped to `[0, 1]`.

---

[← Previous: The Rust core and FFI](08-rust-core-and-ffi.md) · [Documentation index](README.md) · [Next: Running Janus →](10-running-janus.md)
