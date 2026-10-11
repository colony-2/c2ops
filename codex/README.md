# `codex`

Go-backed Codex op with immutable object sessions and outbox artifacts.

The Nix packages include the native **Codex CLI 0.161.0** from our pinned
nixpkgs. Both entrypoints share one CLI store path and invoke it explicitly;
no pnpm or Node setup is needed. Source/Git manifests declare
`nix:github:colony-2/c2ops/published#codex-cli` and use the corresponding qualified
`nix run` command. `C2OPS_CODEX_BIN` can explicitly override the binary for
fixtures or compatibility experiments; the package wrapper defaults it to the
native CLI's absolute path.

Checkpoint producers must use **Codex CLI 0.148.0 or later**; both entrypoints
enforce this minimum with no upper bound. See the
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

Nix package selector:

```yaml
op: nix:github:colony-2/c2ops/published#codex
```

## What It Does

- Reads one JSON payload from stdin
- Runs a prebuilt Go executable from the Nix package (local source selectors use `go run .`)
- Returns a `codex.exec`-style result in the extension-op `{"output": ..., "objects": ...}` envelope

## Inputs

- `prompt`
- `session` (optional `c2ops.codex.session/v1` reference)
- `model`
- `api` (optional external Responses API: `base_url`, `api_key_env`)
- `env`
- `skills`
- `idle_timeout`
- `workdir_path`, `worktree_path`
- `artifact_inbox_path`, `artifact_outbox_path`

`skills` accepts git skill source refs. The op fetches those skills and installs them into the Codex home for the invocation, so Codex can discover them and decide when to use them.

`idle_timeout` is a Go duration string and defaults to `5m`. If Codex produces no stdout/stderr activity for that duration, the op terminates the Codex process group and returns a timeout error instead of waiting for the outer extension timeout.

## External API / CLIProxyAPI

Both `codex` and `skill-run` can send model requests to an external
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) server or another
Responses-compatible endpoint:

```yaml
op: nix:github:colony-2/c2ops/published#codex
inputs:
  prompt: Fix the failing tests in this repository.
  model: gpt-5.3-codex # Select a model available through your proxy.
  api:
    base_url: https://codex-proxy.example.com/v1
    api_key_env: CODEX_PROXY_KEY
```

Provision `CODEX_PROXY_KEY` in the **c2j worker's environment** through your
container/executor secret configuration. It is a client key issued by the proxy;
the proxy holds the subscription credentials and handles their refresh. The
worker needs neither a subscription `auth.json` nor a Codex `config.toml`.
The op references the key by environment variable name and does not put its
value in command arguments or generated configuration. Keep the key out of
recipe inputs; `env` overrides inherited environment values when explicitly set.

`api.base_url` is the API root, typically ending in `/v1`; Codex appends
`/responses`. HTTP and HTTPS are supported. Credentials, query strings, and
fragments in the URL are rejected. `api.api_key_env` defaults to
`C2OPS_CODEX_API_KEY` and must name a nonempty environment variable. External
mode uses Responses streaming over HTTP/SSE, with WebSockets disabled.

With `api` set, the op skips importing the worker's Codex authentication and
configuration files. The same settings apply to skill output repair. Pass `api`
again when resuming a session; the current invocation supplies the endpoint and
key. Workspace commands still execute inside the worker. Omitting `api` retains
the existing behavior of loading credentials/configuration from the worker's
`CODEX_HOME` (or `$HOME/.codex`).

The regular `make -C codex test` suite exercises external mode using the packaged
Codex CLI and a local mock Responses server, including bearer authentication,
tool execution, and session resume through `skill-run`. CI runs this suite on
both AMD64 and ARM64. It does not log into a subscription or require a live proxy.

## Sandbox

Sandboxing is controlled by the reserved extension-op `sandbox` recipe input. The Codex op itself invokes the Codex CLI directly and does not create an additional Shai/Docker sandbox.

## Recipe

```yaml
sequence:
  - id: code_task
    op: nix:github:colony-2/c2ops/published#codex
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
op: nix:github:colony-2/c2ops/published#skill-run
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
