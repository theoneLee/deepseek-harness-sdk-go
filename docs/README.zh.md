# DeepSeek Harness Go SDK

[English](../README.md) | 中文

用于驱动 DeepSeek Harness 的 Go SDK。SDK 把 Harness 运行时作为子进程启动，
通过 stdio 上按行分帧的 JSON-RPC 2.0 协议通信。它是该协议的 clean-room Go
实现，分层结构和活动区间语义与 [上游 Python SDK](https://github.com/deepseek-ai/deepseek-harness/tree/master/python/sdk)
保持对等。

## 环境要求

- Go 1.26 或更高版本。
- 使用内置运行时下载器时，平台为 macOS arm64 或 Linux amd64/arm64。
- `DEEPSEEK_API_KEY` 环境变量；如果所选 Cordis 组合使用其他凭据方式或本地模型代理，则不需要。

## 安装

```sh
go get github.com/theoneLee/deepseek-harness-sdk-go
```

SDK 目标是 DeepSeek Harness 项目发布的单文件运行时。如果没有显式指定可执行程序，
第一次 `Start` 或 `Run` 会从 PyPI 风格索引解析对应平台的 wheel，校验 SHA-256，
解压到缓存，并在后续启动中复用。
当前默认目标是运行时 `0.1.1-rc.2`，对应上游 `dsh-v0.1.1-rc.2` tag。

## 快速开始

```go
package main

import (
	"fmt"

	deepseekharness "github.com/theoneLee/deepseek-harness-sdk-go"
)

func main() {
	harness := deepseekharness.NewDeepSeekHarness()
	defer harness.Close()

	result, err := harness.Run("Say hi.")
	if err != nil {
		panic(err)
	}
	fmt.Println(result.FinalResponse)
}
```

`DeepSeekHarness` 会惰性启动运行时，并在多次运行之间保留子进程。不再使用时请调用
`Close`，以便及时回收运行时。

## 配置

```go
harness := deepseekharness.NewDeepSeekHarness(deepseekharness.DeepSeekHarnessConfig{
	Provider:    "deepseek-official",
	Model:       "deepseek-v4-flash",
	MaxTokens:   49_152,
	Cordis:      "examples/jsonrpc-agent/cordis.yml",
	SessionRoot: ".dsh-sessions",
})
defer harness.Close()
```

运行时默认继承父进程环境。`Env` 会为子进程覆盖环境变量；`BaseURL` 与 `APIKey` 是
`DEEPSEEK_BASE_URL` 和 `DEEPSEEK_API_KEY` 的便捷字段。`CWD` 同时用于 `DSH_CWD` 与
initialize 载荷；`RuntimeCWD` 控制子进程工作目录。`MaxTokens == 0` 时不会向 initialize
发送 `maxTokens`。

启动通道按以下顺序选择，越靠前越明确：

1. `LaunchArgsOverride`。
2. `Command` 与 `Args`。
3. `RuntimeBin` 或 `BridgeBin`。
4. `DSH_RUNTIME_BIN`。
5. 内置运行时下载器。

显式启动通道不会注入下载得到的默认 `cordis.yml`。使用下载器时，只有在
`DSH_CORDIS_CONFIG` 未设置或为空时，才会把默认配置注入该变量。

## 会话与结果

```go
session, err := harness.StartSession("session-reuse")
if err != nil {
	panic(err)
}

result, err := session.Run(
	deepseekharness.BlocksInput([]deepseekharness.JSONObject{
		{"type": "text", "text": "Inspect this task."},
	}),
	func(notification deepseekharness.Notification) {
		if notification.Method == "session.event" {
			// 按需渲染或持久化事件。
		}
	},
)
```

`Run` 会等待提示词持久写入 `agent/inbox/spliced` 的回执，然后收集通知，直到根会话
下一次进入 `idle`。通过 `subagent.started` 发现的后代会话通知也会被包含在内。
`RunResult.Events` 只包含根会话事件。`FinalResponse` 是该活动区间内最后提交的根会话
助手文本；`FinishReason` 是该区间最后一个 `turn/end` 的 reason kind（如果存在）。
这些字段描述的是 `Run` 所拥有的活动区间，不代表某项输出只在因果上归属于刚提交的提示词。

回调会在 inbox receipt 之后对每一条收集到的通知调用。没有轮次结束时，`FinishReason`
为 nil。

## 低层客户端

高级集成可以直接使用 `HarnessClient` 的协议接口：

```go
client := deepseekharness.NewHarnessClient(deepseekharness.HarnessClientOptions{
	RuntimeBin: "/path/to/dsh-jsonrpc-agent-pkg-macos-arm64",
})
defer client.Close()

if err := client.Start(); err != nil {
	panic(err)
}
_, err := client.Initialize(deepseekharness.InitializeParams{
	CWD: "./workspace", Provider: "deepseek-official", Model: "deepseek-v4-flash",
})
```

客户端支持 `Request`、`Notify`、`SessionPrompt`、`Subscribe`、
`SubscribeSessionTree`、`NextNotification`、`NextRequest`、`Respond` 与
`RespondError`。server-to-client 请求会排队等待调用方应答；没有任何订阅匹配的通知
可通过 `NextNotification` 获取。

## 错误

错误是可用于 `errors.As` 的具体类型：`JsonRpcError`、`RequestTimeoutError`、
`ProtocolError`、`TransportClosedError`、`RuntimeResolveError` 与 `IOError`。
`TransportClosedError` 会在可用时携带进程退出码，以及 stderr 最后 400 行。

## 开发

```sh
gofmt -w *.go
go vet ./...
go test -race ./...
```

GitHub Actions 会在 Linux 和 macOS 上执行格式检查、vet、race 测试和普通测试。发布从
匹配的 `v*` tag 创建；计划中的对等工作见 [`ROADMAP.md`](ROADMAP.md)。
后续跟进官方 tag 时，请按 [tag 更新 runbook](TAG_UPDATE_RUNBOOK.zh.md) 执行。

## 代码贡献指南

| 范围 | 要求 |
| --- | --- |
| 变更范围 | 聚焦 Go SDK 及其与上游 Python SDK 的兼容性。 |
| 实现 | 遵循现有包结构，保留原始协议数据；仅为不明显的行为添加注释。 |
| 测试 | 涉及协议、生命周期或运行时解析时，增加或更新机制测试。 |
| 验证 | 创建 Pull Request 前运行 `gofmt`、`go vet ./...` 和 `go test -race ./...`。 |
| 文档 | 公共行为变化时，同步更新 [文档目录](./) 中相关的中英文文档。 |
| Pull Request | 说明行为变化、兼容性影响和验证命令；不要提交 API key 或运行时产物。 |

完整的分支、提交和 Pull Request 流程见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。

## 与 DeepSeek Harness 的关系

本仓库负责 Go SDK。运行时由 [DeepSeek Harness 项目](https://github.com/deepseek-ai/deepseek-harness)
以 `deepseek-harness-runtime-bin` 发布。SDK 遵循文档化的
[stdio JSON-RPC 协议](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/sdk/protocol/README.md)，
并随着 Python SDK 的演进同步其公共行为。
