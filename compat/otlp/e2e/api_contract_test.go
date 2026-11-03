package e2e

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func apiJSON(t *testing.T, endpoint string, target any) {
	t.Helper()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("API response: %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func apiRead(t *testing.T, endpoint string) string {
	t.Helper()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("API response: %d %q %v", response.StatusCode, data, err)
	}
	return string(data)
}

func apiMetric(t *testing.T, exposition, name string, labels map[string]string, want float64) {
	t.Helper()
	var count int
	for _, line := range strings.Split(exposition, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.HasPrefix(line, "#") {
			continue
		}
		metricName, _, _ := strings.Cut(fields[0], "{")
		if metricName != name {
			continue
		}
		match := true
		for key, value := range labels {
			if !strings.Contains(fields[0], key+"="+strconv.Quote(value)) {
				match = false
			}
		}
		if !match {
			continue
		}
		count++
		got, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || got != want {
			t.Fatalf("metric %s labels %v: got %s, want %g", name, labels, fields[1], want)
		}
	}
	if count != 1 {
		t.Fatalf("metric %s labels %v: %d matches\n%s", name, labels, count, exposition)
	}
}

func TestAPISourceOperationsStepsAndOutcomes(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{Scenario: "api", TraceEndpoint: receiver.URL + "/v1/traces"})
	if got := getBehavior(t, app.ControlURL+"/workflow").StatusCode; got != http.StatusNoContent {
		t.Fatalf("workflow: %d", got)
	}
	metrics := apiRead(t, app.MetricsURL+"/metrics")
	for name, want := range map[string]float64{"jobs_processed": 2, "jobs_depth": 3, "jobs_latency_count": 1, "jobs_latency_sum": 4, "jobs_parent_count": 1, "jobs_parent_successes": 1, "jobs_child_count": 1, "jobs_child_successes": 1, "jobs_failure_failures": 1, "rich_failure_failures": 1, "nil_failure_failures": 1, "explicit_failure_failures": 1, "quiet_parent_count": 1, "quiet_child_count": 1} {
		apiMetric(t, metrics, name, nil, want)
	}
	apiMetric(t, metrics, "jobs_parent_count", map[string]string{"region": "west", "tenant": "operation", "missing": "_", "step_label": "step"}, 1)
	apiMetric(t, metrics, "jobs_child_count", map[string]string{"tenant": "source", "step_label": "_"}, 1)
	apiMetric(t, metrics, "jobs_failure_failures", map[string]string{"reason": "legacy"}, 1)
	if strings.Contains(metrics, "lookup_count") || strings.Contains(metrics, "quiet_count") {
		t.Fatal("steps created operation metrics")
	}
	app.Stop(t)
	spans := behaviorSpans(t, nextBehaviorExport(t, receiver.Requests))
	byName := map[string]*tracepb.Span{}
	for _, span := range spans {
		if byName[span.Name] != nil {
			t.Fatalf("duplicate completed span: %s", span.Name)
		}
		byName[span.Name] = span
	}
	wantNames := []string{"jobs.parent", "jobs.child", "lookup", "jobs.failure", "rich.failure", "nil.failure", "explicit.failure", "standalone"}
	if len(byName) != len(wantNames) {
		t.Fatalf("spans: %v", byName)
	}
	for _, name := range wantNames {
		if byName[name] == nil {
			t.Fatalf("missing %s", name)
		}
	}
	parent, child, step := byName["jobs.parent"], byName["jobs.child"], byName["lookup"]
	for _, descendant := range []*tracepb.Span{child, step} {
		if !bytes.Equal(parent.TraceId, descendant.TraceId) || !bytes.Equal(parent.SpanId, descendant.ParentSpanId) {
			t.Fatalf("parent linkage: parent=%v child=%v", parent, descendant)
		}
	}
	if behaviorStringAttr(parent, "tenant") != "operation" || behaviorStringAttr(child, "tenant") != "source" || behaviorIntAttr(step, "rows") != 7 {
		t.Fatalf("inherited or step attributes: %v %v %v", parent, child, step)
	}
	for _, name := range []string{"jobs.parent", "jobs.child"} {
		if byName[name].GetStatus().GetCode() != tracepb.Status_STATUS_CODE_OK {
			t.Fatalf("explicit success lost: %v", byName[name])
		}
	}
	for _, name := range []string{"jobs.failure", "rich.failure", "nil.failure", "explicit.failure"} {
		if byName[name].GetStatus().GetCode() != tracepb.Status_STATUS_CODE_ERROR {
			t.Fatalf("failure lost: %v", byName[name])
		}
	}
	if len(step.Events) != 1 || step.Events[0].Name != "lookup.complete" || len(child.Events) != 1 || child.Events[0].Name != "child.event" {
		t.Fatalf("events: %v %v", step.Events, child.Events)
	}
	rich := byName["rich.failure"]
	if !strings.Contains(behaviorStringAttr(rich, "error.stack"), "apiWorkflow") || len(behaviorStringAttr(rich, "error.fingerprint")) != 16 {
		t.Fatalf("error details: %v", rich)
	}
	var chain []struct{ Message string }
	if err := json.Unmarshal([]byte(behaviorStringAttr(rich, "error.chain")), &chain); err != nil || len(chain) != 1 || chain[0].Message != "inner\tmarker" {
		t.Fatalf("wrapped chain: %+v %v", chain, err)
	}
	logs := apiLogRecords(app.Logs())
	var parentCompletions int
	for _, record := range logs {
		if record["msg"] == "operation.complete" && record["operation"] == "jobs.parent" {
			parentCompletions++
			if record["success"] != true {
				t.Fatalf("canonical outcome: %v", record)
			}
		}
	}
	if parentCompletions != 1 {
		t.Fatalf("parent completion logs: %d", parentCompletions)
	}
	for message, level := range map[string]string{"api.debug": "DEBUG", "api.info": "INFO", "api.warn": "WARN", "api.error": "ERROR", "api.custom": "INFO"} {
		var matches int
		for _, record := range logs {
			if record["msg"] == message {
				matches++
				if record["level"] != level || record["trace_id"] != hex.EncodeToString(parent.TraceId) || record["span_id"] != hex.EncodeToString(parent.SpanId) || record["region"] != "west" {
					t.Fatalf("contextual log: %v", record)
				}
			}
		}
		if matches != 1 {
			t.Fatalf("log %s matches: %d", message, matches)
		}
	}
}

