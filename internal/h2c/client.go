package h2c

import (
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// clientPreface is the HTTP/2 client connection preface (RFC 7540 §3.5).
const clientPreface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// Client is a minimal HTTP/2 client for making unary gRPC calls over h2c or h2 (TLS).
// One RPC at a time is supported; the mutex serialises concurrent callers.
type Client struct {
	host    string // "host:port"
	tls     bool   // true = TLS (h2 via ALPN), false = cleartext (h2c prior knowledge)
	timeout time.Duration

	mu       sync.Mutex
	conn     net.Conn
	nextID   uint32 // next stream ID to use (odd, client-initiated)
	connWin  int    // connection-level send window
	streamWin int   // per-stream send window (default)
}

// NewClient creates a new h2c/h2 client for the given host:port.
// If tlsEnabled is true, TLS is used (ALPN h2). Otherwise cleartext h2c prior knowledge.
func NewClient(host string, tlsEnabled bool, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		host:      host,
		tls:       tlsEnabled,
		timeout:   timeout,
		nextID:    1,
		connWin:   defaultWindowSize,
		streamWin: defaultWindowSize,
	}
}

// Do sends an HTTP/2 POST request and returns the response body.
// headers are additional request headers in name-value pairs.
// The body is sent as-is (callers apply gRPC framing before calling).
func (c *Client) Do(path string, body []byte, extraHeaders map[string]string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConn(); err != nil {
		return nil, err
	}

	streamID := c.nextID
	c.nextID += 2

	// Encode request headers
	hpackBlock := EncodeRequestHeaders(c.host, path, c.tls)
	for k, v := range extraHeaders {
		hpackBlock = appendLiteralNewName(hpackBlock, k, v)
	}

	// HEADERS frame (not END_STREAM — body follows)
	hf := buildFrame(frameTypeHeaders, flagEndHeaders, streamID, hpackBlock)
	if err := c.write(hf); err != nil {
		c.reset()
		return nil, fmt.Errorf("h2c: write HEADERS: %w", err)
	}

	// DATA frame(s): send body with END_STREAM
	// Respect remote send window (simple: send in one shot if fits)
	remaining := body
	for len(remaining) > 0 {
		allowed := c.connWin
		if c.streamWin < allowed {
			allowed = c.streamWin
		}
		if allowed <= 0 {
			// Wait for WINDOW_UPDATE — just drain a frame
			if err := c.drainOneFrame(streamID); err != nil {
				c.reset()
				return nil, fmt.Errorf("h2c: waiting for window: %w", err)
			}
			continue
		}
		chunk := remaining
		if len(chunk) > allowed {
			chunk = chunk[:allowed]
		}
		flags := uint8(0)
		if len(chunk) == len(remaining) {
			flags = flagEndStream
		}
		df := buildFrame(frameTypeData, flags, streamID, chunk)
		if err := c.write(df); err != nil {
			c.reset()
			return nil, fmt.Errorf("h2c: write DATA: %w", err)
		}
		c.connWin -= len(chunk)
		c.streamWin -= len(chunk)
		remaining = remaining[len(chunk):]
	}
	if len(body) == 0 {
		// Send empty DATA with END_STREAM
		df := buildFrame(frameTypeData, flagEndStream, streamID, nil)
		if err := c.write(df); err != nil {
			c.reset()
			return nil, fmt.Errorf("h2c: write empty DATA: %w", err)
		}
	}

	// Read response until END_STREAM on our stream
	respBody, grpcStatus, err := c.readResponse(streamID)
	if err != nil {
		c.reset()
		return nil, err
	}
	if grpcStatus != 0 {
		return nil, fmt.Errorf("h2c: gRPC status %d", grpcStatus)
	}
	return respBody, nil
}

// Shutdown closes the underlying connection.
func (c *Client) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		// Send GOAWAY then close
		goaway := buildFrame(frameTypeGoAway, 0, 0, func() []byte {
			b := make([]byte, 8)
			binary.BigEndian.PutUint32(b[0:], c.nextID-2)
			binary.BigEndian.PutUint32(b[4:], errCodeNoError)
			return b
		}())
		_ = c.conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
		_, _ = c.conn.Write(goaway)
		_ = c.conn.Close()
		c.conn = nil
	}
}

