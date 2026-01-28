# Bedrock - Development Guide

## Project Overview

**Bedrock** is an opinionated, production-ready observability library for Go that provides integrated tracing, metrics, profiling, and structured logging with automatic instrumentation and no external dependencies.

**Core Philosophy:**
- **Context-based architecture**: No globals, everything flows through `context.Context`
- **Success by default**: Operations succeed unless errors are explicitly registered
- **Controlled cardinality**: Metric labels defined upfront to prevent explosion
- **Automatic instrumentation**: Minimal boilerplate for complete observability

**Key Differentiator**: Single library that eliminates observability boilerplate while providing industry-standard telemetry (OTLP traces, Prometheus metrics, structured logs).

## Architecture

### Package Structure

```
bedrock/
├── api.go           # Public API: Init, Operation, Source, Step, logging/metrics helpers
├── bedrock.go       # Core Bedrock struct, New(), accessors
├── config.go        # Config struct, FromEnv(), DefaultConfig()
├── context.go       # Context key types and helpers
├── options.go       # OperationOption, StepOption, SourceOption interfaces
├── operation.go     # Operation and Step implementation
├── middleware.go    # HTTP middleware with trace propagation
├── client.go        # Instrumented HTTP client
├── noop.go          # Noop implementation for uninitialized contexts
├── attr/            # Attribute types (String, Int, Error, Event, etc.)
├── trace/           # Tracing: Tracer, Span, SpanContext, W3C propagation
│   └── otlp/        # OpenTelemetry Protocol export
├── metric/          # Metrics: Registry, Counter, Gauge, Histogram, RuntimeCollector
│   └── prometheus/  # Prometheus exposition format
├── log/             # Logging: Bridge (attr-based), Handler (slog integration)
├── server/          # Observability server: /metrics, /health, /debug/pprof
├── transport/       # HTTP transport with tracing
├── env/             # Environment variable parsing
└── example/         # Examples including gRPC propagator
```

### Component Relationships

```
┌───────────────────────────────────┐
│        Application Code           │
│   bedrock.Init(ctx) → Context     │
└────────────────┬──────────────────┘
                 │
    ┌────────────┼────────────┐
    │            │            │
    ▼            ▼            ▼
┌─────────┐  ┌──────┐  ┌──────────┐
│ Tracer  │  │Logger│  │ Metrics  │
│(Spans)  │  │(slog)│  │(Registry)│
└────┬────┘  └──┬───┘  └────┬─────┘
     │          │           │
     ▼          ▼           ▼
  OTLP      Structured   Prometheus
 Export      JSON Logs    /metrics
```

### Context Flow

1. `bedrock.Init(ctx)` creates a `Bedrock` instance and attaches it to context
2. All operations (`Operation`, `Source`, `Step`) propagate context
3. Attributes, spans, and metrics flow through context
4. HTTP middleware extracts/injects W3C Trace Context headers
5. No global state - everything is explicitly passed

### Package Dependencies

Internal packages should not import from parent:
- `attr` - no dependencies on other bedrock packages
- `trace` - depends on `attr`
- `metric` - depends on `attr`
- `log` - depends on `attr`, `trace`
- `server` - depends on `metric`
- `transport` - depends on `attr`, `trace`
- `env` - no dependencies on other bedrock packages

The main `bedrock` package imports all internal packages.

## Core Concepts

### Operations

Operations are the primary instrumentation unit. Every operation automatically:
- Creates a trace span
- Records 4 metrics (count, successes, failures, duration_ms)
- Supports attribute registration
- Defaults to success unless errors are registered

```go
op, ctx := bedrock.Operation(ctx, "process_user",
    bedrock.Attrs(attr.String("user_id", "123")),
    bedrock.MetricLabels("user_id", "status"),
)
defer op.Done()

op.Register(ctx, attr.String("status", "active"))
if err != nil {
    op.Register(ctx, attr.Error(err))  // Marks as failure
}
```

**Automatic Metrics Generated:**
- `process_user_count{user_id="123",status="active"}` - Total invocations
- `process_user_successes{user_id="123",status="active"}` - Successful completions
- `process_user_failures{user_id="123",status="active"}` - Failed completions
- `process_user_duration_ms{user_id="123",status="active"}` - Histogram in milliseconds

