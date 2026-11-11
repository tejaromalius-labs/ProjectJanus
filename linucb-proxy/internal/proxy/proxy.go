// Package proxy implements the LinUCB-routed reverse proxy and its safety
// override chain.
package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/giltas/projectjanus/linucb-proxy/internal/circuitbreaker"
	"github.com/giltas/projectjanus/linucb-proxy/internal/fallback"
	"github.com/giltas/projectjanus/linucb-proxy/internal/featurestore"
	"github.com/giltas/projectjanus/linucb-proxy/internal/linucb"
	"github.com/giltas/projectjanus/linucb-proxy/internal/registry"
)

// Config tunes the routing safety chain.
type Config struct {
	DecisionLatencyLimit time.Duration // fallback if a decision exceeds this (1.5ms)
	ShadowTrafficCapPct  float64       // max % of traffic to shadow pods (5)
	MaxIdleConnsPerHost  int           // Transport tuning (default 2 is a p99 killer)
	ScoreDelta           float64       // gaps under this are no preference; each step beyond doubles the expected load (0.05)
}

// minExploreObs is how many observations an arm gets before it competes on score.
const minExploreObs = 20

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{
		DecisionLatencyLimit: 1500 * time.Microsecond,
		ShadowTrafficCapPct:  5,
		MaxIdleConnsPerHost:  256,
		ScoreDelta:           0.05,
	}
}

type ctxKey int

const (
	selectedPodKey ctxKey = iota
	requestStartKey
	dispatchInfoKey
)

// Outcome is a per-request result handed to an ObserveFunc for reward feedback.
type Outcome struct {
	PodID      string
	LatencyMs  float64
	HTTPStatus int
	Req        RequestInfo
}

// ObserveFunc receives the outcome of each proxied request. Implementations
// must be non-blocking (the telemetry collector drops on backpressure).
type ObserveFunc func(Outcome)

// Proxy is the LinUCB reverse proxy.
type Proxy struct {
	cfg      Config
	engine   *linucb.Engine
	reg      *registry.Registry
	features *featurestore.Store
	lc       *fallback.LeastConnections
	log      *slog.Logger
	rp       *httputil.ReverseProxy

	// observe is an optional sink for per-request outcomes (latency, status).
	// Wired by main.go to the telemetry collector; nil disables reward feedback.
	observe ObserveFunc

	// metrics (atomic counters; exported via Stats for tests/observability)
	fallbackCount     atomic.Uint64
	shadowCount       atomic.Uint64
	linucbCount       atomic.Uint64
	telemetryStats    func() (uint64, uint64, int)
	connectionMetrics func(http.ResponseWriter)
	decisionNanos     atomic.Uint64
	requestCount      atomic.Uint64
	completedCount    atomic.Uint64
	errorCount        atomic.Uint64
	decisionBuckets   [8]atomic.Uint64
}

// New builds a Proxy wired to its dependencies.
func New(
	cfg Config,
	engine *linucb.Engine,
	reg *registry.Registry,
	features *featurestore.Store,
	lc *fallback.LeastConnections,
	log *slog.Logger,
) *Proxy {
	p := &Proxy{
		cfg:      cfg,
		engine:   engine,
		reg:      reg,
		features: features,
		lc:       lc,
		log:      log,
	}

	base := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
	}

	p.rp = &httputil.ReverseProxy{
		Rewrite:        p.rewrite,
		Transport:      &timingTransport{base: base},
		ModifyResponse: p.modifyResponse,
		ErrorHandler:   p.errorHandler,
	}
	return p
}

// SetObserver wires a reward-feedback sink (the telemetry collector). Pass nil
// to disable feedback.
func (p *Proxy) SetObserver(fn ObserveFunc) { p.observe = fn }

// timingTransport stamps the upstream round-trip start time so latency can be
// measured excluding client write time (true upstream latency).
type timingTransport struct{ base http.RoundTripper }

func (t *timingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(r)
	if resp != nil || err != nil {
		// Stash elapsed time on the request context for modifyResponse.
		if v, ok := r.Context().Value(requestStartKey).(*time.Duration); ok {
			*v = time.Since(start)
		}
	}
	return resp, err
}

