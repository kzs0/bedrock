package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

func init() { fixtureScenarios["trace-api"] = runTraceAPI }

func runTraceAPI(cfg fixtureConfig) error {
	bc := bedrock.DefaultConfig()
	bc.Service = "trace-api"
	bc.ServerEnabled = false
	bc.DisableEnvironmentDetection = true
	bc.TraceURL = cfg.TraceEndpoint
	b, err := bedrock.New(bc, attr.String("safe.resource", "retained"), attr.String("host.name", "private-host"))
	if err != nil {
		return err
	}
	ctx := bedrock.WithBedrock(context.Background(), b)
	mux := http.NewServeMux()
	mux.HandleFunc("/lifecycle", func(w http.ResponseWriter, _ *http.Request) {
		tracer := b.Tracer()
		parentCtx, parent := tracer.Start(ctx, "direct.parent", trace.WithSpanKind(trace.SpanKindProducer), trace.WithAttrs(attr.String("initial", "replace")))
		recording := parent.IsRecording()
		liveDuration := parent.Duration()
		parent.SetAttrSet(attr.NewSet(attr.String("safe", "retained"), attr.String("password", "private-password")))
		parent.SetAttr(attr.Int("answer", 42))
		parent.AddEvent("direct.event", attr.Bool("checked", true))
		parent.RecordError(nil)
		parent.RecordError(errors.New("private-error"), attr.String("safe", "exception-marker"))
		_, child := tracer.Start(parentCtx, "direct.child", trace.WithParent(parent), trace.WithSpanKind(trace.SpanKindConsumer))
		child.SetStatus(trace.StatusOK, "")
		child.End()
		parent.End()
		ended := parent.EndTime()
		duration := parent.Duration()
		parent.SetAttr(attr.String("late", "forbidden"))
		parent.SetAttrSet(attr.NewSet(attr.String("late", "forbidden")))
		parent.AddEvent("late.event")
		parent.RecordError(errors.New("late.error"))
		parent.SetStatus(trace.StatusOK, "late.status")
		parent.End()
		remote := trace.NewRemoteSpanContext(parent.TraceID(), parent.SpanID(), "vendor=value", true)
		_, remoteChild := tracer.Start(parentCtx, "direct.remote", trace.WithRemoteParent(remote), trace.WithSpanKind(trace.SpanKindClient))
		remoteContext := trace.SpanContextFromContext(trace.ContextWithSpan(ctx, remoteChild))
		remoteChild.End()
		status, message := parent.Status()
		_, host := tracer.Resource().Get("host.name")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service": tracer.ServiceName(), "host_exposed": host, "recording_before": recording,
			"recording_after": parent.IsRecording(), "positive_duration": duration > 0 && liveDuration >= 0,
			"stable_end": ended.Equal(parent.EndTime()) && duration == parent.Duration(),
			"events":     len(parent.Events()), "status": status, "message": message,
			"remote_valid": remote.IsValid(), "remote_sampled": remoteContext.Sampled,
			"remote_state": remoteContext.Tracestate, "local_context": !remoteContext.IsRemote,
			"empty_context_valid": trace.SpanContextFromContext(context.Background()).IsValid(),
		})
	})
	mux.HandleFunc("/sampling", func(w http.ResponseWriter, _ *http.Request) {
		seedCtx, seed := b.Tracer().Start(ctx, "direct.seed")
		seed.End()
		id := seed.TraceID()
		results := map[string]int{}
		for name, sampler := range map[string]trace.Sampler{
			"always": trace.AlwaysSampler{}, "never": trace.NeverSampler{},
			"negative": trace.NewRatioSampler(-1), "over_one": trace.NewRatioSampler(2),
			"parent_missing": trace.NewParentBasedSampler(nil),
			"parent_root":    trace.NewParentBasedSampler(trace.AlwaysSampler{}),
		} {
			results[name] = int(sampler.ShouldSample(id, name, false).Decision)
		}
		results["sampled_parent"] = int(trace.NewParentBasedSampler(nil).ShouldSample(id, "parent", true).Decision)
		never := trace.NewTracer(trace.TracerConfig{Sampler: trace.NeverSampler{}})
		_, dropped := never.Start(seedCtx, "direct.dropped")
		dropped.SetAttr(attr.String("late", "ignored"))
		dropped.AddEvent("ignored")
		dropped.End()
		_, droppedFast := never.StartSpan(ctx, "direct.fast.dropped", nil, &trace.SpanContext{}, nil)
		droppedFast.SetAttrSet(attr.NewSet(attr.String("late", "ignored")))
		droppedFast.RecordError(errors.New("ignored"))
		droppedFast.SetStatus(trace.StatusError, "ignored")
		droppedFast.End()
		nilExporter := trace.NewTracer(trace.TracerConfig{})
		_, local := nilExporter.Start(ctx, "local.only")
		local.End()
		shutdownErr := nilExporter.Shutdown(ctx)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"decisions": results, "dropped_recording": dropped.IsRecording() || droppedFast.IsRecording(),
			"dropped_attributes":    dropped.Attrs().Len() + droppedFast.Attrs().Len(),
			"dropped_events":        len(dropped.Events()) + len(droppedFast.Events()),
			"dropped_context_valid": trace.SpanContextFromContext(trace.ContextWithSpan(ctx, dropped)).IsValid(),
			"nil_exporter_shutdown": shutdownErr == nil,
		})
	})
	mux.HandleFunc("/kinds", func(w http.ResponseWriter, _ *http.Request) {
		for _, kind := range []trace.SpanKind{trace.SpanKindInternal, trace.SpanKindServer, trace.SpanKindClient, trace.SpanKindProducer, trace.SpanKindConsumer, 99} {
			_, span := b.Tracer().Start(ctx, "direct.kind", trace.WithSpanKind(kind))
			span.SetAttr(attr.Int("kind", int(kind)))
			span.End()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return serveScenario(ctx, b, nil, mux, nil, nil)
}
