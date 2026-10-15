// Command go-hdckit is a command line client for OpenHarmony devices, built on
// top of github.com/uiautodev/go-hdckit/hdc.
//
// Usage:
//
//	go-hdckit [global flags] <command> [command flags] [args...]
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/uiautodev/go-hdckit/hdc"
)

var (
	host = flag.String("host", "", "hdc server host (default 127.0.0.1)")
	port = flag.Int("port", 0, "hdc server port (default $OHOS_HDC_SERVER_PORT or 8710)")
	bin  = flag.String("bin", "", "path to the hdc binary (default \"hdc\")")

	pngMagic  = []byte("\x89PNG\r\n\x1a\n")
	jpegMagic = []byte{0xff, 0xd8, 0xff}
)

func main() {
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "list":
		err = runList(ctx)
	case "track":
		err = runTrack(ctx)
	case "shell":
		err = runShell(ctx, rest)
	case "forward":
		err = runForward(ctx, rest)
	case "reverse":
		err = runReverse(ctx, rest)
	case "file":
		err = runFile(ctx, rest)
	case "install":
		err = runInstall(ctx, rest)
	case "uninstall":
		err = runUninstall(ctx, rest)
	case "hilog":
		err = runHilog(ctx, rest)
	case "ui":
		err = runUI(ctx, rest)
	case "kill":
		err = runKill()
	case "help", "-h", "--help":
		usage()
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "go-hdckit:", err)
		os.Exit(1)
	}
}

func client() *hdc.Client {
	return hdc.NewClient(hdc.Options{Host: *host, Port: *port, Bin: *bin})
}

func usage() {
	fmt.Fprint(os.Stderr, `go-hdckit - OpenHarmony device client

Usage:
  go-hdckit [global flags] <command> [flags] [args...]

Commands:
  list                       list connected targets
  track                      watch targets connecting/disconnecting
  shell [command...]         run a shell command (-t to pick a target)
  forward add|list|remove    manage TCP forwards
  reverse add|list|remove    manage TCP reverses
  file send|recv             send or receive a file
  install <hap>              install a .hap package
  uninstall <bundle>         uninstall an app
  hilog                      stream device logs
  ui size|screenshot|capture|input|tap   control the device UI
  kill                       kill the hdc server

Global flags:
`)
	flag.PrintDefaults()
}

func targetFlag(fs *flag.FlagSet) *string {
	return fs.String("t", "", "target connect key (defaults to the only connected device)")
}

func resolveTarget(ctx context.Context, key string) (*hdc.Target, error) {
	if key != "" {
		return client().Target(key)
	}
	targets, err := client().ListTargets(ctx)
	if err != nil {
		return nil, err
	}
	if len(targets) != 1 {
		return nil, fmt.Errorf("expected exactly one target, found %d; use -t to select one", len(targets))
	}
	return client().Target(targets[0])
}

func runList(ctx context.Context) error {
	targets, err := client().ListTargets(ctx)
	if err != nil {
		return err
	}
	for _, t := range targets {
		fmt.Println(t)
	}
	return nil
}

func runTrack(ctx context.Context) error {
	tracker, err := client().TrackTargets(ctx)
	if err != nil {
		return err
	}
	defer tracker.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case target := <-tracker.Added():
			fmt.Println("add:", target)
		case target := <-tracker.Removed():
			fmt.Println("remove:", target)
		case err := <-tracker.Errors():
			return err
		}
	}
}

func runShell(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("shell", flag.ExitOnError)
	key := targetFlag(fs)
	fs.Parse(args)

	if fs.NArg() == 0 {
		return errors.New("shell requires a command")
	}
	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	command := strings.Join(fs.Args(), " ")
	conn, err := target.Shell(ctx, command)
	if err != nil {
		return err
	}
	defer conn.Close()

	out, err := conn.ReadAll(ctx)
	if err != nil {
		return err
	}
	os.Stdout.Write(out)
	return nil
}