func apiLogRecords(logs string) []map[string]any {
	var records []map[string]any
	for _, line := range strings.Split(logs, "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil {
			records = append(records, record)
		}
	}
	return records
}

func TestAPIMetricWrappersPreserveValuesAndLabels(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "api"})
	if got := getBehavior(t, app.ControlURL+"/metrics-workload").StatusCode; got != http.StatusNoContent {
		t.Fatalf("metric workload: %d", got)
	}
	metrics := apiRead(t, app.MetricsURL+"/metrics")
	for name, want := range map[string]float64{"api_counter": 7, "api_counter_vec": 3, "api_gauge": 14, "api_gauge_vec": 11, "api_hist_count": 2, "api_hist_sum": 6, "api_hist_vec_count": 1, "api_hist_vec_sum": 3} {
		apiMetric(t, metrics, name, map[string]string{"region": "west"}, want)
	}
	apiMetric(t, metrics, "api_counter_vec", map[string]string{"lane": "fast"}, 3)
	apiMetric(t, metrics, "api_gauge_vec", map[string]string{"lane": "slow"}, 11)
	apiMetric(t, metrics, "api_hist_bucket", map[string]string{"le": "1"}, 0)
	apiMetric(t, metrics, "api_hist_bucket", map[string]string{"le": "5"}, 2)
	for name, want := range map[string]float64{"api_raw_counter": 1, "api_raw_gauge": 9, "api_raw_hist_sum": 1.5, "api_raw_hist_count": 1} {
		apiMetric(t, metrics, name, nil, want)
	}
	app.Stop(t)
}

