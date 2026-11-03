package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func traceAPIResult(t *testing.T, app *fixtureApp, path string, result any) {
	t.Helper()
	response, err := (&http.Client{Timeout: 3 * time.Second}).Get(app.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("trace API status %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(result); err != nil {
		t.Fatal(err)
	}
}

func TestTraceAPILifecycleAndRemoteParent(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{Scenario: "trace-api", TraceEndpoint: receiver.URL + "/v1/traces"})
	var result struct {
		Service           string `json:"service"`
		HostExposed       bool   `json:"host_exposed"`
		RecordingBefore   bool   `json:"recording_before"`
		RecordingAfter    bool   `json:"recording_after"`
		PositiveDuration  bool   `json:"positive_duration"`
		StableEnd         bool   `json:"stable_end"`
		Events            int    `json:"events"`
		Status            int    `json:"status"`
		Message           string `json:"message"`
		RemoteValid       bool   `json:"remote_valid"`
		RemoteSampled     bool   `json:"remote_sampled"`
		RemoteState       string `json:"remote_state"`
		LocalContext      bool   `json:"local_context"`
		EmptyContextValid bool   `json:"empty_context_valid"`
	}
	traceAPIResult(t, app, "/lifecycle", &result)
	if result.Service != "trace-api" || result.HostExposed || !result.RecordingBefore || result.RecordingAfter || !result.PositiveDuration || !result.StableEnd || result.Events != 2 || result.Status != 2 || result.Message != "[redacted]" || !result.RemoteValid || !result.RemoteSampled || result.RemoteState != "vendor=value" || !result.LocalContext || result.EmptyContextValid {
		t.Fatalf("trace lifecycle: %+v", result)
	}
	app.Stop(t)
	spans := behaviorSpans(t, nextBehaviorExport(t, receiver.Requests))
	if len(spans) != 3 {
		t.Fatalf("completed spans %d, want 3", len(spans))
	}
	byName := make(map[string]*tracepb.Span)
	for _, span := range spans {
		byName[span.Name] = span
		if behaviorStringAttr(span, "late") != "" || (behaviorStringAttr(span, "password") != "" && behaviorStringAttr(span, "password") != "[redacted]") {
			t.Fatalf("forbidden attribute: %v", span)
		}
	}
	parent, child, remote := byName["direct.parent"], byName["direct.child"], byName["direct.remote"]
	if parent == nil || child == nil || remote == nil {
		t.Fatalf("missing lifecycle spans: %v", byName)
	}
	if string(child.ParentSpanId) != string(parent.SpanId) || string(remote.ParentSpanId) != string(parent.SpanId) || string(parent.TraceId) != string(child.TraceId) || string(remote.TraceId) != string(parent.TraceId) || parent.Kind != tracepb.Span_SPAN_KIND_PRODUCER || child.Kind != tracepb.Span_SPAN_KIND_CONSUMER || remote.Kind != tracepb.Span_SPAN_KIND_CLIENT || parent.Status.Code != tracepb.Status_STATUS_CODE_ERROR || len(parent.Events) != 2 || behaviorStringAttr(parent, "initial") != "" || behaviorStringAttr(parent, "safe") != "retained" {
		t.Fatalf("lifecycle wire mismatch: %v", spans)
	}
	select {
	case extra := <-receiver.Requests:
		t.Fatalf("duplicate completion exported: %v", extra)
	default:
	}
}

func TestTraceAPISamplingAndKinds(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{Scenario: "trace-api", TraceEndpoint: receiver.URL + "/v1/traces"})
	var result struct {
		Decisions           map[string]int `json:"decisions"`
		DroppedRecording    bool           `json:"dropped_recording"`
		DroppedAttributes   int            `json:"dropped_attributes"`
		DroppedEvents       int            `json:"dropped_events"`
		DroppedContextValid bool           `json:"dropped_context_valid"`
		NilExporterShutdown bool           `json:"nil_exporter_shutdown"`
	}
	traceAPIResult(t, app, "/sampling", &result)
	want := map[string]int{"always": 2, "never": 0, "negative": 0, "over_one": 2, "parent_missing": 0, "parent_root": 2, "sampled_parent": 2}
	if len(result.Decisions) != len(want) || result.DroppedRecording || result.DroppedAttributes != 0 || result.DroppedEvents != 0 || !result.DroppedContextValid || !result.NilExporterShutdown {
		t.Fatalf("sampling result: %+v", result)
	}
	for name, decision := range want {
		if got, ok := result.Decisions[name]; !ok || got != decision {
			t.Fatalf("sampling %s: %d, want %d", name, got, decision)
		}
	}
	profileGET(t, app.URL+"/kinds", http.StatusNoContent)
	app.Stop(t)
	spans := behaviorSpans(t, nextBehaviorExport(t, receiver.Requests))
	if len(spans) != 7 {
		t.Fatalf("exported %d spans, want seed plus six kinds", len(spans))
	}
	kinds := make(map[tracepb.Span_SpanKind]int)
	for _, span := range spans {
		if span.Name == "direct.kind" {
			kinds[span.Kind]++
		} else if span.Name != "direct.seed" {
			t.Fatalf("dropped span exported: %v", span)
		}
	}
	for _, kind := range []tracepb.Span_SpanKind{tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Span_SPAN_KIND_SERVER, tracepb.Span_SPAN_KIND_CLIENT, tracepb.Span_SPAN_KIND_PRODUCER, tracepb.Span_SPAN_KIND_CONSUMER} {
		count := 1
		if kinds[kind] != count {
			t.Fatalf("kind %s count %d, want %d", kind, kinds[kind], count)
		}
	}
	if kinds[tracepb.Span_SPAN_KIND_UNSPECIFIED] != 1 {
		t.Fatalf("unknown kind was not encoded as unspecified: %v", kinds)
	}
}
