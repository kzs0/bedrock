// Package privacy filters emitted telemetry values.
package privacy

import (
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kzs0/bedrock/attr"
)

// Policy controls telemetry content before emission.
// Callbacks must support concurrent calls.
type Policy struct {
	AttributeKeys      []string
	ResourceKeys       []string
	AllowErrorDetails  bool
	AllowProfileLabels bool
	AllowSourcePaths   bool
	MaxStringLength    int
	Redact             func(string, any) (any, bool)
}

// Snapshot copies policy configuration for component ownership.
func (p *Policy) Snapshot() *Policy {
	if p == nil {
		return &Policy{MaxStringLength: 4096}
	}
	copy := *p
	if p.AttributeKeys != nil {
		copy.AttributeKeys = append([]string{}, p.AttributeKeys...)
	}
	if p.ResourceKeys != nil {
		copy.ResourceKeys = append([]string{}, p.ResourceKeys...)
	}
	if copy.MaxStringLength <= 0 {
		copy.MaxStringLength = 4096
	}
	return &copy
}

// Attribute filters one attribute and nested values.
func (p *Policy) Attribute(a attr.Attr) (attr.Attr, bool) {
	if p == nil {
		p = (*Policy)(nil).Snapshot()
	}
	if !allowedAttribute(p.AttributeKeys, a.Key) {
		return attr.Attr{}, false
	}
	value, keep := p.Value(a.Key, a.Value.AsAny())
	if !keep {
		return attr.Attr{}, false
	}
	return attr.Any(a.Key, value), true
}

// Apply filters an immutable attribute set.
func (p *Policy) Apply(set attr.Set) attr.Set {
	out := make([]attr.Attr, 0, set.Len())
	set.Range(func(a attr.Attr) bool {
		if clean, keep := p.Attribute(a); keep {
			out = append(out, clean)
		}
		return true
	})
	return attr.NewSet(out...)
}

// Resource filters infrastructure identifiers independently.
func (p *Policy) Resource(set attr.Set) attr.Set {
	out := make([]attr.Attr, 0, set.Len())
	set.Range(func(a attr.Attr) bool {
		if p != nil && p.ResourceKeys != nil {
			if !allowed(p.ResourceKeys, a.Key) {
				return true
			}
		} else if infrastructure(a.Key) {
			return true
		}
		if clean, keep := p.Attribute(a); keep {
			out = append(out, clean)
		}
		return true
	})
	return attr.NewSet(out...)
}

