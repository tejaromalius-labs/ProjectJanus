package registry

import (
	"context"
)

// PodEvent is a synthetic discovery event for the mock.
type PodEvent struct {
	Added    bool // true = add, false = remove
	ID       string
	Endpoint string
}

// MockDiscovery is an in-memory Discovery used for local runs and tests — it
// lets the proxy operate without a real Kubernetes cluster. Push events via
// the Events channel.
type MockDiscovery struct {
	Events chan PodEvent
}

// NewMockDiscovery returns a mock with seeded backends already added, plus an
// open channel for further events.
func NewMockDiscovery(seed ...PodEvent) *MockDiscovery {
	m := &MockDiscovery{Events: make(chan PodEvent, 64)}
	for _, e := range seed {
		m.Events <- e
	}
	return m
}

// Run applies events to the registry until ctx is cancelled.
func (m *MockDiscovery) Run(ctx context.Context, reg *Registry) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-m.Events:
			if ev.Added {
				_, _ = reg.AddPod(ev.ID, ev.Endpoint)
			} else {
				reg.RemovePod(ev.ID)
			}
		}
	}
}