**Cardinality Control:**
Only attributes matching `MetricLabels()` become metric labels. Missing labels default to `"_"`. This prevents unbounded cardinality from high-cardinality attributes like request IDs.

### Sources

Sources represent long-running processes (background workers, loops). They:
- Prefix all child operation names automatically
- Share attributes and metric labels with children
- Track aggregate metrics (Sum, Gauge, Histogram)

```go
source, ctx := bedrock.Source(ctx, "background.worker",
    bedrock.SourceAttrs(attr.String("worker.type", "async")),
    bedrock.SourceMetricLabels("worker.type"),
)
defer source.Done()

source.Aggregate(ctx,
    attr.Sum("jobs_processed", 1),
    attr.Gauge("queue_depth", 42),
)

// Child operations inherit "background.worker." prefix
op, ctx := bedrock.Operation(ctx, "process")
// Full name: "background.worker.process"
```

### Steps

Steps are lightweight tracing spans for helper functions. They:
- Create spans visible in traces
- Do NOT create separate metrics (contribute to parent)
- Propagate attributes to parent operation

```go
func helper(ctx context.Context) {
    step := bedrock.Step(ctx, "helper",
        attr.String("key", "value"),
    )
    defer step.Done()

    step.Register(ctx, attr.Int("count", 1))
}
```

**When to use:**
- **Steps**: Helper functions, internal logic (want trace visibility only)
- **Operations**: Major units of work (want full metrics + cardinality control)

### NoTrace Mode

Use `NoTrace()` option to disable tracing for hot paths. Metrics still recorded. Inherits through context to children.

### Attributes

Type-safe attribute system for logs, metrics, and traces:

```go
attr.String("key", "value")
attr.Int("count", 42)
attr.Float64("latency", 123.45)
attr.Duration("timeout", 5*time.Second)
attr.Error(err)
attr.Bool("enabled", true)
```

**Static Attributes**: Set during `Init()`, automatically included in:
- All metrics as labels
- All logs as fields
- All trace spans as attributes

```go
ctx, close := bedrock.Init(ctx,
    bedrock.WithStaticAttrs(
        attr.String("env", "production"),
        attr.String("version", "1.2.3"),
    ),
)
```

## Common Patterns

### Adding a New Option

Options use interfaces for type safety:
- `OperationOption` - only for operations
- `StepOption` - only for steps
- `commonOption` - works on both (implements both interfaces)

```go
// Common option (works on Operation and Step)
func MyOption() commonOption {
    return commonOption{...}
}

// Operation-only option
func MyOpOption() operationOnlyOption {
    return operationOnlyOption{fn: func(cfg *operationConfig) {...}}
}
```

### Context Keys

All context values use unexported key types in `context.go`:
- `bedrockKey` - stores `*Bedrock`
- `operationKey` - stores `*operationState`
- `sourceKey` - stores `*sourceConfig`
- `noTraceKey` - stores `bool` for NoTrace inheritance

### Adding a New Operation

```go
op, ctx := bedrock.Operation(ctx, "task_name",
    bedrock.Attrs(
        attr.String("task.id", taskID),
        attr.String("task.type", taskType),
    ),
    bedrock.MetricLabels("task.type"),  // Choose low-cardinality labels!
)
defer op.Done()

// Add dynamic attributes during execution
op.Register(ctx, attr.Int("items_processed", count))

// Mark failures
if err != nil {
    op.Register(ctx, attr.Error(err))
    return err
}
```

## Key Features

### 1. Automatic Metrics

Every operation generates 4 metrics:

| Metric Type | Format | Description |
|-------------|--------|-------------|
| Counter | `<name>_count{labels}` | Total operations |
| Counter | `<name>_successes{labels}` | Successful operations |
| Counter | `<name>_failures{labels}` | Failed operations |
| Histogram | `<name>_duration_ms{labels}` | Duration in milliseconds |

**Label Control:**
- Labels declared via `MetricLabels()` option
- Only declared labels appear in metrics
- Missing values → `"_"` default
- Prevents metric cardinality explosion

### 2. W3C Trace Context Propagation

**Architecture**: Bedrock uses a modular propagation system that supports arbitrary transports.

