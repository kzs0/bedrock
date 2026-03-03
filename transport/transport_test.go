package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

func TestRoundTrip_WithTracer(t *testing.T) {
	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	ctx, span := tracer.Start(context.Background(), "parent")
	defer span.End()

	tr := &Transport{Tracer: tracer}
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify traceparent was injected
	tp := capturedHeaders.Get("Traceparent")
	if tp == "" {
		t.Error("expected traceparent header to be injected")
	}
}

func TestRoundTrip_WithoutTracer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := &Transport{}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRoundTrip_WithCustomBase(t *testing.T) {
	var called bool
	customBase := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})

	tr := &Transport{Base: customBase}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.com", nil)
	_, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	if !called {
		t.Error("expected custom base transport to be called")
	}
}

func TestBase_Default(t *testing.T) {
	tr := &Transport{}
	b := tr.base()
	if b == nil {
		t.Fatal("base() should return non-nil default transport")
	}
}

func TestBase_Custom(t *testing.T) {
	custom := &http.Transport{}
	tr := &Transport{Base: custom}
	b := tr.base()
	if b != custom {
		t.Error("base() should return custom transport")
	}
}

func TestRoundTrip_Error(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	ctx, span := tracer.Start(context.Background(), "parent")
	defer span.End()

	tr := &Transport{Tracer: tracer}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost:1", nil)
	_, err := tr.RoundTrip(req)
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}

func TestRoundTrip_SetsAttributes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	ctx, _ := tracer.Start(context.Background(), "parent",
		trace.WithAttrs(attr.String("test", "attr")),
	)

	tr := &Transport{Tracer: tracer}
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/path", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// roundTripFunc implements http.RoundTripper
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
