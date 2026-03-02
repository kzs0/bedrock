package otlp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

func TestNewExporter(t *testing.T) {
	e := NewExporter(ExporterConfig{
		Endpoint:    "http://localhost:4318/v1/traces",
		ServiceName: "test",
	})
	if e == nil {
		t.Fatal("NewExporter returned nil")
	}
	if e.cfg.Timeout != 10*time.Second {
		t.Errorf("expected default timeout 10s, got %v", e.cfg.Timeout)
	}
}

func TestNewExporter_CustomTimeout(t *testing.T) {
	e := NewExporter(ExporterConfig{
		Endpoint: "http://localhost:4318/v1/traces",
		Timeout:  30 * time.Second,
	})
	if e.cfg.Timeout != 30*time.Second {
		t.Errorf("expected custom timeout 30s, got %v", e.cfg.Timeout)
	}
}

func TestExporter_ExportSpans(t *testing.T) {
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := NewExporter(ExporterConfig{
		Endpoint:    srv.URL,
		ServiceName: "test",
	})

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test-op")
	span.End()

	err := e.ExportSpans(context.Background(), []*trace.Span{span})
	if err != nil {
		t.Fatalf("ExportSpans error: %v", err)
	}
	if received.Load() != 1 {
		t.Errorf("expected 1 request, got %d", received.Load())
	}
}

func TestExporter_ExportSpans_Empty(t *testing.T) {
	e := NewExporter(ExporterConfig{
		Endpoint: "http://localhost:4318/v1/traces",
	})

	err := e.ExportSpans(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExportSpans with nil spans should succeed: %v", err)
	}
}

func TestExporter_ExportSpans_WithHeaders(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := NewExporter(ExporterConfig{
		Endpoint: srv.URL,
		Headers:  map[string]string{"Authorization": "Bearer token123"},
	})

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	span.End()

	err := e.ExportSpans(context.Background(), []*trace.Span{span})
	if err != nil {
		t.Fatalf("ExportSpans error: %v", err)
	}
	if receivedAuth != "Bearer token123" {
		t.Errorf("expected auth header 'Bearer token123', got %q", receivedAuth)
	}
}

func TestExporter_ExportSpans_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	e := NewExporter(ExporterConfig{
		Endpoint: srv.URL,
	})

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	span.End()

	err := e.ExportSpans(context.Background(), []*trace.Span{span})
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestExporter_Shutdown(t *testing.T) {
	e := NewExporter(ExporterConfig{
		Endpoint: "http://localhost:4318/v1/traces",
	})

	err := e.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("Shutdown error: %v", err)
	}

	// After shutdown, ExportSpans should be a noop
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	span.End()

	err = e.ExportSpans(context.Background(), []*trace.Span{span})
	if err != nil {
		t.Fatalf("ExportSpans after Shutdown should succeed (noop): %v", err)
	}
}

func TestExporter_ExportSpans_WithResourceAttrs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := NewExporter(ExporterConfig{
		Endpoint:    srv.URL,
		ServiceName: "test",
		Resource:    attr.NewSet(attr.String("env", "prod")),
	})

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	span.End()

	err := e.ExportSpans(context.Background(), []*trace.Span{span})
	if err != nil {
		t.Fatalf("ExportSpans error: %v", err)
	}
}
