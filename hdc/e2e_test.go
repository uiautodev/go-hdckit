//go:build e2e

// Package hdc_test contains end-to-end tests that talk to a real hdc server
// and, when a device is connected, a real OpenHarmony device.
//
// Run them with:
//
//	go test -tags e2e -v ./hdc/
//
// Tests that need a device are skipped automatically when none is connected.
// A few heavier checks are opt-in via environment variables:
//
//	HDCKIT_E2E_UI=1     exercise UiDriver (starts the uitest daemon)
//	HDCKIT_E2E_HILOG=1  exercise the hilog stream
//	HDCKIT_E2E_BIN      path to the hdc binary (defaults to $PATH)
package hdc_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/uiautodev/go-hdckit/hdc"
)

const callTimeout = 30 * time.Second

func newClient(t *testing.T) *hdc.Client {
	t.Helper()
	bin := os.Getenv("HDCKIT_E2E_BIN")
	if bin == "" {
		bin = "hdc"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("hdc binary %q not found in PATH: %v", bin, err)
	}
	opts := hdc.Options{Bin: bin}
	if p := os.Getenv("HDCKIT_E2E_PORT"); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("invalid HDCKIT_E2E_PORT %q: %v", p, err)
		}
		opts.Port = port
	}
	return hdc.NewClient(opts)
}

// requireTarget returns a client and the first connected target, skipping the
// test when no device is available.
func requireTarget(t *testing.T) (*hdc.Client, *hdc.Target) {
	t.Helper()
	client := newClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	targets, err := client.ListTargets(ctx)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(targets) == 0 {
		t.Skip("no target connected; connect a device to run this test")
	}
	target, err := client.Target(targets[0])
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	t.Logf("using target %s", targets[0])
	return client, target
}

func TestE2E_Options(t *testing.T) {
	client := newClient(t)
	opts := client.Options()

	wantPort := hdc.DefaultPort
	if p := os.Getenv("OHOS_HDC_SERVER_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			wantPort = v
		}
	}
	if opts.Host != hdc.DefaultHost {
		t.Errorf("Host = %q, want %q", opts.Host, hdc.DefaultHost)
	}
	if opts.Port != wantPort {
		t.Errorf("Port = %d, want %d", opts.Port, wantPort)
	}
}

func TestE2E_ListTargets(t *testing.T) {
	client := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	targets, err := client.ListTargets(ctx)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	t.Logf("targets: %v", targets)
}

func TestE2E_ListForwards(t *testing.T) {
	client := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	forwards, err := client.ListForwards(ctx)
	if err != nil {
		t.Fatalf("ListForwards: %v", err)
	}
	t.Logf("forwards: %+v", forwards)
}

func TestE2E_ListReverses(t *testing.T) {
	client := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	reverses, err := client.ListReverses(ctx)
	if err != nil {
		t.Fatalf("ListReverses: %v", err)
	}
	t.Logf("reverses: %+v", reverses)
}

func TestE2E_Shell(t *testing.T) {
	_, target := requireTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	out, err := target.RunShell(ctx, "echo hdckit-e2e")
	if err != nil {
		t.Fatalf("RunShell: %v", err)
	}
	if want := "hdckit-e2e"; !bytes.Contains(out, []byte(want)) {
		t.Fatalf("shell output = %q, want it to contain %q", out, want)
	}
}

func TestE2E_Parameters(t *testing.T) {
	_, target := requireTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	params, err := target.Parameters(ctx)
	if err != nil {
		t.Fatalf("Parameters: %v", err)
	}
	if len(params) == 0 {
		t.Fatal("expected non-empty parameters")
	}
	if name := params["const.product.name"]; name == "" {
		t.Errorf("const.product.name missing in %d params", len(params))
	} else {
		t.Logf("const.product.name = %s", name)
	}
}

func TestE2E_ForwardLifecycle(t *testing.T) {
	_, target := requireTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	local := "tcp:" + strconv.Itoa(freePort(t))
	remote := "tcp:" + strconv.Itoa(freePort(t))

	if err := target.Forward(ctx, local, remote); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	defer target.RemoveForward(ctx, local, remote)

	forwards, err := target.ListForwards(ctx)
	if err != nil {
		t.Fatalf("ListForwards: %v", err)
	}
	if !hasPort(forwards, local, remote) {
		t.Fatalf("forward %s %s not found in %+v", local, remote, forwards)
	}

	if err := target.RemoveForward(ctx, local, remote); err != nil {
		t.Fatalf("RemoveForward: %v", err)
	}
	forwards, err = target.ListForwards(ctx)
	if err != nil {
		t.Fatalf("ListForwards: %v", err)
	}
	if hasPort(forwards, local, remote) {
		t.Fatalf("forward %s %s still present after removal", local, remote)
	}
}

func TestE2E_ReverseLifecycle(t *testing.T) {
	_, target := requireTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	remote := "tcp:" + strconv.Itoa(freePort(t))
	local := "tcp:" + strconv.Itoa(freePort(t))

	if err := target.Reverse(ctx, remote, local); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	defer target.RemoveReverse(ctx, remote, local)

	reverses, err := target.ListReverses(ctx)
	if err != nil {
		t.Fatalf("ListReverses: %v", err)
	}
	if !hasPort(reverses, local, remote) {
		t.Fatalf("reverse %s %s not found in %+v", remote, local, reverses)
	}

	if err := target.RemoveReverse(ctx, remote, local); err != nil {
		t.Fatalf("RemoveReverse: %v", err)
	}
}

