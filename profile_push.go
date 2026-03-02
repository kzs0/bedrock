package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/pprof"
	"sync/atomic"
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
	ticker := time.NewTicker(cfg.profileInterval)
	done := make(chan struct{})
	var collecting atomic.Bool

	collect := func() {
		if !collecting.CompareAndSwap(false, true) {
			return // previous collection still running, skip
		}
		defer collecting.Store(false)

		profiles := []ProfileType{ProfileTypeCPU, ProfileTypeHeap, ProfileTypeGoroutine, ProfileTypeMutex}
		for _, pt := range profiles {
			if err := collectAndPushProfile(service, resource, endpoint, cfg.apiKey, cfg.profileCPUSampleDur, pt); err != nil {
				if logger != nil {
					logger.Warn("profile push failed",
						slog.String("type", string(pt)),
						slog.String("error", err.Error()),
					)
				}
			}
		}
	}

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				go collect() // run collection in separate goroutine to not block ticker
			case <-done:
				return
			}
		}
	}()

	return func(_ context.Context) {
		close(done)
		// Best-effort: no wait for in-flight collection (profiles are non-critical).
	}
}

// collectAndPushProfile collects one profile type and pushes it to the endpoint.
func collectAndPushProfile(
	service string,
	resource attr.Set,
	endpoint, apiKey string,
	cpuSampleDur time.Duration,
	pt ProfileType,
) error {
	start := time.Now()
	var buf bytes.Buffer

	switch pt {
	case ProfileTypeCPU:
		if err := pprof.StartCPUProfile(&buf); err != nil {
			return fmt.Errorf("cpu profile start: %w", err)
		}
		time.Sleep(cpuSampleDur)
		pprof.StopCPUProfile()

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

	end := time.Now()

	encoded := otlp.EncodeProfile(service, resource, string(pt), buf.Bytes(), start, end)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
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
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("profile push: unexpected status %d for type %s", resp.StatusCode, pt)
	}
	return nil
}
