package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestProfileTLSAuthenticationAndTrust(t *testing.T) {
	requests := make(chan exportRequest, 16)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			http.Error(w, "read", 400)
			return
		}
		select {
		case requests <- exportRequest{r.URL.Path, r.Header.Clone(), body}:
		default:
		}
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			http.Error(w, "authorization", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	caFile := writeSecurityCA(t, server.Certificate().Raw)
	t.Run("trusted", func(t *testing.T) {
		app := startApp(t, fixtureConfig{ProfileEndpoint: server.URL + "/v1/profiles", ProfileCAFile: caFile, ProfileHeaders: map[string]string{"Authorization": "Bearer fixture-token"}, ProfileTypes: []string{"goroutine"}})
		export := nextBehaviorExport(t, requests)
		if export.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Fatal("missing authorization")
		}
		request := decodeProfileExport(t, export)
		found := false
		for _, resource := range request.ResourceProfiles {
			for _, scope := range resource.ScopeProfiles {
				for _, profile := range scope.Profiles {
					kind := profileKind(t, request.Dictionary, profile)
					if kind != "goroutine" {
						t.Fatalf("profile type %q", kind)
					}
					validateNativeProfile(t, request.Dictionary, profile, kind)
					found = true
				}
			}
		}
		if !found {
			t.Fatal("TLS export lacks native profiles")
		}
		awaitSecurityProfileStats(t, app, func(exported, failed uint64) bool { return exported > 0 && failed == 0 })
		app.Stop(t)
	})
	for {
		select {
		case <-requests:
			continue
		default:
			goto drained
		}
	}
drained:
	t.Run("untrusted", func(t *testing.T) {
		wrongCA := unrelatedSecurityCA(t)
		app := startApp(t, fixtureConfig{ProfileEndpoint: server.URL + "/v1/profiles", ProfileCAFile: wrongCA, ProfileHeaders: map[string]string{"Authorization": "Bearer fixture-token"}, ProfileTypes: []string{"goroutine"}})
		awaitSecurityProfileStats(t, app, func(exported, failed uint64) bool { return failed > 0 && exported == 0 })
		app.Stop(t)
		select {
		case request := <-requests:
			t.Fatalf("untrusted TLS delivered application payload: %s", request.Path)
		default:
		}
	})
}

func TestProfileRedirectDoesNotForwardCredentials(t *testing.T) {
	destination := newReceiver(t)
	var redirected atomic.Uint64
	origin := newReceiver(t)
	origin.SetHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer redirect-secret" {
			redirected.Add(1)
		}
		w.Header().Set("Location", destination.URL+"/v1/profiles")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	app := startApp(t, fixtureConfig{ProfileEndpoint: origin.URL + "/v1/profiles", ProfileHeaders: map[string]string{"Authorization": "Bearer redirect-secret"}, ProfileTypes: []string{"goroutine"}})
	request := decodeProfileExport(t, nextBehaviorExport(t, origin.Requests))
	if len(request.ResourceProfiles) == 0 {
		t.Fatal("missing profile export")
	}
	awaitSecurityProfileStats(t, app, func(exported, failed uint64) bool { return failed > 0 && exported == 0 })
	app.Stop(t)
	if redirected.Load() == 0 {
		t.Fatal("origin did not receive credentials")
	}
	select {
	case request := <-destination.Requests:
		t.Fatalf("redirect forwarded export with headers %v", request.Header)
	default:
	}
}

func writeSecurityCA(t *testing.T, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func unrelatedSecurityCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unrelated fixture CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return writeSecurityCA(t, der)
}

func awaitSecurityProfileStats(t *testing.T, app *fixtureApp, ready func(uint64, uint64) bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var stats struct {
		Profiles struct{ Exported, Failed uint64 }
	}
	for {
		response, err := (&http.Client{Timeout: time.Second}).Get(app.ControlURL + "/stats")
		if err != nil {
			t.Fatal(err)
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&stats)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if ready(stats.Profiles.Exported, stats.Profiles.Failed) {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("profile outcome deadline exceeded: %+v\n%s", stats, app.Logs())
		}
	}
}
