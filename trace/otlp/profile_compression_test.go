package otlp

import (
	"bytes"
	"compress/gzip"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
)

func TestProfileEncoderRejectsNestedGzip(t *testing.T) {
	var outer bytes.Buffer
	gz := gzip.NewWriter(&outer)
	if _, err := gz.Write(profileFixture(t)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := EncodeProfileChecked("svc", attr.EmptySet, "heap", outer.Bytes(), now, now, ProfileEncodingOptions{}); err == nil {
		t.Fatal("nested gzip bypasses explicit expansion bound")
	}
}

func TestProfileAttributeBoundsReturnErrors(t *testing.T) {
	deep := any("leaf")
	for i := 0; i < 10; i++ {
		deep = []any{deep}
	}
	tree := func() any {
		var build func(int) any
		build = func(depth int) any {
			if depth == 0 {
				return "leaf"
			}
			return []any{build(depth - 1), build(depth - 1), build(depth - 1), build(depth - 1)}
		}
		return build(7)
	}
	cycle := make([]any, 1)
	cycle[0] = cycle
	for name, value := range map[string]any{"array": make([]any, 100), "depth": deep, "nodes": tree(), "cycle": cycle} {
		t.Run(name, func(t *testing.T) {
			var b protoBuf
			budget := profileValueBudget{nodes: 4096, bytes: maxProfileBytes}
			if err := appendProfileValue(&b, 2, value, 0, &budget); err == nil {
				t.Fatal("attribute silently exceeded its bound")
			}
		})
	}
}
