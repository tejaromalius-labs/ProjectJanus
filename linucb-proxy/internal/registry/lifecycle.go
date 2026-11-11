package registry

import (
	"context"
	"math"
	"time"
)

func float64bits(f float64) uint64   { return math.Float64bits(f) }
func bitsToFloat64(u uint64) float64 { return math.Float64frombits(u) }

// SetFleetMedianP99 updates the fleet-median p99 latency (ms) used as the
// SHADOW→ACTIVE graduation reference.
func (r *Registry) SetFleetMedianP99(ms float64) {
	r.fleetMedianP99Ms.Store(float64bits(ms))
}

func (r *Registry) fleetMedianP99() float64 {
	return bitsToFloat64(r.fleetMedianP99Ms.Load())
}

// latencyWithinTolerance reports whether a pod's p99 is within the configured
// percentage of the fleet median.
func (r *Registry) latencyWithinTolerance(e *PodEntry) bool {
	median := r.fleetMedianP99()
	if median <= 0 {
		return true // no baseline yet — don't block graduation
	}
	tol := median * (1.0 + r.cfg.ShadowLatencyTolerancePct/100.0)
	return e.PodP99() <= tol
}

// StartPromotionLoop runs the background lifecycle state machine. It polls
// observation counts and promotes pods WARMING→SHADOW→ACTIVE as they cross the
// configured thresholds. Runs until ctx is cancelled.
func (r *Registry) StartPromotionLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.PromotionPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
			r.promoteEligible()
		}
	}
}

// Stop halts the promotion loop.
func (r *Registry) Stop() { close(r.stop) }

func (r *Registry) promoteEligible() {
	r.pods.Range(func(_, v any) bool {
		e := v.(*PodEntry)
		nObs := e.NObs.Load()
		switch e.State() {
		case StateWarming:
			if nObs >= r.cfg.WarmupObservations {
				e.setState(StateShadow)
				r.log.Info("pod promoted to SHADOW", "pod", e.ID, "n_obs", nObs)
			}
		case StateShadow:
			if nObs >= r.cfg.ShadowObservations && r.latencyWithinTolerance(e) {
				e.setState(StateActive)
				r.log.Info("pod promoted to ACTIVE", "pod", e.ID, "n_obs", nObs)
			}
		}
		return true
	})
}
