package h2c

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestContractHandshakeControlFrames(t *testing.T) {
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := contractReadPreface(conn); err != nil {
			return err
		}
		if err := writeIntegrationFrame(conn, frameTypeSettings, flagAck, 0, nil); err != nil {
			return err
		}
		if err := writeIntegrationWindowUpdate(conn, 0, 16); err != nil {
			return err
		}
		ping := []byte("12345678")
		if err := writeIntegrationFrame(conn, frameTypePing, 0, 0, ping); err != nil {
			return err
		}
		f, payload, err := readFrame(conn)
		if err != nil || f.typ != frameTypePing || f.flags != flagAck || !bytes.Equal(payload, ping) {
			return fmt.Errorf("PING acknowledgement: %+v %q %v", f, payload, err)
		}
		if err := writeIntegrationFrame(conn, frameTypePing, flagAck, 0, ping); err != nil {
			return err
		}
		if err := writeIntegrationFrame(conn, 0xfe, 0, 0, nil); err != nil {
			return err
		}
		if err := writeIntegrationFrame(conn, frameTypeSettings, 0, 0, nil); err != nil {
			return err
		}
		f, _, err = readFrame(conn)
		if err != nil || f.typ != frameTypeSettings || f.flags != flagAck {
			return fmt.Errorf("SETTINGS acknowledgement: %+v %v", f, err)
		}
		if _, _, err := readIntegrationRequest(conn, 1, defaultMaxFrameSize, false); err != nil {
			return err
		}
		return writeIntegrationSuccess(conn, 1)
	})
	client := NewClient(server.addr, false, integrationTimeout)
	defer client.Shutdown()
	if _, err := client.Do("/contract/Handshake", nil, nil); err != nil {
		t.Fatal(err)
	}
	if client.connWin != defaultWindowSize+16 {
		t.Fatalf("handshake lost connection credit: %d", client.connWin)
	}
	server.wait(t)
}

func TestContractHandshakeFailureReconnects(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		frame      []byte
	}{
		{"unopened stream", "unopened stream", buildFrame(frameTypeWindowUpdate, 0, 1, windowUpdatePayload(1))},
		{"zero connection credit", "zero increment", buildFrame(frameTypeWindowUpdate, 0, 0, windowUpdatePayload(0))},
		{"overflow connection credit", "overflow", buildFrame(frameTypeWindowUpdate, 0, 0, windowUpdatePayload(maxFlowWindow))},
		{"server push setting", "SETTINGS_ENABLE_PUSH", buildFrame(frameTypeSettings, 0, 0, encodeSettings([]settingParam{{settingEnablePush, 0}}))},
		{"stream before settings", "before server SETTINGS", buildFrame(frameTypeHeaders, flagEndHeaders, 1, []byte{0x88})},
		{"truncated frame", "handshake read", []byte{0, 0, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := startIntegrationServer(t, 2, func(attempt int, conn net.Conn) error {
				if attempt == 0 {
					if err := contractReadPreface(conn); err != nil {
						return err
					}
					return writeAll(conn, tc.frame)
				}
				if err := integrationHandshake(conn, nil); err != nil {
					return err
				}
				if _, _, err := readIntegrationRequest(conn, 1, defaultMaxFrameSize, false); err != nil {
					return err
				}
				return writeIntegrationSuccess(conn, 1)
			})
			client := NewClient(server.addr, false, integrationTimeout)
			defer client.Shutdown()
			if _, err := client.Do("/contract/Failure", nil, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("first attempt: %v, want %q", err, tc.want)
			}
			if client.conn != nil {
				t.Fatal("failed handshake retained connection")
			}
			if _, err := client.Do("/contract/Recovery", nil, nil); err != nil {
				t.Fatalf("fresh connection failed: %v", err)
			}
			server.wait(t)
		})
	}
}

