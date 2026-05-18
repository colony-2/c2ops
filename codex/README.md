# `codex`

Go-backed Codex op with resumable sessions and outbox artifacts.

## Selector

Use this op from the repo root as:

```yaml
op: ./codex
```

Git selector example:

```yaml
op: git+https://github.com/colony-2/c2ops.git//codex@main
```

## What It Does

- Reads one JSON payload from stdin
- Runs the standalone Go project in this directory with `go run .`
- Returns a `codex.exec`-style result in the extension-op `{"output": ...}` envelope

## Inputs

- `prompt`
- `sessionId`
- `model`
- `env`
- `skills`
- `idle_timeout`
- `workdir_path`, `worktree_path`
- `artifact_inbox_path`, `artifact_outbox_path`
- `cell_relative_path`

`skills` accepts git skill source refs. The op fetches those skills and installs them into the Codex home for the invocation, so Codex can discover them and decide when to use them.

`idle_timeout` is a Go duration string and defaults to `5m`. If Codex produces no stdout/stderr activity for that duration, the op terminates the Codex process group and returns a timeout error instead of waiting for the outer extension timeout.

## Sandbox

Sandboxing is controlled by the reserved extension-op `sandbox` recipe input. The Codex op itself invokes the Codex CLI directly and does not create an additional Shai/Docker sandbox.

## Recipe

```yaml
sequence:
  - id: code_task
    op: git+https://github.com/colony-2/c2ops.git//codex@main
    inputs:
      prompt: Fix the failing tests in this repository.
      worktree_path: "{{ context.environment.worktree_path }}"
      artifact_outbox_path: "{{ context.environment.outbox }}"
```

## Outputs

- `status`
- `sessionId`
- `assistantSummary`
- `incompleteReason`
- `incompleteCategory`
- `pendingDependencies`
- `skills_installed`
- `outcome`
