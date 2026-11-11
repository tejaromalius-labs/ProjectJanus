# 11. Troubleshooting

**What you'll learn:** what to check, in order, when something looks wrong. Each entry
gives the symptom, the likely cause and the fix.

A good first move for almost every problem is to look at what Janus itself reports:

```bash
curl localhost:8080/stats            # counters and per-pod state (plain text)
curl localhost:8080/model | jq .     # per-pod observations, hits and UCB score (JSON)
kubectl -n janus logs deploy/janus | grep -E "ERROR|WARN"
```

---

## "No pods appear" or "mock discovery enabled" with `-service` set

You expect Kubernetes discovery but see none, or `/stats` lists no pods.

1. **Built without the tag?** Kubernetes discovery needs `go build -tags k8s -o proxy ./cmd/proxy`. The Docker image already does this.
2. **Is the flag reaching the pod?** `helm get values janus -n janus | grep service`
3. **Does the Service exist, with ready endpoints?**
   ```bash
   kubectl get svc my-backend -n <backend-namespace>
   kubectl get endpointslice -n <backend-namespace> | grep my-backend
   ```
   Janus only adds endpoints marked **ready**.
4. **Permissions?**
   ```bash
   kubectl auth can-i list endpointslices --as=system:serviceaccount:janus:janus -n janus
   kubectl get clusterrolebinding janus
   ```
5. **Look at the logs:** `kubectl logs -n janus deploy/janus | grep -i discovery`
6. Restart if you've changed something: `kubectl rollout restart deployment/janus -n janus`

## "All requests go to one pod" / traffic looks uneven

First work out whether that is actually wrong. Expected behaviour:

- Every pod gets its first 20 requests, then all pods are scored and share load by
  score-weighted in-flight cost.
- A **slow** pod should get *less*. That's the point. How much less depends on its score gap.
- `hits` can differ a lot, and legitimately, when one pod is genuinely worse.

If it still looks wrong:

1. **Is the favoured pod actually faster?** Compare their latency directly.
2. **Is the difference big enough?** Tiny differences (under about 14 ms of latency) fall inside the "treat as equal" dead zone and are shared by fewest in flight.
3. **Is learning happening?** `nobs` should be rising for every pod, and `telemetry_dropped` should be flat.
4. **Try more exploration:** `helm upgrade janus ./helm-chart --set proxy.alpha=0.30`
5. **Look for circuit breakers.** A tripped breaker excludes a pod; `/stats` shows its state and hits.

## A pod is stuck in WARMING (about 1 observation)

Every available pod gets its first 20 requests, so a pod stuck near 1 observation is
either **not available** (its circuit breaker is open) or its rewards are **not reaching
the engine**. Check `/stats` and look at `telemetry_dropped` and `telemetry_queued`.

## A pod is stuck in SHADOW with thousands of observations

This is usually **correct**. The SHADOW → ACTIVE gate refuses to promote a pod whose p99
is more than 20% above the ACTIVE median. A slow pod stays in SHADOW until it gets
faster or is replaced; more observations never help.

It's a bug only if the pod is genuinely fast. In that case check that the update worker
is calling `SetPodP99` and `SetFleetMedianP99` (see the dependency note on
[page 5](05-pods-and-lifecycle.md)). If the fleet median is 0 the gate misbehaves.

## Learning is slow, or traffic doesn't shift

1. Are observations being collected? `kubectl logs deploy/janus | grep nobs`
2. Is the latency difference significant? Small gaps don't produce a large preference.
3. Is `gamma` too high? 0.999 is stable; 0.995 reacts faster.
4. Remember the first minute: new pods need about 20 requests, then 200 and 1,000
   observations for promotion.

## A recovered pod isn't getting traffic back

Expect a delay. In the benchmark a recovered pod needed a median 1.5–2.3 s to reach 20%
of traffic after a latency or CPU fault, versus about 1 s for the simple policies (see
[page 6](06-adapting-to-change.md)). If a pod is still getting nothing after many
seconds of healthy responses:

- Is its **circuit breaker** open? The 5-second cooldown has to end, and a probe has to succeed.
- Are its latency feature and score recovering? `curl /model` shows its UCB score.
- Is traffic so low that its latency samples don't refresh? Staleness is bounded by 1 s plus the feature refresh interval (up to about 6 s at the default 5 s).

## High latency through the proxy

```bash
kubectl top pod -n janus
kubectl describe pod <janus-pod> -n janus | grep -i oom

# compare direct backend latency with latency through Janus
kubectl run -it --rm debug --image=alpine -- sh
#   inside: time wget -O- http://my-backend:8080/
time wget -O- http://janus:8080/
```

1. Raise resource limits: `helm upgrade janus … --set resources.limits.cpu=2000m`
2. Check network policies: `kubectl get networkpolicy -n janus`
3. Be patient during warm-up. Latency to the "wrong" pod is normal at first.
4. Check how often decisions fall back: a high `fallback=` count in `/stats` means the model is exceeding its 1.5 ms budget.

## Memory growth or OOMKilled

```bash
kubectl top pod -n janus
kubectl logs -n janus deploy/janus | grep dropped
```

1. Raise the limit: `--set resources.limits.memory=1Gi`
2. Restart: `kubectl rollout restart deployment/janus -n janus`
3. If it persists, report it. One *known* cause is arm slots that are never reclaimed when pods churn heavily (see [known issues](known-issues.md)).

## Daily health check

```bash
kubectl -n janus get pods
kubectl -n janus get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.containerStatuses[0].restartCount}{"\n"}{end}'
kubectl -n janus port-forward svc/janus 8080:8080 &
curl -s localhost:8080/model | jq '.pods | length'     # should equal your backend pod count
pkill -f "port-forward"
```

---

[← Previous: Running Janus](10-running-janus.md) · [Documentation index](README.md) · [Next: Testing and benchmarking →](12-testing-and-benchmarking.md)
