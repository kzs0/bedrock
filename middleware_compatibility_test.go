package bedrock

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

var _ interface{ Unwrap() http.ResponseWriter } = (*responseWriter)(nil)

func TestResponseWriterPreservesHeadersAndStatus(t *testing.T) {
	underlying := newCompatibilityWriter()
	base, rw := wrapCompatibilityWriter(underlying)

	rw.Header().Set("X-Test", "preserved")
	rw.WriteHeader(http.StatusAccepted)
	rw.WriteHeader(http.StatusInternalServerError)
	if _, err := rw.Write([]byte("body")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if base.status != http.StatusAccepted || underlying.status != http.StatusAccepted {
		t.Fatalf("status = wrapper %d, underlying %d; want %d", base.status, underlying.status, http.StatusAccepted)
	}
	if got := underlying.Header().Get("X-Test"); got != "preserved" {
		t.Fatalf("header = %q, want preserved", got)
	}
	if got := underlying.body.String(); got != "body" {
		t.Fatalf("body = %q, want body", got)
	}
	unwrapper := rw.(interface{ Unwrap() http.ResponseWriter })
	if unwrapped := unwrapper.Unwrap(); unwrapped != underlying {
		t.Fatalf("Unwrap() = %T, want original writer", unwrapped)
	}
}

func TestResponseWriterDelegatesFlush(t *testing.T) {
	underlying := &optionalCompatibilityWriter{compatibilityWriter: newCompatibilityWriter()}
	base, rw := wrapCompatibilityWriter(underlying)
	if _, ok := rw.(http.Flusher); !ok {
		t.Fatal("wrapped writer does not advertise underlying Flusher capability")
	}

	if err := http.NewResponseController(rw).Flush(); err != nil {
		t.Fatalf("ResponseController.Flush: %v", err)
	}
	if !underlying.flushed {
		t.Fatal("underlying Flush was not called")
	}
	if !base.wroteHeader || base.status != http.StatusOK || underlying.status != http.StatusOK {
		t.Fatalf("flush status = wrapper (%v, %d), underlying %d; want committed 200", base.wroteHeader, base.status, underlying.status)
	}
}

func TestResponseWriterDelegatesLegacyFlushMethod(t *testing.T) {
	underlying := &optionalCompatibilityWriter{compatibilityWriter: newCompatibilityWriter()}
	base, rw := wrapCompatibilityWriter(underlying)
	flusher, ok := rw.(http.Flusher)
	if !ok {
		t.Fatal("wrapped writer does not advertise underlying Flusher capability")
	}

	flusher.Flush()
	if !underlying.flushed {
		t.Fatal("underlying Flush was not called")
	}
	if !base.wroteHeader || base.status != http.StatusOK || underlying.status != http.StatusOK {
		t.Fatalf("flush status = wrapper (%v, %d), underlying %d; want committed 200", base.wroteHeader, base.status, underlying.status)
	}
}

func TestResponseWriterUnwrapSupportsResponseController(t *testing.T) {
	underlying := &optionalCompatibilityWriter{compatibilityWriter: newCompatibilityWriter()}
	nested := &unwrapCompatibilityWriter{ResponseWriter: &unwrapCompatibilityWriter{ResponseWriter: underlying}}
	_, rw := wrapCompatibilityWriter(nested)

	if err := http.NewResponseController(rw).EnableFullDuplex(); err != nil {
		t.Fatalf("ResponseController.EnableFullDuplex: %v", err)
	}
	if !underlying.fullDuplex {
		t.Fatal("ResponseController did not reach the underlying writer through Unwrap")
	}
}

func TestResponseWriterTraversesOnlyControllerCapabilities(t *testing.T) {
	underlying := &optionalCompatibilityWriter{compatibilityWriter: newCompatibilityWriter()}
	nested := &unwrapCompatibilityWriter{ResponseWriter: &unwrapCompatibilityWriter{ResponseWriter: underlying}}
	_, rw := wrapCompatibilityWriter(nested)

	if _, ok := rw.(http.Flusher); !ok {
		t.Error("nested Flusher capability not preserved")
	}
	if _, ok := rw.(http.Hijacker); !ok {
		t.Error("nested Hijacker capability not preserved")
	}
	if _, ok := rw.(http.Pusher); ok {
		t.Error("nested Pusher capability bypassed an intermediate wrapper")
	}
	if _, ok := rw.(io.ReaderFrom); ok {
		t.Error("nested ReaderFrom capability bypassed an intermediate wrapper")
	}
}

func TestResponseWriterCopyDoesNotBypassTransformingWrapper(t *testing.T) {
	underlying := &optionalCompatibilityWriter{compatibilityWriter: newCompatibilityWriter()}
	transforming := &transformingUnwrapWriter{ResponseWriter: underlying}
	_, rw := wrapCompatibilityWriter(transforming)
	if _, ok := rw.(io.ReaderFrom); ok {
		t.Fatal("ReaderFrom was advertised through an intermediate transforming wrapper")
	}
	source := struct{ io.Reader }{strings.NewReader("mixed Case")}

	if _, err := io.Copy(rw, source); err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if transforming.writes != 1 {
		t.Fatalf("transforming wrapper writes = %d, want 1", transforming.writes)
	}
	if underlying.readFrom {
		t.Fatal("copy bypassed the transforming wrapper and called inner ReaderFrom")
	}
	if got := underlying.body.String(); got != "MIXED CASE" {
		t.Fatalf("transformed body = %q, want MIXED CASE", got)
	}
}

func TestResponseWriterPushDoesNotBypassIntermediateWrapper(t *testing.T) {
	underlying := &optionalCompatibilityWriter{compatibilityWriter: newCompatibilityWriter()}
	intermediate := &unwrapCompatibilityWriter{ResponseWriter: underlying}
	_, rw := wrapCompatibilityWriter(intermediate)

	if _, ok := rw.(http.Pusher); ok {
		t.Fatal("Pusher was advertised through an intermediate wrapper")
	}
	if underlying.pushTarget != "" {
		t.Fatalf("inner Push was unexpectedly called with %q", underlying.pushTarget)
	}
}

func TestResponseWriterFlushUsesFirstCapableLayer(t *testing.T) {
	inner := &flushErrorCompatibilityWriter{compatibilityWriter: newCompatibilityWriter()}
	outer := &flusherUnwrapWriter{ResponseWriter: inner}
	base, rw := wrapCompatibilityWriter(outer)

	if err := http.NewResponseController(rw).Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if outer.flushes != 1 {
		t.Fatalf("outer Flush calls = %d, want 1", outer.flushes)
	}
	if inner.calls != 0 {
		t.Fatalf("inner FlushError calls = %d, want 0", inner.calls)
	}
	if !base.wroteHeader || base.status != http.StatusOK {
		t.Fatalf("successful outer flush status = (%v, %d), want committed 200", base.wroteHeader, base.status)
	}
}

func TestResponseWriterDelegatesHijack(t *testing.T) {
	server, peer := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = peer.Close()
	})
	underlying := &optionalCompatibilityWriter{
		compatibilityWriter: newCompatibilityWriter(),
		hijackConn:          server,
		hijackReadWriter:    bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)),
	}
	_, rw := wrapCompatibilityWriter(underlying)
	if _, ok := rw.(http.Hijacker); !ok {
		t.Fatal("wrapped writer does not advertise underlying Hijacker capability")
	}

	conn, readWriter, err := http.NewResponseController(rw).Hijack()
	if err != nil {
		t.Fatalf("ResponseController.Hijack: %v", err)
	}
	if !underlying.hijacked || conn != server || readWriter != underlying.hijackReadWriter {
		t.Fatal("Hijack did not return the underlying writer's connection and buffers")
	}
}

