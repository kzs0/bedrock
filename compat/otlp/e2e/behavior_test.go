package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestHTTPTracePropagationAndShutdown(t *testing.T) {
	receiver := newReceiver(t)
	downstream := newReceiver(t)
	downstream.SetHandler(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	app := startApp(t, fixtureConfig{TraceEndpoint: receiver.URL + "/v1/traces", DownstreamURL: downstream.URL + "/destination"})
	const traceID = "0123456789abcdef0123456789abcdef"
	const parentID = "0123456789abcdef"
	req, err := http.NewRequest(http.MethodGet, app.URL+"/downstream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("traceparent", "00-"+traceID+"-"+parentID+"-01")
	req.Header.Set("tracestate", "vendor=value")
	response := doBehaviorRequest(t, req)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("downstream response: %d", response.StatusCode)
	}
	exportedDownstream := nextBehaviorExport(t, downstream.Requests)
	propagated := strings.Split(exportedDownstream.Header.Get("traceparent"), "-")
	if len(propagated) != 4 || propagated[0] != "00" || propagated[1] != traceID || propagated[3] != "01" {
		t.Fatalf("traceparent: %q", exportedDownstream.Header.Get("traceparent"))
	}
	if exportedDownstream.Header.Get("tracestate") != "vendor=value" {
		t.Fatalf("tracestate: %q", exportedDownstream.Header.Get("tracestate"))
	}
	for _, path := range []string{"/ok/1", "/fail"} {
		getBehavior(t, app.URL+path)
	}
	hints := make(chan int, 1)
	req, err = http.NewRequest(http.MethodGet, app.URL+"/hints", nil)
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{Got1xxResponse: func(code int, _ textproto.MIMEHeader) error { hints <- code; return nil }}))
	if got := doBehaviorRequest(t, req).StatusCode; got != http.StatusCreated {
		t.Fatalf("final response: %d", got)
	}
	select {
	case got := <-hints:
		if got != http.StatusEarlyHints {
			t.Fatalf("interim response: %d", got)
		}
	default:
		t.Fatal("missing early hints")
	}
	app.Stop(t)
	spans := behaviorSpans(t, nextBehaviorExport(t, receiver.Requests))
	var server, client *tracepb.Span
	routes := make(map[string]*tracepb.Span)
	for _, span := range spans {
		if span.EndTimeUnixNano < span.StartTimeUnixNano || span.StartTimeUnixNano == 0 {
			t.Fatalf("invalid span duration: %v", span)
		}
		if span.Kind == tracepb.Span_SPAN_KIND_CLIENT {
			client = span
		} else {
			routes[behaviorStringAttr(span, "http.path")] = span
		}
	}
	server = routes["/downstream"]
	if server == nil || client == nil {
		t.Fatalf("missing server/client spans: %v", spans)
	}
	if hex.EncodeToString(server.TraceId) != traceID || hex.EncodeToString(server.ParentSpanId) != parentID {
		t.Fatalf("incoming parent lost: %v", server)
	}
	if !bytes.Equal(client.TraceId, server.TraceId) || !bytes.Equal(client.ParentSpanId, server.SpanId) || hex.EncodeToString(client.SpanId) != propagated[2] {
		t.Fatalf("client parent linkage lost: server=%v client=%v", server, client)
	}
	if client.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_OK {
		t.Fatalf("client status: %v", client.Status)
	}
	for route, status := range map[string]int64{"/ok/{id}": 204, "/fail": 503, "/hints": 201} {
		span := routes[route]
		if span == nil || behaviorIntAttr(span, "http.status_code") != status {
			t.Fatalf("route %q span: %v", route, span)
		}
		if route == "/fail" {
			if span.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_ERROR {
				t.Fatalf("failure status: %v", span.Status)
			}
		} else if span.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR {
			t.Fatalf("successful route failed: %v", span)
		}
	}
	if len(spans) != 5 {
		t.Fatalf("span count = %d, want 5", len(spans))
	}
}

func TestHTTPMetricsUseRoutesAndOutcomes(t *testing.T) {
	app := startApp(t, fixtureConfig{})
	for i := 0; i < 12; i++ {
		if got := getBehavior(t, fmt.Sprintf("%s/ok/%d?token=private-query", app.URL, i)).StatusCode; got != 204 {
			t.Fatalf("status: %d", got)
		}
	}
	getBehavior(t, app.URL+"/fail")
	req, _ := http.NewRequest(http.MethodGet, app.MetricsURL+"/metrics", nil)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("metrics response: %d %s", response.StatusCode, raw)
	}
	text := string(raw)
	for _, name := range []string{"http_request_count", "http_request_successes", "http_request_duration_ms_count"} {
		assertBehaviorMetric(t, text, name, "/ok/{id}", 12)
	}
	assertBehaviorMetric(t, text, "http_request_count", "/fail", 1)
	assertBehaviorMetric(t, text, "http_request_failures", "/fail", 1)
	assertBehaviorMetric(t, text, "http_request_duration_ms_count", "/fail", 1)
	for _, route := range []string{"/ok/{id}", "/fail"} {
		if got, n := behaviorMetric(text, "http_request_duration_ms_sum", route); n != 1 || got < 0 {
			t.Fatalf("histogram sum %s: %g (%d series)", route, got, n)
		}
	}
	if strings.Contains(text, "private-query") || strings.Contains(text, "/ok/0") {
		t.Fatalf("raw URL leaked into metrics: %s", text)
	}
}

