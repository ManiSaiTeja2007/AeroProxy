# AeroProxy

AeroProxy is an enterprise-grade Layer-7 Reverse Proxy, Load Balancer, and Distributed Edge Gateway built in Go.

## Key Features

- **EWMA Predictive Latency Routing**: Routes requests dynamically to backends based on exponentially weighted moving average latency and active requests (`score = EWMA * (ActiveRequests + 1)`).
- **Stateful Circuit Breaking**: Temporarily isolates failing backends (3 consecutive connection errors or HTTP 5xx responses) for 30 seconds.
- **Distributed State Synchronization**: Integrates a Gossip Protocol (via HashiCorp `memberlist`) to broadcast and sync rate-limiting block states across the cluster.
- **Dynamic Service Discovery Control Plane**: Exposes a management API to register or deregister backends on the fly.
- **Graceful Shutdown**: Intercepts `SIGINT`/`SIGTERM` to drain active connections over a 5-second window, exits the gossip membership cleanly, and terminates health checker loops.
- **Unified Logging & Config**: Atomic logger customization with standard environment overrides.

---

## Technical Specifications

### Exposed Network Ports

| Port | Protocol | Service | Description |
|---|---|---|---|
| **8080** | TCP | Gateway | The public-facing entry point for reverse proxy traffic |
| **9090** | TCP | Management & Control | Hosts Prometheus `/metrics` and the Service Discovery API |
| **7946** | TCP & UDP | Gossip Protocol | Cluster node discovery and rate-limit state sync |

---

## Makefile Automation

A unified `Makefile` is provided to streamline development, testing, and deployment operations.

| Command | Action |
|---|---|
| `make build` | Compiles the statically linked binary (`aeroproxy` / `aeroproxy.exe` based on platform) |
| `make test` | Runs all integration tests with the data race detector enabled |
| `make docker-build` | Builds the production-ready Podman image (`aeroproxy:latest`) |
| `make cluster-up` | Launches a multi-node Gossip-synchronized cluster (scales second node) |
| `make clean` | Cleans up built binaries and temporary compilation files |

---

## Running with Podman / Docker

AeroProxy is fully optimized for the Podman container engine and `podman-compose`, but is 100% compatible with Docker.

### 1. Build and Launch the Cluster
To build the multi-stage Alpine-based container image and launch a 2-node cluster (with Node 2 automatically discovering Node 1 via Gossip):

```bash
make cluster-up
```

### 2. Verify Container Health
To inspect the running edge proxy containers:

```bash
podman ps
```

### 3. Inspect Live Clustering Logs
To monitor the Gossip protocol forming membership and syncing rate limits:

```bash
podman logs -f aeroproxy-1
```

---

## Service Discovery Control Plane API

### Adding a Backend Dynamically
To register a new backend downstream host dynamically into the proxy pool:

```bash
curl -X POST http://localhost:9090/backends/add -H "Content-Type: application/json" -d '{"url": "http://localhost:8084"}'
```

### Scrape Telemetry Metrics
To verify status codes, circuit breaker trips, and rate-limiting blocks metrics:

```bash
curl http://localhost:9090/metrics
```
