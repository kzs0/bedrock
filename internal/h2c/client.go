package h2c

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const clientPreface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

const (
	defaultMaxFrameSize = 1 << 14
	maxMaxFrameSize     = 1<<24 - 1
	maxFlowWindow       = 1<<31 - 1
	maxHeaderBlockSize  = 64 << 10
	maxResponseBodySize = 16 << 20
)

// Client is a minimal HTTP/2 client for unary gRPC calls over h2c or h2 (TLS).
// One RPC at a time is supported; concurrent callers are serialized.
type Client struct {
	host    string
	tls     bool
	timeout time.Duration

	gateOnce  sync.Once
	callGate  chan struct{}
	conn      net.Conn
	nextID    uint32
	connWin   int
	streamWin int // peer's initial per-stream send window
	maxFrame  int // peer's maximum outbound frame payload
	draining  bool
}

// NewClient creates a new h2c/h2 client for the given host:port.
func NewClient(host string, tlsEnabled bool, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		host: host, tls: tlsEnabled, timeout: timeout,
		callGate: make(chan struct{}, 1), nextID: 1,
		connWin: defaultWindowSize, streamWin: defaultWindowSize,
		maxFrame: defaultMaxFrameSize,
	}
}

// Do sends an HTTP/2 POST request and returns the response body.
func (c *Client) Do(path string, body []byte, extraHeaders map[string]string) ([]byte, error) {
	return c.DoContext(context.Background(), path, body, extraHeaders)
}

// DoContext is Do with cancellation and deadline support. The context applies
// while waiting for another call, dialing, writing, and reading the response.
func (c *Client) DoContext(ctx context.Context, path string, body []byte, extraHeaders map[string]string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	if c.draining {
		c.reset()
	}
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}
	clearDeadline := armConnContext(ctx, c.conn)
	defer func() { clearDeadline() }()

	if c.nextID > 0x7fffffff {
		clearDeadline()
		c.reset()
		if err := c.ensureConn(ctx); err != nil {
			return nil, err
		}
		clearDeadline = armConnContext(ctx, c.conn)
	}
	streamID := c.nextID
	c.nextID += 2
	currentStreamWin := c.streamWin

	hpackBlock := EncodeRequestHeaders(c.host, path, c.tls)
	keys := make([]string, 0, len(extraHeaders))
	for k := range extraHeaders {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		hpackBlock = appendLiteralNewName(hpackBlock, strings.ToLower(k), extraHeaders[k])
	}
	if len(hpackBlock) > maxHeaderBlockSize {
		return nil, errors.New("h2c: request header block exceeds limit")
	}
	if err := c.writeHeaderBlock(streamID, hpackBlock); err != nil {
		c.reset()
		return nil, c.ioError(ctx, "write HEADERS", err)
	}

	response := responseState{}
	remaining := body
	for len(remaining) > 0 {
		allowed := min(c.connWin, currentStreamWin, c.maxFrame)
		if allowed <= 0 {
			if err := c.readAndHandle(ctx, streamID, &currentStreamWin, &response); err != nil {
				c.reset()
				return nil, err
			}
			if response.complete {
				_ = c.writeRSTStream(streamID, errCodeCancel)
				return response.result()
			}
			continue
		}
		n := min(len(remaining), allowed)
		flags := uint8(0)
		if n == len(remaining) {
			flags = flagEndStream
		}
		if err := c.writeFrame(frameTypeData, flags, streamID, remaining[:n]); err != nil {
			c.reset()
			return nil, c.ioError(ctx, "write DATA", err)
		}
		c.connWin -= n
		currentStreamWin -= n
		remaining = remaining[n:]
	}
	if len(body) == 0 {
		if err := c.writeFrame(frameTypeData, flagEndStream, streamID, nil); err != nil {
			c.reset()
			return nil, c.ioError(ctx, "write empty DATA", err)
		}
	}

	for !response.complete {
		if err := c.readAndHandle(ctx, streamID, &currentStreamWin, &response); err != nil {
			c.reset()
			return nil, err
		}
	}
	return response.result()
}

// Shutdown closes the underlying connection.
func (c *Client) Shutdown() { _ = c.ShutdownContext(context.Background()) }

// ShutdownContext closes the connection after any current call has released it,
// or returns if ctx expires first.
func (c *Client) ShutdownContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()
	if c.conn != nil {
		payload := make([]byte, 8)
		if c.nextID >= 3 {
			binary.BigEndian.PutUint32(payload, c.nextID-2)
		}
		binary.BigEndian.PutUint32(payload[4:], errCodeNoError)
		deadline := time.Now().Add(time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = c.conn.SetWriteDeadline(deadline)
		_ = writeAll(c.conn, buildFrame(frameTypeGoAway, 0, 0, payload))
	}
	c.reset()
	return nil
}

