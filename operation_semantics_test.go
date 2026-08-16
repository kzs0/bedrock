package bedrock

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/kzs0/bedrock/trace"
)

type operationDoneInterface interface {
	Done()
}

var _ operationDoneInterface = (*Op)(nil)
var _ func(*Op) = (*Op).Done

func TestOperationFailureOptionSetsOutcomeSpanAndMetrics(t *testing.T) {
	ctx, cleanup := Init(context.Background(), WithConfig(Config{Service: "test"}))
	defer cleanup()

	wantErr := errors.New("creation failed")
	op, opCtx := Operation(ctx, "failure.option", Failure(wantErr))
	state := operationStateFromContext(opCtx)

	if state.success {
		t.Fatal("Failure option left operation successful")
	}
	if !errors.Is(state.failure, wantErr) {
		t.Fatalf("failure = %v, want %v", state.failure, wantErr)
	}
	status, message := state.span.Status()
	if status != trace.StatusError || message != wantErr.Error() {
		t.Fatalf("span status = (%v, %q), want (%v, %q)", status, message, trace.StatusError, wantErr.Error())
	}

	op.Done()
	assertOperationMetric(t, FromContext(opCtx), "failure_option_count", 1)
	assertOperationMetric(t, FromContext(opCtx), "failure_option_successes", 0)
	assertOperationMetric(t, FromContext(opCtx), "failure_option_failures", 1)
}

func TestOperationEndFailureOverridesDefault(t *testing.T) {
	ctx, cleanup := Init(context.Background(), WithConfig(Config{Service: "test"}))
	defer cleanup()

	wantErr := errors.New("completion failed")
	op, opCtx := Operation(ctx, "end.failure")
	op.End(EndFailure(wantErr))
	op.Done()

	state := operationStateFromContext(opCtx)
	if state.success {
		t.Fatal("EndFailure left operation successful")
	}
	if !errors.Is(state.failure, wantErr) {
		t.Fatalf("failure = %v, want %v", state.failure, wantErr)
	}
	status, message := state.span.Status()
	if status != trace.StatusError || message != wantErr.Error() {
		t.Fatalf("span status = (%v, %q), want (%v, %q)", status, message, trace.StatusError, wantErr.Error())
	}
	assertOperationMetric(t, FromContext(opCtx), "end_failure_count", 1)
	assertOperationMetric(t, FromContext(opCtx), "end_failure_successes", 0)
	assertOperationMetric(t, FromContext(opCtx), "end_failure_failures", 1)
}

func TestOperationEndSuccessOverridesFailure(t *testing.T) {
	ctx, cleanup := Init(context.Background(), WithConfig(Config{Service: "test"}))
	defer cleanup()

	op, opCtx := Operation(ctx, "end.success", Failure(errors.New("recovered")))
	op.End(EndSuccess())

	state := operationStateFromContext(opCtx)
	if !state.success {
		t.Fatal("EndSuccess left operation failed")
	}
	if state.failure != nil {
		t.Fatalf("failure = %v, want nil", state.failure)
	}
	status, message := state.span.Status()
	if status != trace.StatusOK || message != "" {
		t.Fatalf("span status = (%v, %q), want (%v, empty)", status, message, trace.StatusOK)
	}
	assertOperationMetric(t, FromContext(opCtx), "end_success_successes", 1)
	assertOperationMetric(t, FromContext(opCtx), "end_success_failures", 0)
}

func TestOperationDoneIsIdempotent(t *testing.T) {
	var logs bytes.Buffer
	ctx, cleanup := Init(context.Background(), WithConfig(Config{
		Service:      "test",
		LogCanonical: true,
		LogOutput:    &logs,
		LogFormat:    "json",
	}))
	defer cleanup()

	op, opCtx := Operation(ctx, "done.twice")
	op.Done()
	op.End(EndFailure(errors.New("too late")))

	state := operationStateFromContext(opCtx)
	if !state.success || state.failure != nil {
		t.Fatalf("second Done changed outcome to success=%v failure=%v", state.success, state.failure)
	}
	assertOperationMetric(t, FromContext(opCtx), "done_twice_count", 1)
	assertOperationMetric(t, FromContext(opCtx), "done_twice_successes", 1)
	assertOperationMetric(t, FromContext(opCtx), "done_twice_failures", 0)
	if got := strings.Count(logs.String(), "operation.complete"); got != 1 {
		t.Fatalf("canonical log count = %d, want 1; logs: %s", got, logs.String())
	}
}

func TestOperationDoneIsConcurrentSafeAndIdempotent(t *testing.T) {
	var logs bytes.Buffer
	ctx, cleanup := Init(context.Background(), WithConfig(Config{
		Service:      "test",
		LogCanonical: true,
		LogOutput:    &logs,
		LogFormat:    "json",
	}))
	defer cleanup()

	op, opCtx := Operation(ctx, "done.concurrent")
	wantErr := errors.New("failed once")
	const callers = 64
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			op.End(EndFailure(wantErr))
		}()
	}
	wg.Wait()

	state := operationStateFromContext(opCtx)
	if state.success || !errors.Is(state.failure, wantErr) {
		t.Fatalf("outcome = success %v, failure %v; want failed once", state.success, state.failure)
	}
	assertOperationMetric(t, FromContext(opCtx), "done_concurrent_count", 1)
	assertOperationMetric(t, FromContext(opCtx), "done_concurrent_successes", 0)
	assertOperationMetric(t, FromContext(opCtx), "done_concurrent_failures", 1)
	if got := strings.Count(logs.String(), "operation.complete"); got != 1 {
		t.Fatalf("canonical log count = %d, want 1; logs: %s", got, logs.String())
	}
}

func TestNoopOperationDoneDoesNotApplyEndOptions(t *testing.T) {
	op, _ := Operation(context.Background(), "noop")
	applied := false
	op.End(func(*endConfig) { applied = true })
	if applied {
		t.Fatal("no-op operation applied an end option")
	}
}

func TestOperationDoneMethodValueCompatibility(t *testing.T) {
	op, _ := Operation(context.Background(), "noop")
	var done func() = op.Done
	done()
}

func assertOperationMetric(t *testing.T, b *Bedrock, name string, want float64) {
	t.Helper()
	for _, family := range b.Metrics().Gather() {
		if family.Name != name {
			continue
		}
		if len(family.Metrics) == 0 {
			if want == 0 {
				return
			}
			t.Fatalf("metric %q has no values; want %v", name, want)
		}
		if got := family.Metrics[0].Value; got != want {
			t.Fatalf("metric %q = %v, want %v", name, got, want)
		}
		return
	}
	t.Fatalf("metric %q not found", name)
}
