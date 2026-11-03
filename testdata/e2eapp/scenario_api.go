package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
	bedrocklog "github.com/kzs0/bedrock/log"
	"github.com/kzs0/bedrock/metric"
	"github.com/kzs0/bedrock/metric/prometheus"
	"github.com/kzs0/bedrock/privacy"
	"github.com/kzs0/bedrock/trace"
)

func init() { fixtureScenarios["api"] = runAPIScenario }

func runAPIScenario(cfg fixtureConfig) error {
	bc := bedrock.DefaultConfig()
	bc.Service = "api-contract"
	bc.ServerEnabled = false
	bc.DisableEnvironmentDetection = true
	bc.RuntimeMetrics = false
	bc.TraceURL = cfg.TraceEndpoint
	bc.LogLevel = "debug"
	bc.LogCanonical = true
	bc.DataPolicy = &privacy.Policy{AllowErrorDetails: true, AllowSourcePaths: true}
	b, err := bedrock.New(bc, attr.String("region", "west"))
	if err != nil {
		return err
	}
	ctx := bedrock.WithBedrock(context.Background(), b)
	control := http.NewServeMux()
	control.HandleFunc("/workflow", func(w http.ResponseWriter, _ *http.Request) {
		apiWorkflow(ctx)
		w.WriteHeader(http.StatusNoContent)
	})
	control.HandleFunc("/metrics-workload", func(w http.ResponseWriter, _ *http.Request) {
		apiMetrics(ctx, b.MetricsRegistry())
		w.WriteHeader(http.StatusNoContent)
	})
	control.HandleFunc("/attributes", apiAttributes)
	control.HandleFunc("/capabilities", func(w http.ResponseWriter, r *http.Request) { apiCapabilities(w, r, ctx) })
	control.HandleFunc("/structured-logs", func(w http.ResponseWriter, _ *http.Request) { apiStructuredLogs(w, ctx) })
	control.HandleFunc("/attribute-rejections", apiAttributeRejections)
	control.HandleFunc("/bounds", apiMetricBounds)
	control.HandleFunc("/noop", func(w http.ResponseWriter, _ *http.Request) {
		apiNoop()
		w.WriteHeader(http.StatusNoContent)
	})
	control.HandleFunc("/client", func(w http.ResponseWriter, _ *http.Request) { apiClient(w, ctx, cfg.DownstreamURL) })
	streamRelease := make(chan struct{})
	var releaseOnce sync.Once
	control.HandleFunc("/stream-release", func(w http.ResponseWriter, _ *http.Request) {
		releaseOnce.Do(func() { close(streamRelease) })
		w.WriteHeader(http.StatusNoContent)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "first\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-streamRelease:
		case <-r.Context().Done():
			return
		}
		if reader, ok := w.(io.ReaderFrom); ok {
			_, _ = reader.ReadFrom(struct{ io.Reader }{strings.NewReader("second\n")})
		}
	})
	mux.HandleFunc("/hijack", func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "missing hijacker", http.StatusInternalServerError)
			return
		}
		conn, rw, hijackErr := hijacker.Hijack()
		if hijackErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nhijacked")
		_ = rw.Flush()
	})
	mux.HandleFunc("/accepted", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	mux.HandleFunc("/rejected", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	app := bedrock.HTTPMiddleware(ctx, mux,
		bedrock.WithOperationName("api.http"),
		bedrock.WithAdditionalLabels("tenant"),
		bedrock.WithAdditionalAttrs(func(r *http.Request) []attr.Attr { return []attr.Attr{attr.String("tenant", r.Header.Get("X-Tenant"))} }),
		bedrock.WithSuccessCodes(200, 202),
		bedrock.WithTracePropagation(false),
	)
	return serveScenario(ctx, b, nil, app, control, nil)
}

func apiWorkflow(ctx context.Context) {
	source, sourceCtx := bedrock.Source(ctx, "jobs", bedrock.SourceAttrs(attr.String("tenant", "source")), bedrock.SourceMetricLabels("tenant", "missing", "step_label"))
	source.Sum(sourceCtx, "processed", 2)
	source.Gauge(sourceCtx, "depth", 3)
	source.Histogram(sourceCtx, "latency", 4)
	parent, parentCtx := bedrock.Operation(sourceCtx, "parent", bedrock.Attrs(attr.String("tenant", "operation")), bedrock.Failure(errors.New("discarded initial failure")), bedrock.Success())
	parent.Register(parentCtx)
	step := bedrock.Step(parentCtx, "lookup", bedrock.Attrs(attr.String("step_label", "step")))
	step.Register(parentCtx, attr.Int("rows", 7))
	step.Register(parentCtx)
	step.Event(parentCtx, attr.NewEvent("lookup.complete", attr.Bool("cached", true)))
	step.Done()
	quiet := bedrock.Step(parentCtx, "quiet", bedrock.NoTrace(), bedrock.Attrs(attr.String("safe", "quiet-marker")))
	quiet.Register(parentCtx, attr.String("quiet", "yes"))
	quiet.Event(parentCtx, attr.NewEvent("hidden"))
	quiet.Done()
	child, childCtx := bedrock.Operation(parentCtx, "child", bedrock.Attrs(attr.String("child", "visible")))
	child.Event(childCtx, attr.NewEvent("child.event", attr.Int("items", 2)))
	child.End(bedrock.EndFailure(nil), bedrock.EndSuccess())
	child.End(bedrock.EndFailure(errors.New("ignored duplicate")))
	parent.Event(parentCtx, attr.NewEvent("parent.event"))
	parent.End(bedrock.EndFailure(errors.New("discarded final failure")), bedrock.EndSuccess())
	parent.Done()
	failed, failedCtx := bedrock.Operation(sourceCtx, "failure", bedrock.MetricLabels("reason"))
	failed.Register(failedCtx, attr.String("reason", "legacy"), attr.String("error", "legacy failure"))
	failed.End()
	rich, richCtx := bedrock.Operation(ctx, "rich.failure")
	rich.Register(richCtx, attr.ErrorWithStack(fmt.Errorf("outer \"quoted\"\nwrapped: %w", errors.New("inner\tmarker")), 0))
	rich.Done()
	for name, err := range map[string]error{"nil.failure": nil, "explicit.failure": errors.New("explicit failure")} {
		op, _ := bedrock.Operation(ctx, name, bedrock.Failure(err))
		op.Done()
	}
	quietOp, quietCtx := bedrock.Operation(ctx, "quiet.parent", bedrock.NoTrace())
	quietChild, _ := bedrock.Operation(quietCtx, "quiet.child")
	quietChild.Done()
	quietOp.Done()
	standalone := bedrock.StepFromContext(ctx, "standalone", bedrock.Attrs(attr.String("safe", "standalone")))
	standalone.Done()
	source.Done()
	bedrock.Debug(parentCtx, "api.debug", attr.String("safe", "debug-marker"))
	bedrock.Info(parentCtx, "api.info")
	bedrock.Warn(parentCtx, "api.warn")
	bedrock.Error(parentCtx, "api.error", attr.Error(errors.New("log failure")))
	bedrock.Log(parentCtx, slog.LevelInfo, "api.custom", attr.Int("count", 2))
}

func apiMetrics(ctx context.Context, registry *metric.Registry) {
	counter := bedrock.Counter(ctx, "api_counter", "Counter")
	counter.Inc()
	counter.Add(4)
	bedrock.Counter(ctx, "api_counter", "Counter").With().Add(2)
	vector := bedrock.Counter(ctx, "api_counter_vec", "Counter", "lane")
	vector.With(attr.String("lane", "fast")).Inc()
	vector.With(attr.String("lane", "fast")).Add(2)
	gauge := bedrock.Gauge(ctx, "api_gauge", "Gauge")
	gauge.Set(10)
	gauge.Inc()
	gauge.Dec()
	gauge.Add(5)
	gauge.Sub(2)
	bedrock.Gauge(ctx, "api_gauge", "Gauge").With().Add(1)
	vectorGauge := bedrock.Gauge(ctx, "api_gauge_vec", "Gauge", "lane")
	vec := vectorGauge.With(attr.String("lane", "slow"))
	vec.Set(10)
	vec.Inc()
	vec.Dec()
	vec.Add(3)
	vec.Sub(2)
	hist := bedrock.Histogram(ctx, "api_hist", "Histogram", []float64{1, 5, 10})
	hist.Observe(2)
	bedrock.Histogram(ctx, "api_hist", "Histogram", []float64{1, 5, 10}).With().Observe(4)
	bedrock.Histogram(ctx, "api_hist_vec", "Histogram", nil, "lane").With(attr.String("lane", "fast")).Observe(3)
	raw := registry.Gauge("api_raw_gauge", "Gauge")
	raw.Set(10)
	raw.Inc()
	raw.Dec()
	raw.Add(2)
	raw.Sub(3)
	registry.Counter("api_raw_counter", "Counter").Inc()
	registry.Counter("api_raw_counter", "Counter").Add(-2)
	registry.Histogram("api_raw_hist", "Histogram", []float64{1, 2}).Observe(1.5)
}

func apiNoop() {
	ctx := context.Background()
	op, opctx := bedrock.Operation(ctx, "noop.operation", bedrock.Attrs(attr.String("safe", "invisible")))
	op.Register(opctx, attr.Error(errors.New("noop error")))
	op.Event(opctx, attr.NewEvent("noop.event"))
	op.End(bedrock.EndFailure(errors.New("noop failure")))
	op.Done()
	step := bedrock.Step(opctx, "noop.step")
	step.Register(opctx, attr.String("safe", "invisible"))
	step.Event(opctx, attr.NewEvent("noop.event"))
	step.Done()
	source, srcCtx := bedrock.Source(ctx, "noop.source")
	source.Sum(srcCtx, "count", 1)
	source.Gauge(srcCtx, "depth", 1)
	source.Histogram(srcCtx, "latency", 1)
	source.Done()
	bedrock.Debug(ctx, "noop.debug")
	bedrock.Info(ctx, "noop.info")
	bedrock.Warn(ctx, "noop.warn")
	bedrock.Error(ctx, "noop.error")
	bedrock.Log(ctx, slog.LevelInfo, "noop.log")
}

func apiAttributes(w http.ResponseWriter, _ *http.Request) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 123, time.FixedZone("offset", 3600))
	attrs := []attr.Attr{attr.String("string", "hello"), attr.Int("int", -3), attr.Int64("int64", -4), attr.Uint64("uint64", 5), attr.Float64("float64", 1.25), attr.Bool("true", true), attr.Bool("false", false), attr.Duration("duration", 1500*time.Millisecond), attr.Time("time", at), attr.Any("slice", []string{"a", "b"}), attr.Error(nil), attr.ErrorWithStack(nil, 0)}
	values := map[string]any{}
	texts := map[string]string{}
	for _, a := range attrs {
		values[a.Key] = a.Value.AsAny()
		texts[a.Key] = a.Value.String()
	}
	for _, value := range []any{"typed", int(3), int64(-5), uint64(7), 2.5, true, false, time.Second, at, []int{1, 2}} {
		a := attr.Any(fmt.Sprintf("any.%T.%v", value, value), value)
		texts[a.Key] = a.String()
		values[a.Key] = a.Value.AsAny()
	}
	typed := map[string]any{
		"string": attr.StringValue("typed").AsString(), "int64": attr.Int64Value(-5).AsInt64(), "uint64": attr.Uint64Value(7).AsUint64(), "float64": attr.Float64Value(2.5).AsFloat64(), "true": attr.BoolValue(true).AsBool(), "false": attr.BoolValue(false).AsBool(), "duration": attr.DurationValue(time.Second).AsDuration(), "time": attr.TimeValue(at).AsTime(),
	}
	set := attr.NewSet(attr.String("z", "last"), attr.String("a", "old"), attr.String("a", "new"))
	merged := set.MergeSet(attr.NewSet(attr.String("b", "middle"), attr.String("a", "override")))
	merged = merged.MergeSet(attr.EmptySet).Merge()
	merged = attr.EmptySet.MergeSet(merged)
	_, missing := merged.Get("missing")
	setValues := map[string]string{}
	merged.Range(func(a attr.Attr) bool { setValues[a.Key] = a.Value.String(); return true })
	var first string
	merged.Range(func(a attr.Attr) bool { first = a.Key; return false })
	_ = json.NewEncoder(w).Encode(map[string]any{"values": values, "strings": texts, "typed": typed, "keys": merged.Keys(), "set": setValues, "has_a": merged.Has("a"), "has_missing": merged.Has("missing"), "missing_found": missing, "first": first, "renamed": attr.String("old", "value").WithKey("new").String()})
}