func (c *Client) acquire(ctx context.Context) error {
	c.gateOnce.Do(func() {
		if c.callGate == nil {
			c.callGate = make(chan struct{}, 1)
		}
	})
	select {
	case c.callGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Client) release() { <-c.callGate }

func (c *Client) ensureConn(ctx context.Context) error {
	if c.conn != nil {
		return nil
	}
	return c.connect(ctx)
}

func (c *Client) connect(ctx context.Context) error {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", c.host)
	if err != nil {
		return fmt.Errorf("h2c: dial %s: %w", c.host, err)
	}
	if c.tls {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: hostWithoutPort(c.host), NextProtos: []string{"h2"}})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("h2c: TLS handshake: %w", err)
		}
		if tlsConn.ConnectionState().NegotiatedProtocol != "h2" {
			_ = conn.Close()
			return errors.New("h2c: server did not negotiate h2")
		}
		conn = tlsConn
	}

	clearDeadline := armConnContext(ctx, conn)
	defer clearDeadline()
	fail := func(op string, err error) error {
		_ = conn.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("h2c: %s: %w", op, err)
	}
	if err := writeAll(conn, []byte(clientPreface)); err != nil {
		return fail("write preface", err)
	}
	settings := encodeSettings([]settingParam{
		{settingEnablePush, 0}, {settingHeaderTableSize, 0},
		{settingInitialWindowSize, uint32(defaultWindowSize)},
	})
	if err := writeAll(conn, buildFrame(frameTypeSettings, 0, 0, settings)); err != nil {
		return fail("write SETTINGS", err)
	}

	connWin, streamWin, maxFrame := defaultWindowSize, defaultWindowSize, defaultMaxFrameSize
	for {
		f, payload, err := readFrame(conn)
		if err != nil {
			return fail("handshake read", err)
		}
		switch f.typ {
		case frameTypeSettings:
			if f.streamID != 0 {
				return fail("handshake", errors.New("SETTINGS on non-zero stream"))
			}
			if f.flags&flagAck != 0 {
				if len(payload) != 0 {
					return fail("handshake", errors.New("SETTINGS ACK with payload"))
				}
				continue
			}
			if err := applySettings(payload, &streamWin, &maxFrame); err != nil {
				return fail("server SETTINGS", err)
			}
			if err := writeAll(conn, buildFrame(frameTypeSettings, flagAck, 0, nil)); err != nil {
				return fail("write SETTINGS ACK", err)
			}
			c.conn, c.nextID = conn, 1
			c.connWin, c.streamWin, c.maxFrame = connWin, streamWin, maxFrame
			return nil
		case frameTypeWindowUpdate:
			if f.streamID != 0 {
				return fail("handshake", errors.New("WINDOW_UPDATE on unopened stream"))
			}
			inc, err := windowIncrement(payload)
			if err != nil || connWin > maxFlowWindow-inc {
				if err == nil {
					err = errors.New("connection flow-control window overflow")
				}
				return fail("handshake WINDOW_UPDATE", err)
			}
			connWin += inc
		case frameTypePing:
			if f.streamID != 0 || len(payload) != 8 {
				return fail("handshake PING", errors.New("invalid PING frame"))
			}
			if f.flags&flagAck == 0 {
				if err := writeAll(conn, buildFrame(frameTypePing, flagAck, 0, payload)); err != nil {
					return fail("handshake PING ACK", err)
				}
			}
		case frameTypeGoAway:
			return fail("handshake", goAwayError(payload))
		case frameTypeData, frameTypeHeaders, frameTypeRSTStream, frameTypePushPromise, frameTypeContinuation:
			return fail("handshake", errors.New("stream frame before server SETTINGS"))
		}
	}
}

type responseState struct {
	body           []byte
	httpStatus     int
	grpcStatus     int
	sawHeaders     bool
	sawHTTPStatus  bool
	sawContentType bool
	validGRPCType  bool
	sawGRPCStatus  bool
	statusParseErr error
	complete       bool
}

