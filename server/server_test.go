package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kzs0/bedrock/metric"
)

// newTestServer creates a Server with the given config and uses its Handler()
// to avoid actually binding a port.
func newTestServer(cfg Config) *Server {
	reg := metric.NewRegistry("")
	return New(reg, cfg)
}

// ── DefaultConfig tests ───────────────────────────────────────────────────────

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Addr != ":9090" {
		t.Errorf("Addr: got %q, want :9090", cfg.Addr)
	}
	if !cfg.EnableMetrics {
		t.Error("EnableMetrics should default to true")
	}
	if !cfg.EnablePprof {
		t.Error("EnablePprof should default to true")
	}
	if cfg.ReadTimeout != 10*time.Second {
		t.Errorf("ReadTimeout: got %v, want 10s", cfg.ReadTimeout)
	}
	if cfg.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout: got %v, want 5s", cfg.ReadHeaderTimeout)
	}
	if cfg.WriteTimeout != 30*time.Second {
		t.Errorf("WriteTimeout: got %v, want 30s", cfg.WriteTimeout)
	}
	if cfg.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout: got %v, want 120s", cfg.IdleTimeout)
	}
	if cfg.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes: got %d, want %d", cfg.MaxHeaderBytes, 1<<20)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout: got %v, want 30s", cfg.ShutdownTimeout)
	}
}

func TestNew_ZeroTimeoutsGetDefaults(t *testing.T) {
	reg := metric.NewRegistry("")
	srv := New(reg, Config{}) // all zero values
	// We verify indirectly by checking the server doesn't panic
	if srv == nil {
		t.Fatal("New returned nil")
	}
	if srv.server.ReadTimeout != 10*time.Second {
		t.Errorf("ReadTimeout not defaulted: got %v", srv.server.ReadTimeout)
	}
	if srv.server.WriteTimeout != 30*time.Second {
		t.Errorf("WriteTimeout not defaulted: got %v", srv.server.WriteTimeout)
	}
	if srv.server.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout not defaulted: got %v", srv.server.IdleTimeout)
	}
	if srv.server.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes not defaulted: got %d", srv.server.MaxHeaderBytes)
	}
}

// ── Endpoint tests (via Handler()) ───────────────────────────────────────────

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer(DefaultConfig())
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body: got %q, want ok", rec.Body.String())
	}
}

func TestReadyEndpoint(t *testing.T) {
	srv := newTestServer(DefaultConfig())
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body: got %q, want ok", rec.Body.String())
	}
}

func TestMetricsEndpoint_Enabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableMetrics = true
	srv := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type: got %q, want text/plain prefix", ct)
	}
}

func TestMetricsEndpoint_Disabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableMetrics = false
	srv := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	// With metrics disabled, the mux returns 404
	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 (metrics disabled)", rec.Code)
	}
}

func TestPprofEndpoint_Enabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnablePprof = true
	srv := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
}

func TestPprofEndpoint_Disabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnablePprof = false
	srv := newTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 (pprof disabled)", rec.Code)
	}
}

// ── Serve / Shutdown tests ────────────────────────────────────────────────────

func TestServe_And_Shutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	reg := metric.NewRegistry("")
	srv := New(reg, Config{EnableMetrics: true})

	// Start serving in background
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	// Give the server a moment to start
	addr := ln.Addr().String()
	var resp *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/health")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body: got %q, want ok", string(body))
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	// Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}

	// Server goroutine should have exited
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("server did not stop after Shutdown")
	}
}

func TestShutdown_WithDeadlineContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	reg := metric.NewRegistry("")
	srv := New(reg, Config{})

	go func() { _ = srv.Serve(ln) }()
	time.Sleep(20 * time.Millisecond)

	// Shutdown with a context that already has a deadline
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(5*time.Second))
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown with deadline context: %v", err)
	}
}

func TestShutdown_UsesConfiguredTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	reg := metric.NewRegistry("")
	srv := New(reg, Config{ShutdownTimeout: 1 * time.Second})

	go func() { _ = srv.Serve(ln) }()
	time.Sleep(20 * time.Millisecond)

	// Shutdown with background context (no deadline) — should use ShutdownTimeout
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestHandler_NotNil(t *testing.T) {
	srv := newTestServer(DefaultConfig())
	if srv.Handler() == nil {
		t.Error("Handler() should not return nil")
	}
}