// Value sanitizes values with bounded recursion.
func (p *Policy) Value(key string, value any) (any, bool) {
	if p == nil {
		p = (*Policy)(nil).Snapshot()
	}
	state := &walkState{remaining: 4096, seen: make(map[walkIdentity]any), active: make(map[walkIdentity]bool)}
	return p.clean(key, value, 0, state)
}
func (p *Policy) clean(key string, value any, depth int, state *walkState) (any, bool) {
	if state.remaining <= 0 {
		return "[truncated]", true
	}
	state.remaining--
	if depth > 8 {
		return "[truncated]", true
	}
	lower := strings.ToLower(key)
	if !p.AllowSourcePaths && (namespace(lower, "error.stack") || namespace(lower, "code.filepath") || namespace(lower, "code.file.path")) {
		return "[redacted]", true
	}
	if sensitive(lower) {
		return "[redacted]", true
	}
	if !p.AllowErrorDetails && (namespace(lower, "error") || namespace(lower, "exception")) {
		return "[redacted]", true
	}
	if p.Redact != nil {
		var keep bool
		value, keep = p.Redact(key, value)
		if !keep {
			return nil, false
		}
	}
	if value == nil {
		return nil, true
	}
	if _, ok := value.(time.Time); ok {
		return value, true
	}
	if err, ok := value.(error); ok {
		value = err.Error()
	}
	if s, ok := value.(string); ok {
		s = strings.ToValidUTF8(s, "�")
		if qualified(lower, "http.url") || qualified(lower, "url.full") {
			s = SanitizeURL(s)
		}
		limit := p.MaxStringLength
		if limit <= 0 {
			limit = 4096
		}
		if len(s) > limit {
			for limit > 0 && !utf8.RuneStart(s[limit]) {
				limit--
			}
			s = s[:limit] + "[truncated]"
		}
		return s, true
	}

	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice {
		if !rv.IsNil() {
			id := walkIdentity{kind: rv.Kind(), typ: rv.Type(), pointer: uintptr(rv.UnsafePointer())}
			if rv.Kind() == reflect.Slice {
				id.length = rv.Len()
			}
			if state.active[id] {
				return "[cycle]", true
			}
			if _, seen := state.seen[id]; seen {
				return "[shared]", true
			}
			state.seen[id] = value
			state.active[id] = true
			defer delete(state.active, id)
		}
	}
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return "[unsupported]", true
		}
		if rv.Len() > 64 {
			return "[truncated]", true
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		out := make(map[string]any, len(keys))
		for _, mapKey := range keys {
			k := mapKey.String()
			path := key + "." + k
			if !allowedAttribute(p.AttributeKeys, path) {
				continue
			}
			if state.remaining <= 0 {
				out[k] = "[truncated]"
				break
			}
			if v, keep := p.clean(path, rv.MapIndex(mapKey).Interface(), depth+1, state); keep {
				out[k] = v
			}
		}
		return out, true
	case reflect.Slice, reflect.Array:
		n := min(rv.Len(), 64)
		if rv.Len() > 64 {
			n = 63
		}
		out := make([]any, 0, n+1)
		for i := 0; i < n; i++ {
			if state.remaining <= 0 {
				out = append(out, "[truncated]")
				break
			}
			if v, keep := p.clean(key, rv.Index(i).Interface(), depth+1, state); keep {
				out = append(out, v)
			}
		}
		if rv.Len() > n {
			out = append(out, "[truncated]")
		}
		return out, true
	case reflect.Struct, reflect.Pointer, reflect.Func, reflect.Chan:
		return "[unsupported]", true
	}
	return value, true
}

type walkIdentity struct {
	kind    reflect.Kind
	typ     reflect.Type
	pointer uintptr
	length  int
}
type walkState struct {
	remaining int
	seen      map[walkIdentity]any
	active    map[walkIdentity]bool
}

// GroupAttribute filters qualified log attributes.
// Returned keys retain their original leaf name.
func (p *Policy) GroupAttribute(groups []string, a attr.Attr) (attr.Attr, bool) {
	path := strings.Join(append(append([]string(nil), groups...), a.Key), ".")
	clean, keep := p.Attribute(attr.Any(path, a.Value.AsAny()))
	if !keep {
		return attr.Attr{}, false
	}
	return clean.WithKey(a.Key), true
}
func namespace(path, key string) bool {
	return qualified(path, key) || strings.HasPrefix(path, key+".") || strings.Contains(path, "."+key+".")
}
func qualified(path, key string) bool { return path == key || strings.HasSuffix(path, "."+key) }
func allowedAttribute(keys []string, path string) bool {
	if keys == nil {
		return true
	}
	for _, key := range keys {
		if path == key || strings.HasPrefix(path, key+".") || strings.HasPrefix(key, path+".") {
			return true
		}
	}
	return false
}

func allowed(keys []string, key string) bool {
	if keys == nil {
		return true
	}
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}
func sensitive(key string) bool {
	for _, part := range []string{"password", "passwd", "authorization", "cookie", "api_key", "apikey", "secret", "access_token", "refresh_token"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return namespace(key, "token")
}
func infrastructure(key string) bool {
	for _, prefix := range []string{"host.", "process.", "k8s.", "container."} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// SanitizeURL removes credentials, queries, and fragments.
func SanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid URL]"
	}
	u.User = nil
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}
