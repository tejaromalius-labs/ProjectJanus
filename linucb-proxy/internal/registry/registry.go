// Package registry tracks backend pods and their LinUCB lifecycle state
// (WARMING / SHADOW / ACTIVE), driving the three-phase cold-start safety model.
package registry

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/circuitbreaker"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/linucb"
)

// PodState is the lifecycle phase of a backend pod.
type PodState int32

const (
	// StateWarming pods have fewer observations than the SHADOW threshold.
	// Like every state, they are scored for routing.
	StateWarming PodState = iota
	// StateShadow pods also receive the shadow injection share and must pass
	// the latency gate to become ACTIVE.
	StateShadow
	// StateActive pods participate fully in LinUCB routing with no traffic cap.
	StateActive
	// StateDraining pods are being terminated by Kubernetes.
	StateDraining
)

func (s PodState) String() string {
	switch s {
	case StateWarming:
		return "WARMING"
	case StateShadow:
		return "SHADOW"
	case StateActive:
		return "ACTIVE"
	case StateDraining:
		return "DRAINING"
	default:
		return "UNKNOWN"
	}
}

// PodEntry is the registry's record for one backend pod.
type PodEntry struct {
	ID       string
	Endpoint string // host:port
	ArmID    int    // LinUCB arm index in the engine

	state     atomic.Int32  // PodState, accessed atomically
	NObs      atomic.Uint64 // observation count (mirrors engine; drives promotion)
	RouteHits atomic.Uint64 // total requests routed to this pod
	inFlight  atomic.Int64  // active in-flight requests

	cb *circuitbreaker.CircuitBreaker

	// p99LatencyMs is the pod's recent p99 latency in ms, updated by telemetry;
	// used as the SHADOW→ACTIVE graduation gate.
	p99LatencyMs atomic.Uint64 // stored as math.Float64bits
}

func (e *PodEntry) State() PodState     { return PodState(e.state.Load()) }
func (e *PodEntry) setState(s PodState) { e.state.Store(int32(s)) }

// AllowRequest reports whether the circuit breaker permits routing to this pod.
func (e *PodEntry) AllowRequest() bool {
	if e.cb == nil {
		return true
	}
	return e.cb.AllowRequest()
}

// RecordResult feeds the outcome of a request back to the pod's circuit breaker.
func (e *PodEntry) RecordResult(failed bool) {
	if e.cb != nil {
		e.cb.RecordResult(failed)
	}
}

// CircuitBreaker returns the underlying circuit breaker instance for inspection/testing.
func (e *PodEntry) CircuitBreaker() *circuitbreaker.CircuitBreaker {
	return e.cb
}

// SetCircuitBreaker attaches a custom circuit breaker to the pod (used in tests).
func (e *PodEntry) SetCircuitBreaker(cb *circuitbreaker.CircuitBreaker) {
	e.cb = cb
}

// Acquire increments the in-flight request count for this pod.
func (e *PodEntry) Acquire() { e.inFlight.Add(1) }

// Release decrements the in-flight request count, clamping at zero.
func (e *PodEntry) Release() {
	if e.inFlight.Add(-1) < 0 {
		e.inFlight.Store(0)
	}
}

// InFlightCount returns the current active in-flight request count.
func (e *PodEntry) InFlightCount() int64 {
	v := e.inFlight.Load()
	if v < 0 {
		return 0
	}
	return v
}

// Config holds the lifecycle thresholds from the CRD.
type Config struct {
	WarmupObservations        uint64        // default 200
	ShadowObservations        uint64        // default 1000
	ShadowLatencyTolerancePct float64       // default 20
	PromotionPollInterval     time.Duration // how often to re-check promotion
}

// DefaultConfig returns the default lifecycle thresholds.
func DefaultConfig() Config {
	return Config{
		WarmupObservations:        200,
		ShadowObservations:        1000,
		ShadowLatencyTolerancePct: 20,
		PromotionPollInterval:     time.Second,
	}
}

