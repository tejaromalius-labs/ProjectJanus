// Package fallback provides the deterministic routing policy used when the
// LinUCB decision is too slow or no pod qualifies.
package fallback

import "github.com/giltas/projectjanus/linucb-proxy/internal/registry"

// LeastConnections picks the active pod with the fewest in-flight requests.
type LeastConnections struct{}

// New creates a least-connections picker.
func New() *LeastConnections { return &LeastConnections{} }

// Pick returns the active pod with the fewest in-flight requests, or nil if
// none are available. Lock-free via PodEntry.InFlightCount.
func (l *LeastConnections) Pick(pods []*registry.PodEntry) *registry.PodEntry {
	if len(pods) == 0 {
		return nil
	}
	var best *registry.PodEntry
	bestN := int64(^uint64(0) >> 1) // max int64
	for _, p := range pods {
		if n := p.InFlightCount(); n < bestN {
			bestN = n
			best = p
		}
	}
	if best != nil {
		return best
	}
	return pods[0]
}