func TestE2E_FileRoundTrip(t *testing.T) {
	_, target := requireTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	content := randomBytes(t, 64*1024)
	local, err := os.CreateTemp(t.TempDir(), "hdckit-e2e-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Write(content); err != nil {
		t.Fatal(err)
	}
	local.Close()

	remote := fmt.Sprintf("/data/local/tmp/hdckit-e2e-%d.bin", os.Getpid())
	defer target.RunShell(ctx, "rm -f "+remote)

	if err := target.SendFile(ctx, local.Name(), remote); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	back := local.Name() + ".back"
	if err := target.RecvFile(ctx, remote, back); err != nil {
		t.Fatalf("RecvFile: %v", err)
	}
	got, err := os.ReadFile(back)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

func TestE2E_TrackerSmoke(t *testing.T) {
	client := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	tracker, err := client.TrackTargets(ctx)
	if err != nil {
		t.Fatalf("TrackTargets: %v", err)
	}
	select {
	case target := <-tracker.Added():
		t.Logf("target added: %s", target)
	case err := <-tracker.Errors():
		t.Fatalf("tracker error: %v", err)
	case <-time.After(3 * time.Second):
		t.Log("no target event within 3s (fine for a stable device)")
	}
	tracker.Close()
}

func requireUIOpt(t *testing.T) {
	t.Helper()
	if os.Getenv("HDCKIT_E2E_UI") == "" {
		t.Skip("set HDCKIT_E2E_UI=1 to exercise UiDriver")
	}
}

func TestE2E_UiDriverDisplaySize(t *testing.T) {
	requireUIOpt(t)
	_, target := requireTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	drv := target.CreateUiDriver()
	defer drv.Stop(ctx)

	size, err := drv.DisplaySize(ctx)
	if err != nil {
		t.Fatalf("DisplaySize: %v", err)
	}
	if size.Width <= 0 || size.Height <= 0 {
		t.Fatalf("unexpected display size %dx%d", size.Width, size.Height)
	}
	t.Logf("display size: %dx%d", size.Width, size.Height)
}

func TestE2E_UiDriverLayoutAndControl(t *testing.T) {
	requireUIOpt(t)
	_, target := requireTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	drv := target.CreateUiDriver()
	defer drv.Stop(ctx)

	size, err := drv.DisplaySize(ctx)
	if err != nil {
		t.Fatalf("DisplaySize: %v", err)
	}
	cx, cy := size.Width/2, size.Height/2

	layout, err := drv.CaptureLayout(ctx)
	if err != nil {
		t.Fatalf("CaptureLayout: %v", err)
	}
	if len(layout) == 0 {
		t.Fatal("CaptureLayout returned an empty layout")
	}
	t.Logf("layout: %d bytes", len(layout))

	// Gestures: a tap and a short drag in the middle of the screen.
	if err := drv.Tap(ctx, cx, cy); err != nil {
		t.Errorf("Tap: %v", err)
	}
	if err := drv.TouchDown(ctx, cx, cy); err != nil {
		t.Errorf("TouchDown: %v", err)
	}
	if err := drv.TouchMove(ctx, cx, cy-100); err != nil {
		t.Errorf("TouchMove: %v", err)
	}
	if err := drv.TouchUp(ctx, cx, cy-100); err != nil {
		t.Errorf("TouchUp: %v", err)
	}
	if err := drv.InputText(ctx, "hdckit", cx, cy-100); err != nil {
		t.Errorf("InputText: %v", err)
	}
}

func TestE2E_UiDriverCaptureScreen(t *testing.T) {
	requireUIOpt(t)
	_, target := requireTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	drv := target.CreateUiDriver()
	defer drv.Stop(ctx)

	stream, err := drv.CaptureScreen(ctx, 0.5)
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}
	defer stream.Close(ctx)

	select {
	case frame, ok := <-stream.Frames():
		if !ok {
			t.Fatal("capture stream closed before a frame arrived")
		}
		if ext := imageFormat(frame); ext == "" {
			t.Fatalf("frame is not PNG/JPEG (%d bytes, prefix % x)", len(frame), frame[:min(4, len(frame))])
		} else {
			t.Logf("received %s frame (%d bytes)", ext, len(frame))
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for a frame")
	}
}

func TestE2E_UiDriverScreenshot(t *testing.T) {
	requireUIOpt(t)
	_, target := requireTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	drv := target.CreateUiDriver()
	defer drv.Stop(ctx)

	frame, err := drv.Screenshot(ctx, 0)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if imageFormat(frame) == "" {
		t.Fatalf("screenshot is not PNG/JPEG (%d bytes)", len(frame))
	}
	t.Logf("screenshot: %d bytes", len(frame))
}

func TestE2E_HilogSmoke(t *testing.T) {
	if os.Getenv("HDCKIT_E2E_HILOG") == "" {
		t.Skip("set HDCKIT_E2E_HILOG=1 to exercise the hilog stream")
	}
	_, target := requireTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	hilog, err := target.OpenHilog(ctx, false)
	if err != nil {
		t.Fatalf("OpenHilog: %v", err)
	}
	defer hilog.Close()

	select {
	case entry, ok := <-hilog.Entries():
		if !ok {
			t.Fatal("hilog stream closed unexpectedly")
		}
		t.Logf("entry: [%s] %s/%s: %s", entry.Level, entry.Type, entry.Tag, entry.Message)
	case err := <-hilog.Errors():
		t.Fatalf("hilog error: %v", err)
	case <-time.After(15 * time.Second):
		t.Skip("no log entry within 15s")
	}
}

func imageFormat(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return "jpg"
	default:
		return ""
	}
}

func hasPort(forwards []hdc.Forward, local, remote string) bool {
	for _, f := range forwards {
		if f.Local == local && f.Remote == remote {
			return true
		}
	}
	return false
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return buf
}