func TestContractResponseDataReturnsBodyAndCredit(t *testing.T) {
	wire := newContractWire(append(append(contractHeaders(false), buildFrame(frameTypeData, 0, 1, []byte("reply"))...), contractTrailers()...))
	client := contractClient(wire)
	//nolint:staticcheck // Verify nil-context support.
	body, err := client.DoContext(nil, "/contract/Body", nil, map[string]string{"X-Z": "last", "X-A": "first"})
	if err != nil || string(body) != "reply" {
		t.Fatalf("response = %q, %v", body, err)
	}
	output := bytes.NewReader(wire.output.Bytes())
	f, headers, err := readFrame(output)
	if err != nil || f.typ != frameTypeHeaders {
		t.Fatalf("request headers: %+v %v", f, err)
	}
	decoded, err := DecodeHeadersStrict(headers, DefaultMaxHeaderListSize)
	if err != nil || len(decoded) < 2 || decoded[len(decoded)-2] != (hpackHeader{"x-a", "first"}) || decoded[len(decoded)-1] != (hpackHeader{"x-z", "last"}) {
		t.Fatalf("ordered lowercase request headers: %v, %v", decoded, err)
	}
	f, _, err = readFrame(output)
	if err != nil || f.typ != frameTypeData || f.flags != flagEndStream || f.length != 0 {
		t.Fatalf("empty request termination: %+v, %v", f, err)
	}
	for _, stream := range []uint32{1, 0} {
		f, credit, err := readFrame(output)
		if err != nil || f.typ != frameTypeWindowUpdate || f.streamID != stream {
			t.Fatalf("flow credit: %+v, %v", f, err)
		}
		if got, err := windowIncrement(credit); err != nil || got != len(body) {
			t.Fatalf("credit = %d, %v", got, err)
		}
	}
	if output.Len() != 0 {
		t.Fatalf("unexpected response-side writes: %d bytes", output.Len())
	}
}

func TestContractResponseRejectsAmbiguousMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		headers    []hpackHeader
		terminal   bool
	}{
		{"missing HTTP status", "missing HTTP status", []hpackHeader{{"grpc-status", "0"}}, true},
		{"malformed HTTP status", "malformed HTTP status", []hpackHeader{{":status", "abc"}}, true},
		{"duplicate HTTP status", "duplicate HTTP status", []hpackHeader{{":status", "200"}, {":status", "201"}}, true},
		{"duplicate grpc status", "duplicate grpc-status", []hpackHeader{{":status", "200"}, {"grpc-status", "0"}, {"grpc-status", "0"}}, true},
		{"duplicate content type", "duplicate response content-type", []hpackHeader{{":status", "200"}, {"content-type", "application/grpc"}, {"content-type", "text/plain"}}, true},
		{"premature grpc status", "non-terminal response headers", []hpackHeader{{":status", "200"}, {"grpc-status", "0"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var block []byte
			for _, header := range tc.headers {
				block = appendLiteralNewName(block, header.name, header.value)
			}
			flags := uint8(flagEndHeaders)
			if tc.terminal {
				flags |= flagEndStream
			}
			input := buildFrame(frameTypeHeaders, flags, 1, block)
			if !tc.terminal {
				input = append(input, buildFrame(frameTypeData, flagEndStream, 1, nil)...)
			}
			client := contractClient(newContractWire(input))
			if body, err := client.Do("/contract/Metadata", nil, nil); err == nil || !strings.Contains(err.Error(), tc.want) || body != nil {
				t.Fatalf("response = %q, %v; want %q", body, err, tc.want)
			}
		})
	}
}

func TestContractResponseProtocolFailureClosesConnection(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		input      []byte
		window     int
	}{
		{"DATA before headers", "before response headers", buildFrame(frameTypeData, flagEndStream, 1, nil), defaultWindowSize},
		{"unexpected continuation", "unexpected CONTINUATION", buildFrame(frameTypeContinuation, flagEndHeaders, 1, nil), defaultWindowSize},
		{"disabled server push", "PUSH_PROMISE", buildFrame(frameTypePushPromise, flagEndHeaders, 1, make([]byte, 4)), defaultWindowSize},
		{"invalid HPACK", "decode response headers", buildFrame(frameTypeHeaders, flagEndHeaders, 1, []byte{0x80}), defaultWindowSize},
		{"wrong continuation stream", "expected CONTINUATION", append(buildFrame(frameTypeHeaders, 0, 1, []byte{0x88}), buildFrame(frameTypeContinuation, flagEndHeaders, 3, nil)...), defaultWindowSize},
		{"zero connection increment", "WINDOW_UPDATE", buildFrame(frameTypeWindowUpdate, 0, 0, windowUpdatePayload(0)), defaultWindowSize},
		{"connection overflow", "connection flow-control window overflow", buildFrame(frameTypeWindowUpdate, 0, 0, windowUpdatePayload(maxFlowWindow)), defaultWindowSize},
		{"stream overflow", "stream flow-control window overflow", buildFrame(frameTypeWindowUpdate, 0, 1, windowUpdatePayload(maxFlowWindow)), defaultWindowSize},
		{"invalid settings", "server SETTINGS", buildFrame(frameTypeSettings, 0, 0, encodeSettings([]settingParam{{settingEnablePush, 0}})), defaultWindowSize},
		{"settings overflow", "overflow after SETTINGS", append(buildFrame(frameTypeWindowUpdate, 0, 1, windowUpdatePayload(maxFlowWindow-defaultWindowSize)), buildFrame(frameTypeSettings, 0, 0, encodeSettings([]settingParam{{settingInitialWindowSize, maxFlowWindow}}))...), defaultWindowSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := newContractWire(tc.input)
			client := contractClient(wire)
			client.streamWin = tc.window
			if _, err := client.Do("/contract/Protocol", nil, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
			if client.conn != nil || !wire.closed {
				t.Fatal("protocol failure retained connection")
			}
		})
	}
}

