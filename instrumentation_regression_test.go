package bedrock_test

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	b "github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/metric"
)

func regressionBedrock(t *testing.T, attrs ...attr.Attr) (*b.Bedrock, context.Context) {
	t.Helper()
	cfg := b.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.RuntimeMetrics = false
	cfg.ServerEnabled = false
	br, err := b.New(cfg, attrs...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := br.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return br, b.WithBedrock(context.Background(), br)
}
func regressionFamily(t *testing.T, br *b.Bedrock, name string) metric.MetricFamily {
	t.Helper()
	for _, f := range br.Metrics().Gather() {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("missing metric %q", name)
	return metric.MetricFamily{}
}
func TestTraceSampleRateZeroDisablesRecording(t *testing.T) {
	cfg := b.DefaultConfig()
	cfg.TraceSampleRate = 0
	cfg.LogOutput = io.Discard
	br, err := b.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, span := br.Tracer().Start(context.Background(), "disabled")
	defer span.End()
	if span.IsRecording() {
		t.Fatal("zero sampling rate records a span")
	}
}
func TestInvalidTraceSampleRatesRejected(t *testing.T) {
	for _, rate := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			cfg := b.DefaultConfig()
			cfg.TraceSampleRate = rate
			br, err := b.New(cfg)
			if err == nil {
				if br != nil {
					_ = br.Shutdown(context.Background())
				}
				t.Fatal("invalid sampling rate accepted")
			}
		})
	}
}
func TestHTTPMetricPathsUseRouteTemplates(t *testing.T) {
	br, ctx := regressionBedrock(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := b.HTTPMiddleware(ctx, mux)
	for i := 0; i < 40; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("/users/%d", i), nil))
	}
	fam := regressionFamily(t, br, "http_request_count")
	if len(fam.Metrics) != 1 {
		t.Fatalf("route created %d series, want 1", len(fam.Metrics))
	}
	if fam.Metrics[0].Value != 40 {
		t.Fatalf("count=%v", fam.Metrics[0].Value)
	}
	for _, a := range fam.Metrics[0].Labels.Attrs() {
		if a.Value.String() == "/users/0" {
			t.Fatal("raw path remains a metric label")
		}
	}
}
func TestHTTPUnmatchedPathsShareMetricSeries(t *testing.T) {
	br, ctx := regressionBedrock(t)
	h := b.HTTPMiddleware(ctx, http.NewServeMux())
	for i := 0; i < 40; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("/unknown/%d", i), nil))
	}
	fam := regressionFamily(t, br, "http_request_count")
	if len(fam.Metrics) != 1 || fam.Metrics[0].Value != 40 {
		t.Fatalf("unmatched paths created %d series", len(fam.Metrics))
	}
}
func TestHTTPInformationalResponsesPreserveFinalStatus(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			br, ctx := regressionBedrock(t)
			h := b.HTTPMiddleware(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(103)
				w.WriteHeader(status)
				_, _ = w.Write([]byte("response"))
			}))
			srv := httptest.NewServer(h)
			defer srv.Close()
			resp, err := http.Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != status {
				t.Fatalf("wire status=%d, want %d", resp.StatusCode, status)
			}
			fam := regressionFamily(t, br, "http_request_count")
			if len(fam.Metrics) != 1 {
				t.Fatal("missing request metric")
			}
			value, ok := fam.Metrics[0].Labels.Get("http_status_code")
			if !ok || value.String() != fmt.Sprint(status) {
				t.Fatalf("metric status=%v", value)
			}
		})
	}
}
func TestHTTPPanicsRecordFailureAndPropagate(t *testing.T) {
	br, ctx := regressionBedrock(t)
	sentinel := &struct{}{}
	h := b.HTTPMiddleware(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic(sentinel) }))
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/panic", nil))
	}()
	if recovered != sentinel {
		t.Fatal("middleware changed panic propagation")
	}
	fam := regressionFamily(t, br, "http_request_failures")
	if len(fam.Metrics) != 1 || fam.Metrics[0].Value != 1 {
		t.Fatal("panic did not increment failure counter")
	}
	successes := regressionFamily(t, br, "http_request_successes")
	for _, m := range successes.Metrics {
		if m.Value != 0 {
			t.Fatal("panic counted as success")
		}
	}
}
func TestNoTraceSuppressesHTTPDescendants(t *testing.T) {
	for _, kind := range []string{"client", "do", "get", "post"} {
		t.Run(kind, func(t *testing.T) {
			_, ctx := regressionBedrock(t)
			op, ctx := b.Operation(ctx, "private", b.NoTrace())
			defer op.Done()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("traceparent") != "" {
					t.Error("suppressed request propagated a new trace")
				}
			}))
			defer srv.Close()
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
			var resp *http.Response
			var err error
			switch kind {
			case "client":
				resp, err = b.NewClient(nil).Do(req)
			case "do":
				resp, err = b.Do(ctx, req)
			case "get":
				resp, err = b.Get(ctx, srv.URL)
			case "post":
				resp, err = b.Post(ctx, srv.URL, "text/plain", nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		})
	}
}
func TestOperationMetricSchemaMismatchIsVisible(t *testing.T) {
	_, ctx := regressionBedrock(t)
	op, _ := b.Operation(ctx, "work")
	op.Done()
	defer func() {
		if recover() == nil {
			t.Fatal("conflicting metric schema silently accepted")
		}
	}()
	op, _ = b.Operation(ctx, "work", b.MetricLabels("region"), b.Attrs(attr.String("region", "west")))
	op.Done()
}
func TestInfrastructureResourcesAreNotAutomaticMetricLabels(t *testing.T) {
	br, ctx := regressionBedrock(t, attr.String("service.name", "svc"), attr.String("host.name", "private-host"), attr.String("container.id", "private-container"), attr.Int("process.pid", 123))
	op, _ := b.Operation(ctx, "work")
	op.Done()
	fam := regressionFamily(t, br, "work_count")
	for _, m := range fam.Metrics {
		for _, key := range []string{"host_name", "container_id", "process_pid"} {
			if m.Labels.Has(key) {
				t.Errorf("resource %q copied into metric dimensions", key)
			}
		}
	}
}

func TestOperationMetricSchemaIgnoresLabelOrder(t *testing.T) {
	br, ctx := regressionBedrock(t)
	first, _ := b.Operation(ctx, "ordered", b.MetricLabels("region", "tenant"))
	first.Done()
	second, _ := b.Operation(ctx, "ordered", b.MetricLabels("tenant", "region"))
	second.Done()
	family := regressionFamily(t, br, "ordered_count")
	if len(family.Metrics) != 1 || family.Metrics[0].Value != 2 {
		t.Fatal("equivalent metric schemas diverged")
	}
}
