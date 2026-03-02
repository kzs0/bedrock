package prometheus

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/metric"
)

func TestHandler_Metrics(t *testing.T) {
	r := metric.NewRegistry("")
	c := r.Counter("http_requests_total", "Total requests", "method")
	c.With(attr.String("method", "GET")).Inc()
	c.With(attr.String("method", "GET")).Inc()

	handler := Handler(r)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %q", ct)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "http_requests_total") {
		t.Error("expected metric in response body")
	}
}

func TestHandler_EmptyRegistryResponse(t *testing.T) {
	r := metric.NewRegistry("")

	handler := Handler(r)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestHandler_Gauge(t *testing.T) {
	r := metric.NewRegistry("")
	g := r.Gauge("temperature", "Current temp")
	g.Set(42.5)

	handler := Handler(r)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "temperature") {
		t.Error("expected temperature gauge in response")
	}
	if !strings.Contains(body, "42.5") {
		t.Error("expected gauge value in response")
	}
}

func TestHandler_Histogram(t *testing.T) {
	r := metric.NewRegistry("")
	h := r.Histogram("latency", "Latency", []float64{10, 50, 100})
	h.Observe(25)
	h.Observe(75)

	handler := Handler(r)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "latency") {
		t.Error("expected histogram in response")
	}
}
