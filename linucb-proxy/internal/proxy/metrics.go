package proxy

import (
	"fmt"
	"net/http"
	"time"
)

var decisionBounds = [...]float64{0.00001, 0.00005, 0.0001, 0.0005, 0.0015, 0.005, 0.05}

// SetTelemetryStats connects queue health counters without coupling packages.
func (p *Proxy) SetTelemetryStats(fn func() (enqueued, dropped uint64, queued int)) {
	p.telemetryStats = fn
}

func (p *Proxy) recordDecision(d time.Duration) {
	seconds := d.Seconds()
	p.decisionNanos.Add(uint64(d))
	for i, bound := range decisionBounds {
		if seconds <= bound {
			p.decisionBuckets[i].Add(1)
			return
		}
	}
	p.decisionBuckets[len(decisionBounds)].Add(1)
}

func (p *Proxy) serveMetrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# TYPE janus_requests_total counter\njanus_requests_total %d\n# TYPE janus_completed_total counter\njanus_completed_total %d\n# TYPE janus_errors_total counter\njanus_errors_total %d\n", p.requestCount.Load(), p.completedCount.Load(), p.errorCount.Load())
	s := p.Stats()
	fmt.Fprintf(w, "# TYPE janus_routes_total counter\njanus_routes_total{policy=\"linucb\"} %d\njanus_routes_total{policy=\"fallback\"} %d\njanus_routes_total{policy=\"shadow\"} %d\n", s.LinUCB, s.Fallback, s.Shadow)
	fmt.Fprintln(w, "# TYPE janus_decision_duration_seconds histogram")
	var count uint64
	for i, bound := range decisionBounds {
		count += p.decisionBuckets[i].Load()
		fmt.Fprintf(w, "janus_decision_duration_seconds_bucket{le=\"%g\"} %d\n", bound, count)
	}
	count += p.decisionBuckets[len(decisionBounds)].Load()
	fmt.Fprintf(w, "janus_decision_duration_seconds_bucket{le=\"+Inf\"} %d\njanus_decision_duration_seconds_count %d\n", count, count)
	fmt.Fprintf(w, "janus_decision_duration_seconds_sum %g\n", float64(p.decisionNanos.Load())/1e9)
	if p.telemetryStats != nil {
		enqueued, dropped, queued := p.telemetryStats()
		fmt.Fprintf(w, "# TYPE janus_telemetry_enqueued_total counter\njanus_telemetry_enqueued_total %d\n# TYPE janus_telemetry_dropped_total counter\njanus_telemetry_dropped_total %d\n# TYPE janus_telemetry_queue_depth gauge\njanus_telemetry_queue_depth %d\n", enqueued, dropped, queued)
	}
	if p.connectionMetrics != nil {
		p.connectionMetrics(w)
	}
	for _, pod := range p.reg.All() {
		fmt.Fprintf(w, "janus_backend_requests_total{pod=%q} %d\njanus_backend_inflight{pod=%q} %d\njanus_backend_observations{pod=%q} %d\njanus_backend_p99_seconds{pod=%q} %g\n", pod.ID, pod.RouteHits.Load(), pod.ID, pod.InFlightCount(), pod.ID, pod.NObs.Load(), pod.ID, pod.PodP99()/1000)
		if cb := pod.CircuitBreaker(); cb != nil {
			fmt.Fprintf(w, "janus_backend_breaker_state{pod=%q} %d\n", pod.ID, cb.State())
		}
	}
}

func (p *Proxy) SetConnectionMetrics(fn func(http.ResponseWriter)) { p.connectionMetrics = fn }