**Core Packages:**
- `trace/w3c` - W3C format parsing/formatting utilities (protocol-agnostic)
- `trace/http` - HTTP propagator implementation
- `trace/propagator.go` - Generic `Propagator` interface
- `example/grpc` - gRPC propagator example (copy into your project)

**Traceparent Header Format**: `00-{trace-id}-{parent-id}-{flags}`
- Version: `00` (2 hex chars)
- Trace ID: 32 hex chars (16 bytes, lowercase, non-zero)
- Parent ID: 16 hex chars (8 bytes, lowercase, non-zero)
- Flags: 2 hex chars (bit 0 = sampled)

**Tracestate Header**: `key1=value1,key2=value2`
- Multi-tenant format: `{tenant}@{system}`
- Max 32 entries
- Passthrough propagation

**Automatic HTTP Propagation:**
- **HTTP Middleware**: Extracts traceparent/tracestate from inbound requests
- **HTTP Client**: Injects traceparent/tracestate into outbound requests
- **Remote Parent Support**: Child spans link to upstream distributed traces

**Custom Propagators:**
You can implement the `trace.Propagator` interface for any transport:

```go
type Propagator interface {
    Extract(carrier any) (SpanContext, error)
    Inject(ctx context.Context, carrier any) error
}
```

Example for Kafka:

```go
type KafkaPropagator struct{}

func (p *KafkaPropagator) Extract(carrier any) (trace.SpanContext, error) {
    headers := carrier.([]kafka.Header)
    // Find traceparent header
    for _, h := range headers {
        if h.Key == "traceparent" {
            traceID, spanID, flags, err := w3c.ParseTraceparent(string(h.Value))
            if err != nil {
                return trace.SpanContext{}, err
            }
            return trace.NewRemoteSpanContext(traceID, spanID, "", flags&w3c.SampledFlag != 0), nil
        }
    }
    return trace.SpanContext{}, errors.New("no traceparent")
}

func (p *KafkaPropagator) Inject(ctx context.Context, carrier any) error {
    headers := carrier.(*[]kafka.Header)
    span := trace.SpanFromContext(ctx)
    if span == nil {
        return nil
    }
    traceparent := w3c.FormatTraceparent(span.TraceID(), span.SpanID(), true)
    *headers = append(*headers, kafka.Header{Key: "traceparent", Value: []byte(traceparent)})
    return nil
}
```

### 3. HTTP Integration

**Middleware** (`middleware.go`):

```go
handler := bedrock.HTTPMiddleware(ctx, mux,
    bedrock.WithOperationName("http.request"),
    bedrock.WithAdditionalLabels("user_agent"),
    bedrock.WithAdditionalAttrs(func(r *http.Request) []attr.Attr {
        return []attr.Attr{attr.String("custom", r.Header.Get("X-Custom"))}
    }),
    bedrock.WithSuccessCodes(200, 201, 202),
    bedrock.WithTracePropagation(true),
)
```

**Default Attributes:**
- `http.method` - GET, POST, etc.
- `http.route` - Request path
- `http.scheme` - http/https
- `http.host` - Host header
- `http.user_agent` - User-Agent header
- `http.status_code` - Response status

**Default Metric Labels**: `http_method`, `http_route`, `http_status_code`

**Security**: Middleware supports DoS protection via HTTP server timeouts (see Configuration).

### 4. Convenient APIs

**Direct Logging** (includes static attributes + trace context):

```go
bedrock.Debug(ctx, "message", attr.String("key", "value"))
bedrock.Info(ctx, "message", attr.Int("count", 42))
bedrock.Warn(ctx, "warning", attr.Duration("timeout", 5*time.Second))
bedrock.Error(ctx, "error", attr.Error(err))
bedrock.Log(ctx, slog.LevelInfo, "custom", attr.String("key", "value"))
```

**Direct Metrics** (includes static labels):

```go
counter := bedrock.Counter(ctx, "requests_total", "Total requests", "method", "status")
counter.With(attr.String("method", "GET"), attr.String("status", "200")).Inc()
counter.Inc()  // Uses static labels only

gauge := bedrock.Gauge(ctx, "active_connections", "Active connections")
gauge.Set(42)
gauge.Inc()
gauge.Dec()

histogram := bedrock.Histogram(ctx, "duration_ms", "Duration", nil, "endpoint")
histogram.With(attr.String("endpoint", "/users")).Observe(123.45)
histogram.Observe(100)  // Uses static labels only
```

