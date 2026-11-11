//go:build ebpf

// Package bpf contains the optional connection-lifetime BPF source.
// Build the ELF with build-ebpf.sh and pass -ebpf-object to the proxy.
// The userspace loader reads this object directly; generated Go bindings are
// unnecessary. HTTP request rewards always come from the reverse proxy.
package bpf