func TestAPIMetricLimitsRejectWithoutCorruptingRegistry(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "api"})
	var result struct {
		Panics  map[string]string
		Drops   int
		Metrics string
	}
	apiJSON(t, app.ControlURL+"/bounds", &result)
	if result.Drops != 3 || len(result.Panics) != 4 {
		t.Fatalf("bounds: %+v", result)
	}
	for name, want := range map[string]string{"family": "family limit exceeded", "type": "conflicting registration", "schema": "conflicting registration", "invalid_limits": "limits must be positive"} {
		if !strings.Contains(result.Panics[name], want) {
			t.Fatalf("panic %s: %q", name, result.Panics[name])
		}
	}
	for name, want := range map[string]float64{"bounds_counter": 3, "bounds_gauge": 3, "bounds_hist_count": 1, "bounds_hist_sum": 4} {
		apiMetric(t, result.Metrics, name, map[string]string{"lane": "accepted"}, want)
	}
	if strings.Contains(result.Metrics, "rejected") || strings.Contains(result.Metrics, "unknown=") || strings.Contains(result.Metrics, "fourth") {
		t.Fatalf("rejected registration leaked: %s", result.Metrics)
	}
	app.Stop(t)
}

func TestAPIAttributeTypesSetsAndRepresentations(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "api"})
	var result struct {
		Values       map[string]any
		Strings      map[string]string
		Typed        map[string]any
		Keys         []string
		Set          map[string]string
		HasA         bool `json:"has_a"`
		HasMissing   bool `json:"has_missing"`
		MissingFound bool `json:"missing_found"`
		First        string
		Renamed      string
	}
	apiJSON(t, app.ControlURL+"/attributes", &result)
	if !reflect.DeepEqual(result.Keys, []string{"a", "b", "z"}) || !reflect.DeepEqual(result.Set, map[string]string{"a": "override", "b": "middle", "z": "last"}) || !result.HasA || result.HasMissing || result.MissingFound || result.First != "a" || result.Renamed != "new=value" {
		t.Fatalf("immutable set results: %+v", result)
	}
	for key, want := range map[string]any{"string": "hello", "int": float64(-3), "int64": float64(-4), "uint64": float64(5), "float64": 1.25, "true": true, "false": false, "duration": float64(1500000000), "time": "2026-01-02T02:04:05.000000123Z", "slice": []any{"a", "b"}, "error": ""} {
		if !reflect.DeepEqual(result.Values[key], want) {
			t.Fatalf("attribute %s: %#v, want %#v", key, result.Values[key], want)
		}
	}
	for key, want := range map[string]string{"string": "hello", "int": "-3", "int64": "-4", "uint64": "5", "float64": "1.25", "true": "true", "false": "false", "duration": "1.5s", "time": "2026-01-02T02:04:05.000000123Z", "slice": "[a b]", "error": ""} {
		if result.Strings[key] != want {
			t.Fatalf("attribute string %s: %q, want %q", key, result.Strings[key], want)
		}
	}
	wantTyped := map[string]any{"string": "typed", "int64": float64(-5), "uint64": float64(7), "float64": 2.5, "true": true, "false": false, "duration": float64(1000000000), "time": "2026-01-02T02:04:05.000000123Z"}
	if !reflect.DeepEqual(result.Typed, wantTyped) {
		t.Fatalf("typed attribute accessors: %#v", result.Typed)
	}
	if len(result.Values) != 21 || len(result.Strings) != 21 {
		t.Fatalf("Any conversion results: %v %v", result.Values, result.Strings)
	}
	for key, want := range map[string]any{"any.string.typed": "typed", "any.int.3": float64(3), "any.int64.-5": float64(-5), "any.uint64.7": float64(7), "any.float64.2.5": 2.5, "any.bool.true": true, "any.bool.false": false, "any.time.Duration.1s": float64(1000000000), "any.time.Time.2026-01-02 03:04:05.000000123 +0100 offset": "2026-01-02T02:04:05.000000123Z", "any.[]int.[1 2]": []any{float64(1), float64(2)}} {
		if got, ok := result.Values[key]; !ok || !reflect.DeepEqual(got, want) || !strings.HasPrefix(result.Strings[key], key+"=") {
			t.Fatalf("Any conversion %s: %v %v", key, result.Values, result.Strings)
		}
	}
	app.Stop(t)
}

