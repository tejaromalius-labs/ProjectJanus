package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/fallback"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/featurestore"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/linucb"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/registry"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// hostOf strips scheme from an httptest URL → "host:port".
func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func TestProxyRoutesAwayFromDegradedBackend(t *testing.T) {
	// Two upstreams: "good" returns fast 200s, "bad" is sluggish.
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("good"))
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(40 * time.Millisecond)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("bad"))
	}))
	defer bad.Close()

	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	regCfg := registry.DefaultConfig()
	regCfg.WarmupObservations = 0 // make pods immediately eligible for this test
	regCfg.ShadowObservations = 0
	regCfg.PromotionPollInterval = 5 * time.Millisecond
	reg := registry.New(regCfg, eng, discardLogger())

	src := featurestore.NewMockSource()
	// good backend: low CPU/latency; bad backend: high.
	src.Set("good", featurestore.PodFeatures{CPUUtilization: 0.2, P99LatencyEMA: 0.1})
	src.Set("bad", featurestore.PodFeatures{CPUUtilization: 0.95, P99LatencyEMA: 0.9})
	fs := featurestore.New(src)
	fs.Set("good", featurestore.PodFeatures{CPUUtilization: 0.2, P99LatencyEMA: 0.1})
	fs.Set("bad", featurestore.PodFeatures{CPUUtilization: 0.95, P99LatencyEMA: 0.9})

	goodPod, _ := reg.AddPod("good", hostOf(t, good.URL))
	badPod, _ := reg.AddPod("bad", hostOf(t, bad.URL))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reg.StartPromotionLoop(ctx)
	time.Sleep(20 * time.Millisecond) // let both promote to ACTIVE

	cfg := DefaultConfig()
	cfg.ShadowTrafficCapPct = 0 // disable shadow injection for a clean count
	px := New(cfg, eng, reg, fs, fallback.New(), discardLogger())

	front := httptest.NewServer(px)
	defer front.Close()

	// Teach the model offline: reward good high, bad low, simulating telemetry.
	xGood := buildContext(featurestore.PodFeatures{CPUUtilization: 0.2, P99LatencyEMA: 0.1}, RequestInfo{Now: time.Now()})
	xBad := buildContext(featurestore.PodFeatures{CPUUtilization: 0.95, P99LatencyEMA: 0.9}, RequestInfo{Now: time.Now()})
	for i := 0; i < 300; i++ {
		_ = eng.Update(goodPod.ArmID, xGood, 0.95)
		_ = eng.Update(badPod.ArmID, xBad, 0.10)
	}

	// Drive traffic and count which backend served.
	var goodHits, badHits int
	client := front.Client()
	for i := 0; i < 200; i++ {
		resp, err := client.Get(front.URL)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch strings.TrimSpace(string(body)) {
		case "good":
			goodHits++
		case "bad":
			badHits++
		}
	}

	if goodHits <= badHits {
		t.Fatalf("expected traffic to favor good backend: good=%d bad=%d", goodHits, badHits)
	}
	t.Logf("routing: good=%d bad=%d stats=%+v", goodHits, badHits, px.Stats())
}

// TestConcurrentRouting exercises random cold-arm and shadow choices from
// concurrent HTTP handlers. Run with -race to catch unsafe shared RNG access.
func TestConcurrentRouting(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	reg.AddPod("a", "127.0.0.1:1")
	reg.AddPod("b", "127.0.0.1:2")
	reg.ForceState("a", registry.StateActive)
	reg.ForceState("b", registry.StateActive)
	px := New(DefaultConfig(), eng, reg, featurestore.New(featurestore.NewMockSource()), fallback.New(), discardLogger())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if px.route(httptest.NewRequest("GET", "/", nil)) == nil {
					t.Error("concurrent route returned no backend")
				}
			}
		}()
	}
	wg.Wait()
}

