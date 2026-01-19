package bedrock

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/kzs0/bedrock/attr"
)

func TestCounter(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	// Create counter
	counter := Counter(ctx, "test_counter", "Test counter", "method")
	if counter == nil {
		t.Fatal("expected counter to be created")
	}

	// Use counter
	counter.With(attr.String("method", "GET")).Inc()

	// Verify it was registered
	b := FromContext(ctx)
	families := b.Metrics().Gather()

	found := false
	for _, fam := range families {
		if fam.Name == "test_counter" {
			found = true
			if len(fam.Metrics) == 0 {
				t.Error("expected counter to have values")
			}
		}
	}
	if !found {
		t.Error("expected counter to be registered")
	}
}

func TestCounterNoop(t *testing.T) {
	// Use context without bedrock - should use noop
	ctx := context.Background()

	counter := Counter(ctx, "test_counter", "Test counter")
	if counter == nil {
		t.Fatal("expected noop counter to be created")
	}

	// Should not panic
	counter.Inc()
}

func TestGauge(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	// Create gauge
	gauge := Gauge(ctx, "test_gauge", "Test gauge", "status")
	if gauge == nil {
		t.Fatal("expected gauge to be created")
	}

	// Use gauge
	gauge.With(attr.String("status", "active")).Set(42)

	// Verify it was registered
	b := FromContext(ctx)
	families := b.Metrics().Gather()

	found := false
	for _, fam := range families {
		if fam.Name == "test_gauge" {
			found = true
			if len(fam.Metrics) == 0 {
				t.Error("expected gauge to have values")
			} else if fam.Metrics[0].Value != 42 {
				t.Errorf("expected value 42, got %f", fam.Metrics[0].Value)
			}
		}
	}
	if !found {
		t.Error("expected gauge to be registered")
	}
}

func TestGaugeNoop(t *testing.T) {
	// Use context without bedrock - should use noop
	ctx := context.Background()

	gauge := Gauge(ctx, "test_gauge", "Test gauge")
	if gauge == nil {
		t.Fatal("expected noop gauge to be created")
	}

	// Should not panic
	gauge.Set(100)
}

func TestHistogram(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	// Create histogram
	hist := Histogram(ctx, "test_histogram", "Test histogram", nil, "endpoint")
	if hist == nil {
		t.Fatal("expected histogram to be created")
	}

	// Use histogram
	hist.With(attr.String("endpoint", "/api")).Observe(123.45)

	// Verify it was registered
	b := FromContext(ctx)
	families := b.Metrics().Gather()

	found := false
	for _, fam := range families {
		if fam.Name == "test_histogram" {
			found = true
			if len(fam.Metrics) == 0 {
				t.Error("expected histogram to have values")
			} else {
				if fam.Metrics[0].Count != 1 {
					t.Errorf("expected count 1, got %d", fam.Metrics[0].Count)
				}
				if fam.Metrics[0].Sum != 123.45 {
					t.Errorf("expected sum 123.45, got %f", fam.Metrics[0].Sum)
				}
			}
		}
	}
	if !found {
		t.Error("expected histogram to be registered")
	}
}

func TestHistogramWithCustomBuckets(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	customBuckets := []float64{10, 50, 100}
	hist := Histogram(ctx, "test_custom_histogram", "Test histogram", customBuckets)
	if hist == nil {
		t.Fatal("expected histogram to be created")
	}

	hist.Observe(25)

	b := FromContext(ctx)
	families := b.Metrics().Gather()

	found := false
	for _, fam := range families {
		if fam.Name == "test_custom_histogram" {
			found = true
			if len(fam.Metrics[0].Buckets) != len(customBuckets) {
				t.Errorf("expected %d buckets, got %d", len(customBuckets), len(fam.Metrics[0].Buckets))
			}
		}
	}
	if !found {
		t.Error("expected histogram to be registered")
	}
}

func TestHistogramNoop(t *testing.T) {
	// Use context without bedrock - should use noop
	ctx := context.Background()

	hist := Histogram(ctx, "test_histogram", "Test histogram", nil)
	if hist == nil {
		t.Fatal("expected noop histogram to be created")
	}

	// Should not panic
	hist.Observe(50)
}

func TestDebug(t *testing.T) {
	var buf bytes.Buffer
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:   "test-service",
			LogLevel:  "debug",
			LogFormat: "json",
			LogOutput: &buf,
		}),
	)
	defer close()

	Debug(ctx, "debug message", attr.String("key", "value"))

	output := buf.String()
	if output == "" {
		t.Error("expected log output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("debug message")) {
		t.Error("expected log to contain message")
	}
	if !bytes.Contains(buf.Bytes(), []byte("key")) {
		t.Error("expected log to contain attribute key")
	}
}