func (r *responseState) result() ([]byte, error) {
	if r.statusParseErr != nil {
		return nil, r.statusParseErr
	}
	if r.httpStatus == 0 {
		return nil, errors.New("h2c: response missing HTTP status")
	}
	if r.httpStatus != 200 {
		return nil, fmt.Errorf("h2c: HTTP status %d", r.httpStatus)
	}
	if !r.sawGRPCStatus {
		return nil, errors.New("h2c: response missing grpc-status")
	}
	if r.grpcStatus == 0 && (!r.sawContentType || !r.validGRPCType) {
		return nil, errors.New("h2c: successful response has invalid or missing gRPC content-type")
	}
	if r.grpcStatus != 0 {
		return nil, fmt.Errorf("h2c: gRPC status %d", r.grpcStatus)
	}
	return r.body, nil
}

func (c *Client) readAndHandle(ctx context.Context, streamID uint32, currentStreamWin *int, response *responseState) error {
	f, payload, err := readFrame(c.conn)
	if err != nil {
		return c.ioError(ctx, "read response", err)
	}
	switch f.typ {
	case frameTypeData:
		if f.streamID == 0 {
			return errors.New("h2c: DATA on stream 0")
		}
		if f.streamID != streamID {
			return nil
		}
		if !response.sawHeaders {
			return errors.New("h2c: DATA received before response headers")
		}
		flowBytes := int(f.length)
		if flowBytes > 0 {
			if err := c.writeFrame(frameTypeWindowUpdate, 0, streamID, windowUpdatePayload(uint32(flowBytes))); err != nil {
				return c.ioError(ctx, "write stream WINDOW_UPDATE", err)
			}
			if err := c.writeFrame(frameTypeWindowUpdate, 0, 0, windowUpdatePayload(uint32(flowBytes))); err != nil {
				return c.ioError(ctx, "write connection WINDOW_UPDATE", err)
			}
		}
		if len(response.body) > maxResponseBodySize-len(payload) {
			return errors.New("h2c: response body exceeds limit")
		}
		response.body = append(response.body, payload...)
		response.complete = f.flags&flagEndStream != 0
	case frameTypeHeaders:
		if f.streamID == 0 {
			return errors.New("h2c: HEADERS on stream 0")
		}
		block, err := c.finishHeaderBlock(ctx, f, payload)
		if err != nil {
			return err
		}
		if f.streamID == streamID {
			initial := !response.sawHeaders
			headers, err := DecodeHeadersStrict(block, DefaultMaxHeaderListSize)
			if err != nil {
				return fmt.Errorf("h2c: decode response headers: %w", err)
			}
			for _, h := range headers {
				switch h.name {
				case ":status":
					if !initial || response.sawHTTPStatus {
						response.statusParseErr = errors.New("h2c: misplaced or duplicate HTTP status")
					} else {
						status, parseErr := strconv.Atoi(h.value)
						if parseErr != nil || status < 100 || status > 999 {
							response.statusParseErr = errors.New("h2c: malformed HTTP status")
						} else {
							response.httpStatus = status
							response.sawHTTPStatus = true
						}
					}
				case "grpc-status":
					if response.sawGRPCStatus {
						response.statusParseErr = errors.New("h2c: duplicate grpc-status")
					} else {
						status, parseErr := strconv.Atoi(h.value)
						if parseErr != nil || status < 0 || status > 16 {
							response.statusParseErr = errors.New("h2c: malformed grpc-status")
						} else {
							response.grpcStatus = status
							response.sawGRPCStatus = true
						}
					}
				case "content-type":
					if initial {
						if response.sawContentType {
							response.statusParseErr = errors.New("h2c: duplicate response content-type")
						} else {
							response.sawContentType = true
							response.validGRPCType = strings.HasPrefix(strings.ToLower(h.value), "application/grpc")
						}
					}
				}
			}
			if initial && response.sawGRPCStatus && f.flags&flagEndStream == 0 {
				response.statusParseErr = errors.New("h2c: grpc-status in non-terminal response headers")
			}
			response.sawHeaders = true
			response.complete = f.flags&flagEndStream != 0
		}
	case frameTypeContinuation:
		return errors.New("h2c: unexpected CONTINUATION frame")
	case frameTypePushPromise:
		return errors.New("h2c: server sent PUSH_PROMISE after push was disabled")
	case frameTypeRSTStream:
		if f.streamID == streamID {
			if len(payload) != 4 {
				return errors.New("h2c: malformed RST_STREAM")
			}
			return fmt.Errorf("h2c: stream RST_STREAM error code %d", binary.BigEndian.Uint32(payload))
		}
	case frameTypeGoAway:
		lastID, code, err := parseGoAway(payload)
		if err != nil {
			return fmt.Errorf("h2c: %w", err)
		}
		c.draining = true
		if streamID > lastID {
			return fmt.Errorf("h2c: server sent GOAWAY (last stream %d, error code %d); stream %d was not processed", lastID, code, streamID)
		}
		// The peer accepted this stream. Keep reading it to END_STREAM, but
		// never open another stream on this connection.
		return nil
	case frameTypeWindowUpdate:
		inc, err := windowIncrement(payload)
		if err != nil {
			return fmt.Errorf("h2c: WINDOW_UPDATE: %w", err)
		}
		switch f.streamID {
		case 0:
			if c.connWin > maxFlowWindow-inc {
				return errors.New("h2c: connection flow-control window overflow")
			}
			c.connWin += inc
		case streamID:
			if *currentStreamWin > maxFlowWindow-inc {
				return errors.New("h2c: stream flow-control window overflow")
			}
			*currentStreamWin += inc
		}
	case frameTypeSettings:
		if f.streamID != 0 {
			return errors.New("h2c: SETTINGS on non-zero stream")
		}
		if f.flags&flagAck != 0 {
			if len(payload) != 0 {
				return errors.New("h2c: SETTINGS ACK with payload")
			}
			return nil
		}
		oldInitial := c.streamWin
		if err := applySettings(payload, &c.streamWin, &c.maxFrame); err != nil {
			return fmt.Errorf("h2c: server SETTINGS: %w", err)
		}
		*currentStreamWin += c.streamWin - oldInitial
		if *currentStreamWin > maxFlowWindow {
			return errors.New("h2c: stream flow-control window overflow after SETTINGS")
		}
		if err := c.writeFrame(frameTypeSettings, flagAck, 0, nil); err != nil {
			return c.ioError(ctx, "write SETTINGS ACK", err)
		}
	case frameTypePing:
		if f.streamID != 0 || len(payload) != 8 {
			return errors.New("h2c: invalid PING frame")
		}
		if f.flags&flagAck == 0 {
			if err := c.writeFrame(frameTypePing, flagAck, 0, payload); err != nil {
				return c.ioError(ctx, "write PING ACK", err)
			}
		}
	}
	return nil
}