func apiMetricBounds(w http.ResponseWriter, _ *http.Request) {
	registry := metric.NewRegistry("bounds")
	registry.SetLimits(3, 1)
	counter := registry.Counter("counter", "Counter", "lane")
	counter.With(attr.String("lane", "accepted"), attr.String("unknown", "ignored")).Add(2)
	counter.With(attr.String("lane", "rejected")).Inc()
	gauge := registry.Gauge("gauge", "Gauge", "lane")
	gauge.With(attr.String("lane", "accepted")).Set(3)
	rejected := gauge.With(attr.String("lane", "rejected"))
	rejected.Set(10)
	rejected.Inc()
	rejected.Dec()
	rejected.Add(5)
	rejected.Sub(2)
	hist := registry.Histogram("hist", "Histogram", []float64{1, 5}, "lane")
	hist.With(attr.String("lane", "accepted")).Observe(4)
	hist.With(attr.String("lane", "rejected")).Observe(99)
	panics := map[string]string{}
	for name, call := range map[string]func(){
		"family":         func() { registry.Counter("fourth", "Full") },
		"type":           func() { registry.Gauge("counter", "Conflict", "lane") },
		"schema":         func() { registry.Counter("counter", "Conflict", "other") },
		"invalid_limits": func() { registry.SetLimits(0, 0) },
	} {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					panics[name] = fmt.Sprint(recovered)
				}
			}()
			call()
		}()
	}
	registry.Counter("counter", "Counter", "lane").With(attr.String("lane", "accepted")).Inc()
	text := &strings.Builder{}
	recorder := &apiMetricWriter{header: make(http.Header), body: text}
	prometheus.Handler(registry).ServeHTTP(recorder, &http.Request{Method: http.MethodGet, Header: make(http.Header)})
	_ = json.NewEncoder(w).Encode(map[string]any{"panics": panics, "drops": registry.DroppedSeries(), "metrics": text.String()})
}

