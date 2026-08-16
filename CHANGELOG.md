# Changelog

Notable changes to Bedrock are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases use
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Because Bedrock is
pre-1.0, minor releases may include intentional API or behavior changes.

## Unreleased

### Added

- Added `Op.End` with explicit success and failure outcome overrides.
- Added registry-level configurable histogram buckets.
- Added automated verification and GitHub Release publication for semantic
  version tags on `main`.
- Added repository contribution, security disclosure, compatibility, and
  release guidance.

### Changed

- Operation completion is idempotent: the first `Done` or `End` call wins.
- Metric registries and Bedrock instances defensively copy histogram bucket
  configuration so later caller mutations do not change live behavior.
- The observability server now binds to `127.0.0.1:9090` and leaves pprof
  disabled by default. Wildcard/public binding and profiling are explicit
  opt-ins.
- `Init` now fails fast when implicit `BEDROCK_*` environment configuration is
  malformed instead of silently replacing it with defaults.
- Environment decoding now parses integers and floating-point values at the
  destination field's bit width. Empty variable names, unknown options, and
  trailing empty options in explicit `env` tags are errors, including tags on
  nested structs; `env:"-"` skips an entire nested subtree.
- W3C Trace Context parsing now rejects uppercase version and flag fields,
  extension fields on version `00`, duplicate `tracestate` keys, invalid key
  grammar, and `tracestate` values beyond Bedrock's 512-byte acceptance limit.
- Prometheus exposition validates metric names, label names, metric types,
  UTF-8 data, histogram structure, and cross-family sample-name uniqueness
  before encoding, and applies Prometheus label-value escaping.
- Telemetry shutdown is context-aware and concurrency-safe. Batch export drains
  before the owned exporter closes, periodic pushes are bounded and cancellable,
  and metrics perform one final shutdown push.
- The dependency-free OTLP gRPC transport now honors HTTP/2 peer settings,
  frame fragmentation, flow control, cancellation, GOAWAY/RST_STREAM, and
  reconnect behavior, and supports both TLS with ALPN and cleartext h2c.

### Fixed

- Counters preserve fractional increments during atomic concurrent updates.
- Counters ignore negative and NaN updates without corrupting their value.
- Operation outcome options now consistently affect spans, canonical logs, and
  success or failure metrics.
- HTTP middleware preserves the optional `ResponseWriter` capabilities actually
  supported by the wrapped writer, including flushing, hijacking, pushing,
  `io.ReaderFrom`, and `http.ResponseController` unwrapping.
- Prometheus exposition no longer reorders the caller's metric-family slice
  while producing deterministic output.
- Concurrent or repeated shutdown calls now share one drain/finalization result;
  a timed-out caller does not prevent a later caller from completing shutdown.
- OTLP batch-export failures are retained and returned during shutdown rather
  than being silently discarded, without double-shutting down the exporter.
- OTLP gRPC validates request metadata and response HTTP/gRPC status,
  content type, headers, trailers, HPACK/Huffman encodings, and protocol frame
  structure instead of accepting malformed responses.

### Compatibility notes

- Code that called operation completion more than once now records only the
  first completion.
- Counter output can now contain fractional values that older behavior lost.
- Callers that mutate histogram bucket slices after construction must instead
  create a new registry or Bedrock instance for the new configuration.
- Environment values that previously overflowed a narrow numeric field, used a
  misspelled tag option, or declared an empty tag name/option now fail decoding
  instead of being truncated or silently ignored.
- Deployments that scrape the observability server from another host or
  container must explicitly set `BEDROCK_SERVER_ADDR` to a reachable address;
  pprof requires `BEDROCK_SERVER_PPROF=true`.
- Applications relying on malformed implicit environment configuration being
  ignored must validate or correct it before calling `Init`, which now panics on
  that configuration error. Explicit `WithConfig` behavior is unchanged.
- Invalid W3C Trace Context fields and invalid Prometheus identifiers that may
  previously have been accepted now produce parse or encoding errors. The
  Prometheus encoder still sorts its output, but preserves caller-owned slice
  order; families whose names collide with histogram-generated `_bucket`,
  `_sum`, or `_count` samples are rejected before output is written. Histogram
  buckets must have finite, strictly increasing bounds and monotonic cumulative
  counts that do not exceed the total count.
- Shutdown callers should pass a context with an appropriate deadline and may
  retry waiting with a fresh context after a timeout; the underlying shutdown
  lifecycle continues exactly once.
- OTLP gRPC calls now reject invalid metadata, non-gRPC successful responses,
  missing or malformed `grpc-status`, malformed HPACK/Huffman data, and invalid
  HTTP/2 frame sequences. Integrations that depended on permissive parsing must
  send standards-compliant gRPC responses.

## v0.4.0 - 2026-03-02

- Expanded automated test and coverage reporting across the repository.
- Added managed cloud telemetry export, environment detection, and telemetry
  push support.
- Reduced allocations in instrumentation hot paths.

## Earlier releases

Tags `v0.1.0` through `v0.3.2` predate this changelog. Their history is
available in the repository's [tag list](https://github.com/kzs0/bedrock/tags).
