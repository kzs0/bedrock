package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
	tracehttp "github.com/kzs0/bedrock/trace/http"
	"github.com/kzs0/bedrock/trace/otlp"
	"github.com/kzs0/bedrock/trace/w3c"
)

func init() { fixtureScenarios["transport"] = runTransportScenario }

type transportOptions struct {
	Endpoint string
	Protocol string
	Insecure bool
	Headers  map[string]string
	Timeout  int
}

func runTransportScenario(cfg fixtureConfig) error {
	var options transportOptions
	if err := scenarioOptions(cfg, &options); err != nil {
		return err
	}
	bc := bedrock.DefaultConfig()
	bc.DisableEnvironmentDetection, bc.ServerEnabled = true, false
	b, err := bedrock.New(bc)
	if err != nil {
		return err
	}
	ctx := bedrock.WithBedrock(context.Background(), b)
	timeout := time.Duration(options.Timeout) * time.Millisecond
	var exporter trace.Exporter
	if options.Protocol == "grpc" {
		exporter = otlp.NewGRPCExporter(otlp.GRPCExporterConfig{Endpoint: options.Endpoint, Insecure: options.Insecure, Headers: options.Headers, Timeout: timeout, ServiceName: "transport-contract"})
	} else {
		exporter = otlp.NewExporter(otlp.ExporterConfig{Endpoint: options.Endpoint, Headers: options.Headers, Timeout: timeout, ServiceName: "transport-contract"})
	}
	defer func() { _ = exporter.Shutdown(context.Background()) }()
	mux := http.NewServeMux()
	mux.HandleFunc("/export", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 || n > 2048 {
			n = 1
		}
		payload, _ := strconv.Atoi(r.URL.Query().Get("payload"))
		payload = max(0, min(4096, payload))
		tracer := trace.NewTracer(trace.TracerConfig{})
		spans := make([]*trace.Span, n)
		for i := range spans {
			_, span := tracer.Start(context.Background(), "transport.export", trace.WithSpanKind(trace.SpanKindProducer))
			span.SetAttr(attr.Int("fixture.index", i), attr.String("fixture.payload", strings.Repeat("x", payload)))
			if r.URL.Query().Get("typed") == "true" {
				span.SetAttr(attr.Uint64("typed.uint.zero", 0), attr.Bool("typed.bool.zero", false), attr.String("typed.string.zero", ""), attr.Float64("typed.double.zero", 0), attr.Duration("typed.duration.zero", 0), attr.Int64("typed.int.max", math.MaxInt64), attr.Uint64("typed.uint.max", math.MaxUint64))
			}
			span.AddEvent("fixture.event", attr.Bool("fixture.event.seen", true))
			span.SetStatus(trace.StatusOK, "")
			span.End()
			spans[i] = span
		}
		callCtx := r.Context()
		if r.URL.Query().Get("context") == "canceled" {
			canceled, cancel := context.WithCancel(callCtx)
			cancel()
			callCtx = canceled
		}
		err := exporter.ExportSpans(callCtx, spans)
		message := ""
		if err != nil {
			message = err.Error()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "count": n})
	})
	mux.HandleFunc("/context", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Traceparent string
			Tracestate  []string
			Format      []w3c.Entry
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		headers := make(http.Header)
		headers.Set("traceparent", input.Traceparent)
		for _, state := range input.Tracestate {
			headers.Add("tracestate", state)
		}
		remote, extractErr := (&tracehttp.Propagator{}).Extract(headers)
		result := map[string]any{"valid": remote.IsValid(), "sampled": remote.Sampled, "tracestate": remote.Tracestate, "formatted": w3c.FormatTracestate(input.Format)}
		if extractErr != nil {
			result["error"] = extractErr.Error()
		} else {
			result["traceparent"] = w3c.FormatTraceparent(remote.TraceID, remote.SpanID, remote.Sampled)
		}
		_ = json.NewEncoder(w).Encode(result)
	})
	mux.HandleFunc("/sampling", func(w http.ResponseWriter, r *http.Request) {
		rate, _ := strconv.ParseFloat(r.URL.Query().Get("rate"), 64)
		tracer := trace.NewTracer(trace.TracerConfig{Sampler: trace.NewRatioSampler(rate)})
		const n = 10000
		accepted := 0
		for i := 0; i < n; i++ {
			_, span := tracer.Start(context.Background(), fmt.Sprint("sample.", i))
			if span.IsRecording() {
				accepted++
			}
			span.End()
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted": accepted, "total": n})
	})
	return serveScenario(ctx, b, nil, mux, nil, nil)
}