type apiMetricWriter struct {
	header http.Header
	body   io.Writer
}

func (w *apiMetricWriter) Header() http.Header         { return w.header }
func (w *apiMetricWriter) WriteHeader(int)             {}
func (w *apiMetricWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func apiClient(w http.ResponseWriter, ctx context.Context, target string) {
	op, opctx := bedrock.Operation(ctx, "api.client")
	defer op.Done()
	results := make([]map[string]any, 0, 5)
	requests := []func() (*http.Response, error){
		func() (*http.Response, error) { return bedrock.Get(opctx, target) },
		func() (*http.Response, error) {
			return bedrock.Post(opctx, target, "text/plain", strings.NewReader("payload"))
		},
		func() (*http.Response, error) {
			return bedrock.Post(opctx, target, "", strings.NewReader("empty-type"))
		},
		func() (*http.Response, error) {
			req, _ := http.NewRequest(http.MethodPut, target, strings.NewReader("owned"))
			req.Header.Set("X-Original", "preserved")
			resp, err := bedrock.Do(opctx, req)
			if req.Header.Get("traceparent") != "" || req.Context() != context.Background() {
				return resp, errors.New("request mutated")
			}
			return resp, err
		},
		func() (*http.Response, error) {
			req, _ := http.NewRequestWithContext(opctx, http.MethodGet, target, nil)
			return bedrock.NewClient(nil).Do(req)
		},
		func() (*http.Response, error) { return bedrock.NewClient(nil).Get(target) },
	}
	for _, call := range requests {
		resp, err := call()
		if err != nil {
			results = append(results, map[string]any{"error": err.Error()})
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		results = append(results, map[string]any{"status": resp.StatusCode, "body": string(body), "read_error": readErr != nil})
	}
	_, getErr := bedrock.Get(opctx, ":bad")
	_, postErr := bedrock.Post(opctx, ":bad", "", nil)
	_ = json.NewEncoder(w).Encode(map[string]any{"results": results, "invalid_get": getErr != nil, "invalid_post": postErr != nil})
}

type apiCapabilitySink struct {
	header                                        http.Header
	body                                          bytes.Buffer
	status                                        int
	flushes, flushErrors, hijacks, pushes, copies int
	pushTarget, pushHeader                        string
}

func (s *apiCapabilitySink) Header() http.Header { return s.header }
func (s *apiCapabilitySink) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
}
func (s *apiCapabilitySink) Write(p []byte) (int, error) { s.WriteHeader(200); return s.body.Write(p) }

type apiFlushCapability struct{ sink *apiCapabilitySink }

func (f *apiFlushCapability) Flush() { f.sink.flushes++; f.sink.WriteHeader(200) }

type apiHijackCapability struct{ sink *apiCapabilitySink }

func (h *apiHijackCapability) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.sink.hijacks++
	return nil, nil, errors.New("adapter hijack marker")
}

