package h2c

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestNewClientTimeoutDefaults(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		client := NewClient("collector:4317", false, timeout)
		if client.timeout != 10*time.Second {
			t.Errorf("NewClient timeout for %v = %v, want 10s", timeout, client.timeout)
		}
	}

	client := NewClient("collector:4317", false, 3*time.Second)
	if client.timeout != 3*time.Second {
		t.Fatalf("NewClient explicit timeout = %v, want 3s", client.timeout)
	}
}

func TestClientAcquireHonorsCancellation(t *testing.T) {
	client := &Client{}
	if err := client.acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("contended acquire error = %v, want context.Canceled", err)
	}
	client.release()
}

func TestHostWithoutPort(t *testing.T) {
	tests := map[string]string{
		"collector.example.com:4317": "collector.example.com",
		"[2001:db8::1]:4317":         "2001:db8::1",
		"collector.example.com":      "collector.example.com",
		"2001:db8::1":                "2001:db8::1",
	}
	for input, want := range tests {
		if got := hostWithoutPort(input); got != want {
			t.Errorf("hostWithoutPort(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGoAwayError(t *testing.T) {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint32(payload[:4], 7)
	binary.BigEndian.PutUint32(payload[4:], errCodeProtocol)
	if err := goAwayError(payload); err == nil || !strings.Contains(err.Error(), "last stream 7") ||
		!strings.Contains(err.Error(), "error code 1") {
		t.Fatalf("goAwayError(valid) = %v", err)
	}
	if err := goAwayError([]byte{1}); err == nil || !strings.Contains(err.Error(), "malformed GOAWAY") {
		t.Fatalf("goAwayError(short) = %v, want malformed GOAWAY", err)
	}
}

func TestValidateFrameHeader(t *testing.T) {
	tests := []struct {
		name  string
		frame frame
	}{
		{name: "DATA on stream zero", frame: frame{typ: frameTypeData}},
		{name: "RST_STREAM on stream zero", frame: frame{typ: frameTypeRSTStream, length: 4}},
		{name: "RST_STREAM wrong length", frame: frame{typ: frameTypeRSTStream, length: 3, streamID: 1}},
		{name: "SETTINGS on a stream", frame: frame{typ: frameTypeSettings, length: 6, streamID: 1}},
		{name: "SETTINGS partial parameter", frame: frame{typ: frameTypeSettings, length: 5}},
		{name: "SETTINGS ACK with payload", frame: frame{typ: frameTypeSettings, flags: flagAck, length: 6}},
		{name: "PUSH_PROMISE on stream zero", frame: frame{typ: frameTypePushPromise}},
		{name: "PING on a stream", frame: frame{typ: frameTypePing, length: 8, streamID: 1}},
		{name: "PING wrong length", frame: frame{typ: frameTypePing, length: 7}},
		{name: "GOAWAY on a stream", frame: frame{typ: frameTypeGoAway, length: 8, streamID: 1}},
		{name: "GOAWAY too short", frame: frame{typ: frameTypeGoAway, length: 7}},
		{name: "WINDOW_UPDATE wrong length", frame: frame{typ: frameTypeWindowUpdate, length: 3, streamID: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateFrameHeader(tt.frame); err == nil {
				t.Fatalf("validateFrameHeader(%+v) succeeded, want error", tt.frame)
			}
		})
	}
	if err := validateFrameHeader(frame{typ: 0xff}); err != nil {
		t.Fatalf("unknown extension frame should be ignored: %v", err)
	}
}

func TestReadFrameValidationAndTransforms(t *testing.T) {
	t.Run("short header", func(t *testing.T) {
		if _, _, err := readFrame(bytes.NewReader([]byte{0})); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("readFrame error = %v, want io.ErrUnexpectedEOF", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		header := make([]byte, 9)
		writeFrameHeader(header, defaultMaxFrameSize+1, frameTypeData, 0, 1)
		if _, _, err := readFrame(bytes.NewReader(header)); err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
			t.Fatalf("readFrame oversized error = %v", err)
		}
	})

	t.Run("invalid header", func(t *testing.T) {
		if _, _, err := readFrame(bytes.NewReader(buildFrame(frameTypeData, 0, 0, nil))); err == nil {
			t.Fatal("readFrame accepted DATA on stream zero")
		}
	})

	t.Run("empty payload", func(t *testing.T) {
		f, payload, err := readFrame(bytes.NewReader(buildFrame(frameTypeSettings, flagAck, 0, nil)))
		if err != nil || f.typ != frameTypeSettings || payload != nil {
			t.Fatalf("readFrame empty SETTINGS = (%+v, %v, %v)", f, payload, err)
		}
	})

	t.Run("truncated payload", func(t *testing.T) {
		frameBytes := buildFrame(frameTypeData, 0, 1, []byte("body"))
		if _, _, err := readFrame(bytes.NewReader(frameBytes[:len(frameBytes)-1])); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("readFrame truncated error = %v, want io.ErrUnexpectedEOF", err)
		}
	})

	t.Run("invalid padding", func(t *testing.T) {
		frameBytes := buildFrame(frameTypeData, flagPadded, 1, []byte{2, 'x'})
		if _, _, err := readFrame(bytes.NewReader(frameBytes)); err == nil || !strings.Contains(err.Error(), "invalid padding") {
			t.Fatalf("readFrame invalid padding error = %v", err)
		}
	})

	t.Run("valid padding", func(t *testing.T) {
		frameBytes := buildFrame(frameTypeData, flagPadded, 1, []byte{1, 'x', 0})
		_, payload, err := readFrame(bytes.NewReader(frameBytes))
		if err != nil || string(payload) != "x" {
			t.Fatalf("readFrame padded payload = %q, %v; want x", payload, err)
		}
	})

	t.Run("priority too short", func(t *testing.T) {
		frameBytes := buildFrame(frameTypeHeaders, flagPriority, 1, []byte{0, 0, 0, 0})
		if _, _, err := readFrame(bytes.NewReader(frameBytes)); err == nil || !strings.Contains(err.Error(), "PRIORITY too short") {
			t.Fatalf("readFrame short priority error = %v", err)
		}
	})

	t.Run("priority stripped", func(t *testing.T) {
		frameBytes := buildFrame(frameTypeHeaders, flagPriority, 1, append(make([]byte, 5), []byte("block")...))
		_, payload, err := readFrame(bytes.NewReader(frameBytes))
		if err != nil || string(payload) != "block" {
			t.Fatalf("readFrame priority payload = %q, %v; want block", payload, err)
		}
	})
}

func TestApplySettings(t *testing.T) {
	streamWindow, maxFrame := defaultWindowSize, defaultMaxFrameSize
	payload := encodeSettings([]settingParam{
		{settingInitialWindowSize, 1024},
		{settingMaxFrameSize, 32768},
		{settingMaxConcurrentStreams, 8}, // accepted and ignored by the serial client
	})
	if err := applySettings(payload, &streamWindow, &maxFrame); err != nil {
		t.Fatalf("applySettings(valid): %v", err)
	}
	if streamWindow != 1024 || maxFrame != 32768 {
		t.Fatalf("settings = (window %d, frame %d), want (1024, 32768)", streamWindow, maxFrame)
	}

	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "partial parameter", payload: []byte{1}},
		{name: "oversized initial window", payload: encodeSettings([]settingParam{{settingInitialWindowSize, maxFlowWindow + 1}})},
		{name: "undersized max frame", payload: encodeSettings([]settingParam{{settingMaxFrameSize, defaultMaxFrameSize - 1}})},
		{name: "oversized max frame", payload: encodeSettings([]settingParam{{settingMaxFrameSize, maxMaxFrameSize + 1}})},
		{name: "server enable push", payload: encodeSettings([]settingParam{{settingEnablePush, 0}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			window, frameSize := defaultWindowSize, defaultMaxFrameSize
			if err := applySettings(tt.payload, &window, &frameSize); err == nil {
				t.Fatal("applySettings succeeded, want protocol error")
			}
		})
	}
}

func TestFinishHeaderBlockValidation(t *testing.T) {
	client := &Client{}
	first := frame{typ: frameTypeHeaders, streamID: 1}
	if _, err := client.finishHeaderBlock(context.Background(), first, make([]byte, maxHeaderBlockSize+1)); err == nil ||
		!strings.Contains(err.Error(), "header block exceeds limit") {
		t.Fatalf("oversized initial header block error = %v", err)
	}

	t.Run("requires continuation", func(t *testing.T) {
		withPeerFrame(t, buildFrame(frameTypeData, 0, 1, nil), func(conn net.Conn) {
			client := &Client{conn: conn}
			if _, err := client.finishHeaderBlock(context.Background(), first, []byte("partial")); err == nil ||
				!strings.Contains(err.Error(), "expected CONTINUATION") {
				t.Fatalf("wrong continuation error = %v", err)
			}
		})
	})

	t.Run("reports truncated continuation", func(t *testing.T) {
		conn, peer := net.Pipe()
		_ = peer.Close()
		defer func() { _ = conn.Close() }()
		client := &Client{conn: conn}
		if _, err := client.finishHeaderBlock(context.Background(), first, []byte("partial")); err == nil ||
			!strings.Contains(err.Error(), "read CONTINUATION") {
			t.Fatalf("truncated continuation error = %v", err)
		}
	})

	t.Run("caps accumulated block", func(t *testing.T) {
		continuation := buildFrame(frameTypeContinuation, flagEndHeaders, 1, []byte{1})
		withPeerFrame(t, continuation, func(conn net.Conn) {
			client := &Client{conn: conn}
			if _, err := client.finishHeaderBlock(context.Background(), first, make([]byte, maxHeaderBlockSize)); err == nil ||
				!strings.Contains(err.Error(), "header block exceeds limit") {
				t.Fatalf("accumulated header block error = %v", err)
			}
		})
	})
}

func TestWindowIncrement(t *testing.T) {
	if _, err := windowIncrement([]byte{1}); err == nil {
		t.Fatal("windowIncrement accepted a short payload")
	}
	if _, err := windowIncrement(make([]byte, 4)); err == nil {
		t.Fatal("windowIncrement accepted a zero increment")
	}
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, 0x80000001)
	if got, err := windowIncrement(payload); err != nil || got != 1 {
		t.Fatalf("windowIncrement reserved-bit payload = (%d, %v), want (1, nil)", got, err)
	}
}

