# Bedrock

An opinionated observability library for Go that provides tracing, metrics, profiling, and structured logging with automatic instrumentation.

## Features

- **Context-based**: No globals, everything flows through `context.Context`
- **Automatic metrics**: Every operation records count, success, failure, and duration (milliseconds)
- **Controlled cardinality**: Define metric labels upfront with `_` defaults for missing values
- **Success by default**: Operations succeed unless errors are registered
- **Clean API**: `Init()`, `Operation()`, `Source()`, `Step()` with `Done()` methods
- **W3C Trace Context**: Standards-compliant distributed tracing with automatic propagation
- **HTTP middleware**: Automatic operation setup for HTTP handlers with trace extraction
- **HTTP client**: Instrumented clients with automatic trace injection and span creation
- **Observability server**: Built-in endpoints for metrics, profiling, and health checks
- **Environment configuration**: Parse from env vars or provide explicit config
- **Canonical logging**: Complete operation lifecycle logging for analysis
- **Convenient APIs**: Direct logging and metrics functions without manual setup
- **Production-ready**: Security timeouts, graceful shutdown, DoS protection, trace sampling

## Table of Contents

- [Quick Start](#quick-start)
- [Core Concepts](#core-concepts)
  - [Initialization](#1-initialization)
  - [Operations](#2-operations)
  - [Sources](#3-sources)
  - [Steps](#4-steps)
  - [Success by Default](#5-success-by-default)
  - [W3C Trace Context Propagation](#6-w3c-trace-context-propagation)
- [API Reference](#api-reference)
  - [Initialization](#initialization)
  - [Operations](#operations)
  - [Sources](#sources)
  - [Steps](#steps)
  - [HTTP Middleware](#http-middleware)
  - [HTTP Client Instrumentation](#http-client-instrumentation)
  - [Convenient Logging](#convenient-logging)
  - [Convenient Metrics](#convenient-metrics)
- [Configuration](#configuration)
  - [Environment Variables](#environment-variables)
  - [Programmatic](#programmatic)
  - [Security Defaults](#security-defaults)
- [Examples](#examples)
  - [HTTP Service](#http-service)
  - [Background Worker](#background-worker)
  - [Nested Operations](#nested-operations)
  - [HTTP Client with Distributed Tracing](#http-client-with-distributed-tracing)
  - [Custom Metrics](#custom-metrics)
  - [Canonical Logging](#canonical-logging-1)
- [Metrics](#metrics)
- [Full-Stack Observability](#full-stack-observability)
- [Design Principles](#design-principles)
- [License](#license)

## Quick Start

```go
package main

import (
    "context"
    "net/http"
    
    "github.com/kzs0/bedrock"
    "github.com/kzs0/bedrock/attr"
)

func main() {
    // 1. Initialize bedrock
    ctx, close := bedrock.Init(context.Background())
    defer close()
    
    // 2. Setup HTTP handler
    mux := http.NewServeMux()
    mux.HandleFunc("/users", handleUsers)
    
    // 3. Wrap with middleware
    handler := bedrock.HTTPMiddleware(ctx, mux)
    http.ListenAndServe(":8080", handler)
}

func handleUsers(w http.ResponseWriter, r *http.Request) {
    op, ctx := bedrock.Operation(r.Context(), "http.get_users")
    defer op.Done()
    
    op.Register(ctx, attr.Int("user_count", 42))
    
    // Convenient logging (includes static attributes automatically)
    bedrock.Info(ctx, "processing request", attr.String("user_id", "123"))
    
    // Your logic here
}
```

## Core Concepts

### 1. Initialization

Initialize bedrock once at startup. This sets up tracing, metrics, and logging infrastructure:

```go
// From environment variables
ctx, close := bedrock.Init(ctx)
defer close()

// With explicit config
ctx, close := bedrock.Init(ctx,
    bedrock.WithConfig(bedrock.Config{
        Service:   "my-service",
        LogLevel:  "info",
        LogFormat: "json",
    }),
    bedrock.WithStaticAttrs(
        attr.String("env", "production"),
        attr.String("version", "1.2.3"),
    ),
)
defer close()
```

**Static attributes** are automatically included in:
- All metrics as labels
- All logs as fields
- All traces as span attributes

### 2. Operations

Operations are units of work that automatically record metrics. They are the primary building block for instrumentation:

```go
op, ctx := bedrock.Operation(ctx, "process_user",
    bedrock.Attrs(attr.String("user_id", "123")),
    bedrock.MetricLabels("user_id", "status"),
)
defer op.Done()

// Register attributes (used in logs, traces, and metrics)
op.Register(ctx, attr.String("status", "active"))

// Register errors (marks operation as failure)
if err != nil {
    op.Register(ctx, attr.Error(err))
    return err
}
```

**Automatic Metrics** (per operation):
- `<name>_count{labels}` - Total operations
- `<name>_successes{labels}` - Successful operations  
- `<name>_failures{labels}` - Failed operations
- `<name>_duration_ms{labels}` - Duration histogram in milliseconds

**Metric Labels**: Only attributes matching registered `MetricLabels` are used as metric labels. This prevents metric cardinality explosion. Missing labels default to `"_"`.

**Operation Hierarchy**: Child operations inherit parent context and can have enumerated names when duplicated (e.g., `operation[1]`, `operation[2]`).

### 3. Sources

Sources represent long-running processes that spawn operations. They're useful for background workers, loops, or services:

```go
source, ctx := bedrock.Source(ctx, "background.worker",
    bedrock.SourceAttrs(attr.String("worker.type", "async")),
    bedrock.SourceMetricLabels("worker.type"),
)
defer source.Done()

// Track aggregates (Sum, Gauge, Histogram)
source.Aggregate(ctx, 
    attr.Sum("loops", 1),
    attr.Gauge("queue_depth", 42),
    attr.Histogram("latency_ms", 123.45),
)

// Operations inherit source config
op, ctx := bedrock.Operation(ctx, "process")
defer op.Done()
// Operation name becomes: "background.worker.process"
```

**Source Benefits**:
- Automatic name prefixing for child operations
- Shared attributes and metric labels across all operations
- Aggregate metrics for tracking overall state

### 4. Steps

Steps are lightweight tracing spans for helper functions. They don't create separate metrics but contribute to their parent operation:

```go
func helper(ctx context.Context) {
    step := bedrock.Step(ctx, "helper", 
        attr.String("key", "value"),
    )
    defer step.Done()
    
    step.Register(ctx, attr.Int("count", 1))
    // Attributes/events propagate to parent operation
}
```

**When to use Steps vs Operations**:
- **Steps**: Helper functions, internal logic, want trace visibility only
- **Operations**: Major units of work, want full metrics and cardinality control

**Step Enumeration**: Like operations, duplicate step names are automatically enumerated (e.g., `helper[1]`, `helper[2]`).

### 5. Success by Default

Operations default to success. Only register errors to mark as failure:

```go
op, ctx := bedrock.Operation(ctx, "db.query")
defer op.Done()

result, err := db.Query(...)
if err != nil {
    op.Register(ctx, attr.Error(err)) // Marks as failure
    return err
}
// Otherwise recorded as success
```

This approach:
- Reduces boilerplate (no need to explicitly mark success)
- Makes error tracking explicit
- Aligns with Go's error handling patterns

### 6. W3C Trace Context Propagation

Bedrock uses the [W3C Trace Context](https://www.w3.org/TR/trace-context/) standard for distributed tracing. Trace context automatically flows across service boundaries through HTTP headers.

**Architecture**: Bedrock implements a modular propagation system with these packages:

| Package | Purpose |
|---------|---------|
| `trace/propagator.go` | Generic `Propagator` interface for any transport |
| `trace/w3c` | W3C format parsing/formatting utilities (protocol-agnostic) |
| `trace/http` | HTTP propagator implementation |
| `example/grpc` | gRPC propagator reference (copy into your project) |

**Traceparent Header Format**: `00-{trace-id}-{parent-id}-{flags}`

```
00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01
│  │                                │                │
│  └─ trace-id (32 hex chars)      │                └─ flags (01 = sampled)
│                                   └─ parent-id (16 hex chars)
└─ version (00)
```

**Tracestate Header**: Optional vendor-specific tracing state
```
key1=value1,key2=value2
```

**Automatic Propagation**:

1. **HTTP Middleware** - Extracts trace context from inbound requests:
```go
handler := bedrock.HTTPMiddleware(ctx, mux,
    bedrock.WithTracePropagation(true), // Default: true
)
// Middleware extracts traceparent/tracestate and creates remote parent span
```

2. **HTTP Client** - Injects trace context into outbound requests:
```go
client := bedrock.NewClient(nil)
resp, err := client.Get(url)
// Client automatically injects traceparent/tracestate headers
```

**Custom Propagators**:

Implement the `trace.Propagator` interface for other transports (Kafka, AMQP, gRPC, etc.):

```go
type Propagator interface {
    Extract(carrier any) (SpanContext, error)
    Inject(ctx context.Context, carrier any) error
}
```

Example Kafka propagator:

```go
type KafkaPropagator struct{}

func (p *KafkaPropagator) Extract(carrier any) (trace.SpanContext, error) {
    headers := carrier.([]kafka.Header)
    for _, h := range headers {
        if h.Key == "traceparent" {
            traceID, spanID, flags, err := w3c.ParseTraceparent(string(h.Value))
            if err != nil {
                return trace.SpanContext{}, err
            }
            return trace.NewRemoteSpanContext(traceID, spanID, "", flags&w3c.SampledFlag != 0), nil
        }
    }
    return trace.SpanContext{}, errors.New("no traceparent header")
}

func (p *KafkaPropagator) Inject(ctx context.Context, carrier any) error {
    headers := carrier.(*[]kafka.Header)
    span := trace.SpanFromContext(ctx)
    if span == nil {
        return nil
    }
    
    traceparent := w3c.FormatTraceparent(span.TraceID(), span.SpanID(), true)
    *headers = append(*headers, kafka.Header{
        Key:   "traceparent",
        Value: []byte(traceparent),
    })
    return nil
}
```

**W3C Utilities** (`trace/w3c` package):

```go
// Parse traceparent header
traceID, spanID, flags, err := w3c.ParseTraceparent(value)

// Format traceparent header
traceparent := w3c.FormatTraceparent(traceID, spanID, sampled)

// Parse tracestate header
entries, err := w3c.ParseTracestate(value)

// Format tracestate header
tracestate := w3c.FormatTracestate(entries)

// Validation
isValid := w3c.IsValidTracestateKey(key)
isValid = w3c.IsValidTracestateValue(value)
```

**gRPC Example**:

See `example/grpc/` for a complete gRPC propagator implementation with client/server interceptors. This is kept separate to avoid adding gRPC as a dependency.

**Validation Rules**:
- Invalid `traceparent` → starts a new trace (ignores `tracestate`)
- Header names are case-insensitive per HTTP RFC
- Multiple `tracestate` headers are combined with commas
- Trace/Span IDs must be non-zero lowercase hex characters

## API Reference

### Initialization

#### `Init(ctx, opts...) (context.Context, func())`

Initialize bedrock and return context + cleanup function.

```go
ctx, close := bedrock.Init(ctx,
    bedrock.WithConfig(cfg),
    bedrock.WithStaticAttrs(attr.String("env", "prod")),
)
defer close()
```

**Options**:
- `WithConfig(Config)` - Explicit configuration
- `WithStaticAttrs(...attr.Attr)` - Static attributes for all operations

**Returns**: 
- Updated context with bedrock instance
- Cleanup function for graceful shutdown

### Operations

#### `Operation(ctx, name, opts...) (*Op, context.Context)`

Start a new operation or create child if parent exists.

```go
op, ctx := bedrock.Operation(ctx, "process_user",
    bedrock.Attrs(attr.String("user_id", "123")),
    bedrock.MetricLabels("user_id", "status"),
)
defer op.Done()
```

**Options**:
- `Attrs(...attr.Attr)` - Set initial attributes
- `MetricLabels(...string)` - Define metric label names (controls cardinality)

**Op Methods**:
- `Register(ctx, ...interface{})` - Add attributes, events, or errors
- `Done()` - Complete operation and record metrics

**Registerable Items**:
- `attr.Attr` - Attributes for logs, traces, and metrics
- `attr.Event` - Trace events (not added to operation attributes)
- `attr.Error(err)` - Errors (marks operation as failure)

### Sources

#### `Source(ctx, name, opts...) (*Src, context.Context)`

Register a source for long-running processes.

```go
source, ctx := bedrock.Source(ctx, "worker",
    bedrock.SourceAttrs(attr.String("type", "async")),
    bedrock.SourceMetricLabels("type"),
)
defer source.Done()
```

**Options**:
- `SourceAttrs(...attr.Attr)` - Source attributes (inherited by operations)
- `SourceMetricLabels(...string)` - Metric labels for all operations

**Src Methods**:
- `Aggregate(ctx, ...attr.Aggregation)` - Record aggregate metrics
- `Done()` - No-op (sources don't complete, for API consistency)

**Aggregation Types**:
- `attr.Sum(name, value)` - Increment counter
- `attr.Gauge(name, value)` - Set gauge value
- `attr.Histogram(name, value)` - Observe histogram value

### Steps

#### `Step(ctx, name, attrs...) *Step`

Create a lightweight step for tracing.

```go
step := bedrock.Step(ctx, "helper",
    attr.String("key", "value"),
)
defer step.Done()
```

**Step Methods**:
- `Register(ctx, ...attr.Attr)` - Add attributes
- `Done()` - End step

**Note**: Steps don't create separate metrics. They contribute to parent operation traces.

### HTTP Middleware

#### `HTTPMiddleware(ctx, handler, opts...) http.Handler`

Wrap HTTP handler with automatic operations.

```go
handler := bedrock.HTTPMiddleware(ctx, mux,
    bedrock.WithOperationName("http.request"),
    bedrock.WithAdditionalLabels("user_agent"),
)
```

**Options**:
- `WithOperationName(string)` - Custom operation name (default: "http.request")
- `WithAdditionalLabels(...string)` - Extra metric labels
- `WithAdditionalAttrs(func(*http.Request) []attr.Attr)` - Custom attributes
- `WithSuccessCodes(...int)` - Define success status codes (default: 200-399)

**Default Attributes**:
- `http.method` - Request method (GET, POST, etc.)
- `http.route` - Request path
- `http.scheme` - http or https
- `http.host` - Host header
- `http.user_agent` - User-Agent header
- `http.status_code` - Response status code

**Default Metric Labels**: `http_method`, `http_route`, `http_status_code`

### HTTP Client Instrumentation

Bedrock provides instrumented HTTP clients that automatically create spans and propagate W3C Trace Context headers.

#### `NewClient(base *http.Client) *http.Client`

Create an instrumented HTTP client that wraps an existing client:

```go
// Create from scratch
client := bedrock.NewClient(nil)

// Wrap existing client
baseClient := &http.Client{Timeout: 30 * time.Second}
client := bedrock.NewClient(baseClient)

// Use like a normal http.Client
resp, err := client.Get(ctx, url)
```

**Automatic Behavior**:
- Creates a client span for each request with name `HTTP {METHOD}`
- Injects W3C Trace Context headers (`traceparent`, `tracestate`)
- Records request attributes: `http.method`, `http.url`, `http.host`, `http.scheme`, `http.target`
- Records response `http.status_code`
- Marks as error for 4xx/5xx responses
- Preserves all client settings (timeout, redirect policy, cookie jar)

#### Convenience Functions

For one-off requests without creating a client:

```go
// GET request
resp, err := bedrock.Get(ctx, "https://api.example.com/users")

// POST request
resp, err := bedrock.Post(ctx, 
    "https://api.example.com/users",
    "application/json", 
    bytes.NewReader(jsonBody))

// Full control with custom request
req, _ := http.NewRequestWithContext(ctx, "PUT", url, body)
req.Header.Set("Authorization", "Bearer "+token)
resp, err := bedrock.Do(ctx, req)
```

**Note**: For better performance with multiple requests, use `NewClient()` to create a reusable client.

**Trace Propagation**:

HTTP clients automatically propagate trace context across service boundaries:

```go
func handleRequest(w http.ResponseWriter, r *http.Request) {
    op, ctx := bedrock.Operation(r.Context(), "handle_request")
    defer op.Done()
    
    // This request becomes a child span and shares the same trace_id
    resp, err := bedrock.Get(ctx, "https://downstream-service/api")
    if err != nil {
        op.Register(ctx, attr.Error(err))
        http.Error(w, err.Error(), 500)
        return
    }
    
    // Downstream service receives traceparent header linking to this trace
}
```

### Convenient Logging

Direct logging functions that automatically include static attributes and trace context:

```go
// Log levels
bedrock.Debug(ctx, "debug message", attr.String("key", "value"))
bedrock.Info(ctx, "info message", attr.Int("count", 42))
bedrock.Warn(ctx, "warning message", attr.Duration("timeout", 5*time.Second))
bedrock.Error(ctx, "error message", attr.Error(err))

// Custom level
bedrock.Log(ctx, slog.LevelInfo, "custom log", attr.String("key", "value"))
```

**Benefits**:
- No need to manually get logger from context
- Static attributes automatically included
- Trace context (span ID, trace ID) automatically added
- Uses structured logging (slog)

### Convenient Metrics

Direct metric creation functions that automatically include static labels:

```go
// Counter
counter := bedrock.Counter(ctx, "requests_total", "Total requests", "method", "status")
counter.With(attr.String("method", "GET"), attr.String("status", "200")).Inc()
counter.Inc() // Uses static labels only

// Gauge
gauge := bedrock.Gauge(ctx, "active_connections", "Active connections")
gauge.Set(42) // Automatically includes static labels
gauge.Inc()
gauge.Dec()

// Histogram
hist := bedrock.Histogram(ctx, "duration_ms", "Duration in ms", nil, "endpoint")
hist.With(attr.String("endpoint", "/users")).Observe(123.45)
hist.Observe(100) // Uses static labels only
```

**Benefits**:
- No need to manually access metrics registry
- Static labels automatically included
- Type-safe API with label validation
- Reuses existing metrics (registry-based)

## Configuration

### Environment Variables

```bash
# Service identification
BEDROCK_SERVICE=my-service

# Tracing
BEDROCK_TRACE_URL=http://localhost:4318/v1/traces
BEDROCK_TRACE_SAMPLE_RATE=1.0  # 0.0 to 1.0

# Logging
BEDROCK_LOG_LEVEL=info         # debug, info, warn, error
BEDROCK_LOG_FORMAT=json        # json or text
BEDROCK_LOG_CANONICAL=true     # Enable operation lifecycle logs

# Metrics
BEDROCK_METRIC_PREFIX=myapp    # Prefix for all metrics
BEDROCK_METRIC_BUCKETS=5,10,25,50,100,250,500,1000  # Custom buckets (ms)

# Server (observability endpoints)
BEDROCK_SERVER_ENABLED=false   # Auto-start server
BEDROCK_SERVER_ADDR=:9090      # Server address
BEDROCK_SERVER_METRICS=true    # Enable /metrics
BEDROCK_SERVER_PPROF=true      # Enable /debug/pprof
BEDROCK_SERVER_READ_TIMEOUT=10s
BEDROCK_SERVER_READ_HEADER_TIMEOUT=5s
BEDROCK_SERVER_WRITE_TIMEOUT=30s
BEDROCK_SERVER_IDLE_TIMEOUT=120s
BEDROCK_SERVER_MAX_HEADER_BYTES=1048576  # 1 MB

# Shutdown
BEDROCK_SHUTDOWN_TIMEOUT=30s   # Graceful shutdown timeout
```

### Programmatic

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

ctx, close := bedrock.Init(ctx, bedrock.WithConfig(cfg))
defer close()
```

**Config Parsing**: Use `config.Parse[T]()` to parse custom config structs from environment variables:

```go
type Config struct {
    Bedrock  bedrock.Config
    Port     int    `env:"PORT" envDefault:"8080"`
    Database string `env:"DATABASE_URL"`
}

cfg, err := config.Parse[Config]()
if err != nil {
    // Handle error
}

ctx, close := bedrock.Init(ctx, bedrock.WithConfig(cfg.Bedrock))
defer close()
```

### Security Defaults

Bedrock provides production-ready security defaults to protect against DoS attacks and resource exhaustion.

**Observability Server** (metrics/pprof endpoints):

```go
b := bedrock.FromContext(ctx)
server := b.NewServer(bedrock.DefaultServerConfig())
go server.ListenAndServe()
```

**Default Security Settings**:

| Setting | Default | Purpose |
|---------|---------|---------|
| `ReadTimeout` | 10s | Maximum time to read entire request (including body) |
| `ReadHeaderTimeout` | 5s | **Slowloris attack protection** - limits header reading time |
| `WriteTimeout` | 30s | Maximum time to write response |
| `IdleTimeout` | 120s | Keep-alive connection timeout |
| `MaxHeaderBytes` | 1 MB | Prevents header bomb attacks |
| `ShutdownTimeout` | 30s | Graceful shutdown wait time |

**Why These Defaults Matter**:

- **ReadHeaderTimeout (5s)**: Prevents Slowloris DoS attacks where attackers send headers very slowly to exhaust server connections
- **ReadTimeout (10s)**: Limits total request read time to prevent slow-read attacks
- **WriteTimeout (30s)**: Prevents slow-write attacks and stalled connections
- **IdleTimeout (120s)**: Closes idle keep-alive connections to free resources
- **MaxHeaderBytes (1MB)**: Prevents attackers from sending enormous headers
- **ShutdownTimeout (30s)**: Allows in-flight requests to complete during graceful shutdown

**Custom Configuration**:

Override defaults for specific requirements:

```go
server := b.NewServer(bedrock.ServerConfig{
    Addr:              ":9090",
    EnableMetrics:     true,
    EnablePprof:       true,
    EnableHealth:      true,
    ReadTimeout:       5 * time.Second,
    ReadHeaderTimeout: 2 * time.Second,
    WriteTimeout:      10 * time.Second,
    IdleTimeout:       60 * time.Second,
    MaxHeaderBytes:    512 * 1024, // 512KB
    ShutdownTimeout:   15 * time.Second,
})
```

**Application HTTP Servers**:

Apply the same security defaults to your application servers:

```go
appServer := &http.Server{
    Addr:              ":8080",
    Handler:           bedrock.HTTPMiddleware(ctx, mux),
    ReadTimeout:       10 * time.Second,
    ReadHeaderTimeout: 5 * time.Second,  // Slowloris protection
    WriteTimeout:      30 * time.Second,
    IdleTimeout:       120 * time.Second,
    MaxHeaderBytes:    1 << 20, // 1 MB
}

// Graceful shutdown
go func() {
    <-ctx.Done()
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    appServer.Shutdown(shutdownCtx)
}()

appServer.ListenAndServe()
```

## Examples

### HTTP Service

```go
func main() {
    ctx, close := bedrock.Init(context.Background())
    defer close()
    
    // Start observability server
    b := bedrock.FromContext(ctx)
    obsServer := b.NewServer(bedrock.DefaultServerConfig())
    go obsServer.ListenAndServe()
    // Metrics: http://localhost:9090/metrics
    // Health:  http://localhost:9090/health
    // Pprof:   http://localhost:9090/debug/pprof/
    
    // Setup application server
    mux := http.NewServeMux()
    mux.HandleFunc("/", handler)
    
    http.ListenAndServe(":8080", bedrock.HTTPMiddleware(ctx, mux))
}

func handler(w http.ResponseWriter, r *http.Request) {
    op, ctx := bedrock.Operation(r.Context(), "handle_request")
    defer op.Done()
    
    op.Register(ctx, attr.String("custom", "value"))
    bedrock.Info(ctx, "processing request")
    
    w.Write([]byte("OK"))
}
```

### Background Worker

```go
func main() {
    ctx, close := bedrock.Init(context.Background())
    defer close()
    
    source, ctx := bedrock.Source(ctx, "worker")
    defer source.Done()
    
    for job := range jobs {
        source.Aggregate(ctx, attr.Sum("jobs_processed", 1))
        processJob(ctx, job)
    }
}

func processJob(ctx context.Context, job Job) {
    op, ctx := bedrock.Operation(ctx, "process",
        bedrock.Attrs(attr.String("job.id", job.ID)),
    )
    defer op.Done()
    
    if err := job.Execute(); err != nil {
        op.Register(ctx, attr.Error(err))
        bedrock.Error(ctx, "job failed", attr.Error(err))
    }
}
```

### Nested Operations

```go
func handleRequest(w http.ResponseWriter, r *http.Request) {
    op, ctx := bedrock.Operation(r.Context(), "handle_request")
    defer op.Done()
    
    user, err := getUser(ctx, "123")
    if err != nil {
        op.Register(ctx, attr.Error(err))
        http.Error(w, err.Error(), 500)
        return
    }
    
    json.NewEncoder(w).Encode(user)
}

func getUser(ctx context.Context, id string) (*User, error) {
    op, ctx := bedrock.Operation(ctx, "db.get_user",
        bedrock.Attrs(attr.String("user.id", id)),
        bedrock.MetricLabels("user.id"),
    )
    defer op.Done()
    
    user, err := db.Get(id)
    if err != nil {
        op.Register(ctx, attr.Error(err))
        return nil, err
    }
    
    return user, nil
}
```

### HTTP Client with Distributed Tracing

Example of making HTTP requests with automatic trace propagation:

```go
func main() {
    ctx, close := bedrock.Init(context.Background())
    defer close()
    
    // Create reusable instrumented client
    client := bedrock.NewClient(&http.Client{
        Timeout: 30 * time.Second,
    })
    
    mux := http.NewServeMux()
    mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
        handleAPI(w, r, client)
    })
    
    http.ListenAndServe(":8080", bedrock.HTTPMiddleware(ctx, mux))
}

func handleAPI(w http.ResponseWriter, r *http.Request, client *http.Client) {
    op, ctx := bedrock.Operation(r.Context(), "api.aggregate_data")
    defer op.Done()
    
    // Make parallel requests to downstream services
    // All share the same trace_id and become child spans
    var wg sync.WaitGroup
    results := make(chan Response, 3)
    
    services := []string{
        "http://users-service/api/users",
        "http://orders-service/api/orders",
        "http://inventory-service/api/inventory",
    }
    
    for _, url := range services {
        wg.Add(1)
        go func(serviceURL string) {
            defer wg.Done()
            
            // Each request creates a child span with automatic trace propagation
            resp, err := client.Get(serviceURL)
            if err != nil {
                bedrock.Error(ctx, "service request failed", 
                    attr.String("url", serviceURL),
                    attr.Error(err))
                return
            }
            defer resp.Body.Close()
            
            // Process response
            var data Response
            json.NewDecoder(resp.Body).Decode(&data)
            results <- data
        }(url)
    }
    
    wg.Wait()
    close(results)
    
    // Aggregate results
    aggregated := aggregateResults(results)
    
    op.Register(ctx, attr.Int("total_results", len(aggregated)))
    json.NewEncoder(w).Encode(aggregated)
}

// Using convenience functions for one-off requests
func fetchUserData(ctx context.Context, userID string) (*User, error) {
    op, ctx := bedrock.Operation(ctx, "fetch_user")
    defer op.Done()
    
    // Simple GET request
    resp, err := bedrock.Get(ctx, "https://api.example.com/users/"+userID)
    if err != nil {
        op.Register(ctx, attr.Error(err))
        return nil, err
    }
    defer resp.Body.Close()
    
    var user User
    if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
        op.Register(ctx, attr.Error(err))
        return nil, err
    }
    
    return &user, nil
}

func createOrder(ctx context.Context, order *Order) error {
    op, ctx := bedrock.Operation(ctx, "create_order")
    defer op.Done()
    
    body, _ := json.Marshal(order)
    
    // Simple POST request
    resp, err := bedrock.Post(ctx, 
        "https://api.example.com/orders",
        "application/json",
        bytes.NewReader(body))
    if err != nil {
        op.Register(ctx, attr.Error(err))
        return err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode >= 400 {
        err := fmt.Errorf("create order failed: %s", resp.Status)
        op.Register(ctx, attr.Error(err))
        return err
    }
    
    return nil
}
```

**Trace Visualization**:

When viewing traces in Jaeger, you'll see:
- Parent span: `api.aggregate_data`
- Child spans: `HTTP GET` (one for each downstream service)
- All spans share the same `trace_id`
- Request timings and errors are visible

### Custom Metrics

```go
func main() {
    ctx, close := bedrock.Init(context.Background(),
        bedrock.WithStaticAttrs(
            attr.String("env", "production"),
            attr.String("region", "us-west-2"),
        ),
    )
    defer close()
    
    // Create custom metrics (static labels automatically included)
    requestCounter := bedrock.Counter(ctx, "api_requests_total", 
        "Total API requests", "endpoint", "method")
    
    cacheHits := bedrock.Counter(ctx, "cache_hits_total",
        "Total cache hits", "cache_type")
    
    queueDepth := bedrock.Gauge(ctx, "queue_depth",
        "Current queue depth", "queue_name")
    
    latency := bedrock.Histogram(ctx, "query_latency_ms",
        "Query latency in milliseconds", nil, "db_type")
    
    // Use metrics
    requestCounter.With(
        attr.String("endpoint", "/users"),
        attr.String("method", "GET"),
    ).Inc()
    
    cacheHits.With(attr.String("cache_type", "redis")).Inc()
    queueDepth.With(attr.String("queue_name", "jobs")).Set(42)
    latency.With(attr.String("db_type", "postgres")).Observe(123.45)
    
    // Or use without additional labels (static labels only)
    cacheHits.Inc()
    queueDepth.Set(10)
}
```

### Canonical Logging

Enable complete operation lifecycle logging for analysis:

```go
// Set environment variable
os.Setenv("BEDROCK_LOG_CANONICAL", "true")

ctx, close := bedrock.Init(context.Background())
defer close()

op, ctx := bedrock.Operation(ctx, "process_user",
    bedrock.Attrs(attr.String("user_id", "123")),
)
defer op.Done()

op.Register(ctx, attr.String("status", "active"))
```

**Output** (when operation completes):
```json
{
  "time": "2026-01-18T12:34:56Z",
  "level": "INFO",
  "msg": "operation completed",
  "operation": "process_user",
  "duration_ms": 123.45,
  "success": true,
  "user_id": "123",
  "status": "active",
  "trace_id": "abc123...",
  "span_id": "def456..."
}
```

**Benefits**:
- Complete operation lifecycle in structured logs
- Queryable in Loki/Grafana
- Includes all attributes, duration, and success status
- Automatic trace correlation
- Useful for debugging and analysis

## Metrics

Automatic metrics for every operation:

```
# Operation count
process_user_count{user_id="123",status="active",env="production"} 10

# Successes
process_user_successes{user_id="123",status="active",env="production"} 9

# Failures  
process_user_failures{user_id="123",status="active",env="production"} 1

# Duration in milliseconds (histogram)
process_user_duration_ms_bucket{user_id="123",status="active",env="production",le="10"} 5
process_user_duration_ms_bucket{user_id="123",status="active",env="production",le="50"} 8
process_user_duration_ms_sum{user_id="123",status="active",env="production"} 234.5
process_user_duration_ms_count{user_id="123",status="active",env="production"} 10
```

**Note**: Static attributes (e.g., `env="production"`) are automatically added to all metrics.

**Observability Server**:

The observability server provides metrics, profiling, and health check endpoints:

```go
b := bedrock.FromContext(ctx)
server := b.NewServer(bedrock.ServerConfig{
    Addr:          ":9090",
    EnableMetrics: true,
    EnablePprof:   true,
    EnableHealth:  true,
})
go server.ListenAndServe()
```

**Available Endpoints**:

| Endpoint | Purpose |
|----------|---------|
| `/metrics` | Prometheus exposition format metrics |
| `/health` | Health check (returns "ok") |
| `/ready` | Readiness check (returns "ok") |
| `/debug/pprof/` | pprof index with all available profiles |
| `/debug/pprof/profile?seconds=N` | CPU profile (30s default) |
| `/debug/pprof/heap` | Heap memory profile |
| `/debug/pprof/goroutine` | Goroutine stack traces |
| `/debug/pprof/allocs` | Memory allocation profile |
| `/debug/pprof/block` | Block contention profile |
| `/debug/pprof/mutex` | Mutex contention profile |
| `/debug/pprof/threadcreate` | Thread creation profile |
| `/debug/pprof/trace?seconds=N` | Execution trace |

**Usage Examples**:

```bash
# View metrics
curl http://localhost:9090/metrics

# CPU profile (30 seconds)
curl -o cpu.prof http://localhost:9090/debug/pprof/profile?seconds=30
go tool pprof cpu.prof

# Heap profile with visualization
curl -o heap.prof http://localhost:9090/debug/pprof/heap
go tool pprof -http=:8081 heap.prof

# Check health
curl http://localhost:9090/health
```

## Full-Stack Observability

Bedrock includes a complete observability stack example with Docker Compose:

**Location**: `/example/fullstack/`

**Stack Components**:
- **Prometheus** - Metrics collection and storage
- **Jaeger** - Distributed tracing visualization
- **Grafana** - Unified dashboard for metrics, traces, logs, and profiles
- **Loki + Promtail** - Log aggregation and querying
- **Pyroscope** - Continuous profiling (CPU, memory, goroutines)

**Quick Start**:

```bash
cd example/fullstack
docker-compose up -d
```

**Access Points**:
- **Application**: http://localhost:8080
- **Grafana**: http://localhost:3000 (admin/admin)
- **Prometheus**: http://localhost:9091
- **Jaeger**: http://localhost:16686
- **Pyroscope**: http://localhost:4040

**Features**:
- Pre-configured datasources for all components
- Automatic metric scraping from `:9090/metrics`
- OTLP trace export to Jaeger
- JSON log collection via Promtail
- Continuous profiling (CPU, heap, goroutines) via pprof scraping
- Health checks for all services

**Profiling Options**:

1. **Manual profiling** (pprof):
```bash
# CPU profile (30 seconds)
curl -o cpu.prof http://localhost:9090/debug/pprof/profile?seconds=30
go tool pprof cpu.prof

# Heap profile
curl -o heap.prof http://localhost:9090/debug/pprof/heap
go tool pprof heap.prof

# Goroutine profile
curl -o goroutine.prof http://localhost:9090/debug/pprof/goroutine
go tool pprof goroutine.prof
```

2. **Continuous profiling** (Pyroscope):
- Automatically scrapes pprof endpoints every 15 seconds
- View flamegraphs in Grafana or Pyroscope UI
- Compare profiles over time
- Analyze CPU, memory (alloc/inuse), goroutines, mutex, and block profiles

**Configuration Files**:
- `docker-compose.yml` - Stack orchestration
- `config/prometheus.yml` - Metric scraping config
- `config/loki.yml` - Log storage config
- `config/promtail.yml` - Log collection config
- `config/pyroscope.yml` - Profiling config
- `grafana/datasources/` - Pre-configured data sources

## Design Principles

1. **Context flows everything**: No globals, explicit context passing
2. **Success by default**: Optimistic execution, register failures explicitly
3. **Explicit labels**: Control cardinality upfront, prevent metric explosion
4. **Automatic instrumentation**: Metrics without manual tracking
5. **Clean API**: Simple, consistent patterns across all operations
6. **Production-ready**: Security defaults, graceful shutdown, DoS protection
7. **Unified observability**: Logs, metrics, traces, and profiles all connected
8. **Type-safe**: Compile-time safety for attributes and metrics
9. **Zero allocations for noop**: When not initialized, all operations are no-ops
10. **Enumeration support**: Handles duplicate operations/steps automatically

## License

MIT
