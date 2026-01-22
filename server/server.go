// Package server provides an HTTP server for observability endpoints.
// This includes metrics, health checks, and pprof profiling endpoints.
// Most users won't need to use this package directly as the observability server
// is automatically started by bedrock.Init() when Config.ServerEnabled is true.
package server

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/kzs0/bedrock/metric"
	"github.com/kzs0/bedrock/metric/prometheus"
	"github.com/kzs0/bedrock/profile"
)

// Server provides HTTP endpoints for metrics and profiling.
type Server struct {
	metrics         *metric.Registry
	server          *http.Server
	mux             *http.ServeMux
	shutdownTimeout time.Duration
}

// Config configures the observability HTTP server.
type Config struct {
	// Addr is the address to listen on (e.g., ":9090").
	Addr string
	// EnableMetrics enables the /metrics endpoint.
	EnableMetrics bool
	// EnablePprof enables the /debug/pprof endpoints.
	EnablePprof bool

	// HTTP Protection Settings

	// ReadTimeout is the maximum duration for reading the entire request,
	// including the body. A zero or negative value means no timeout.
	// Default: 10 seconds
	ReadTimeout time.Duration

	// ReadHeaderTimeout is the amount of time allowed to read request headers.
	// This should be set to protect against slow-loris attacks.
	// Default: 5 seconds
	ReadHeaderTimeout time.Duration

	// WriteTimeout is the maximum duration before timing out writes of the response.
	// This includes processing time. A zero or negative value means no timeout.
	// Default: 30 seconds
	WriteTimeout time.Duration

	// IdleTimeout is the maximum amount of time to wait for the next request
	// when keep-alives are enabled. If IdleTimeout is zero, ReadTimeout is used.
	// Default: 120 seconds
	IdleTimeout time.Duration

	// MaxHeaderBytes controls the maximum number of bytes the server will
	// read parsing the request header's keys and values, including the
	// request line. It does not limit the size of the request body.
	// Default: 1 MB (1 << 20)
	MaxHeaderBytes int

	// ShutdownTimeout is the maximum duration to wait for graceful shutdown.
	// Default: 30 seconds
	ShutdownTimeout time.Duration
}

// DefaultConfig returns a default server configuration with
// production-grade security settings to protect against DoS attacks.
func DefaultConfig() Config {
	return Config{
		Addr:          ":9090",
		EnableMetrics: true,
		EnablePprof:   true,

		// DoS Protection Defaults
		ReadTimeout:       10 * time.Second,  // Total request read timeout
		ReadHeaderTimeout: 5 * time.Second,   // Protect against slow-loris attacks
		WriteTimeout:      30 * time.Second,  // Response write timeout
		IdleTimeout:       120 * time.Second, // Keep-alive timeout
		MaxHeaderBytes:    1 << 20,           // 1 MB header limit
		ShutdownTimeout:   30 * time.Second,  // Graceful shutdown timeout
	}
}

// New creates a new observability HTTP server.
//
// Usage:
//
//	import "github.com/kzs0/bedrock/server"
//
//	cfg := server.DefaultConfig()
//	cfg.Addr = ":8080"
//	obsServer := server.New(b.Metrics(), cfg)
//	go obsServer.ListenAndServe()
func New(metrics *metric.Registry, cfg Config) *Server {
	mux := http.NewServeMux()

	if cfg.EnableMetrics {
		mux.Handle("/metrics", prometheus.Handler(metrics))
	}

	if cfg.EnablePprof {
		profile.RegisterHandlers(mux)
	}

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Ready check endpoint
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Apply timeout defaults if not set
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 10 * time.Second
	}
	if cfg.ReadHeaderTimeout == 0 {
		cfg.ReadHeaderTimeout = 5 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 30 * time.Second
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 120 * time.Second
	}
	if cfg.MaxHeaderBytes == 0 {
		cfg.MaxHeaderBytes = 1 << 20 // 1 MB
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = 30 * time.Second
	}

	return &Server{
		metrics:         metrics,
		mux:             mux,
		shutdownTimeout: cfg.ShutdownTimeout,
		server: &http.Server{
			Addr:    cfg.Addr,
			Handler: mux,

			// Security timeouts to prevent DoS attacks
			ReadTimeout:       cfg.ReadTimeout,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			MaxHeaderBytes:    cfg.MaxHeaderBytes,
		},
	}
}

// ListenAndServe starts the server.
func (s *Server) ListenAndServe() error {
	return s.server.ListenAndServe()
}

// Serve starts the server on an existing listener.
func (s *Server) Serve(ln net.Listener) error {
	return s.server.Serve(ln)
}

// Shutdown gracefully shuts down the server.
// If the provided context does not have a deadline, a timeout context
// is created using the configured ShutdownTimeout.
func (s *Server) Shutdown(ctx context.Context) error {
	// If no deadline set, apply shutdown timeout
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && s.shutdownTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.shutdownTimeout)
		defer cancel()
	}
	return s.server.Shutdown(ctx)
}

// Handler returns the HTTP handler for use with custom servers.
func (s *Server) Handler() http.Handler {
	return s.mux
}
