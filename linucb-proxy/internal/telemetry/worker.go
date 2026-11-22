// Package telemetry collects request outcomes and feeds reward observations
// back into the LinUCB model. The update worker serializes writes per
// arm so the Sherman-Morrison update needs no per-arm mutex.
package telemetry

import (
	"context"
	"log/slog"
	"sort"

	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/featurestore"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/linucb"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/proxy"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/registry"
)

// ArmUpdate is one reward observation queued for application to a pod's arm.
type ArmUpdate struct {
	PodID  string
	Reward float64
	Req    proxy.RequestInfo
}

// UpdateWorker applies queued reward observations to LinUCB arms. The design
// runs one logical writer per arm; because all updates flow through a single
// worker goroutine here, writes to a given arm are inherently serialized.
// Independent arms remain independent inside the Rust core's RCU.
type UpdateWorker struct {
	engine   *linucb.Engine
	reg      *registry.Registry
	features *featurestore.Store
	updates  <-chan ArmUpdate
	log      *slog.Logger

	applied uint64
}

// NewUpdateWorker creates a worker reading from the given update channel.
func NewUpdateWorker(
	engine *linucb.Engine,
	reg *registry.Registry,
	features *featurestore.Store,
	updates <-chan ArmUpdate,
	log *slog.Logger,
) *UpdateWorker {
	return &UpdateWorker{
		engine:   engine,
		reg:      reg,
		features: features,
		updates:  updates,
		log:      log,
	}
}

// Run consumes updates until ctx is cancelled.
func (w *UpdateWorker) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-w.updates:
			if !ok {
				return
			}
			w.apply(u)
		}
	}
}

func (w *UpdateWorker) apply(u ArmUpdate) {
	entry, ok := w.reg.Get(u.PodID)
	if !ok {
		return // pod deregistered between request and reward
	}
	f, _ := w.features.Get(u.PodID)
	x := proxy.BuildContextVector(u.Req.Features, u.Req)

	if err := w.engine.Update(entry.ArmID, x, u.Reward); err != nil {
		w.log.Warn("linucb update failed", "pod", u.PodID, "err", err)
		return
	}
	w.reg.RecordObservation(u.PodID)
	w.applied++

	// The promotion gate compares unnormalized rolling p99 values in ms.
	entry.SetPodP99(f.P99LatencyMs)
	w.refreshFleetMedianP99()
}

// refreshFleetMedianP99 recomputes the median p99 across ACTIVE pods that have a
// latency sample, and publishes it as the SHADOW→ACTIVE graduation reference.
// Newcomers are compared with the established fleet, not with each other; with
// no ACTIVE sample it publishes 0, which leaves the gate open.
func (w *UpdateWorker) refreshFleetMedianP99() {
	pods := w.reg.All()
	vals := make([]float64, 0, len(pods))
	for _, p := range pods {
		if v := p.PodP99(); v > 0 && p.State() == registry.StateActive {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		w.reg.SetFleetMedianP99(0)
		return
	}
	sort.Float64s(vals)
	median := vals[len(vals)/2]
	if len(vals)%2 == 0 {
		median = (vals[len(vals)/2-1] + vals[len(vals)/2]) / 2
	}
	w.reg.SetFleetMedianP99(median)
}

// Applied returns the number of successfully applied updates (for tests/metrics).
func (w *UpdateWorker) Applied() uint64 { return w.applied }