func runForward(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: forward add|list|remove ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return runPortAdd(ctx, "forward", rest)
	case "remove":
		return runPortRemove(ctx, "forward", rest)
	case "list":
		fs := flag.NewFlagSet("forward list", flag.ExitOnError)
		key := targetFlag(fs)
		fs.Parse(rest)
		if *key != "" {
			target, err := client().Target(*key)
			if err != nil {
				return err
			}
			forwards, err := target.ListForwards(ctx)
			if err != nil {
				return err
			}
			printForwards(forwards)
			return nil
		}
		forwards, err := client().ListForwards(ctx)
		if err != nil {
			return err
		}
		printForwards(forwards)
		return nil
	default:
		return fmt.Errorf("unknown forward subcommand %q", sub)
	}
}

func runReverse(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: reverse add|list|remove ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return runPortAdd(ctx, "reverse", rest)
	case "remove":
		return runPortRemove(ctx, "reverse", rest)
	case "list":
		fs := flag.NewFlagSet("reverse list", flag.ExitOnError)
		key := targetFlag(fs)
		fs.Parse(rest)
		if *key != "" {
			target, err := client().Target(*key)
			if err != nil {
				return err
			}
			reverses, err := target.ListReverses(ctx)
			if err != nil {
				return err
			}
			printForwards(reverses)
			return nil
		}
		reverses, err := client().ListReverses(ctx)
		if err != nil {
			return err
		}
		printForwards(reverses)
		return nil
	default:
		return fmt.Errorf("unknown reverse subcommand %q", sub)
	}
}

func runPortAdd(ctx context.Context, kind string, args []string) error {
	fs := flag.NewFlagSet(kind+" add", flag.ExitOnError)
	key := targetFlag(fs)
	fs.Parse(args)

	if fs.NArg() != 2 {
		return fmt.Errorf("usage: %s add [-t target] <a> <b>", kind)
	}
	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	if kind == "forward" {
		err = target.Forward(ctx, fs.Arg(0), fs.Arg(1))
	} else {
		err = target.Reverse(ctx, fs.Arg(0), fs.Arg(1))
	}
	if err != nil {
		return err
	}
	fmt.Println("OK")
	return nil
}

func runPortRemove(ctx context.Context, kind string, args []string) error {
	fs := flag.NewFlagSet(kind+" remove", flag.ExitOnError)
	key := targetFlag(fs)
	fs.Parse(args)

	if fs.NArg() != 2 {
		return fmt.Errorf("usage: %s remove [-t target] <a> <b>", kind)
	}
	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	if kind == "forward" {
		err = target.RemoveForward(ctx, fs.Arg(0), fs.Arg(1))
	} else {
		err = target.RemoveReverse(ctx, fs.Arg(0), fs.Arg(1))
	}
	if err != nil {
		return err
	}
	fmt.Println("OK")
	return nil
}

func printForwards(forwards []hdc.Forward) {
	for _, f := range forwards {
		fmt.Printf("%s %s %s\n", f.Target, f.Local, f.Remote)
	}
}

func runFile(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: file send|recv ...")
	}
	sub, rest := args[0], args[1:]

	fs := flag.NewFlagSet("file "+sub, flag.ExitOnError)
	key := targetFlag(fs)
	fs.Parse(rest)
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: file %s [-t target] <a> <b>", sub)
	}
	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}

	switch sub {
	case "send":
		return target.SendFile(ctx, fs.Arg(0), fs.Arg(1))
	case "recv":
		return target.RecvFile(ctx, fs.Arg(0), fs.Arg(1))
	default:
		return fmt.Errorf("unknown file subcommand %q", sub)
	}
}

func runInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	key := targetFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: install [-t target] <hap>")
	}
	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	return target.Install(ctx, fs.Arg(0))
}

func runUninstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	key := targetFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: uninstall [-t target] <bundle>")
	}
	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	return target.Uninstall(ctx, fs.Arg(0))
}

func runHilog(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("hilog", flag.ExitOnError)
	key := targetFlag(fs)
	clear := fs.Bool("clear", false, "clear the log buffer first")
	fs.Parse(args)

	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	hilog, err := target.OpenHilog(ctx, *clear)
	if err != nil {
		return err
	}
	defer hilog.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case entry, ok := <-hilog.Entries():
			if !ok {
				return nil
			}
			fmt.Printf("%s %s %s/%s(%d): %s\n",
				entry.Date.Format("2006-01-02 15:04:05.000"),
				entry.Level, entry.Type, entry.Tag, entry.PID, entry.Message)
		case err := <-hilog.Errors():
			if err != nil {
				return err
			}
		}
	}
}

