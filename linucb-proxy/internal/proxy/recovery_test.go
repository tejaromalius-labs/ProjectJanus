package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/giltas/projectjanus/linucb-proxy/internal/circuitbreaker"
	"github.com/giltas/projectjanus/linucb-proxy/internal/fallback"
	"github.com/giltas/projectjanus/linucb-proxy/internal/featurestore"
	"github.com/giltas/projectjanus/linucb-proxy/internal/linucb"
	"github.com/giltas/projectjanus/linucb-proxy/internal/registry"
)

func TestBreakerFilteringAcrossRoutingPaths(t *testing.T) {
	for _, scenario := range []string{"fallback", "warming", "shadow"} {
		t.Run(scenario, func(t *testing.T) {
			eng, err := linucb.NewEngine(linucb.D, .15)
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
			reg.AddPod("good", "127.0.0.1:1")
			bad, _ := reg.AddPod("bad", "127.0.0.1:2")
			reg.ForceState("good", registry.StateActive)
			cfg := DefaultConfig()
			switch scenario {
			case "warming":
				// bad stays WARMING; it is scored like any other pod.
			case "shadow":
				reg.ForceState("bad", registry.StateShadow)
				cfg.ShadowTrafficCapPct = 100
			case "fallback":
				reg.ForceState("bad", registry.StateActive)
				cfg.DecisionLatencyLimit = -1 // every decision takes the fallback path
			}
			for i := 0; i < 5; i++ {
				bad.RecordResult(true)
			}
			px := New(cfg, eng, reg, featurestore.New(featurestore.NewMockSource()), fallback.New(), discardLogger())
			for i := 0; i < 30; i++ {
				if pod := px.route(httptest.NewRequest("GET", "/", nil)); pod == nil || pod.ID != "good" {
					t.Fatalf("selected unavailable backend: %+v", pod)
				}
			}
		})
	}
}

func TestRecoveryProbeIsSelectedAndNotStranded(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, .15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	reg.AddPod("good", "127.0.0.1:1")
	bad, _ := reg.AddPod("bad", "127.0.0.1:2")
	reg.ForceState("good", registry.StateActive)
	reg.ForceState("bad", registry.StateActive)
	bad.SetCircuitBreaker(circuitbreaker.NewWithConfig(1, time.Millisecond))
	bad.RecordResult(true)
	time.Sleep(2 * time.Millisecond)
	px := New(DefaultConfig(), eng, reg, featurestore.New(featurestore.NewMockSource()), fallback.New(), discardLogger())
	req := httptest.NewRequest("GET", "/", nil)
	if pod := px.route(req); pod != bad {
		t.Fatal("recovery probe was not selected")
	}
	for i := 0; i < 30; i++ {
		if pod := px.route(req); pod == nil || pod.ID != "good" {
			t.Fatal("half-open probe admitted extra traffic")
		}
	}
	bad.RecordResult(false)
	if bad.CircuitBreaker().State() != circuitbreaker.StateClosed {
		t.Fatal("successful probe did not recover")
	}
}

func TestUpstreamConnectionFailureProducesFeedback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := hostOf(t, upstream.URL)
	upstream.Close()
	eng, err := linucb.NewEngine(linucb.D, .15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	pod, _ := reg.AddPod("bad", endpoint)
	px := New(DefaultConfig(), eng, reg, featurestore.New(featurestore.NewMockSource()), fallback.New(), discardLogger())
	var outcomes []Outcome
	px.SetObserver(func(o Outcome) { outcomes = append(outcomes, o) })
	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		px.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
		if response.Code != 502 {
			t.Fatalf("status=%d", response.Code)
		}
	}
	if len(outcomes) != 2 || outcomes[0].HTTPStatus != 502 || outcomes[0].PodID != "bad" {
		t.Fatalf("feedback=%+v", outcomes)
	}
	if pod.CircuitBreaker().State() != circuitbreaker.StateOpen {
		t.Fatal("connection failures did not trip breaker")
	}
}

func TestReadinessAndOperationalMetrics(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, .15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	px := New(DefaultConfig(), eng, reg, featurestore.New(featurestore.NewMockSource()), fallback.New(), discardLogger())
	check := func(path string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		px.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != status {
			t.Fatalf("%s=%d", path, w.Code)
		}
		return w.Body.String()
	}
	check("/healthz", 200)
	check("/readyz", 503)
	pod, _ := reg.AddPod("p1", "127.0.0.1:1")
	check("/readyz", 200)
	for i := 0; i < 5; i++ {
		pod.RecordResult(true)
	}
	check("/readyz", 503)
	px.SetTelemetryStats(func() (uint64, uint64, int) { return 10, 2, 3 })
	check("/", 502)
	body := check("/metrics", 200)
	for _, want := range []string{"janus_requests_total 1", "janus_completed_total 1", "janus_errors_total 1", "janus_telemetry_dropped_total 2", "janus_telemetry_queue_depth 3", "janus_decision_duration_seconds_count 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
}
