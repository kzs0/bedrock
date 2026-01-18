package log

import (
	"context"
	"log/slog"

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

// Log logs a message at the given level with bedrock attributes.
func (b *Bridge) Log(ctx context.Context, level slog.Level, msg string, attrs ...attr.Attr) {
	if !b.logger.Enabled(ctx, level) {
		return
	}
	slogAttrs := AttrsToSlog(attrs)
	b.logger.LogAttrs(ctx, level, msg, slogAttrs...)
}

// Debug logs a debug message with bedrock attributes.
func (b *Bridge) Debug(ctx context.Context, msg string, attrs ...attr.Attr) {
	b.Log(ctx, slog.LevelDebug, msg, attrs...)
}

// Info logs an info message with bedrock attributes.
func (b *Bridge) Info(ctx context.Context, msg string, attrs ...attr.Attr) {
	b.Log(ctx, slog.LevelInfo, msg, attrs...)
}

// Warn logs a warning message with bedrock attributes.
func (b *Bridge) Warn(ctx context.Context, msg string, attrs ...attr.Attr) {
	b.Log(ctx, slog.LevelWarn, msg, attrs...)
}

// Error logs an error message with bedrock attributes.
func (b *Bridge) Error(ctx context.Context, msg string, attrs ...attr.Attr) {
	b.Log(ctx, slog.LevelError, msg, attrs...)
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
