package h2c

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
)

const integrationTimeout = 750 * time.Millisecond

func TestClientIntegration_StandardUnaryAndConnectionReuse(t *testing.T) {
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, nil); err != nil {
			return err
		}
		for _, streamID := range []uint32{1, 3} {
			headers, body, err := readIntegrationRequest(conn, streamID, 1<<20, false)
			if err != nil {
				return err
			}
			if headers[":method"] != "POST" || headers[":path"] != "/svc/Unary" {
				return fmt.Errorf("unexpected request headers: %v", headers)
			}
			if headers["content-type"] != "application/grpc+proto" || headers["te"] != "trailers" {
				return fmt.Errorf("request is not gRPC compatible: %v", headers)
			}
			if !bytes.Equal(body, []byte("request")) {
				return fmt.Errorf("request body = %q", body)
			}
			if err := writeIntegrationSuccess(conn, streamID); err != nil {
				return err
			}
		}
		return nil
	})

	client := NewClient(server.addr, false, integrationTimeout)
	defer client.Shutdown()
	for range 2 {
		if _, err := client.Do("/svc/Unary", []byte("request"), nil); err != nil {
			t.Errorf("Do: %v", err)
		}
	}
	server.wait(t)
}

func TestClientIntegration_LargePayloadRespectsDefaultMaxFrameSize(t *testing.T) {
	frameLengths := make(chan uint32, 8)
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, nil); err != nil {
			return err
		}
		f, _, err := readFrame(conn)
		if err != nil || f.typ != frameTypeHeaders {
			return fmt.Errorf("read HEADERS: frame=%+v err=%v", f, err)
		}
		for {
			f, _, err = readFrame(conn)
			if err != nil {
				return err
			}
			if f.typ != frameTypeData || f.streamID != 1 {
				continue
			}
			frameLengths <- f.length
			if f.length > 16384 {
				return writeIntegrationGoAway(conn, 1, errCodeSettings)
			}
			if f.flags&flagEndStream != 0 {
				return writeIntegrationSuccess(conn, 1)
			}
		}
	})

	client := NewClient(server.addr, false, integrationTimeout)
	defer client.Shutdown()
	_, err := client.Do("/svc/Large", bytes.Repeat([]byte{0x5a}, 40<<10), nil)
	if err != nil {
		t.Errorf("Do with 40KiB payload should respect the peer's default 16KiB frame limit: %v", err)
	}
	server.wait(t)
	close(frameLengths)
	for length := range frameLengths {
		if length > 16384 {
			t.Errorf("client emitted DATA frame of %d bytes, maximum is 16384", length)
		}
	}
}

func TestClientIntegration_RespectsAdvertisedMaxFrameSize(t *testing.T) {
	const peerMaxFrame = uint32(32768)
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, []settingParam{{settingMaxFrameSize, peerMaxFrame}}); err != nil {
			return err
		}
		f, _, err := readFrame(conn)
		if err != nil || f.typ != frameTypeHeaders {
			return fmt.Errorf("read HEADERS: frame=%+v err=%v", f, err)
		}
		var total int
		for {
			f, payload, err := readIntegrationFrameWithLimit(conn, peerMaxFrame)
			if err != nil {
				return err
			}
			if f.typ != frameTypeData || f.streamID != 1 {
				continue
			}
			total += len(payload)
			if f.flags&flagEndStream != 0 {
				if total != 50<<10 {
					return fmt.Errorf("received %d bytes, want %d", total, 50<<10)
				}
				return writeIntegrationSuccess(conn, 1)
			}
		}
	})

	client := NewClient(server.addr, false, integrationTimeout)
	defer client.Shutdown()
	if _, err := client.Do("/svc/PeerFrame", bytes.Repeat([]byte{4}, 50<<10), nil); err != nil {
		t.Errorf("Do with advertised 32KiB max frame: %v", err)
	}
	server.wait(t)
}

