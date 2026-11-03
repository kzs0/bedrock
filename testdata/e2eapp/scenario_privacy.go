package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/privacy"
	"github.com/kzs0/bedrock/trace/otlp"
)

func init() { fixtureScenarios["privacy"] = privacyScenario }

func privacyScenario(cfg fixtureConfig) error {
	bc := bedrock.DefaultConfig()
	bc.Service = "privacy-contract"
	bc.ServerEnabled = false
	bc.DisableEnvironmentDetection = true
	bc.TraceURL, bc.TraceProtocol = cfg.TraceEndpoint, "http"
	bc.LogCanonical = true
	bc.DataPolicy = &privacy.Policy{AllowErrorDetails: true, AllowSourcePaths: true, ResourceKeys: []string{"host.name", "service.namespace"}, MaxStringLength: 24, Redact: func(key string, value any) (any, bool) {
		if key == "removed" {
			return nil, false
		}
		if key == "custom" {
			return "custom-redacted", true
		}
		return value, true
	}}
	b, err := bedrock.New(bc, attr.String("host.name", "explicit-host-marker"), attr.String("service.namespace", "safe-resource-marker"))
	if err != nil {
		return err
	}
	ctx := bedrock.WithBedrock(context.Background(), b)
	app := http.NewServeMux()
	app.HandleFunc("/policy", func(w http.ResponseWriter, r *http.Request) {
		policy := bc.DataPolicy.Snapshot()
		if r.URL.Query().Get("allow") == "selected" {
			policy.AttributeKeys = []string{"nested.safe", "visible", "host.name", "service.namespace"}
		}
		if r.URL.Query().Get("allow") == "empty" {
			policy.AttributeKeys = []string{}
		}
		cycle := map[string]any{}
		cycle["self"] = cycle
		shared := map[string]any{"safe": "shared-marker"}
		deep := any("deep-marker")
		for range 12 {
			deep = []any{deep}
		}
		broad := map[string]any{}
		for i := range 65 {
			broad[fmt.Sprint(i)] = i
		}
		wide := make([]int, 65)
		attrs := []attr.Attr{
			attr.String("visible", "safe-marker"), attr.String("password", "secret-never-visible"), attr.String("custom", "replace-this"), attr.String("removed", "drop-this"),
			attr.Any("nested", map[string]any{"safe": "nested-safe", "password": "secret-nested"}), attr.Any("cycle", cycle), attr.Any("shared", []any{shared, shared}),
			attr.Any("deep", deep), attr.Any("broad", broad), attr.Any("wide", wide), attr.Any("unsupported", map[int]string{1: "secret-map"}), attr.Any("pointer", new(int)),
			attr.Any("function", func() {}), attr.Any("channel", make(chan int)), attr.Any("struct", struct{ Value string }{"secret-struct"}), attr.Any("nil", nil), attr.Time("time", time.Unix(1, 0).UTC()),
			attr.String("utf8", "a\xffb"), attr.String("long", "abcdefghijklmnopqrstuvwxyz界"), attr.String("http.url", "https://user:pass@example.test/private?token=secret#fragment"), attr.String("bad.url", "http://[invalid"),
			attr.String("error.message", "reviewed-error"), attr.String("code.filepath", "/reviewed/source.go"), attr.Bool("boolean", true), attr.Int("integer", 17),
		}
		filtered := policy.Apply(attr.NewSet(attrs...))
		values := map[string]any{}
		filtered.Range(func(a attr.Attr) bool { values[a.Key] = a.Value.AsAny(); return true })
		op, opctx := bedrock.Operation(ctx, "privacy.contract")
		op.Register(opctx, attrs...)
		op.Done()
		b.Logger().WithGroup("application").LogAttrs(opctx, slog.LevelInfo, "privacy.contract.log", slog.String("visible", "safe-marker"), slog.String("password", "secret-log"))
		_ = json.NewEncoder(w).Encode(values)
	})
	app.HandleFunc("/encode-profile", func(w http.ResponseWriter, r *http.Request) {
		raw, readErr := io.ReadAll(io.LimitReader(r.Body, (33<<20)+1))
		if readErr != nil {
			http.Error(w, readErr.Error(), 400)
			return
		}
		options := otlp.ProfileEncodingOptions{SourcePaths: r.URL.Query().Get("paths") == "full"}
		if mode := r.URL.Query().Get("filter"); mode != "none" {
			options.LabelFilter = func(a attr.Attr) (attr.Attr, bool) { return privacyProfileLabel(mode, a) }
		}
		start := time.Unix(100, 0)
		end := start.Add(time.Second)
		if r.URL.Query().Get("reverse") == "true" {
			end = start.Add(-time.Second)
		}
		encoded, encodeErr := otlp.EncodeProfileChecked("codec-contract", attr.NewSet(attr.String("safe.resource", "codec-marker")), "goroutine", raw, start, end, options)
		if encodeErr != nil {
			http.Error(w, encodeErr.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(encoded)
	})
	app.HandleFunc("/profile-config", func(w http.ResponseWriter, r *http.Request) {
		var options struct{ Kind string }
		_ = json.NewDecoder(r.Body).Decode(&options)
		pc := bedrock.ProfileExportConfig{Endpoint: "http://127.0.0.1:1/v1/profiles", AllowHTTP: true, Interval: time.Hour, CPUSampleDuration: time.Second, Types: []bedrock.ProfileType{bedrock.ProfileTypeGoroutine}}
		switch options.Kind {
		case "scheme":
			pc.Endpoint = "ftp://example.test/profile"
		case "plain":
			pc.AllowHTTP = false
		case "credentials":
			pc.Endpoint = "https://user:pass@example.test/profile"
		case "query":
			pc.Endpoint = "https://example.test/profile?q=1"
		case "fragment":
			pc.Endpoint = "https://example.test/profile#x"
		case "timeout":
			pc.Timeout = -1
		case "interval":
			pc.Interval = -1
		case "window":
			pc.CPUSampleDuration = -1
		case "bytes":
			pc.MaxBytes = -1
		case "oversize":
			pc.MaxBytes = 33 << 20
		case "retries":
			pc.Retries = 4
		case "negative-retries":
			pc.Retries = -1
		case "type":
			pc.Types = []bedrock.ProfileType{"unknown"}
		case "cpu":
			pc.Types = []bedrock.ProfileType{bedrock.ProfileTypeCPU}
			pc.CPUSampleDuration = pc.Interval
		case "mutex":
			pc.MutexSampleFraction = -1
		case "block":
			pc.BlockSampleRate = -1
		case "tls":
			pc.TLSConfig = &tls.Config{InsecureSkipVerify: true}
		case "disabled":
			pc.Types = []bedrock.ProfileType{}
		}
		stop, startErr := b.StartProfileExport(pc)
		if startErr != nil {
			http.Error(w, startErr.Error(), 400)
			return
		}
		stop(context.Background())
		w.WriteHeader(204)
	})
	app.HandleFunc("/profile-lifecycle", func(w http.ResponseWriter, _ *http.Request) {
		pc := bedrock.ProfileExportConfig{Endpoint: "http://127.0.0.1:1/v1/profiles", AllowHTTP: true, Interval: time.Hour, CPUSampleDuration: time.Second, Types: []bedrock.ProfileType{}}
		previous := runtime.SetMutexProfileFraction(7)
		pc.MutexSampleFraction = 3
		_, conflict := b.StartProfileExport(pc)
		runtime.SetMutexProfileFraction(previous)
		pc.MutexSampleFraction = 0
		admitted := 0
		for range 8 {
			stop, err := b.StartProfileExport(pc)
			if err != nil {
				break
			}
			admitted++
			stop(context.Background())
			stop(context.Background())
		}
		_, capped := b.StartProfileExport(pc)
		shutdownErr := b.Shutdown(ctx)
		_, closed := b.StartProfileExport(pc)
		_ = json.NewEncoder(w).Encode(map[string]any{"mutex_conflict": conflict != nil, "admitted": admitted, "capped": capped != nil, "closed": closed != nil, "shutdown": shutdownErr == nil})
	})
	app.HandleFunc("/profile-skip", func(w http.ResponseWriter, _ *http.Request) {
		previous := runtime.SetMutexProfileFraction(0)
		defer runtime.SetMutexProfileFraction(previous)
		stop, startErr := b.StartProfileExport(bedrock.ProfileExportConfig{Endpoint: cfg.ProfileEndpoint, AllowHTTP: true, Types: []bedrock.ProfileType{bedrock.ProfileTypeMutex, bedrock.ProfileTypeBlock}, Interval: 20 * time.Millisecond, CPUSampleDuration: time.Millisecond})
		if startErr != nil {
			http.Error(w, startErr.Error(), 500)
			return
		}
		deadline := time.Now().Add(time.Second)
		for b.ProfileStats().Skipped < 2 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		stop(context.Background())
		_ = json.NewEncoder(w).Encode(b.ProfileStats())
	})
	app.HandleFunc("/profile-cancel", func(w http.ResponseWriter, _ *http.Request) {
		stop, startErr := b.StartProfileExport(bedrock.ProfileExportConfig{Endpoint: cfg.ProfileEndpoint, AllowHTTP: true, Types: []bedrock.ProfileType{bedrock.ProfileTypeCPU}, Interval: 2 * time.Second, CPUSampleDuration: 1500 * time.Millisecond})
		if startErr != nil {
			http.Error(w, startErr.Error(), 500)
			return
		}
		defer stop(context.Background())
		owned := false
		time.Sleep(2050 * time.Millisecond)
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			if pprof.StartCPUProfile(io.Discard) != nil {
				owned = true
				break
			}
			pprof.StopCPUProfile()
			time.Sleep(10 * time.Millisecond)
		}
		before := time.Now()
		stop(context.Background())
		elapsed := time.Since(before)
		released := pprof.StartCPUProfile(io.Discard) == nil
		if released {
			pprof.StopCPUProfile()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"owned": owned, "released": released, "bounded": elapsed < time.Second, "stats": b.ProfileStats()})
	})
	return serveScenario(ctx, b, nil, app, nil, nil)
}

