// Package profile provides optional HTTP debugging.
// Importing it also registers standard global pprof handlers.
package profile

import (
	"context"
	"net/http"
	"net/http/pprof"
	"strconv"
	"strings"
	"time"
)

var debugSlot = make(chan struct{}, 1)

// Handler returns explicit, bounded pprof debugging handlers.
// Protect these endpoints with application authentication.
func Handler() http.Handler { mux := http.NewServeMux(); RegisterHandlers(mux); return mux }

// RegisterHandlers installs GET-only debug endpoints.
// Collection lasts at most thirty seconds.
func RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /debug/pprof/", bounded(pprof.Index))
	mux.HandleFunc("GET /debug/pprof/cmdline", bounded(pprof.Cmdline))
	mux.HandleFunc("GET /debug/pprof/profile", bounded(pprof.Profile))
	mux.HandleFunc("GET /debug/pprof/symbol", bounded(pprof.Symbol))
	mux.HandleFunc("GET /debug/pprof/trace", bounded(pprof.Trace))
}
func bounded(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if raw := r.URL.Query().Get("seconds"); raw != "" {
			seconds, err := strconv.ParseFloat(raw, 64)
			if err != nil || seconds <= 0 || seconds > 30 {
				http.Error(w, "duration must be within thirty seconds", 400)
				return
			}
		}
		collecting := r.URL.Query().Get("seconds") != "" || strings.HasSuffix(r.URL.Path, "/profile") || strings.HasSuffix(r.URL.Path, "/trace")
		if collecting {
			select {
			case debugSlot <- struct{}{}:
				defer func() { <-debugSlot }()
			default:
				http.Error(w, "profiling is busy", http.StatusServiceUnavailable)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		next(w, r.WithContext(ctx))
	}
}
