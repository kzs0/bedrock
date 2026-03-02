package prometheus

import (
	"bytes"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/metric"
)

// ── Encode tests ──────────────────────────────────────────────────────────────

func TestEncode_Counter(t *testing.T) {
	families := []metric.MetricFamily{
		{
			Name: "requests_total",
			Help: "Total requests",
			Type: metric.TypeCounter,
			Metrics: []metric.Metric{
				{Labels: attr.NewSet(attr.String("method", "GET")), Value: 42},
			},
		},
	}
	var buf bytes.Buffer
	if err := Encode(&buf, families); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "# HELP requests_total Total requests") {
		t.Errorf("missing HELP line: %q", out)
	}
	if !strings.Contains(out, "# TYPE requests_total counter") {
		t.Errorf("missing TYPE line: %q", out)
	}
	if !strings.Contains(out, `requests_total{method="GET"} 42`) {
		t.Errorf("missing metric line: %q", out)
	}
}

func TestEncode_Gauge(t *testing.T) {
	families := []metric.MetricFamily{
		{
			Name: "queue_depth",
			Help: "Queue depth",
			Type: metric.TypeGauge,
			Metrics: []metric.Metric{
				{Labels: attr.NewSet(), Value: 7},
			},
		},
	}
	var buf bytes.Buffer
	if err := Encode(&buf, families); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "# TYPE queue_depth gauge") {
		t.Errorf("missing TYPE line: %q", out)
	}
	if !strings.Contains(out, "queue_depth 7") {
		t.Errorf("missing metric line (no labels): %q", out)
	}
}