func privacyProfileLabel(mode string, a attr.Attr) (attr.Attr, bool) {
	if mode == "drop" {
		return attr.Attr{}, false
	}
	if mode == "merge" {
		return a.WithKey("merged"), true
	}
	if mode == "merge-array" {
		return attr.Any("merged", []int{1, 2}), true
	}
	switch a.Key {
	case "bool":
		return attr.Bool(a.Key, true), true
	case "false":
		return attr.Bool(a.Key, false), true
	case "float":
		return attr.Float64(a.Key, 1.25), true
	case "int":
		return attr.Int(a.Key, -5), true
	case "uint":
		return attr.Uint64(a.Key, 1<<63), true
	case "uint_max":
		return attr.Uint64(a.Key, ^uint64(0)), true
	case "uint_intmax":
		return attr.Uint64(a.Key, 1<<63-1), true
	case "nil":
		return attr.Any(a.Key, nil), true
	case "map":
		return attr.Any(a.Key, map[string]any{"n": 3, "s": "map-marker", "b": true}), true
	case "array":
		return attr.Any(a.Key, []any{1, "array-marker", false}), true
	case "array65":
		return attr.Any(a.Key, make([]int, 65)), true
	case "map65":
		m := map[string]any{}
		for i := range 65 {
			m[fmt.Sprint(i)] = i
		}
		return attr.Any(a.Key, m), true
	case "badmap":
		return attr.Any(a.Key, map[int]int{1: 1}), true
	case "deep":
		var v any = 1
		for range 10 {
			v = []any{v}
		}
		return attr.Any(a.Key, v), true
	case "duration":
		return attr.Duration(a.Key, time.Second), true
	case "time":
		return attr.Time(a.Key, time.Unix(1, 0)), true
	case "error":
		return attr.Any(a.Key, errors.New("typed-error")), true
	case "nodes":
		v := make([]any, 64)
		for i := range v {
			v[i] = make([]int, 64)
		}
		return attr.Any(a.Key, v), true
	}
	return a, true
}