// TestLateWarmingPodScoredWithActive: a pod added to a running fleet is scored
// with the ACTIVE pods, instead of being held to a small fixed share.
func TestLateWarmingPodScoredWithActive(t *testing.T) {
	eng, _ := linucb.NewEngine(linucb.D, 0.15)
	defer eng.Close()

	regCfg := registry.DefaultConfig()
	regCfg.WarmupObservations = 1_000_000 // keep p2 WARMING
	reg := registry.New(regCfg, eng, discardLogger())
	fs := featurestore.New(featurestore.NewMockSource())
	p1, _ := reg.AddPod("p1", "127.0.0.1:1")
	reg.AddPod("p2", "127.0.0.1:2")
	reg.ForceState("p1", registry.StateActive)
	x := buildContext(featurestore.PodFeatures{}, RequestInfo{Now: time.Now()})
	for i := 0; i < 100; i++ {
		_ = eng.Update(p1.ArmID, x, 0.95)
	}

	cfg := DefaultConfig()
	cfg.ShadowTrafficCapPct = 0
	px := New(cfg, eng, reg, fs, fallback.New(), discardLogger())

	hits := 0
	for i := 0; i < 400; i++ {
		if px.route(httptest.NewRequest("GET", "/", nil)).ID == "p2" {
			hits++
		}
	}
	if hits < 120 {
		t.Fatalf("late WARMING pod got %d/400 requests, want at least 120", hits)
	}
}

// TestColdStartSpreadsUntilActive: with nothing ACTIVE, an early SHADOW pod must
// not take all the traffic; WARMING pods stay in the scoring pool so load spreads
// and every pod can graduate.
func TestColdStartSpreadsUntilActive(t *testing.T) {
	eng, _ := linucb.NewEngine(linucb.D, 0.15)
	defer eng.Close()

	regCfg := registry.DefaultConfig()
	regCfg.WarmupObservations = 1_000_000
	reg := registry.New(regCfg, eng, discardLogger())

	fs := featurestore.New(featurestore.NewMockSource())
	ids := []string{"p1", "p2", "p3", "p4"}
	for i, id := range ids {
		reg.AddPod(id, "127.0.0.1:"+string(rune('1'+i)))
	}
	reg.ForceState("p1", registry.StateShadow)

	px := New(DefaultConfig(), eng, reg, fs, fallback.New(), discardLogger())

	hits := map[string]int{}
	for i := 0; i < 400; i++ {
		hits[px.route(httptest.NewRequest("GET", "/", nil)).ID]++
	}
	for _, id := range ids {
		if hits[id] < 40 {
			t.Fatalf("pod %s got %d/400 requests during cold start: %v", id, hits[id], hits)
		}
	}
}

// TestShadowPodsScoredWithActive: a SHADOW pod competes in UCB scoring once a
// pod is ACTIVE, instead of being held to the shadow injection share.
func TestShadowPodsScoredWithActive(t *testing.T) {
	eng, _ := linucb.NewEngine(linucb.D, 0.15)
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	fs := featurestore.New(featurestore.NewMockSource())
	reg.AddPod("p1", "127.0.0.1:1")
	reg.AddPod("p2", "127.0.0.1:2")
	reg.ForceState("p1", registry.StateActive)
	reg.ForceState("p2", registry.StateShadow)

	cfg := DefaultConfig()
	cfg.ShadowTrafficCapPct = 0 // only scored traffic can reach p2
	px := New(cfg, eng, reg, fs, fallback.New(), discardLogger())

	hits := 0
	for i := 0; i < 400; i++ {
		if px.route(httptest.NewRequest("GET", "/", nil)).ID == "p2" {
			hits++
		}
	}
	if hits < 120 {
		t.Fatalf("SHADOW pod got %d/400 scored requests, want at least 120", hits)
	}
}

// TestNewArmExploredBeforeScoring: a new arm takes priority until it has
// minExploreObs observations, even though a trained incumbent scores higher.
func TestNewArmExploredBeforeScoring(t *testing.T) {
	eng, _ := linucb.NewEngine(linucb.D, 0.15)
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	old, _ := reg.AddPod("old", "127.0.0.1:1")
	reg.AddPod("new", "127.0.0.1:2")
	reg.ForceState("old", registry.StateActive)
	reg.ForceState("new", registry.StateActive)

	cfg := DefaultConfig()
	cfg.ShadowTrafficCapPct = 0
	px := New(cfg, eng, reg, featurestore.New(featurestore.NewMockSource()), fallback.New(), discardLogger())

	x := buildContext(featurestore.PodFeatures{}, RequestInfo{Now: time.Now()})
	for i := 0; i < 50; i++ {
		_ = eng.Update(old.ArmID, x, 0.95)
	}
	for i := 0; i < minExploreObs; i++ {
		pod := px.route(httptest.NewRequest("GET", "/", nil))
		if pod.ID != "new" {
			t.Fatalf("request %d went to %s before the new arm had %d observations", i, pod.ID, minExploreObs)
		}
		_ = eng.Update(pod.ArmID, x, 0.5)
	}
	if pod := px.route(httptest.NewRequest("GET", "/", nil)); pod.ID != "old" {
		t.Fatalf("explored arm with lower reward still preferred: got %s", pod.ID)
	}
}

