# 10. Running Janus

**What you'll learn:** how to build and run Janus on your laptop, in Docker and on
Kubernetes; what every setting does; how to tune it; and what happens when pods restart.

---

## Before you start

| You want to… | You need |
| --- | --- |
| Run locally | Go 1.24+, Rust 1.75+, CGO enabled |
| Use Kubernetes | `kubectl`, a cluster (Kubernetes 1.24+), Helm 3 |
| Build the image | A Docker daemon |

> **Build order matters.** The Go proxy links `linucb-core/target/release/liblinucb_core.a`,
> so build the Rust core first.

---

## Option 1: on your laptop (fastest)

This uses mock discovery, so you need no cluster.

**1. Build.**

```bash
(cd linucb-core && cargo build --release)
(cd linucb-proxy && go build -o proxy ./cmd/proxy/)
```

**2. Start two backends, one slow and one fast**, each in its own terminal:

```bash
# Terminal 1: slow backend (500 ms) on :9001
python3 -c "
import http.server, time
class SlowHandler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        time.sleep(0.5)
        self.send_response(200)
        self.send_header('Content-type', 'text/plain')
        self.end_headers()
        self.wfile.write(b'slow backend')
http.server.HTTPServer(('127.0.0.1', 9001), SlowHandler).serve_forever()
"

# Terminal 2: fast backend on :9002
python3 -c "
import http.server
class FastHandler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header('Content-type', 'text/plain')
        self.end_headers()
        self.wfile.write(b'fast backend')
http.server.HTTPServer(('127.0.0.1', 9002), FastHandler).serve_forever()
"
```

**3. Start Janus.**

```bash
cd linucb-proxy && ./proxy -listen=:8080 -alpha=0.15 -gamma=0.999
```

```
level=INFO msg="janus proxy listening" addr=:8080 alpha=0.15 gamma=0.999
level=INFO msg="mock discovery enabled (no -service flag)"
```

**4. Send traffic and watch it learn.**

```bash
for i in {1..200}; do curl -s http://localhost:8080/ >/dev/null; sleep 0.05; done
curl localhost:8080/stats
```

Over a few hundred requests, `hits` for the fast backend (port 9002) should pull far
ahead. For your own backends: `-backends=id=host:port,id2=host2:port2`.

---

## Option 2: on Kubernetes

**1. Prepare a cluster** (for example `kind create cluster --name janus-test`).

**2. Create something to balance.**

```bash
kubectl create namespace janus

kubectl apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata: { name: my-backend, namespace: janus }
spec:
  replicas: 2
  selector: { matchLabels: { app: my-backend } }
  template:
    metadata: { labels: { app: my-backend } }
    spec:
      containers:
      - name: server
        image: python:3.11-alpine
        command: ["sh", "-c", "python -m http.server 8080"]
        ports: [{ containerPort: 8080 }]
---
apiVersion: v1
kind: Service
metadata: { name: my-backend, namespace: janus }
spec:
  selector: { app: my-backend }
  ports: [{ port: 8080, targetPort: 8080 }]
EOF

kubectl -n janus wait --for=condition=ready pod -l app=my-backend --timeout=60s
```

**3. Install Janus.**

```bash
helm install janus ./helm-chart \
  --namespace janus \
  --set proxy.service=my-backend \
  --set proxy.alpha=0.15 \
  --set proxy.gamma=0.999 \
  --set image.repository=ghcr.io/giltas/projectjanus \
  --set image.tag=latest
```

**4. Try it.**

```bash
kubectl -n janus port-forward svc/janus 8080:8080 &
for i in {1..100}; do curl -s http://localhost:8080/ >/dev/null; sleep 0.1; done
kubectl -n janus logs -f deploy/janus | grep nobs
```

### Build your own image

```bash
docker build -t your-registry/janus:v1.0.0 .
docker push your-registry/janus:v1.0.0
helm install janus ./helm-chart \
  --set image.repository=your-registry/janus --set image.tag=v1.0.0 \
  --set proxy.service=my-backend
```

The Dockerfile is multi-stage: it compiles the Rust core, then the Go proxy with
`-tags k8s`, then copies the single binary into a minimal runtime image.

### With `kind`: don't forget to load the image

`kind` has its own container cache, separate from Docker's. After rebuilding you must
load the image or the pod will keep running the old binary:

```bash
cd linucb-core && cargo build --release && cd ..
docker build -t localhost/janus-proxy:latest .
kind load docker-image localhost/janus-proxy:latest --name janus
helm upgrade janus ./helm-chart -n janus-system --reuse-values \
  --set image.repository=localhost/janus-proxy --set image.tag=latest
kubectl rollout status deployment/janus-project-janus -n janus-system
kubectl logs -l app.kubernetes.io/name=project-janus -n janus-system --tail=3
```

---

## Checklist before a real deployment

- [ ] Image built, pushed and tested locally (`docker run … -listen=:8080`)
- [ ] Cluster reachable, Helm 3 installed, namespace created
- [ ] The target Service exists and has **ready** endpoints (`kubectl get endpointslice`)
- [ ] Network policies allow Janus → backends
- [ ] You've read [known issues](known-issues.md) (no TLS, no retries, unauthenticated admin endpoints)

---

## Settings reference

### Command-line flags