### 5. Observability Server

**Implementation**: `server/server.go`

**Endpoints:**
- `/metrics` - Prometheus exposition format
- `/debug/pprof/*` - Go profiling endpoints (cpu, heap, goroutine, etc.)
- `/health` - Health check

**Auto-start** (if enabled in config):

```go
ctx, close := bedrock.Init(ctx, bedrock.WithConfig(bedrock.Config{
    ServerEnabled: true,
    ServerAddr:    ":9090",
}))
```

**Production Security Defaults:**
- ReadTimeout: 10s
- ReadHeaderTimeout: 5s (Slowloris protection)
- WriteTimeout: 30s
- IdleTimeout: 120s
- MaxHeaderBytes: 1MB

## Configuration Reference

### Environment Variables

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `BEDROCK_SERVICE` | string | `unknown` | Service name identifier |
| `BEDROCK_TRACE_URL` | string | - | OTLP HTTP endpoint (e.g., `http://jaeger:4318/v1/traces`) |
| `BEDROCK_TRACE_SAMPLE_RATE` | float | `1.0` | Sampling rate (0.0 to 1.0) |
| `BEDROCK_LOG_LEVEL` | string | `info` | Log level: debug, info, warn, error |
| `BEDROCK_LOG_FORMAT` | string | `json` | Log format: json or text |
| `BEDROCK_LOG_CANONICAL` | bool | `false` | Enable operation completion logs |
| `BEDROCK_METRIC_PREFIX` | string | - | Prefix for all metric names |
| `BEDROCK_METRIC_BUCKETS` | string | - | Histogram buckets (comma-separated) |
| `BEDROCK_SERVER_ENABLED` | bool | `true` | Auto-start observability server |
| `BEDROCK_SERVER_ADDR` | string | `:9090` | Server listen address |
| `BEDROCK_SERVER_METRICS` | bool | `true` | Enable /metrics endpoint |
| `BEDROCK_SERVER_PPROF` | bool | `true` | Enable /debug/pprof endpoints |
| `BEDROCK_SERVER_READ_TIMEOUT` | duration | `10s` | HTTP read timeout |
| `BEDROCK_SERVER_READ_HEADER_TIMEOUT` | duration | `5s` | HTTP header read timeout |
| `BEDROCK_SERVER_WRITE_TIMEOUT` | duration | `30s` | HTTP write timeout |
| `BEDROCK_SERVER_IDLE_TIMEOUT` | duration | `120s` | HTTP idle timeout |
| `BEDROCK_SERVER_MAX_HEADER_BYTES` | int | `1048576` | Max header size (1MB) |
| `BEDROCK_SHUTDOWN_TIMEOUT` | duration | `30s` | Graceful shutdown timeout |

### Programmatic Configuration

```go
cfg := bedrock.Config{
    Service:         "my-service",
    TraceURL:        "http://localhost:4318/v1/traces",
    TraceSampleRate: 1.0,
    LogLevel:        "info",
    LogFormat:       "json",
    LogCanonical:    true,
    MetricPrefix:    "myapp",
    ServerEnabled:   true,
    ServerAddr:      ":9090",
    ShutdownTimeout: 30 * time.Second,
}

ctx, close := bedrock.Init(ctx,
    bedrock.WithConfig(cfg),
    bedrock.WithStaticAttrs(
        attr.String("env", "production"),
        attr.String("region", "us-west-2"),
    ),
)
defer close()
```

### Custom Config Parsing

Use `env.Parse[T]()` to parse custom config structs:

```go
type Config struct {
    Bedrock  bedrock.Config
    Port     int    `env:"PORT" envDefault:"8080"`
    Database string `env:"DATABASE_URL"`
}

cfg, err := env.Parse[Config]()
if err != nil {
    log.Fatal(err)
}

ctx, close := bedrock.Init(ctx, bedrock.WithConfig(cfg.Bedrock))
defer close()
```

## Testing

```bash
# Run tests
go test ./...

# Run tests with race detection
go test -race ./...

# Run example
go run example/main.go
```