func TestAPINoopContextProducesNoTelemetry(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{Scenario: "api", TraceEndpoint: receiver.URL + "/v1/traces"})
	if got := getBehavior(t, app.ControlURL+"/noop").StatusCode; got != http.StatusNoContent {
		t.Fatalf("noop workload: %d", got)
	}
	if text := apiRead(t, app.MetricsURL+"/metrics"); strings.Contains(text, "noop") {
		t.Fatalf("noop metrics: %s", text)
	}
	app.Stop(t)
	if strings.Contains(app.Logs(), "noop.") {
		t.Fatalf("noop logs: %s", app.Logs())
	}
	select {
	case request := <-receiver.Requests:
		t.Fatalf("noop export: %s", request.Body)
	default:
	}
	if stats := app.FinalStats(t); stats.Trace.DroppedSpans != 0 || stats.Trace.FailedExports != 0 {
		t.Fatalf("noop losses: %+v", stats)
	}
}

func TestAPIHTTPStreamingHijackingAndMiddlewareOptions(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{Scenario: "api", TraceEndpoint: receiver.URL + "/v1/traces"})
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(app.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	reader := bufio.NewReader(response.Body)
	if first, err := reader.ReadString('\n'); err != nil || first != "first\n" {
		t.Fatalf("flushed chunk before release: %q %v", first, err)
	}
	if got := getBehavior(t, app.ControlURL+"/stream-release").StatusCode; got != http.StatusNoContent {
		t.Fatalf("stream release: %d", got)
	}
	if tail, err := io.ReadAll(reader); err != nil || string(tail) != "second\n" {
		t.Fatalf("ReaderFrom chunk: %q %v", tail, err)
	}
	_ = response.Body.Close()
	if got := apiRead(t, app.URL+"/hijack"); got != "hijacked" {
		t.Fatalf("hijacked body: %q", got)
	}
	for _, path := range []string{"/accepted", "/rejected"} {
		req, _ := http.NewRequest(http.MethodGet, app.URL+path, nil)
		req.Header.Set("X-Tenant", "test-tenant")
		req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
		response := doBehaviorRequest(t, req)
		want := http.StatusAccepted
		if path == "/rejected" {
			want = http.StatusCreated
		}
		if response.StatusCode != want {
			t.Fatalf("%s status: %d", path, response.StatusCode)
		}
	}
	metrics := apiRead(t, app.MetricsURL+"/metrics")
	apiMetric(t, metrics, "api_http_successes", map[string]string{"http_path": "/accepted", "tenant": "test-tenant"}, 1)
	apiMetric(t, metrics, "api_http_failures", map[string]string{"http_path": "/rejected", "tenant": "test-tenant"}, 1)
	app.Stop(t)
	spans := behaviorSpans(t, nextBehaviorExport(t, receiver.Requests))
	if len(spans) != 4 {
		t.Fatalf("middleware spans: %v", spans)
	}
	for _, span := range spans {
		if span.Name != "api.http" || hex.EncodeToString(span.TraceId) == "0123456789abcdef0123456789abcdef" || len(span.ParentSpanId) != 0 {
			t.Fatalf("disabled propagation: %v", span)
		}
		if behaviorStringAttr(span, "http.path") == "/rejected" && span.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_ERROR {
			t.Fatalf("custom success codes lost: %v", span)
		}
	}
}

