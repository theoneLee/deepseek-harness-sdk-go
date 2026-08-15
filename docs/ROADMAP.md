# Go SDK Roadmap

The compatibility reference is the upstream
[Python SDK](https://github.com/deepseek-ai/deepseek-harness/tree/master/python/sdk).
Each release should update the Go API, tests, and both language README files
against the same reference behavior.

## Current

- High-level `DeepSeekHarness`, `Session`, and prompt-to-idle `Run` intervals.
- Low-level synchronous JSON-RPC client over subprocess stdio.
- Durable inbox receipt detection and root-only event extraction.
- Session-tree notification subscriptions through subagent lineage edges.
- Server-to-client request queue with result and error responses.
- Request timeouts, transport diagnostics, bounded stderr tails, and shutdown
  escalation.
- PyPI-style runtime resolution with digest verification and atomic cache
  extraction.
- Linux amd64/arm64 and macOS arm64 runtime tags.

## Next

- Add a versioned compatibility test matrix driven by Python SDK fixtures.
- Add optional context-aware cancellation for requests and subscriptions.
- Add typed helpers for common event families without hiding raw JSON events.
- Add release automation that records the upstream Python SDK reference commit.
- Add a keyless real-runtime smoke job when a stable model fixture is available.

## Deferred

- Windows runtime support, pending an upstream runtime wheel for that platform.
- Prompt-level cancellation and session-close methods until the protocol exposes
  documented methods for them.
- Causal attribution of `FinalResponse` and `FinishReason` to an individual
  prompt; the runtime currently exposes an activity interval boundary instead.
