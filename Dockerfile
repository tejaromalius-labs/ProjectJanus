# Build stage: compile Rust core and Go proxy
FROM ubuntu:24.04 AS builder

RUN apt-get update && apt-get install -y \
  curl \
  build-essential \
  pkg-config \
  && rm -rf /var/lib/apt/lists/*

# Install Rust
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
ENV PATH="/root/.cargo/bin:${PATH}"

# Install Go
RUN curl -fsSL https://go.dev/dl/go1.24.0.linux-amd64.tar.gz -o go.tar.gz && \
  tar -C /usr/local -xzf go.tar.gz && \
  rm go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /build

# Copy source
COPY linucb-core ./linucb-core
COPY linucb-proxy ./linucb-proxy

# Build Rust core
WORKDIR /build/linucb-core
RUN cargo build --release

# Build Go proxy (links against liblinucb_core.a via CGO)
WORKDIR /build/linucb-proxy
RUN go mod download
RUN CGO_ENABLED=1 \
  CGO_CFLAGS="-I../linucb-core/include" \
  CGO_LDFLAGS="-L../linucb-core/target/release -ldl -lpthread -lm" \
  go build -tags k8s -o proxy ./cmd/proxy

# Runtime stage: minimal image with just the proxy binary
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build/linucb-proxy/proxy /usr/local/bin/proxy

ENTRYPOINT ["/usr/local/bin/proxy"]