func TestAPIHTTPClientHelpersPreserveOwnership(t *testing.T) {
	receiver := newReceiver(t)
	downstream := newReceiver(t)
	downstream.SetHandler(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, "%s:%s:%s", r.Method, r.Header.Get("Content-Type"), body)
	})
	app := startApp(t, fixtureConfig{Scenario: "api", TraceEndpoint: receiver.URL + "/v1/traces", DownstreamURL: downstream.URL + "/resource"})
	var result struct {
		Results []struct {
			Status    int
			Body      string
			Error     string
			ReadError bool `json:"read_error"`
		}
		InvalidGet  bool `json:"invalid_get"`
		InvalidPost bool `json:"invalid_post"`
	}
	apiJSON(t, app.ControlURL+"/client", &result)
	wantBodies := []string{"GET::", "POST:text/plain:payload", "POST::empty-type", "PUT::owned", "GET::", "GET::"}
	if len(result.Results) != len(wantBodies) || !result.InvalidGet || !result.InvalidPost {
		t.Fatalf("client result: %+v", result)
	}
	var propagated []string
	for i, want := range wantBodies {
		got := result.Results[i]
		if got.Status != http.StatusAccepted || got.Body != want || got.Error != "" || got.ReadError {
			t.Fatalf("helper %d: %+v, want %s", i, got, want)
		}
		request := nextBehaviorExport(t, downstream.Requests)
		if i < 5 {
			if request.Header.Get("traceparent") == "" {
				t.Fatalf("helper %d omitted propagation", i)
			}
			propagated = append(propagated, strings.Split(request.Header.Get("traceparent"), "-")[2])
		} else if request.Header.Get("traceparent") != "" {
			t.Fatal("context-free client invented propagation")
		}
		if i == 3 && request.Header.Get("X-Original") != "preserved" {
			t.Fatal("original request header lost")
		}
	}
	app.Stop(t)
	spans := behaviorSpans(t, nextBehaviorExport(t, receiver.Requests))
	var parent *tracepb.Span
	for _, span := range spans {
		if span.Name == "api.client" {
			parent = span
		}
	}
	if parent == nil || len(spans) != 6 {
		t.Fatalf("client spans: %v", spans)
	}
	seen := map[string]bool{}
	for _, span := range spans {
		if span == parent {
			continue
		}
		if span.Kind != tracepb.Span_SPAN_KIND_CLIENT || !bytes.Equal(span.ParentSpanId, parent.SpanId) || !bytes.Equal(span.TraceId, parent.TraceId) || span.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_OK {
			t.Fatalf("client linkage/status: %v", span)
		}
		seen[hex.EncodeToString(span.SpanId)] = true
	}
	for _, id := range propagated {
		if !seen[id] {
			t.Fatalf("propagated client span missing: %s", id)
		}
	}
}

func TestAPIResponseAdapterCapabilityContracts(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "api"})
	cases := []struct {
		mask       int
		mode       string
		advertised int
	}{}
	for mask := 0; mask < 16; mask++ {
		cases = append(cases, struct {
			mask       int
			mode       string
			advertised int
		}{mask, "", mask})
	}
	cases = append(cases, []struct {
		mask       int
		mode       string
		advertised int
	}{{15, "wrapped", 3}, {0, "wrapped", 0}, {15, "nil", 0}, {0, "error", 1}, {0, "failed", 1}, {0, "legacy", 1}}...)
	for _, tc := range cases {
		t.Run(fmt.Sprintf("mask%d/%s", tc.mask, tc.mode), func(t *testing.T) {
			var got struct {
				Mask, Status, Flushes, Hijacks, Pushes, Copies int
				Body                                           string
				FlushErrors                                    int    `json:"flush_errors"`
				PushTarget                                     string `json:"push_target"`
				PushHeader                                     string `json:"push_header"`
				FlushError                                     string `json:"flush_error"`
				HijackError                                    string `json:"hijack_error"`
				PushError                                      string `json:"push_error"`
				DeadlineUnsupported                            bool   `json:"deadline_unsupported"`
			}
			apiJSON(t, fmt.Sprintf("%s/capabilities?mask=%d&mode=%s", app.ControlURL, tc.mask, tc.mode), &got)
			status := 200
			if tc.mode == "failed" {
				status = 503
			}
			if got.Mask != tc.advertised || got.Status != status || got.Body != "adapter-body" || !got.DeadlineUnsupported {
				t.Fatalf("adapter result: %+v", got)
			}
			wantFlushes, wantFlushErrors := 0, 0
			if tc.advertised&1 != 0 {
				wantFlushes = 1
			}
			if tc.mode == "error" || tc.mode == "failed" {
				wantFlushes, wantFlushErrors = 0, 1
			}
			if got.Flushes != wantFlushes || got.FlushErrors != wantFlushErrors {
				t.Fatalf("flush precedence: %+v", got)
			}
			for _, capability := range []struct {
				bit, count int
				message    string
			}{{2, got.Hijacks, got.HijackError}, {4, got.Pushes, got.PushError}, {8, got.Copies, ""}} {
				want := 0
				if tc.advertised&capability.bit != 0 {
					want = 1
				}
				if capability.count != want || (capability.bit != 8 && want == 1 && !strings.Contains(capability.message, "adapter")) {
					t.Fatalf("capability %d: %+v", capability.bit, got)
				}
			}
			if got.Pushes == 1 && (got.PushTarget != "/asset" || got.PushHeader != "forwarded") {
				t.Fatalf("push delegation: %+v", got)
			}
			if (tc.mode == "failed") != (got.FlushError == "adapter flush marker") {
				t.Fatalf("flush error propagation: %+v", got)
			}
		})
	}
	app.Stop(t)
}

