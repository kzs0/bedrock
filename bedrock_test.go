package bedrock

import (
	"context"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
)

func TestInit(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	b := FromContext(ctx)
	if b == nil {
		t.Fatal("expected bedrock in context")
	}

	if b.Logger() == nil {
		t.Error("expected logger")
	}
	if b.Metrics() == nil {
		t.Error("expected metrics registry")
	}
	if b.Tracer() == nil {
		t.Error("expected tracer")
	}
}

func TestOperation(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	op, ctx := Operation(ctx, "test.operation",
		Attrs(attr.String("key", "value")),
		MetricLabels("key"),
	)
	defer op.Done()

	state := operationStateFromContext(ctx)
	if state == nil {
		t.Fatal("expected operation state in context")
	}

	if state.name != "test.operation" {
		t.Errorf("expected name 'test.operation', got %q", state.name)
	}

	if !state.success {
		t.Error("expected operation to default to success")
	}
}

func TestNestedOperations(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	parent, ctx := Operation(ctx, "parent")
	defer parent.Done()
	parentState := operationStateFromContext(ctx)

	child, ctx := Operation(ctx, "child")
	defer child.Done()
	childState := operationStateFromContext(ctx)

	// Child should have parent reference
	if childState.parent != parentState {
		t.Error("expected child to have parent reference")
	}

	// Both should have spans with same trace ID
	if parentState.span.TraceID() != childState.span.TraceID() {
		t.Error("expected same trace ID")
	}
}

func TestRegister(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	op, ctx := Operation(ctx, "test",
		Attrs(attr.String("initial", "value")),
	)
	defer op.Done()

	op.Register(ctx, attr.Int("count", 42))

	state := operationStateFromContext(ctx)
	if state.attrs.Len() != 2 {
		t.Errorf("expected 2 attrs, got %d", state.attrs.Len())
	}
}

func TestRegisterError(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	op, ctx := Operation(ctx, "test")
	defer op.Done()

	// Register an error - should mark as failure
	testErr := context.Canceled
	op.Register(ctx, attr.Error(testErr))

	state := operationStateFromContext(ctx)
	if state.success {
		t.Error("expected success to be false after registering error")
	}
	if state.failure == nil {
		t.Error("expected failure to be set")
	} else if state.failure.Error() != testErr.Error() {
		t.Errorf("expected failure message %q, got %q", testErr.Error(), state.failure.Error())
	}
}

func TestSource(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	source, ctx := Source(ctx, "background.worker",
		SourceAttrs(attr.String("worker.type", "test")),
		SourceMetricLabels("worker.type"),
	)
	defer source.Done()

	// Verify source config is in context
	sourceCfg := sourceConfigFromContext(ctx)
	if sourceCfg == nil {
		t.Fatal("expected source config in context")
	}
	if sourceCfg.name != "background.worker" {
		t.Errorf("expected name 'background.worker', got %q", sourceCfg.name)
	}

	// Create operation from source context
	op, ctx := Operation(ctx, "process",
		Attrs(attr.Int("batch.size", 100)),
	)
	defer op.Done()

	state := operationStateFromContext(ctx)
	if state.name != "background.worker.process" {
		t.Errorf("expected name 'background.worker.process', got %q", state.name)
	}

	// Should inherit source metric labels
	if len(state.metricLabels) != 1 || state.metricLabels[0] != "worker.type" {
		t.Errorf("expected to inherit source metric labels, got %v", state.metricLabels)
	}
}

func TestAutomaticMetrics(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	op, ctx := Operation(ctx, "test.operation",
		MetricLabels("status"),
		Attrs(attr.String("status", "ok")),
	)
	time.Sleep(5 * time.Millisecond)
	op.Done()

	// Verify automatic metrics were recorded
	b := FromContext(ctx)
	families := b.Metrics().Gather()

	expectedMetrics := map[string]bool{
		"test_operation_count":       false,
		"test_operation_successes":   false,
		"test_operation_duration_ms": false,
	}

	for _, fam := range families {
		if _, exists := expectedMetrics[fam.Name]; exists {
			expectedMetrics[fam.Name] = true

			if len(fam.Metrics) == 0 {
				t.Errorf("metric %s has no data points", fam.Name)
				continue
			}

			// Verify metric has the label
			metric := fam.Metrics[0]
			hasStatusLabel := false
			metric.Labels.Range(func(a attr.Attr) bool {
				if a.Key == "status" && a.Value.AsString() == "ok" {
					hasStatusLabel = true
					return false
				}
				return true
			})
			if !hasStatusLabel {
				t.Errorf("metric %s missing 'status' label", fam.Name)
			}
		}
	}

	for metric, found := range expectedMetrics {
		if !found {
			t.Errorf("expected metric %s not found", metric)
		}
	}
}

