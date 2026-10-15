package hdc

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// writeValue writes a length-prefixed value to w.
func writeValue(t *testing.T, w io.Writer, data []byte) {
	t.Helper()
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf, uint32(len(data)))
	copy(buf[4:], data)
	if _, err := w.Write(buf); err != nil {
		t.Fatalf("write value: %v", err)
	}
}

func readValue(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		t.Fatalf("read header: %v", err)
	}
	buf := make([]byte, binary.BigEndian.Uint32(header[:]))
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return buf
}

func TestConnectionHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	banner := []byte("OHOS HDC\x00\x00\x00\x00")
	replyCh := make(chan []byte, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		hello := append(append([]byte{}, banner...), 0, 0, 0, 1)
		writeValue(t, conn, hello)
		replyCh <- readValue(t, conn)
	}()

	addr := ln.Addr().(*net.TCPAddr)
	conn := &Connection{opts: Options{Host: "127.0.0.1", Port: addr.Port}}
	if err := conn.connect(context.Background(), "testkey"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	reply := <-replyCh
	if len(reply) != 12+maxConnectKeySize {
		t.Fatalf("reply length = %d, want %d", len(reply), 12+maxConnectKeySize)
	}
	if string(reply[:12]) != string(banner) {
		t.Errorf("banner = %q, want %q", reply[:12], banner)
	}
	if got := string(reply[12 : 12+len("testkey")]); got != "testkey" {
		t.Errorf("connect key = %q, want testkey", got)
	}
}

func TestConnectionFraming(t *testing.T) {
	client, server := net.Pipe()
	c := &Connection{conn: client}

	received := make(chan []byte, 1)
	go func() {
		received <- readValue(t, server)
		writeValue(t, server, []byte("pong"))
	}()

	if err := c.send(context.Background(), []byte("ping")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := <-received; string(got) != "ping" {
		t.Errorf("server received %q, want ping", got)
	}

	value, err := c.readValue(context.Background())
	if err != nil {
		t.Fatalf("readValue: %v", err)
	}
	if string(value) != "pong" {
		t.Errorf("readValue = %q, want pong", value)
	}
}

func TestConnectionReadAllEndsOnEOF(t *testing.T) {
	client, server := net.Pipe()
	c := &Connection{conn: client}

	go func() {
		writeValue(t, server, []byte("one"))
		writeValue(t, server, []byte("two"))
		server.Close()
	}()

	all, err := c.ReadAll(context.Background())
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(all) != "onetwo" {
		t.Errorf("ReadAll = %q, want onetwo", all)
	}
}

func TestConnectionReadContextCancel(t *testing.T) {
	client, _ := net.Pipe()
	c := &Connection{conn: client}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := c.readValue(ctx); err == nil {
		t.Fatal("expected context error, got nil")
	}
}
