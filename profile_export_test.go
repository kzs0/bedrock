package bedrock

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIndependentProfileExportDoesNotRequireCloud(t *testing.T) {
	received := make(chan struct{}, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/profiles" {
			t.Error(r.URL.Path)
		}
		if r.Header.Get("X-Test") != "configured" {
			t.Error("headers missing")
		}
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerEnabled = false
	cfg.Profiles = &ProfileExportConfig{Endpoint: server.URL + "/v1/profiles", AllowHTTP: true, Headers: map[string]string{"X-Test": "configured"}, Interval: 10 * time.Millisecond, Types: []ProfileType{ProfileTypeHeap}}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Shutdown(context.Background()) }()
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("profile not exported")
	}
	if b.ProfileStats().Failed != 0 {
		t.Fatal(b.ProfileStats())
	}
}
func TestProfileExportConfigurationRejectsUnsafeValues(t *testing.T) {
	for _, cfg := range []ProfileExportConfig{{Endpoint: "http://localhost:1234/v1/profiles"}, {Endpoint: "https://example.test/v1/profiles", TLSConfig: &tls.Config{InsecureSkipVerify: true}}, {Endpoint: "https://example.test/v1/profiles", Types: []ProfileType{"invalid"}}, {Endpoint: "https://example.test/v1/profiles", Interval: time.Second, CPUSampleDuration: time.Minute}, {Endpoint: "https://example.test/v1/profiles", Retries: 4}} {
		if _, err := normalizeProfileConfig(cfg); err == nil {
			t.Fatal("invalid profile configuration accepted")
		}
	}
}
func TestEmptyProfileSelectionDisablesCollection(t *testing.T) {
	cfg, err := normalizeProfileConfig(ProfileExportConfig{Endpoint: "https://example.test/v1/profiles", Types: []ProfileType{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Types) != 0 {
		t.Fatal("empty selection replaced")
	}
}

func TestShutdownOwnsExplicitProfileWorkers(t *testing.T) {
	b, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.StartProfileExport(ProfileExportConfig{Endpoint: "https://example.test/v1/profiles", Interval: time.Hour, Types: []ProfileType{ProfileTypeHeap}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.profileStops) != 1 {
		t.Fatal("worker cleanup not registered")
	}
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StartProfileExport(ProfileExportConfig{Endpoint: "https://example.test/v1/profiles"}); err == nil {
		t.Fatal("profiling restarted after shutdown")
	}
}
