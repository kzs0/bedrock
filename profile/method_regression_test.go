package profile

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProfilingHandlersRejectNonGETMethods(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/goroutine"} {
				w := httptest.NewRecorder()
				Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
				if w.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s status=%d, want 405", method, path, w.Code)
				}
			}
		})
	}
}
