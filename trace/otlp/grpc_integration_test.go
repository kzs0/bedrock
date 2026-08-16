package otlp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

func TestGRPCExporterIntegration_StandardCompatibleFraming(t *testing.T) {
	received := make(chan []byte, 1)
	headers := make(chan []byte, 1)
	server := startGRPCWireServer(t, func(conn net.Conn) error {
		if err := grpcWireHandshake(conn); err != nil {
			return err
		}
		headerBlock, body, err := readGRPCWireRequest(conn)
		if err != nil {
			return err
		}
		headers <- headerBlock
		received <- body
		return writeGRPCWireSuccess(conn, 1)
	})

	metadata := map[string]string{"x-bedrock-key": "integration-key"}
	exporter := NewGRPCExporter(GRPCExporterConfig{
		Endpoint:    server.addr,
		Insecure:    true,
		Timeout:     time.Second,
		ServiceName: "integration-service",
		Headers:     metadata,
		Resource:    attr.NewSet(attr.String("environment", "test")),
	})
	metadata["x-bedrock-key"] = "mutated-after-construction"
	defer func() { _ = exporter.Shutdown(context.Background()) }()

	span := newGRPCIntegrationSpan("standard-wire")
	if err := exporter.ExportSpans(context.Background(), []*trace.Span{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}

	headerBlock := receiveGRPCWireValue(t, headers)
	if !bytes.Contains(headerBlock, []byte(otlpGRPCPath)) ||
		!bytes.Contains(headerBlock, []byte("application/grpc+proto")) ||
		!bytes.Contains(headerBlock, []byte("x-bedrock-key")) ||
		!bytes.Contains(headerBlock, []byte("integration-key")) ||
		bytes.Contains(headerBlock, []byte("mutated-after-construction")) {
		t.Errorf("request HPACK block is missing expected path/content-type/auth metadata: %q", headerBlock)
	}

	body := receiveGRPCWireValue(t, received)
	if len(body) < 6 {
		t.Fatalf("gRPC request body is only %d bytes", len(body))
	}
	if body[0] != 0 {
		t.Errorf("compressed flag = %d, want 0", body[0])
	}
	declared := int(binary.BigEndian.Uint32(body[1:5]))
	if declared != len(body)-5 {
		t.Errorf("gRPC message length = %d, actual protobuf length = %d", declared, len(body)-5)
	}
	if body[5] != 0x0a {
		t.Errorf("protobuf first tag = 0x%02x, want resource_spans tag 0x0a", body[5])
	}
	server.wait(t)
}

func TestGRPCExporterIntegration_InvalidMetadataFailsBeforeDial(t *testing.T) {
	tests := []map[string]string{
		{"te": "not-trailers"},
		{"bad header": "value"},
		{"x-value": "line\nbreak"},
		{"X-Duplicate": "one", "x-duplicate": "two"},
	}
	for _, metadata := range tests {
		exporter := NewGRPCExporter(GRPCExporterConfig{
			Endpoint: "127.0.0.1:1", Insecure: true, Headers: metadata,
		})
		err := exporter.ExportSpans(context.Background(), []*trace.Span{newGRPCIntegrationSpan("metadata")})
		if err == nil || !strings.Contains(err.Error(), "invalid metadata") && !strings.Contains(err.Error(), "duplicate metadata") {
			t.Errorf("ExportSpans with metadata %q = %v, want metadata validation error", metadata, err)
		}
	}
}

func TestGRPCExporterIntegration_CallerCancellationAbortsRPC(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	releaseResponse := make(chan struct{})
	server := startGRPCWireServer(t, func(conn net.Conn) error {
		if err := grpcWireHandshake(conn); err != nil {
			return err
		}
		if _, _, err := readGRPCWireRequest(conn); err != nil {
			return err
		}
		requestStarted <- struct{}{}
		<-releaseResponse
		return nil
	})

	exporter := NewGRPCExporter(GRPCExporterConfig{
		Endpoint:    server.addr,
		Insecure:    true,
		Timeout:     time.Second,
		ServiceName: "integration-service",
	})
	defer func() { _ = exporter.Shutdown(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- exporter.ExportSpans(ctx, []*trace.Span{newGRPCIntegrationSpan("cancel")})
	}()
	receiveGRPCWireValue(t, requestStarted)
	cancel()

	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-result:
		close(releaseResponse)
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Errorf("ExportSpans after cancellation = %v, want context.Canceled", err)
		}
	case <-timer.C:
		close(releaseResponse)
		err := receiveGRPCWireValue(t, result)
		t.Errorf("ExportSpans ignored caller cancellation and waited for the server response; eventual error = %v", err)
	}
	server.wait(t)
}

func TestGRPCExporterIntegration_ShutdownWaitsForExportWithoutReconnect(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	releaseResponse := make(chan struct{})
	server := startGRPCWireServer(t, func(conn net.Conn) error {
		if err := grpcWireHandshake(conn); err != nil {
			return err
		}
		if _, _, err := readGRPCWireRequest(conn); err != nil {
			return err
		}
		requestStarted <- struct{}{}
		<-releaseResponse
		return writeGRPCWireSuccess(conn, 1)
	})

	exporter := NewGRPCExporter(GRPCExporterConfig{
		Endpoint: server.addr, Insecure: true, Timeout: time.Second,
		ServiceName: "integration-service",
	})
	exportResult := make(chan error, 1)
	go func() {
		exportResult <- exporter.ExportSpans(context.Background(), []*trace.Span{newGRPCIntegrationSpan("shutdown-race")})
	}()
	receiveGRPCWireValue(t, requestStarted)

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	cancelShutdown()
	if err := exporter.Shutdown(shutdownCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown with an in-flight export and canceled context = %v, want context.Canceled", err)
	}

	close(releaseResponse)
	if err := receiveGRPCWireValue(t, exportResult); err != nil {
		t.Errorf("ExportSpans: %v", err)
	}
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	if err := exporter.Shutdown(retryCtx); err != nil {
		t.Errorf("Shutdown retry after export completed: %v", err)
	}
	// A stopped exporter must not reconnect or emit another request.
	if err := exporter.ExportSpans(context.Background(), []*trace.Span{newGRPCIntegrationSpan("after-shutdown")}); err != nil {
		t.Errorf("ExportSpans after Shutdown: %v", err)
	}
	server.wait(t)
}