| Flag | Default | What it does |
| --- | --- | --- |
| `-listen` | `:8080` | Address to listen on |
| `-alpha` | `0.15` | Exploration weight. Higher means more exploring |
| `-gamma` | `0.999` | Forgetting factor, `[0.99, 1.0]`. Lower forgets faster |
| `-service` | `""` | Kubernetes Service to watch. Empty means mock discovery |
| `-namespace` | `$POD_NAMESPACE` | Namespace to watch |
| `-backends` | `backend-a=127.0.0.1:9001,backend-b=127.0.0.1:9002` | Mock backends as `id=host:port,...` |
| `-prometheus-url` | `""` | Enables resource features. Empty means response telemetry only |
| `-feature-interval` | `5s` | How often resource features refresh |
| `-ebpf-object` | `""` | Optional BPF ELF for TCP diagnostics (needs `-tags ebpf` and privileges) |

### Helm values (`helm-chart/values.yaml`)

```yaml
replicaCount: 2
image: { repository: ghcr.io/giltas/projectjanus, tag: "", pullPolicy: IfNotPresent }  # tag defaults to appVersion
proxy:
  listen: ":8080"
  alpha: 0.15
  gamma: 0.999
  prometheusURL: ""
  featureInterval: "5s"
  service: ""          # empty = mock discovery (local dev only)
  port: 8080
service: { type: ClusterIP, port: 8080 }
resources:
  requests: { cpu: 100m, memory: 128Mi }
  limits:   { cpu: 500m, memory: 256Mi }
```

`100m` is a tenth of a CPU core and `128Mi` is 128 mebibytes. **Requests** are what the
scheduler guarantees; **limits** are the ceiling (CPU is throttled past it, memory is
OOM-killed).

| Level | CPU | Memory | When |
| --- | --- | --- | --- |
| Minimum | 100m | 128Mi | Scheduling on shared nodes |
| Recommended | 200m | 256Mi | Normal production |
| High-load | 1000m | 512Mi | High request rates |

The proxy mostly waits on the network; at 12 features and 100 arms the model state is
only about 12 KB, so Go's runtime dominates memory.

### Endpoints and probes

| Path | Returns | Used for |
| --- | --- | --- |
| `/healthz` | `200` | Liveness probe |
| `/readyz` | `200` if at least one backend is available, else `503` | Readiness probe |
| `/stats` | Plain-text counters per pod (see [page 4](04-life-of-a-request.md)) | Humans and scripts |
| `/model` | JSON: per-pod state, observations, hits, UCB score | Debugging the model |
| `/metrics` | Prometheus metrics | Scraping |

> These endpoints are served on the **client-facing port**, unauthenticated, and take
> priority over any upstream path with the same name. See [known issues](known-issues.md).

Prometheus Operator users can scrape `/metrics` with a `ServiceMonitor`:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata: { name: janus }
spec:
  selector: { matchLabels: { app: janus } }
  endpoints: [{ port: http, path: /metrics, interval: 15s }]
```

---

## Operating it

### Install, upgrade, roll back

```bash
helm install  janus ./helm-chart -n janus --set proxy.service=my-backend
helm upgrade  janus ./helm-chart -n janus --set proxy.alpha=0.25
helm upgrade  janus ./helm-chart -n janus --set replicaCount=5
helm history  janus -n janus
helm rollback janus -n janus        # previous revision
helm rollback janus 2 -n janus      # a specific one
```

### Important log lines

| Message | Meaning |
| --- | --- |
| `janus proxy listening` | Started successfully |
| `k8s discovery enabled` | Connected to the Kubernetes API |
| `endpoint slice cache synced` | Discovery is ready to route |
| `pod ACTIVE` | A pod was promoted from SHADOW |
| `discovery stopped` | Discovery exited (normally at shutdown) |

### State is not saved

Janus keeps its model **in memory only**.

- A restarted pod starts learning from scratch.
- Replicas each learn independently; nothing is shared. Two replicas each see about half the traffic.
- A Helm rolling upgrade briefly resets learning.

This is deliberate: no volumes, no backup, and it recovers quickly if traffic is steady.
But it does mean a single replica is a single point of failure, and many replicas
fragment what each one learns (see [known issues](known-issues.md)).

At 100 req/s with 2 pods, expect roughly 4 s in WARMING (200 observations each) and
20 s in SHADOW (1,000 observations) before every pod is ACTIVE.

### Graceful shutdown

On `SIGTERM`, Janus stops discovery, stops accepting connections, lets in-flight
requests finish (up to 5 s) and exits. The chart doesn't set `terminationGracePeriodSeconds`
or a `preStop` hook, so Kubernetes' default grace period (30 s) applies; add them in your own
deployment overlay if you need a delayed shutdown.

### Scaling

```bash
kubectl -n janus scale deployment janus --replicas=5
```

A `HorizontalPodAutoscaler` on CPU (for example, target 70%) works; remember each new
replica starts with an empty model.

### Tuning

| Goal | Change |
| --- | --- |
| Explore more, learn faster | Raise `-alpha` (0.20, 0.30) |
| Steadier routing | Lower `-alpha` (0.10, 0.05) |
| React faster to changes | Lower `-gamma` (0.995, 0.99) |
| Calmer, less reactive | Raise `-gamma` (0.9999) |
| Warm up faster (less safe) | Lower `WarmupObservations` (a code change) |

Change one knob at a time, and judge it with a benchmark rather than a hunch.

---

[← Previous: The Go proxy](09-go-proxy-internals.md) · [Documentation index](README.md) · [Next: Troubleshooting →](11-troubleshooting.md)
