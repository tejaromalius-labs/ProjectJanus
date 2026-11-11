package registry

import "context"

// Discovery feeds pod add/remove events into the registry. Implementations:
//   - mockDiscovery (discovery_mock.go): in-memory, for local runs/tests.
//   - endpointSliceDiscovery (discovery_k8s.go, build tag `k8s`): client-go
//     EndpointSlice informer for real clusters.
type Discovery interface {
	// Run blocks until ctx is cancelled, calling reg.AddPod / reg.RemovePod as
	// backend endpoints appear and disappear.
	Run(ctx context.Context, reg *Registry) error
}
