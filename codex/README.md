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

For authentication failures or hangs, see [troubleshooting](./TROUBLESHOOTING.md). Both Codex
ops write live phase and activity records to `codex-progress.jsonl` in the
artifact outbox, with a copy on process stderr. Its `exec.config` record shows the
launch mode, proxy URL, credential source/presence, model override, and fallback
reason without recording keys. It is written before launch and retained on 401
failures; see [custom API diagnostics](./TROUBLESHOOTING.md#diagnose-a-custom-api-401).

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
- `api` (optional external Responses API: `base_url`, `api_key`; legacy `api_key_env`)
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
Responses-compatible endpoint. Both manifests automatically bind their API
settings to these tenant values in JobDB:

- Config `codex_api_base_url`: the API root, such as `http://192.168.1.122:8317/v1`.
- Secret `codex_api_key`: the proxy's client API key.

```yaml
sequence:
  - id: code_task
    op: nix:github:colony-2/c2ops/published#codex
    inputs:
      prompt: Fix the failing tests in this repository.
```

No `api` input is needed. Keep your existing `model` input; the same defaults
apply to `skill-run`, including resumed sessions. See the
[complete example](examples/proxy-config.yaml).

This requires c2j's extension reference-default support, currently on `main`;
tests pin `v0.0.76-0.20261015024712-66010fb85af6`. c2j v0.0.75 and earlier do not
support these secret defaults. Invoke the op directly using its selector, as
above; the lower-level `extension_execution` form cannot prepare these defaults.

The manifests provide these default bindings:

```yaml
api:
  base_url: '${{ context.config.?codex_api_base_url.orValue(null) }}'
  api_key: '${{ context.secrets.?codex_api_key.orValue(null) }}'
```

To disable the bindings for one invocation and use the worker's normal Codex
setup without any settings lookup, set `api: null`. A partial `api` object
fills only missing fields from the manifest defaults. Explicit fields win;
for example, `api: {base_url: "https://other-proxy.example/v1"}` keeps the default
secret binding. To select another key, supply an explicit `api_key` secret
reference. If that reference is unavailable, c2j applies its fallback without
trying the default tenant key.

**The override activates only when both settings are available.** Missing config
resolves to null; a missing optional secret omits `api_key`. If either is absent,
the op retains its normal Codex provider and authentication setup, including
loading the worker's `CODEX_HOME` (or `$HOME/.codex`) files. Null or empty URL
config also retains that behavior. An empty secret is invalid; a settings-provider
outage or an unconfigured provider still fails the task rather than silently
changing providers. Use `api: null` on workers without a settings provider.

c2j resolves these references inside the consuming task. The manifest marks
`api_key` with `x-secret`; put a secret reference there, never a literal key.
The op passes the resolved key through the Codex child process environment,
not command arguments or generated configuration. The proxy holds subscription
credentials and handles their refresh; in external mode the worker needs neither
subscription `auth.json` nor Codex `config.toml`.

Codex appends `/responses` to `api.base_url`. HTTP and HTTPS are supported;
credentials, query strings, and fragments in the URL are rejected. The op sets
`wire_api="responses"`, disables OpenAI-specific authentication and WebSockets,
and uses Responses streaming over HTTP/SSE. No additional provider settings are
needed in the recipe.

For compatibility, `api_key_env` can name a nonempty worker environment variable
instead of using the tenant secret. An explicit `api_key_env` takes precedence
over `api_key`, including its default; the unused resolved key is not forwarded
to Codex. An explicitly named but missing environment variable is an error.
Prefer the default secret binding for new recipes.

With the override active, the op skips importing the worker's Codex authentication
and configuration files. The same settings apply to skill output repair. Each
invocation obtains its own settings; repeat any explicit overrides or `api: null`
when resuming a session. Workspace commands still execute inside the worker.

The regular `make -C codex test` suite checks c2j reference resolution against both
manifests and runs the packaged Codex CLI against local mock Responses servers.
It covers omitted and overridden API inputs, authentication, default behavior
when either setting is absent, tool execution, skill repair, and session resume.
CI runs this suite on both AMD64 and ARM64 without a subscription login or live
proxy.

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
