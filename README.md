# DeepSeek Harness Go SDK

English | [中文](docs/README.zh.md)

Go SDK for driving DeepSeek Harness. The SDK starts the Harness runtime as a
subprocess and speaks the line-delimited JSON-RPC 2.0 protocol over stdio. It
is a clean-room Go implementation of the protocol and mirrors the layering and
activity semantics of the [upstream Python SDK](https://github.com/deepseek-ai/deepseek-harness/tree/master/python/sdk).

## Requirements

- Go 1.26 or newer.
- macOS arm64 or Linux amd64/arm64 when using the bundled runtime downloader.
- A `DEEPSEEK_API_KEY` environment variable, unless the selected Cordis
  composition uses another credential path or a local model proxy.

## Install

```sh
go get github.com/theoneLee/deepseek-harness-sdk-go
```

The SDK targets the single-file runtime published by the DeepSeek Harness
project. With no explicit executable, the first `Start` or `Run` resolves the
matching platform wheel from a PyPI-style index, verifies SHA-256, extracts it
into a cache, and reuses it on later starts. The default target is runtime
`0.1.1-rc.2`, corresponding to upstream tag `dsh-v0.1.1-rc.2`.

## Quick start

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

`DeepSeekHarness` starts lazily and keeps the subprocess alive across runs.
Always call `Close` when the harness is no longer needed so the runtime is
reaped promptly.

## Configuration

```go
harness := deepseekharness.NewDeepSeekHarness(deepseekharness.DeepSeekHarnessConfig{
	Provider:   "deepseek-official",
	Model:      "deepseek-v4-flash",
	MaxTokens:  49_152,
	Cordis:     "examples/jsonrpc-agent/cordis.yml",
	SessionRoot: ".dsh-sessions",
})
defer harness.Close()
```

The runtime inherits the parent environment. `Env` overlays it for the child;
`BaseURL` and `APIKey` are convenience fields for
`DEEPSEEK_BASE_URL` and `DEEPSEEK_API_KEY`. `CWD` becomes `DSH_CWD` and the
initialize payload; `RuntimeCWD` controls the subprocess working directory.
`MaxTokens == 0` omits `maxTokens` from initialize.

Launch channels are resolved from most explicit to least explicit:

1. `LaunchArgsOverride`.
2. `Command` with `Args`.
3. `RuntimeBin` or `BridgeBin`.
4. `DSH_RUNTIME_BIN`.
5. The bundled runtime downloader.

Explicit launch channels do not inject a downloaded default `cordis.yml`.
When the downloader is selected, its default configuration is injected through
`DSH_CORDIS_CONFIG` only when that variable is absent or empty.

## Sessions and results

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
			// Render or persist the event as needed.
		}
	},
)
```

`Run` waits for the prompt's durable `agent/inbox/spliced` receipt, then
collects notifications until the root session reaches its next `idle` state.
Notifications from known descendant sessions are included through
`subagent.started` lineage edges. `RunResult.Events` contains root-session
events only. `FinalResponse` is the last committed root assistant text in the
interval, and `FinishReason` is the last `turn/end` reason kind when present.
Those values describe the owned activity interval, not an output causally
assigned to only the submitted prompt.

The callback is invoked for each collected notification after the inbox
receipt. `FinishReason` is a `*string`; it is nil when no turn ended.

## Low-level client

`HarnessClient` exposes the protocol surface used by advanced integrations:

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

The client supports `Request`, `Notify`, `SessionPrompt`, `Subscribe`,
`SubscribeSessionTree`, `NextNotification`, `NextRequest`, `Respond`, and
`RespondError`. Server-to-client requests are queued until the caller answers
them. Notifications that no subscription matched are available through
`NextNotification`.

## Errors

Errors are concrete types suitable for `errors.As`: `JsonRpcError`,
`RequestTimeoutError`, `ProtocolError`, `TransportClosedError`,
`RuntimeResolveError`, and `IOError`. `TransportClosedError` includes the
process exit code when available and the last 400 stderr lines.

## Development

```sh
gofmt -w *.go
go vet ./...
go test -race ./...
```

GitHub Actions runs formatting, vet, race-enabled tests, and the regular test
suite on Linux and macOS. Releases are created from matching `v*` tags; see
[`docs/ROADMAP.md`](docs/ROADMAP.md) for planned parity work.
For the next upstream tag update, follow the [tag update runbook](docs/TAG_UPDATE_RUNBOOK.zh.md).

## Code Contribution Guide

| Area | Requirement |
| --- | --- |
| Scope | Keep changes focused on the Go SDK and its compatibility with the upstream Python SDK. |
| Implementation | Follow existing package patterns, preserve raw protocol data, and add comments only for non-obvious behavior. |
| Tests | Add or update mechanism tests for protocol, lifecycle, and runtime-resolution changes. |
| Validation | Run `gofmt`, `go vet ./...`, and `go test -race ./...` before opening a pull request. |
| Documentation | Update the relevant English and Chinese docs in [`docs/`](docs/) when public behavior changes. |
| Pull request | Explain the behavior change, compatibility impact, and verification commands. Do not include API keys or runtime artifacts. |

The full contributor workflow, including branch, commit, and pull request
guidance, is in [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md).

## Relationship to DeepSeek Harness

This repository owns the Go SDK. The runtime is published by the
[DeepSeek Harness project](https://github.com/deepseek-ai/deepseek-harness) as
`deepseek-harness-runtime-bin`. The SDK follows the documented
[stdio JSON-RPC protocol](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/sdk/protocol/README.md)
and tracks the Python SDK's public behavior as that implementation evolves.
