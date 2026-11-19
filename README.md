# DeepSeek Harness Go SDK

English | [中文](docs/README.zh.md)

Go SDK for driving DeepSeek Harness. The SDK starts the Harness runtime as a
subprocess and speaks the line-delimited JSON-RPC 2.0 protocol over stdio. It
is a clean-room Go implementation of the protocol and mirrors the layering and
activity semantics of the [upstream Python SDK](https://github.com/deepseek-ai/deepseek-harness/tree/master/python/sdk).

## Requirements

- Go 1.26 or newer.
- macOS arm64/x64, Linux amd64/arm64, or Windows amd64 when using the bundled
  runtime downloader.
- An explicit `DSHHome` or non-empty `DSH_HOME` environment variable.
- A `DEEPSEEK_API_KEY` environment variable, unless the selected profile uses
  another credential path or a local model proxy.

## Install

```sh
go get github.com/theoneLee/deepseek-harness-sdk-go
```

The SDK targets the single-file runtime published by the DeepSeek Harness
project. With no explicit executable, the first `Start` or `Run` resolves the
matching platform wheel from a PyPI-style index, verifies SHA-256, extracts it
into a cache, and reuses it on later starts. The default target is runtime
`0.2.2-rc.2`, distributed on [PyPI](https://pypi.org/project/deepseek-harness-runtime-bin/0.2.2rc2/).
The protocol reference is Python SDK commit
[`2e39e704`](https://github.com/deepseek-ai/deepseek-harness/tree/2e39e70496aa02986ca6a820c5d497e816b69d7e/python/sdk).
Use a matching build via `DSHBin` for features newer than the public wheel,
including the resume permission override and workspace-root relocation semantics.

## Quick start

```go
package main

import (
	"fmt"

	deepseekharness "github.com/theoneLee/deepseek-harness-sdk-go"
)

func main() {
	harness := deepseekharness.NewDeepSeekHarness(deepseekharness.DeepSeekHarnessConfig{
		DSHHome: "/absolute/path/to/dsh-home",
	})
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
	ReasoningEffort: "max",
	MaxTokens:  49_152,
	DSHHome:    "/absolute/path/to/dsh-home",
	Profile:    "sdk",
	Patches:    []string{"/absolute/path/to/patch.yml"},
})
defer harness.Close()
```

The runtime inherits the parent environment. `Env` overlays it for the child;
`BaseURL` and `APIKey` are convenience fields for
`DEEPSEEK_BASE_URL` and `DEEPSEEK_API_KEY`. `CWD` is sent in the initialize
payload; `RuntimeCWD` controls the subprocess working directory. `DSHHome`
overrides the child `DSH_HOME`; without it, the child must inherit a non-empty
`DSH_HOME`. `Profile` defaults to `sdk`, and each `Patches` entry becomes an
absolute `--patch` argument. `ReasoningEffort` becomes `reasoningEffort` in
initialize, while `MaxTokens == 0` omits `maxTokens`.

The initialize handshake has its own 30-second default timeout through
`InitializeTimeoutSeconds`; `RequestTimeoutSeconds` controls ordinary
requests.

Launch channels are resolved from most explicit to least explicit:

1. `LaunchArgsOverride`.
2. `Command` with `Args`.
3. `DSHBin`.
4. `RuntimeBin` or `BridgeBin` (deprecated compatibility aliases).
5. `DSH_RUNTIME_BIN` (deprecated compatibility override).
6. The bundled runtime downloader.

`DSHBin` and the bundled downloader append `--profile <Profile>` and ordered
`--patch <absolute path>` arguments, and require an explicit Harness home. The
new runtime has no downloaded default `cordis.yml`; its profile owns runtime
configuration. `LaunchArgsOverride` remains an internal-style escape hatch for
fake runtimes and bypasses the home check.

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

The callback receives each collected notification from the matching inbox
receipt or command admission onward. `FinishReason` is a `*string`; it is nil when no turn ended.

## Low-level client

`HarnessClient` exposes the protocol surface used by advanced integrations:

```go
client := deepseekharness.NewHarnessClient(deepseekharness.HarnessClientOptions{
	DSHBin:  "/path/to/dsh",
	DSHHome: "/absolute/path/to/dsh-home",
	Profile: "sdk",
})
defer client.Close()

if err := client.Start(); err != nil {
	panic(err)
}
_, err := client.Initialize(deepseekharness.InitializeParams{
	CWD: "./workspace", Provider: "deepseek-official", Model: "deepseek-v4-flash",
	ReasoningEffort: "max",
})
```

The client supports `Request`, `Notify`, `SessionPrompt`, `Subscribe`,
`SubscribeSessionTree`, `NextNotification`, `NextRequest`, `Respond`, and
`RespondError`. Server-to-client requests are queued until the caller answers
them. Notifications that no subscription matched are available through
`NextNotification`.

## Session control and native commands

- `ResumeSession` restores persisted history without sending input. Omit
  `ResumeSessionOptions.PermissionMode` to keep the saved permission preset.
- `Session.GetWorkingDirectory` and `Session.SetWorkingDirectory` read and
  change the runtime's effective session directory.
- `Run` routes input through `session/query`. Ordinary prompts complete at root
  idle after their inbox receipt. Native commands such as `/compact` and
  `/permission` wait for their matching command outcome. Command refusals return
  `FinishReason == "error"` with the runtime diagnostic in `FinalResponse`.
- Enable `ApprovalRequests` to receive `approval/request` over `NextRequest`.
  Drain requests concurrently with `Run` and reply through `Respond` using
  `ApprovalRequestResult`. For user questions, set `OnUserQuestion` and observe
  the callback context for withdrawal or shutdown.

## Local Office operations

`ConvertOffice`, `RecalculateOffice`, and `RenderOfficeImages` run the
upstream Office CLI through an installed sidecar. Prepare it with the Python
runtime's `download_office()` and pass the returned directory as
`OfficeOptions.SidecarDir` or `DSH_OFFICE_SIDECAR`.

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
