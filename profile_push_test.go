package bedrock

import (
	"net/http"
	"net/http/httptest"
	"runtime"
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
	err := collectAndPushProfile("test-service", resource, srv.URL, "tok", time.Millisecond, ProfileTypeHeap)
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
	err := collectAndPushProfile("svc", resource, srv.URL, "tok", time.Millisecond, ProfileTypeGoroutine)
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

	err := collectAndPushProfile("svc", attr.EmptySet, srv.URL, "brk_live_abc123", time.Millisecond, ProfileTypeHeap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth := gotAuth.Load(); auth != "Bearer brk_live_abc123" {
		t.Errorf("expected Bearer token, got %v", auth)
	}
}

// TestProfilePush_StopStopsGoroutine verifies no goroutine leak after stop.
func TestProfilePush_StopStopsGoroutine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	goroutinesBefore := runtime.NumGoroutine()

	cfg := cloudConfig{
		apiKey:              "key",
		endpoint:            srv.URL,
		profileInterval:     24 * time.Hour, // effectively disabled
		profileCPUSampleDur: time.Millisecond,
	}
	stop := startProfilePush("svc", attr.EmptySet, nil, cfg)
	stop(nil) //nolint

	// Allow goroutines to wind down.
	time.Sleep(50 * time.Millisecond)

	goroutinesAfter := runtime.NumGoroutine()
	if goroutinesAfter > goroutinesBefore+5 {
		t.Errorf("possible goroutine leak: before=%d after=%d", goroutinesBefore, goroutinesAfter)
	}
}