// TestOutcomeContextUsesDispatchSnapshot: feedback must use the request info
// captured at dispatch, not values recomputed from the request afterwards.
func TestOutcomeContextUsesDispatchSnapshot(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	original := RequestInfo{Now: time.Unix(100, 0), IsMutation: true, PayloadLogNorm: .5}
	r = r.WithContext(context.WithValue(r.Context(), dispatchInfoKey, original))
	r.Method = "GET"
	r.ContentLength = 0
	if got := requestInfo(r); got != original {
		t.Fatalf("lost dispatch context: %+v", got)
	}
}

// TestEqualScoresBalanceByInFlight verifies that pods with equal scores share
// traffic by in-flight count, avoiding single-backend saturation (JANUS-01).
func TestEqualScoresBalanceByInFlight(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	p1, _ := reg.AddPod("p1", "127.0.0.1:1")
	p2, _ := reg.AddPod("p2", "127.0.0.1:2")
	reg.ForceState("p1", registry.StateActive)
	reg.ForceState("p2", registry.StateActive)

	fs := featurestore.New(featurestore.NewMockSource())
	cfg := DefaultConfig()
	cfg.ShadowTrafficCapPct = 0
	px := New(cfg, eng, reg, fs, fallback.New(), discardLogger())

	// Pretrain both pods with identical contexts and high rewards
	x := buildContext(featurestore.PodFeatures{}, RequestInfo{Now: time.Now()})
	for i := 0; i < 50; i++ {
		_ = eng.Update(p1.ArmID, x, 0.95)
		_ = eng.Update(p2.ArmID, x, 0.95)
	}

	// Concurrent simulated traffic: in-flight requests alternate selection
	var p1Hits, p2Hits atomic.Uint64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				pod := px.route(httptest.NewRequest("GET", "/", nil))
				if pod == nil {
					t.Error("route returned nil")
					return
				}
				pod.Acquire()
				// Small sleep to keep in-flight active concurrently
				time.Sleep(100 * time.Microsecond)
				if pod.ID == "p1" {
					p1Hits.Add(1)
				} else if pod.ID == "p2" {
					p2Hits.Add(1)
				}
				pod.Release()
			}
		}()
	}
	wg.Wait()

	total := p1Hits.Load() + p2Hits.Load()
	if total != 1000 {
		t.Fatalf("expected 1000 total requests, got %d", total)
	}
	p1Ratio := float64(p1Hits.Load()) / float64(total)
	t.Logf("traffic distribution: p1=%d (%.1f%%), p2=%d (%.1f%%)", p1Hits.Load(), p1Ratio*100, p2Hits.Load(), (1-p1Ratio)*100)
	if p1Ratio < 0.40 || p1Ratio > 0.60 {
		t.Fatalf("expected balanced traffic between [40%%, 60%%], got %.1f%% for p1", p1Ratio*100)
	}
}