// ensureConn establishes the HTTP/2 connection if not already connected.
func (c *Client) ensureConn() error {
	if c.conn != nil {
		return nil
	}
	return c.connect()
}

// connect dials and performs the HTTP/2 handshake.
func (c *Client) connect() error {
	var conn net.Conn
	var err error

	d := net.Dialer{Timeout: c.timeout}
	if c.tls {
		tlsCfg := &tls.Config{
			ServerName: hostWithoutPort(c.host),
			NextProtos: []string{"h2"},
		}
		conn, err = tls.DialWithDialer(&d, "tcp", c.host, tlsCfg)
	} else {
		conn, err = d.Dial("tcp", c.host)
	}
	if err != nil {
		return fmt.Errorf("h2c: dial %s: %w", c.host, err)
	}

	if err = conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("h2c: set deadline: %w", err)
	}

	// Send client connection preface
	if _, err = io.WriteString(conn, clientPreface); err != nil {
		_ = conn.Close()
		return fmt.Errorf("h2c: write preface: %w", err)
	}

	// Send client SETTINGS: disable push, set header table size to 0
	settingsPayload := encodeSettings([]settingParam{
		{settingEnablePush, 0},
		{settingHeaderTableSize, 0},
		{settingInitialWindowSize, uint32(defaultWindowSize)},
	})
	sf := buildFrame(frameTypeSettings, 0, 0, settingsPayload)
	if _, err = conn.Write(sf); err != nil {
		_ = conn.Close()
		return fmt.Errorf("h2c: write SETTINGS: %w", err)
	}

	// Read and process frames until we've seen server SETTINGS and sent ACK
	gotServerSettings := false
	for !gotServerSettings {
		f, payload, rerr := readFrame(conn)
		if rerr != nil {
			_ = conn.Close()
			return fmt.Errorf("h2c: handshake read: %w", rerr)
		}
		switch f.typ {
		case frameTypeSettings:
			if f.flags&flagAck != 0 {
				// ACK to our SETTINGS — handshake complete
				gotServerSettings = true
				continue
			}
			// Parse server settings
			c.applyServerSettings(payload)
			// Send ACK
			ack := buildFrame(frameTypeSettings, flagAck, 0, nil)
			if _, err = conn.Write(ack); err != nil {
				_ = conn.Close()
				return fmt.Errorf("h2c: write SETTINGS ACK: %w", err)
			}
			gotServerSettings = true

		case frameTypeWindowUpdate:
			if len(payload) == 4 {
				inc := int(binary.BigEndian.Uint32(payload) & 0x7fffffff)
				c.connWin += inc
			}

		case frameTypeGoAway:
			_ = conn.Close()
			return errors.New("h2c: server sent GOAWAY during handshake")
		}
	}

	_ = conn.SetDeadline(time.Time{}) // clear deadline; per-operation timeouts used
	c.conn = conn
	c.connWin = defaultWindowSize
	c.streamWin = defaultWindowSize
	return nil
}

// applyServerSettings updates client state based on server SETTINGS payload.
func (c *Client) applyServerSettings(payload []byte) {
	for i := 0; i+6 <= len(payload); i += 6 {
		id := binary.BigEndian.Uint16(payload[i:])
		val := binary.BigEndian.Uint32(payload[i+2:])
		switch id {
		case settingInitialWindowSize:
			c.streamWin = int(val)
		}
	}
}

