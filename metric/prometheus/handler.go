package prometheus

import (
	"net/http"

	"github.com/kzs0/bedrock/metric"
)

// Handler returns an HTTP handler that serves metrics in Prometheus format.
func Handler(registry *metric.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		families := registry.Gather()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		if err := Encode(w, families); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})
}
