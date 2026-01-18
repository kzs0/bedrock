package bedrock

import (
	"context"
	"net"
	"net/http"

	"github.com/kzs0/bedrock/metric/prometheus"
	"github.com/kzs0/bedrock/profile"
)

// Server provides HTTP endpoints for metrics and profiling.
type Server struct {
	bedrock *Bedrock
	server  *http.Server
	mux     *http.ServeMux
}

// ServerConfig configures the observability HTTP server.
type ServerConfig struct {
	// Addr is the address to listen on (e.g., ":9090").
	Addr string
	// EnableMetrics enables the /metrics endpoint.
	EnableMetrics bool
	// EnablePprof enables the /debug/pprof endpoints.
	EnablePprof bool
}

// DefaultServerConfig returns a default server configuration.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Addr:          ":9090",
		EnableMetrics: true,
		EnablePprof:   true,
	}
}

// NewServer creates a new observability HTTP server.
func (b *Bedrock) NewServer(cfg ServerConfig) *Server {
	mux := http.NewServeMux()

	if cfg.EnableMetrics {
		mux.Handle("/metrics", prometheus.Handler(b.metrics))
	}

	if cfg.EnablePprof {
		profile.RegisterHandlers(mux)
	}

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Ready check endpoint
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	return &Server{
		bedrock: b,
		mux:     mux,
		server: &http.Server{
			Addr:    cfg.Addr,
			Handler: mux,
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
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// Handler returns the HTTP handler for use with custom servers.
func (s *Server) Handler() http.Handler {
	return s.mux
}
