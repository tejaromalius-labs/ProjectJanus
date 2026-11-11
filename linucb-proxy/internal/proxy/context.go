package proxy

import (
	"math"
	"time"

	"github.com/giltas/projectjanus/linucb-proxy/internal/featurestore"
	"github.com/giltas/projectjanus/linucb-proxy/internal/linucb"
)

// RequestInfo carries the per-request characteristics that feed the context
// vector.
type RequestInfo struct {
	PayloadLogNorm float64 // log(bytes)/log(max_bytes), already in [0,1]
	IsMutation     bool    // POST/PUT/PATCH/DELETE
	Now            time.Time
	// Features are the pod's features when the request was dispatched. The
	// update must train on the context the pod was routed at, not on features
	// refreshed while the request ran.
	Features featurestore.PodFeatures
}

// BuildContextVector assembles the d=12 context vector from pod features and
// request info. Exported for the telemetry worker, which must build the exact
// same vector used at routing time. See buildContext for the index contract.
func BuildContextVector(f featurestore.PodFeatures, req RequestInfo) []float64 {
	return buildContext(f, req)
}

// buildContext assembles the d=12 context vector. Routing and the telemetry
// worker must use the same index order, or updates train the wrong weights.
func buildContext(f featurestore.PodFeatures, req RequestInfo) []float64 {
	hourAngle := 2 * math.Pi * float64(req.Now.Hour()) / 24.0
	dowAngle := 2 * math.Pi * float64(int(req.Now.Weekday())) / 7.0

	mutation := 0.0
	if req.IsMutation {
		mutation = 1.0
	}

	x := make([]float64, linucb.D)
	x[0] = f.CPUUtilization
	x[1] = f.MemorySaturation
	x[2] = f.ActiveConns
	x[3] = f.QueueDepth
	x[4] = f.GCPauseRate
	x[5] = req.PayloadLogNorm
	x[6] = mutation
	x[7] = math.Sin(hourAngle)
	x[8] = math.Cos(hourAngle)
	x[9] = math.Sin(dowAngle)
	x[10] = math.Cos(dowAngle)
	x[11] = f.P99LatencyEMA
	return x
}