func TestInfo(t *testing.T) {
	var buf bytes.Buffer
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:   "test-service",
			LogLevel:  "info",
			LogFormat: "json",
			LogOutput: &buf,
		}),
	)
	defer close()

	Info(ctx, "info message", attr.String("user_id", "123"))

	output := buf.String()
	if output == "" {
		t.Error("expected log output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("info message")) {
		t.Error("expected log to contain message")
	}
	if !bytes.Contains(buf.Bytes(), []byte("user_id")) {
		t.Error("expected log to contain attribute")
	}
}

func TestWarn(t *testing.T) {
	var buf bytes.Buffer
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:   "test-service",
			LogLevel:  "warn",
			LogFormat: "json",
			LogOutput: &buf,
		}),
	)
	defer close()

	Warn(ctx, "warning message", attr.Int("count", 42))

	output := buf.String()
	if output == "" {
		t.Error("expected log output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("warning message")) {
		t.Error("expected log to contain message")
	}
}

func TestError(t *testing.T) {
	var buf bytes.Buffer
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:   "test-service",
			LogLevel:  "error",
			LogFormat: "json",
			LogOutput: &buf,
		}),
	)
	defer close()

	Error(ctx, "error message", attr.String("error", "something went wrong"))

	output := buf.String()
	if output == "" {
		t.Error("expected log output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("error message")) {
		t.Error("expected log to contain message")
	}
}

func TestLog(t *testing.T) {
	var buf bytes.Buffer
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:   "test-service",
			LogLevel:  "debug",
			LogFormat: "json",
			LogOutput: &buf,
		}),
	)
	defer close()

	Log(ctx, slog.LevelWarn, "custom level", attr.String("custom", "value"))

	output := buf.String()
	if output == "" {
		t.Error("expected log output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("custom level")) {
		t.Error("expected log to contain message")
	}
	if !bytes.Contains(buf.Bytes(), []byte("custom")) {
		t.Error("expected log to contain attribute")
	}
}

func TestLoggingWithStaticAttrs(t *testing.T) {
	var buf bytes.Buffer
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:   "test-service",
			LogLevel:  "info",
			LogFormat: "json",
			LogOutput: &buf,
		}),
		WithStaticAttrs(attr.String("env", "test"), attr.String("version", "1.0")),
	)
	defer close()

	Info(ctx, "test message")

	output := buf.String()
	if output == "" {
		t.Error("expected log output")
	}
	// Note: Static attributes are part of the logger config, but may not appear
	// in every log message depending on the handler implementation.
	// The important thing is that logging doesn't panic.
}

func TestLoggingNoop(t *testing.T) {
	// Use context without bedrock - should use noop
	ctx := context.Background()

	// Should not panic
	Debug(ctx, "debug")
	Info(ctx, "info")
	Warn(ctx, "warn")
	Error(ctx, "error")
	Log(ctx, slog.LevelInfo, "log")
}

func TestMetricsSameName(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	// Creating metrics with the same name should return the same underlying instance
	counter1 := Counter(ctx, "requests", "Request count")
	counter2 := Counter(ctx, "requests", "Request count")

	if counter1.counter != counter2.counter {
		t.Error("expected same underlying counter instance for same name")
	}
}

func TestStaticAttributesInLogs(t *testing.T) {
	var buf bytes.Buffer
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:   "test-service",
			LogLevel:  "info",
			LogFormat: "json",
			LogOutput: &buf,
		}),
		WithStaticAttrs(
			attr.String("env", "production"),
			attr.String("version", "1.2.3"),
		),
	)
	defer close()

	Info(ctx, "test log message", attr.String("user_id", "42"))

	output := buf.String()
	if output == "" {
		t.Fatal("expected log output")
	}

	// Verify static attributes are included
	if !bytes.Contains(buf.Bytes(), []byte("env")) {
		t.Error("expected log to contain 'env' static attribute")
	}
	if !bytes.Contains(buf.Bytes(), []byte("production")) {
		t.Error("expected log to contain 'production' value")
	}
	if !bytes.Contains(buf.Bytes(), []byte("version")) {
		t.Error("expected log to contain 'version' static attribute")
	}
	if !bytes.Contains(buf.Bytes(), []byte("1.2.3")) {
		t.Error("expected log to contain '1.2.3' value")
	}
	if !bytes.Contains(buf.Bytes(), []byte("user_id")) {
		t.Error("expected log to contain 'user_id' dynamic attribute")
	}
}