func TestEncode_Histogram(t *testing.T) {
	families := []metric.MetricFamily{
		{
			Name: "latency_ms",
			Help: "Latency",
			Type: metric.TypeHistogram,
			Metrics: []metric.Metric{
				{
					Labels: attr.NewSet(attr.String("route", "/api")),
					Buckets: []metric.Bucket{
						{UpperBound: 10, Count: 3},
						{UpperBound: 100, Count: 7},
					},
					Count: 10,
					Sum:   350.5,
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := Encode(&buf, families); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "# TYPE latency_ms histogram") {
		t.Errorf("missing TYPE line: %q", out)
	}
	if !strings.Contains(out, `latency_ms_bucket{route="/api",le="10"} 3`) {
		t.Errorf("missing bucket line: %q", out)
	}
	if !strings.Contains(out, `latency_ms_bucket{route="/api",le="+Inf"} 10`) {
		t.Errorf("missing +Inf bucket: %q", out)
	}
	if !strings.Contains(out, `latency_ms_sum{route="/api"} 350.5`) {
		t.Errorf("missing sum line: %q", out)
	}
	if !strings.Contains(out, `latency_ms_count{route="/api"} 10`) {
		t.Errorf("missing count line: %q", out)
	}
}

func TestEncode_EmptyFamily(t *testing.T) {
	// Families with no metrics should be skipped entirely
	families := []metric.MetricFamily{
		{Name: "empty_metric", Help: "No data", Type: metric.TypeCounter, Metrics: nil},
	}
	var buf bytes.Buffer
	if err := Encode(&buf, families); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output for empty metric family, got %q", buf.String())
	}
}

func TestEncode_NoHelp(t *testing.T) {
	families := []metric.MetricFamily{
		{
			Name: "no_help_metric",
			Help: "", // empty help
			Type: metric.TypeCounter,
			Metrics: []metric.Metric{
				{Labels: attr.NewSet(), Value: 1},
			},
		},
	}
	var buf bytes.Buffer
	if err := Encode(&buf, families); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "# HELP") {
		t.Errorf("should not emit # HELP for empty help string: %q", out)
	}
	if !strings.Contains(out, "# TYPE no_help_metric counter") {
		t.Errorf("missing TYPE line: %q", out)
	}
}

func TestEncode_SortedByName(t *testing.T) {
	families := []metric.MetricFamily{
		{Name: "zzz_metric", Type: metric.TypeCounter, Metrics: []metric.Metric{{Value: 1}}},
		{Name: "aaa_metric", Type: metric.TypeCounter, Metrics: []metric.Metric{{Value: 2}}},
	}
	var buf bytes.Buffer
	if err := Encode(&buf, families); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := buf.String()
	aPos := strings.Index(out, "aaa_metric")
	zPos := strings.Index(out, "zzz_metric")
	if aPos > zPos {
		t.Errorf("families should be sorted alphabetically, got aaa at %d, zzz at %d", aPos, zPos)
	}
}

func TestEncode_MultipleLabels(t *testing.T) {
	families := []metric.MetricFamily{
		{
			Name: "multi_label",
			Type: metric.TypeCounter,
			Metrics: []metric.Metric{
				{
					Labels: attr.NewSet(
						attr.String("env", "prod"),
						attr.String("region", "us-east-1"),
					),
					Value: 100,
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := Encode(&buf, families); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "multi_label{") {
		t.Errorf("missing metric with labels: %q", out)
	}
	if !strings.Contains(out, `env="prod"`) {
		t.Errorf("missing env label: %q", out)
	}
	if !strings.Contains(out, `region="us-east-1"`) {
		t.Errorf("missing region label: %q", out)
	}
}

// ── formatFloat tests ─────────────────────────────────────────────────────────

func TestFormatFloat_Normal(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{1.5, "1.5"},
		{100, "100"},
		{-1, "-1"},
	}
	for _, tt := range tests {
		got := formatFloat(tt.input)
		if got != tt.want {
			t.Errorf("formatFloat(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatFloat_NaN(t *testing.T) {
	if got := formatFloat(math.NaN()); got != "NaN" {
		t.Errorf("formatFloat(NaN) = %q, want NaN", got)
	}
}

func TestFormatFloat_PosInf(t *testing.T) {
	if got := formatFloat(math.Inf(1)); got != "+Inf" {
		t.Errorf("formatFloat(+Inf) = %q, want +Inf", got)
	}
}

func TestFormatFloat_NegInf(t *testing.T) {
	if got := formatFloat(math.Inf(-1)); got != "-Inf" {
		t.Errorf("formatFloat(-Inf) = %q, want -Inf", got)
	}
}

// ── escapeHelp tests ──────────────────────────────────────────────────────────

func TestEscapeHelp_Backslash(t *testing.T) {
	got := escapeHelp(`path\to\file`)
	want := `path\\to\\file`
	if got != want {
		t.Errorf("escapeHelp backslash: got %q, want %q", got, want)
	}
}

func TestEscapeHelp_Newline(t *testing.T) {
	got := escapeHelp("line1\nline2")
	want := `line1\nline2`
	if got != want {
		t.Errorf("escapeHelp newline: got %q, want %q", got, want)
	}
}

func TestEscapeHelp_NoChange(t *testing.T) {
	s := "normal help text with spaces and numbers 123"
	if got := escapeHelp(s); got != s {
		t.Errorf("escapeHelp should not change plain text: got %q", got)
	}
}

func TestEscapeHelp_Both(t *testing.T) {
	got := escapeHelp("a\\b\nc")
	want := `a\\b\nc`
	if got != want {
		t.Errorf("escapeHelp both: got %q, want %q", got, want)
	}
}

// ── Handler tests ─────────────────────────────────────────────────────────────

func TestHandler_MetricsEndpoint(t *testing.T) {
	reg := metric.NewRegistry("")
	c := reg.Counter("req_total", "Requests", "method")
	c.With(attr.String("method", "GET")).Inc()

	h := Handler(reg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type: got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "req_total") {
		t.Errorf("expected metric name in body: %q", body)
	}
}

func TestHandler_EmptyRegistry(t *testing.T) {
	reg := metric.NewRegistry("")
	h := Handler(reg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	// Empty output is valid
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for empty registry, got %q", rec.Body.String())
	}
}

// ── Registry integration via Encode ──────────────────────────────────────────

func TestEncode_ViaRegistry(t *testing.T) {
	reg := metric.NewRegistry("")
	g := reg.Gauge("active_conn", "Active connections")
	g.Set(5)

	h := reg.Histogram("dur_ms", "Duration", []float64{10, 100, 1000})
	h.Observe(50)
	h.Observe(200)

	families := reg.Gather()
	var buf bytes.Buffer
	if err := Encode(&buf, families); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "active_conn 5") {
		t.Errorf("missing gauge: %q", out)
	}
	if !strings.Contains(out, "dur_ms_count") {
		t.Errorf("missing histogram count: %q", out)
	}
	if !strings.Contains(out, "dur_ms_sum") {
		t.Errorf("missing histogram sum: %q", out)
	}
}
