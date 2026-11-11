package main

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"
)

// The response must decode the way linucb-proxy/internal/featurestore/source_prometheus.go reads it.
func TestVectorMatchesJanusPrometheusDecoder(t *testing.T) {
	var buf bytes.Buffer
	writeVector(&buf, map[string]float64{"backend-0": 0.25})
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
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.Data.ResultType != "vector" || len(result.Data.Result) != 1 {
		t.Fatalf("unexpected shape: %s", buf.String())
	}
	item := result.Data.Result[0]
	var value string
	if err := json.Unmarshal(item.Value[1], &value); err != nil {
		t.Fatalf("value must be a JSON string: %v", err)
	}
	if v, _ := strconv.ParseFloat(value, 64); item.Metric["pod"] != "backend-0" || v != 0.25 {
		t.Fatalf("got pod=%q value=%q", item.Metric["pod"], value)
	}
}

func TestPickCoversEveryJanusQuery(t *testing.T) {
	for _, q := range []string{"container_cpu_usage_seconds_total", "container_memory_working_set_bytes",
		"janus_app_active_connections_ratio", "janus_app_queue_depth_ratio", "go_gc_duration_seconds_sum"} {
		if pick("rate("+q+"{x})") == nil {
			t.Errorf("query %s not mapped", q)
		}
	}
	if pick("unknown") != nil {
		t.Error("unknown query mapped")
	}
}
