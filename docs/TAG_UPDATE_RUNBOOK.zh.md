# 官方 Tag 跟进 Runbook

## 目标与边界

本流程用于把官方 `deepseek-harness` 的最新 `dsh-*` tag 同步到本 Go SDK。
每次只处理一个最近版本：从当前 Go 仓库的最新版本直接更新到目标官方 tag，
不回补中间版本，不为历史 tag 追加改动。

官方 tag 与 Go 运行时版本的映射如下：

| 官方 tag | Go `SDKVersion` | PyPI 运行时版本 |
| --- | --- | --- |
| `dsh-v0.1.1-rc.2` | `0.1.1-rc.2` | `0.1.1rc2` |
| `dsh-v0.1.5-alpha.1` | `0.1.5-alpha.1` | `0.1.5a1` |

Go 仓库发布 tag 使用 `v${SDKVersion}`，因此本次 Go 发布 tag 是
`v0.1.5-alpha.1`，不是官方的 `dsh-v0.1.5-alpha.1`。

本次核对时，官方 Python 发布工作流尚未把 `0.1.5a1` runtime wheel 发布到公开 PyPI；
PyPI 当前可见的 `deepseek-harness-runtime-bin` 最新版本仍是 `0.1.2rc1`。因此 Go
模块和 API 可以先按新 tag 发布，但在 wheel 可下载前，自动 runtime 启动会得到明确的
索引错误；临时运行应通过 `DSHBin` 指向匹配的 `dsh`，或通过 `RuntimeIndexURL` 使用
已发布该版本 wheel 的内部索引。每次 tag 更新都必须重新检查这一状态。

## 1. 固定基线

先确认 Go 仓库没有无关工作区改动，并记录当前版本：

```sh
cd /Users/lee/learn/deepseek-harness-sdk-go
git status --short --branch
git describe --tags --abbrev=0
rg -n '^const SDKVersion' runtime.go
```

准备官方仓库的临时 clone，并切到目标 tag：

```sh
target_tag=dsh-v0.1.5-alpha.1
upstream_dir="$(mktemp -d /tmp/deepseek-harness-tag.XXXXXX)"
git clone https://github.com/deepseek-ai/deepseek-harness.git "$upstream_dir/repo"
git -C "$upstream_dir/repo" checkout --detach "$target_tag"
git -C "$upstream_dir/repo" rev-parse HEAD
```

如果需要找上一个官方参考版本，使用 tag 列表或 GitHub Release 列表；不要把 Go
仓库的版本号直接当作官方 tag，因为两边的 tag 命名和发布节奏可能不同：

```sh
git -C "$upstream_dir/repo" tag --list 'dsh-v*' --sort=-v:refname | head -20
gh release list --repo deepseek-ai/deepseek-harness --limit 20
# 当前 Go 基线是 dsh-v0.1.1-rc.2，因此只对比最近一个已同步版本。
previous_tag=dsh-v0.1.1-rc.2
```

## 2. 判断本次变化属于哪一层

先看 release notes，再分别看 Python SDK 和 runtime 变化：

```sh
gh release view "$target_tag" --repo deepseek-ai/deepseek-harness
git -C "$upstream_dir/repo" diff "$previous_tag..$target_tag" --stat -- python/sdk
git -C "$upstream_dir/repo" diff "$previous_tag..$target_tag" -- python/sdk
git -C "$upstream_dir/repo" diff "$previous_tag..$target_tag" --stat
```

按下面的规则处理：

1. `python/sdk` 有 API、模型或协议变化：同步修改 Go 的 `api.go`、`client.go`、
   `errors.go`、`events.go`、`models.go`，并为每个行为增加测试。配置字段、启动参数、
   初始化超时和错误诊断也属于公共行为，不能只更新版本号。
2. 只有 runtime、adapter、wheel 或配置变化：通常只更新 `runtime.go` 中的
   `SDKVersion`，但仍要确认可执行文件名、sidecar、平台 wheel 标签和缓存解压布局。
3. release notes 有功能变化但 `python/sdk` 没有代码变化时，不要在 Go SDK 中复制
   runtime 内部实现；Go SDK 通过下载匹配版本的 runtime 自动获得该功能。
4. 如果协议字段变化同时出现在 Python SDK 和 runtime，先以 Python SDK 的公开行为
   为兼容边界，再用 fake runtime 覆盖 JSON-RPC 交互。

## 3. Python 与 Go 的对比基线

当前两边保持的主要公共行为：

| 能力 | Python SDK | Go SDK |
| --- | --- | --- |
| 高层入口 | `DeepSeekHarness`、`Session`、`run` | `DeepSeekHarness`、`Session`、`Run` |
| 活动边界 | inbox receipt 到根 session 下一次 idle | 相同 |
| 通知范围 | 根 session 和已发现的 subagent 后代 | 相同 |
| 结果 | `final_response`、`finish_reason`、events、notifications | `FinalResponse`、`FinishReason`、Events、Notifications |
| 低层协议 | request、notify、session prompt、订阅、server request | 对应导出的 Go 方法 |
| 初始化 | `reasoning_effort`、`max_tokens`，initialize 独立 30 秒超时 | `ReasoningEffort`、`MaxTokens`、`InitializeTimeoutSeconds` |
| 启动 | `dsh` + `--profile`/`--patch`，必须显式 `DSH_HOME` | 相同，另支持 Go 下载器和显式 `DSHBin` |
| 运行时 | pip 安装同版本 runtime wheel，含主程序和 sidecar | PyPI 风格索引下载、SHA-256 校验、缓存、解压和 sidecar 校验 |