// TestDegradedPodIsolated verifies that a pod scoring far below the best
// (cost factor 2^(gap/ScoreDelta)) gets no traffic even while the healthy pod
// has heavy concurrent in-flight load.
func TestDegradedPodIsolated(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	healthy, _ := reg.AddPod("healthy", "127.0.0.1:1")
	degraded, _ := reg.AddPod("degraded", "127.0.0.1:2")
	reg.ForceState("healthy", registry.StateActive)
	reg.ForceState("degraded", registry.StateActive)

	fs := featurestore.New(featurestore.NewMockSource())
	cfg := DefaultConfig()
	cfg.ShadowTrafficCapPct = 0
	px := New(cfg, eng, reg, fs, fallback.New(), discardLogger())

	// Teach model: healthy high reward (0.95), degraded low reward (0.10)
	x := buildContext(featurestore.PodFeatures{}, RequestInfo{Now: time.Now()})
	for i := 0; i < 100; i++ {
		_ = eng.Update(healthy.ArmID, x, 0.95)
		_ = eng.Update(degraded.ArmID, x, 0.10)
	}

	// Artificially load the healthy pod with 50 concurrent in-flight requests
	for i := 0; i < 50; i++ {
		healthy.Acquire()
	}
	defer func() {
		for i := 0; i < 50; i++ {
			healthy.Release()
		}
	}()

	// Even with 50 in-flight requests on healthy and 0 on degraded, degraded must NOT be chosen
	for i := 0; i < 100; i++ {
		chosen := px.route(httptest.NewRequest("GET", "/", nil))
		if chosen == nil {
			t.Fatal("expected pod, got nil")
		}
		if chosen.ID != "healthy" {
			t.Fatalf("degraded pod chosen despite its score gap: got %s", chosen.ID)
		}
	}
}

// TestSlightlyWorsePodSharesLoad verifies that a lower score is a load penalty,
// not an exclusion: a pod about 2.5 ScoreDelta below the best counts as 2 or 4
// times as loaded, so it loses while both are idle and takes traffic once the
// best pod has 4 in flight.
func TestSlightlyWorsePodSharesLoad(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	best, _ := reg.AddPod("best", "127.0.0.1:1")
	worse, _ := reg.AddPod("worse", "127.0.0.1:2")
	reg.ForceState("best", registry.StateActive)
	reg.ForceState("worse", registry.StateActive)

	cfg := DefaultConfig()
	cfg.ShadowTrafficCapPct = 0
	px := New(cfg, eng, reg, featurestore.New(featurestore.NewMockSource()), fallback.New(), discardLogger())

	x := buildContext(featurestore.PodFeatures{}, RequestInfo{Now: time.Now()})
	for i := 0; i < 100; i++ {
		_ = eng.Update(best.ArmID, x, 0.95)
		_ = eng.Update(worse.ArmID, x, 0.825)
	}

	for i := 0; i < 50; i++ {
		if got := px.route(httptest.NewRequest("GET", "/", nil)); got != best {
			t.Fatalf("idle pods: want best, got %v", got.ID)
		}
	}
	for i := 0; i < 4; i++ {
		best.Acquire()
		defer best.Release()
	}
	for i := 0; i < 50; i++ {
		if got := px.route(httptest.NewRequest("GET", "/", nil)); got != worse {
			t.Fatalf("best has 4 in flight: want worse, got %v", got.ID)
		}
	}
}

// TestPenaltyIsContinuousPastDeadZone: a pod about 1.5 ScoreDelta below the
// best gets 0 or 1 penalty steps, half the time each, so idle traffic splits
// about 3:1 instead of jumping between 1:1 and all-or-nothing.
func TestPenaltyIsContinuousPastDeadZone(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	best, _ := reg.AddPod("best", "127.0.0.1:1")
	worse, _ := reg.AddPod("worse", "127.0.0.1:2")
	reg.ForceState("best", registry.StateActive)
	reg.ForceState("worse", registry.StateActive)

	cfg := DefaultConfig()
	cfg.ShadowTrafficCapPct = 0
	px := New(cfg, eng, reg, featurestore.New(featurestore.NewMockSource()), fallback.New(), discardLogger())

	x := buildContext(featurestore.PodFeatures{}, RequestInfo{Now: time.Now()})
	for i := 0; i < 100; i++ {
		_ = eng.Update(best.ArmID, x, 0.95)
		_ = eng.Update(worse.ArmID, x, 0.875)
	}

	hits := 0
	for i := 0; i < 2000; i++ {
		if px.route(httptest.NewRequest("GET", "/", nil)) == worse {
			hits++
		}
	}
	if hits < 300 || hits > 700 {
		t.Fatalf("worse pod share %d/2000, want about 500", hits)
	}
}