func (c *Client) finishHeaderBlock(ctx context.Context, first frame, payload []byte) ([]byte, error) {
	if len(payload) > maxHeaderBlockSize {
		return nil, errors.New("h2c: header block exceeds limit")
	}
	block := append([]byte(nil), payload...)
	if first.flags&flagEndHeaders != 0 {
		return block, nil
	}
	for {
		f, payload, err := readFrame(c.conn)
		if err != nil {
			return nil, c.ioError(ctx, "read CONTINUATION", err)
		}
		if f.typ != frameTypeContinuation || f.streamID != first.streamID {
			return nil, errors.New("h2c: expected CONTINUATION frame")
		}
		if len(block) > maxHeaderBlockSize-len(payload) {
			return nil, errors.New("h2c: header block exceeds limit")
		}
		block = append(block, payload...)
		if f.flags&flagEndHeaders != 0 {
			return block, nil
		}
	}
}

func applySettings(payload []byte, streamWin, maxFrame *int) error {
	if len(payload)%6 != 0 {
		return errors.New("invalid SETTINGS payload length")
	}
	for i := 0; i < len(payload); i += 6 {
		id := binary.BigEndian.Uint16(payload[i:])
		val := binary.BigEndian.Uint32(payload[i+2:])
		switch id {
		case settingInitialWindowSize:
			if val > maxFlowWindow {
				return errors.New("invalid SETTINGS_INITIAL_WINDOW_SIZE")
			}
			*streamWin = int(val)
		case settingMaxFrameSize:
			if val < defaultMaxFrameSize || val > maxMaxFrameSize {
				return errors.New("invalid SETTINGS_MAX_FRAME_SIZE")
			}
			*maxFrame = int(val)
		case settingEnablePush:
			return errors.New("server sent SETTINGS_ENABLE_PUSH")
		}
	}
	return nil
}

func (c *Client) writeHeaderBlock(streamID uint32, block []byte) error {
	first := true
	for first || len(block) > 0 {
		n := min(len(block), c.maxFrame)
		typ := uint8(frameTypeContinuation)
		if first {
			typ = frameTypeHeaders
		}
		flags := uint8(0)
		if n == len(block) {
			flags = flagEndHeaders
		}
		if err := c.writeFrame(typ, flags, streamID, block[:n]); err != nil {
			return err
		}
		block = block[n:]
		first = false
	}
	return nil
}

