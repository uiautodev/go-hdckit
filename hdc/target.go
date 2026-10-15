package hdc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	readyTimeout  = 10 * time.Second
	readyInterval = time.Second
)

// Target is a handle to a single device.
type Target struct {
	client *Client
	key    string

	mu    sync.Mutex
	ready bool
}

// ConnectKey returns the target connect key.
func (t *Target) ConnectKey() string { return t.key }

// transport waits for the device to become ready and then opens a fresh
// channel to it. The caller owns the returned connection and must close it.
func (t *Target) transport(ctx context.Context) (*Connection, error) {
	if err := t.waitReady(ctx); err != nil {
		return nil, err
	}
	return t.client.connection(ctx, t.key)
}

// waitReady polls the device until it can serve a shell command. It is a no-op
// on subsequent calls once the device has been observed ready.
func (t *Target) waitReady(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ready {
		return nil
	}

	deadline := time.Now().Add(readyTimeout)
	for {
		if t.checkReady(ctx) {
			t.ready = true
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hdc: target %s is not ready", t.key)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyInterval):
		}
	}
}

func (t *Target) checkReady(ctx context.Context) bool {
	conn, err := t.client.connection(ctx, t.key)
	if err != nil {
		return false
	}
	defer conn.Close()

	if err := conn.send(ctx, []byte("shell echo ready\n")); err != nil {
		return false
	}
	data, err := conn.ReadAll(ctx)
	if err != nil {
		return false
	}
	return !bytes.Contains(data, []byte("E000004"))
}

// Parameters returns the device parameters reported by `param get`.
func (t *Target) Parameters(ctx context.Context) (map[string]string, error) {
	conn, err := t.transport(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.send(ctx, []byte("shell param get")); err != nil {
		return nil, err
	}
	data, err := conn.ReadAll(ctx)
	if err != nil {
		return nil, err
	}
	return parseParameters(string(data)), nil
}

// Shell runs command on the device and returns the connection streaming its
// output. Use Connection.ReadAll to consume the output, or read incrementally
// for long running commands.
func (t *Target) Shell(ctx context.Context, command string) (*Connection, error) {
	conn, err := t.transport(ctx)
	if err != nil {
		return nil, err
	}
	if err := conn.send(ctx, []byte("shell "+command)); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// RunShell runs command on the device and returns its complete output.
func (t *Target) RunShell(ctx context.Context, command string) ([]byte, error) {
	conn, err := t.Shell(ctx, command)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.ReadAll(ctx)
}

// Forward registers a forward from local to remote.
func (t *Target) Forward(ctx context.Context, local, remote string) error {
	return t.runPortCommand(ctx, fmt.Sprintf("fport %s %s", local, remote), "OK")
}

// ListForwards returns the forwards belonging to this target.
func (t *Target) ListForwards(ctx context.Context) ([]Forward, error) {
	all, err := t.client.ListForwards(ctx)
	if err != nil {
		return nil, err
	}
	return filterForwards(all, t.key), nil
}

// RemoveForward removes a previously registered forward.
func (t *Target) RemoveForward(ctx context.Context, local, remote string) error {
	return t.runPortCommand(ctx, fmt.Sprintf("fport rm %s %s", local, remote), "success")
}

// Reverse registers a reverse from remote to local.
func (t *Target) Reverse(ctx context.Context, remote, local string) error {
	return t.runPortCommand(ctx, fmt.Sprintf("rport %s %s", remote, local), "OK")
}

// ListReverses returns the reverses belonging to this target.
func (t *Target) ListReverses(ctx context.Context) ([]Forward, error) {
	all, err := t.client.ListReverses(ctx)
	if err != nil {
		return nil, err
	}
	return filterForwards(all, t.key), nil
}

// RemoveReverse removes a previously registered reverse.
func (t *Target) RemoveReverse(ctx context.Context, remote, local string) error {
	return t.runPortCommand(ctx, fmt.Sprintf("fport rm %s %s", remote, local), "success")
}

func (t *Target) runPortCommand(ctx context.Context, command, expect string) error {
	conn, err := t.transport(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.send(ctx, []byte(command)); err != nil {
		return err
	}
	data, err := conn.readValue(ctx)
	if err != nil {
		return err
	}
	if !strings.Contains(string(data), expect) {
		return errors.New(string(data))
	}
	return nil
}

// SendFile uploads a local file to the device.
func (t *Target) SendFile(ctx context.Context, local, remote string) error {
	abs, err := filepath.Abs(local)
	if err != nil {
		return err
	}
	return t.runExec(ctx, []string{"file", "send", abs, remote}, "finish")
}

// RecvFile downloads a remote device file to the local filesystem.
func (t *Target) RecvFile(ctx context.Context, remote, local string) error {
	abs, err := filepath.Abs(local)
	if err != nil {
		return err
	}
	return t.runExec(ctx, []string{"file", "recv", remote, abs}, "finish")
}

// Install installs a .hap package on the device.
func (t *Target) Install(ctx context.Context, hap string) error {
	abs, err := filepath.Abs(hap)
	if err != nil {
		return err
	}
	return t.runExec(ctx, []string{"install", abs}, "install bundle successfully")
}

// Uninstall uninstalls the app with the given bundle name.
func (t *Target) Uninstall(ctx context.Context, bundleName string) error {
	return t.runExec(ctx, []string{"uninstall", bundleName}, "uninstall bundle successfully")
}

// runExec runs an hdc subcommand against this target, requiring expect to
// appear in the combined output.
func (t *Target) runExec(ctx context.Context, args []string, expect string) error {
	out, err := t.exec(ctx, args...)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), expect) {
		return fmt.Errorf("hdc %s: unexpected output: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func filterForwards(all []Forward, key string) []Forward {
	out := make([]Forward, 0, len(all))
	for _, f := range all {
		if f.Target == key {
			out = append(out, f)
		}
	}
	return out
}

// execArgs builds the hdc argument list targeting this device.
func (t *Target) execArgs(args ...string) []string {
	return append([]string{"-s", t.client.opts.Host + ":" + strconv.Itoa(t.client.opts.Port), "-t", t.key}, args...)
}

func (t *Target) exec(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, t.client.opts.Bin, t.execArgs(args...)...)
	cmd.Env = append(os.Environ(), serverPortEnv+"="+strconv.Itoa(t.client.opts.Port))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return out.Bytes(), fmt.Errorf(
			"hdc %s: %w: %s",
			strings.Join(args, " "),
			err,
			strings.TrimSpace(out.String()),
		)
	}
	return out.Bytes(), nil
}
