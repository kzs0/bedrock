package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/metric"
	"github.com/kzs0/bedrock/metric/prometheus"
)

type metricCodecLabel struct{ Name, Value []byte }
type metricCodecBucket struct {
	Bound string
	Count uint64
}
type metricCodecSample struct {
	Labels  []metricCodecLabel
	Value   string
	Buckets []metricCodecBucket
	Count   uint64
	Sum     string
}
type metricCodecFamily struct {
	Name, Help []byte
	Type       string
	Samples    []metricCodecSample
}
type metricCodecRequest struct {
	Families    []metricCodecFamily
	RejectWrite bool
}

type metricRejectedWriter struct{ Calls int }

func (w *metricRejectedWriter) Write([]byte) (int, error) {
	w.Calls++
	return 0, errors.New("application destination unavailable")
}

func init() { fixtureScenarios["metric-codec"] = runMetricCodec }

func runMetricCodec(_ fixtureConfig) error {
	bc := bedrock.DefaultConfig()
	bc.ServerEnabled, bc.RuntimeMetrics = false, false
	bc.DisableEnvironmentDetection = true
	b, err := bedrock.New(bc)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /encode", func(w http.ResponseWriter, r *http.Request) {
		var request metricCodecRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		families := make([]metric.MetricFamily, 0, len(request.Families))
		for _, input := range request.Families {
			family := metric.MetricFamily{Name: string(input.Name), Help: string(input.Help), Type: metric.MetricType(input.Type)}
			for _, sample := range input.Samples {
				labels := make([]attr.Attr, 0, len(sample.Labels))
				for _, label := range sample.Labels {
					labels = append(labels, attr.String(string(label.Name), string(label.Value)))
				}
				m := metric.Metric{Labels: attr.NewSet(labels...), Count: sample.Count}
				for name, value := range map[string]string{"value": sample.Value, "sum": sample.Sum} {
					if value == "" {
						continue
					}
					number, err := strconv.ParseFloat(value, 64)
					if err != nil {
						http.Error(w, fmt.Sprint(err), 400)
						return
					}
					if name == "value" {
						m.Value = number
					} else {
						m.Sum = number
					}
				}
				for _, bucket := range sample.Buckets {
					bound, err := strconv.ParseFloat(bucket.Bound, 64)
					if err != nil {
						http.Error(w, fmt.Sprint(err), 400)
						return
					}
					m.Buckets = append(m.Buckets, metric.Bucket{UpperBound: bound, Count: bucket.Count})
				}
				family.Metrics = append(family.Metrics, m)
			}
			families = append(families, family)
		}
		before := make([]metric.MetricFamily, len(families))
		copy(before, families)
		var destination bytes.Buffer
		var rejected metricRejectedWriter
		var writer io.Writer = &destination
		if request.RejectWrite {
			writer = &rejected
		}
		encodeErr := prometheus.Encode(writer, families)
		message, status := "", http.StatusOK
		if encodeErr != nil {
			message, status = encodeErr.Error(), http.StatusBadRequest
			if request.RejectWrite {
				status = http.StatusServiceUnavailable
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"text": destination.String(), "error": message, "unchanged": reflect.DeepEqual(before, families), "write_calls": rejected.Calls})
	})
	return serveScenario(context.Background(), b, nil, mux, nil, nil)
}
