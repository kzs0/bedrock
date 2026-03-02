package profile

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandler(t *testing.T) {
	h := Handler()
	if h == nil {
		t.Fatal("Handler returned nil")
	}

	req := httptest.NewRequest("GET", "/debug/pprof/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestRegisterHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHandlers(mux)

	// Verify the endpoints are registered by making requests
	tests := []struct {
		path string
	}{
		{"/debug/pprof/"},
		{"/debug/pprof/cmdline"},
		{"/debug/pprof/symbol"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			// Just verify it doesn't 404 (served by our handlers)
			if rr.Code == http.StatusNotFound {
				t.Errorf("expected handler for %s, got 404", tt.path)
			}
		})
	}
}