以下是有意保留的语言适配差异，不应在每次 tag 更新时机械消除：

- Python 使用上下文管理器、异常、Pydantic 和 `snake_case`；Go 使用 `defer Close`、
  `error`、`errors.As`、普通结构体和导出的 `CamelCase` 方法。
- Python 的 `JsonValue` 是递归类型；Go 使用 `any` 与 `JSONObject` 保留原始 JSON，
  并用 `RequestAs[T]` 提供可选的类型化解码。
- Go 额外提供 `Command`、`Args`、`RuntimeIndexURL`、`RuntimeVersion`、
  `RuntimeCacheDir` 和 `DSH_RUNTIME_BIN`，以适应 Go 二进制部署和测试 fixture；旧的
  `RuntimeBin`、`BridgeBin`、`LaunchArgsOverride` 作为兼容入口保留。
- Python 的低层 `request`/`session_prompt` 支持回调、通知过滤器和显式订阅参数；Go
  当前通过 `Subscribe`、`SubscribeSessionTree` 和高层 `Session.Run` 完成同一主要流程，
  但 `SessionPrompt` 尚未提供完全相同的可选参数集合。这是已知 parity 差异。
- Python 通过 `dsh_bin` 使用已安装 runtime wheel；Go 默认从 PyPI 风格索引按平台下载
  wheel，因此 Go 还需要处理 SHA-256、缓存、zip 解包和跨平台文件名。
- Python 的 `RunResult` 已移除 `session_root`；Go 为源兼容暂时保留 `RunResult.SessionRoot`
  和旧配置字段，但新代码不应依赖它们。

## 4. 实现与测试

把官方 tag 的版本部分转换为 Go 版本并更新默认 runtime：

```go
const SDKVersion = "0.1.5-alpha.1"
```

同时检查 `ToPyPIVersion(SDKVersion)` 是否得到 `0.1.5a1`。公共 API 或协议发生变化
时，遵循下面的最小变更范围：

- 优先复用现有 Go 类型和 JSON 原始字段，不为一次 tag 更新引入新的抽象层。
- 为正常路径、协议错误、超时或 transport 关闭分别补机制测试。
- runtime 变化使用本地 HTTP fixture 和 fake runtime，不提交 wheel、凭据或 session 数据。
- 同步更新中英文 README 中的默认目标版本，并在本文件的“本次更新记录”追加一项。

## 5. 验证清单

```sh
gofmt -w *.go
go vet ./...
go test ./...
go test -race ./...
git diff --check
```

确认默认版本和旧版本引用：

```sh
rg -n 'SDKVersion|0\.1\.5|dsh-v|dsh-jsonrpc-agent|cordis\.yml|DSH_CWD' runtime.go README.md docs
git status --short
```

发布前还要确认 `.github/workflows/release.yml` 的校验规则：它要求 Go tag
等于 `v${SDKVersion}`。正常发布时只推送新的 Go `v*` tag，让 workflow 创建 Release；
不要移动官方 `dsh-*` tag。

## 本次更新记录

- 官方目标：`dsh-v0.1.5-alpha.1`
- 官方 commit：`5dda764ed3aa172535a7967b06ff95d9cbfe536a`
- Release 变化：支持动态系统提示词、实验性 Sidebar、会话格式 V3，以及 agent/inbox
  插件 API 调整；这些 runtime 内部变化由匹配版本的 runtime wheel 提供。
- Python SDK：相对 `dsh-v0.1.1-rc.2`，新增 `reasoning_effort`、`dsh_bin`、`profile`、
  `patches`、`dsh_home` 和 `initialize_timeout_seconds`；移除 `session_root`、`cordis`、
  `runtime_bin`、`bridge_bin`、`launch_args_override`，并移除 `RunResult.session_root`。
  initialize 新增 `reasoningEffort`，启动改为普通 `dsh --profile/--patch`，要求显式
  `DSH_HOME`。
- Runtime：可执行文件切换为 `deepseek-harness-sdk-runtime-*`，Linux/macOS/Windows
  增加或校验 `-rg` sidecar，macOS 继续需要 `-spawn-helper`，并支持 macOS x64 与
  Windows x64；不再提供默认 `cordis.yml`。
- Go 侧：同步上述公共行为，默认 runtime 更新为 `0.1.5-alpha.1`，保留旧字段作为
  deprecated 兼容入口，并新增 profile/patch/home/独立初始化超时及对应机制测试。


## 0.2.2 同步记录

- Python SDK 协议参考为 `master` 的 `2e39e70496aa02986ca6a820c5d497e816b69d7e`。
- 最新上游 tag 为 `dsh-v0.2.2-rc.5`。核对时 PyPI 已发布的最新 wheel 为
  `0.2.2rc2`，Go 默认使用 `0.2.2-rc.2`。依赖更新协议的调用方可指定对应的
  `DSHBin`，或通过 `RuntimeVersion` 与 `RuntimeIndexURL` 使用相应 wheel。
- Go 同步了会话恢复、权限覆盖参数、工作目录、原生命令运行边界、审批与用户问答。
  `session/query` 返回 method not found 时，可使用旧运行时的 `session/prompt`。
- 发布 wheel 新增平台资源下载清单，解压时保留在运行时预期的位置。Office API 复用
  已安装的 sidecar，资源安装交给上游 runtime 的 `download_office()`。
