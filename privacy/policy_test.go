package privacy

import (
	"strings"
	"testing"

	"github.com/kzs0/bedrock/attr"
)

func TestPolicyFiltersNestedAndOpaqueValues(t *testing.T) {
	p := (*Policy)(nil).Snapshot()
	cases := []attr.Attr{attr.Any("payload", map[string]any{"password": "secret-value", "items": []any{map[string]any{"authorization": "secret-value"}}}), attr.Any("opaque", struct{ Password string }{"secret-value"}), attr.String("http.url", "https://user:secret-value@example.test/private/secret-value?token=secret-value#secret-value"), attr.String("error.stack", "secret-value")}
	for _, a := range cases {
		got, keep := p.Attribute(a)
		if !keep {
			continue
		}
		if strings.Contains(got.Value.String(), "secret-value") {
			t.Errorf("secret survived %s: %v", a.Key, got.Value)
		}
	}
}
func TestPolicyConfigurationAndLimits(t *testing.T) {
	p := (&Policy{AttributeKeys: []string{"keep"}, MaxStringLength: 3, Redact: func(_ string, v any) (any, bool) { return v, true }}).Snapshot()
	if _, keep := p.Attribute(attr.String("drop", "x")); keep {
		t.Fatal("allowlist ignored")
	}
	a, keep := p.Attribute(attr.String("keep", "abcdef"))
	if !keep || a.Value.String() != "abc[truncated]" {
		t.Fatal(a)
	}
	resource := p.Resource(attr.NewSet(attr.String("host.name", "hidden")))
	if resource.Len() != 0 {
		t.Fatal("infrastructure leaked")
	}
}
