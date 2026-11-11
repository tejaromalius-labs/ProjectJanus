//go:build k8s

package registry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

type endpointSliceDiscovery struct {
	client    kubernetes.Interface
	service   string
	namespace string
	log       *slog.Logger
}

func NewK8sDiscovery(service, namespace string, log *slog.Logger) (Discovery, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	return &endpointSliceDiscovery{
		client:    client,
		service:   service,
		namespace: namespace,
		log:       log,
	}, nil
}

func (d *endpointSliceDiscovery) Run(ctx context.Context, reg *Registry) error {
	factory := informers.NewSharedInformerFactoryWithOptions(
		d.client,
		30*time.Second,
		informers.WithNamespace(d.namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = "kubernetes.io/service-name=" + d.service
		}),
	)

	esInformer := factory.Discovery().V1().EndpointSlices()

	esInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			eps, ok := obj.(*discoveryv1.EndpointSlice)
			if !ok {
				return
			}
			for podID, endpoint := range readyEndpoints(eps) {
				if _, err := reg.AddPod(podID, endpoint); err != nil {
					d.log.Error("failed to add pod from EndpointSlice", "pod", podID, "err", err)
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldEps, ok := oldObj.(*discoveryv1.EndpointSlice)
			if !ok {
				return
			}
			newEps, ok := newObj.(*discoveryv1.EndpointSlice)
			if !ok {
				return
			}
			oldMap := readyEndpoints(oldEps)
			newMap := readyEndpoints(newEps)

			// Add or update pods that are in newMap.
			for podID, endpoint := range newMap {
				if oldEndpoint, exists := oldMap[podID]; !exists || oldEndpoint != endpoint {
					// New or changed endpoint.
					if exists {
						reg.RemovePod(podID)
					}
					if _, err := reg.AddPod(podID, endpoint); err != nil {
						d.log.Error("failed to add/update pod from EndpointSlice", "pod", podID, "err", err)
					}
				}
			}

			// Remove pods that are no longer in newMap.
			for podID := range oldMap {
				if _, exists := newMap[podID]; !exists {
					reg.RemovePod(podID)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			eps, ok := obj.(*discoveryv1.EndpointSlice)
			if !ok {
				return
			}
			for podID := range readyEndpoints(eps) {
				reg.RemovePod(podID)
			}
		},
	})

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), esInformer.Informer().HasSynced) {
		return ctx.Err()
	}

	d.log.Info("endpoint slice cache synced, watching for pod events", "service", d.service, "namespace", d.namespace)
	<-ctx.Done()
	return ctx.Err()
}

func readyEndpoints(eps *discoveryv1.EndpointSlice) map[string]string {
	result := make(map[string]string)
	if eps == nil {
		return result
	}

	// Extract port from the slice's ports.
	port, ok := firstPort(eps.Ports)
	if !ok {
		return result
	}

	for _, endpoint := range eps.Endpoints {
		// Skip if no target reference.
		if endpoint.TargetRef == nil {
			continue
		}

		// Skip if no addresses.
		if len(endpoint.Addresses) == 0 {
			continue
		}

		// Skip if not ready.
		if endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready {
			continue
		}

		podID := endpoint.TargetRef.Name
		addr := endpoint.Addresses[0]
		endpoint := addr + ":" + port
		result[podID] = endpoint
	}

	return result
}

func firstPort(ports []discoveryv1.EndpointPort) (string, bool) {
	if len(ports) == 0 {
		return "", false
	}
	if ports[0].Port == nil {
		return "", false
	}
	return fmt.Sprintf("%d", *ports[0].Port), true
}