func TestMetricLabelDefaults(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	op, ctx := Operation(ctx, "test.op",
		MetricLabels("foo", "bar"),
		Attrs(attr.String("foo", "value1")),
		// Note: "bar" is not provided
	)
	op.Done()

	// Verify metrics have "_" for missing label
	b := FromContext(ctx)
	families := b.Metrics().Gather()
	for _, fam := range families {
		if fam.Name == "test.op_count" {
			if len(fam.Metrics) == 0 {
				t.Fatal("no metrics found")
			}

			metric := fam.Metrics[0]
			foundBar := false
			metric.Labels.Range(func(a attr.Attr) bool {
				if a.Key == "bar" {
					foundBar = true
					if a.Value.AsString() != "_" {
						t.Errorf("expected label 'bar' to have value '_', got %q", a.Value.AsString())
					}
				}
				return true
			})
			if !foundBar {
				t.Error("expected to find 'bar' label with default value")
			}
			break
		}
	}
}

func TestStep(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
	)
	defer close()

	// Create a parent operation first
	op, ctx := Operation(ctx, "parent")
	defer op.Done()

	// Now create a step
	step := Step(ctx, "helper",
		Attrs(attr.String("key", "value")),
	)
	defer step.Done()

	// Step should be tracked in parent operation
	parentState := operationStateFromContext(ctx)
	if parentState == nil {
		t.Fatal("expected parent operation state in context")
	}

	// Check that step was added to parent
	if len(parentState.steps) != 1 {
		t.Errorf("expected 1 step in parent, got %d", len(parentState.steps))
	}

	// Step should have attributes
	if step.attrs.Len() != 1 {
		t.Errorf("expected 1 attr, got %d", step.attrs.Len())
	}
}

func TestNoopBedrock(t *testing.T) {
	// Context without bedrock should use noop
	ctx := context.Background()
	op, ctx := Operation(ctx, "test")
	defer op.Done()

	state := operationStateFromContext(ctx)
	if state == nil {
		t.Fatal("expected operation state even with noop bedrock")
	}

	if !state.bedrock.isNoop {
		t.Error("expected noop bedrock")
	}

	// Should not panic
	op.Register(ctx, attr.String("key", "value"))
}

func TestStaticAttributesInMetrics(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test-service"}),
		WithStaticAttrs(
			attr.String("env", "test"),
			attr.String("region", "us-west-2"),
		),
	)
	defer close()

	// Create operation
	op, ctx := Operation(ctx, "test.static_metrics",
		MetricLabels("status"),
		Attrs(attr.String("status", "ok")),
	)
	op.Done()

	// Verify metrics include static attributes as labels
	b := FromContext(ctx)
	families := b.Metrics().Gather()

	foundMetric := false
	for _, fam := range families {
		if fam.Name == "test_static_metrics_count" {
			foundMetric = true
			if len(fam.Metrics) == 0 {
				t.Fatal("expected metric to have values")
			}

			metric := fam.Metrics[0]

			// Check for static attributes
			hasEnv := false
			hasRegion := false
			hasStatus := false

			metric.Labels.Range(func(a attr.Attr) bool {
				if a.Key == "env" && a.Value.AsString() == "test" {
					hasEnv = true
				}
				if a.Key == "region" && a.Value.AsString() == "us-west-2" {
					hasRegion = true
				}
				if a.Key == "status" && a.Value.AsString() == "ok" {
					hasStatus = true
				}
				return true
			})

			if !hasEnv {
				t.Error("expected metric to have 'env' static label")
			}
			if !hasRegion {
				t.Error("expected metric to have 'region' static label")
			}
			if !hasStatus {
				t.Error("expected metric to have 'status' operation label")
			}
		}
	}

	if !foundMetric {
		t.Error("expected to find test.static_metrics_count metric")
	}
}
