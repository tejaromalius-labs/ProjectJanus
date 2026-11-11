//go:build ebpf

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
)

// RunEBPFSource records TCP connection lifetimes as supplementary diagnostics.
// A TCP lifetime is not HTTP request latency or an HTTP status; it must never
// feed request rewards, especially with keep-alive or multiplexed connections.
func RunEBPFSource(ctx context.Context, objectPath string, c *Collector, resolvePod func(string) string, log *slog.Logger) error {
	if err := rlimit.RemoveMemlock(); err != nil {
		return err
	}
	spec, err := ebpf.LoadCollectionSpec(objectPath)
	if err != nil {
		return err
	}
	objects, err := ebpf.NewCollection(spec)
	if err != nil {
		return err
	}
	defer objects.Close()
	program, events := objects.Programs["handle_set_state"], objects.Maps["events"]
	if program == nil || events == nil {
		return fmt.Errorf("eBPF object lacks handle_set_state or events")
	}
	tp, err := link.Tracepoint("sock", "inet_sock_set_state", program, nil)
	if err != nil {
		return err
	}
	defer tp.Close()
	rd, err := ringbuf.NewReader(events)
	if err != nil {
		return err
	}
	defer rd.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = rd.Close()
		case <-done:
		}
	}()
	log.Info("eBPF connection telemetry enabled")
	for {
		record, err := rd.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return nil
			}
			return err
		}
		event, ok := parseConnectionEvent(record.RawSample)
		if !ok {
			continue
		}
		if pod := resolvePod(event.endpoint); pod != "" {
			c.ObserveConnection(pod, float64(event.endNs-event.startNs)/1e9)
		}
	}
}
