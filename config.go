package bedrock

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/kzs0/bedrock/config"
	"github.com/kzs0/bedrock/trace"
)

// Config configures the Bedrock instance.
type Config struct {
	// ServiceName is the name of the service.
	ServiceName string `env:"SERVICE_NAME" envDefault:"unknown"`
	// TraceEndpoint is the OTLP HTTP endpoint for traces.
	TraceEndpoint string `env:"TRACE_ENDPOINT"`
	// TraceSampleRate controls trace sampling (0.0 to 1.0). If 0, defaults to 1.0 (always sample).
	TraceSampleRate float64 `env:"OTEL_TRACES_SAMPLER_ARG" envDefault:"1.0"`
	// LogLevel is the minimum log level (debug, info, warn, error).
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
	// LogFormat is "json" or "text". Defaults to "json".
	LogFormat string `env:"LOG_FORMAT" envDefault:"json"`
	// MetricPrefix is prepended to all metric names.
	MetricPrefix string `env:"METRIC_PREFIX"`
	// DefaultHistogramBuckets are the default histogram buckets (comma-separated).
	DefaultHistogramBuckets []float64 `env:"HISTOGRAM_BUCKETS"`
	// ShutdownTimeout is the timeout for shutdown operations.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"5s"`
	// CanonicalLog enables structured logging of operation completion.
	CanonicalLog bool `env:"CANONICAL_LOG" envDefault:"false"`

	// Non-env configurable fields
	// TraceSampler controls trace sampling (overrides TraceSampleRate if set).
	TraceSampler trace.Sampler `env:"-"`
	// LogOutput is the log output writer. Defaults to os.Stderr.
	LogOutput io.Writer `env:"-"`
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		ServiceName:     "unknown",
		LogLevel:        "info",
		LogFormat:       "json",
		ShutdownTimeout: 5 * time.Second,
		TraceSampleRate: 1.0,
	}
}

// FromEnv loads configuration from environment variables.
func FromEnv() (Config, error) {
	cfg, err := config.Parse[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("bedrock: failed to parse config from env: %w", err)
	}
	return cfg, nil
}

// MustFromEnv loads configuration from environment variables, panicking on error.
func MustFromEnv() Config {
	cfg, err := FromEnv()
	if err != nil {
		panic(err)
	}
	return cfg
}

// parseLogLevel converts a string log level to slog.Level.
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// logLevel returns the parsed slog.Level from the string LogLevel field.
func (c Config) logLevel() slog.Level {
	return parseLogLevel(c.LogLevel)
}
