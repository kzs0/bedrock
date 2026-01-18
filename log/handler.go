package log

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/kzs0/bedrock/attr"
)

// Handler is a custom slog.Handler that injects trace context into logs.
type Handler struct {
	inner       slog.Handler
	attrs       []slog.Attr
	groups      []string
	getTraceCtx func(ctx context.Context) (traceID, spanID string)
}

// HandlerOptions configures the Handler.
type HandlerOptions struct {
	// Level is the minimum log level to output.
	Level slog.Leveler
	// AddSource adds source code position to log output.
	AddSource bool
	// Output is the writer to write logs to. Defaults to os.Stderr.
	Output io.Writer
	// Format is the output format ("json" or "text"). Defaults to "json".
	Format string
}

// NewHandler creates a new Handler with the given options.
func NewHandler(opts *HandlerOptions) *Handler {
	if opts == nil {
		opts = &HandlerOptions{}
	}

	output := opts.Output
	if output == nil {
		output = os.Stderr
	}

	var inner slog.Handler
	handlerOpts := &slog.HandlerOptions{
		Level:     opts.Level,
		AddSource: opts.AddSource,
	}

	if opts.Format == "text" {
		inner = slog.NewTextHandler(output, handlerOpts)
	} else {
		inner = slog.NewJSONHandler(output, handlerOpts)
	}

	return &Handler{
		inner: inner,
	}
}

// SetTraceContextFunc sets the function used to extract trace context from context.
func (h *Handler) SetTraceContextFunc(fn func(ctx context.Context) (traceID, spanID string)) {
	h.getTraceCtx = fn
}

// Enabled reports whether the handler handles records at the given level.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle handles the Record.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	// Inject trace context if available
	if h.getTraceCtx != nil {
		traceID, spanID := h.getTraceCtx(ctx)
		if traceID != "" {
			r.AddAttrs(slog.String("trace_id", traceID))
		}
		if spanID != "" {
			r.AddAttrs(slog.String("span_id", spanID))
		}
	}

	// Add handler-level attributes
	for _, a := range h.attrs {
		r.AddAttrs(a)
	}

	return h.inner.Handle(ctx, r)
}

// WithAttrs returns a new Handler with the given attributes added.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs), len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	newAttrs = append(newAttrs, attrs...)

	return &Handler{
		inner:       h.inner.WithAttrs(attrs),
		attrs:       newAttrs,
		groups:      h.groups,
		getTraceCtx: h.getTraceCtx,
	}
}

// WithGroup returns a new Handler with the given group name.
func (h *Handler) WithGroup(name string) slog.Handler {
	newGroups := make([]string, len(h.groups), len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups = append(newGroups, name)

	return &Handler{
		inner:       h.inner.WithGroup(name),
		attrs:       h.attrs,
		groups:      newGroups,
		getTraceCtx: h.getTraceCtx,
	}
}

// AttrToSlog converts a bedrock attr.Attr to a slog.Attr.
func AttrToSlog(a attr.Attr) slog.Attr {
	switch a.Value.Kind() {
	case attr.KindString:
		return slog.String(a.Key, a.Value.AsString())
	case attr.KindInt64:
		return slog.Int64(a.Key, a.Value.AsInt64())
	case attr.KindUint64:
		return slog.Uint64(a.Key, a.Value.AsUint64())
	case attr.KindFloat64:
		return slog.Float64(a.Key, a.Value.AsFloat64())
	case attr.KindBool:
		return slog.Bool(a.Key, a.Value.AsBool())
	case attr.KindDuration:
		return slog.Duration(a.Key, a.Value.AsDuration())
	case attr.KindTime:
		return slog.Time(a.Key, a.Value.AsTime())
	default:
		return slog.Any(a.Key, a.Value.AsAny())
	}
}

// AttrsToSlog converts a slice of bedrock attrs to slog attrs.
func AttrsToSlog(attrs []attr.Attr) []slog.Attr {
	slogAttrs := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		slogAttrs[i] = AttrToSlog(a)
	}
	return slogAttrs
}
