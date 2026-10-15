package hdc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uiautodev/go-hdckit/uitestkit_sdk"
)

const (
	// agentPath is where the uitest agent library is pushed on the device.
	agentPath = "/data/local/tmp/agent.so"
	// uiTestForwardPort is the device port used by the uitest daemon.
	uiTestForwardPort = 8012
	// uiCreateTimeout bounds the initial Driver.create call.
	uiCreateTimeout = 3 * time.Second
	// uiCallTimeout bounds a single RPC call.
	uiCallTimeout = 10 * time.Second
	// uiDaemonStartDelay is how long to wait after starting the uitest daemon.
	uiDaemonStartDelay = 2 * time.Second
	// uiFrameBuffer is the number of screen frames buffered before dropping.
	uiFrameBuffer = 8
)

// errTimeout is returned when an RPC call exceeds its timeout.
var errTimeout = errors.New("hdc: timeout")

// Size is the logical size of the device screen, in pixels.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// UiDriver drives the device UI through the uitest agent (hypium API).
//
// All methods accept a context.Context and lazily start the agent, push the
// embedded SDK and set up the port forward on first use. Call Stop to release
// the device-side daemon.
type UiDriver struct {
	target     *Target
	sdkPath    string
	sdkVersion string

	mu            sync.Mutex
	conn          *uiRPCConn
	driverName    string
	port          int
	triedStarting bool

	streamMu sync.Mutex
	stream   *ScreenStream
}

// CreateUiDriver returns a UiDriver for this target using the embedded SDK.
func (t *Target) CreateUiDriver() *UiDriver {
	return &UiDriver{target: t, sdkVersion: uitestkit_sdk.Version}
}

// SetSDK overrides the agent library path and/or version. Empty values are
// ignored.
func (d *UiDriver) SetSDK(path, version string) {
	if path != "" {
		d.sdkPath = path
	}
	if version != "" {
		d.sdkVersion = version
	}
}

// Start prepares the device (test mode, agent, daemon, port forward) and
// establishes the driver connection. It is safe to call multiple times.
func (d *UiDriver) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil {
		return nil
	}
	if err := d.startLocked(ctx); err != nil {
		return err
	}
	conn, err := d.connectLocked(ctx)
	if err != nil {
		return err
	}
	d.conn = conn
	return nil
}

// Stop tears down the driver connection and kills the uitest daemon.
func (d *UiDriver) Stop(ctx context.Context) error {
	d.mu.Lock()
	if d.conn != nil {
		d.conn.Close()
		d.conn = nil
	}
	d.mu.Unlock()
	return d.killUitest(ctx)
}

// DisplaySize returns the device screen size.
func (d *UiDriver) DisplaySize(ctx context.Context) (Size, error) {
	res, err := d.call(
		ctx,
		"CtrlCmd",
		"getDisplaySize",
		map[string]any{},
	)
	if err != nil {
		return Size{}, err
	}
	var size Size
	if err := json.Unmarshal(res, &size); err != nil {
		return Size{}, fmt.Errorf("hdc: decode display size: %w", err)
	}
	return size, nil
}

// CaptureLayout returns the current UI layout tree as raw JSON.
func (d *UiDriver) CaptureLayout(ctx context.Context) (json.RawMessage, error) {
	return d.call(
		ctx,
		"Captures",
		"captureLayout",
		map[string]any{},
	)
}

// InputText inputs text at the given coordinates.
func (d *UiDriver) InputText(ctx context.Context, text string, x, y int) error {
	_, err := d.callHypium(ctx, "inputText", []any{map[string]int{"x": x, "y": y}, text})
	return err
}

// TouchDown sends a touch-down gesture at (x, y).
func (d *UiDriver) TouchDown(ctx context.Context, x, y int) error {
	return d.touch(
		ctx,
		"touchDown",
		x,
		y,
	)
}

// TouchMove sends a touch-move gesture to (x, y).
func (d *UiDriver) TouchMove(ctx context.Context, x, y int) error {
	return d.touch(
		ctx,
		"touchMove",
		x,
		y,
	)
}

// TouchUp sends a touch-up gesture at (x, y).
func (d *UiDriver) TouchUp(ctx context.Context, x, y int) error {
	return d.touch(
		ctx,
		"touchUp",
		x,
		y,
	)
}

// Tap performs a quick tap at (x, y).
func (d *UiDriver) Tap(ctx context.Context, x, y int) error {
	if err := d.TouchDown(ctx, x, y); err != nil {
		return err
	}
	return d.TouchUp(ctx, x, y)
}