func TestResponseWriterDelegatesPush(t *testing.T) {
	underlying := &optionalCompatibilityWriter{compatibilityWriter: newCompatibilityWriter()}
	_, rw := wrapCompatibilityWriter(underlying)
	opts := &http.PushOptions{Method: http.MethodGet, Header: http.Header{"X-Test": {"value"}}}

	pusher, ok := rw.(http.Pusher)
	if !ok {
		t.Fatal("wrapped writer does not advertise underlying Pusher capability")
	}
	if err := pusher.Push("/asset.js", opts); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if underlying.pushTarget != "/asset.js" || underlying.pushOptions != opts {
		t.Fatalf("Push delegated (%q, %p), want (%q, %p)", underlying.pushTarget, underlying.pushOptions, "/asset.js", opts)
	}
}

func TestResponseWriterDelegatesReaderFrom(t *testing.T) {
	underlying := &optionalCompatibilityWriter{compatibilityWriter: newCompatibilityWriter()}
	base, rw := wrapCompatibilityWriter(underlying)
	source := struct{ io.Reader }{strings.NewReader("copied through ReaderFrom")}
	if _, ok := rw.(io.ReaderFrom); !ok {
		t.Fatal("wrapped writer does not advertise underlying ReaderFrom capability")
	}

	n, err := io.Copy(rw, source)
	if err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if n != int64(len("copied through ReaderFrom")) || !underlying.readFrom {
		t.Fatalf("io.Copy = (%d, delegated %v), want delegated copy", n, underlying.readFrom)
	}
	if !base.wroteHeader || base.status != http.StatusOK || underlying.status != http.StatusOK {
		t.Fatalf("copy status = wrapper (%v, %d), underlying %d; want committed 200", base.wroteHeader, base.status, underlying.status)
	}
	if got := underlying.body.String(); got != "copied through ReaderFrom" {
		t.Fatalf("body = %q", got)
	}
}

