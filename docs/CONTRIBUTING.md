# Code Contribution Guide

This repository tracks the public behavior of the upstream DeepSeek Harness
Python SDK while keeping the Go API idiomatic. Contributions should preserve
that compatibility boundary and keep the raw JSON-RPC protocol observable.

## Before coding

1. Read the relevant section of the upstream Python SDK and the local
   [`ROADMAP.md`](ROADMAP.md).
2. Check existing tests and decide which protocol or lifecycle behavior proves
   the change.
3. Keep the change focused. Avoid unrelated dependency, formatting, or API
   churn.

## Local workflow

Use a short-lived branch for each change. Go code must be formatted and pass
the repository checks:

```sh
gofmt -w *.go
go vet ./...
go test -race ./...
```

Changes to subprocess behavior should use a fake runtime or a local HTTP
fixture rather than live model credentials. Do not commit downloaded runtime
wheels, API keys, session data, or machine-specific paths.

## Public behavior

When a public API or protocol behavior changes:

- Add or update a mechanism test that exercises the behavior through real
  stdio pipes where practical.
- Preserve JSON fields that callers may need even when typed helpers are added.
- Update [`README.md`](../README.md), [`README.zh.md`](README.zh.md), and the
  applicable roadmap item.
- State compatibility and migration impact in the pull request.

## Pull requests

Pull requests should have a concise English title and explain the reason for
the change, the compatibility impact, and the commands used for verification.
Keep commits focused and avoid generated authorship or tool metadata.
