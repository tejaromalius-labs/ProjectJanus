// Command proxy runs the Project Janus LinUCB load balancer.
//
// By default it runs with in-memory mock discovery and a mock feature source so
// it can be exercised locally without a Kubernetes cluster. Real EndpointSlice discovery and Prometheus scraping are wired
// in behind build tags / flags for cluster deployment.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/fallback"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/featurestore"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/linucb"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/proxy"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/registry"
	"github.com/tejaromalius-labs/ProjectJanus/linucb-proxy/internal/telemetry"
)

func namespaceFromEnv() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	return "default"
}

func main() {
	listen := flag.String("listen", ":8080", "address to listen on")
	alpha := flag.Float64("alpha", 0.15, "LinUCB exploration weight")
	gamma := flag.Float64("gamma", 0.999, "forgetting factor in (0,1]; 1.0=no forgetting, 0.999≈1000-obs window")
	service := flag.String("service", "", "Kubernetes Service name to watch for EndpointSlice discovery (empty = mock discovery)")
	ebpfObject := flag.String("ebpf-object", "", "compiled BPF ELF for optional TCP connection diagnostics; requires -tags ebpf and kernel privileges")
	prometheusURL := flag.String("prometheus-url", "", "Prometheus base URL for live per-pod resource features (empty = response telemetry only)")
	featureInterval := flag.Duration("feature-interval", 5*time.Second, "resource telemetry refresh interval")
	namespace := flag.String("namespace", namespaceFromEnv(), "Kubernetes namespace to watch")
	backends := flag.String("backends", "backend-a=127.0.0.1:9001,backend-b=127.0.0.1:9002", "mock discovery backends as id=host:port,... (used when -service is empty)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	eng, err := linucb.NewEngineWithGamma(linucb.D, *alpha, *gamma)
	if err != nil {
		log.Error("failed to create LinUCB engine", "err", err)
		os.Exit(1)
	}
	defer eng.Close()

	reg := registry.New(registry.DefaultConfig(), eng, log)

	var src featurestore.Source = featurestore.NewMockSource()
	if *featureInterval <= 0 {
		log.Error("feature-interval must be positive")
		os.Exit(1)
	}
	if *prometheusURL != "" {
		src, err = featurestore.NewPrometheusSource(*prometheusURL, *namespace)
		if err != nil {
			log.Error("invalid resource source", "err", err)
			os.Exit(1)
		}
	} else {
		log.Warn("resource telemetry disabled: set -prometheus-url for CPU/memory/application features")
	}
	fs := featurestore.New(src)

	lc := fallback.New()
	px := proxy.New(proxy.DefaultConfig(), eng, reg, fs, lc, log)

	// Telemetry: in-proxy reward path. Each proxied request's outcome is turned
	// into a reward observation and applied to the model by the update worker.
	// One worker drains the channel, serializing per-arm writes.
	updateCh := make(chan telemetry.ArmUpdate, 4096)
	collector := telemetry.NewCollector(proxy.DefaultRewardConfig(), fs, updateCh)
	worker := telemetry.NewUpdateWorker(eng, reg, fs, updateCh, log)
	px.SetTelemetryStats(func() (uint64, uint64, int) { return collector.Enqueued(), collector.Dropped(), len(updateCh) })
	px.SetObserver(func(o proxy.Outcome) {
		collector.Observe(telemetry.RequestOutcome{
			PodID:      o.PodID,
			LatencyMs:  o.LatencyMs,
			HTTPStatus: o.HTTPStatus,
			Req:        o.Req,
		})
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Background loops.
	go reg.StartPromotionLoop(ctx)
	go fs.StartRefreshLoop(ctx, *featureInterval)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); worker.Run(ctx) }()
	defer func() { stop(); <-workerDone }()
	if *ebpfObject != "" {
		resolve := func(endpoint string) string {
			for _, pod := range reg.All() {
				if pod.Endpoint == endpoint {
					return pod.ID
				}
			}
			return ""
		}
		go func() {
			if err := telemetry.RunEBPFSource(ctx, *ebpfObject, collector, resolve, log); err != nil && ctx.Err() == nil {
				log.Error("eBPF connection telemetry stopped", "err", err)
			}
		}()
	}
	px.SetConnectionMetrics(func(w http.ResponseWriter) {
		for pod, stats := range collector.Connections() {
			fmt.Fprintf(w, "janus_backend_connections_total{pod=%q} %d\njanus_backend_connection_duration_seconds_total{pod=%q} %g\n", pod, stats.Count, pod, stats.Seconds)
		}
	})

	// Discovery: use Kubernetes EndpointSlice if -service is provided, otherwise mock.
	var disco registry.Discovery
	if *service != "" {
		var err error
		disco, err = registry.NewK8sDiscovery(*service, *namespace, log)
		if err != nil {
			log.Error("failed to create k8s discovery", "err", err)
			os.Exit(1)
		}
		log.Info("k8s discovery enabled", "service", *service, "namespace", *namespace)
	} else {
		var seed []registry.PodEvent
		for _, entry := range strings.Split(*backends, ",") {
			id, endpoint, ok := strings.Cut(strings.TrimSpace(entry), "=")
			if !ok || id == "" || endpoint == "" {
				log.Error("invalid -backends entry, want id=host:port", "entry", entry)
				os.Exit(1)
			}
			seed = append(seed, registry.PodEvent{Added: true, ID: id, Endpoint: endpoint})
		}
		disco = registry.NewMockDiscovery(seed...)
		log.Info("mock discovery enabled (no -service flag)", "backends", *backends)
	}
	go func() {
		if err := disco.Run(ctx, reg); err != nil && ctx.Err() == nil {
			log.Error("discovery stopped", "err", err)
		}
	}()

	srv := &http.Server{Addr: *listen, Handler: px}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("janus proxy listening", "addr", *listen, "alpha", *alpha, "gamma", *gamma)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
	log.Info("janus proxy stopped")
}