// ServeHTTP routes one request through the safety override chain.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.URL.Path == "/stats" {
		s := p.Stats()
		pods := p.reg.All()
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "linucb=%d fallback=%d shadow=%d\n", s.LinUCB, s.Fallback, s.Shadow)
		for _, pod := range pods {
			fmt.Fprintf(w, "pod=%s state=%s nobs=%d hits=%d\n", pod.ID, pod.State(), pod.NObs.Load(), pod.RouteHits.Load())
		}
		if p.telemetryStats != nil {
			enqueued, dropped, queued := p.telemetryStats()
			fmt.Fprintf(w, "requests=%d completed=%d errors=%d telemetry_enqueued=%d telemetry_dropped=%d telemetry_queued=%d\n", p.requestCount.Load(), p.completedCount.Load(), p.errorCount.Load(), enqueued, dropped, queued)
		}
		return
	}
	if r.URL.Path == "/model" {
		p.serveModelDebug(w, r)
		return
	}
	if r.URL.Path == "/readyz" {
		for _, pod := range p.reg.All() {
			if pod.Available() {
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		http.Error(w, "no available backend", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/metrics" {
		p.serveMetrics(w)
		return
	}
	p.requestCount.Add(1)
	defer p.completedCount.Add(1)
	start := time.Now()
	pod := p.route(r)
	p.recordDecision(time.Since(start))
	if pod == nil {
		p.errorCount.Add(1)
		http.Error(w, "no healthy backend", http.StatusServiceUnavailable)
		return
	}
	pod.RouteHits.Add(1)
	dispatch := requestInfo(r)
	dispatch.Features, _ = p.features.Get(pod.ID)
	pod.Acquire()
	defer pod.Release()

	var upstreamDur time.Duration
	ctx := context.WithValue(r.Context(), selectedPodKey, pod)
	ctx = context.WithValue(ctx, dispatchInfoKey, dispatch)
	ctx = context.WithValue(ctx, requestStartKey, &upstreamDur)
	p.rp.ServeHTTP(w, r.WithContext(ctx))
}

func (p *Proxy) modifyResponse(resp *http.Response) error {
	ctx := resp.Request.Context()
	pod, _ := ctx.Value(selectedPodKey).(*registry.PodEntry)
	if resp.StatusCode >= 500 {
		p.errorCount.Add(1)
	}
	if pod != nil {
		pod.RecordResult(resp.StatusCode >= 500)
	}
	if p.observe == nil {
		return nil
	}
	if pod == nil {
		return nil
	}
	var latencyMs float64
	if d, ok := ctx.Value(requestStartKey).(*time.Duration); ok {
		latencyMs = float64(d.Microseconds()) / 1000.0
	}
	p.observe(Outcome{
		PodID:      pod.ID,
		LatencyMs:  latencyMs,
		HTTPStatus: resp.StatusCode,
		Req:        requestInfo(resp.Request),
	})
	return nil
}

// route runs the safety override chain and returns the chosen pod.
// route claims recovery probes only for the pod actually receiving a request.
func (p *Proxy) route(r *http.Request) *registry.PodEntry {
	pods := p.reg.All()
	for _, pod := range pods {
		if pod.Available() && pod.CircuitBreaker() != nil && pod.CircuitBreaker().State() != circuitbreaker.StateClosed && pod.AllowRequest() {
			return pod
		}
	}
	for attempt := 0; attempt <= len(pods); attempt++ {
		pod := p.choose(r)
		if pod == nil {
			break
		}
		if pod.AllowRequest() {
			return pod
		}
	}
	// Preserve best-effort service only when every servable breaker is OPEN.
	// HALF_OPEN probes must never be bypassed by this outage fallback.
	var outage []*registry.PodEntry
	for _, pod := range pods {
		if pod.State() == registry.StateDraining {
			continue
		}
		cb := pod.CircuitBreaker()
		if cb == nil || cb.State() != circuitbreaker.StateOpen {
			return nil
		}
		outage = append(outage, pod)
	}
	if len(outage) > 0 {
		p.fallbackCount.Add(1)
	}
	return p.lc.Pick(outage)
}

func available(pods []*registry.PodEntry) []*registry.PodEntry {
	out := pods[:0]
	for _, pod := range pods {
		if pod.Available() {
			out = append(out, pod)
		}
	}
	return out
}

func (p *Proxy) choose(r *http.Request) *registry.PodEntry {
	// Every available pod is scored, whatever its lifecycle state: a new pod
	// gets its first requests through minimum exploration, then competes on
	// cost. Lifecycle state gates promotion, not routing. Holding unproven pods
	// to a small fixed share left capacity idle, overloaded the pods that
	// graduated first, and kept a late pod WARMING for most of a minute.
	pods := available(p.reg.PodsInState(registry.StateActive))
	pods = append(pods, available(p.reg.PodsInState(registry.StateShadow))...)
	pods = append(pods, available(p.reg.PodsInState(registry.StateWarming))...)

	// Step 1+2: time the LinUCB decision; fall back if too slow or unhealthy.
	start := time.Now()
	best := p.linucbBest(r, pods)
	decision := time.Since(start)

	if decision > p.cfg.DecisionLatencyLimit || best == nil {
		p.fallbackCount.Add(1)
		return p.lc.Pick(pods)
	}

	// Step 3: probabilistic shadow traffic injection (non-blocking).
	if p.shouldInjectShadow() {
		shadow := available(p.reg.PodsInState(registry.StateShadow))
		if s := p.lc.Pick(shadow); s != nil {
			p.shadowCount.Add(1)
			return s
		}
	}

	p.linucbCount.Add(1)
	return best
}

// linucbBest scores all active arms and returns the pod with the lowest cost,
// (in flight + 1) · 2^steps, random among ties, where steps is 0 for a score
// gap under ScoreDelta and ⌊gap/ScoreDelta − 1 + U⌋ beyond it.
func (p *Proxy) linucbBest(r *http.Request, active []*registry.PodEntry) *registry.PodEntry {
	if len(active) == 0 {
		return nil
	}

	// Filter candidate pods through per-pod circuit breakers (JANUS-02).
	// Degraded pods in StateOpen are suppressed from candidate selection.
	var eligible []*registry.PodEntry
	for _, pod := range active {
		if pod.Available() {
			eligible = append(eligible, pod)
		}
	}
	if len(eligible) == 0 {
		return nil
	}

	req := requestInfo(r)

	// Under-explored arms (fewer than minExploreObs observations) get priority,
	// least-observed first with random ties. A new arm's estimate starts at zero
	// (A⁻¹ = I, b = 0), so with a single observation it scores below busy
	// incumbents and would never be chosen on merit.
	var cold []*registry.PodEntry
	fewest := uint64(minExploreObs)
	for _, pod := range eligible {
		switch n := p.engine.NObs(pod.ArmID); {
		case n < fewest:
			fewest, cold = n, append(cold[:0], pod)
		case n == fewest && n < minExploreObs:
			cold = append(cold, pod)
		}
	}
	if len(cold) > 0 {
		return cold[rand.Intn(len(cold))]
	}

	delta := p.cfg.ScoreDelta
	if delta <= 0 {
		delta = 0.05
	}

	type scoredArm struct {
		pod   *registry.PodEntry
		score float64
	}
	var stackBuf [16]scoredArm
	var scored []scoredArm
	if len(eligible) <= len(stackBuf) {
		scored = stackBuf[:0]
	} else {
		scored = make([]scoredArm, 0, len(eligible))
	}

	bestScore := math.Inf(-1)
	for _, pod := range eligible {
		f, ok := p.features.Get(pod.ID)
		if !ok {
			f = featurestore.PodFeatures{} // cold cache → zero features
		}
		x := buildContext(f, req)
		score, err := p.engine.Score(pod.ArmID, x)
		if err != nil {
			continue
		}
		scored = append(scored, scoredArm{pod: pod, score: score})
		if score > bestScore {
			bestScore = score
		}
	}
	if len(scored) == 0 {
		return nil
	}

	// Each ScoreDelta step below the best doubles an arm's expected load. A low
	// score never excludes an arm outright: a pod is chosen only while its
	// in-flight + 1 is at most 2^steps times another's, so noisy scores cannot
	// pile all traffic onto one pod, and in-flight counts react at once.
	//
	// A gap under one ScoreDelta is no preference, and equal costs are broken
	// at random. With smooth gaps, near-equal pods never tie and the highest
	// score took every request that found it idle: at 600 RPS that was ~260
	// req/s on one 250 req/s pod, which was CPU-throttled into ~10× tails.
	//
	// Beyond that dead zone the step count is randomly rounded,
	// ⌊gap/ScoreDelta − 1 + U⌋ with U uniform in [0,1), so the expected
	// multiplier is continuous in the gap (1 at the edge of the dead zone) while
	// costs stay whole powers of two that can tie. A hard step edge flipped a
	// recovering pod's share back and forth as its score crossed it.
	var best *registry.PodEntry
	bestCost := math.Inf(1)
	ties := 0
	for _, s := range scored {
		steps := 0.0
		if gap := (bestScore - s.score) / delta; gap >= 1 {
			steps = math.Floor(gap - 1 + rand.Float64())
		}
		cost := float64(s.pod.InFlightCount()+1) * math.Exp2(steps)
		switch {
		case cost < bestCost:
			best, bestCost, ties = s.pod, cost, 1
		case cost == bestCost:
			ties++
			if rand.Intn(ties) == 0 {
				best = s.pod
			}
		}
	}
	return best
}

func (p *Proxy) shouldInjectShadow() bool {
	return rand.Float64()*100.0 < p.cfg.ShadowTrafficCapPct
}

func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	pod, _ := pr.In.Context().Value(selectedPodKey).(*registry.PodEntry)
	if pod == nil {
		return
	}
	target := &url.URL{Scheme: "http", Host: pod.Endpoint}
	pr.SetURL(target)
	pr.SetXForwarded()
}

func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	p.errorCount.Add(1)
	pod, _ := r.Context().Value(selectedPodKey).(*registry.PodEntry)
	if pod != nil {
		pod.RecordResult(true)
		if p.observe != nil {
			var latencyMs float64
			if d, ok := r.Context().Value(requestStartKey).(*time.Duration); ok {
				latencyMs = float64(d.Microseconds()) / 1000
			}
			p.observe(Outcome{PodID: pod.ID, LatencyMs: latencyMs, HTTPStatus: http.StatusBadGateway, Req: requestInfo(r)})
		}
	}
	p.log.Warn("upstream error", "err", err)
	w.WriteHeader(http.StatusBadGateway)
}

