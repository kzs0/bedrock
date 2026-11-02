package trace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/privacy"
)

func TestSpanPrivacyCoversEveryMutation(t *testing.T) {
	p := &privacy.Policy{Redact: func(key string, v any) (any, bool) {
		if key == "customer" {
			return "[redacted]", true
		}
		return v, true
	}}
	tracer := NewTracer(TracerConfig{DataPolicy: p})
	_, s := tracer.Start(context.Background(), "test", WithAttrs(attr.String("password", "secret-value")))
	s.SetAttr(attr.String("customer", "secret-value"))
	s.AddEvent("test", attr.Any("payload", map[string]any{"api_key": "secret-value"}))
	s.RecordError(errors.New("secret-value"))
	s.SetStatus(StatusError, "secret-value")
	for _, a := range s.Attrs().Attrs() {
		if strings.Contains(a.Value.String(), "secret-value") {
			t.Error(a)
		}
	}
	for _, e := range s.Events() {
		for _, a := range e.Attrs.Attrs() {
			if strings.Contains(a.Value.String(), "secret-value") {
				t.Error(a)
			}
		}
	}
	_, message := s.Status()
	if strings.Contains(message, "secret-value") {
		t.Fatal("status bypassed policy")
	}
}
