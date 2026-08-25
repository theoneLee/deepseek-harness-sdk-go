# 官方 Tag 跟进 Runbook

## 目标与边界

本流程用于把官方 `deepseek-harness` 的最新 `dsh-*` tag 同步到本 Go SDK。
每次只处理一个最近版本：从当前 Go 仓库的最新版本直接更新到目标官方 tag，
不回补中间版本，不为历史 tag 追加改动。

官方 tag 与 Go 运行时版本的映射如下：

| 官方 tag | Go `SDKVersion` | PyPI 运行时版本 |
| --- | --- | --- |
| `dsh-v0.1.1-rc.2` | `0.1.1-rc.2` | `0.1.1rc2` |

Go 仓库发布 tag 使用 `v${SDKVersion}`，因此本例的 Go 发布 tag 是
`v0.1.1-rc.2`，不是官方的 `dsh-v0.1.1-rc.2`。

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
target_tag=dsh-v0.1.1-rc.2
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
# 将下面的值替换为目标 tag 之前最近的官方 tag。
previous_tag=dsh-v0.1.0-rc.8
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
   `errors.go`、`events.go`、`models.go`，并为每个行为增加测试。
2. 只有 runtime、adapter、wheel 或配置变化：通常只更新 `runtime.go` 中的
   `SDKVersion`，确认运行时解析、平台 wheel 标签和缓存解压布局仍然有效。
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
| 运行时 | pip 安装同版本 runtime wheel | PyPI 风格索引下载、SHA-256 校验、缓存和解压 |

以下是有意保留的语言适配差异，不应在每次 tag 更新时机械消除：

- Python 使用上下文管理器、异常、Pydantic 和 `snake_case`；Go 使用 `defer Close`、
  `error`、`errors.As`、普通结构体和导出的 `CamelCase` 方法。
- Python 的 `JsonValue` 是递归类型；Go 使用 `any` 与 `JSONObject` 保留原始 JSON，
  并用 `RequestAs[T]` 提供可选的类型化解码。
- Go 额外提供 `Command`、`Args`、`RuntimeIndexURL`、`RuntimeVersion`、
  `RuntimeCacheDir` 和 `DSH_RUNTIME_BIN`，以适应 Go 二进制部署和测试 fixture。
- Python 的低层 `request`/`session_prompt` 支持回调、通知过滤器和显式订阅参数；Go
  当前通过 `Subscribe`、`SubscribeSessionTree` 和高层 `Session.Run` 完成同一主要流程，
  但 `SessionPrompt` 尚未提供完全相同的可选参数集合。这是已知 parity 差异，不是本次
  `dsh-v0.1.1-rc.2` 的新增变化。

## 4. 实现与测试

把官方 tag 的版本部分转换为 Go 版本并更新默认 runtime：

```go
const SDKVersion = "0.1.1-rc.2"
```

同时检查 `ToPyPIVersion(SDKVersion)` 是否得到 `0.1.1rc2`。公共 API 或协议发生变化
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
rg -n 'SDKVersion|0\.1\.0|0\.1\.1|dsh-v' runtime.go README.md docs
git status --short
```

发布前还要确认 `.github/workflows/release.yml` 的校验规则：它要求 Go tag
等于 `v${SDKVersion}`。正常发布时只推送新的 Go `v*` tag，让 workflow 创建 Release；
不要移动官方 `dsh-*` tag。

## 本次更新记录

- 官方目标：`dsh-v0.1.1-rc.2`
- 官方 commit：`b150a551b8d465e31e418e1b2eaf5e79bbb7d28e`
- Release 变化：DeepSeek 适配器优先使用 Files API 上传并复用图像，图像预处理按模型
  要求自动缩放和转换格式。
- Python SDK：`api.py`、`client.py`、`errors.py`、`models.py`、`__init__.py` 在本次
  官方 tag 与上一个官方 SDK 版本间没有公共 API 代码变化。
- Go 侧：更新默认 runtime 版本到 `0.1.1-rc.2`，由 runtime wheel 提供上述功能；Go
  JSON-RPC 和高层 API 无需额外改动。