func TestContractResponseSettingsAndPingAcknowledged(t *testing.T) {
	settings := buildFrame(frameTypeSettings, 0, 0, encodeSettings([]settingParam{{settingInitialWindowSize, 4096}, {settingMaxFrameSize, 32768}}))
	input := append(settings, buildFrame(frameTypeSettings, flagAck, 0, nil)...)
	input = append(input, buildFrame(frameTypePing, 0, 0, []byte("abcdefgh"))...)
	input = append(input, buildFrame(frameTypePing, flagAck, 0, []byte("abcdefgh"))...)
	input = append(input, contractHeaders(true)...)
	wire := newContractWire(input)
	client := contractClient(wire)
	if _, err := client.Do("/contract/Settings", nil, nil); err != nil {
		t.Fatal(err)
	}
	if client.streamWin != 4096 || client.maxFrame != 32768 {
		t.Fatalf("peer settings lost: window %d, frame %d", client.streamWin, client.maxFrame)
	}
	output := bytes.NewReader(wire.output.Bytes())
	for range 2 {
		if _, _, err := readFrame(output); err != nil {
			t.Fatal(err)
		}
	}
	f, _, err := readFrame(output)
	if err != nil || f.typ != frameTypeSettings || f.flags != flagAck {
		t.Fatalf("SETTINGS ACK: %+v %v", f, err)
	}
	f, payload, err := readFrame(output)
	if err != nil || f.typ != frameTypePing || f.flags != flagAck || string(payload) != "abcdefgh" {
		t.Fatalf("PING ACK: %+v %q %v", f, payload, err)
	}
	if output.Len() != 0 {
		t.Fatal("ACK frames were acknowledged again")
	}
}

func TestContractWriteFailureClosesConnection(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		body       []byte
		failAt     int
		window     int
	}{
		{"headers", "write HEADERS", nil, 1, defaultWindowSize},
		{"nonempty DATA", "write DATA", []byte("payload"), 2, defaultWindowSize},
		{"empty DATA", "write empty DATA", nil, 2, defaultWindowSize},
		{"flow wait EOF", "read response", []byte("payload"), 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := newContractWire(nil)
			wire.failWriteAt = tc.failAt
			client := contractClient(wire)
			client.streamWin = tc.window
			if _, err := client.Do("/contract/Write", tc.body, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("write error = %v, want %q", err, tc.want)
			}
			if client.conn != nil || !wire.closed {
				t.Fatal("failed write retained connection")
			}
		})
	}
}

func TestContractRequestHeaderBudgetAndFragmentation(t *testing.T) {
	response := contractHeaders(true)
	writeFrameHeader(response[:9], uint32(len(response)-9), frameTypeHeaders, flagEndHeaders|flagEndStream, 3)
	wire := newContractWire(response)
	client := contractClient(wire)
	if _, err := client.Do("/contract/Headers", nil, map[string]string{"large": strings.Repeat("x", maxHeaderBlockSize)}); err == nil || !strings.Contains(err.Error(), "request header block exceeds limit") {
		t.Fatalf("header-budget error: %v", err)
	}
	if wire.output.Len() != 0 {
		t.Fatal("oversized header block reached wire")
	}
	value := strings.Repeat("x", 20<<10)
	if _, err := client.Do("/contract/Headers", nil, map[string]string{"large": value}); err != nil {
		t.Fatalf("valid fragmented headers after rejection: %v", err)
	}
	output := bytes.NewReader(wire.output.Bytes())
	var block []byte
	for index, typ := range []uint8{frameTypeHeaders, frameTypeContinuation} {
		f, payload, err := readFrame(output)
		if err != nil || f.typ != typ || f.streamID != 3 || (f.flags&flagEndHeaders != 0) != (index == 1) {
			t.Fatalf("fragment %d: %+v %v", index, f, err)
		}
		block = append(block, payload...)
	}
	headers, err := DecodeHeadersStrict(block, DefaultMaxHeaderListSize)
	if err != nil || headers[len(headers)-1] != (hpackHeader{"large", value}) {
		t.Fatalf("fragmented header value lost: %v", err)
	}
}