func TestClientIntegration_AppliesPeerInitialStreamWindow(t *testing.T) {
	settings := []settingParam{{settingInitialWindowSize, 1024}}
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, settings); err != nil {
			return err
		}
		f, _, err := readFrame(conn)
		if err != nil || f.typ != frameTypeHeaders {
			return fmt.Errorf("read HEADERS: frame=%+v err=%v", f, err)
		}
		available := uint32(1024)
		for {
			f, payload, err := readFrame(conn)
			if err != nil {
				return err
			}
			if f.typ != frameTypeData || f.streamID != 1 {
				continue
			}
			if uint32(len(payload)) > available {
				return writeIntegrationRST(conn, 1, errCodeFlowCtrl)
			}
			available -= uint32(len(payload))
			if f.flags&flagEndStream != 0 {
				return writeIntegrationSuccess(conn, 1)
			}
			if err := writeIntegrationWindowUpdate(conn, 1, 1024); err != nil {
				return err
			}
			available += 1024
		}
	})

	client := NewClient(server.addr, false, integrationTimeout)
	defer client.Shutdown()
	if _, err := client.Do("/svc/Window", bytes.Repeat([]byte{1}, 4096), nil); err != nil {
		t.Errorf("Do should honor SETTINGS_INITIAL_WINDOW_SIZE: %v", err)
	}
	server.wait(t)
}

func TestClientIntegration_FlowControlAcrossFrames(t *testing.T) {
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, nil); err != nil {
			return err
		}
		f, _, err := readFrame(conn)
		if err != nil || f.typ != frameTypeHeaders {
			return fmt.Errorf("read HEADERS: frame=%+v err=%v", f, err)
		}
		var total int
		for {
			f, payload, err := readFrame(conn)
			if err != nil {
				return err
			}
			if f.typ != frameTypeData || f.streamID != 1 {
				continue
			}
			total += len(payload)
			if f.flags&flagEndStream != 0 {
				if total != 80<<10 {
					return fmt.Errorf("received %d request bytes, want %d", total, 80<<10)
				}
				return writeIntegrationSuccess(conn, 1)
			}
			if err := writeIntegrationWindowUpdate(conn, 0, uint32(len(payload))); err != nil {
				return err
			}
			if err := writeIntegrationWindowUpdate(conn, 1, uint32(len(payload))); err != nil {
				return err
			}
		}
	})

	client := NewClient(server.addr, false, integrationTimeout)
	defer client.Shutdown()
	if _, err := client.Do("/svc/Flow", bytes.Repeat([]byte{2}, 80<<10), nil); err != nil {
		t.Errorf("Do across flow-control windows: %v", err)
	}
	server.wait(t)
}

func TestClientIntegration_ResetsStreamWindowForEachStream(t *testing.T) {
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, nil); err != nil {
			return err
		}
		for _, streamID := range []uint32{1, 3} {
			_, body, err := readIntegrationRequest(conn, streamID, 1<<20, false)
			if err != nil {
				return err
			}
			if len(body) != 40<<10 {
				return fmt.Errorf("stream %d body = %d bytes", streamID, len(body))
			}
			// The connection window is shared and replenished. The stream window
			// belongs to this completed stream and intentionally is not updated.
			if err := writeIntegrationWindowUpdate(conn, 0, uint32(len(body))); err != nil {
				return err
			}
			if err := writeIntegrationSuccess(conn, streamID); err != nil {
				return err
			}
		}
		return nil
	})

	client := NewClient(server.addr, false, 250*time.Millisecond)
	defer client.Shutdown()
	for i := range 2 {
		if _, err := client.Do("/svc/StreamWindow", bytes.Repeat([]byte{3}, 40<<10), nil); err != nil {
			t.Errorf("Do call %d should receive a fresh per-stream window: %v", i+1, err)
			break
		}
	}
	server.wait(t)
}

