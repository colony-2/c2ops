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
- `output.schema` and `output.max_repair_attempts` (optional)
- `idle_timeout`
- `workdir_path`, `worktree_path`
- `artifact_inbox_path`, `artifact_outbox_path`

`skills` accepts git skill source refs. The op fetches those skills and installs them into the Codex home for the invocation, so Codex can discover them and decide when to use them.

`idle_timeout` is an optional Go duration string; omitted or `"0"` disables the idle watchdog. When explicitly set, no stdout/stderr activity for that duration terminates the Codex process group with a timeout error. The op has no default execution, input-read, version-check, or skill-preparation deadline; caller cancellation and recipe deadlines still apply.

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

The model returns its answer directly. The op no longer requests the old
`status` / `assistantSummary` / `pendingDependencies` JSON envelope from Codex.
The c2j process envelope (`output`, `objects`) and durable session references
remain part of the extension protocol.

| Field | Meaning |
| --- | --- |
| `response` | Final answer as a string without a schema, or the validated JSON value with a schema. Null if no answer is available or validation fails. |
| `response_text` | Original final-answer text, including invalid output for diagnosis. |
| `events` | Every native JSONL event in order, including all fields, unknown event types, intermediate updates, errors, and repair attempts. |
| `items` | Latest complete snapshot of each item, in first-seen order. Item IDs are scoped to each turn/invocation. |
| `terminal_event` | Last attempt's full `turn.completed` or `turn.failed` event, or null if the stream ended early. |
| `usage` | Last turn's native usage object, including any additional counters Codex emits. Earlier turns' usage remains in `events`. |
| `exit_code` | Last CLI process exit code; -1 for a signal or failure without an exit code, null if no process result is available. |
| `status`, `errorMessage` | Execution/validation result derived by the op, independent of the answer's contents. |
| `session`, `sessionId` | Durable checkpoint and diagnostic thread ID. Resume with `session`. |
| `skills_installed` | Installed skill names. |
| `workflow` | Optional status-artifact interpretation, present only with explicit `status_contract.path`. |
| `output_schema_valid`, `output_schema_errors` | Local validation result when a response schema is supplied. |
| `output_repair_attempts`, `schema_mode` | Correction count and `native` or `prompt` enforcement mode. |

A completed turn means the CLI finished successfully; it does not establish that
the requested task was fulfilled. A tool command can fail and Codex can recover
within the same turn. Inspect item statuses and command exit codes for details.
Transient `error` events are retained but do not override a later successful turn.

**Migration:** use `response_text` in place of `assistantSummary`, or `response`
for structured answers. The generic op no longer outputs `incompleteReason`,
`incompleteCategory`, or `pendingDependencies`. If a recipe needs semantic task
status, request those fields in its own response schema with instructions defining
their meaning, or explicitly configure a status artifact and read `workflow`.

## Optional response schema

```yaml
op: nix:github:colony-2/c2ops/published#codex
inputs:
  prompt: Assess this repository and report a summary and whether tests passed.
  output:
    schema:
      type: object
      additionalProperties: false
      required: [summary, tests_passed]
      properties:
        summary: {type: string}
        tests_passed: {type: boolean}
    max_repair_attempts: 3
```

The exact schema is passed to `codex exec --output-schema`, including on resume.
The final answer must parse as exactly one JSON value and pass local JSON Schema
validation before it is exposed as `response`. Markdown fences and trailing JSON
are rejected. Without a schema (or with `schema: null`), no native output schema
is supplied and `response` is the final text string.

An invalid answer triggers up to three corrections after the initial attempt.
Set `max_repair_attempts: 0` to fail immediately, or another nonnegative integer
to change the limit. Corrections resume the same conversation with validation
feedback. An explicit provider rejection of native structured output before any
tool activity switches to schema instructions in the prompt and local validation;
this fallback also consumes a correction attempt. Authentication, process, and
other execution failures are not retried as schema repairs.

Exhaustion fails the op, leaves `response` null, and retains the raw answer,
events, validation errors, and stdout/stderr artifacts. A failed op does not
publish a successor session. Repair prompts ask Codex to correct only its answer;
a resumed model still has the same tool access as the original invocation.

## Execution events and questions

The pinned CLI emits agent messages, reasoning summaries, command executions
(including output, status, and exit code), file changes, MCP tool calls,
collaboration tool calls, web searches, todo/plan updates, and error items.
These are available through `events` and `items`; the full transcript is also
retained as `stdout.jsonl` in the outbox. No known event field is filtered out.

Codex CLI 0.161.0's `exec` mode rejects `request_user_input` and has no dedicated
question event. A question in an ordinary agent message remains answer text;
the op does not infer a `needs_user_input` state from punctuation. A recipe can
request an explicit question/status field in its own schema and resume with a
subsequent prompt. Interactive structured questions, approvals, and MCP
elicitation require an app-server integration, which this op does not implement.
See the official [exec stream](https://learn.chatgpt.com/docs/non-interactive-mode)
and [app-server requests](https://learn.chatgpt.com/docs/app-server) documentation.

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

`run_skill` also returns native `events`, `items`, `response`, and execution
metadata. Its legacy `assistantSummary` output is a compatibility alias for the
final answer, not a field the model must fill. Its existing artifact validation
and optional status-contract outputs remain available. `output.from: response`
selects the final answer for JSON validation (`assistantSummary` remains an
accepted source alias); with a schema, this also uses native schema enforcement
and the same response repair mechanism (configure `output.max_repair_attempts`).
Final-answer schemas always fail the op on exhaustion; artifact validation retains
its existing `output.validation.on_error` policy. Artifact output schemas constrain the
artifact, not the model's final summary.
