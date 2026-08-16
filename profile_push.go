package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace/otlp"
)

// ProfileType identifies the kind of profile to collect.
type ProfileType string

const (
	ProfileTypeCPU       ProfileType = "cpu"
	ProfileTypeHeap      ProfileType = "heap"
	ProfileTypeGoroutine ProfileType = "goroutine"
	ProfileTypeMutex     ProfileType = "mutex"
)

// startProfilePush starts a background goroutine that periodically collects
// Go runtime profiles and sends them to the managed platform as OTLP Profiles
// requests. The returned function stops the goroutine.
//
// Profile collection is non-blocking to the ticker: if collection is already
// running when the next tick fires, that tick is skipped.
func startProfilePush(
	service string,
	resource attr.Set,
	logger *slog.Logger,
	cfg cloudConfig,
) func(ctx context.Context) {
	endpoint := cfg.endpoint + "/v1/profiles"
	profileInterval := cfg.profileInterval
	if profileInterval <= 0 {
		profileInterval = defaultProfileInterval
	}
	ticker := time.NewTicker(profileInterval)

	collect := func(ctx context.Context, pt ProfileType) error {
		return collectAndPushProfile(ctx, service, resource, endpoint, cfg.apiKey, cfg.profileCPUSampleDur, pt)
	}
	return startProfilePushWorker(ticker.C, ticker.Stop, logger, collect)
}

type collectProfileFunc func(context.Context, ProfileType) error

// startProfilePushWorker runs at most one profile collection at a time. The
// ticker and collector are injected so lifecycle behavior can be tested
// deterministically without relying on wall-clock sleeps.
func startProfilePushWorker(
	ticks <-chan time.Time,
	stopTicker func(),
	logger *slog.Logger,
	collect collectProfileFunc,
) func(context.Context) {
	workerCtx, cancel := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer close(workerDone)
		if stopTicker != nil {
			defer stopTicker()
		}

		var collectionDone <-chan struct{}
		for {
			select {
			case _, ok := <-ticks:
				if !ok {
					ticks = nil
					continue
				}
				if collectionDone != nil {
					continue // previous collection still running, skip this tick
				}
				done := make(chan struct{})
				collectionDone = done
				go func() {
					defer close(done)
					profiles := []ProfileType{ProfileTypeCPU, ProfileTypeHeap, ProfileTypeGoroutine, ProfileTypeMutex}
					for _, pt := range profiles {
						if workerCtx.Err() != nil {
							return
						}
						if err := collect(workerCtx, pt); err != nil {
							if workerCtx.Err() != nil {
								return
							}
							if logger != nil {
								logger.Warn("profile push failed",
									slog.String("type", string(pt)),
									slog.String("error", err.Error()),
								)
							}
						}
					}
				}()
			case <-collectionDone:
				collectionDone = nil
			case <-workerCtx.Done():
				if collectionDone != nil {
					<-collectionDone
				}
				return
			}
		}
	}()

	return func(ctx context.Context) {
		stopOnce.Do(cancel)
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case <-workerDone:
		case <-ctx.Done():
		}
	}
}

// collectAndPushProfile collects one profile type and pushes it to the endpoint.
func collectAndPushProfile(
	ctx context.Context,
	service string,
	resource attr.Set,
	endpoint, apiKey string,
	cpuSampleDur time.Duration,
	pt ProfileType,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	start := time.Now()
	var buf bytes.Buffer

	switch pt {
	case ProfileTypeCPU:
		if err := pprof.StartCPUProfile(&buf); err != nil {
			return fmt.Errorf("cpu profile start: %w", err)
		}
		waitErr := waitForProfileSample(ctx, cpuSampleDur)
		pprof.StopCPUProfile()
		if waitErr != nil {
			return fmt.Errorf("cpu profile wait: %w", waitErr)
		}

	case ProfileTypeHeap:
		p := pprof.Lookup("heap")
		if p == nil {
			return nil
		}
		if err := p.WriteTo(&buf, 0); err != nil {
			return fmt.Errorf("heap profile: %w", err)
		}

	case ProfileTypeGoroutine:
		p := pprof.Lookup("goroutine")
		if p == nil {
			return nil
		}
		if err := p.WriteTo(&buf, 0); err != nil {
			return fmt.Errorf("goroutine profile: %w", err)
		}

	case ProfileTypeMutex:
		p := pprof.Lookup("mutex")
		if p == nil {
			return nil
		}
		if err := p.WriteTo(&buf, 0); err != nil {
			return fmt.Errorf("mutex profile: %w", err)
		}

	default:
		return fmt.Errorf("unknown profile type: %s", pt)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	end := time.Now()

	encoded := otlp.EncodeProfile(service, resource, string(pt), buf.Bytes(), start, end)

	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("profile request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-profile-type", string(pt))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("profile http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("profile push: unexpected status %d for type %s", resp.StatusCode, pt)
	}
	return nil
}

func waitForProfileSample(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