func (c *Client) writeRSTStream(streamID, code uint32) error {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, code)
	return c.writeFrame(frameTypeRSTStream, 0, streamID, payload)
}
func (c *Client) writeFrame(typ, flags uint8, streamID uint32, payload []byte) error {
	return writeAll(c.conn, buildFrame(typ, flags, streamID, payload))
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func armConnContext(ctx context.Context, conn net.Conn) func() {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
		close(done)
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			if !stop() {
				<-done
			}
			_ = conn.SetDeadline(time.Time{})
		})
	}
}

func (c *Client) ioError(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("h2c: %s: %w", op, err)
}
func (c *Client) reset() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.nextID, c.connWin = 1, defaultWindowSize
	c.streamWin, c.maxFrame = defaultWindowSize, defaultMaxFrameSize
	c.draining = false
}

func readFrame(r io.Reader) (frame, []byte, error) {
	var hdr [9]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return frame{}, nil, err
	}
	f := parseFrameHeader(hdr)
	if f.length > defaultMaxFrameSize {
		return frame{}, nil, fmt.Errorf("h2c: inbound frame length %d exceeds maximum %d", f.length, defaultMaxFrameSize)
	}
	if err := validateFrameHeader(f); err != nil {
		return frame{}, nil, err
	}
	if f.length == 0 {
		return f, nil, nil
	}
	payload := make([]byte, f.length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return frame{}, nil, err
	}
	if (f.typ == frameTypeData || f.typ == frameTypeHeaders) && f.flags&flagPadded != 0 {
		if len(payload) == 0 {
			return frame{}, nil, errors.New("h2c: padded frame has no pad length")
		}
		padLen := int(payload[0])
		if padLen+1 > len(payload) {
			return frame{}, nil, errors.New("h2c: invalid padding length")
		}
		payload = payload[1 : len(payload)-padLen]
	}
	if f.typ == frameTypeHeaders && f.flags&flagPriority != 0 {
		if len(payload) < 5 {
			return frame{}, nil, errors.New("h2c: HEADERS PRIORITY too short")
		}
		payload = payload[5:]
	}
	return f, payload, nil
}

func validateFrameHeader(f frame) error {
	switch f.typ {
	case frameTypeData, frameTypeHeaders, frameTypeContinuation:
		if f.streamID == 0 {
			return errors.New("h2c: stream frame on stream 0")
		}
	case frameTypeRSTStream:
		if f.streamID == 0 || f.length != 4 {
			return errors.New("h2c: invalid RST_STREAM frame")
		}
	case frameTypeSettings:
		if f.streamID != 0 || f.length%6 != 0 || (f.flags&flagAck != 0 && f.length != 0) {
			return errors.New("h2c: invalid SETTINGS frame")
		}
	case frameTypePushPromise:
		if f.streamID == 0 {
			return errors.New("h2c: invalid PUSH_PROMISE frame")
		}
	case frameTypePing:
		if f.streamID != 0 || f.length != 8 {
			return errors.New("h2c: invalid PING frame")
		}
	case frameTypeGoAway:
		if f.streamID != 0 || f.length < 8 {
			return errors.New("h2c: invalid GOAWAY frame")
		}
	case frameTypeWindowUpdate:
		if f.length != 4 {
			return errors.New("h2c: invalid WINDOW_UPDATE frame")
		}
	}
	return nil
}

func windowUpdatePayload(increment uint32) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, increment&0x7fffffff)
	return payload
}
func makeWindowUpdate(streamID, increment uint32) []byte {
	return buildFrame(frameTypeWindowUpdate, 0, streamID, windowUpdatePayload(increment))
}
func windowIncrement(payload []byte) (int, error) {
	if len(payload) != 4 {
		return 0, errors.New("invalid payload length")
	}
	inc := int(binary.BigEndian.Uint32(payload) & 0x7fffffff)
	if inc == 0 {
		return 0, errors.New("zero increment")
	}
	return inc, nil
}
func goAwayError(payload []byte) error {
	lastID, code, err := parseGoAway(payload)
	if err != nil {
		return err
	}
	return fmt.Errorf("server sent GOAWAY (last stream %d, error code %d)", lastID, code)
}

func parseGoAway(payload []byte) (lastID, code uint32, err error) {
	if len(payload) < 8 {
		return 0, 0, errors.New("malformed GOAWAY")
	}
	lastID = binary.BigEndian.Uint32(payload[:4]) & 0x7fffffff
	code = binary.BigEndian.Uint32(payload[4:8])
	return lastID, code, nil
}

func parseGRPCStatus(s string) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
func hostWithoutPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
