# Bedrock

An opinionated observability library for Go that provides tracing, metrics, profiling, and structured logging with automatic instrumentation.

## Features

- **Context-based**: No globals, everything flows through `context.Context`
- **Automatic metrics**: Every operation records count, success, failure, and duration (milliseconds)
- **Controlled cardinality**: Define metric labels upfront with `_` defaults for missing values
- **Success by default**: Operations succeed unless errors are registered
- **Clean API**: `Init()`, `Operation()`, `Source()`, `Span()` with `Done()` methods
- **HTTP middleware**: Automatic operation setup for HTTP handlers
- **Environment configuration**: Parse from env vars or provide explicit config

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
    ctx, op := bedrock.Operation(r.Context(), "http.get_users")
    defer op.Done()
    
    op.Register(ctx, attr.Int("user_count", 42))
    // Your logic here
}
```

## Core Concepts

### 1. Initialization

Initialize bedrock once at startup:

```go
// From environment variables
ctx, close := bedrock.Init(ctx)
defer close()

// With explicit config
ctx, close := bedrock.Init(ctx,
    bedrock.WithConfig(bedrock.Config{
        ServiceName: "my-service",
    }),
    bedrock.WithStaticAttrs(
        attr.String("env", "production"),
    ),
)
defer close()
```

### 2. Operations

Operations are units of work that automatically record metrics:

```go
ctx, op := bedrock.Operation(ctx, "process_user",
    bedrock.Attrs(attr.String("user_id", "123")),
    bedrock.MetricLabels("user_id", "status"),
)
defer op.Done()

// Register attributes
op.Register(ctx, attr.String("status", "active"))

// Register errors (marks as failure)
if err != nil {
    op.Register(ctx, err)
    return err
}
```

**Automatic Metrics** (per operation):
- `<name>_count` - Total operations
- `<name>_successes` - Successful operations  
- `<name>_failures` - Failed operations
- `<name>_duration_ms` - Duration in milliseconds

### 3. Sources

Sources represent long-running processes that spawn operations:

```go
ctx, source := bedrock.Source(ctx, "background.worker",
    bedrock.SourceAttrs(attr.String("worker.type", "async")),
    bedrock.SourceMetricLabels("worker.type"),
)
defer source.Done()

// Track aggregates
source.Aggregate(ctx, attr.Sum("loops", 1))

// Operations inherit source config
ctx, op := bedrock.Operation(ctx, "process")
defer op.Done()
// Operation name: "background.worker.process"
```

### 4. Spans

Lightweight tracing for helper functions (no full metrics):

```go
func helper(ctx context.Context) {
    ctx, span := bedrock.Span(ctx, "helper")
    defer span.Done()
    
    span.Register(ctx, attr.Int("count", 1))
}
```

### 5. Success by Default

Operations default to success. Register errors to mark as failure:

```go
ctx, op := bedrock.Operation(ctx, "db.query")
defer op.Done()

result, err := db.Query(...)
if err != nil {
    op.Register(ctx, err) // Marks as failure
    return err
}
// Otherwise recorded as success
```

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

### Operations

#### `Operation(ctx, name, opts...) (context.Context, *Op)`

Start a new operation or create child if parent exists.

```go
ctx, op := bedrock.Operation(ctx, "process_user",
    bedrock.Attrs(attr.String("user_id", "123")),
    bedrock.MetricLabels("user_id", "status"),
)
defer op.Done()
```

**Options**:
- `Attrs(...attr.Attr)` - Set attributes
- `MetricLabels(...string)` - Define metric label names

**Op Methods**:
- `Register(ctx, ...interface{})` - Add attributes or errors
- `Done()` - Complete operation and record metrics

### Sources

#### `Source(ctx, name, opts...) (context.Context, *Src)`

Register a source for long-running processes.

```go
ctx, source := bedrock.Source(ctx, "worker",
    bedrock.SourceAttrs(attr.String("type", "async")),
    bedrock.SourceMetricLabels("type"),
)
defer source.Done()
```

**Options**:
- `SourceAttrs(...attr.Attr)` - Source attributes
- `SourceMetricLabels(...string)` - Metric labels for operations

**Src Methods**:
- `Aggregate(ctx, ...interface{})` - Record aggregate metrics
- `Done()` - No-op (sources don't complete)

### Spans

#### `Span(ctx, name, attrs...) (context.Context, *Spn)`

Create a lightweight span for tracing.

```go
ctx, span := bedrock.Span(ctx, "helper",
    attr.String("key", "value"),
)
defer span.Done()
```

**Spn Methods**:
- `Register(ctx, ...attr.Attr)` - Add attributes
- `Done()` - End span

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
- `WithOperationName(string)` - Custom operation name
- `WithAdditionalLabels(...string)` - Extra metric labels
- `WithAdditionalAttrs(func(*http.Request) []attr.Attr)` - Custom attributes
- `WithSuccessCodes(...int)` - Define success status codes

## Configuration

### Environment Variables

```bash
SERVICE_NAME=my-service
TRACE_ENDPOINT=http://localhost:4318/v1/traces
OTEL_TRACES_SAMPLER_ARG=1.0
LOG_LEVEL=info
LOG_FORMAT=json
SHUTDOWN_TIMEOUT=5s
```

### Programmatic

```go
cfg := bedrock.Config{
    ServiceName:     "my-service",
    TraceEndpoint:   "http://localhost:4318/v1/traces",
    LogLevel:        "info",
    LogFormat:       "json",
    ShutdownTimeout: 5 * time.Second,
    TraceSampleRate: 1.0,
}

