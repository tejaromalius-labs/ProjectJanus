//go:build !k8s

package registry

import (
	"errors"
	"log/slog"
)

func NewK8sDiscovery(service, namespace string, log *slog.Logger) (Discovery, error) {
	return nil, errors.New("binary not compiled with -tags k8s; rebuild with -tags k8s for cluster mode")
}
