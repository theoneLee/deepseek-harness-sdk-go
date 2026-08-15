# Go SDK 演进路线

兼容性参考是上游的
[Python SDK](https://github.com/deepseek-ai/deepseek-harness/tree/master/python/sdk)。
每个版本都应针对同一份参考实现同步 Go API、测试以及中英文 README 文档。

## 当前已完成

- 高层 `DeepSeekHarness`、`Session` 与 prompt-to-idle 的 `Run` 活动区间。
- 基于子进程 stdio 的低层同步 JSON-RPC 客户端。
- 持久 inbox receipt 检测与只提取根会话事件。
- 通过 subagent 血缘边实现会话树通知订阅。
- server-to-client 请求队列，以及结果和错误应答。
- 请求超时、传输诊断、有界 stderr 尾部与关闭升级阶梯。
- PyPI 风格运行时解析、摘要校验与原子缓存解压。
- Linux amd64/arm64 与 macOS arm64 运行时标签。

## 下一步

- 使用 Python SDK fixture 建立带版本的兼容性测试矩阵。
- 为请求和订阅增加可选的 context 取消支持。
- 为常见事件族增加类型化辅助，同时保留原始 JSON 事件。
- 增加记录上游 Python SDK 参考 commit 的发布自动化。
- 在有稳定模型 fixture 后增加无密钥真实运行时 smoke job。

## 暂缓

- Windows 运行时支持，等待上游提供该平台的运行时 wheel。
- prompt 级取消与 session-close，等待协议公开文档化的方法。
- 将 `FinalResponse` 与 `FinishReason` 因果归属于单个 prompt；当前运行时只提供活动区间边界。