ctx, close := bedrock.Init(ctx, bedrock.WithConfig(cfg))
defer close()
```

## Examples

### HTTP Service

```go
func main() {
    ctx, close := bedrock.Init(context.Background())
    defer close()
    
    mux := http.NewServeMux()
    mux.HandleFunc("/", handler)
    
    http.ListenAndServe(":8080", bedrock.HTTPMiddleware(ctx, mux))
}

func handler(w http.ResponseWriter, r *http.Request) {
    ctx, op := bedrock.Operation(r.Context(), "handle_request")
    defer op.Done()
    
    op.Register(ctx, attr.String("custom", "value"))
    w.Write([]byte("OK"))
}
```

### Background Worker

```go
func main() {
    ctx, close := bedrock.Init(context.Background())
    defer close()
    
    ctx, source := bedrock.Source(ctx, "worker")
    defer source.Done()
    
    for job := range jobs {
        processJob(ctx, job)
    }
}

func processJob(ctx context.Context, job Job) {
    ctx, op := bedrock.Operation(ctx, "process",
        bedrock.Attrs(attr.String("job.id", job.ID)),
    )
    defer op.Done()
    
    if err := job.Execute(); err != nil {
        op.Register(ctx, err)
    }
}
```

### Nested Operations

```go
func handleRequest(w http.ResponseWriter, r *http.Request) {
    ctx, op := bedrock.Operation(r.Context(), "handle_request")
    defer op.Done()
    
    user, err := getUser(ctx, "123")
    if err != nil {
        op.Register(ctx, err)
        http.Error(w, err.Error(), 500)
        return
    }
    
    json.NewEncoder(w).Encode(user)
}

func getUser(ctx context.Context, id string) (*User, error) {
    ctx, op := bedrock.Operation(ctx, "db.get_user",
        bedrock.Attrs(attr.String("user.id", id)),
        bedrock.MetricLabels("user.id"),
    )
    defer op.Done()
    
    user, err := db.Get(id)
    if err != nil {
        op.Register(ctx, err)
        return nil, err
    }
    
    return user, nil
}
```

## Metrics

Automatic metrics for every operation:

```
# Operation count
process_user_count{user_id="123",status="active"} 10

# Successes
process_user_successes{user_id="123",status="active"} 9

# Failures  
process_user_failures{user_id="123",status="active"} 1

# Duration in milliseconds
process_user_duration_ms_bucket{user_id="123",status="active",le="10"} 5
process_user_duration_ms_bucket{user_id="123",status="active",le="50"} 8
process_user_duration_ms_sum{user_id="123",status="active"} 234.5
process_user_duration_ms_count{user_id="123",status="active"} 10
```

Access metrics via:

```go
b := bedrock.FromContext(ctx)
server := b.NewServer(bedrock.ServerConfig{
    Addr:          ":9090",
    EnableMetrics: true,
    EnablePprof:   true,
})
go server.ListenAndServe()
// Metrics at http://localhost:9090/metrics
```

## Design Principles

1. **Context flows everything**: No globals, explicit context passing
2. **Success by default**: Optimistic execution, register failures
3. **Explicit labels**: Control cardinality, prevent metric explosion
4. **Automatic instrumentation**: Metrics without manual tracking
5. **Clean API**: Simple, consistent patterns

## Migration from v1

### Before
```go
b := bedrock.MustInit()
op := b.StartOperation("test", bedrock.WithAttrs(attr.String("key", "val")))
defer op.Done()
```

### After
```go
ctx, close := bedrock.Init(ctx)
defer close()

ctx, op := bedrock.Operation(ctx, "test",
    bedrock.Attrs(attr.String("key", "val")),
)
defer op.Done()
```

**Key Changes**:
- `Init()` returns `(ctx, close)` instead of bedrock instance
- `Operation()` returns `(ctx, *Op)` instead of just `*Op`
- Use `op.Register()` instead of `op.SetAttr()`
- Duration metrics are in milliseconds (not seconds)
- Success is default (register errors for failures)

## License

MIT
