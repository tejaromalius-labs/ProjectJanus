# Repository Guidelines

## Project Structure & Module Organization

`linucb-core/` is the Rust LinUCB engine. Its implementation is in `src/`, integration tests in `tests/`, and C ABI header in `include/`. `linucb-proxy/` is the Go HTTP proxy: `cmd/proxy/` contains the entry point, and `internal/` contains routing, discovery, telemetry, and the CGO binding. Go tests sit beside their packages. `benchmark/` is a separate Go module that tests the unmodified Janus binary against round robin, least connections, P2C and Peak EWMA; it must not import `linucb-proxy` code. `helm-chart/` contains Kubernetes manifests, `Dockerfile` builds the image, `docs/README.md` indexes the step-by-step technical reference (`docs/architecture.md` maps the old single-file sections to it), and `docs/history.md` records past studies and decisions. The eBPF C source and its build script are under `linucb-proxy/internal/bpf/`.

## Build, Test, and Development Commands

Use Rust 1.75+, Go 1.24+, and enabled CGO. Build the Rust release archive before building or testing Go; the CGO binding links `linucb-core/target/release/liblinucb_core.a`.

```bash
(cd linucb-core && cargo build --release)
(cd linucb-proxy && go build -o proxy ./cmd/proxy/)
(cd linucb-proxy && ./proxy -listen=:8080)
```

The local proxy uses mock discovery: backends on ports 9001 and 9002 by default, or set `-backends=id=host:port,...`. For Kubernetes discovery, build or test Go with `-tags k8s`. `docker build -t janus:local .` builds the container image; `helm-chart/` contains the deployment chart.

## Coding Style & Naming Conventions

Use `gofmt` for Go and `cargo fmt` for Rust; follow each language's standard indentation. Keep Go packages lowercase and tests in `*_test.go` files with `TestXxx` functions. Use Rust `snake_case` for modules and functions, and place integration tests in descriptive `tests/*.rs` files.

## Testing Guidelines

Run `cargo test` from `linucb-core/`, then `go test ./...` from `linucb-proxy/` after building the Rust archive. Run `go test -tags k8s ./...` for discovery changes and `go test -race ./...` for concurrency changes. Rust tests and the simulator live in `linucb-core/tests/` (shared helpers in `tests/common/`), never in `src/`; Go uses package tests and fake Kubernetes clients. The production reward is `linucb-proxy/internal/proxy/reward.go`; keep the simulator copy in `linucb-core/tests/common/reward.rs` in step with it. No numeric coverage threshold is documented. Run `go test ./...` in `benchmark/` for harness changes; `benchmark/run.sh` runs the full campaign on a dedicated Multipass VM.

## Commit & Pull Request Guidelines

Recent commits use imperative, sentence-style subjects such as `Add ...`, `Enhance ...`, and `Refactor ...`. Keep subjects focused on one change. In pull requests, describe behavior and verification commands, link any relevant issue, and include screenshots or sample `/stats` output when observable behavior changes.