type apiPushCapability struct{ sink *apiCapabilitySink }

func (p *apiPushCapability) Push(target string, options *http.PushOptions) error {
	p.sink.pushes++
	p.sink.pushTarget = target
	p.sink.pushHeader = options.Header.Get("X-Push")
	return errors.New("adapter push marker")
}

type apiCopyCapability struct{ sink *apiCapabilitySink }

func (c *apiCopyCapability) ReadFrom(r io.Reader) (int64, error) {
	c.sink.copies++
	return io.Copy(&c.sink.body, r)
}

func apiBackendWriter(s *apiCapabilitySink, mask int) http.ResponseWriter {
	f, h, p, c := &apiFlushCapability{s}, &apiHijackCapability{s}, &apiPushCapability{s}, &apiCopyCapability{s}
	writers := []http.ResponseWriter{
		s,
		struct {
			http.ResponseWriter
			http.Flusher
		}{s, f},
		struct {
			http.ResponseWriter
			http.Hijacker
		}{s, h},
		struct {
			http.ResponseWriter
			http.Flusher
			http.Hijacker
		}{s, f, h},
		struct {
			http.ResponseWriter
			http.Pusher
		}{s, p},
		struct {
			http.ResponseWriter
			http.Flusher
			http.Pusher
		}{s, f, p},
		struct {
			http.ResponseWriter
			http.Hijacker
			http.Pusher
		}{s, h, p},
		struct {
			http.ResponseWriter
			http.Flusher
			http.Hijacker
			http.Pusher
		}{s, f, h, p},
		struct {
			http.ResponseWriter
			io.ReaderFrom
		}{s, c},
		struct {
			http.ResponseWriter
			http.Flusher
			io.ReaderFrom
		}{s, f, c},
		struct {
			http.ResponseWriter
			http.Hijacker
			io.ReaderFrom
		}{s, h, c},
		struct {
			http.ResponseWriter
			http.Flusher
			http.Hijacker
			io.ReaderFrom
		}{s, f, h, c},
		struct {
			http.ResponseWriter
			http.Pusher
			io.ReaderFrom
		}{s, p, c},
		struct {
			http.ResponseWriter
			http.Flusher
			http.Pusher
			io.ReaderFrom
		}{s, f, p, c},
		struct {
			http.ResponseWriter
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{s, h, p, c},
		struct {
			http.ResponseWriter
			http.Flusher
			http.Hijacker
			http.Pusher
			io.ReaderFrom
		}{s, f, h, p, c},
	}
	return writers[mask]
}

type apiUnwrapCapability struct {
	http.ResponseWriter
	next http.ResponseWriter
}

func (u *apiUnwrapCapability) Unwrap() http.ResponseWriter { return u.next }

type apiErrorFlushCapability struct {
	http.ResponseWriter
	sink *apiCapabilitySink
	fail bool
}

func (f *apiErrorFlushCapability) Unwrap() http.ResponseWriter { return f.ResponseWriter }
func (f *apiErrorFlushCapability) FlushError() error {
	f.sink.flushErrors++
	if f.fail {
		return errors.New("adapter flush marker")
	}
	f.sink.WriteHeader(200)
	return nil
}

type apiLegacyFlushCapability struct {
	http.ResponseWriter
	sink *apiCapabilitySink
}

func (f *apiLegacyFlushCapability) Unwrap() http.ResponseWriter { return f.ResponseWriter }
func (f *apiLegacyFlushCapability) Flush()                      { f.sink.flushes++; f.sink.WriteHeader(200) }

func apiCapabilities(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	mask, err := strconv.Atoi(r.URL.Query().Get("mask"))
	if err != nil || mask < 0 || mask > 15 {
		http.Error(w, "invalid capability mask", 400)
		return
	}
	sink := &apiCapabilitySink{header: make(http.Header)}
	backend := apiBackendWriter(sink, mask)
	mode := r.URL.Query().Get("mode")
	switch mode {
	case "wrapped":
		backend = &apiUnwrapCapability{backend, backend}
	case "nil":
		backend = &apiUnwrapCapability{sink, nil}
	case "error", "failed", "legacy":
		backend = &apiErrorFlushCapability{backend, sink, mode == "failed"}
		if mode == "legacy" {
			backend = &apiLegacyFlushCapability{backend, sink}
		}
	}
	advertised := 0
	flushError, hijackError, pushError := "", "", ""
	unsupported := false
	bedrock.HTTPMiddleware(ctx, http.HandlerFunc(func(inner http.ResponseWriter, _ *http.Request) {
		_, flush := inner.(http.Flusher)
		_, hijack := inner.(http.Hijacker)
		_, push := inner.(http.Pusher)
		_, copyReader := inner.(io.ReaderFrom)
		if flush {
			advertised |= 1
		}
		if hijack {
			advertised |= 2
		}
		if push {
			advertised |= 4
		}
		if copyReader {
			advertised |= 8
		}
		unsupported = errors.Is(http.NewResponseController(inner).SetWriteDeadline(time.Time{}), http.ErrNotSupported)
		if flush {
			if mode == "error" || mode == "failed" || mode == "legacy" {
				if err := inner.(interface{ FlushError() error }).FlushError(); err != nil {
					flushError = err.Error()
					inner.WriteHeader(503)
				}
			} else {
				inner.(http.Flusher).Flush()
			}
		}
		if hijack {
			_, _, err := inner.(http.Hijacker).Hijack()
			if err != nil {
				hijackError = err.Error()
			}
		}
		if push {
			if err := inner.(http.Pusher).Push("/asset", &http.PushOptions{Header: http.Header{"X-Push": []string{"forwarded"}}}); err != nil {
				pushError = err.Error()
			}
		}
		if copyReader {
			_, _ = inner.(io.ReaderFrom).ReadFrom(strings.NewReader("adapter-body"))
		} else {
			_, _ = inner.Write([]byte("adapter-body"))
		}
		inner.WriteHeader(201)
	}), bedrock.WithOperationName("api.adapter")).ServeHTTP(backend, r)
	_ = json.NewEncoder(w).Encode(map[string]any{"mask": advertised, "status": sink.status, "body": sink.body.String(), "flushes": sink.flushes, "flush_errors": sink.flushErrors, "hijacks": sink.hijacks, "pushes": sink.pushes, "copies": sink.copies, "push_target": sink.pushTarget, "push_header": sink.pushHeader, "flush_error": flushError, "hijack_error": hijackError, "push_error": pushError, "deadline_unsupported": unsupported})
}

func apiStructuredLogs(w http.ResponseWriter, ctx context.Context) {
	op, opctx := bedrock.Operation(ctx, "api.structured.logs")
	defer op.Done()
	var output, text bytes.Buffer
	policy := &privacy.Policy{Redact: func(key string, value any) (any, bool) { return value, key != "transaction.omit" }}
	handler := bedrocklog.NewHandler(&bedrocklog.HandlerOptions{Output: &output, Level: slog.LevelWarn, AddSource: true, Policy: policy})
	handler.SetTraceContextFunc(func(ctx context.Context) (string, string) {
		sc := trace.SpanContextFromContext(ctx)
		if !sc.IsValid() {
			return "", ""
		}
		return sc.TraceID.String(), sc.SpanID.String()
	})
	logger := slog.New(handler).With("app", "adapter").WithGroup("transaction").With("tenant", "visible")
	logger.InfoContext(opctx, "suppressed")
	logger.WarnContext(opctx, "structured", slog.String("password", "private"), slog.String("safe", "visible"), slog.String("omit", "excluded"))
	logger.Warn("context-free", slog.Int("count", 2))
	typed := []attr.Attr{attr.Int64("int64", -7), attr.Uint64("uint64", 8), attr.Float64("float64", 2.5), attr.Bool("bool", true), attr.Duration("duration", time.Second), attr.Time("time", time.Unix(0, 0)), attr.Any("slice", []int{1, 2})}
	slog.New(bedrocklog.NewHandler(&bedrocklog.HandlerOptions{Output: &text, Format: "TEXT", Policy: &privacy.Policy{AllowSourcePaths: true}, AddSource: true})).LogAttrs(ctx, slog.LevelInfo, "typed", bedrocklog.AttrsToSlog(typed)...)
	var omitted bytes.Buffer
	slog.New(bedrocklog.NewHandler(&bedrocklog.HandlerOptions{Output: &omitted, Policy: &privacy.Policy{Redact: func(key string, value any) (any, bool) { return value, key != slog.MessageKey }}})).Info("message-to-omit", "safe", "retained")
	_ = bedrocklog.NewHandler(nil).WithGroup("").Handle(ctx, slog.NewRecord(time.Now(), slog.LevelInfo, "api.default-handler", 0))
	_ = json.NewEncoder(w).Encode(map[string]any{"json": output.String(), "text": text.String(), "omitted": omitted.String()})
}

func apiAttributeRejections(w http.ResponseWriter, _ *http.Request) {
	panics := map[string]string{}
	for key, call := range map[string]func(){
		"string":   func() { attr.Int64Value(1).AsString() },
		"int64":    func() { attr.StringValue("bad").AsInt64() },
		"uint64":   func() { attr.StringValue("bad").AsUint64() },
		"float64":  func() { attr.StringValue("bad").AsFloat64() },
		"bool":     func() { attr.StringValue("bad").AsBool() },
		"duration": func() { attr.StringValue("bad").AsDuration() },
		"time":     func() { attr.StringValue("bad").AsTime() },
	} {
		func() {
			defer func() {
				if v := recover(); v != nil {
					panics[key] = fmt.Sprint(v)
				}
			}()
			call()
		}()
	}
	_ = json.NewEncoder(w).Encode(panics)
}
