# go-hdckit

OpenHarmony HDC 的纯 Go 客户端与命令行工具。

> **本项目是 [liriliri/hdckit](https://github.com/liriliri/hdckit) 的 Go 移植版**（original project: <https://github.com/liriliri/hdckit>）。
> 原项目为 Node.js/TypeScript 实现，这里用 Go 重新实现了相同的 hdc 协议与 uitest agent RPC，
> 并按 Go 的习惯重新设计了对外接口。

它直接通过 TCP 实现 hdc 协议，只有在启动服务端，以及使用 `file send/recv`、`install/uninstall` 这几个协议未暴露的命令时，才需要 `hdc` 可执行文件。

## 安装

作为库使用：

```bash
go get github.com/uiautodev/go-hdckit/hdc
```

命令行工具：

```bash
go install github.com/uiautodev/go-hdckit/cmd/go-hdckit@latest
```

## 库用法

```go
package main

import (
	"context"
	"fmt"

	"github.com/uiautodev/go-hdckit/hdc"
)

func main() {
	ctx := context.Background()
	client := hdc.NewClient(hdc.Options{})

	targets, err := client.ListTargets(ctx)
	if err != nil {
		panic(err)
	}
	if len(targets) == 0 {
		panic("no target found")
	}

	target, err := client.Target(targets[0])
	if err != nil {
		panic(err)
	}

	params, err := target.Parameters(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(params["const.product.name"])
}
```

所有网络与命令调用都接受 `context.Context`，可用于超时和取消。

### 常用能力

```go
// shell
out, _ := target.RunShell(ctx, "echo hello")

// 流式 shell（长命令）
conn, _ := target.Shell(ctx, "some long command")
defer conn.Close()
data, _ := conn.ReadAll(ctx)

// 文件与安装
target.SendFile(ctx, "./a.txt", "/data/local/tmp/a.txt")
target.RecvFile(ctx, "/data/local/tmp/a.txt", "./a.txt")
target.Install(ctx, "./app.hap")
target.Uninstall(ctx, "com.example.app")

// 端口转发
target.Forward(ctx, "tcp:9000", "tcp:8000")
target.ListForwards(ctx)
target.RemoveForward(ctx, "tcp:9000", "tcp:8000")

// 设备参数
target.Parameters(ctx)
```

### 设备监听

```go
tracker, _ := client.TrackTargets(ctx)
defer tracker.Close()
for {
	select {
	case t := <-tracker.Added():
		fmt.Println("add", t)
	case t := <-tracker.Removed():
		fmt.Println("remove", t)
	case err := <-tracker.Errors():
		return
	}
}
```

### hilog

```go
hilog, _ := target.OpenHilog(ctx, true) // true 表示先清空
defer hilog.Close()
for entry := range hilog.Entries() {
	fmt.Printf("%s %s/%s: %s\n", entry.Date, entry.Level, entry.Tag, entry.Message)
}
```

### UiDriver

`UiDriver` 通过内嵌的 uitest agent（`uitestkit_sdk`）驱动设备 UI，无需外部 `.so` 文件。
接口按 Go 习惯设计：方法名不加 `Get` 前缀，屏幕流用 channel 而不是回调。

```go
drv := target.CreateUiDriver()
defer drv.Stop(ctx)

// 屏幕信息与布局
size, _ := drv.DisplaySize(ctx)
fmt.Println(size.Width, size.Height)
layout, _ := drv.CaptureLayout(ctx) // json.RawMessage

// 控制
_ = drv.Tap(ctx, 540, 1200)
_ = drv.TouchDown(ctx, 540, 1200)
_ = drv.TouchMove(ctx, 540, 900)
_ = drv.TouchUp(ctx, 540, 900)
_ = drv.InputText(ctx, "hello", 540, 900)

// 单帧截图（返回 PNG/JPEG 原始字节）
png, _ := drv.Screenshot(ctx, 0.5)

// 投屏：以 channel 读取帧，消费者过慢时自动丢帧
stream, _ := drv.CaptureScreen(ctx, 0.5)
defer stream.Close(ctx)
for frame := range stream.Frames() {
	// frame 为 PNG/JPEG 原始字节
}
```

如需覆盖 agent，可调用 `drv.SetSDK(path, version)`。

## 命令行

```
go-hdckit [全局参数] <命令> [命令参数] [参数...]
```

全局参数：`-host`、`-port`、`-bin`。仅连接一台设备时可省略 `-t`。

```bash
go-hdckit list
go-hdckit track
go-hdckit shell "echo hello"
go-hdckit -t <target> shell "echo hello"

go-hdckit forward add tcp:9000 tcp:8000
go-hdckit forward list
go-hdckit forward remove tcp:9000 tcp:8000

go-hdckit file send ./a.txt /data/local/tmp/a.txt
go-hdckit file recv /data/local/tmp/a.txt ./a.txt

go-hdckit install ./app.hap
go-hdckit uninstall com.example.app

go-hdckit hilog --clear
go-hdckit ui size
go-hdckit ui screenshot -out shot.jpg -scale 0.5
go-hdckit ui capture -out frames -count 20 -timeout 60s
go-hdckit ui tap -x 540 -y 1200
go-hdckit ui input "hello" -x 540 -y 900

go-hdckit kill
```

## 测试

常用命令已固化到 `Makefile`（可用 `GO=... GOFMT=...` 覆盖工具路径）：

```bash
make test        # 单元测试（不需要设备/hdc）
make race        # 单元测试 + 竞态检测
make vet
make fmt-check   # 校验格式
make e2e         # 端到端（需要 hdc/设备）
```

等价的直接命令：

```bash
go test ./...
```

端到端测试（需要本机有 `hdc`，连接设备后效果更完整）。它们用 `e2e` build tag 隔离，
未连接设备时依赖设备的用例会自动跳过：

```bash
go test -tags e2e -v ./hdc/
```

可选的环境变量：

| 变量 | 说明 |
| --- | --- |
| `HDCKIT_E2E_BIN` | hdc 可执行文件路径，默认 `$PATH` 中的 `hdc` |
| `HDCKIT_E2E_PORT` | hdc 服务端端口 |
| `HDCKIT_E2E_UI=1` | 额外跑 UiDriver（会启动 uitest 守护进程） |
| `HDCKIT_E2E_HILOG=1` | 额外跑 hilog 流 |

覆盖范围：`list targets`、`fport ls`（forward/reverse）、shell、参数读取、
端口转发增删、文件收发往返、设备监听，以及可选的 UiDriver（屏幕尺寸、布局、
tap/触摸/输入、投屏帧、单帧截图）与 hilog。

## 设计说明

- 包 `hdc` 面向库使用者，`cmd/go-hdckit` 为 CLI。
- 事件回调统一改用 Go channel（`Added`/`Removed`/`Errors`、`Entries`/`Errors`、`Frames`/`Errors`）。
- `UiDriver` 方法不带 `Get` 前缀，屏幕流用 `ScreenStream` + channel，并返回 `Size`、`json.RawMessage` 等显式类型，避免 `any` 漫天飞。
- 协议细节：uitest agent 依据 session id 高位字节决定是否对响应加帧，客户端需生成高字节非 0 的 session id（见 `hdc/uidriver_rpc.go`）。
- 默认端口顺序：`Options.Port` > `OHOS_HDC_SERVER_PORT` > `8710`。

## 目录结构

```
go-hdckit/
├── hdc/                 # 库实现
│   ├── client.go        # Client 与连接管理
│   ├── connection.go    # hdc 协议连接与握手
│   ├── target.go        # 设备命令
│   ├── tracker.go       # 设备监听
│   ├── hilog.go         # 日志解析
│   ├── uidriver.go      # UiDriver
│   ├── uidriver_stream.go # 屏幕帧流 ScreenStream
│   ├── uidriver_sdk.go  # 内嵌 agent 提取与版本比较
│   └── uidriver_rpc.go  # uitest agent RPC 协议
├── cmd/go-hdckit/       # 命令行入口
├── uitestkit_sdk/       # 内嵌的 uitest agent
└── Makefile             # build/test/e2e 等常用命令
```

## License

MIT