// TestNearEqualPodsShareIdleTraffic: a gap under one ScoreDelta step is not a
// preference. Equally loaded pods tie and the tie is random; always giving it
// to the higher score sent ~260 req/s to one 250 req/s pod at 600 RPS.
func TestNearEqualPodsShareIdleTraffic(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	a, _ := reg.AddPod("a", "127.0.0.1:1")
	b, _ := reg.AddPod("b", "127.0.0.1:2")
	reg.ForceState("a", registry.StateActive)
	reg.ForceState("b", registry.StateActive)

	cfg := DefaultConfig()
	cfg.ShadowTrafficCapPct = 0
	px := New(cfg, eng, reg, featurestore.New(featurestore.NewMockSource()), fallback.New(), discardLogger())

	x := buildContext(featurestore.PodFeatures{}, RequestInfo{Now: time.Now()})
	for i := 0; i < 100; i++ {
		_ = eng.Update(a.ArmID, x, 0.95)
		_ = eng.Update(b.ArmID, x, 0.93)
	}

	hits := map[string]int{}
	for i := 0; i < 400; i++ {
		hits[px.route(httptest.NewRequest("GET", "/", nil)).ID]++
	}
	if hits["a"] < 140 || hits["b"] < 140 {
		t.Fatalf("near-equal idle pods should split traffic, got %v", hits)
	}
}

func Test503FaultIsolation(t *testing.T) {
	// JANUS-02 verification: pod failing 1-in-5 with 503 must receive < 10% traffic within 500 requests
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	healthy, _ := reg.AddPod("healthy", "127.0.0.1:9001")
	degraded, _ := reg.AddPod("degraded", "127.0.0.1:9002")
	reg.ForceState("healthy", registry.StateActive)
	reg.ForceState("degraded", registry.StateActive)

	fs := featurestore.New(featurestore.NewMockSource())
	px := New(DefaultConfig(), eng, reg, fs, fallback.New(), discardLogger())

	// Pretrain both pods identically with healthy rewards
	req := httptest.NewRequest("GET", "/", nil)
	x := buildContext(featurestore.PodFeatures{}, requestInfo(req))
	for i := 0; i < 50; i++ {
		_ = eng.Update(healthy.ArmID, x, 1.0)
		_ = eng.Update(degraded.ArmID, x, 1.0)
	}

	degradedHits := 0
	healthyHits := 0
	degradedFailCount := 0

	for i := 0; i < 500; i++ {
		chosen := px.route(req)
		if chosen == nil {
			t.Fatal("expected pod, got nil")
		}
		if chosen.ID == "degraded" {
			degradedHits++
			degradedFailCount++
			if degradedFailCount%5 == 0 {
				chosen.RecordResult(true) // 503 error
				_ = eng.Update(chosen.ArmID, x, 0.0)
			} else {
				chosen.RecordResult(false) // 200 OK
				_ = eng.Update(chosen.ArmID, x, 1.0)
			}
		} else {
			healthyHits++
			chosen.RecordResult(false) // 200 OK
			_ = eng.Update(chosen.ArmID, x, 1.0)
		}
	}

	degradedFraction := float64(degradedHits) / 500.0
	healthyFraction := float64(healthyHits) / 500.0

	if degradedFraction >= 0.10 {
		t.Fatalf("degraded pod got %.1f%% of traffic, want < 10%%", degradedFraction*100.0)
	}
	if healthyFraction <= 0.80 {
		t.Fatalf("healthy pod only got %.1f%% of traffic, want > 80%%", healthyFraction*100.0)
	}
}

func TestAllBackendsFailingFallback(t *testing.T) {
	// Risk 3 verification: when all backends are tripped, proxy must not drop traffic
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, discardLogger())
	podA, _ := reg.AddPod("pod-a", "127.0.0.1:9001")
	podB, _ := reg.AddPod("pod-b", "127.0.0.1:9002")
	reg.ForceState("pod-a", registry.StateActive)
	reg.ForceState("pod-b", registry.StateActive)

	fs := featurestore.New(featurestore.NewMockSource())
	px := New(DefaultConfig(), eng, reg, fs, fallback.New(), discardLogger())

	// Force trip both pods into StateOpen
	for i := 0; i < 10; i++ {
		podA.RecordResult(true)
		podB.RecordResult(true)
	}

	if podA.AllowRequest() || podB.AllowRequest() {
		t.Fatal("expected both pods to have tripped circuit breakers")
	}

	// Routing should fall back and not panic or return nil
	req := httptest.NewRequest("GET", "/", nil)
	chosen := px.route(req)
	if chosen == nil {
		t.Fatal("expected pod under outage fallback, got nil")
	}
}
