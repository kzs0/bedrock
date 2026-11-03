package e2e

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kzs0/bedrock/trace/otlp"
)

func emitOperations(t *testing.T, app *fixtureApp, n int, query string) {
	t.Helper()
	response, err := (&http.Client{Timeout: 3 * time.Second}).Get(fmt.Sprintf("%s/operations?n=%d%s", app.ControlURL, n, query))
	if err != nil {
		t.Fatalf("operations blocked: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	var result struct{ Count int }
	if response.StatusCode != http.StatusOK {
		t.Fatalf("operations status: %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&result); err != nil || result.Count != n {
		t.Fatalf("operations count: %+v, error %v", result, err)
	}
}

func assertCancellationError(t *testing.T, app *fixtureApp) {
	t.Helper()
	for _, message := range []string{"context deadline exceeded", "context canceled", "i/o timeout"} {
		if strings.Contains(app.Logs(), message) {
			return
		}
	}
	t.Fatal("shutdown omitted cancellation error")
}

func TestPublicQueueSaturationAccountsForEverySpan(t *testing.T) {
	receiver := newReceiver(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	receiver.SetHandler(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	})
	app := startApp(t, fixtureConfig{TraceEndpoint: receiver.URL + "/v1/traces"})
	defaults := otlp.DefaultBatchConfig()
	emitOperations(t, app, defaults.BatchSize, "")
	select {
	case <-entered:
	case <-time.After(12 * time.Second):
		t.Fatal("export did not reach backend")
	}
	const emitted = 6000
	emitOperations(t, app, emitted-defaults.BatchSize, "")
	stats := readBehaviorStats(t, app)
	if stats.Trace.DroppedSpans == 0 || stats.Trace.FailedExports != 0 {
		t.Fatalf("saturation stats: %+v", stats.Trace)
	}
	accepted := uint64(emitted) - stats.Trace.DroppedSpans
	if accepted != uint64(defaults.MaxQueueSize) {
		t.Fatalf("accepted %d spans under stalled export, bound %d", accepted, defaults.MaxQueueSize)
	}
	unblock()
	app.Stop(t)
	final := app.FinalStats(t)
	if final.Trace != stats.Trace {
		t.Fatalf("final loss changed after successful drain: %+v, before %+v", final.Trace, stats.Trace)
	}
	seen := make(map[string]bool)
	for {
		select {
		case request := <-receiver.Requests:
			for _, span := range behaviorSpans(t, request) {
				if span.Name != "fixture.operation" {
					t.Fatalf("unexpected span: %v", span)
				}
				id := hex.EncodeToString(span.TraceId) + hex.EncodeToString(span.SpanId)
				if seen[id] {
					t.Fatalf("duplicate span %s", id)
				}
				seen[id] = true
			}
		default:
			if uint64(len(seen))+final.Trace.DroppedSpans != emitted || uint64(len(seen)) != accepted {
				t.Fatalf("emitted %d, exported %d, dropped %d", emitted, len(seen), final.Trace.DroppedSpans)
			}
			if receiver.dropped.Load() != 0 {
				t.Fatal("receiver lost captures")
			}
			return
		}
	}
}

func TestStalledHTTPExportShutdownCancellation(t *testing.T) {
	for _, signal := range []bool{false, true} {
		t.Run(fmt.Sprintf("signal=%t", signal), func(t *testing.T) {
			receiver := newReceiver(t)
			entered, canceled := make(chan struct{}), make(chan struct{})
			receiver.SetHandler(func(_ http.ResponseWriter, r *http.Request) {
				close(entered)
				<-r.Context().Done()
				close(canceled)
			})
			app := startApp(t, fixtureConfig{TraceEndpoint: receiver.URL + "/v1/traces", ShutdownMillis: 500})
			emitOperations(t, app, otlp.DefaultBatchConfig().BatchSize, "")
			select {
			case <-entered:
			case <-time.After(12 * time.Second):
				t.Fatal("export did not reach backend")
			}
			if got := len(behaviorSpans(t, nextBehaviorExport(t, receiver.Requests))); got != otlp.DefaultBatchConfig().BatchSize {
				t.Fatalf("stalled batch spans: %d", got)
			}
			before := time.Now()
			var err error
			if signal {
				err = app.SignalError(t)
			} else {
				err = app.StopError(t)
			}
			if err == nil || time.Since(before) > 3*time.Second {
				t.Fatalf("bounded shutdown failed: error %v, elapsed %s", err, time.Since(before))
			}
			assertCancellationError(t, app)
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("backend context was not canceled")
			}
			// Deadline snapshots may precede asynchronous failure accounting.
			if stats := app.FinalStats(t); stats.Trace.DroppedSpans != 0 || stats.Trace.FailedExports > 1 {
				t.Fatalf("unexpected cancellation losses: %+v", stats.Trace)
			}
		})
	}
}

func TestSIGTERMFlushesAcceptedOperations(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{TraceEndpoint: receiver.URL + "/v1/traces"})
	emitOperations(t, app, 7, "")
	app.Signal(t)
	spans := behaviorSpans(t, nextBehaviorExport(t, receiver.Requests))
	if len(spans) != 7 {
		t.Fatalf("signal flushed %d spans, want 7", len(spans))
	}
	if stats := app.FinalStats(t); stats.Trace.DroppedSpans != 0 || stats.Trace.FailedExports != 0 {
		t.Fatalf("graceful signal loss: %+v", stats.Trace)
	}
}