// Registry is the concurrent set of known pods and their lifecycle state.
type Registry struct {
	cfg    Config
	engine *linucb.Engine
	log    *slog.Logger

	pods sync.Map // map[string]*PodEntry

	// fleetMedianP99Ms is the current fleet-median p99 latency; SHADOW pods
	// graduate only when within ShadowLatencyTolerancePct of it.
	fleetMedianP99Ms atomic.Uint64 // math.Float64bits

	stop chan struct{}
}

// New creates a registry bound to a LinUCB engine.
func New(cfg Config, engine *linucb.Engine, log *slog.Logger) *Registry {
	return &Registry{
		cfg:    cfg,
		engine: engine,
		log:    log,
		stop:   make(chan struct{}),
	}
}

// Get returns the entry for a pod ID.
func (r *Registry) Get(id string) (*PodEntry, bool) {
	v, ok := r.pods.Load(id)
	if !ok {
		return nil, false
	}
	return v.(*PodEntry), true
}

// PodsInState returns all pods currently in the given lifecycle state.
func (r *Registry) PodsInState(s PodState) []*PodEntry {
	var out []*PodEntry
	r.pods.Range(func(_, v any) bool {
		e := v.(*PodEntry)
		if e.State() == s {
			out = append(out, e)
		}
		return true
	})
	return out
}

// All returns every registered pod entry.
func (r *Registry) All() []*PodEntry {
	var out []*PodEntry
	r.pods.Range(func(_, v any) bool {
		out = append(out, v.(*PodEntry))
		return true
	})
	return out
}

// AddPod registers a new backend pod. Newly added pods always start WARMING and
// are hard-excluded from scoring until warmed.
func (r *Registry) AddPod(id, endpoint string) (*PodEntry, error) {
	if e, ok := r.Get(id); ok {
		return e, nil
	}
	armID, err := r.engine.AddArm(id)
	if err != nil {
		return nil, err
	}
	e := &PodEntry{
		ID:       id,
		Endpoint: endpoint,
		ArmID:    armID,
		cb:       circuitbreaker.New(),
	}
	e.setState(StateWarming)
	r.pods.Store(id, e)
	r.log.Info("pod registered", "pod", id, "endpoint", endpoint, "state", "WARMING")
	return e, nil
}

// ForceState overrides a pod's lifecycle state. Intended for local demos and
// tests only — production state transitions go through the promotion loop.
func (r *Registry) ForceState(id string, s PodState) {
	if e, ok := r.Get(id); ok {
		e.setState(s)
	}
}

// RemovePod marks a pod draining, removes it from routing, and reclaims its
// LinUCB arm. The arm is reset in place (its learned state is freed) rather than
// deleted, so the arm index stays stable for all other pods — arm IDs are
// positional and must never shift (see linucb.Engine.ResetArm).
//
// Ordering is best-effort: we delete from the routing map first so no new
// request picks this pod, then reset the arm. A reward update already in flight
// on the telemetry worker may land after the reset, harmlessly re-dirtying a
// slot that is about to go idle — the index is never reused by another pod, so
// there is no cross-contamination. We deliberately avoid cross-goroutine locking
// here to preserve the lock-free hot path.
func (r *Registry) RemovePod(id string) {
	if e, ok := r.Get(id); ok {
		e.setState(StateDraining)
		r.pods.Delete(id)
		if err := r.engine.ResetArm(e.ArmID, ""); err != nil {
			r.log.Warn("arm reset on pod removal failed", "pod", id, "arm", e.ArmID, "err", err)
		}
		r.log.Info("pod removed", "pod", id, "arm", e.ArmID)
	}
}

// RecordObservation increments a pod's observation count after a reward update.
func (r *Registry) RecordObservation(id string) {
	if e, ok := r.Get(id); ok {
		e.NObs.Add(1)
	}
}

// SetPodP99 records a pod's recent p99 latency (ms), used for graduation.
func (e *PodEntry) SetPodP99(ms float64) { e.p99LatencyMs.Store(float64bits(ms)) }

// PodP99 returns the pod's recent p99 latency in ms.
func (e *PodEntry) PodP99() float64 { return bitsToFloat64(e.p99LatencyMs.Load()) }

// Available reports whether a pod can be selected without claiming a probe.
func (e *PodEntry) Available() bool {
	return e.State() != StateDraining && (e.cb == nil || e.cb.Available())
}
