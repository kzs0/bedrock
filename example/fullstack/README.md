# Bedrock Full-Stack Observability Example

This example demonstrates a complete observability stack using Bedrock with industry-standard open-source tools.

## Stack Overview

- **Application**: Go service instrumented with Bedrock
- **Metrics**: Prometheus (scraping from Bedrock's `/metrics` endpoint)
- **Traces**: Jaeger (receiving OTLP traces from Bedrock)
- **Logs**: Grafana Loki + Promtail (collecting structured JSON logs)
- **Visualization**: Grafana (unified dashboard for metrics, logs, and traces)

## Architecture

```
┌─────────────────┐
│  Bedrock App    │
│  :8080 (HTTP)   │
│  :9090 (Obs)    │
└────┬────┬───┬───┘
     │    │   │
     │    │   └────────────────┐
     │    │                    │
     │    └────────┐           │
     │             │           │
     ▼             ▼           ▼
┌──────────┐  ┌─────────┐  ┌───────┐
│Prometheus│  │ Jaeger  │  │ Loki  │
│  :9091   │  │ :16686  │  │ :3100 │
└────┬─────┘  └────┬────┘  └───┬───┘
     │             │           │
     └─────────────┴───────────┘
                   │
                   ▼
              ┌─────────┐
              │ Grafana │
              │  :3000  │
              └─────────┘
```

## Prerequisites

- Docker and Docker Compose
- [just](https://github.com/casey/just) command runner (install: `brew install just` or `cargo install just`)
- Go 1.21+ (for local development)

## Quick Start

### 1. Start the Stack

```bash
cd example/fullstack
just up
```

**Or without just**:
```bash
docker-compose up -d
```

### 2. Generate Traffic

```bash
# Using just
just traffic

# Or manually
curl http://localhost:8080/users
curl http://localhost:8080/users
curl http://localhost:8080/users
```

### 3. Access Observability Tools

| Tool | URL | Purpose |
|------|-----|---------|
| **Application** | http://localhost:8080 | Demo service endpoints |
| **Grafana** | http://localhost:3000 | Unified observability dashboard (admin/admin) |
| **Prometheus** | http://localhost:9091 | Metrics storage and queries |
| **Jaeger UI** | http://localhost:16686 | Distributed trace visualization |
| **App Metrics** | http://localhost:9090/metrics | Raw Prometheus metrics |
| **App Pprof** | http://localhost:9090/debug/pprof/ | Go profiling endpoints |

## Just Commands

The `justfile` provides convenient commands for managing the stack:

```bash
just                  # List all available commands
just up              # Start the observability stack
just down            # Stop the stack
just logs            # View logs from all services
just logs-app        # View only application logs
just restart         # Restart all services
just clean           # Stop and remove all data
just traffic         # Generate sample HTTP traffic
just traffic-loop    # Generate continuous traffic
just status          # Show service status
just health          # Quick health check of all services
just open-grafana    # Open Grafana in browser
just open-jaeger     # Open Jaeger in browser
just open-prometheus # Open Prometheus in browser
just open-all        # Open all UIs in browser
just demo            # Full demo: start, generate traffic, open UIs
just tail <service>  # Tail logs for a specific service
just shell <service> # Execute a shell in a service
just stats           # Show resource usage
```

**Quick demo**:
```bash
just demo  # Starts everything and opens all UIs
```

## What Bedrock Automatically Provides

### 1. Metrics (Prometheus format)

For every operation, Bedrock automatically creates:

```
<operation_name>_count{labels...}          # Total invocations
<operation_name>_successes{labels...}      # Successful completions
<operation_name>_failures{labels...}       # Failed completions
<operation_name>_duration_ms{labels...}    # Duration histogram (milliseconds)
```

Example metrics from the demo app:
```
http_request_count{http_method="GET",http_path="/users",http_status_code="200"}
http_request_successes{http_method="GET",http_path="/users",http_status_code="200"}
http_request_duration_ms_bucket{http_method="GET",http_path="/users",le="100"}
db_query_count{db_system="postgresql"}
db_query_duration_ms_bucket{db_system="postgresql",le="50"}
```

### 2. Traces (OpenTelemetry → Jaeger)

- **Automatic trace propagation** through nested operations
- **Parent-child relationships** between operations and steps
- **Attribute enrichment** from operation context
- **Trace events** for significant actions (e.g., cache hits, retries)

Example trace structure:
```
http.request (span)
  └─ http.get_users (operation)
       └─ db.query (operation)
            └─ helper (step)
```

### 3. Structured Logs (JSON → Loki)

When `CANONICAL_LOG=true`, Bedrock emits structured logs on operation completion:

```json
{
  "level": "info",
  "msg": "operation.complete",
  "operation": "http.request",
  "duration_ms": 52,
  "success": true,
  "attributes": {
    "http.method": "GET",
    "http.path": "/users",
    "http.status_code": 200
  },
  "steps": [
    {"name": "db.query", "attributes": {...}},
    {"name": "helper", "attributes": {...}}
  ],
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "00f067aa0ba902b7"
}
```

## Exploring the Stack

### Grafana Dashboards

1. **Navigate to Grafana**: http://localhost:3000 (admin/admin)
2. **Explore → Metrics**: Query Prometheus metrics
3. **Explore → Logs**: Search Loki logs with LogQL
4. **Explore → Traces**: View Jaeger traces
5. **Dashboards → Bedrock**: Pre-configured overview dashboard

### Sample Queries

**Prometheus (Metrics)**:
```promql
# Request rate by endpoint
rate(http_request_count[5m])

# 95th percentile latency
histogram_quantile(0.95, rate(http_request_duration_ms_bucket[5m]))

# Error rate
rate(http_request_failures[5m]) / rate(http_request_count[5m])
```

**LogQL (Logs)**:
```logql
# All logs from the app
{container="bedrock-demo"}

# Only operation completion logs
{container="bedrock-demo"} | json | msg="operation.complete"

# Failed operations
{container="bedrock-demo"} | json | success="false"

# Operations slower than 100ms
{container="bedrock-demo"} | json | duration_ms > 100
```

**Jaeger (Traces)**:
```
Service: bedrock-demo
Operation: http.request
Tags: http.method=GET http.status_code=200
```

## Application Code Highlights

### Automatic HTTP Instrumentation

```go
handler := bedrock.HTTPMiddleware(ctx, mux)
```

Every HTTP request automatically gets:
- Operation started with request attributes
- Response status captured
- Duration measured
- Success/failure tracked
- Trace span created

### Custom Operations

```go
op, ctx := bedrock.Operation(ctx, "db.query",
    bedrock.Attrs(
        attr.String("db.system", "postgresql"),
        attr.String("db.statement", "SELECT * FROM users"),
    ),
    bedrock.MetricLabels("db.system"),
)
defer op.Done()
```

### Lightweight Steps

```go
step := bedrock.NewStep(ctx, "helper")
defer step.Done()

step.Register(ctx, attr.Int("rows_processed", 42))
// Attributes automatically propagate to parent operation
```

### Error Handling

```go
if err != nil {
    op.Register(ctx, attr.Error(err))  // Marks operation as failed
    return err
}
```

### Background Sources

```go
source, ctx := bedrock.Source(ctx, "background.worker")
defer source.Done()

// Track aggregated metrics
source.Aggregate(ctx, attr.Sum("jobs_processed", 1))
```

## Configuration

Environment variables for the demo app:

```bash
SERVICE_NAME=bedrock-demo           # Service identifier
LOG_LEVEL=info                      # debug, info, warn, error
LOG_FORMAT=json                     # json or text
TRACE_ENDPOINT=http://jaeger:4318/v1/traces
TRACE_SAMPLE_RATE=1.0              # 0.0 to 1.0 (1.0 = 100%)
CANONICAL_LOG=true                  # Emit structured operation logs
LOOP_TERM=5s                        # Background loop interval
```

## Stopping the Stack

```bash
# Using just
just down

# Remove volumes to clear data
just clean

# Or with docker-compose directly
docker-compose down
docker-compose down -v  # with data cleanup
```

## Troubleshooting

Having issues? See the [detailed troubleshooting guide](TROUBLESHOOTING.md) for comprehensive diagnostics and solutions.

### Quick Checks

1. **Verify all services are running**:
   ```bash
   just health
   ```

2. **Check metrics endpoint**:
   ```bash
   curl http://localhost:9090/metrics
   ```

3. **Check Prometheus targets** (should all be green):
   ```bash
   open http://localhost:9091/targets
   ```

4. **Generate traffic and wait**:
   ```bash
   just traffic
   sleep 10
   ```

5. **Check each service**:
   - Grafana: http://localhost:3000 (admin/admin)
   - Prometheus: http://localhost:9091
   - Jaeger: http://localhost:16686

### Common Issues

- **No data in Grafana**: Wait 15-30 seconds after startup, generate traffic with `just traffic`
- **Prometheus not scraping**: Check `http://localhost:9091/targets` - all should be UP
- **No traces in Jaeger**: Verify TRACE_SAMPLE_RATE=1.0 in docker-compose.yml
- **Services won't start**: Check for port conflicts, run `just clean` and `just up`

For detailed solutions, see [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

## Architecture Decisions

### Why This Stack?

- **Open Source**: No vendor lock-in, full control
- **Industry Standard**: Prometheus, Jaeger, Grafana are widely adopted
- **Cloud Native**: All tools are CNCF projects
- **Unified UX**: Grafana provides single pane of glass
- **Production Ready**: These tools run at scale (Uber, Shopify, etc.)

### Bedrock Design Choices

1. **Context-based**: No globals, explicit dependencies
2. **Automatic metrics**: Every operation gets 4 metrics by default
3. **Upfront label registration**: Prevents cardinality explosion
4. **Success by default**: Operations succeed unless errors registered
5. **Step propagation**: Helper functions contribute to parent metrics
6. **Canonical logging**: Optional structured operation logs
7. **Enumeration**: Duplicate operations/steps tracked with indices

## Next Steps

1. **Customize dashboards**: Edit `grafana/dashboards/*.json`
2. **Add alerts**: Configure Prometheus alerting rules
3. **Tune sampling**: Adjust `TRACE_SAMPLE_RATE` for production
4. **Create SLIs/SLOs**: Define service level indicators
5. **Integrate with your app**: Replace demo service with real code

## Resources

- [Bedrock Documentation](../../README.md)
- [Prometheus Docs](https://prometheus.io/docs/)
- [Jaeger Docs](https://www.jaegertracing.io/docs/)
- [Grafana Docs](https://grafana.com/docs/)
- [Loki Docs](https://grafana.com/docs/loki/)

## License

Same as parent Bedrock project.
