package bedrock

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/metric"
)

func TestMetricsPush_Headers(t *testing.T) {
	type headers struct {
		contentType string
		auth        string
	}
	received := make(chan headers, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- headers{
			contentType: r.Header.Get("Content-Type"),
			auth:        r.Header.Get("Authorization"),
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stop := startMetricsPush(metric.NewRegistry(""), nil, cloudConfig{
		apiKey:       "test-key",
		endpoint:     srv.URL,
		pushInterval: 24 * time.Hour,
	})
	stop(testContext(t))

	got := receiveTestValue(t, received)
	if !strings.HasPrefix(got.contentType, "text/plain") {
		t.Errorf("expected text/plain content-type, got %q", got.contentType)
	}
	if got.auth != "Bearer test-key" {
		t.Errorf("expected 'Bearer test-key', got %q", got.auth)
	}
}

func TestMetricsPush_ContainsMetrics(t *testing.T) {
	body := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contents, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		body <- string(contents)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := metric.NewRegistry("")
	reg.Counter("test_requests_total", "test counter", "method").
		With(attr.String("method", "GET")).Inc()
	stop := startMetricsPush(reg, nil, cloudConfig{
		apiKey:       "key",
		endpoint:     srv.URL,
		pushInterval: 24 * time.Hour,
	})
	stop(testContext(t))

	if got := receiveTestValue(t, body); !strings.Contains(got, "test_requests_total") {
		t.Errorf("expected test_requests_total in push body, got:\n%s", got)
	}
}

func TestMetricsPush_PeriodicRequestHasBoundedLifetime(t *testing.T) {
	started := make(chan struct{}, 1)
	canceled := make(chan error, 1)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			started <- struct{}{}
			<-r.Context().Done()
			canceled <- r.Context().Err()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stop := startMetricsPush(metric.NewRegistry(""), nil, cloudConfig{
		apiKey:       "key",
		endpoint:     srv.URL,
		pushInterval: 50 * time.Millisecond,
	})
	receiveTestValue(t, started)
	if err := receiveTestValue(t, canceled); err == nil {
		t.Fatal("periodic request context was not canceled at its deadline")
	}
	stop(testContext(t))
}

func TestMetricsPush_StopCancelsPeriodicAndCallerCanBoundFinalWait(t *testing.T) {
	periodicStarted := make(chan struct{}, 1)
	periodicCanceled := make(chan struct{}, 1)
	finalStarted := make(chan struct{}, 1)
	finalRelease := make(chan struct{})
	finalFinished := make(chan struct{}, 1)
	var requests atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			periodicStarted <- struct{}{}
			<-r.Context().Done()
			periodicCanceled <- struct{}{}
			return
		}

		finalStarted <- struct{}{}
		<-finalRelease
		w.WriteHeader(http.StatusOK)
		finalFinished <- struct{}{}
	}))
	defer srv.Close()

	stop := startMetricsPush(metric.NewRegistry(""), nil, cloudConfig{
		apiKey:       "key",
		endpoint:     srv.URL,
		pushInterval: 100 * time.Millisecond,
	})
	receiveTestValue(t, periodicStarted)

	stopCtx, cancelStop := context.WithCancel(context.Background())
	defer cancelStop()
	stopDone := make(chan struct{})
	go func() {
		stop(stopCtx)
		close(stopDone)
	}()

	receiveTestValue(t, periodicCanceled)
	receiveTestValue(t, finalStarted)
	cancelStop()
	receiveTestValue(t, stopDone)
	close(finalRelease)
	receiveTestValue(t, finalFinished)
	stop(testContext(t))

	if got := requests.Load(); got != 2 {
		t.Errorf("request count = %d, want one canceled periodic and one final push", got)
	}
}

func TestMetricsPush_StopDeadlineBoundsNonCooperativeCollection(t *testing.T) {
	collector := &firstBlockingCollector{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	registry := metric.NewRegistry("")
	registry.RegisterCollector(collector)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stop := startMetricsPush(registry, nil, cloudConfig{
		apiKey:       "key",
		endpoint:     srv.URL,
		pushInterval: 10 * time.Millisecond,
	})
	receiveTestValue(t, collector.started)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	firstStopDone := make(chan struct{})
	go func() {
		stop(canceled)
		close(firstStopDone)
	}()
	receiveTestValue(t, firstStopDone)
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests before blocked collection release = %d, want 0", got)
	}

	close(collector.release)
	stop(testContext(t))
	if got := requests.Load(); got != 1 {
		t.Errorf("requests = %d, want exactly one final push", got)
	}
	if got := collector.calls.Load(); got != 2 {
		t.Errorf("collector calls = %d, want one periodic and one final collection", got)
	}
}

func TestMetricsPush_StopIsConcurrentAndIdempotent(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	releaseRequest := make(chan struct{})
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		requestStarted <- struct{}{}
		<-releaseRequest
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stop := startMetricsPush(metric.NewRegistry(""), nil, cloudConfig{
		apiKey:       "key",
		endpoint:     srv.URL,
		pushInterval: 24 * time.Hour,
	})

	const callers = 16
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			stop(context.Background())
		}()
	}

	receiveTestValue(t, requestStarted)
	close(releaseRequest)
	waitForTestGroup(t, &wg)
	if got := requests.Load(); got != 1 {
		t.Errorf("request count = %d, want exactly one final push", got)
	}

	stop(context.Background())
	if got := requests.Load(); got != 1 {
		t.Errorf("request count after repeated stop = %d, want 1", got)
	}
}

func TestMetricsPush_ErrorResponse(t *testing.T) {
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	stop := startMetricsPush(metric.NewRegistry(""), nil, cloudConfig{
		apiKey:       "key",
		endpoint:     srv.URL,
		pushInterval: 24 * time.Hour,
	})
	stop(testContext(t))
	if got := requestCount.Load(); got != 1 {
		t.Errorf("request count = %d, want 1", got)
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func receiveTestValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		t.Fatal("timed out waiting for test event")
		var zero T
		return zero
	}
}

func waitForTestGroup(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	receiveTestValue(t, done)
}

type firstBlockingCollector struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (c *firstBlockingCollector) Collect() {
	if c.calls.Add(1) != 1 {
		return
	}
	close(c.started)
	<-c.release
}
