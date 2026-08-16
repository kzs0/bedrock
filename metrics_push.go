package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kzs0/bedrock/metric"
	"github.com/kzs0/bedrock/metric/prometheus"
)

const maxMetricsPushDuration = 10 * time.Second

// startMetricsPush starts a background goroutine that periodically gathers all
// registered metrics and POSTs them in Prometheus text exposition format to the
// managed platform. The returned function initiates shutdown and one final
// push, then waits for that shared shutdown lifecycle up to the caller's context
// deadline. A later call can continue waiting after an earlier call times out.
//
// Backpressure: pushes run synchronously in the worker, so the ticker can retain
// at most one pending tick and no push goroutines accumulate. A pending tick may
// run immediately after a slow push completes.
func startMetricsPush(
	registry *metric.Registry,
	logger *slog.Logger,
	cfg cloudConfig,
) func(ctx context.Context) {
	endpoint := cfg.endpoint + "/v1/metrics"
	pushInterval := cfg.pushInterval
	if pushInterval <= 0 {
		pushInterval = defaultCloudPushInterval
	}
	ticker := time.NewTicker(pushInterval)
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	var stopOnce sync.Once
	shutdownDone := make(chan struct{})

	push := func(ctx context.Context) {
		if err := pushMetrics(ctx, registry, endpoint, cfg.apiKey); err != nil {
			if logger != nil && ctx.Err() == nil {
				logger.Warn("metrics push failed", slog.String("error", err.Error()))
			}
		}
	}

	go func() {
		defer close(workerDone)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				timeout := min(pushInterval, maxMetricsPushDuration)
				pushCtx, cancelPush := context.WithTimeout(workerCtx, timeout)
				push(pushCtx)
				cancelPush()
			case <-workerCtx.Done():
				return
			}
		}
	}()

	return func(ctx context.Context) {
		stopOnce.Do(func() {
			cancelWorker()
			go func() {
				defer close(shutdownDone)
				<-workerDone
				// Capture metrics recorded while the periodic worker was stopping.
				// The lifecycle continues independently if a shutdown caller times
				// out; later callers can join the same one final push.
				push(context.Background())
			}()
		})
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case <-shutdownDone:
		case <-ctx.Done():
		}
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

	client := &http.Client{Timeout: maxMetricsPushDuration}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("metrics http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("metrics push: unexpected status %d", resp.StatusCode)
	}
	return nil
}
