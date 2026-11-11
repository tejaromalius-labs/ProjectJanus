//go:build k8s

package registry

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/giltas/projectjanus/linucb-proxy/internal/linucb"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestK8sDiscoveryAddsReadyPod(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	client := fake.NewSimpleClientset()
	d := &endpointSliceDiscovery{
		client:    client,
		service:   "my-svc",
		namespace: "default",
		log:       discard(),
	}
	reg := New(DefaultConfig(), eng, discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start discovery in background.
	go func() {
		if err := d.Run(ctx, reg); err != nil && ctx.Err() == nil {
			t.Errorf("discovery error: %v", err)
		}
	}()

	// Give the informer time to sync.
	time.Sleep(500 * time.Millisecond)

	// Create an EndpointSlice with a ready endpoint.
	port := int32(8080)
	eps := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-svc-abc",
			Namespace: "default",
			Labels: map[string]string{
				"kubernetes.io/service-name": "my-svc",
			},
		},
		Ports: []discoveryv1.EndpointPort{
			{
				Port: &port,
			},
		},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses: []string{"10.0.0.1"},
				TargetRef: &corev1.ObjectReference{
					Name: "pod-1",
				},
				Conditions: discoveryv1.EndpointConditions{
					Ready: boolPtr(true),
				},
			},
		},
	}

	_, createErr := client.DiscoveryV1().EndpointSlices("default").Create(ctx, eps, metav1.CreateOptions{})
	if createErr != nil {
		t.Fatalf("failed to create EndpointSlice: %v", createErr)
	}

	// Wait for the pod to appear in the registry.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("pod-1"); ok {
			cancel()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("pod-1 was not added to registry after EndpointSlice creation")
}

func TestK8sDiscoveryRemovesPodOnDelete(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	client := fake.NewSimpleClientset()
	d := &endpointSliceDiscovery{
		client:    client,
		service:   "my-svc",
		namespace: "default",
		log:       discard(),
	}
	reg := New(DefaultConfig(), eng, discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start discovery.
	go func() {
		if err := d.Run(ctx, reg); err != nil && ctx.Err() == nil {
			t.Errorf("discovery error: %v", err)
		}
	}()

	time.Sleep(500 * time.Millisecond)

	// Create EndpointSlice.
	port := int32(8080)
	eps := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-svc-abc",
			Namespace: "default",
			Labels: map[string]string{
				"kubernetes.io/service-name": "my-svc",
			},
		},
		Ports: []discoveryv1.EndpointPort{{Port: &port}},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.1"},
				TargetRef:  &corev1.ObjectReference{Name: "pod-1"},
				Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)},
			},
		},
	}
	_, createErr := client.DiscoveryV1().EndpointSlices("default").Create(ctx, eps, metav1.CreateOptions{})
	if createErr != nil {
		t.Fatalf("create: %v", createErr)
	}

	// Wait for pod to appear.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("pod-1"); ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Delete the EndpointSlice.
	err = client.DiscoveryV1().EndpointSlices("default").Delete(ctx, eps.Name, metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Wait for pod to be removed.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("pod-1"); !ok {
			cancel()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("pod-1 was not removed from registry after EndpointSlice deletion")
}

func TestK8sDiscoverySkipsNotReadyEndpoints(t *testing.T) {
	eng, err := linucb.NewEngine(linucb.D, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	client := fake.NewSimpleClientset()
	d := &endpointSliceDiscovery{
		client:    client,
		service:   "my-svc",
		namespace: "default",
		log:       discard(),
	}
	reg := New(DefaultConfig(), eng, discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := d.Run(ctx, reg); err != nil && ctx.Err() == nil {
			t.Errorf("discovery error: %v", err)
		}
	}()

	time.Sleep(500 * time.Millisecond)

	// Create EndpointSlice with a NOT ready endpoint.
	port := int32(8080)
	eps := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-svc-abc",
			Namespace: "default",
			Labels: map[string]string{
				"kubernetes.io/service-name": "my-svc",
			},
		},
		Ports: []discoveryv1.EndpointPort{{Port: &port}},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.1"},
				TargetRef:  &corev1.ObjectReference{Name: "pod-1"},
				Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(false)},
			},
		},
	}
	_, createErr := client.DiscoveryV1().EndpointSlices("default").Create(ctx, eps, metav1.CreateOptions{})
	if createErr != nil {
		t.Fatalf("create: %v", createErr)
	}

	// Pod should not be added.
	time.Sleep(500 * time.Millisecond)
	if _, ok := reg.Get("pod-1"); ok {
		t.Fatal("not-ready pod was added to registry; should have been skipped")
	}

	cancel()
}

func boolPtr(b bool) *bool {
	return &b
}
