package log

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
)

// newBridgeWithBuf creates a Bridge backed by a JSON handler writing to buf.
func newBridgeWithBuf(buf *bytes.Buffer) *Bridge {
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return NewBridge(slog.New(h))
}

func decodeLog(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("json.Unmarshal: %v (raw: %q)", err, buf.String())
	}
	return m
}

// ── Bridge tests ─────────────────────────────────────────────────────────────

func TestBridge_Info(t *testing.T) {
	var buf bytes.Buffer
	b := newBridgeWithBuf(&buf)
	b.Info(context.Background(), "hello", attr.String("k", "v"))
	m := decodeLog(t, &buf)
	if m["msg"] != "hello" {
		t.Errorf("msg: got %v, want %q", m["msg"], "hello")
	}
	if m["k"] != "v" {
		t.Errorf("k: got %v, want %q", m["k"], "v")
	}
	if m["level"] != "INFO" {
		t.Errorf("level: got %v, want INFO", m["level"])
	}
}

func TestBridge_Debug(t *testing.T) {
	var buf bytes.Buffer
	b := newBridgeWithBuf(&buf)
	b.Debug(context.Background(), "dbg msg")
	m := decodeLog(t, &buf)
	if m["level"] != "DEBUG" {
		t.Errorf("level: got %v, want DEBUG", m["level"])
	}
	if m["msg"] != "dbg msg" {
		t.Errorf("msg: got %v", m["msg"])
	}
}

func TestBridge_Warn(t *testing.T) {
	var buf bytes.Buffer
	b := newBridgeWithBuf(&buf)
	b.Warn(context.Background(), "warn msg")
	m := decodeLog(t, &buf)
	if m["level"] != "WARN" {
		t.Errorf("level: got %v, want WARN", m["level"])
	}
}

func TestBridge_Error(t *testing.T) {
	var buf bytes.Buffer
	b := newBridgeWithBuf(&buf)
	b.Error(context.Background(), "err msg")
	m := decodeLog(t, &buf)
	if m["level"] != "ERROR" {
		t.Errorf("level: got %v, want ERROR", m["level"])
	}
}

func TestBridge_Log(t *testing.T) {
	var buf bytes.Buffer
	b := newBridgeWithBuf(&buf)
	b.Log(context.Background(), slog.LevelWarn, "log msg", attr.Int("n", 7))
	m := decodeLog(t, &buf)
	if m["msg"] != "log msg" {
		t.Errorf("msg: got %v", m["msg"])
	}
	if m["level"] != "WARN" {
		t.Errorf("level: got %v", m["level"])
	}
	// JSON numbers unmarshal as float64
	if m["n"] != float64(7) {
		t.Errorf("n: got %v, want 7", m["n"])
	}
}

func TestBridge_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	b := NewBridge(slog.New(h))
	b.Debug(context.Background(), "should be dropped")
	b.Info(context.Background(), "also dropped")
	if buf.Len() > 0 {
		t.Errorf("expected empty output for filtered levels, got %q", buf.String())
	}
	b.Warn(context.Background(), "should appear")
	if buf.Len() == 0 {
		t.Error("expected output for Warn level")
	}
}

func TestBridge_With(t *testing.T) {
	var buf bytes.Buffer
	b := newBridgeWithBuf(&buf)
	b2 := b.With(attr.String("service", "svc"))
	b2.Info(context.Background(), "msg")
	m := decodeLog(t, &buf)
	if m["service"] != "svc" {
		t.Errorf("service: got %v, want svc", m["service"])
	}
}

func TestBridge_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	b := newBridgeWithBuf(&buf)
	b2 := b.WithGroup("req")
	b2.Info(context.Background(), "msg", attr.String("id", "abc"))
	m := decodeLog(t, &buf)
	group, ok := m["req"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'req' group in output, got: %v", m)
	}
	if group["id"] != "abc" {
		t.Errorf("req.id: got %v, want abc", group["id"])
	}
}

func TestBridge_Logger(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, nil)
	logger := slog.New(h)
	b := NewBridge(logger)
	if b.Logger() != logger {
		t.Error("Logger() should return the underlying logger")
	}
}

// ── AttrToSlog conversion tests ───────────────────────────────────────────────

func TestAttrToSlog_String(t *testing.T) {
	a := AttrToSlog(attr.String("k", "v"))
	if a.Key != "k" || a.Value.String() != "v" {
		t.Errorf("got key=%q val=%q", a.Key, a.Value)
	}
}

func TestAttrToSlog_Int(t *testing.T) {
	a := AttrToSlog(attr.Int("count", 42))
	if a.Key != "count" || a.Value.Int64() != 42 {
		t.Errorf("got key=%q val=%v", a.Key, a.Value)
	}
}

func TestAttrToSlog_Float64(t *testing.T) {
	a := AttrToSlog(attr.Float64("ratio", 3.14))
	if a.Key != "ratio" || a.Value.Float64() != 3.14 {
		t.Errorf("got key=%q val=%v", a.Key, a.Value)
	}
}