func TestWriteAllHandlesPartialAndFailedWrites(t *testing.T) {
	writer := &chunkWriter{size: 2}
	if err := writeAll(writer, []byte("complete")); err != nil {
		t.Fatalf("writeAll partial writes: %v", err)
	}
	if got := writer.buf.String(); got != "complete" {
		t.Fatalf("writeAll result = %q, want complete", got)
	}

	if err := writeAll(zeroWriter{}, []byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAll zero write error = %v, want io.ErrShortWrite", err)
	}
	wantErr := errors.New("write failed")
	if err := writeAll(errorWriter{err: wantErr}, []byte("x")); !errors.Is(err, wantErr) {
		t.Fatalf("writeAll writer error = %v, want %v", err, wantErr)
	}
}

func TestClientIOErrorPrefersContextCancellation(t *testing.T) {
	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.ioError(ctx, "read", io.EOF); !errors.Is(err, context.Canceled) {
		t.Fatalf("ioError with canceled context = %v, want context.Canceled", err)
	}
	if err := client.ioError(context.Background(), "read", io.EOF); !errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "h2c: read") {
		t.Fatalf("ioError = %v, want wrapped read EOF", err)
	}
}

func TestRemainingHeaderBytes(t *testing.T) {
	for _, tc := range []struct {
		used, limit uint64
		want        uint64
	}{
		{used: 10, limit: 100, want: 58},
		{used: 100, limit: 100, want: 0},
		{used: 101, limit: 100, want: 0},
		{used: 68, limit: 100, want: 0},
	} {
		if got := remainingHeaderBytes(tc.used, tc.limit); got != tc.want {
			t.Errorf("remainingHeaderBytes(%d, %d) = %d, want %d", tc.used, tc.limit, got, tc.want)
		}
	}
}

type chunkWriter struct {
	buf  bytes.Buffer
	size int
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	return w.buf.Write(p[:min(len(p), w.size)])
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

func withPeerFrame(t *testing.T, frameBytes []byte, test func(net.Conn)) {
	t.Helper()
	client, peer := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = peer.Close()
	})
	written := make(chan error, 1)
	go func() {
		written <- writeAll(peer, frameBytes)
	}()
	test(client)
	if err := <-written; err != nil {
		t.Fatalf("write peer frame: %v", err)
	}
}