func (d *UiDriver) touch(ctx context.Context, api string, x, y int) error {
	_, err := d.call(
		ctx,
		"Gestures",
		api,
		map[string]int{"x": x, "y": y},
	)
	return err
}

// CaptureScreen starts streaming screen frames. The optional scale (0 < scale
// < 1) downsizes the frames. The caller must Close the returned stream.
func (d *UiDriver) CaptureScreen(ctx context.Context, scale float64) (*ScreenStream, error) {
	d.streamMu.Lock()
	active := d.stream != nil
	d.streamMu.Unlock()
	if active {
		return nil, errors.New("hdc: screen capture is already running")
	}

	conn, err := d.ensure(ctx)
	if err != nil {
		return nil, err
	}

	opts := map[string]any{}
	if scale > 0 && scale < 1 {
		opts["scale"] = scale
	}
	payload := uiAPIPayload("Captures", "startCaptureScreen", map[string]any{"options": opts})
	session, _, err := conn.send(ctx, payload, uiCallTimeout)
	if err != nil {
		return nil, err
	}

	stream := &ScreenStream{
		driver:  d,
		session: session,
		frames:  make(chan []byte, uiFrameBuffer),
	}

	d.streamMu.Lock()
	d.stream = stream
	d.streamMu.Unlock()
	return stream, nil
}

