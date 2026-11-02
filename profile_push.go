package bedrock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	ProfileTypeBlock     ProfileType = "block"
)

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
					profiles := []ProfileType{ProfileTypeCPU, ProfileTypeHeap, ProfileTypeGoroutine, ProfileTypeMutex, ProfileTypeBlock}
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
	cfg := ProfileExportConfig{Endpoint: endpoint, Headers: map[string]string{"Authorization": "Bearer " + apiKey}, Timeout: 30 * time.Second, CPUSampleDuration: cpuSampleDur, MaxBytes: 8 << 20}
	return collectAndPushProfileWithConfig(ctx, service, resource, cfg, http.DefaultClient, pt, otlp.ProfileEncodingOptions{})
}

// ErrCPUProfilerBusy identifies conflicting CPU profiling.
var ErrCPUProfilerBusy = errors.New("CPU profiler already in use")

var cpuProfileSlot = make(chan struct{}, 1)

type limitedProfileBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedProfileBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		return 0, fmt.Errorf("profile exceeds collection limit")
	}
	return b.Buffer.Write(data)
}

func collectAndPushProfileWithConfig(ctx context.Context, service string, resource attr.Set, cfg ProfileExportConfig, client *http.Client, pt ProfileType, encoding otlp.ProfileEncodingOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	start := time.Now()
	var buf limitedProfileBuffer
	buf.limit = cfg.MaxBytes
	if buf.limit <= 0 {
		buf.limit = 8 << 20
	}

	switch pt {
	case ProfileTypeCPU:
		if cfg.CPUSampleDuration <= 0 {
			return fmt.Errorf("cpu profile duration must be positive")
		}
		select {
		case cpuProfileSlot <- struct{}{}:
			defer func() { <-cpuProfileSlot }()
		default:
			return ErrCPUProfilerBusy
		}
		if err := pprof.StartCPUProfile(&buf); err != nil {
			return fmt.Errorf("cpu profile start: %w: %v", ErrCPUProfilerBusy, err)
		}
		waitErr := waitForProfileSample(ctx, cfg.CPUSampleDuration)
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

	case ProfileTypeMutex, ProfileTypeBlock:
		p := pprof.Lookup(string(pt))
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

	encoded, err := otlp.EncodeProfileChecked(service, resource, string(pt), buf.Bytes(), start, end, encoding)
	if err != nil {
		return err
	}
	if len(encoded) > buf.limit {
		return fmt.Errorf("profile exceeds upload limit")
	}

	requestCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	for attempt := 0; attempt <= cfg.Retries; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 100 * time.Millisecond)
			select {
			case <-requestCtx.Done():
				timer.Stop()
				return requestCtx.Err()
			case <-timer.C:
			}
		}
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, cfg.Endpoint, bytes.NewReader(encoded))
		if err != nil {
			return fmt.Errorf("profile request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		for key, value := range cfg.Headers {
			req.Header.Set(key, value)
		}
		req.Header.Set("x-profile-type", string(pt))
		resp, err := client.Do(req)
		if err != nil {
			if attempt < cfg.Retries && requestCtx.Err() == nil {
				continue
			}
			if requestCtx.Err() != nil {
				return requestCtx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return context.DeadlineExceeded
			}
			return fmt.Errorf("profile HTTP delivery failed")
		}
		status := resp.StatusCode
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if status >= 200 && status < 300 {
			return nil
		}
		if (status == 429 || status >= 500) && attempt < cfg.Retries {
			continue
		}
		return fmt.Errorf("profile push returned status %d", status)
	}
	return fmt.Errorf("profile retries exhausted")
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
