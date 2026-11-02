package bedrock

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace/otlp"
)

// ProfileExportConfig configures OTLP HTTP profile exports.
// Endpoint includes the /v1/profiles path.
type ProfileExportConfig struct {
	Endpoint            string
	Headers             map[string]string
	TLSConfig           *tls.Config
	AllowHTTP           bool
	Timeout             time.Duration
	Interval            time.Duration
	CPUSampleDuration   time.Duration
	Types               []ProfileType
	MaxBytes            int
	Retries             int
	MutexSampleFraction int
	BlockSampleRate     time.Duration
}

// ProfileExportStats reports collection and delivery outcomes.
type ProfileExportStats struct {
	Exported uint64
	Failed   uint64
	Skipped  uint64
}

type profileCounters struct{ exported, failed, skipped atomic.Uint64 }

// ProfileStats returns cumulative profiling outcomes.
func (b *Bedrock) ProfileStats() ProfileExportStats {
	return ProfileExportStats{b.profileStats.exported.Load(), b.profileStats.failed.Load(), b.profileStats.skipped.Load()}
}

func normalizeProfileConfig(cfg ProfileExportConfig) (ProfileExportConfig, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return cfg, fmt.Errorf("bedrock: profiles require HTTPS or explicit AllowHTTP")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return cfg, fmt.Errorf("bedrock: unsupported profile protocol")
	}
	if u.Scheme == "http" && !cfg.AllowHTTP {
		return cfg, fmt.Errorf("bedrock: plaintext profiles require AllowHTTP")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Interval == 0 {
		cfg.Interval = time.Minute
	}
	if cfg.CPUSampleDuration == 0 {
		cfg.CPUSampleDuration = 10 * time.Second
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 8 << 20
	}
	if cfg.Timeout < 0 || cfg.Interval <= 0 || cfg.CPUSampleDuration <= 0 || cfg.MaxBytes <= 0 || cfg.MaxBytes > 32<<20 || cfg.Retries < 0 || cfg.Retries > 3 || cfg.MutexSampleFraction < 0 || cfg.BlockSampleRate < 0 {
		return cfg, fmt.Errorf("bedrock: invalid profile export configuration")
	}
	if cfg.Types == nil {
		cfg.Types = []ProfileType{ProfileTypeCPU, ProfileTypeHeap, ProfileTypeGoroutine}
	}
	cfg.Types = append([]ProfileType{}, cfg.Types...)
	for _, typ := range cfg.Types {
		if !slices.Contains([]ProfileType{ProfileTypeCPU, ProfileTypeHeap, ProfileTypeGoroutine, ProfileTypeMutex, ProfileTypeBlock}, typ) {
			return cfg, fmt.Errorf("bedrock: unknown profile type %q", typ)
		}
	}
	if slices.Contains(cfg.Types, ProfileTypeCPU) && cfg.CPUSampleDuration >= cfg.Interval {
		return cfg, fmt.Errorf("bedrock: CPU window must be shorter than interval")
	}
	headers := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	cfg.Headers = headers
	if cfg.TLSConfig != nil {
		cfg.TLSConfig = cfg.TLSConfig.Clone()
		if cfg.TLSConfig.InsecureSkipVerify {
			return cfg, fmt.Errorf("bedrock: TLS verification cannot be disabled")
		}
	}
	return cfg, nil
}

var contentionSettings sync.Mutex

// StartProfileExport starts bounded, export-only profiling.
// The returned stop function cancels and joins.
func (b *Bedrock) StartProfileExport(cfg ProfileExportConfig) (func(context.Context), error) {
	cfg, err := normalizeProfileConfig(cfg)
	if err != nil {
		return nil, err
	}
	b.profileMu.Lock()
	defer b.profileMu.Unlock()
	if b.profileClosed {
		return nil, fmt.Errorf("bedrock: profiling after shutdown")
	}
	if len(b.profileStops) >= 8 {
		return nil, fmt.Errorf("bedrock: profile exporter limit exceeded")
	}
	contentionSettings.Lock()
	if cfg.MutexSampleFraction > 0 {
		current := runtime.SetMutexProfileFraction(-1)
		if current != 0 && current != cfg.MutexSampleFraction {
			contentionSettings.Unlock()
			return nil, fmt.Errorf("bedrock: mutex sampler already configured")
		}
		runtime.SetMutexProfileFraction(cfg.MutexSampleFraction)
	}
	if cfg.BlockSampleRate > 0 {
		runtime.SetBlockProfileRate(int(cfg.BlockSampleRate))
	}
	contentionSettings.Unlock()
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	if cfg.TLSConfig != nil {
		transport.TLSClientConfig = cfg.TLSConfig
	}
	client := &http.Client{Transport: transport, Timeout: cfg.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	ticker := time.NewTicker(cfg.Interval)
	collect := func(ctx context.Context, pt ProfileType) error {
		if !slices.Contains(cfg.Types, pt) {
			return nil
		}
		if pt == ProfileTypeMutex && runtime.SetMutexProfileFraction(-1) == 0 {
			b.profileStats.skipped.Add(1)
			return nil
		}
		if pt == ProfileTypeBlock && cfg.BlockSampleRate == 0 {
			b.profileStats.skipped.Add(1)
			return nil
		}
		err := collectAndPushProfileWithConfig(ctx, b.config.Service, b.staticAttr, cfg, client, pt, otlp.ProfileEncodingOptions{SourcePaths: b.policy.AllowSourcePaths, LabelFilter: func(a attr.Attr) (attr.Attr, bool) {
			if !b.policy.AllowProfileLabels {
				return attr.Attr{}, false
			}
			return b.policy.Attribute(a)
		}})
		if errors.Is(err, ErrCPUProfilerBusy) {
			b.profileStats.skipped.Add(1)
			return nil
		}
		if err != nil {
			b.profileStats.failed.Add(1)
			return err
		}
		b.profileStats.exported.Add(1)
		return nil
	}
	stop := startProfilePushWorker(ticker.C, ticker.Stop, b.logger, collect)
	joined := func(ctx context.Context) { stop(ctx); transport.CloseIdleConnections() }
	b.profileStops = append(b.profileStops, joined)
	return joined, nil
}