func TestContractStreamIDExhaustionReconnects(t *testing.T) {
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		if err := integrationHandshake(conn, nil); err != nil {
			return err
		}
		if _, _, err := readIntegrationRequest(conn, 1, defaultMaxFrameSize, false); err != nil {
			return err
		}
		return writeIntegrationSuccess(conn, 1)
	})
	old := newContractWire(nil)
	client := contractClient(old)
	client.host, client.nextID = server.addr, 0x80000001
	defer client.Shutdown()
	if _, err := client.Do("/contract/Rollover", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !old.closed || client.nextID != 3 {
		t.Fatalf("rollover retained old connection or IDs: closed %t, next %d", old.closed, client.nextID)
	}
	server.wait(t)
}

func TestContractGateCancellationAndShutdown(t *testing.T) {
	wire := newContractWire(nil)
	client := contractClient(wire)
	if err := client.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.DoContext(ctx, "/contract/Queued", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued call cancellation: %v", err)
	}
	if err := client.ShutdownContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued shutdown cancellation: %v", err)
	}
	if wire.closed {
		t.Fatal("queued shutdown interrupted active owner")
	}
	client.release()
	client.nextID = 5
	//nolint:staticcheck // Verify nil-context support.
	if err := client.ShutdownContext(nil); err != nil {
		t.Fatal(err)
	}
	f, payload, err := readFrame(bytes.NewReader(wire.output.Bytes()))
	if err != nil || f.typ != frameTypeGoAway {
		t.Fatalf("shutdown GOAWAY: %+v, %v", f, err)
	}
	last, code, err := parseGoAway(payload)
	if err != nil || last != 3 || code != errCodeNoError || !wire.closed || client.conn != nil {
		t.Fatalf("shutdown state: last %d code %d closed %t conn %v error %v", last, code, wire.closed, client.conn, err)
	}
}

func TestContractTLSHandshakeFailureClosesConnection(t *testing.T) {
	server := startIntegrationServer(t, 1, func(_ int, conn net.Conn) error {
		return writeAll(conn, []byte("not a TLS server"))
	})
	client := NewClient(server.addr, true, integrationTimeout)
	if _, err := client.Do("/contract/TLS", nil, nil); err == nil || !strings.Contains(err.Error(), "TLS handshake") {
		t.Fatalf("TLS handshake failure: %v", err)
	}
	if client.conn != nil {
		t.Fatal("failed TLS handshake retained connection")
	}
	server.wait(t)
}

func contractReadPreface(conn net.Conn) error {
	preface := make([]byte, len(clientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil {
		return err
	}
	if string(preface) != clientPreface {
		return fmt.Errorf("invalid preface: %q", preface)
	}
	f, _, err := readFrame(conn)
	if err != nil || f.typ != frameTypeSettings || f.streamID != 0 {
		return fmt.Errorf("client SETTINGS: %+v %v", f, err)
	}
	return nil
}

func contractHeaders(terminal bool) []byte {
	block := appendLiteralNewName([]byte{0x88}, "content-type", "application/grpc+proto")
	flags := uint8(flagEndHeaders)
	if terminal {
		flags |= flagEndStream
		block = appendLiteralNewName(block, "grpc-status", "0")
	}
	return buildFrame(frameTypeHeaders, flags, 1, block)
}

func contractTrailers() []byte {
	return buildFrame(frameTypeHeaders, flagEndHeaders|flagEndStream, 1, appendLiteralNewName(nil, "grpc-status", "0"))
}

func contractClient(wire *contractWire) *Client {
	client := NewClient("collector.invalid:4317", false, time.Second)
	client.conn = wire
	return client
}

type contractWire struct {
	input       *bytes.Reader
	output      bytes.Buffer
	writes      int
	failWriteAt int
	closed      bool
}

func newContractWire(input []byte) *contractWire   { return &contractWire{input: bytes.NewReader(input)} }
func (c *contractWire) Read(p []byte) (int, error) { return c.input.Read(p) }
func (c *contractWire) Write(p []byte) (int, error) {
	c.writes++
	if c.writes == c.failWriteAt {
		return 0, errors.New("injected write failure")
	}
	return c.output.Write(p)
}
func (c *contractWire) Close() error                     { c.closed = true; return nil }
func (c *contractWire) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *contractWire) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *contractWire) SetDeadline(time.Time) error      { return nil }
func (c *contractWire) SetReadDeadline(time.Time) error  { return nil }
func (c *contractWire) SetWriteDeadline(time.Time) error { return nil }
