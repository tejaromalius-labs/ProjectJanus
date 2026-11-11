//go:build !ebpf

package telemetry

import (
	"context"
	"errors"
	"log/slog"
)

var ErrEBPFDisabled = errors.New("telemetry: build with -tags ebpf to enable connection telemetry")

func RunEBPFSource(_ context.Context, _ string, _ *Collector, _ func(string) string, _ *slog.Logger) error {
	return ErrEBPFDisabled
}