func TestClientIntegration_FragmentedHeadersAndTrailers(t *testing.T) {
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, nil); err != nil {
			return err
		}
		if _, _, err := readIntegrationRequest(conn, 1, 1<<20, false); err != nil {
			return err
		}

		initial := append([]byte{0x88}, appendLiteralNewName(nil, "content-type", "application/grpc")...)
		if err := writeIntegrationFrame(conn, frameTypeHeaders, 0, 1, initial[:1]); err != nil {
			return err
		}
		if err := writeIntegrationFrame(conn, frameTypeContinuation, flagEndHeaders, 1, initial[1:]); err != nil {
			return err
		}

		trailers := appendLiteralNewName(nil, "grpc-status", "13")
		cut := len(trailers) - 1
		if err := writeIntegrationFrame(conn, frameTypeHeaders, flagEndStream, 1, trailers[:cut]); err != nil {
			return err
		}
		return writeIntegrationFrame(conn, frameTypeContinuation, flagEndHeaders, 1, trailers[cut:])
	})

	client := NewClient(server.addr, false, integrationTimeout)
	defer client.Shutdown()
	if _, err := client.Do("/svc/Continuation", []byte("request"), nil); err == nil || !strings.Contains(err.Error(), "status 13") {
		t.Errorf("Do error = %v, want fragmented grpc-status 13", err)
	}
	server.wait(t)
}

func TestClientIntegration_RejectsInvalidResponseStatus(t *testing.T) {
	tests := []struct {
		name     string
		respond  func(net.Conn) error
		wantText string
	}{
		{
			name: "missing grpc status",
			respond: func(conn net.Conn) error {
				return writeIntegrationFrame(conn, frameTypeHeaders, flagEndHeaders|flagEndStream, 1, integrationResponseHeaders("application/grpc+proto"))
			},
			wantText: "missing grpc-status",
		},
		{
			name: "malformed grpc status",
			respond: func(conn net.Conn) error {
				if err := writeIntegrationFrame(conn, frameTypeHeaders, flagEndHeaders, 1, integrationResponseHeaders("application/grpc+proto")); err != nil {
					return err
				}
				trailers := appendLiteralNewName(nil, "grpc-status", "bogus")
				return writeIntegrationFrame(conn, frameTypeHeaders, flagEndHeaders|flagEndStream, 1, trailers)
			},
			wantText: "malformed grpc-status",
		},
		{
			name: "HTTP failure",
			respond: func(conn net.Conn) error {
				if err := writeIntegrationFrame(conn, frameTypeHeaders, flagEndHeaders, 1, []byte{0x8e}); err != nil {
					return err
				}
				trailers := appendLiteralNewName(nil, "grpc-status", "0")
				return writeIntegrationFrame(conn, frameTypeHeaders, flagEndHeaders|flagEndStream, 1, trailers)
			},
			wantText: "HTTP status 500",
		},
		{
			name: "missing gRPC content type",
			respond: func(conn net.Conn) error {
				headers := append([]byte{0x88}, appendLiteralNewName(nil, "grpc-status", "0")...)
				return writeIntegrationFrame(conn, frameTypeHeaders, flagEndHeaders|flagEndStream, 1, headers)
			},
			wantText: "content-type",
		},
		{
			name: "invalid gRPC content type",
			respond: func(conn net.Conn) error {
				headers := integrationResponseHeaders("text/plain")
				headers = append(headers, appendLiteralNewName(nil, "grpc-status", "0")...)
				return writeIntegrationFrame(conn, frameTypeHeaders, flagEndHeaders|flagEndStream, 1, headers)
			},
			wantText: "content-type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
				if err := integrationHandshake(conn, nil); err != nil {
					return err
				}
				if _, _, err := readIntegrationRequest(conn, 1, 1<<20, false); err != nil {
					return err
				}
				return tt.respond(conn)
			})
			client := NewClient(server.addr, false, integrationTimeout)
			defer client.Shutdown()
			_, err := client.Do("/svc/InvalidStatus", nil, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("Do error = %v, want text %q", err, tt.wantText)
			}
			server.wait(t)
		})
	}
}

func TestClientIntegration_RSTStream(t *testing.T) {
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, nil); err != nil {
			return err
		}
		if _, _, err := readIntegrationRequest(conn, 1, 1<<20, false); err != nil {
			return err
		}
		return writeIntegrationRST(conn, 1, errCodeRefused)
	})
	client := NewClient(server.addr, false, integrationTimeout)
	defer client.Shutdown()
	if _, err := client.Do("/svc/Reset", nil, nil); err == nil || !strings.Contains(err.Error(), "RST_STREAM") {
		t.Errorf("Do error = %v, want RST_STREAM", err)
	}
	server.wait(t)
}