func TestAPIStructuredLoggingGroupsLevelsAndTypes(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "api"})
	var result struct{ JSON, Text, Omitted string }
	apiJSON(t, app.ControlURL+"/structured-logs", &result)
	records := apiLogRecords(result.JSON)
	if len(records) != 2 {
		t.Fatalf("filtered logs: %s", result.JSON)
	}
	seen := map[string]int{}
	for _, record := range records {
		group, ok := record["transaction"].(map[string]any)
		if !ok || record["app"] != "adapter" || group["tenant"] != "visible" || record["level"] != "WARN" || record["source"] != nil || group["omit"] != nil {
			t.Fatalf("grouped log: %v", record)
		}
		if record["msg"] == "structured" {
			seen["structured"]++
			traceID, _ := group["trace_id"].(string)
			spanID, _ := group["span_id"].(string)
			_, traceErr := hex.DecodeString(traceID)
			_, spanErr := hex.DecodeString(spanID)
			if len(traceID) != 32 || len(spanID) != 16 || traceErr != nil || spanErr != nil || traceID == strings.Repeat("0", 32) || spanID == strings.Repeat("0", 16) || group["safe"] != "visible" || group["password"] != "[redacted]" {
				t.Fatalf("context and privacy: %v", record)
			}
		} else if record["msg"] != "context-free" || group["trace_id"] != nil || group["span_id"] != nil || group["count"] != float64(2) {
			t.Fatalf("context-free log: %v", record)
		} else {
			seen["context-free"]++
		}
	}
	if seen["structured"] != 1 || seen["context-free"] != 1 {
		t.Fatalf("log multiplicity: %v", seen)
	}
	omitted := apiLogRecords(result.Omitted)
	if len(omitted) != 1 || omitted[0]["msg"] != nil || omitted[0]["safe"] != "retained" {
		t.Fatalf("message filtering: %s", result.Omitted)
	}
	for _, token := range []string{"source=", "msg=typed", "int64=-7", "uint64=8", "float64=2.5", "bool=true", "duration=1s", "time=1970-01-01T00:00:00.000Z", "slice=\"[1 2]\""} {
		if !strings.Contains(result.Text, token) {
			t.Fatalf("typed text missing %s: %s", token, result.Text)
		}
	}
	app.Stop(t)
	if !strings.Contains(app.Logs(), "api.default-handler") || strings.Contains(result.JSON, "suppressed") || strings.Contains(result.JSON, "private") || strings.Contains(result.JSON, "excluded") {
		t.Fatalf("logging policy: %s\n%s", result.JSON, app.Logs())
	}
}

func TestAPIAttributeWrongKindRejection(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "api"})
	var result map[string]string
	apiJSON(t, app.ControlURL+"/attribute-rejections", &result)
	if len(result) != 7 {
		t.Fatalf("attribute rejection count: %v", result)
	}
	for key, want := range map[string]string{"string": "Value.AsString: not a string", "int64": "Value.AsInt64: not an int64", "uint64": "Value.AsUint64: not a uint64", "float64": "Value.AsFloat64: not a float64", "bool": "Value.AsBool: not a bool", "duration": "Value.AsDuration: not a duration", "time": "Value.AsTime: not a time"} {
		if result[key] != want {
			t.Fatalf("%s rejection: %s, want %s", key, result[key], want)
		}
	}
	app.Stop(t)
}