**Key test files:**
- `api_test.go` - Integration tests for public API
- `bedrock_test.go` - Core functionality tests
- `middleware_test.go` - HTTP middleware tests
- `client_test.go` - HTTP client tests

### Testing Patterns

**Basic Setup:**

```go
func TestMyFunction(t *testing.T) {
    ctx, close := bedrock.Init(context.Background(),
        bedrock.WithConfig(bedrock.Config{
            Service: "test-service",
        }),
    )
    defer close()

    // Your test code
}
```

**Verifying Operations:**

```go
func TestOperation(t *testing.T) {
    ctx, close := bedrock.Init(context.Background())
    defer close()

    op, ctx := bedrock.Operation(ctx, "test_operation",
        bedrock.Attrs(attr.String("key", "value")),
    )

    // Verify operation is in context
    if op == nil {
        t.Fatal("operation should not be nil")
    }

    op.Done()

    // Check metrics were recorded
    b := bedrock.FromContext(ctx)
    registry := b.MetricsRegistry()

    counter := registry.GetCounter("test_operation_count")
    if counter == nil {
        t.Fatal("counter not found")
    }
}
```

**Testing HTTP Middleware:**

```go
func TestMiddleware(t *testing.T) {
    ctx, close := bedrock.Init(context.Background())
    defer close()

    handler := bedrock.HTTPMiddleware(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Verify bedrock is in context
        b := bedrock.FromContext(r.Context())
        if b == nil {
            t.Error("bedrock not in context")
        }

        w.WriteHeader(http.StatusOK)
    }))

    req := httptest.NewRequest("GET", "/test", nil)
    rec := httptest.NewRecorder()

    handler.ServeHTTP(rec, req)

    if rec.Code != http.StatusOK {
        t.Errorf("expected 200, got %d", rec.Code)
    }
}
```

**Testing W3C Propagation:**

```go
func TestTracePropagation(t *testing.T) {
    ctx, close := bedrock.Init(context.Background())
    defer close()

    // Create parent span
    parentOp, ctx := bedrock.Operation(ctx, "parent")
    defer parentOp.Done()

    req := httptest.NewRequest("GET", "/test", nil)

    // Inject trace context
    client := bedrock.NewClient(nil)
    // Extract traceparent header from request after transport

    // Verify traceparent format
    header := req.Header.Get("traceparent")
    if !strings.HasPrefix(header, "00-") {
        t.Errorf("invalid traceparent: %s", header)
    }
}
```

## Important Implementation Details

### Noop Pattern

`bedrockFromContext()` returns a singleton noop instance if bedrock isn't in context, avoiding nil checks everywhere.

All operations gracefully handle missing bedrock from context:
- `bedrock.Operation(ctx, ...)` → Returns noop operation if bedrock not in context
- `bedrock.Info(ctx, ...)` → No-op if bedrock not initialized
- `bedrock.Counter(ctx, ...)` → Returns noop counter
- **Zero allocations** when not initialized
- Safe for library code without initialization requirements

### Metric Labels

Use `MetricLabels()` to pre-register label names. Missing labels default to `"_"` to maintain consistent cardinality.

**Problem**: Unbounded labels cause metric explosion (e.g., user IDs, request IDs).

**Solution**: Pre-declare labels via `MetricLabels()` option.

```go
// GOOD: Controlled cardinality
op, ctx := bedrock.Operation(ctx, "process_user",
    bedrock.Attrs(
        attr.String("user_id", "12345"),  // High cardinality
        attr.String("status", "active"),  // Low cardinality
    ),
    bedrock.MetricLabels("status"),  // Only "status" becomes metric label
)

// Metrics: process_user_count{status="active"}
// user_id is logged/traced but NOT a metric label
```

### Success by Default

Operations are successful unless `attr.Error()` is registered.

### W3C Trace Context

Middleware extracts `traceparent`/`tracestate` headers. Client injects them. See `trace/w3c/` for parsing.

**W3C Validation Rules:**
- Invalid traceparent → start new trace (ignore tracestate)
- Case-insensitive header names per RFC
- Multiple tracestate headers combined with commas
- Lowercase hex characters only
- Non-zero trace/span IDs

### Runtime Metrics

When `RuntimeMetrics` is enabled (default), Go runtime metrics are collected via `metric.RuntimeCollector`.

### Sampling Strategies (`trace/sampler.go`)