func TestClientIntegration_DoContextCancellation(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	releaseServer := make(chan struct{})
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, nil); err != nil {
			return err
		}
		if _, _, err := readIntegrationRequest(conn, 1, 1<<20, false); err != nil {
			return err
		}
		requestStarted <- struct{}{}
		<-releaseServer
		return nil
	})

	client := NewClient(server.addr, false, time.Second)
	defer client.Shutdown()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.DoContext(ctx, "/svc/Cancel", []byte("request"), nil)
		result <- err
	}()
	receiveIntegrationValue(t, requestStarted)
	cancel()
	err := receiveIntegrationValue(t, result)
	close(releaseServer)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("DoContext error = %v, want context.Canceled", err)
	}
	server.wait(t)
}

func TestClientIntegration_GracefulGOAWAYCompletesActiveStreamThenReconnects(t *testing.T) {
	server := startIntegrationServer(t, 2, func(connection int, conn net.Conn) error {
		if err := integrationHandshake(conn, nil); err != nil {
			return err
		}
		streamID := uint32(1)
		if _, _, err := readIntegrationRequest(conn, streamID, 1<<20, false); err != nil {
			return err
		}
		if connection == 0 {
			if err := writeIntegrationGoAway(conn, streamID, errCodeNoError); err != nil {
				return err
			}
		}
		return writeIntegrationSuccess(conn, streamID)
	})

	client := NewClient(server.addr, false, integrationTimeout)
	defer client.Shutdown()
	if _, err := client.Do("/svc/Reconnect", nil, nil); err != nil {
		t.Errorf("active stream covered by graceful GOAWAY should complete: %v", err)
	}
	if _, err := client.Do("/svc/Reconnect", nil, nil); err != nil {
		t.Errorf("next Do should reconnect after graceful GOAWAY: %v", err)
	}
	server.wait(t)
}

func TestClientIntegration_GOAWAYBelowActiveStreamFails(t *testing.T) {
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, nil); err != nil {
			return err
		}
		if _, _, err := readIntegrationRequest(conn, 1, 1<<20, false); err != nil {
			return err
		}
		return writeIntegrationGoAway(conn, 0, errCodeNoError)
	})

	client := NewClient(server.addr, false, integrationTimeout)
	defer client.Shutdown()
	if _, err := client.Do("/svc/RejectedByGOAWAY", nil, nil); err == nil || !strings.Contains(err.Error(), "GOAWAY") {
		t.Errorf("Do error = %v, want GOAWAY rejecting active stream", err)
	}
	server.wait(t)
}

type integrationServer struct {
	addr string
	ln   net.Listener
	done chan error

	mu    sync.Mutex
	conns []net.Conn
}

func startIntegrationServer(t *testing.T, connectionCount int, handler func(int, net.Conn) error) *integrationServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &integrationServer{addr: ln.Addr().String(), ln: ln, done: make(chan error, 1)}
	t.Cleanup(func() {
		_ = ln.Close()
		s.mu.Lock()
		for _, conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
	})
	go func() {
		for i := 0; i < connectionCount; i++ {
			conn, err := ln.Accept()
			if err != nil {
				s.done <- err
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			err = handler(i, conn)
			_ = conn.Close()
			if err != nil {
				s.done <- err
				return
			}
		}
		s.done <- nil
	}()
	return s
}

func (s *integrationServer) wait(t *testing.T) {
	t.Helper()
	select {
	case err := <-s.done:
		if err != nil {
			t.Errorf("protocol server: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("timed out waiting for protocol server")
	}
}

func receiveIntegrationValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for protocol event")
		var zero T
		return zero
	}
}

