package featurestore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PrometheusSource queries normalized per-pod resource features. Application
// metrics are optional: an absent series contributes zero, not a scrape error.
type PrometheusSource struct {
	endpoint  string
	namespace string
	client    *http.Client
}

func NewPrometheusSource(endpoint, namespace string) (*PrometheusSource, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid Prometheus URL")
	}
	return &PrometheusSource{endpoint: strings.TrimRight(endpoint, "/"), namespace: namespace, client: &http.Client{Timeout: 3 * time.Second}}, nil
}

func (s *PrometheusSource) Scrape(ctx context.Context) (map[string]PodFeatures, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	label := "namespace=" + strconv.Quote(s.namespace)
	queries := []struct {
		expr string
		set  func(*PodFeatures, float64)
	}{
		{`sum by(pod)(rate(container_cpu_usage_seconds_total{` + label + `,container!="",container!="POD"}[1m])) / on(pod) sum by(pod)(kube_pod_container_resource_limits{` + label + `,resource="cpu",unit="core"})`, func(f *PodFeatures, v float64) { f.CPUUtilization = v }},
		{`sum by(pod)(container_memory_working_set_bytes{` + label + `,container!="",container!="POD"}) / on(pod) sum by(pod)(kube_pod_container_resource_limits{` + label + `,resource="memory",unit="byte"})`, func(f *PodFeatures, v float64) { f.MemorySaturation = v }},
		{`max by(pod)(janus_app_active_connections_ratio{` + label + `})`, func(f *PodFeatures, v float64) { f.ActiveConns = v }},
		{`max by(pod)(janus_app_queue_depth_ratio{` + label + `})`, func(f *PodFeatures, v float64) { f.QueueDepth = v }},
		{`max by(pod)(rate(go_gc_duration_seconds_sum{` + label + `}[1m]))`, func(f *PodFeatures, v float64) { f.GCPauseRate = v }},
	}
	out := make(map[string]PodFeatures)
	for _, q := range queries {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint+"/api/v1/query?"+url.Values{"query": {q.expr}}.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, err
		}
		var result struct {
			Status string `json:"status"`
			Data   struct {
				ResultType string `json:"resultType"`
				Result     []struct {
					Metric map[string]string `json:"metric"`
					Value  []json.RawMessage `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}
		err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4<<20)).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode Prometheus response: %w", err)
		}
		if resp.StatusCode != http.StatusOK || result.Status != "success" || result.Data.ResultType != "vector" {
			return nil, fmt.Errorf("Prometheus query failed: HTTP %d status %q", resp.StatusCode, result.Status)
		}
		for _, item := range result.Data.Result {
			pod := item.Metric["pod"]
			if pod == "" || len(item.Value) != 2 {
				continue
			}
			var value string
			if err := json.Unmarshal(item.Value[1], &value); err != nil {
				return nil, err
			}
			v, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			f := out[pod]
			q.set(&f, math.Max(0, math.Min(1, v)))
			out[pod] = f
		}
	}
	return out, nil
}