**Available Samplers:**

| Sampler | Behavior | Use Case |
|---------|----------|----------|
| `AlwaysSampler` | Samples all traces | Development, critical operations |
| `NeverSampler` | Drops all traces | Cost reduction on high-volume operations |
| `RatioSampler` | Probabilistic (0.0-1.0) | Production rate-limiting |
| `ParentBasedSampler` | Respects parent decision | **Default - distributed systems** |

**Sampling Decision Flow:**
1. Parent sampled → child sampled
2. No parent → use root sampler
3. Remote parent takes precedence
4. Maintains trace-wide consistency

**Important**: Sampling affects export, not span creation. All spans are created for trace visibility.

### Operation Lifecycle

1. **Start**: `Operation()` creates span, starts timer
2. **During**: `Register()` adds attributes to span and operation state
3. **End**: `Done()` finalizes span, records metrics (duration, success/failure)
4. **Export**: Span sent to OTLP exporter asynchronously

### Metric Label Resolution

When `Done()` is called:
1. Collect all attributes from operation
2. Filter to only `MetricLabels()` declared labels
3. Add static labels from bedrock initialization
4. Missing label values → `"_"` default
5. Record metrics with resolved label set

### Trace Span Hierarchy

```
Parent Span (trace_id: ABC, span_id: 123)
  └─ Child Operation (trace_id: ABC, span_id: 456, parent_id: 123)
       └─ Step (trace_id: ABC, span_id: 789, parent_id: 456)
```

All spans share the same `trace_id`. Parent-child relationships via `parent_id`.

### Canonical Logging

When `LogCanonical: true`, operation completion emits:

```json
{
  "level": "info",
  "msg": "operation.complete",
  "operation": "http.request",
  "duration_ms": 52.3,
  "success": true,
  "attributes": {
    "http.method": "GET",
    "http.status_code": 200
  },
  "trace_id": "abc123",
  "span_id": "def456"
}
```

Useful for log-based analysis and correlation with traces.

## File Structure Reference

### Core Files

| File | Purpose | Key Types/Functions |
|------|---------|---------------------|
| `api.go` | Public convenience functions | `Info()`, `Counter()`, `Gauge()`, `Histogram()`, `Get()`, `Post()` |
| `bedrock.go` | Main struct and initialization | `Bedrock`, `Init()`, `FromContext()` |
| `operation.go` | Operation implementations | `Op`, `Src`, `OpStep`, `Operation()`, `Source()`, `Step()` |
| `config.go` | Configuration struct and loading | `Config`, `DefaultConfig()`, `LoadConfig()` |
| `options.go` | Functional options pattern | `Attrs()`, `MetricLabels()`, `WithOperationName()` |
| `middleware.go` | HTTP middleware | `HTTPMiddleware()`, middleware options |
| `noop.go` | Noop implementations | `noopOp`, `noopSrc`, `noopStep` |

### HTTP Integration

| File | Purpose | Key Functions |
|------|---------|---------------|
| `client.go` | HTTP client instrumentation | `NewClient()`, `Do()`, `Get()`, `Post()` |
| `transport/transport.go` | RoundTripper implementation | `Transport`, `RoundTrip()` |

### Tracing

| File | Purpose | Key Types |
|------|---------|-----------|
| `trace/tracer.go` | Tracer and span factory | `Tracer`, `StartSpan()` |
| `trace/span.go` | Span implementation | `Span`, `End()`, `SetAttr()`, `RecordError()` |
| `trace/context.go` | Context management | `SpanContext`, `ContextWithSpan()`, `SpanFromContext()` |
| `trace/propagator.go` | Propagator interface | `Propagator` interface |
| `trace/w3c/w3c.go` | W3C format utilities | `ParseTraceparent()`, `FormatTraceparent()`, `ParseTracestate()` |
| `trace/http/propagator.go` | HTTP propagator | `Propagator`, `Extract()`, `Inject()` |
| `trace/sampler.go` | Sampling strategies | `Sampler`, `AlwaysSampler`, `ParentBasedSampler` |
| `trace/otlp/exporter.go` | OTLP export | `Exporter`, `Export()` |
| `trace/otlp/batch.go` | Batch processing | `BatchProcessor` |

### Metrics