func integrationHandshake(conn net.Conn, settings []settingParam) error {
	preface := make([]byte, len(clientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil {
		return fmt.Errorf("read preface: %w", err)
	}
	if string(preface) != clientPreface {
		return fmt.Errorf("preface = %q", preface)
	}
	f, _, err := readFrame(conn)
	if err != nil || f.typ != frameTypeSettings || f.streamID != 0 {
		return fmt.Errorf("client SETTINGS: frame=%+v err=%v", f, err)
	}
	if err := writeIntegrationFrame(conn, frameTypeSettings, 0, 0, encodeSettings(settings)); err != nil {
		return err
	}
	f, _, err = readFrame(conn)
	if err != nil || f.typ != frameTypeSettings || f.flags&flagAck == 0 {
		return fmt.Errorf("client SETTINGS ACK: frame=%+v err=%v", f, err)
	}
	return nil
}

func readIntegrationRequest(conn net.Conn, streamID uint32, maxFrame uint32, replenish bool) (map[string]string, []byte, error) {
	f, headerBlock, err := readFrame(conn)
	if err != nil || f.typ != frameTypeHeaders || f.streamID != streamID {
		return nil, nil, fmt.Errorf("request HEADERS: frame=%+v err=%v", f, err)
	}
	headers := make(map[string]string)
	for _, header := range DecodeHeaders(headerBlock) {
		headers[header.name] = header.value
	}
	var body []byte
	for {
		f, payload, err := readFrame(conn)
		if err != nil {
			return nil, nil, err
		}
		if f.typ != frameTypeData || f.streamID != streamID {
			continue
		}
		if f.length > maxFrame {
			return nil, nil, fmt.Errorf("DATA frame length %d exceeds %d", f.length, maxFrame)
		}
		body = append(body, payload...)
		if replenish && len(payload) > 0 {
			if err := writeIntegrationWindowUpdate(conn, 0, uint32(len(payload))); err != nil {
				return nil, nil, err
			}
			if err := writeIntegrationWindowUpdate(conn, streamID, uint32(len(payload))); err != nil {
				return nil, nil, err
			}
		}
		if f.flags&flagEndStream != 0 {
			return headers, body, nil
		}
	}
}

func readIntegrationFrameWithLimit(r io.Reader, maxFrame uint32) (frame, []byte, error) {
	var header [9]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return frame{}, nil, err
	}
	f := parseFrameHeader(header)
	if f.length > maxFrame {
		return frame{}, nil, fmt.Errorf("frame length %d exceeds peer maximum %d", f.length, maxFrame)
	}
	payload := make([]byte, f.length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return frame{}, nil, err
	}
	return f, payload, nil
}

func writeIntegrationSuccess(conn net.Conn, streamID uint32) error {
	if err := writeIntegrationFrame(conn, frameTypeHeaders, flagEndHeaders, streamID, integrationResponseHeaders("application/grpc+proto")); err != nil {
		return err
	}
	trailers := appendLiteralNewName(nil, "grpc-status", "0")
	return writeIntegrationFrame(conn, frameTypeHeaders, flagEndHeaders|flagEndStream, streamID, trailers)
}

func integrationResponseHeaders(contentType string) []byte {
	headers := []byte{0x88}
	if contentType != "" {
		headers = append(headers, appendLiteralNewName(nil, "content-type", contentType)...)
	}
	return headers
}

func writeIntegrationRST(conn net.Conn, streamID, code uint32) error {
	var payload [4]byte
	binary.BigEndian.PutUint32(payload[:], code)
	return writeIntegrationFrame(conn, frameTypeRSTStream, 0, streamID, payload[:])
}

func writeIntegrationGoAway(conn net.Conn, lastStreamID, code uint32) error {
	var payload [8]byte
	binary.BigEndian.PutUint32(payload[:4], lastStreamID)
	binary.BigEndian.PutUint32(payload[4:], code)
	return writeIntegrationFrame(conn, frameTypeGoAway, 0, 0, payload[:])
}

func writeIntegrationWindowUpdate(conn net.Conn, streamID, increment uint32) error {
	return writeIntegrationFrame(conn, frameTypeWindowUpdate, 0, streamID, makeWindowUpdate(streamID, increment)[9:])
}

func writeIntegrationFrame(conn net.Conn, typ, flags uint8, streamID uint32, payload []byte) error {
	frame := buildFrame(typ, flags, streamID, payload)
	for len(frame) > 0 {
		n, err := conn.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[n:]
	}
	return nil
}
