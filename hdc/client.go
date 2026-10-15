// Package hdc is a pure Go client for the OpenHarmony Device Connector (hdc).
//
// It talks the hdc wire protocol directly over TCP, mirroring the behaviour of
// the original Node.js implementation (github.com/liriliri/hdckit). The hdc
// binary is only required to start the server and for a few commands (file
// transfer and install/uninstall) that the protocol does not expose.
package hdc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Default values used when Options fields are left zero.
const (
	DefaultHost = "127.0.0.1"
	DefaultPort = 8710
	DefaultBin  = "hdc"

	serverPortEnv = "OHOS_HDC_SERVER_PORT"
)

// ErrEmptyConnectKey is returned by Client.Target when the connect key is blank.
var ErrEmptyConnectKey = errors.New("hdc: connect key is required")

// Options configures a Client.
type Options struct {
	// Host is the hdc server host. Defaults to 127.0.0.1.
	Host string
	// Port is the hdc server port. When zero it is read from the
	// OHOS_HDC_SERVER_PORT environment variable, falling back to 8710.
	Port int
	// Bin is the path to the hdc binary. Defaults to "hdc".
	Bin string
}

// withDefaults returns a copy of o with zero values replaced by defaults.
func (o Options) withDefaults() Options {
	if o.Host == "" {
		o.Host = DefaultHost
	}
	if o.Port == 0 {
		o.Port = defaultPort()
	}
	if o.Bin == "" {
		o.Bin = DefaultBin
	}
	return o
}

// defaultPort returns the port from OHOS_HDC_SERVER_PORT, or DefaultPort.
func defaultPort() int {
	if v, err := strconv.Atoi(os.Getenv(serverPortEnv)); err == nil && v != 0 {
		return v
	}
	return DefaultPort
}

// Client is an hdc server client.
type Client struct {
	opts Options
}

// NewClient returns a Client configured with opts. Missing fields are filled
// with sensible defaults.
func NewClient(opts Options) *Client {
	return &Client{opts: opts.withDefaults()}
}

// Options returns the effective options used by the client.
func (c *Client) Options() Options { return c.opts }

// connection opens a new channel to the hdc server, transparently starting the
// server once if the first dial fails.
func (c *Client) connection(ctx context.Context, connectKey string) (*Connection, error) {
	conn := &Connection{opts: c.opts}
	err := conn.connect(ctx, connectKey)
	if err == nil {
		return conn, nil
	}

	// The server may not be running yet; try to start it and dial again.
	if startErr := c.startServer(ctx); startErr != nil {
		return nil, err
	}
	conn = &Connection{opts: c.opts}
	if err := conn.connect(ctx, connectKey); err != nil {
		return nil, err
	}
	return conn, nil
}

// startServer runs `hdc start` with the configured server port.
func (c *Client) startServer(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, c.opts.Bin, "start")
	cmd.Env = append(os.Environ(), serverPortEnv+"="+strconv.Itoa(c.opts.Port))
	return cmd.Run()
}

// ListTargets returns the connect keys of all connected devices.
func (c *Client) ListTargets(ctx context.Context) ([]string, error) {
	conn, err := c.connection(ctx, "")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.send(ctx, []byte("list targets")); err != nil {
		return nil, err
	}
	data, err := conn.readValue(ctx)
	if err != nil {
		return nil, err
	}
	return parseTargets(string(data)), nil
}

// Target returns a handle for the device identified by connectKey.
func (c *Client) Target(connectKey string) (*Target, error) {
	if strings.TrimSpace(connectKey) == "" {
		return nil, ErrEmptyConnectKey
	}
	return &Target{client: c, key: connectKey}, nil
}

// ListForwards returns all TCP forwards registered on the server.
func (c *Client) ListForwards(ctx context.Context) ([]Forward, error) {
	return c.listPorts(ctx, false)
}

// ListReverses returns all TCP reverses registered on the server.
func (c *Client) ListReverses(ctx context.Context) ([]Forward, error) {
	return c.listPorts(ctx, true)
}

func (c *Client) listPorts(ctx context.Context, reverse bool) ([]Forward, error) {
	conn, err := c.connection(ctx, "")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.send(ctx, []byte("fport ls")); err != nil {
		return nil, err
	}
	data, err := conn.readValue(ctx)
	if err != nil {
		return nil, err
	}
	return parsePorts(string(data), reverse), nil
}

// Kill terminates the hdc server process recorded in the server pid file.
func (c *Client) Kill() error {
	pid := lastServerPid()
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