func TestResponseWriterUnsupportedOptionalOperations(t *testing.T) {
	underlying := newCompatibilityWriter()
	base, rw := wrapCompatibilityWriter(underlying)

	if _, ok := rw.(http.Flusher); ok {
		t.Error("wrapped writer advertises unsupported Flusher capability")
	}
	if _, ok := rw.(http.Hijacker); ok {
		t.Error("wrapped writer advertises unsupported Hijacker capability")
	}
	if _, ok := rw.(http.Pusher); ok {
		t.Error("wrapped writer advertises unsupported Pusher capability")
	}
	if _, ok := rw.(io.ReaderFrom); ok {
		t.Error("wrapped writer advertises unsupported ReaderFrom capability")
	}

	if err := http.NewResponseController(rw).Flush(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("Flush error = %v, want ErrNotSupported", err)
	}
	if _, _, err := http.NewResponseController(rw).Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("Hijack error = %v, want ErrNotSupported", err)
	}
	if base.wroteHeader || underlying.status != 0 {
		t.Fatalf("unsupported operations committed a response: wrapper %v, underlying %d", base.wroteHeader, underlying.status)
	}
}

func TestResponseWriterFlushErrorStatusCapture(t *testing.T) {
	t.Run("unsupported does not commit", func(t *testing.T) {
		underlying := &flushErrorCompatibilityWriter{
			compatibilityWriter: newCompatibilityWriter(),
			err:                 http.ErrNotSupported,
		}
		base, rw := wrapCompatibilityWriter(underlying)

		if _, ok := rw.(http.Flusher); !ok {
			t.Fatal("FlushError-capable writer was not exposed as a Flusher")
		}
		if err := http.NewResponseController(rw).Flush(); !errors.Is(err, http.ErrNotSupported) {
			t.Fatalf("Flush error = %v, want ErrNotSupported", err)
		}
		if underlying.calls != 1 {
			t.Fatalf("FlushError calls = %d, want 1", underlying.calls)
		}
		if base.wroteHeader || underlying.status != 0 {
			t.Fatalf("unsupported flush committed status: wrapper %v, underlying %d", base.wroteHeader, underlying.status)
		}
	})

	t.Run("successful commits implicit OK", func(t *testing.T) {
		underlying := &flushErrorCompatibilityWriter{compatibilityWriter: newCompatibilityWriter()}
		base, rw := wrapCompatibilityWriter(underlying)

		if err := http.NewResponseController(rw).Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
		if !base.wroteHeader || base.status != http.StatusOK || underlying.status != http.StatusOK {
			t.Fatalf("successful flush status = wrapper (%v, %d), underlying %d", base.wroteHeader, base.status, underlying.status)
		}
	})
}