| File | Purpose | Key Types |
|------|---------|-----------|
| `metric/registry.go` | Metric registration | `Registry`, `GetOrCreateCounter()` |
| `metric/counter.go` | Counter implementation | `Counter`, `Inc()`, `With()` |
| `metric/gauge.go` | Gauge implementation | `Gauge`, `Set()`, `Inc()`, `Dec()` |
| `metric/histogram.go` | Histogram implementation | `Histogram`, `Observe()` |
| `metric/prometheus/exposition.go` | Prometheus format | Exposition format encoding |
| `metric/prometheus/handler.go` | HTTP handler | `/metrics` endpoint handler |

### Logging

| File | Purpose | Key Types |
|------|---------|-----------|
| `log/bridge.go` | Slog bridge | `Bridge`, `Logger()` |
| `log/handler.go` | Slog handler | `Handler`, custom slog handler |

### Other

| File | Purpose | Key Functions |
|------|---------|---------------|
| `attr/attr.go` | Attribute types | `String()`, `Int()`, `Error()`, etc. |
| `attr/set.go` | Attribute sets | `Set`, `Merge()` |
| `server/server.go` | Observability server | `Server`, `ListenAndServe()` |
| `env/config.go` | Config parsing | `Parse[T]()` |
| `env/parser.go` | Tag-based parsing | Environment variable parsing |

## Common Tasks

### Instrumenting HTTP Handlers

```go
func main() {
    ctx, close := bedrock.Init(context.Background())
    defer close()

    mux := http.NewServeMux()
    mux.HandleFunc("/users", handleUsers)

    handler := bedrock.HTTPMiddleware(ctx, mux,
        bedrock.WithAdditionalLabels("user_type"),
        bedrock.WithAdditionalAttrs(func(r *http.Request) []attr.Attr {
            return []attr.Attr{
                attr.String("user_type", r.Header.Get("X-User-Type")),
            }
        }),
    )

    http.ListenAndServe(":8080", handler)
}

func handleUsers(w http.ResponseWriter, r *http.Request) {
    // Bedrock context already in r.Context() from middleware
    op, ctx := bedrock.Operation(r.Context(), "get_users")
    defer op.Done()

    users, err := fetchUsers(ctx)
    if err != nil {
        op.Register(ctx, attr.Error(err))
        http.Error(w, err.Error(), 500)
        return
    }

    json.NewEncoder(w).Encode(users)
}
```

### Creating Custom Metrics

```go
// At service initialization
ctx, close := bedrock.Init(ctx,
    bedrock.WithStaticAttrs(
        attr.String("env", "production"),
        attr.String("region", "us-west-2"),
    ),
)

// Create metrics (static labels automatically included)
requestCounter := bedrock.Counter(ctx, "api_requests_total",
    "Total API requests", "endpoint", "method")

cacheHits := bedrock.Counter(ctx, "cache_hits_total",
    "Total cache hits", "cache_type")

queueDepth := bedrock.Gauge(ctx, "queue_depth",
    "Current queue depth", "queue_name")

latency := bedrock.Histogram(ctx, "query_latency_ms",
    "Query latency", nil, "db_type")

// Use metrics
requestCounter.With(
    attr.String("endpoint", "/users"),
    attr.String("method", "GET"),
).Inc()

cacheHits.With(attr.String("cache_type", "redis")).Inc()
queueDepth.With(attr.String("queue_name", "jobs")).Set(42)
latency.With(attr.String("db_type", "postgres")).Observe(123.45)
```

### Background Workers

```go
func runWorker(ctx context.Context) error {
    source, ctx := bedrock.Source(ctx, "background.worker",
        bedrock.SourceAttrs(attr.String("worker.type", "async")),
        bedrock.SourceMetricLabels("worker.type"),
    )
    defer source.Done()

    loopCounter := bedrock.Counter(ctx, "worker_iterations", "Total iterations")
    activeGauge := bedrock.Gauge(ctx, "worker_active", "Worker active")
    activeGauge.Set(1)
    defer activeGauge.Set(0)

    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return nil
        case <-ticker.C:
            loopCounter.Inc()
            source.Aggregate(ctx, attr.Sum("jobs_processed", 1))

            // Operations inherit source prefix
            op, ctx := bedrock.Operation(ctx, "process_job")
            defer op.Done()

            if err := processJob(ctx); err != nil {
                op.Register(ctx, attr.Error(err))
            }
        }
    }
}
```

