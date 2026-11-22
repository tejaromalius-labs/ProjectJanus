package telemetry

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/featurestore"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/proxy"
)

// RequestOutcome is the observed HTTP result of one proxied request. TCP
// connection diagnostics are recorded separately and never become rewards.
type RequestOutcome struct {
	PodID      string
	LatencyMs  float64
	HTTPStatus int
	Req        proxy.RequestInfo
}

// Keep the most recent 256 observations per pod. Nearest-rank p99 is
// recomputed on a bounded copy over the samples no older than
// featurestore.LatencyMaxAge; no unbounded history or background timer.
const latencyWindowSize = 256

type latencyWindow struct {
	values      [latencyWindowSize]float64
	at          [latencyWindowSize]time.Time
	next, count int
}

// latencyNorm maps an observed rolling p99 (ms) into the [0,1] range used by the
// context vector. We scale against 10×SLA so a backend at the SLA reads ~0.1 and
// one an order of magnitude over saturates at 1.0; this keeps index 11 on the
// same scale as the other context features so theta weights it sanely.
func latencyNorm(p99Ms, slaMs float64) float64 {
	if slaMs <= 0 {
		slaMs = 100
	}
	v := p99Ms / (slaMs * 10)
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Collector turns request outcomes into reward observations and enqueues them
// for the UpdateWorker via a non-blocking send (drop on backpressure).
//
// It also feeds observed latency back into the feature store as a per-pod rolling p99 so
// the LinUCB context vector (index 11, P99LatencyEMA) actually carries the signal
// that distinguishes a fast backend from a slow one. Without this the context is
// identical across pods and the bandit cannot separate the arms.
type Collector struct {
	rewardCfg proxy.RewardConfig
	features  *featurestore.Store
	updates   chan<- ArmUpdate

	mu          sync.Mutex
	latencies   map[string]*latencyWindow
	connections map[string]ConnectionStats

	dropped atomic.Uint64
	enq     atomic.Uint64
}

// NewCollector creates a collector that writes ArmUpdates to the given channel.
func NewCollector(
	rewardCfg proxy.RewardConfig,
	features *featurestore.Store,
	updates chan<- ArmUpdate,
) *Collector {
	return &Collector{
		rewardCfg:   rewardCfg,
		features:    features,
		updates:     updates,
		latencies:   make(map[string]*latencyWindow),
		connections: make(map[string]ConnectionStats),
	}
}

// updateLatencyFeature stores a true rolling p99 and its normalized context value.
func (c *Collector) updateLatencyFeature(podID string, latencyMs float64) {
	if math.IsNaN(latencyMs) || math.IsInf(latencyMs, 0) || latencyMs < 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	window := c.latencies[podID]
	if window == nil {
		window = &latencyWindow{}
		c.latencies[podID] = window
	}
	now := time.Now()
	window.values[window.next] = latencyMs
	window.at[window.next] = now
	window.next = (window.next + 1) % latencyWindowSize
	if window.count < latencyWindowSize {
		window.count++
	}
	// Samples from before a gap in traffic (such as a fault the pod has since
	// recovered from) must not outvote fresh ones. The new sample is always kept.
	var values [latencyWindowSize]float64
	n := 0
	for i := 0; i < window.count; i++ {
		if now.Sub(window.at[i]) <= featurestore.LatencyMaxAge {
			values[n] = window.values[i]
			n++
		}
	}
	sort.Float64s(values[:n])
	p99 := values[int(math.Ceil(0.99*float64(n)))-1]
	c.features.SetLatency(podID, p99, latencyNorm(p99, c.rewardCfg.SLATargetMs))
}

// Observe computes the reward for an outcome and enqueues it. Non-blocking: if
// the update channel is full the observation is dropped (backpressure is OK —
// the model tolerates missed updates).
func (c *Collector) Observe(o RequestOutcome) {
	c.updateLatencyFeature(o.PodID, o.LatencyMs)

	f, _ := c.features.Get(o.PodID)
	reward := proxy.ComputeReward(o.LatencyMs, o.HTTPStatus, f.CPUUtilization, c.rewardCfg)

	select {
	case c.updates <- ArmUpdate{PodID: o.PodID, Reward: reward, Req: o.Req}:
		c.enq.Add(1)
	default:
		c.dropped.Add(1)
	}
}

// Dropped returns the number of dropped observations (backpressure metric).
func (c *Collector) Dropped() uint64 { return c.dropped.Load() }

// Enqueued returns the number of successfully enqueued observations.
func (c *Collector) Enqueued() uint64 { return c.enq.Load() }
