package bedrock

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/kzs0/bedrock/env"
	"github.com/kzs0/bedrock/trace"
)

// Config configures the Bedrock instance.
type Config struct {
	// Service is the name of the service.
	Service string `env:"BEDROCK_SERVICE" envDefault:"unknown"`

	// Tracing configuration
	// TraceURL is the OTLP HTTP endpoint for traces.
	TraceURL string `env:"BEDROCK_TRACE_URL"`
	// TraceSampleRate controls trace sampling (0.0 to 1.0).
	TraceSampleRate float64 `env:"BEDROCK_TRACE_SAMPLE_RATE" envDefault:"1.0"`
	// TraceSampler controls trace sampling (overrides TraceSampleRate if set).
	TraceSampler trace.Sampler `env:"-"`

	// Logging configuration
	// LogLevel is the minimum log level (debug, info, warn, error).
	LogLevel string `env:"BEDROCK_LOG_LEVEL" envDefault:"info"`
	// LogFormat is "json" or "text".
	LogFormat string `env:"BEDROCK_LOG_FORMAT" envDefault:"json"`
	// LogOutput is the log output writer. Defaults to os.Stderr.
	LogOutput io.Writer `env:"-"`
	// LogCanonical enables structured logging of operation completion.
	LogCanonical bool `env:"BEDROCK_LOG_CANONICAL" envDefault:"false"`

	// Metrics configuration
	// MetricPrefix is prepended to all metric names.
	MetricPrefix string `env:"BEDROCK_METRIC_PREFIX"`
	// MetricBuckets are the default histogram buckets.
	MetricBuckets []float64 `env:"BEDROCK_METRIC_BUCKETS"`

	// Server configuration
	// ServerEnabled enables the automatic observability server.
	ServerEnabled bool `env:"BEDROCK_SERVER_ENABLED" envDefault:"true"`
	// ServerAddr is the address to listen on.
	ServerAddr string `env:"BEDROCK_SERVER_ADDR" envDefault:":9090"`
	// ServerMetrics enables /metrics endpoint.
	ServerMetrics bool `env:"BEDROCK_SERVER_METRICS" envDefault:"true"`
	// ServerPprof enables /debug/pprof endpoints.
	ServerPprof bool `env:"BEDROCK_SERVER_PPROF" envDefault:"true"`
	// ServerReadTimeout is the max request read duration.
	ServerReadTimeout time.Duration `env:"BEDROCK_SERVER_READ_TIMEOUT" envDefault:"10s"`
	// ServerReadHeaderTimeout is the header read timeout.
	ServerReadHeaderTimeout time.Duration `env:"BEDROCK_SERVER_READ_HEADER_TIMEOUT" envDefault:"5s"`
	// ServerWriteTimeout is the response write timeout.
	ServerWriteTimeout time.Duration `env:"BEDROCK_SERVER_WRITE_TIMEOUT" envDefault:"30s"`
	// ServerIdleTimeout is the keep-alive timeout.
	ServerIdleTimeout time.Duration `env:"BEDROCK_SERVER_IDLE_TIMEOUT" envDefault:"120s"`
	// ServerMaxHeaderBytes is the header size limit.
	ServerMaxHeaderBytes int `env:"BEDROCK_SERVER_MAX_HEADER_BYTES" envDefault:"1048576"` // 1 MB

	// ShutdownTimeout is the timeout for shutdown operations.
	ShutdownTimeout time.Duration `env:"BEDROCK_SHUTDOWN_TIMEOUT" envDefault:"30s"`
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		Service:                 "unknown",
		TraceSampleRate:         1.0,
		LogLevel:                "info",
		LogFormat:               "json",
		LogCanonical:            false,
		ServerEnabled:           true,
		ServerAddr:              ":9090",
		ServerMetrics:           true,
		ServerPprof:             true,
		ServerReadTimeout:       10 * time.Second,
		ServerReadHeaderTimeout: 5 * time.Second,
		ServerWriteTimeout:      30 * time.Second,
		ServerIdleTimeout:       120 * time.Second,
		ServerMaxHeaderBytes:    1 << 20, // 1 MB
		ShutdownTimeout:         30 * time.Second,
	}
}

// FromEnv loads configuration from environment variables.
func FromEnv() (Config, error) {
	cfg, err := env.Parse[Config]()
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

// serverConfig returns a ServerConfig from the Config fields.
func (c Config) serverConfig() ServerConfig {
	return ServerConfig{
		Addr:              c.ServerAddr,
		EnableMetrics:     c.ServerMetrics,
		EnablePprof:       c.ServerPprof,
		ReadTimeout:       c.ServerReadTimeout,
		ReadHeaderTimeout: c.ServerReadHeaderTimeout,
		WriteTimeout:      c.ServerWriteTimeout,
		IdleTimeout:       c.ServerIdleTimeout,
		MaxHeaderBytes:    c.ServerMaxHeaderBytes,
		ShutdownTimeout:   c.ShutdownTimeout,
	}
}
