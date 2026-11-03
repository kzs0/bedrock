package privacy

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/kzs0/bedrock/attr"
)

func FuzzPolicyNestedValues(f *testing.F) {
	for _, seed := range [][]byte{{}, {0}, {1, 2, 3}, {4, 255, 0, 8}, bytes.Repeat([]byte{3}, 64)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1024 {
			t.Skip()
		}
		const sentinel = "FUZZ_PRIVATE_SENTINEL_6c9a"
		offset, nodes := 0, 0
		next := func() byte {
			if len(raw) == 0 {
				return 0
			}
			value := raw[offset%len(raw)]
			offset++
			return value
		}
		var generate func(int) any
		generate = func(depth int) any {
			nodes++
			if depth >= 6 || nodes >= 128 {
				return int(next())
			}
			switch next() % 5 {
			case 0:
				return string([]byte{next(), next(), next()})
			case 1:
				return int64(next()) - 128
			case 2:
				return []any{generate(depth + 1), generate(depth + 1)}
			case 3:
				return map[string]any{"child": generate(depth + 1), "password": sentinel, "token": sentinel}
			default:
				return [2]any{generate(depth + 1), nil}
			}
		}
		shared := map[string]any{"visible": generate(0), "password": sentinel, "token": sentinel}
		cycle := map[string]any{"password": sentinel, "token": sentinel}
		cycle["self"] = cycle
		slice := make([]any, 2)
		slice[0], slice[1] = slice, cycle
		input := map[string]any{"a": shared, "b": shared, "cycle": cycle, "slice": slice, "password": sentinel, "token": sentinel}
		calls := 0
		policy := (&Policy{Redact: func(_ string, value any) (any, bool) { calls++; return value, true }}).Snapshot()
		value, keep := policy.Value("payload", input)
		if !keep {
			t.Fatal("ordinary collection was dropped")
		}
		if calls > 4096 {
			t.Fatalf("traversal exceeded node budget: %d", calls)
		}
		check := func(value any) {
			t.Helper()
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("filtered output is not JSON serializable: %v", err)
			}
			if bytes.Contains(encoded, []byte(sentinel)) {
				t.Fatal("sensitive value escaped filtering")
			}
		}
		check(value)
		calls = 0
		group, keep := policy.GroupAttribute([]string{"request", "credentials"}, attr.Any("payload", input))
		if !keep {
			t.Fatal("ordinary group was dropped")
		}
		check(group.Value.AsAny())
		if calls > 4096 {
			t.Fatalf("group traversal exceeded node budget: %d", calls)
		}
		if shared["password"] != sentinel || cycle["token"] != sentinel {
			t.Fatal("policy mutated sensitive input")
		}
	})
}
