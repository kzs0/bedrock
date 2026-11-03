package privacy

import "testing"

func TestPolicyBoundsCyclicGraphWork(t *testing.T) {
	calls := 0
	p := (&Policy{Redact: func(_ string, value any) (any, bool) { calls++; return value, true }}).Snapshot()
	value := make(map[string]any)
	for _, key := range []string{"a", "b", "c", "d"} {
		value[key] = value
	}
	_, _ = p.Value("payload", value)
	if calls > 4096 {
		t.Fatalf("four-node cyclic value invoked redaction %d times", calls)
	}
}

func TestPolicyBoundsSharedDAGAndPreservesInput(t *testing.T) {
	calls := 0
	p := (&Policy{Redact: func(_ string, v any) (any, bool) { calls++; return v, true }}).Snapshot()
	leaf := map[string]any{"visible": "retained"}
	root := map[string]any{"a": leaf, "b": leaf}
	value, keep := p.Value("payload", root)
	if !keep || calls > 4096 {
		t.Fatal("shared DAG work exceeded budget")
	}
	result := value.(map[string]any)
	if result["b"] != "[shared]" || leaf["visible"] != "retained" {
		t.Fatalf("unexpected DAG output: %#v", result)
	}
	if result["a"].(map[string]any)["visible"] != "retained" {
		t.Fatal("first shared occurrence lost")
	}
}

func TestPolicyHasStrictTotalTraversalBudget(t *testing.T) {
	calls := 0
	p := (&Policy{Redact: func(_ string, v any) (any, bool) { calls++; return v, true }}).Snapshot()
	var tree func(int) any
	tree = func(depth int) any {
		if depth == 0 {
			return "leaf"
		}
		return []any{tree(depth - 1), tree(depth - 1), tree(depth - 1), tree(depth - 1)}
	}
	_, keep := p.Value("payload", tree(7))
	if !keep || calls > 4096 {
		t.Fatalf("tree redactions=%d", calls)
	}
}