func TestAttrToSlog_Bool(t *testing.T) {
	a := AttrToSlog(attr.Bool("ok", true))
	if a.Key != "ok" || !a.Value.Bool() {
		t.Errorf("got key=%q val=%v", a.Key, a.Value)
	}
}

func TestAttrToSlog_Duration(t *testing.T) {
	d := 5 * time.Second
	a := AttrToSlog(attr.Duration("timeout", d))
	if a.Key != "timeout" {
		t.Errorf("key: got %q", a.Key)
	}
	// slog Duration is stored as int64 nanoseconds via Any
	if a.Value.Duration() != d {
		t.Errorf("duration: got %v, want %v", a.Value.Duration(), d)
	}
}

func TestAttrToSlog_Error(t *testing.T) {
	// attr.Error stores the error as any; AttrToSlog falls through to Any
	a := AttrToSlog(attr.Error(nil))
	if a.Key != "error" {
		t.Errorf("key: got %q, want 'error'", a.Key)
	}
}

func TestAttrsToSlog(t *testing.T) {
	in := []attr.Attr{
		attr.String("a", "1"),
		attr.Int("b", 2),
		attr.Bool("c", true),
	}
	out := AttrsToSlog(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 slog attrs, got %d", len(out))
	}
	if out[0].Key != "a" || out[1].Key != "b" || out[2].Key != "c" {
		t.Errorf("unexpected keys: %v %v %v", out[0].Key, out[1].Key, out[2].Key)
	}
}

func TestAttrsToSlog_Empty(t *testing.T) {
	out := AttrsToSlog(nil)
	if len(out) != 0 {
		t.Errorf("expected empty slice, got %d items", len(out))
	}
}

// ── Handler tests ─────────────────────────────────────────────────────────────

func TestNewHandler_Defaults(t *testing.T) {
	h := NewHandler(nil)
	if h == nil {
		t.Fatal("NewHandler(nil) returned nil")
	}
	// Should be enabled at Info by default (slog default level is LevelInfo = 0)
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected Info to be enabled with default options")
	}
}

func TestNewHandler_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&HandlerOptions{
		Output: &buf,
		Format: "json",
		Level:  slog.LevelDebug,
	})
	logger := slog.New(h)
	logger.Info("json test")
	if !strings.Contains(buf.String(), `"msg"`) {
		t.Errorf("expected JSON output, got %q", buf.String())
	}
}

func TestNewHandler_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&HandlerOptions{
		Output: &buf,
		Format: "text",
		Level:  slog.LevelDebug,
	})
	logger := slog.New(h)
	logger.Info("text test")
	// Text format does not use JSON keys like "msg"=
	if strings.Contains(buf.String(), `"msg"`) {
		t.Errorf("did not expect JSON output for text format, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "text test") {
		t.Errorf("expected message in text output, got %q", buf.String())
	}
}

func TestHandler_Enabled(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&HandlerOptions{
		Output: &buf,
		Level:  slog.LevelWarn,
	})
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Info should not be enabled when level=Warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("Error should be enabled when level=Warn")
	}
}

func TestHandler_Handle_InjectsTraceContext(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&HandlerOptions{
		Output: &buf,
		Format: "json",
		Level:  slog.LevelDebug,
	})
	h.SetTraceContextFunc(func(ctx context.Context) (string, string) {
		return "trace123", "span456"
	})

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "with trace", 0)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("json.Unmarshal: %v (raw: %q)", err, buf.String())
	}
	if m["trace_id"] != "trace123" {
		t.Errorf("trace_id: got %v", m["trace_id"])
	}
	if m["span_id"] != "span456" {
		t.Errorf("span_id: got %v", m["span_id"])
	}
}

func TestHandler_Handle_NoTraceFunc(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&HandlerOptions{Output: &buf, Format: "json", Level: slog.LevelDebug})
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "no trace", 0)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["trace_id"]; ok {
		t.Error("trace_id should not be present when no trace func set")
	}
}

func TestHandler_WithAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&HandlerOptions{Output: &buf, Format: "json", Level: slog.LevelDebug})
	h2 := h.WithAttrs([]slog.Attr{slog.String("env", "test")})

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "with attrs", 0)
	if err := h2.Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["env"] != "test" {
		t.Errorf("env: got %v, want test", m["env"])
	}
}

func TestHandler_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&HandlerOptions{Output: &buf, Format: "json", Level: slog.LevelDebug})
	h2 := h.WithGroup("grp")

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "with group", 0)
	r.AddAttrs(slog.String("x", "y"))
	if err := h2.Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	grp, ok := m["grp"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'grp' group, got: %v", m)
	}
	if grp["x"] != "y" {
		t.Errorf("grp.x: got %v, want y", grp["x"])
	}
}

func TestHandler_EmptyTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&HandlerOptions{Output: &buf, Format: "json", Level: slog.LevelDebug})
	// Set a trace func that returns empty strings
	h.SetTraceContextFunc(func(ctx context.Context) (string, string) {
		return "", ""
	})
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "empty trace", 0)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Empty trace/span IDs should not be injected
	if _, ok := m["trace_id"]; ok {
		t.Error("empty trace_id should not appear")
	}
	if _, ok := m["span_id"]; ok {
		t.Error("empty span_id should not appear")
	}
}