func TestSamplingAndNoTracePreserveMetrics(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		rate       float64
	}{{"zero_sample", "/ok/1", 0}, {"no_trace", "/notrace", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			receiver := newReceiver(t)
			downstream := newReceiver(t)
			downstream.SetHandler(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
			app := startApp(t, fixtureConfig{TraceEndpoint: receiver.URL + "/v1/traces", SampleRate: &tc.rate, DownstreamURL: downstream.URL})
			if got := getBehavior(t, app.URL+tc.path).StatusCode; got != 204 {
				t.Fatalf("status: %d", got)
			}
			response, err := (&http.Client{Timeout: 5 * time.Second}).Get(app.MetricsURL + "/metrics")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			route := tc.path
			if tc.name == "zero_sample" {
				route = "/ok/{id}"
			}
			assertBehaviorMetric(t, string(raw), "http_request_count", route, 1)
			app.Stop(t)
			if tc.name == "no_trace" {
				request := nextBehaviorExport(t, downstream.Requests)
				if request.Header.Get("traceparent") != "" {
					t.Fatalf("NoTrace injected context: %v", request.Header)
				}
				spans := behaviorSpans(t, nextBehaviorExport(t, receiver.Requests))
				if len(spans) != 1 || spans[0].Name != "http.request" {
					t.Fatalf("NoTrace exported nested spans: %v", spans)
				}
			} else {
				select {
				case request := <-receiver.Requests:
					t.Fatalf("unexpected trace export: %s", request.Body)
				default:
				}
			}
		})
	}
}

func doBehaviorRequest(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func getBehavior(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return doBehaviorRequest(t, req)
}
func nextBehaviorExport(t *testing.T, requests <-chan exportRequest) exportRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(12 * time.Second):
		t.Fatal("export deadline exceeded")
		return exportRequest{}
	}
}
func behaviorSpans(t *testing.T, request exportRequest) []*tracepb.Span {
	t.Helper()
	if request.Path != "/v1/traces" || request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("trace envelope: %s %v", request.Path, request.Header)
	}
	var document any
	if err := json.Unmarshal(request.Body, &document); err != nil {
		t.Fatal(err)
	}
	var normalize func(any)
	normalize = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			for key, child := range node {
				if key == "traceId" || key == "spanId" || key == "parentSpanId" {
					s, ok := child.(string)
					if !ok {
						t.Fatalf("invalid %s", key)
					}
					decoded, err := hex.DecodeString(s)
					want := 8
					if key == "traceId" {
						want = 16
					}
					if err != nil || len(decoded) != want || bytes.Equal(decoded, make([]byte, want)) {
						t.Fatalf("invalid %s: %q", key, s)
					}
					node[key] = base64.StdEncoding.EncodeToString(decoded)
				} else {
					normalize(child)
				}
			}
		case []any:
			for _, child := range node {
				normalize(child)
			}
		}
	}
	normalize(document)
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded := new(collector.ExportTraceServiceRequest)
	if err := protojson.Unmarshal(raw, decoded); err != nil {
		t.Fatalf("official OTLP decoder: %v", err)
	}
	var spans []*tracepb.Span
	for _, resource := range decoded.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			spans = append(spans, scope.Spans...)
		}
	}
	return spans
}
func behaviorStringAttr(span *tracepb.Span, key string) string {
	for _, a := range span.Attributes {
		if a.Key == key {
			return a.Value.GetStringValue()
		}
	}
	return ""
}
func behaviorIntAttr(span *tracepb.Span, key string) int64 {
	for _, a := range span.Attributes {
		if a.Key == key {
			return a.Value.GetIntValue()
		}
	}
	return 0
}
func behaviorMetric(text, name, route string) (float64, int) {
	var sum float64
	var count int
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, name+"{") || !strings.Contains(line, `http_path="`+route+`"`) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err == nil {
			sum += value
			count++
		}
	}
	return sum, count
}
func assertBehaviorMetric(t *testing.T, text, name, route string, want float64) {
	t.Helper()
	got, count := behaviorMetric(text, name, route)
	if count != 1 || got != want {
		t.Fatalf("%s route %q = %g (%d series), want %g in one series\n%s", name, route, got, count, want, text)
	}
}

func TestUnsampledTraceContextReachesDownstream(t *testing.T) {
	receiver := newReceiver(t)
	downstream := newReceiver(t)
	rate := 0.0
	app := startApp(t, fixtureConfig{TraceEndpoint: receiver.URL + "/v1/traces", DownstreamURL: downstream.URL, SampleRate: &rate})
	req, err := http.NewRequest(http.MethodGet, app.URL+"/downstream", nil)
	if err != nil {
		t.Fatal(err)
	}
	const traceID = "fedcba9876543210fedcba9876543210"
	req.Header.Set("traceparent", "00-"+traceID+"-fedcba9876543210-00")
	doBehaviorRequest(t, req)
	request := nextBehaviorExport(t, downstream.Requests)
	parent := strings.Split(request.Header.Get("traceparent"), "-")
	if len(parent) != 4 || parent[1] != traceID || parent[3] != "00" {
		t.Fatalf("unsampled traceparent: %q", request.Header.Get("traceparent"))
	}
	app.Stop(t)
	select {
	case request := <-receiver.Requests:
		t.Fatalf("unsampled trace exported: %s", request.Body)
	default:
	}
}
