package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	common "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestPrivacyDefaultsAndExplicitOptIns(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		name := "defaults"
		if optIn {
			name = "explicit_opt_ins"
		}
		t.Run(name, func(t *testing.T) {
			traces := newReceiver(t)
			profiles := newReceiver(t)
			downstream := newReceiver(t)
			downstream.SetHandler(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			app := startApp(t, fixtureConfig{
				TraceEndpoint: traces.URL + "/v1/traces", TraceProtocol: "http",
				ProfileEndpoint: profiles.URL + "/v1/profiles", ProfileTypes: []string{"goroutine"},
				ProfileIntervalMillis: 200, ErrorDetails: optIn, ProfileLabels: optIn,
				DownstreamURL: downstream.URL + "/private-path-marker?token=secret-downstream-query-marker",
			})
			profileGET(t, app.ControlURL+"/workload", http.StatusNoContent)
			request, err := http.NewRequest(http.MethodGet, app.URL+"/secret?token=secret-query-marker", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer secret-header-marker")
			request.Header.Set("Cookie", "session=secret-cookie-marker")
			response := doBehaviorRequest(t, request)
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("secret route: %d", response.StatusCode)
			}
			metrics := string(profileGET(t, app.MetricsURL+"/metrics", http.StatusOK))
			profileDeadline := time.NewTimer(10 * time.Second)
			defer profileDeadline.Stop()
			profileCaptured := false
			for !profileCaptured {
				select {
				case export := <-profiles.Requests:
					decoded := decodeProfileExport(t, export)
					profileText, err := protojson.Marshal(decoded)
					if err != nil {
						t.Fatal(err)
					}
					assertPrivateMarkersAbsent(t, "profiles", string(profileText), optIn)
					if !strings.Contains(string(profileText), "visible-resource-marker") {
						t.Fatal("profile safe resource missing")
					}
					workloadLabel := false
					workloadStack := false
					for _, resource := range decoded.ResourceProfiles {
						for _, scope := range resource.ScopeProfiles {
							for _, profile := range scope.Profiles {
								functions := validateNativeProfile(t, decoded.Dictionary, profile, "goroutine")
								if strings.Contains(strings.Join(functions, "\n"), "fixtureGoroutine") {
									workloadStack = true
								}
								for _, sample := range profile.Samples {
									if !optIn && len(sample.AttributeIndices) != 0 {
										t.Fatal("profile labels enabled by default")
									}
									for _, index := range sample.AttributeIndices {
										attribute := decoded.Dictionary.AttributeTable[index]
										if profileString(t, decoded.Dictionary, attribute.KeyStrindex) == "workload" && strings.Contains(attribute.Value.String(), "fixture-workload-marker") {
											workloadLabel = true
										}
									}
								}
							}
						}
					}
					if !workloadStack || (optIn && !workloadLabel) {
						continue
					}
					if !optIn && strings.Contains(string(profileText), "fixture-workload-marker") {
						t.Fatal("default profile labels leaked into dictionary")
					}
					profileCaptured = true
				case <-profileDeadline.C:
					t.Fatalf("profile privacy export timed out\n%s", app.Logs())
				}
			}
			app.Stop(t)
			var spans []*tracepb.Span
			var traceText strings.Builder
			for {
				select {
				case export := <-traces.Requests:
					spans = append(spans, behaviorSpans(t, export)...)
					var document any
					if err := json.Unmarshal(export.Body, &document); err != nil {
						t.Fatal(err)
					}
					normalized, err := json.Marshal(document)
					if err != nil {
						t.Fatal(err)
					}
					traceText.Write(normalized)
				default:
					goto captured
				}
			}
		captured:
			assertPrivateMarkersAbsent(t, "traces", traceText.String(), optIn)
			if !strings.Contains(traceText.String(), "visible-resource-marker") {
				t.Fatal("trace safe resource missing")
			}
			foundOperation, foundEvent := false, false
			for _, span := range spans {
				if span.Name != "fixture.secret" {
					continue
				}
				foundOperation = true
				assertPrivacyAttributes(t, span.Attributes, optIn)
				for _, event := range span.Events {
					if event.Name == "fixture.secret.event" {
						foundEvent = true
						assertPrivacyAttributes(t, event.Attributes, optIn)
					}
				}
			}
			if !foundOperation || !foundEvent {
				t.Fatalf("privacy fixture operation/event missing: %t/%t", foundOperation, foundEvent)
			}
			logs := app.Logs()
			assertPrivateMarkersAbsent(t, "logs", logs, optIn)
			if !strings.Contains(logs, "fixture.secret.log") || !strings.Contains(logs, "visible-marker") {
				t.Fatal("structured privacy log missing")
			}
			if strings.Contains(logs, "secret-error-marker") != optIn {
				t.Fatal("log error detail opt-in not respected")
			}
			assertPrivateMarkersAbsent(t, "metrics", metrics, false)
			if !strings.Contains(metrics, "/secret") {
				t.Fatal("secret request metric missing")
			}
		})
	}
}

func assertPrivacyAttributes(t *testing.T, attributes []*common.KeyValue, optIn bool) {
	t.Helper()
	values := map[string]*common.AnyValue{}
	for _, attribute := range attributes {
		values[attribute.Key] = attribute.Value
	}
	if values["safe"].GetStringValue() != "visible-marker" {
		t.Fatal("safe attribute lost")
	}
	for _, key := range []string{"password", "authorization"} {
		if values[key].GetStringValue() != "[redacted]" {
			t.Fatalf("%s is not redacted: %v", key, values[key])
		}
	}
	nested := values["nested"].GetStringValue()
	if !strings.Contains(nested, "password:[redacted]") || !strings.Contains(nested, "safe:visible-marker") {
		t.Fatalf("nested filtering incorrect: %q", nested)
	}
	if values["error"] == nil {
		t.Fatal("error field missing")
	}
	if strings.Contains(values["error"].String(), "secret-error-marker") != optIn {
		t.Fatal("error detail opt-in not respected")
	}
}

func assertPrivateMarkersAbsent(t *testing.T, destination, value string, errorDetails bool) {
	t.Helper()
	markers := []string{"secret-password-marker", "secret-authorization-marker", "secret-nested-marker", "secret-profile-marker", "secret-host-marker", "secret-query-marker", "secret-header-marker", "secret-cookie-marker", "secret-downstream-query-marker", "private-path-marker"}
	if !errorDetails {
		markers = append(markers, "secret-error-marker")
	}
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			t.Errorf("%s leaked %q", destination, marker)
		}
	}
}