// Screenshot captures a single frame, downsized by scale when 0 < scale < 1.
func (d *UiDriver) Screenshot(ctx context.Context, scale float64) ([]byte, error) {
	stream, err := d.CaptureScreen(ctx, scale)
	if err != nil {
		return nil, err
	}
	defer stream.Close(ctx)

	select {
	case frame, ok := <-stream.Frames():
		if !ok {
			return nil, errors.New("hdc: screen capture closed before a frame arrived")
		}
		return frame, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *UiDriver) handleMessage(session uint32, payload []byte) {
	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	if stream := d.stream; stream != nil && session == stream.session {
		select {
		case stream.frames <- payload:
		default: // drop frames the consumer is too slow to read
		}
	}
}

func (d *UiDriver) stopCapture(ctx context.Context) error {
	_, err := d.call(
		ctx,
		"Captures",
		"stopCaptureScreen",
		map[string]any{},
	)

	d.streamMu.Lock()
	if d.stream != nil {
		d.stream.closeFrames()
		d.stream = nil
	}
	d.streamMu.Unlock()
	return err
}

// onConnClosed is invoked when the RPC connection drops, closing any active
// screen stream so consumers ranging over Frames are not blocked forever.
func (d *UiDriver) onConnClosed() {
	d.streamMu.Lock()
	if d.stream != nil {
		d.stream.closeFrames()
		d.stream = nil
	}
	d.streamMu.Unlock()

	// A closing mutex (e.g. Stop) already clears d.conn; TryLock avoids a
	// deadlock in that case.
	if d.mu.TryLock() {
		d.conn = nil
		d.mu.Unlock()
	}
}

func (d *UiDriver) ensure(ctx context.Context) (*uiRPCConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil {
		return d.conn, nil
	}
	if err := d.startLocked(ctx); err != nil {
		return nil, err
	}
	conn, err := d.connectLocked(ctx)
	if err == nil {
		d.conn = conn
		return conn, nil
	}
	// The daemon may have been started long ago with a stale agent; restart it
	// and retry once before giving up.
	if d.triedStarting {
		return nil, err
	}
	d.triedStarting = true
	if killErr := d.killUitest(ctx); killErr != nil {
		return nil, err
	}
	if startErr := d.startLocked(ctx); startErr != nil {
		return nil, startErr
	}
	conn, err = d.connectLocked(ctx)
	if err != nil {
		return nil, err
	}
	d.conn = conn
	return conn, nil
}

// startLocked prepares the device. The caller must hold d.mu.
func (d *UiDriver) startLocked(ctx context.Context) error {
	pid, err := d.shell(ctx, "pidof uitest")
	if err != nil {
		return err
	}
	pid = strings.TrimSpace(pid)

	shouldUpdate, err := d.shouldUpdateSDK(ctx)
	if err != nil {
		return err
	}

	if pid == "" || shouldUpdate {
		if _, err := d.shell(ctx, "param set persist.ace.testmode.enabled 1"); err != nil {
			return err
		}
		if shouldUpdate {
			if pid != "" {
				if _, err := d.shell(ctx, "kill -9 "+pid); err != nil {
					return err
				}
				pid = ""
			}
			if err := d.updateSDK(ctx); err != nil {
				return err
			}
		}
		if pid == "" {
			if _, err := d.shell(ctx, "uitest start-daemon singleness"); err != nil {
				return err
			}
			time.Sleep(uiDaemonStartDelay)
		}
	}

	port, err := d.forwardTCP(ctx, uiTestForwardPort)
	if err != nil {
		return err
	}
	d.port = port
	return nil
}

// connectLocked opens the RPC connection and creates the driver. The caller
// must hold d.mu.
func (d *UiDriver) connectLocked(ctx context.Context) (*uiRPCConn, error) {
	conn := &uiRPCConn{}
	if err := conn.connect(ctx, d.port); err != nil {
		return nil, err
	}
	conn.setOnMessage(d.handleMessage)
	conn.setOnClose(d.onConnClosed)

	_, res, err := conn.send(ctx, d.hypiumPayload("create", nil, []any{}), uiCreateTimeout)
	if err != nil {
		conn.Close()
		return nil, err
	}
	var name string
	if err := json.Unmarshal(res, &name); err != nil {
		conn.Close()
		return nil, fmt.Errorf("hdc: invalid Driver.create response: %w", err)
	}
	d.driverName = name
	return conn, nil
}

func (d *UiDriver) call(ctx context.Context, method, api string, args any) (json.RawMessage, error) {
	return d.do(ctx, uiAPIPayload(method, api, args))
}

func (d *UiDriver) callHypium(ctx context.Context, api string, args any) (json.RawMessage, error) {
	return d.do(ctx, d.hypiumPayload(api, d.driverName, args))
}

func (d *UiDriver) do(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	conn, err := d.ensure(ctx)
	if err != nil {
		return nil, err
	}
	_, res, err := conn.send(ctx, payload, uiCallTimeout)
	return res, err
}

func (d *UiDriver) killUitest(ctx context.Context) error {
	pid, err := d.shell(ctx, "pidof uitest")
	if err != nil {
		return err
	}
	if pid = strings.TrimSpace(pid); pid != "" {
		_, err = d.shell(ctx, "kill -9 "+pid)
	}
	return err
}

func (d *UiDriver) forwardTCP(ctx context.Context, p int) (int, error) {
	remote := "tcp:" + strconv.Itoa(p)
	forwards, err := d.target.ListForwards(ctx)
	if err != nil {
		return 0, err
	}
	for _, f := range forwards {
		if f.Remote == remote {
			if port, err := parseTCPPort(f.Local); err == nil {
				return port, nil
			}
		}
	}

	localPort, err := freePort()
	if err != nil {
		return 0, err
	}
	if err := d.target.Forward(ctx, "tcp:"+strconv.Itoa(localPort), remote); err != nil {
		return 0, err
	}
	return localPort, nil
}

func (d *UiDriver) shouldUpdateSDK(ctx context.Context) (bool, error) {
	result, err := d.shell(ctx, "cat "+agentPath+" | grep -a UITEST_AGENT_LIBRARY")
	if err != nil {
		return true, nil
	}
	if !strings.Contains(result, "UITEST_AGENT_LIBRARY") {
		return true, nil
	}
	return cmpVersion(sdkVersionOf(result), d.sdkVersion) < 0, nil
}

func (d *UiDriver) updateSDK(ctx context.Context) error {
	if _, err := d.shell(ctx, "rm "+agentPath); err != nil {
		return err
	}
	path, err := d.resolveSDKPath()
	if err != nil {
		return err
	}
	return d.target.SendFile(ctx, path, agentPath)
}

func (d *UiDriver) resolveSDKPath() (string, error) {
	if d.sdkPath != "" {
		return d.sdkPath, nil
	}
	return embeddedSDK()
}

func (d *UiDriver) shell(ctx context.Context, command string) (string, error) {
	out, err := d.target.RunShell(ctx, command)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (d *UiDriver) hypiumPayload(api string, this any, args any) map[string]any {
	return map[string]any{
		"module": "com.ohos.devicetest.hypiumApiHelper",
		"method": "callHypiumApi",
		"params": map[string]any{
			"api":          "Driver." + api,
			"this":         this,
			"args":         args,
			"message_type": "hypium",
		},
	}
}

func uiAPIPayload(method, api string, args any) map[string]any {
	return map[string]any{
		"module": "com.ohos.devicetest.hypiumApiHelper",
		"method": method,
		"params": map[string]any{
			"api":  api,
			"args": args,
		},
	}
}
