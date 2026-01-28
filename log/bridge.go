package log

import (
	"context"
	"log/slog"
	"runtime"
	"time"

	"github.com/kzs0/bedrock/attr"
)

// Bridge provides logging functionality that bridges bedrock attributes to slog.
type Bridge struct {
	logger *slog.Logger
}

// NewBridge creates a new Bridge with the given logger.
func NewBridge(logger *slog.Logger) *Bridge {
	return &Bridge{logger: logger}
}

// log logs a message at the given level with bedrock attributes.
// skip is the number of stack frames to skip when determining the source location.
func (b *Bridge) log(ctx context.Context, level slog.Level, skip int, msg string, attrs ...attr.Attr) {
	if !b.logger.Enabled(ctx, level) {
		return
	}

	// Get the caller's PC for source location
	var pcs [1]uintptr
	runtime.Callers(skip, pcs[:])

	// Create record with the correct source location
	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	slogAttrs := AttrsToSlog(attrs)
	r.AddAttrs(slogAttrs...)

	_ = b.logger.Handler().Handle(ctx, r)
}

// Log logs a message at the given level with bedrock attributes.
func (b *Bridge) Log(ctx context.Context, level slog.Level, msg string, attrs ...attr.Attr) {
	// skip: runtime.Callers(1) + log(2) + Log(3) + caller(4)
	b.log(ctx, level, 4, msg, attrs...)
}

// Debug logs a debug message with bedrock attributes.
func (b *Bridge) Debug(ctx context.Context, msg string, attrs ...attr.Attr) {
	// skip: runtime.Callers(1) + log(2) + Debug(3) + caller(4)
	b.log(ctx, slog.LevelDebug, 4, msg, attrs...)
}

// Info logs an info message with bedrock attributes.
func (b *Bridge) Info(ctx context.Context, msg string, attrs ...attr.Attr) {
	// skip: runtime.Callers(1) + log(2) + Info(3) + caller(4)
	b.log(ctx, slog.LevelInfo, 4, msg, attrs...)
}

// Warn logs a warning message with bedrock attributes.
func (b *Bridge) Warn(ctx context.Context, msg string, attrs ...attr.Attr) {
	// skip: runtime.Callers(1) + log(2) + Warn(3) + caller(4)
	b.log(ctx, slog.LevelWarn, 4, msg, attrs...)
}

// Error logs an error message with bedrock attributes.
func (b *Bridge) Error(ctx context.Context, msg string, attrs ...attr.Attr) {
	// skip: runtime.Callers(1) + log(2) + Error(3) + caller(4)
	b.log(ctx, slog.LevelError, 4, msg, attrs...)
}

// With returns a new Bridge with the given attributes added.
func (b *Bridge) With(attrs ...attr.Attr) *Bridge {
	slogAttrs := make([]any, 0, len(attrs)*2)
	for _, a := range attrs {
		slogAttrs = append(slogAttrs, AttrToSlog(a))
	}
	return &Bridge{logger: b.logger.With(slogAttrs...)}
}

// WithGroup returns a new Bridge with the given group name.
func (b *Bridge) WithGroup(name string) *Bridge {
	return &Bridge{logger: b.logger.WithGroup(name)}
}

// Logger returns the underlying slog.Logger.
func (b *Bridge) Logger() *slog.Logger {
	return b.logger
}
