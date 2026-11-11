package featurestore

import (
	"context"
	"sync"
)

// MockSource returns canned per-pod features for local runs. Update the values
// at runtime via Set to simulate a degrading pod.
type MockSource struct {
	mu   sync.Mutex
	data map[string]PodFeatures
}

// NewMockSource creates an empty mock source.
func NewMockSource() *MockSource {
	return &MockSource{data: make(map[string]PodFeatures)}
}

// Set assigns features for a pod (simulates a Prometheus reading).
func (m *MockSource) Set(podID string, f PodFeatures) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[podID] = f
}

// Scrape returns a copy of the current canned features.
func (m *MockSource) Scrape(_ context.Context) (map[string]PodFeatures, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]PodFeatures, len(m.data))
	for k, v := range m.data {
		out[k] = v
	}
	return out, nil
}
