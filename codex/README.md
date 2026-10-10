# `codex`

Go-backed Codex op with immutable object sessions and outbox artifacts.

Both manifests declare **`pnpm:@openai/codex@0.157.1`**, which c2j prepares and
places on the invocation's PATH. Direct invocations require **Codex CLI 0.148.0
or later**; both entrypoints enforce this minimum with no upper bound. See the
[compatibility results](./CLI_COMPATIBILITY.md).

**Breaking change:** resume with `session`, not `sessionId` or a
`codex-home-state` artifact. Read [the migration guide](./MIGRATION_OBJECT_SESSIONS.md)
for required runtime versions, recipe changes, and session forwarding.

For a hanging invocation, see [troubleshooting](./TROUBLESHOOTING.md). Both Codex
ops write live phase and activity records to `codex-progress.jsonl` in the
artifact outbox, with a copy on process stderr.

## Selector

Configure the [`colony2` cache](../NIX_PACKAGES.md) before using a Nix selector.

Use this op from the repo root as:

```yaml
op: ./codex
```

Nix package selector (replace `<commit>` with a published CI revision):

```yaml
op: nix:github:colony-2/c2ops/<commit>#codex
```

## What It Does

- Reads one JSON payload from stdin
- Runs a prebuilt Go executable from the Nix package (local source selectors use `go run .`)
- Returns a `codex.exec`-style result in the extension-op `{"output": ..., "objects": ...}` envelope

## Inputs

- `prompt`
- `session` (optional `c2ops.codex.session/v1` reference)
- `model`
- `env`
- `skills`
- `idle_timeout`
- `workdir_path`, `worktree_path`
- `artifact_inbox_path`, `artifact_outbox_path`

`skills` accepts git skill source refs. The op fetches those skills and installs them into the Codex home for the invocation, so Codex can discover them and decide when to use them.

`idle_timeout` is a Go duration string and defaults to `5m`. If Codex produces no stdout/stderr activity for that duration, the op terminates the Codex process group and returns a timeout error instead of waiting for the outer extension timeout.

## Sandbox

Sandboxing is controlled by the reserved extension-op `sandbox` recipe input. The Codex op itself invokes the Codex CLI directly and does not create an additional Shai/Docker sandbox.

## Recipe

```yaml
sequence:
  - id: code_task
    op: nix:github:colony-2/c2ops/<commit>#codex
    inputs:
      prompt: Fix the failing tests in this repository.
      worktree_path: "{{ context.environment.op.worktree_path }}"
      artifact_outbox_path: "{{ context.environment.op.outbox }}"
```

## Outputs

- `session` (durable `c2ops.codex.session/v1` reference)
- `status`
- `sessionId` (diagnostic only; never pass it as a resume input)
- `assistantSummary`
- `incompleteReason`
- `incompleteCategory`
- `pendingDependencies`
- `skills_installed`
- `outcome`

## `run_skill`

This module also exposes a nested selector-backed op for running one requested
skill:

```yaml
op: nix:github:colony-2/c2ops/<commit>#skill-run
```

`run_skill` uses the same Codex execution path as this op, but generates the
skill invocation prompt from structured inputs and validates the declared JSON
output artifact after Codex exits. Its required input is `skill`; `prompt` is
optional supplemental text.

Both selectors use the same object type. Every successful call, including a clean
`incomplete` return, produces a successor checkpoint. Omitting `session` starts a
new private session. Old session caches and inbox home-state directories are not
used. Git workspace state and user deliverables continue through c2j's existing
workspace/artifact channels.

`run_skill` also accepts `env` for invocation credentials and provider settings.
Output repair shares the invocation's private state and publishes only after
validation finishes. No successful checkpoint is returned for a failed op.
