package bedrock

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kzs0/bedrock/attr"
	httpProp "github.com/kzs0/bedrock/trace/http"
)

// HTTPMiddleware wraps an HTTP handler with bedrock operations.
// It expects bedrock to already be in the context (use Init or WithBedrock first).
//
// Usage:
//
//	ctx, close := bedrock.Init(ctx)
//	defer close()
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("/users", handleUsers)
//
//	handler := bedrock.HTTPMiddleware(ctx, mux)
//	http.ListenAndServe(":8080", handler)
func HTTPMiddleware(ctx context.Context, handler http.Handler, opts ...MiddlewareOption) http.Handler {
	cfg := applyMiddlewareOptions(opts)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Build initial attributes
		attrs := []attr.Attr{
			attr.String("http.method", r.Method),
			attr.String("http.path", r.URL.Path),
			attr.String("http.scheme", r.URL.Scheme),
			attr.String("http.host", r.Host),
			attr.String("http.user_agent", r.UserAgent()),
		}

		// Add custom attributes if provided
		if cfg.additionalAttrs != nil {
			attrs = append(attrs, cfg.additionalAttrs(r)...)
		}

		// Build metric labels
		labels := []string{"http.method", "http.path", "http.status_code"}
		labels = append(labels, cfg.additionalLabels...)

		// Start operation with the request context
		// Add bedrock from base context if not already present
		reqCtx := r.Context()
		baseBedrock := bedrockFromContext(ctx)

		// Add bedrock to request context if not present (preserves other context values)
		if bedrockFromContext(reqCtx).isNoop && baseBedrock != nil && !baseBedrock.isNoop {
			reqCtx = WithBedrock(reqCtx, baseBedrock)
		}

		// Extract W3C Trace Context from headers if trace propagation is enabled
		var opOpts []any
		opOpts = append(opOpts, Attrs(attrs...))
		opOpts = append(opOpts, MetricLabels(labels...))

		if cfg.tracePropagation {
			prop := &httpProp.Propagator{}
			remoteCtx, err := prop.Extract(r.Header)
			if err == nil && remoteCtx.IsValid() {
				// Start operation with remote parent context
				opOpts = append(opOpts, WithRemoteParent(remoteCtx))
			}
		}

		op, opCtx := Operation(reqCtx, cfg.operationName, opOpts...)
		defer op.Done()

		// Wrap response writer to capture status code
		rw := &responseWriter{
			ResponseWriter: w,
			status:         http.StatusOK,
			wroteHeader:    false,
		}

		// Call next handler with operation context
		handler.ServeHTTP(rw, r.WithContext(opCtx))

		// Add status code as attribute
		op.Register(opCtx, attr.Int("http.status_code", rw.status))

		// Register failure if error status
		if cfg.successStatusCodes != nil {
			if !cfg.successStatusCodes[rw.status] {
				op.Register(opCtx, attr.Error(fmt.Errorf("HTTP %d", rw.status)))
			}
		} else {
			// Default: 4xx and 5xx are failures
			if rw.status >= 400 {
				op.Register(opCtx, attr.Error(fmt.Errorf("HTTP %d", rw.status)))
			}
		}
	})
}

// MiddlewareOption configures the HTTP middleware.
type MiddlewareOption func(*middlewareConfig)

// middlewareConfig holds HTTP middleware configuration.
type middlewareConfig struct {
	operationName      string
	additionalLabels   []string
	additionalAttrs    func(*http.Request) []attr.Attr
	successStatusCodes map[int]bool
	tracePropagation   bool
}

// WithOperationName sets a custom operation name (default: "http.request").
func WithOperationName(name string) MiddlewareOption {
	return func(cfg *middlewareConfig) {
		cfg.operationName = name
	}
}

// WithAdditionalLabels adds extra metric label names beyond the defaults.
// Default labels are: method, path, status_code
func WithAdditionalLabels(labels ...string) MiddlewareOption {
	return func(cfg *middlewareConfig) {
		cfg.additionalLabels = append(cfg.additionalLabels, labels...)
	}
}

// WithAdditionalAttrs provides a function to extract additional attributes from the request.
func WithAdditionalAttrs(fn func(*http.Request) []attr.Attr) MiddlewareOption {
	return func(cfg *middlewareConfig) {
		cfg.additionalAttrs = fn
	}
}

// WithSuccessCodes defines which HTTP status codes are considered successful.
// Default: 2xx and 3xx are success, 4xx and 5xx are failures.
func WithSuccessCodes(codes ...int) MiddlewareOption {
	return func(cfg *middlewareConfig) {
		cfg.successStatusCodes = make(map[int]bool)
		for _, code := range codes {
			cfg.successStatusCodes[code] = true
		}
	}
}

// WithTracePropagation enables or disables W3C Trace Context propagation.
// Default: enabled (true).
func WithTracePropagation(enable bool) MiddlewareOption {
	return func(cfg *middlewareConfig) {
		cfg.tracePropagation = enable
	}
}

// applyMiddlewareOptions applies middleware options.
func applyMiddlewareOptions(opts []MiddlewareOption) middlewareConfig {
	cfg := middlewareConfig{
		operationName:      "http.request",
		additionalLabels:   make([]string, 0),
		successStatusCodes: nil,
		tracePropagation:   true, // Default: enabled
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.status = code
		rw.wroteHeader = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}