func runUI(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ui size|screenshot|capture|input|tap ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "size":
		return runUISize(ctx, rest)
	case "screenshot":
		return runUIScreenshot(ctx, rest)
	case "capture":
		return runUICapture(ctx, rest)
	case "input":
		return runUIInput(ctx, rest)
	case "tap":
		return runUITap(ctx, rest)
	default:
		return fmt.Errorf("unknown ui subcommand %q", sub)
	}
}

func runUISize(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ui size", flag.ExitOnError)
	key := targetFlag(fs)
	fs.Parse(args)

	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	drv := target.CreateUiDriver()
	defer drv.Stop(ctx)

	size, err := drv.DisplaySize(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%dx%d\n", size.Width, size.Height)
	return nil
}

func runUIScreenshot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ui screenshot", flag.ExitOnError)
	key := targetFlag(fs)
	out := fs.String("out", "screenshot", "output file (extension inferred when omitted)")
	scale := fs.Float64("scale", 0, "frame scale between 0 and 1")
	fs.Parse(args)

	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	drv := target.CreateUiDriver()
	defer drv.Stop(ctx)

	frame, err := drv.Screenshot(ctx, *scale)
	if err != nil {
		return err
	}
	name := *out
	if filepath.Ext(name) == "" {
		name += "." + imageExt(frame)
	}
	if err := os.WriteFile(name, frame, 0o644); err != nil {
		return err
	}
	fmt.Printf("saved %s (%d bytes)\n", name, len(frame))
	return nil
}

func runUICapture(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ui capture", flag.ExitOnError)
	key := targetFlag(fs)
	out := fs.String("out", "frames", "output directory")
	count := fs.Int("count", 10, "number of frames to save")
	timeout := fs.Duration("timeout", 30*time.Second, "maximum time to wait")
	scale := fs.Float64("scale", 0, "frame scale between 0 and 1")
	fs.Parse(args)

	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}

	drv := target.CreateUiDriver()
	defer drv.Stop(ctx)

	stream, err := drv.CaptureScreen(ctx, *scale)
	if err != nil {
		return err
	}
	defer stream.Close(context.Background())

	var saved atomic.Int64
	deadline := time.After(*timeout)
	for int(saved.Load()) < *count {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return nil
		case frame, ok := <-stream.Frames():
			if !ok {
				return nil
			}
			n := saved.Add(1)
			name := fmt.Sprintf("frame_%03d.%s", n, imageExt(frame))
			if err := os.WriteFile(filepath.Join(*out, name), frame, 0o644); err != nil {
				fmt.Fprintln(os.Stderr, "save frame:", err)
			}
		}
	}
	return nil
}

func runUIInput(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ui input", flag.ExitOnError)
	key := targetFlag(fs)
	x := fs.Int("x", 0, "x coordinate")
	y := fs.Int("y", 0, "y coordinate")
	fs.Parse(args)

	if fs.NArg() != 1 {
		return errors.New("usage: ui input [-t target] [-x x] [-y y] <text>")
	}
	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	drv := target.CreateUiDriver()
	defer drv.Stop(ctx)
	return drv.InputText(ctx, fs.Arg(0), *x, *y)
}

func runUITap(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ui tap", flag.ExitOnError)
	key := targetFlag(fs)
	x := fs.Int("x", 0, "x coordinate")
	y := fs.Int("y", 0, "y coordinate")
	fs.Parse(args)

	if fs.NArg() != 0 {
		return errors.New("usage: ui tap [-t target] -x <x> -y <y>")
	}
	target, err := resolveTarget(ctx, *key)
	if err != nil {
		return err
	}
	drv := target.CreateUiDriver()
	defer drv.Stop(ctx)
	return drv.Tap(ctx, *x, *y)
}

func runKill() error {
	return client().Kill()
}

func imageExt(frame []byte) string {
	switch {
	case bytes.HasPrefix(frame, pngMagic):
		return "png"
	case bytes.HasPrefix(frame, jpegMagic):
		return "jpg"
	default:
		return "bin"
	}
}
