package server

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/kzs0/bedrock/metric"
)

func TestListenAndServe(t *testing.T) {
	r := metric.NewRegistry("")
	cfg := Config{
		EnableMetrics:     true,
		EnablePprof:       true,
		ReadTimeout:       time.Second,
		ReadHeaderTimeout: time.Second,
		WriteTimeout:      time.Second,
		IdleTimeout:       time.Second,
		MaxHeaderBytes:    1 << 20,
		ShutdownTimeout:   time.Second,
	}

	srv := New(r, cfg)

	// Use a real listener on loopback to test Serve
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ln)
	}()

	// Poll until server is ready
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
		t.Fatalf("health check error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected health 200, got %d", resp.StatusCode)
	}

	// Test ready endpoint
	resp, err = http.Get("http://" + addr + "/ready")
	if err != nil {
		t.Fatalf("ready check error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected ready 200, got %d", resp.StatusCode)
	}

	// Test metrics endpoint
	resp, err = http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("metrics error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected metrics 200, got %d", resp.StatusCode)
	}

	// Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = srv.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown error: %v", err)
	}
}

func TestListenAndServe_Method(t *testing.T) {
	r := metric.NewRegistry("")
	// Use a high random port for ListenAndServe
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen error: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // Free the port

	cfg := Config{
		Addr:          addr,
		EnableMetrics: true,
	}
	srv := New(r, cfg)

	done := make(chan error, 1)
	go func() {
		done <- srv.ListenAndServe()
	}()

	// Poll until server is ready
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, getErr := http.Get("http://" + addr + "/health")
		if getErr == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown error: %v", err)
	}
}
