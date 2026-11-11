package featurestore

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPrometheusFeaturesAndLatencyOwnership(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, `namespace="test"`) {
			t.Errorf("missing namespace: %s", query)
		}
		value := "0.4"
		if strings.Contains(query, "container_cpu") {
			value = "1.8"
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"pod":"p1"},"value":[1,%q]}]}}`, value)
	}))
	defer server.Close()
	source, err := NewPrometheusSource(server.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	store := New(source)
	store.SetLatency("p1", 200, 0.2)
	store.refresh(context.Background())
	f, _ := store.Get("p1")
	if f.CPUUtilization != 1 || f.MemorySaturation != 0.4 || f.ActiveConns != 0.4 || f.QueueDepth != 0.4 || f.GCPauseRate != 0.4 {
		t.Fatalf("resource features: %+v", f)
	}
	if f.P99LatencyMs != 200 || f.P99LatencyEMA != 0.2 {
		t.Fatalf("scrape overwrote latency: %+v", f)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			store.refresh(context.Background())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			store.SetLatency("p1", 300, 0.3)
		}
	}()
	wg.Wait()
	f, _ = store.Get("p1")
	if f.P99LatencyMs != 300 || f.CPUUtilization != 1 {
		t.Fatalf("concurrent fields lost: %+v", f)
	}
}

func TestPrometheusFailurePreservesCache(t *testing.T) {
	for _, response := range []string{`{"status":"error"}`, `bad json`, `{"status":"success","data":{"resultType":"matrix"}}`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
			defer server.Close()
			source, _ := NewPrometheusSource(server.URL, "test")
			store := New(source)
			store.Set("p1", PodFeatures{CPUUtilization: 0.6})
			store.refresh(context.Background())
			f, _ := store.Get("p1")
			if f.CPUUtilization != 0.6 {
				t.Fatalf("cache changed: %+v", f)
			}
		})
	}
}

func TestPrometheusRejectsInvalidURL(t *testing.T) {
	for _, endpoint := range []string{"", "file:///tmp/data", "http://", "http://localhost?query=bad"} {
		if _, err := NewPrometheusSource(endpoint, "test"); err == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
}
