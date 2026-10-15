package hdc

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestCmpVersion(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.1.0", "1.1.0", 0},
		{"1.0.0", "1.1.0", -1},
		{"1.2", "1.1.9", 1},
		{"1.1", "1.1.0", 0},
	}
	for _, tt := range tests {
		if got := cmpVersion(tt.a, tt.b); got != tt.want {
			t.Errorf("cmpVersion(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSDKVersionOf(t *testing.T) {
	raw := "blah UITEST_AGENT_LIBRARY @v1.1.0\nmore binary"
	if got := sdkVersionOf(raw); got != "1.1.0" {
		t.Errorf("sdkVersionOf = %q, want 1.1.0", got)
	}
	if got := sdkVersionOf("no marker"); got != "" {
		t.Errorf("sdkVersionOf = %q, want empty", got)
	}
}

func TestParseTCPPort(t *testing.T) {
	if got, err := parseTCPPort("tcp:8012"); err != nil || got != 8012 {
		t.Errorf("parseTCPPort = %d, %v", got, err)
	}
	if _, err := parseTCPPort("bogus"); err == nil {
		t.Error("expected error for invalid port")
	}
}

func TestDecodeUIResult(t *testing.T) {
	res := decodeUIResult([]byte(`{"result":{"x":5,"y":6}}`))
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if string(res.payload) != `{"x":5,"y":6}` {
		t.Errorf("payload = %s", res.payload)
	}

	res = decodeUIResult([]byte(`{"exception":{"message":"boom"}}`))
	if res.err == nil || res.err.Error() != "boom" {
		t.Errorf("expected boom error, got %v", res.err)
	}

	res = decodeUIResult([]byte(`not json`))
	if res.err != nil || string(res.payload) != "not json" {
		t.Errorf("expected raw payload, got %q / %v", res.payload, res.err)
	}
}

func readUIFrame(t *testing.T, r io.Reader) (uint32, []byte) {
	t.Helper()
	br := bufio.NewReader(r)
	header := make([]byte, len(uiRPCHeader))
	if _, err := io.ReadFull(br, header); err != nil {
		t.Fatalf("read header: %v", err)
	}
	if string(header) != uiRPCHeader {
		t.Fatalf("bad header %q", header)
	}
	var meta [8]byte
	if _, err := io.ReadFull(br, meta[:]); err != nil {
		t.Fatalf("read meta: %v", err)
	}
	sessionID := binary.BigEndian.Uint32(meta[0:])
	length := binary.BigEndian.Uint32(meta[4:])
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	tailer := make([]byte, len(uiRPCTailer))
	if _, err := io.ReadFull(br, tailer); err != nil {
		t.Fatalf("read tailer: %v", err)
	}
	if string(tailer) != uiRPCTailer {
		t.Fatalf("bad tailer %q", tailer)
	}
	return sessionID, body
}

func writeUIFrame(w io.Writer, sessionID uint32, body []byte) {
	buf := make([]byte, 0, len(uiRPCHeader)+8+len(body)+len(uiRPCTailer))
	buf = append(buf, uiRPCHeader...)
	var meta [8]byte
	binary.BigEndian.PutUint32(meta[0:], sessionID)
	binary.BigEndian.PutUint32(meta[4:], uint32(len(body)))
	buf = append(buf, meta[:]...)
	buf = append(buf, body...)
	buf = append(buf, uiRPCTailer...)
	w.Write(buf)
}

func TestUIRPCSendReceive(t *testing.T) {
	client, server := net.Pipe()
	u := &uiRPCConn{conn: client, pending: make(map[uint32]chan uiResult)}
	go u.readLoop()
	defer u.Close()

	go func() {
		sessionID, body := readUIFrame(t, server)
		if !strings.Contains(string(body), `"api":"Driver.create"`) {
			t.Errorf("unexpected request body: %s", body)
		}
		writeUIFrame(server, sessionID, []byte(`{"result":{"width":5,"height":6}}`))
	}()

	sessionID, res, err := u.send(context.Background(), map[string]any{"api": "Driver.create"}, time.Second)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sessionID == 0 {
		t.Error("expected non-zero session id")
	}
	if sessionID < uiSessionHighByte {
		t.Errorf("session id %#x must have a non-zero high byte", sessionID)
	}
	var size Size
	if err := json.Unmarshal(res, &size); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if size.Width != 5 || size.Height != 6 {
		t.Errorf("size = %dx%d, want 5x6", size.Width, size.Height)
	}
}

func TestUIRPCOnClose(t *testing.T) {
	client, server := net.Pipe()
	u := &uiRPCConn{conn: client, pending: make(map[uint32]chan uiResult)}

	done := make(chan struct{})
	u.setOnClose(func() { close(done) })
	go u.readLoop()

	server.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onClose was not invoked when the connection dropped")
	}
}

func TestUIRPCOnMessage(t *testing.T) {
	client, server := net.Pipe()
	u := &uiRPCConn{conn: client, pending: make(map[uint32]chan uiResult)}

	got := make(chan []byte, 1)
	u.setOnMessage(func(sessionID uint32, payload []byte) {
		got <- payload
	})
	go u.readLoop()
	defer u.Close()

	writeUIFrame(server, 42, []byte("frame-bytes"))

	select {
	case payload := <-got:
		if string(payload) != "frame-bytes" {
			t.Errorf("payload = %q", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message")
	}
}