// readResponse reads frames until END_STREAM on streamID.
// Returns the concatenated DATA payload and the gRPC status code from trailers.
func (c *Client) readResponse(streamID uint32) (body []byte, grpcStatus int, err error) {
	_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()

	for {
		f, payload, rerr := readFrame(c.conn)
		if rerr != nil {
			return nil, 0, fmt.Errorf("h2c: read response: %w", rerr)
		}

		switch f.typ {
		case frameTypeData:
			if f.streamID == streamID {
				// Send WINDOW_UPDATE to replenish flow control
				if len(payload) > 0 {
					wu := makeWindowUpdate(streamID, uint32(len(payload)))
					_ = c.write(wu)
					wu = makeWindowUpdate(0, uint32(len(payload)))
					_ = c.write(wu)
				}
				body = append(body, payload...)
				if f.flags&flagEndStream != 0 {
					return body, grpcStatus, nil
				}
			}

		case frameTypeHeaders:
			if f.streamID == streamID {
				headers := DecodeHeaders(payload)
				for _, h := range headers {
					if h.name == "grpc-status" {
						grpcStatus = parseGRPCStatus(h.value)
					}
				}
				if f.flags&flagEndStream != 0 {
					return body, grpcStatus, nil
				}
			}

		case frameTypeRSTStream:
			if f.streamID == streamID && len(payload) == 4 {
				code := binary.BigEndian.Uint32(payload)
				return nil, 0, fmt.Errorf("h2c: stream RST_STREAM error code %d", code)
			}

		case frameTypeGoAway:
			c.reset()
			return nil, 0, errors.New("h2c: server sent GOAWAY")

		case frameTypeWindowUpdate:
			inc := uint32(0)
			if len(payload) == 4 {
				inc = binary.BigEndian.Uint32(payload) & 0x7fffffff
			}
			switch f.streamID {
			case 0:
				c.connWin += int(inc)
			case streamID:
				c.streamWin += int(inc)
			}

		case frameTypeSettings:
			if f.flags&flagAck == 0 {
				c.applyServerSettings(payload)
				ack := buildFrame(frameTypeSettings, flagAck, 0, nil)
				_ = c.write(ack)
			}

		case frameTypePing:
			if f.flags&flagAck == 0 {
				pong := buildFrame(frameTypePing, flagAck, 0, payload)
				_ = c.write(pong)
			}
		}
	}
}

// drainOneFrame reads and processes a single frame (used while waiting for window).
func (c *Client) drainOneFrame(streamID uint32) error {
	_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	f, payload, err := readFrame(c.conn)
	if err != nil {
		return err
	}
	if f.typ == frameTypeWindowUpdate && len(payload) == 4 {
		inc := int(binary.BigEndian.Uint32(payload) & 0x7fffffff)
		switch f.streamID {
		case 0:
			c.connWin += inc
		case streamID:
			c.streamWin += inc
		}
	}
	return nil
}

// write sends bytes on the connection.
func (c *Client) write(b []byte) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	_, err := c.conn.Write(b)
	return err
}

// reset closes the current connection so the next call reconnects.
func (c *Client) reset() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.connWin = defaultWindowSize
	c.streamWin = defaultWindowSize
}

// readFrame reads one HTTP/2 frame from r.
func readFrame(r io.Reader) (frame, []byte, error) {
	var hdr [9]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return frame{}, nil, err
	}
	f := parseFrameHeader(hdr)
	if f.length == 0 {
		return f, nil, nil
	}
	payload := make([]byte, f.length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return frame{}, nil, err
	}
	// Strip padding if PADDED flag is set on DATA/HEADERS frames
	if (f.typ == frameTypeData || f.typ == frameTypeHeaders) && f.flags&flagPadded != 0 {
		if len(payload) == 0 {
			return f, payload, nil
		}
		padLen := int(payload[0])
		if padLen+1 > len(payload) {
			return frame{}, nil, errors.New("h2c: invalid padding length")
		}
		payload = payload[1 : len(payload)-padLen]
	}
	// Strip PRIORITY if PRIORITY flag set on HEADERS frame
	if f.typ == frameTypeHeaders && f.flags&flagPriority != 0 {
		if len(payload) < 5 {
			return frame{}, nil, errors.New("h2c: HEADERS PRIORITY too short")
		}
		payload = payload[5:]
	}
	return f, payload, nil
}

// makeWindowUpdate builds a WINDOW_UPDATE frame.
func makeWindowUpdate(streamID, increment uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], increment&0x7fffffff)
	return buildFrame(frameTypeWindowUpdate, 0, streamID, b[:])
}

// parseGRPCStatus parses a gRPC status code string to int (0 = OK).
func parseGRPCStatus(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// hostWithoutPort strips the port from a host:port string.
func hostWithoutPort(h string) string {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i] == ':' {
			return h[:i]
		}
	}
	return h
}
