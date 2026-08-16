package bedrock

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestInitPanicsOnInvalidEnvironmentConfiguration(t *testing.T) {
	clearBedrockEnv(t)
	t.Setenv("BEDROCK_TRACE_SAMPLE_RATE", "not-a-number")

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("Init did not panic for invalid environment configuration")
		}
		message := fmt.Sprint(recovered)
		for _, want := range []string{
			"bedrock: failed to parse config from env",
			"TraceSampleRate",
			"invalid float",
		} {
			if !strings.Contains(message, want) {
				t.Errorf("panic %q does not contain %q", message, want)
			}
		}
	}()

	Init(context.Background())
}

func TestInitLoadsValidEnvironmentConfiguration(t *testing.T) {
	clearBedrockEnv(t)
	t.Setenv("BEDROCK_SERVICE", "env-service")
	t.Setenv("BEDROCK_METRIC_PREFIX", "env_prefix")
	t.Setenv("BEDROCK_TRACE_SAMPLE_RATE", "0.25")
	t.Setenv("BEDROCK_SERVER_ENABLED", "false")

	ctx, cleanup := Init(context.Background())
	defer cleanup()

	b := FromContext(ctx)
	if b.config.Service != "env-service" {
		t.Errorf("service = %q, want env-service", b.config.Service)
	}
	if b.config.MetricPrefix != "env_prefix" {
		t.Errorf("metric prefix = %q, want env_prefix", b.config.MetricPrefix)
	}
	if b.config.TraceSampleRate != 0.25 {
		t.Errorf("trace sample rate = %v, want 0.25", b.config.TraceSampleRate)
	}
	if b.config.ServerEnabled {
		t.Error("observability server enabled despite BEDROCK_SERVER_ENABLED=false")
	}
}

func TestInitLoadsEnvironmentDefaults(t *testing.T) {
	clearBedrockEnv(t)
	// Keep initialization listener-free while leaving the fields under test at
	// their environment defaults.
	t.Setenv("BEDROCK_SERVER_ENABLED", "false")

	ctx, cleanup := Init(context.Background())
	defer cleanup()

	b := FromContext(ctx)
	if b.config.Service != "unknown" {
		t.Errorf("default service = %q, want unknown", b.config.Service)
	}
	if b.config.TraceSampleRate != 1 {
		t.Errorf("default trace sample rate = %v, want 1", b.config.TraceSampleRate)
	}
	if b.config.ShutdownTimeout != 30*time.Second {
		t.Errorf("default shutdown timeout = %v, want 30s", b.config.ShutdownTimeout)
	}
}

func clearBedrockEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"BEDROCK_SERVICE",
		"BEDROCK_TRACE_URL",
		"BEDROCK_TRACE_PROTOCOL",
		"BEDROCK_TRACE_INSECURE",
		"BEDROCK_TRACE_SAMPLE_RATE",
		"BEDROCK_LOG_LEVEL",
		"BEDROCK_LOG_FORMAT",
		"BEDROCK_LOG_ADD_SOURCE",
		"BEDROCK_LOG_CANONICAL",
		"BEDROCK_METRIC_PREFIX",
		"BEDROCK_METRIC_BUCKETS",
		"BEDROCK_RUNTIME_METRICS",
		"BEDROCK_SERVER_ENABLED",
		"BEDROCK_SERVER_ADDR",
		"BEDROCK_SERVER_METRICS",
		"BEDROCK_SERVER_PPROF",
		"BEDROCK_SERVER_READ_TIMEOUT",
		"BEDROCK_SERVER_READ_HEADER_TIMEOUT",
		"BEDROCK_SERVER_WRITE_TIMEOUT",
		"BEDROCK_SERVER_IDLE_TIMEOUT",
		"BEDROCK_SERVER_MAX_HEADER_BYTES",
		"BEDROCK_SHUTDOWN_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
}
