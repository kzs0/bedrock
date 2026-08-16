package bedrock

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
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
		var opOpts []OperationOption
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
		handler.ServeHTTP(wrapResponseWriter(rw), r.WithContext(opCtx))

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

// Unwrap exposes the underlying writer to http.ResponseController.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

type flushErrorWriter interface {
	FlushError() error
}

type responseFlusher struct {
	base         *responseWriter
	flushErrorer flushErrorWriter
	flusher      http.Flusher
}

func (f *responseFlusher) Flush() {
	_ = f.FlushError()
}

func (f *responseFlusher) FlushError() error {
	if f.flushErrorer != nil {
		err := f.flushErrorer.FlushError()
		if err == nil && !f.base.wroteHeader {
			// FlushError has already committed the underlying response. Capture
			// the implicit status without writing a duplicate header.
			f.base.status = http.StatusOK
			f.base.wroteHeader = true
		}
		return err
	}
	if !f.base.wroteHeader {
		f.base.WriteHeader(http.StatusOK)
	}
	f.flusher.Flush()
	return nil
}

type responseHijacker struct {
	hijacker http.Hijacker
}

func (h *responseHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.hijacker.Hijack()
}

type responsePusher struct {
	pusher http.Pusher
}

func (p *responsePusher) Push(target string, opts *http.PushOptions) error {
	return p.pusher.Push(target, opts)
}

type responseReaderFrom struct {
	base       *responseWriter
	readerFrom io.ReaderFrom
}

func (rf *responseReaderFrom) ReadFrom(r io.Reader) (int64, error) {
	if !rf.base.wroteHeader {
		rf.base.WriteHeader(http.StatusOK)
	}
	return rf.readerFrom.ReadFrom(r)
}

// findResponseCapability follows the same Unwrap convention as
// http.ResponseController so nested wrappers retain their capabilities.
func findResponseCapability[T any](w http.ResponseWriter) (T, bool) {
	var zero T
	for w != nil {
		if capability, ok := any(w).(T); ok {
			return capability, true
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return zero, false
		}
		w = unwrapper.Unwrap()
	}
	return zero, false
}

// findResponseFlushCapability walks one layer at a time, matching
// ResponseController precedence: FlushError, then Flusher, then Unwrap.
func findResponseFlushCapability(w http.ResponseWriter) (flushErrorWriter, http.Flusher, bool) {
	for w != nil {
		if flushErrorer, ok := w.(flushErrorWriter); ok {
			return flushErrorer, nil, true
		}
		if flusher, ok := w.(http.Flusher); ok {
			return nil, flusher, true
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil, nil, false
		}
		w = unwrapper.Unwrap()
	}
	return nil, nil, false
}

// wrapResponseWriter exposes exactly the optional capabilities supported by
// the underlying writer chain. This keeps legacy interface assertions honest
// while the embedded responseWriter preserves status capture and Unwrap.
func wrapResponseWriter(base *responseWriter) http.ResponseWriter {
	flushErrorer, flusher, hasFlush := findResponseFlushCapability(base.ResponseWriter)
	hijacker, hasHijacker := findResponseCapability[http.Hijacker](base.ResponseWriter)
	// Pusher and ReaderFrom are not ResponseController capabilities. Preserve
	// only the immediate writer's interfaces so intermediate wrappers are not
	// bypassed when pushing or copying a response body.
	pusher, hasPusher := base.ResponseWriter.(http.Pusher)
	readerFrom, hasReaderFrom := base.ResponseWriter.(io.ReaderFrom)

	var flush *responseFlusher
	if hasFlush {
		flush = &responseFlusher{base: base, flushErrorer: flushErrorer, flusher: flusher}
	}
	var hijack *responseHijacker
	if hasHijacker {
		hijack = &responseHijacker{hijacker: hijacker}
	}
	var push *responsePusher
	if hasPusher {
		push = &responsePusher{pusher: pusher}
	}
	var readFrom *responseReaderFrom
	if hasReaderFrom {
		readFrom = &responseReaderFrom{base: base, readerFrom: readerFrom}
	}

	capabilities := 0
	if flush != nil {
		capabilities |= 1
	}
	if hijack != nil {
		capabilities |= 2
	}
	if push != nil {
		capabilities |= 4
	}
	if readFrom != nil {
		capabilities |= 8
	}

	switch capabilities {
	case 0:
		return base
	case 1:
		return struct {
			*responseWriter
			*responseFlusher
		}{base, flush}
	case 2:
		return struct {
			*responseWriter
			*responseHijacker
		}{base, hijack}
	case 3:
		return struct {
			*responseWriter
			*responseFlusher
			*responseHijacker
		}{base, flush, hijack}
	case 4:
		return struct {
			*responseWriter
			*responsePusher
		}{base, push}
	case 5:
		return struct {
			*responseWriter
			*responseFlusher
			*responsePusher
		}{base, flush, push}
	case 6:
		return struct {
			*responseWriter
			*responseHijacker
			*responsePusher
		}{base, hijack, push}
	case 7:
		return struct {
			*responseWriter
			*responseFlusher
			*responseHijacker
			*responsePusher
		}{base, flush, hijack, push}
	case 8:
		return struct {
			*responseWriter
			*responseReaderFrom
		}{base, readFrom}
	case 9:
		return struct {
			*responseWriter
			*responseFlusher
			*responseReaderFrom
		}{base, flush, readFrom}
	case 10:
		return struct {
			*responseWriter
			*responseHijacker
			*responseReaderFrom
		}{base, hijack, readFrom}
	case 11:
		return struct {
			*responseWriter
			*responseFlusher
			*responseHijacker
			*responseReaderFrom
		}{base, flush, hijack, readFrom}
	case 12:
		return struct {
			*responseWriter
			*responsePusher
			*responseReaderFrom
		}{base, push, readFrom}
	case 13:
		return struct {
			*responseWriter
			*responseFlusher
			*responsePusher
			*responseReaderFrom
		}{base, flush, push, readFrom}
	case 14:
		return struct {
			*responseWriter
			*responseHijacker
			*responsePusher
			*responseReaderFrom
		}{base, hijack, push, readFrom}
	default:
		return struct {
			*responseWriter
			*responseFlusher
			*responseHijacker
			*responsePusher
			*responseReaderFrom
		}{base, flush, hijack, push, readFrom}
	}
}
