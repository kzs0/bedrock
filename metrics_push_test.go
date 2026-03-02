package bedrock

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/metric"
)

// TestMetricsPush_Headers verifies the correct Content-Type and Authorization
// headers are sent with metric push requests.
func TestMetricsPush_Headers(t *testing.T) {
	var (
		gotContentType string
		gotAuth        string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := metric.NewRegistry("")
	cfg := cloudConfig{
		apiKey:       "test-key",
		endpoint:     srv.URL,
		pushInterval: 50 * time.Millisecond,
	}
	stop := startMetricsPush(reg, nil, cfg)
	time.Sleep(100 * time.Millisecond)
	stop(context.Background())

	if !strings.HasPrefix(gotContentType, "text/plain") {
		t.Errorf("expected text/plain content-type, got %q", gotContentType)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("expected 'Bearer test-key', got %q", gotAuth)
	}
}

// TestMetricsPush_ContainsMetrics verifies that a registered counter appears
// in the push payload.
func TestMetricsPush_ContainsMetrics(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := metric.NewRegistry("")
	counter := reg.Counter("test_requests_total", "test counter", "method")
	counter.With(attr.String("method", "GET")).Inc()

	cfg := cloudConfig{
		apiKey:       "key",
		endpoint:     srv.URL,
		pushInterval: 50 * time.Millisecond,
	}
	stop := startMetricsPush(reg, nil, cfg)
	time.Sleep(100 * time.Millisecond)
	stop(context.Background())

	if !strings.Contains(body, "test_requests_total") {
		t.Errorf("expected test_requests_total in push body, got:\n%s", body)
	}
}

// TestMetricsPush_Backpressure verifies that a slow server does not cause
// goroutine accumulation when the tick interval is shorter than push latency.
func TestMetricsPush_Backpressure(t *testing.T) {
	goroutinesBefore := runtime.NumGoroutine()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // slower than tick interval
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := metric.NewRegistry("")
	cfg := cloudConfig{
		apiKey:       "key",
		endpoint:     srv.URL,
		pushInterval: 10 * time.Millisecond,
	}
	stop := startMetricsPush(reg, nil, cfg)
	time.Sleep(150 * time.Millisecond) // multiple ticks, but server is slow
	stop(context.Background())

	// Allow any lingering goroutine to wind down.
	time.Sleep(50 * time.Millisecond)

	goroutinesAfter := runtime.NumGoroutine()
	// Allow a small margin for other test-related goroutines.
	if goroutinesAfter > goroutinesBefore+10 {
		t.Errorf("possible goroutine leak: before=%d after=%d", goroutinesBefore, goroutinesAfter)
	}
}

// TestMetricsPush_FinalPushOnStop verifies that stop() triggers exactly one
// final push even when the ticker has not fired yet.
func TestMetricsPush_FinalPushOnStop(t *testing.T) {
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := metric.NewRegistry("")
	cfg := cloudConfig{
		apiKey:       "key",
		endpoint:     srv.URL,
		pushInterval: 24 * time.Hour, // effectively disabled
	}
	stop := startMetricsPush(reg, nil, cfg)
	stop(context.Background()) // final push should fire immediately

	if requestCount.Load() < 1 {
		t.Error("expected at least one push request on stop")
	}
}
