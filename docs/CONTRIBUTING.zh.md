# 代码贡献指南

本仓库跟踪 DeepSeek Harness 上游 Python SDK 的公共行为，同时保持 Go API
符合 Go 的使用习惯。贡献需要维护这条兼容性边界，并保留原始 JSON-RPC
协议的可观察性。

## 开始编码前

1. 阅读上游 Python SDK 的相关章节和本地 [`ROADMAP.md`](ROADMAP.md)。
2. 查看已有测试，确定用哪个协议或生命周期行为证明本次改动。
3. 保持变更聚焦，避免无关的依赖、格式或 API 变动。

## 本地流程

每项改动使用独立的短生命周期分支。Go 代码必须格式化并通过以下检查：

```sh
gofmt -w *.go
go vet ./...
go test -race ./...
```

涉及子进程行为时，尽量使用 fake runtime 或本地 HTTP fixture，不要依赖真实模型凭据。
不要提交下载的运行时 wheel、API key、会话数据或机器相关路径。

## 公共行为

当公共 API 或协议行为发生变化时：

- 增加或更新机制测试；可行时通过真实 stdio 管道验证。
- 即使增加类型化辅助，也要保留调用方可能需要的 JSON 字段。
- 同步更新 [`README.md`](../README.md)、[`README.zh.md`](README.zh.md) 和相应路线项。
- 在 Pull Request 中说明兼容性和迁移影响。

## Pull Request

Pull Request 使用简洁的英文标题，并说明改动原因、兼容性影响和验证命令。
保持提交聚焦，不添加自动生成的署名或工具元数据。
