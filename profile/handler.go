package profile

import (
	"net/http"
	"net/http/pprof"
)

// Handler returns an HTTP handler that serves pprof profiling data.
// The handler serves:
//   - /debug/pprof/           - Index page with links
//   - /debug/pprof/cmdline    - Running program's command line
//   - /debug/pprof/profile    - CPU profile (use ?seconds=N)
//   - /debug/pprof/symbol     - Symbol lookup
//   - /debug/pprof/trace      - Execution trace (use ?seconds=N)
//   - /debug/pprof/heap       - Heap memory profile
//   - /debug/pprof/goroutine  - Goroutine stack traces
//   - /debug/pprof/block      - Block profiling
//   - /debug/pprof/mutex      - Mutex profiling
//   - /debug/pprof/allocs     - Allocation profile
//   - /debug/pprof/threadcreate - Thread creation profile
func Handler() http.Handler {
	mux := http.NewServeMux()

	// Register pprof handlers
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	return mux
}

// RegisterHandlers registers pprof handlers on an existing ServeMux.
func RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}