func requestInfo(r *http.Request) RequestInfo {
	if info, ok := r.Context().Value(dispatchInfoKey).(RequestInfo); ok {
		return info
	}
	return RequestInfo{
		PayloadLogNorm: payloadLogNorm(r.ContentLength),
		IsMutation:     isMutation(r.Method),
		Now:            time.Now(),
	}
}

func isMutation(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// payloadLogNorm maps content length to [0,1] via log normalization (context
// index 5). maxBytes is a fixed normalization ceiling (~16 MiB).
func payloadLogNorm(contentLength int64) float64 {
	if contentLength <= 0 {
		return 0
	}
	const maxBytes = 16 << 20
	v := math.Log(float64(contentLength)) / math.Log(float64(maxBytes))
	return math.Max(0, math.Min(1, v))
}

// Stats is a snapshot of routing counters.
type Stats struct {
	LinUCB   uint64
	Fallback uint64
	Shadow   uint64
}

// Stats returns the current routing counters.
func (p *Proxy) Stats() Stats {
	return Stats{
		LinUCB:   p.linucbCount.Load(),
		Fallback: p.fallbackCount.Load(),
		Shadow:   p.shadowCount.Load(),
	}
}

// serveModelDebug exposes the LinUCB model state for inspection.
func (p *Proxy) serveModelDebug(w http.ResponseWriter, r *http.Request) {
	pods := p.reg.All()
	w.Header().Set("Content-Type", "application/json")

	// Build a mock request to get context features
	req := requestInfo(r)

	fmt.Fprintf(w, "{\n")
	fmt.Fprintf(w, "  \"pods\": [\n")

	for i, pod := range pods {
		nObs := p.engine.NObs(pod.ArmID)
		f, _ := p.features.Get(pod.ID)
		x := buildContext(f, req)

		// Compute score
		score, scoreErr := p.engine.Score(pod.ArmID, x)
		if scoreErr != nil {
			score = math.NaN()
		}

		fmt.Fprintf(w, "    {\n")
		fmt.Fprintf(w, "      \"pod\": %q,\n", pod.ID)
		fmt.Fprintf(w, "      \"state\": %q,\n", pod.State())
		fmt.Fprintf(w, "      \"arm_id\": %d,\n", pod.ArmID)
		fmt.Fprintf(w, "      \"observations\": %d,\n", nObs)
		fmt.Fprintf(w, "      \"hits\": %d,\n", pod.RouteHits.Load())
		fmt.Fprintf(w, "      \"ucb_score\": %.6f\n", score)
		fmt.Fprintf(w, "    }")
		if i < len(pods)-1 {
			fmt.Fprintf(w, ",")
		}
		fmt.Fprintf(w, "\n")
	}

	fmt.Fprintf(w, "  ]\n")
	fmt.Fprintf(w, "}\n")
}
