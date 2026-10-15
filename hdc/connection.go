package hdc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// handshakeBanner is the magic prefix sent by the hdc server during the
// channel handshake.
const handshakeBanner = "OHOS HDC"

// maxConnectKeySize is the fixed length of the connect key field in a
// handshake response.
const maxConnectKeySize = 32

// Connection is a single logical channel to the hdc server. It speaks the
// simple length-prefixed framing used by hdc: every message is a 4-byte
// big-endian length followed by that many bytes.
//
// A Connection is not safe for concurrent use.
type Connection struct {
	conn net.Conn
	opts Options

	closeOnce sync.Once
}

// connect dials the hdc server and performs the channel handshake. When
// connectKey is empty the server selects the only connected device.
func (c *Connection) connect(ctx context.Context, connectKey string) error {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.opts.Host, strconv.Itoa(c.opts.Port)))
	if err != nil {
		return err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	c.conn = conn

	hello, err := c.readValue(ctx)
	if err != nil {
		c.Close()
		return err
	}
	if !strings.HasPrefix(string(hello), handshakeBanner) {
		c.Close()
		return errors.New("hdc: channel handshake failed")
	}

	// Reply with the 12-byte banner followed by the 32-byte connect key.
	banner := make([]byte, 12)
	copy(banner, hello)
	key := make([]byte, maxConnectKeySize)
	copy(key, connectKey)
	if err := c.send(ctx, append(banner, key...)); err != nil {
		c.Close()
		return err
	}

	return nil
}

// send writes data prefixed with its length in bytes.
func (c *Connection) send(ctx context.Context, data []byte) error {
	if c.conn == nil {
		return errors.New("hdc: connection is closed")
	}
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf, uint32(len(data)))
	copy(buf[4:], data)
	return c.write(ctx, buf)
}

// ReadAll reads length-prefixed values until the peer closes the stream or
// ctx is cancelled, returning everything read so far.
func (c *Connection) ReadAll(ctx context.Context) ([]byte, error) {
	all := []byte{}
	for {
		value, err := c.readValue(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return all, nil
			}
			return all, err
		}
		all = append(all, value...)
	}
}

// Close closes the underlying connection. It is safe to call multiple times
// and from a different goroutine than the one blocked in a read.
func (c *Connection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		if c.conn != nil {
			err = c.conn.Close()
		}
	})
	return err
}

// readValue reads a single length-prefixed value.
func (c *Connection) readValue(ctx context.Context) ([]byte, error) {
	var header [4]byte
	if err := c.readFull(ctx, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	if err := c.readFull(ctx, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// readFull reads exactly len(buf) bytes, unblocking early if ctx is done.
func (c *Connection) readFull(ctx context.Context, buf []byte) error {
	conn := c.conn
	if conn == nil {
		return errors.New("hdc: connection is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Done() == nil {
		_, err := io.ReadFull(conn, buf)
		return err
	}

	err := runWithContext(ctx, func() error {
		_, err := io.ReadFull(conn, buf)
		return err
	}, conn.SetReadDeadline)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

func (c *Connection) write(ctx context.Context, buf []byte) error {
	conn := c.conn
	if conn == nil {
		return errors.New("hdc: connection is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Done() == nil {
		if _, err := conn.Write(buf); err != nil {
			return fmt.Errorf("hdc: write: %w", err)
		}
		return nil
	}

	err := runWithContext(ctx, func() error {
		_, err := conn.Write(buf)
		return err
	}, conn.SetWriteDeadline)
	_ = conn.SetWriteDeadline(time.Time{})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("hdc: write: %w", err)
	}
	return nil
}

// runWithContext runs fn while guaranteeing that, if ctx is cancelled, setDeadline
// is invoked to unblock fn and the watcher goroutine has exited before
// runWithContext returns.
func runWithContext(ctx context.Context, fn func() error, setDeadline func(time.Time) error) error {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			_ = setDeadline(time.Now())
		case <-done:
		}
	}()
	err := fn()
	close(done)
	wg.Wait()
	return err
}
