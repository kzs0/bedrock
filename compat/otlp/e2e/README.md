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
Failed tests print the process logs. `Signal` sends SIGTERM through the same
bounded wait; `SignalError` returns the process result. Both shutdown paths
report a forced kill separately after eight seconds. After telemetry shutdown,
the fixture writes a structured `fixture_final_stats` record to standard error;
`FinalStats` reads the public counters after process exit.

The application uses real `HTTPMiddleware`. Routes include `/ok/{id}`, `/fail`,
`/hints`, `/panic`, `/downstream`, `/notrace`, and `/secret`. The instrumented
client calls `DownstreamURL` exactly as configured. `/notrace` disables tracing
for its nested operation and client call while preserving the middleware span.
`/secret` emits sensitive, nested, error, and safe markers in operations,
events, and structured logs.

The separate control listener provides `/ready`, `/stats`, `/workload`, and
`/shutdown`. `/operations?n=N` creates exactly N public `fixture.operation`
operations without HTTP middleware spans. N must be an integer from 0 through
10000; invalid input returns HTTP 400. Optional `payload_bytes` adds a
`fixture.payload` string attribute and accepts integers from 0 through 4096.
POST `/cpu-profile/start` acquires the standard runtime CPU profiler into a
bounded buffer, returning 204 on success and 409 when busy. POST
`/cpu-profile/stop` releases only a profiler acquired by the fixture and returns
204, including repeated stops. Shutdown also releases the fixture profiler.
These endpoints belong only to the test fixture. Stats expose only public trace export statistics, profile export
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
`ProfileTimeoutMillis`, `ProfileRetries`, and `ProfileMaxBytes` configure the
public profile exporter timeout, retries, and collection limit.
`ProfileRetries=0` means no retries; zero timeout and byte limits preserve
the fixture timeout and SDK collection limit. `ShutdownMillis` sets both the public SDK shutdown
timeout and the fixture server shutdown deadline. `MetricFamilyLimit` and
`MetricSeriesLimit` configure public metric bounds.
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

Maintain this contract matrix when scenarios change. Each row identifies the
observable proof and its remaining gaps; coverage does not imply every
transport, profile type, configuration, and failure combination was exercised.
Test names below omit the `Test` prefix.

| Scenarios | Contract and observable proof | Not yet covered here |
| --- | --- | --- |
| `HTTPTracePropagationAndShutdown`, `GRPCTracePropagationAndShutdown` | Official decoders verify resource, span IDs, incoming parent, request semantics, and shutdown delivery. HTTP also checks server/client linkage and downstream propagation. | Trace TLS trust, authentication rejection, and malformed backend replies. |
| `HTTPMetricsUseRoutesAndOutcomes`, `SamplingAndNoTracePreserveMetrics`, `UnsampledTraceContextReachesDownstream` | Route aggregation and outcome counters remain correct with zero sampling or nested tracing disabled; downstream unsampled context is preserved. | Fractional sampling distribution and metric family-limit boundaries. |
| `TraceBackendFailureAndRecovery`, `BlockedExporterDoesNotBlockRequests`, `HandlerPanicRecordsFailure` | Real HTTP failure increments loss counters, later delivery recovers, stalled export permits application requests, and panic produces an error span. | HTTP trace status-code matrix and repeated outage cycles. |
| `MetricSeriesLimitReportsDrops` | Rejected series increment the public drop counter and do not appear in Prometheus output; accepted series remain visible. | Exact family-budget admission and recovery after rejected registrations. |
| `PublicQueueSaturationAccountsForEverySpan` | A stalled receiver and 6,000 operations establish the 2,048-span bound; unique exported IDs plus reported drops account for every operation after drain. | Concurrent producer saturation and caller-configured batch bounds. |
| `SIGTERMFlushesAcceptedOperations`, `StalledHTTPExportShutdownCancellation`, `GRPCStalledExportHonorsShutdownCancellation` | SIGTERM drains accepted spans; HTTP and gRPC stalls cancel backend contexts and exit within the deadline without forced kills. HTTP checks both shutdown routes. | Stalled gRPC shutdown through SIGTERM and repeated shutdown calls. |
| `GRPCUnavailableRecoveryAndReconnect` | Official collector returns Unavailable, stops, restarts on the same port, and receives later unique batches including a payload over 64 KiB; failure counters retain prior losses. | TLS reconnect, concurrent streams, and repeated restarts. |
| `ProfileExportNativeWorkloads`, `DefaultProfilingEndpointsAbsent` | All five profile types preserve native samples and recognizable workload stacks; debug endpoints remain absent on all listeners. | Every profile type under every transport fault. |
| `ProfileTLSAuthenticationAndTrust`, `ProfileRedirectDoesNotForwardCredentials` | Trusted TLS receives authenticated native profiles; wrong trust fails before application delivery; redirects fail without forwarding exports. | Expired certificates, hostname mismatch, and certificate rotation. |
| `PrivacyDefaultsAndExplicitOptIns` | Positive safe markers and real workload samples accompany checks for absent secret markers in traces, profiles, metrics, and logs; explicit error/label opt-ins are verified. | Arbitrary user-defined policies and every supported attribute shape. |
| `ProfileTransientRetryIdentity`, `ProfileTerminalAndExhaustedRetries` | 429/503 retries preserve exact payload bytes; successful recovery and exhausted budgets have exact attempt counts and final counters; 400/401/403 receive no retries. | Retry-After behavior and network disconnects during upload. |
| `ProfileTimeoutCancelsStalledBackend`, `ProfileShutdownCancelsInflightUpload` | Backend entry and cancellation handshakes prove timeout/shutdown cancellation; application requests remain responsive and final counters report one failed collection. | Cancellation during each native collection type. |
| `ProfileCollectionByteLimit`, `ProfileBusyCPUOwnerAndRecovery`, `ProfileStopBeforeFirstTick` | A one-byte cap fails without HTTP delivery; an external CPU owner causes skips without failures and export resumes after release; stopping before the first tick exports nothing. | Exact accepted/rejected byte boundary and shutdown during CPU ownership contention. |
| `CloudExportsAndProfileSelection` | Real HTTP/2 multiplexing delivers authenticated metrics, profiles, and gRPC traces; heap-only and disabled selections are checked through worker drain. | Cloud backend failure, cancellation, and TLS matrices. |
| `FixtureControlBoundaries` | Public fixture controls reject invalid operation/payload bounds and preserve manual CPU-profiler ownership. | Fixture controls are test infrastructure, not SDK contracts. |

Shutdown cancellation checks use backend context cancellation, cancellation logs,
bounded process exit, and final stats. A trace deadline can return before the
export worker publishes its final failure counter, so those scenarios allow
zero or one failed export. Profile worker shutdown joins its worker, permitting
an exact final failed-collection assertion. Negative delivery assertions run
after process exit, or after observed CPU skips, rather than an arbitrary quiet
window. Race validation must instrument both the parent and fixture processes.
