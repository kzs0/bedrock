# Bedrock - Development Guide

## Project Overview

Bedrock is an opinionated observability library for Go that provides tracing, metrics, profiling, and structured logging with automatic instrumentation. Everything flows through `context.Context` - no globals.

## Architecture

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
├── metric/          # Metrics: Registry, Counter, Gauge, Histogram, RuntimeCollector
├── log/             # Logging: Bridge (attr-based), Handler (slog integration)
├── server/          # Observability server: /metrics, /health, /debug/pprof
├── transport/       # HTTP transport with tracing
├── env/             # Environment variable parsing
└── example/         # Examples including gRPC propagator
```

## Key Concepts

### Operations
Operations are the primary unit of work. They automatically record:
- `<name>_count` - Total count
- `<name>_successes` - Successful completions
- `<name>_failures` - Failed completions
- `<name>_duration_ms` - Duration histogram

### Steps
Lightweight spans within operations for tracing helper functions. No separate metrics - contribute to parent operation.

### Sources
Long-running processes that spawn operations (e.g., background workers). Prefix child operation names and share attributes.

### NoTrace Mode
Use `NoTrace()` option to disable tracing for hot paths. Metrics still recorded. Inherits through context to children.

## Common Patterns

### Adding a new option
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

### Context keys
All context values use unexported key types in `context.go`:
- `bedrockKey` - stores `*Bedrock`
- `operationKey` - stores `*operationState`
- `sourceKey` - stores `*sourceConfig`
- `noTraceKey` - stores `bool` for NoTrace inheritance

## Testing

```bash
go test ./...
```

Key test files:
- `api_test.go` - Integration tests for public API
- `bedrock_test.go` - Core functionality tests
- `middleware_test.go` - HTTP middleware tests
- `client_test.go` - HTTP client tests

## Build Commands

```bash
# Run tests
go test ./...

# Run tests with race detection
go test -race ./...

# Run example
go run example/main.go
```

## Important Implementation Details

1. **Noop pattern**: `bedrockFromContext()` returns a singleton noop instance if bedrock isn't in context, avoiding nil checks everywhere.

2. **Metric labels**: Use `MetricLabels()` to pre-register label names. Missing labels default to `"_"` to maintain consistent cardinality.

3. **Success by default**: Operations are successful unless `attr.Error()` is registered.

4. **W3C Trace Context**: Middleware extracts `traceparent`/`tracestate` headers. Client injects them. See `trace/w3c/` for parsing.

5. **Runtime metrics**: When `RuntimeMetrics` is enabled (default), Go runtime metrics are collected via `metric.RuntimeCollector`.

## Package Dependencies

Internal packages should not import from parent:
- `attr` - no dependencies on other bedrock packages
- `trace` - depends on `attr`
- `metric` - depends on `attr`
- `log` - depends on `attr`, `trace`
- `server` - depends on `metric`
- `transport` - depends on `attr`, `trace`
- `env` - no dependencies on other bedrock packages

The main `bedrock` package imports all internal packages.