func TestResponseWriterReaderFromFallsBackToWrite(t *testing.T) {
	underlying := newCompatibilityWriter()
	base, rw := wrapCompatibilityWriter(underlying)
	if _, ok := rw.(io.ReaderFrom); ok {
		t.Fatal("wrapped writer advertises ReaderFrom despite using generic copy fallback")
	}
	source := struct{ io.Reader }{strings.NewReader("fallback")}

	n, err := io.Copy(rw, source)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != int64(len("fallback")) || underlying.body.String() != "fallback" {
		t.Fatalf("ReadFrom = (%d, %q), want fallback body", n, underlying.body.String())
	}
	if !base.wroteHeader || underlying.status != http.StatusOK {
		t.Fatalf("fallback did not commit status 200: wrapper %v, underlying %d", base.wroteHeader, underlying.status)
	}
}

func wrapCompatibilityWriter(underlying http.ResponseWriter) (*responseWriter, http.ResponseWriter) {
	base := &responseWriter{ResponseWriter: underlying, status: http.StatusOK}
	return base, wrapResponseWriter(base)
}

type compatibilityWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newCompatibilityWriter() *compatibilityWriter {
	return &compatibilityWriter{header: make(http.Header)}
}

func (w *compatibilityWriter) Header() http.Header { return w.header }

func (w *compatibilityWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *compatibilityWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(p)
}

type optionalCompatibilityWriter struct {
	*compatibilityWriter
	flushed          bool
	hijacked         bool
	hijackConn       net.Conn
	hijackReadWriter *bufio.ReadWriter
	pushTarget       string
	pushOptions      *http.PushOptions
	readFrom         bool
	fullDuplex       bool
}

type unwrapCompatibilityWriter struct {
	http.ResponseWriter
}

func (w *unwrapCompatibilityWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type transformingUnwrapWriter struct {
	http.ResponseWriter
	writes int
}

func (w *transformingUnwrapWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.ResponseWriter.Write(bytes.ToUpper(p))
}

func (w *transformingUnwrapWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type flusherUnwrapWriter struct {
	http.ResponseWriter
	flushes int
}

func (w *flusherUnwrapWriter) Flush() {
	w.flushes++
}

func (w *flusherUnwrapWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type flushErrorCompatibilityWriter struct {
	*compatibilityWriter
	err   error
	calls int
}

func (w *flushErrorCompatibilityWriter) FlushError() error {
	w.calls++
	if w.err == nil {
		w.WriteHeader(http.StatusOK)
	}
	return w.err
}

func (w *optionalCompatibilityWriter) Flush() { w.flushed = true }

func (w *optionalCompatibilityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return w.hijackConn, w.hijackReadWriter, nil
}

func (w *optionalCompatibilityWriter) Push(target string, opts *http.PushOptions) error {
	w.pushTarget = target
	w.pushOptions = opts
	return nil
}

func (w *optionalCompatibilityWriter) ReadFrom(r io.Reader) (int64, error) {
	w.readFrom = true
	return io.Copy(&w.body, r)
}

func (w *optionalCompatibilityWriter) EnableFullDuplex() error {
	w.fullDuplex = true
	return nil
}
