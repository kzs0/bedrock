package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kzs0/bedrock/metric"
	"github.com/kzs0/bedrock/metric/prometheus"
)

// startMetricsPush starts a background goroutine that periodically gathers all
// registered metrics and POSTs them in Prometheus text exposition format to the
// managed platform. The returned function stops the goroutine and performs one
// final push before returning.
//
// Backpressure: if a push is still in progress when the next tick fires, that
// tick is skipped rather than queued — preventing goroutine accumulation when
// the server is slow.
func startMetricsPush(
	registry *metric.Registry,
	logger *slog.Logger,
	cfg cloudConfig,
) func(ctx context.Context) {
	endpoint := cfg.endpoint + "/v1/metrics"
	ticker := time.NewTicker(cfg.pushInterval)
	done := make(chan struct{})
	var pushing atomic.Bool

	push := func(ctx context.Context) {
		if !pushing.CompareAndSwap(false, true) {
			return // previous push still running
		}
		defer pushing.Store(false)

		if err := pushMetrics(ctx, registry, endpoint, cfg.apiKey); err != nil {
			if logger != nil {
				logger.Warn("metrics push failed", slog.String("error", err.Error()))
			}
		}
	}

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				push(context.Background())
			case <-done:
				return
			}
		}
	}()

	return func(ctx context.Context) {
		close(done)
		// Final push to capture last-moment metrics.
		push(ctx)
	}
}

// pushMetrics serializes the registry contents and POSTs them to the endpoint.
func pushMetrics(ctx context.Context, registry *metric.Registry, endpoint, apiKey string) error {
	families := registry.Gather()

	var buf bytes.Buffer
	if err := prometheus.Encode(&buf, families); err != nil {
		return fmt.Errorf("metrics encode: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return fmt.Errorf("metrics request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("metrics http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("metrics push: unexpected status %d", resp.StatusCode)
	}
	return nil
}
