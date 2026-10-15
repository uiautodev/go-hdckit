package hdc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"
)

// Framing constants for the uitest agent RPC protocol.
const (
	uiRPCHeader = "_uitestkit_rpc_message_head_"
	uiRPCTailer = "_uitestkit_rpc_message_tail_"
)

// uiSessionHighByte marks a session id as using the framed protocol.
//
// The uitest agent only wraps its replies in the header/tailer framing when the
// most significant byte of the session id is non-zero. For small ids (high byte
// zero) it falls back to a bare JSON reply, which cannot be correlated. The
// reference implementation derives the id from a hash, which always has a
// non-zero high byte; we set it explicitly instead.
const uiSessionHighByte = 0x01000000

// uiResult is the outcome of a single RPC request.
type uiResult struct {
	payload json.RawMessage
	err     error
}

// uiRPCConn implements the uitest agent RPC framing:
//
//	header | sessionId(4) | len(4) | message(len) | tailer
type uiRPCConn struct {
	conn net.Conn

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[uint32]chan uiResult
	onMsg   func(sessionID uint32, payload []byte)
	onClose func()
	buf     []byte
	closed  bool
}

func (u *uiRPCConn) connect(ctx context.Context, port int) error {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	u.conn = conn
	u.pending = make(map[uint32]chan uiResult)
	go u.readLoop()
	return nil
}

func (u *uiRPCConn) setOnMessage(fn func(sessionID uint32, payload []byte)) {
	u.mu.Lock()
	u.onMsg = fn
	u.mu.Unlock()
}

// setOnClose registers a callback invoked once when the connection closes.
func (u *uiRPCConn) setOnClose(fn func()) {
	u.mu.Lock()
	u.onClose = fn
	u.mu.Unlock()
}

// send writes message and waits for its response. It returns the session id so
// callers can correlate streamed messages, along with the raw JSON `result`.
func (u *uiRPCConn) send(ctx context.Context, message any, timeout time.Duration) (uint32, json.RawMessage, error) {
	body, err := json.Marshal(message)
	if err != nil {
		return 0, nil, err
	}

	ch := make(chan uiResult, 1)
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return 0, nil, errors.New("hdc: ui connection is closed")
	}
	sessionID := newUISessionID(u.pending)
	u.pending[sessionID] = ch
	u.mu.Unlock()

	if err := u.writeFrame(sessionID, body); err != nil {
		u.forget(sessionID)
		return sessionID, nil, err
	}

	var timer *time.Timer
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	select {
	case <-ctx.Done():
		u.forget(sessionID)
		return sessionID, nil, ctx.Err()
	case <-timeoutCh:
		u.forget(sessionID)
		return sessionID, nil, errTimeout
	case res := <-ch:
		return sessionID, res.payload, res.err
	}
}

func (u *uiRPCConn) writeFrame(sessionID uint32, body []byte) error {
	u.writeMu.Lock()
	defer u.writeMu.Unlock()

	frame := make([]byte, 0, len(uiRPCHeader)+8+len(body)+len(uiRPCTailer))
	frame = append(frame, uiRPCHeader...)
	var header [8]byte
	binary.BigEndian.PutUint32(header[0:], sessionID)
	binary.BigEndian.PutUint32(header[4:], uint32(len(body)))
	frame = append(frame, header[:]...)
	frame = append(frame, body...)
	frame = append(frame, uiRPCTailer...)
	_, err := u.conn.Write(frame)
	return err
}

func (u *uiRPCConn) forget(sessionID uint32) {
	u.mu.Lock()
	delete(u.pending, sessionID)
	u.mu.Unlock()
}

func (u *uiRPCConn) Close() {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.closed = true
	pending := u.pending
	onClose := u.onClose
	u.pending = make(map[uint32]chan uiResult)
	u.mu.Unlock()

	if u.conn != nil {
		_ = u.conn.Close()
	}
	for _, ch := range pending {
		select {
		case ch <- uiResult{err: errors.New("hdc: ui connection is closed")}:
		default:
		}
	}
	if onClose != nil {
		onClose()
	}
}

func (u *uiRPCConn) readLoop() {
	header := []byte(uiRPCHeader)
	tailer := []byte(uiRPCTailer)
	tmp := make([]byte, 64*1024)

	for {
		n, err := u.conn.Read(tmp)
		if n > 0 {
			u.buf = append(u.buf, tmp[:n]...)
			u.drain(header, tailer)
		}
		if err != nil {
			u.Close()
			return
		}
	}
}

func (u *uiRPCConn) drain(header, tailer []byte) {
	for {
		if len(u.buf) < len(header)+8 {
			return
		}
		if string(u.buf[:len(header)]) != uiRPCHeader {
			u.buf = u.buf[:0]
			return
		}
		bodyLen := int(binary.BigEndian.Uint32(u.buf[len(header)+4 : len(header)+8]))
		total := len(header) + 8 + bodyLen + len(tailer)
		if len(u.buf) < total {
			return
		}
		sessionID := binary.BigEndian.Uint32(u.buf[len(header) : len(header)+4])
		body := u.buf[len(header)+8 : len(header)+8+bodyLen]
		if string(u.buf[len(header)+8+bodyLen:total]) != uiRPCTailer {
			u.buf = u.buf[:0]
			return
		}
		u.buf = u.buf[total:]

		payload := make([]byte, len(body))
		copy(payload, body)

		u.mu.Lock()
		ch, ok := u.pending[sessionID]
		if ok {
			delete(u.pending, sessionID)
		}
		handler := u.onMsg
		u.mu.Unlock()

		if ok {
			ch <- decodeUIResult(payload)
		} else if handler != nil {
			handler(sessionID, payload)
		}
	}
}

// newUISessionID returns an unused session id with a non-zero high byte. The
// caller must hold u.mu.
func newUISessionID(pending map[uint32]chan uiResult) uint32 {
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return uint32(time.Now().UnixNano()) | uiSessionHighByte
		}
		id := binary.BigEndian.Uint32(b[:]) | uiSessionHighByte
		if _, exists := pending[id]; !exists {
			return id
		}
	}
}

func decodeUIResult(payload []byte) uiResult {
	var envelope struct {
		Result    json.RawMessage `json:"result"`
		Exception *struct {
			Message string `json:"message"`
		} `json:"exception"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return uiResult{payload: payload}
	}
	if envelope.Exception != nil {
		return uiResult{err: errors.New(envelope.Exception.Message)}
	}
	return uiResult{payload: envelope.Result}
}
