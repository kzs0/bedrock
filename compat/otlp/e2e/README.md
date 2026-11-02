# Functional exporter tests

Run from `compat/otlp` with `go test ./e2e`. Tests build the root module's
`testdata/e2eapp` fixture using the test compiler, then launch isolated processes
on ephemeral loopback ports. The fixture imports only standard packages and
public Bedrock packages. Official OTLP decoders remain in this compatibility
module.

The harness removes inherited `BEDROCK_*` and `OTEL_*` variables. It sends one
JSON configuration in `BEDROCK_E2E_CONFIG`; the fixture disables environment
detection. Standard output contains one readiness record:

```json
{"app_url":"http://127.0.0.1:12345","control_url":"http://127.0.0.1:12346","metrics_url":"http://127.0.0.1:12347"}
```

Telemetry and HTTP server logs go to captured standard error. `fixtureApp.Stop`
requests graceful shutdown, waits for telemetry flush, and enforces a deadline.
Failed tests print the process logs.

The application uses real `HTTPMiddleware`. Routes include `/ok/{id}`, `/fail`,
`/hints`, `/panic`, `/downstream`, `/notrace`, and `/secret`. The instrumented
client calls `DownstreamURL` exactly as configured. `/notrace` disables tracing
for its nested operation and client call while preserving the middleware span.
`/secret` emits sensitive, nested, error, and safe markers in operations,
events, and structured logs.

The separate control listener provides `/ready`, `/stats`, `/workload`, and
`/shutdown`. Stats expose only public trace export statistics, profile export
statistics, and metric dropped-series counts under `trace`, `profiles`, and
`metrics`. `/workload` starts retained heap, recognizable goroutines, CPU work,
and mutex/block contention. `ContentionSampling` explicitly enables contention
samplers. Profile labels include safe and sensitive markers. No debugging HTTP
handlers are imported. Unmatched application requests reach the default mux,
so debugging endpoint probes also check global registration. The metrics listener serves `/metrics` through the public
Prometheus handler.

`fixtureConfig` mirrors the JSON protocol. Profiling stays disabled unless
`ProfileEndpoint` is set; `ProfileTypes` selects the exact types, and an empty
selection exports nothing. Plain HTTP is explicitly allowed for local profile
receivers. CPU windows and intervals are configurable in milliseconds.
`CloudEndpoint` selects the public `Init`/`WithCloud` path, with explicit
`CloudAPIKey`, `CloudProfileTypes`, and `CloudPushIntervalMillis`. Cloud cleanup
flushes workers before returning; local-only fixtures use `New` and `Shutdown`.

`newReceiver` captures bounded request bodies, headers, and paths through
`Requests`. `SetHandler` scripts backend responses and receives the restored
body. The capture queue holds 256 requests and reports dropped captures during
cleanup rather than blocking exporters. Scenario files decode and assert the
wire payloads using official protobuf types.

Set `E2E_COVER_DIR` to preserve instrumented subprocess coverage. Each process
writes into a separate `fixture-*` directory. Merge those directories using
`go tool covdata`; exclude the fixture package from library reports. Its main
package is instrumented to initialize Go's exit coverage hook. `make
e2e-coverage` filters the exported profile to root library packages. Its
percentage measures only library code linked into the fixture, excluding
fixture main and examples. Optional packages such as `profile/handler.go` are
not linked and appear as uninstrumented in the inventory; root unit coverage
instruments all core packages. Normal runs use
temporary directories. Compiled fixture bytes
are cached per coverage and race mode within each test process, while each launch owns
its executable and process lifecycle.

Set `E2E_RACE=1` to build the subprocess with `-race`; parent test race flags
alone do not instrument subprocesses. The harness verifies the executable's
build settings. Combining coverage and race uses atomic coverage counters.

Extend scenarios through public routes and configuration fields. Keep the
fixture's configuration declaration and `fixtureConfig` in the harness aligned;
avoid internal-state hooks and third-party fixture imports.
