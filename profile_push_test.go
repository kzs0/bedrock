package bedrock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
)

// TestProfilePush_HeapProfileSent verifies that a heap profile is sent with the
// correct content type and profile type header.
func TestProfilePush_HeapProfileSent(t *testing.T) {
	var (
		gotContentType atomic.Value
		gotProfileType atomic.Value
		requestCount   atomic.Int32
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType.Store(r.Header.Get("Content-Type"))
		gotProfileType.Store(r.Header.Get("x-profile-type"))
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resource := attr.NewSet(attr.String("env", "test"))
	err := collectAndPushProfile(context.Background(), "test-service", resource, srv.URL, "tok", time.Millisecond, ProfileTypeHeap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ct := gotContentType.Load(); ct != "application/x-protobuf" {
		t.Errorf("expected application/x-protobuf, got %v", ct)
	}
	if pt := gotProfileType.Load(); pt != "heap" {
		t.Errorf("expected x-profile-type=heap, got %v", pt)
	}
}

// TestProfilePush_GoroutineProfileSent verifies the goroutine profile is sent.
func TestProfilePush_GoroutineProfileSent(t *testing.T) {
	var gotProfileType atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProfileType.Store(r.Header.Get("x-profile-type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resource := attr.EmptySet
	err := collectAndPushProfile(context.Background(), "svc", resource, srv.URL, "tok", time.Millisecond, ProfileTypeGoroutine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pt := gotProfileType.Load(); pt != "goroutine" {
		t.Errorf("expected x-profile-type=goroutine, got %v", pt)
	}
}

// TestProfilePush_APIKeyInHeader verifies that the Authorization header is set.
func TestProfilePush_APIKeyInHeader(t *testing.T) {
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := collectAndPushProfile(context.Background(), "svc", attr.EmptySet, srv.URL, "brk_live_abc123", time.Millisecond, ProfileTypeHeap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth := gotAuth.Load(); auth != "Bearer brk_live_abc123" {
		t.Errorf("expected Bearer token, got %v", auth)
	}
}

// TestProfilePush_MutexProfileSent verifies the mutex profile is sent with the
// correct x-profile-type header.
func TestProfilePush_MutexProfileSent(t *testing.T) {
	var gotProfileType atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProfileType.Store(r.Header.Get("x-profile-type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := collectAndPushProfile(context.Background(), "svc", attr.EmptySet, srv.URL, "tok", time.Millisecond, ProfileTypeMutex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pt := gotProfileType.Load(); pt != "mutex" {
		t.Errorf("expected x-profile-type=mutex, got %v", pt)
	}
}

// TestProfilePush_ErrorStatus verifies that a non-2xx response is returned as
// an error by collectAndPushProfile.
func TestProfilePush_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	err := collectAndPushProfile(context.Background(), "svc", attr.EmptySet, srv.URL, "tok", time.Millisecond, ProfileTypeHeap)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

// TestProfilePush_UnknownProfileType verifies that an unknown ProfileType
// returns an error immediately without making a network request.
func TestProfilePush_UnknownProfileType(t *testing.T) {
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := collectAndPushProfile(context.Background(), "svc", attr.EmptySet, srv.URL, "tok", time.Millisecond, ProfileType("unknown"))
	if err == nil {
		t.Fatal("expected error for unknown profile type")
	}
	if requestCount.Load() != 0 {
		t.Error("expected no HTTP request for unknown profile type")
	}
}

// TestProfilePush_StopCancelsAndWaits verifies stop cancellation and joining
// without timing-dependent goroutine counts or sleeps.
func TestProfilePush_StopCancelsAndWaits(t *testing.T) {
	ticks := make(chan time.Time, 1)
	started := make(chan struct{})
	canceled := make(chan struct{})
	var calls atomic.Int32
	collect := func(ctx context.Context, _ ProfileType) error {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}
	stop := startProfilePushWorker(ticks, nil, nil, collect)
	ticks <- time.Now()
	waitForProfileSignal(t, started, "collection start")

	stopped := make(chan struct{})
	go func() {
		stop(context.Background())
		close(stopped)
	}()
	waitForProfileSignal(t, canceled, "collection cancellation")
	waitForProfileSignal(t, stopped, "worker stop")

	// Repeated stops are safe and return after the already-completed join.
	stop(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("collector calls = %d, want 1", calls.Load())
	}
}

func TestProfilePush_StopContextCanBoundWait(t *testing.T) {
	ticks := make(chan time.Time, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	collect := func(context.Context, ProfileType) error {
		close(started)
		<-release // deliberately ignore cancellation to exercise bounded waiting
		return nil
	}
	stop := startProfilePushWorker(ticks, nil, nil, collect)
	ticks <- time.Now()
	waitForProfileSignal(t, started, "collection start")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stop(ctx)

	close(release)
	stopped := make(chan struct{})
	go func() {
		stop(context.Background())
		close(stopped)
	}()
	waitForProfileSignal(t, stopped, "worker join after bounded stop")
}

func TestCollectAndPushProfile_CPUWaitIsCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- waitForProfileSample(ctx, time.Hour)
	}()
	waitForProfileSignal(t, started, "CPU sample wait start")
	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCollectAndPushProfile_HTTPRequestIsCancellable(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseHandler
	}))
	defer srv.Close()
	defer close(releaseHandler)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- collectAndPushProfile(ctx, "svc", attr.EmptySet, srv.URL, "tok", time.Millisecond, ProfileTypeHeap)
	}()
	waitForProfileSignal(t, requestStarted, "HTTP request start")
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP request did not return after parent context cancellation")
	}
}

func waitForProfileSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