func newGRPCIntegrationSpan(name string) *trace.Span {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), name)
	span.SetAttr(attr.String("payload", strings.Repeat("x", 256)))
	span.End()
	return span
}

type grpcWireServer struct {
	addr string
	ln   net.Listener
	done chan error

	mu   sync.Mutex
	conn net.Conn
}

func startGRPCWireServer(t *testing.T, handler func(net.Conn) error) *grpcWireServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &grpcWireServer{addr: ln.Addr().String(), ln: ln, done: make(chan error, 1)}
	t.Cleanup(func() {
		_ = ln.Close()
		s.mu.Lock()
		if s.conn != nil {
			_ = s.conn.Close()
		}
		s.mu.Unlock()
	})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			s.done <- err
			return
		}
		s.mu.Lock()
		s.conn = conn
		s.mu.Unlock()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		err = handler(conn)
		_ = conn.Close()
		s.done <- err
	}()
	return s
}

func (s *grpcWireServer) wait(t *testing.T) {
	t.Helper()
	select {
	case err := <-s.done:
		if err != nil {
			t.Errorf("gRPC wire server: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("timed out waiting for gRPC wire server")
	}
}

func grpcWireHandshake(conn net.Conn) error {
	const preface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
	got := make([]byte, len(preface))
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != preface {
		return fmt.Errorf("preface = %q", got)
	}
	typ, _, streamID, _, err := readGRPCWireFrame(conn)
	if err != nil || typ != 4 || streamID != 0 {
		return fmt.Errorf("client SETTINGS: type=%d stream=%d err=%v", typ, streamID, err)
	}
	if err := writeGRPCWireFrame(conn, 4, 0, 0, nil); err != nil {
		return err
	}
	typ, flags, streamID, _, err := readGRPCWireFrame(conn)
	if err != nil || typ != 4 || flags&1 == 0 || streamID != 0 {
		return fmt.Errorf("client SETTINGS ACK: type=%d flags=%d stream=%d err=%v", typ, flags, streamID, err)
	}
	return nil
}

func readGRPCWireRequest(conn net.Conn) ([]byte, []byte, error) {
	typ, _, streamID, headers, err := readGRPCWireFrame(conn)
	if err != nil || typ != 1 || streamID != 1 {
		return nil, nil, fmt.Errorf("request HEADERS: type=%d stream=%d err=%v", typ, streamID, err)
	}
	var body []byte
	for {
		typ, flags, gotStreamID, payload, err := readGRPCWireFrame(conn)
		if err != nil {
			return nil, nil, err
		}
		if typ != 0 || gotStreamID != 1 {
			continue
		}
		body = append(body, payload...)
		if flags&1 != 0 {
			return headers, body, nil
		}
	}
}

func writeGRPCWireSuccess(conn net.Conn, streamID uint32) error {
	headers := appendGRPCWireLiteral([]byte{0x88}, "content-type", "application/grpc+proto")
	if err := writeGRPCWireFrame(conn, 1, 4, streamID, headers); err != nil {
		return err
	}
	trailers := []byte{0x00, 0x0b}
	trailers = append(trailers, "grpc-status"...)
	trailers = append(trailers, 0x01, '0')
	return writeGRPCWireFrame(conn, 1, 5, streamID, trailers)
}

func appendGRPCWireLiteral(dst []byte, name, value string) []byte {
	dst = append(dst, 0, byte(len(name)))
	dst = append(dst, name...)
	dst = append(dst, byte(len(value)))
	return append(dst, value...)
}

func readGRPCWireFrame(r io.Reader) (typ, flags uint8, streamID uint32, payload []byte, err error) {
	var header [9]byte
	if _, err = io.ReadFull(r, header[:]); err != nil {
		return
	}
	length := int(header[0])<<16 | int(header[1])<<8 | int(header[2])
	typ = header[3]
	flags = header[4]
	streamID = binary.BigEndian.Uint32(header[5:]) & 0x7fffffff
	payload = make([]byte, length)
	_, err = io.ReadFull(r, payload)
	return
}

func writeGRPCWireFrame(conn net.Conn, typ, flags uint8, streamID uint32, payload []byte) error {
	frame := make([]byte, 9+len(payload))
	frame[0] = byte(len(payload) >> 16)
	frame[1] = byte(len(payload) >> 8)
	frame[2] = byte(len(payload))
	frame[3] = typ
	frame[4] = flags
	binary.BigEndian.PutUint32(frame[5:], streamID)
	copy(frame[9:], payload)
	for len(frame) > 0 {
		n, err := conn.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[n:]
	}
	return nil
}

func receiveGRPCWireValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for gRPC wire event")
		var zero T
		return zero
	}
}