### HTTP Client with Tracing

```go
// Create instrumented client
client := bedrock.NewClient(nil)  // or pass existing *http.Client

// Automatic trace propagation and span creation
resp, err := client.Get("https://api.example.com/users")

// Or use convenience functions
resp, err := bedrock.Get(ctx, "https://api.example.com/users")
resp, err := bedrock.Post(ctx, "https://api.example.com/users",
    "application/json", bytes.NewReader(body))
```

## Full-Stack Observability

**Location**: `example/fullstack/`

### Stack Components

```
┌─────────────────┐
│  Bedrock App    │
│  :8080 (HTTP)   │
│  :9090 (Obs)    │
└──┬────┬───┬──┬──┘
   │    │   │  │
   ▼    ▼   ▼  ▼
Prom Jaeger Loki Pyroscope
   │    │   │  │
   └────┴───┴──┴──┐
                  ▼
              ┌─────────┐
              │ Grafana │
              │  :3000  │
              └─────────┘
```

### Access Points

| Service | URL | Purpose |
|---------|-----|---------|
| Application | http://localhost:8080 | Demo service |
| Observability | http://localhost:9090 | Metrics, pprof, health |
| Grafana | http://localhost:3000 | Unified dashboards (admin/admin) |
| Prometheus | http://localhost:9091 | Metrics storage |
| Jaeger | http://localhost:16686 | Trace visualization |
| Pyroscope | http://localhost:4040 | Continuous profiling |

### Quick Start

```bash
cd example/fullstack
docker-compose up -d

# Generate traffic
curl http://localhost:8080/users

# Open Grafana
open http://localhost:3000  # admin/admin
```

### What You Get

1. **Metrics**: Prometheus scrapes `/metrics` every 15s
2. **Traces**: OTLP export to Jaeger for distributed tracing
3. **Logs**: Promtail collects JSON logs to Loki
4. **Profiling**: Pyroscope scrapes pprof endpoints for flamegraphs
5. **Dashboards**: Grafana provides unified view

### Profiling

**Continuous (Pyroscope)**: Automatic scraping every 15s
- CPU, memory (alloc/inuse), goroutines, mutex, block profiles
- View flamegraphs in Grafana → Explore → Pyroscope

**Manual (pprof)**:
```bash
# CPU profile (30s)
curl -o cpu.prof http://localhost:9090/debug/pprof/profile?seconds=30
go tool pprof cpu.prof

# Heap profile
curl -o heap.prof http://localhost:9090/debug/pprof/heap
go tool pprof -http=:8081 heap.prof
```

## Design Principles

1. **Context-based architecture**: No globals, explicit context passing ensures testability and clarity
2. **Success by default**: Optimistic execution reduces boilerplate; register failures explicitly
3. **Controlled cardinality**: Metric labels defined upfront prevents unbounded explosion
4. **Automatic instrumentation**: Operations generate metrics/traces/logs automatically
5. **Type safety**: Compile-time safety for attributes and metrics via typed functions
6. **Production-ready**: Security defaults (timeouts), graceful shutdown, DoS protection
7. **Zero allocations for noop**: Efficient when not initialized; safe for library code
8. **Enumeration support**: Duplicate operations/steps automatically numbered (`operation[1]`, `operation[2]`)
9. **Explicit over implicit**: Clear API contracts, no hidden behavior
10. **Unified observability**: Logs, metrics, traces, and profiles all connected via trace/span IDs

## Module Information

- **Module**: `github.com/kzs0/bedrock`
- **Go Version**: 1.25+
- **Dependencies**: Standard library only (no external dependencies)

## Summary

Bedrock provides complete observability with minimal code:
- **One initialization**: `bedrock.Init(ctx)`
- **One wrapper per unit of work**: `bedrock.Operation(ctx, name)`
- **Automatic telemetry**: Metrics, traces, logs generated automatically
- **Production-ready**: Security defaults, graceful shutdown, industry-standard exports
- **Full-stack example**: Docker Compose with Prometheus, Jaeger, Grafana, Loki, Pyroscope

Focus on business logic; Bedrock handles observability.
