# Migrate Codex recipes to object sessions

This is a **hard breaking change** for both `./codex` and `./codex/run_skill`
(`skill.run`). Resume by passing `session`, the complete object reference returned
by an earlier invocation. Omit `session` to start a new conversation.

There is no legacy mode, automatic importer, or fallback to local state.
`sessionId` and `resume_context` inputs are rejected, including empty values.
`session: null` is rejected; omit the field for a new session.

## 1. Upgrade the execution environment

Use a c2j runtime with immutable object support. The current tested baseline is
**v0.0.61**. Tagged c2j `v0.0.55` does not contain this API. Upgrade the actual
workers as well as the recipe submission/validation tools. See the
[troubleshooting guide](./TROUBLESHOOTING.md) for a known stdin-forwarding issue
in v0.0.61's Shai execution path.

For a source-installed c2j CLI:

```sh
go install github.com/colony-2/c2j/cmd/c2j@v0.0.61
```

Install **Codex CLI 0.148.0 or later** on the execution environment's PATH,
including inside the sandbox if used. Older or unparseable versions are rejected;
there is no upper version bound. For example, the pinned test runtime is:

```sh
npm install -g @openai/codex@0.157.1
codex --version
# codex-cli 0.157.1
```

Checkpoints record the actual producer CLI version. A consumer can use a
different CLI version at or above the minimum; the checkpoint format and files
must still pass validation. The existing `codex-0.157.1/v1` state-format name is
retained as a layout identifier, not a required CLI version. Existing object
sessions from 0.157.1 remain usable. See [compatibility tests](./CLI_COMPATIBILITY.md).

The Go-backed extension needs Go 1.26 or later and access to its module
dependencies. SQLite handling is implemented in Go; no `sqlite3` executable is
required. Pin both Codex selectors to the same c2ops revision containing this
change. In the examples below, local selectors refer to that migrated checkout.
For Git selectors, replace `REVISION` with its actual commit or release:

```yaml
op: git+https://github.com/colony-2/c2ops.git//codex@REVISION
# or:
op: git+https://github.com/colony-2/c2ops.git//codex/run_skill@REVISION
```

c2j supplies `C2J_OBJECT_OUTBOX` automatically. Do not set it in recipe `env`, and
do not substitute `artifact_outbox_path` for it. A missing object outbox is an
error before Codex runs, even for a new conversation.

## 2. Replace old resume wiring

| Old usage | New usage |
| --- | --- |
| `sessionId: "${{ sequence.previous.outputs.sessionId }}"` | `session: "${{ sequence.previous.outputs.session }}"` |
| Bind `codex-home-state` from the previous artifact outbox into the next inbox | Remove that binding. The session object carries its files. |
| Reuse a workdir or shared Codex home to keep conversation state | Pass the session reference. Every invocation has its own home. |
| Supply `resume_context` | Remove it. Supply any new task context in `prompt` or the skill's `input`. |
| Set `env.CODEX_HOME` or `env.CODEX_SQLITE_HOME` | Remove it. The op controls both state locations. |
| Store only the returned `sessionId` for a later job | Store and submit the complete `session` reference. |

Keep ordinary artifact bindings that supply task documents or generated outputs.
Only session-state artifact wiring is removed. An inbox containing the old
`codex-home-state` directory is rejected rather than silently ignored.

**Existing sessions cannot be resumed by this revision.** Start a new session and,
if useful, include a prior summary or relevant user deliverables in the prompt or
artifact inbox. An old ID or directory is not a c2j object reference. Finish any
work that requires the old runtime before switching that recipe to this revision.

## 3. Start and continue a conversation

```yaml
id: codex-session-example
version: "1"
sequence:
  - id: investigate
    op: ./codex
    inputs:
      prompt: Investigate the failing tests and explain the cause.
      env:
        CODEX_API_KEY: "${{ secrets.openai_api_key }}"
  - id: fix
    op: ./codex
    inputs:
      prompt: Apply the fix and run the relevant tests.
      session: "${{ sequence.investigate.outputs.session }}"
      env:
        CODEX_API_KEY: "${{ secrets.openai_api_key }}"
outputs:
  session: "${{ sequence.fix.outputs.session }}"
  status: "${{ sequence.fix.outputs.status }}"
  summary: "${{ sequence.fix.outputs.assistantSummary }}"
```

Use a complete `${{ ... }}` expression. Do not concatenate the reference into a
string, serialize it into a prompt, extract its ID, or pass a hydrated filesystem
descriptor from another invocation.

Credentials and invocation configuration are supplied on every call. Checkpoints
exclude authentication files, `config.toml`, and installed skills. Supply `skills`
again when needed and pin external skill sources for repeatable behavior. A
worker-provided Codex credential/configuration home may still seed the private
home; its conversation state is never used for resumption. Configure credentials
in the execution environment through your existing c2j secret mechanism.

The standard noninteractive CLI API-key variable is `CODEX_API_KEY`; see the
[official noninteractive documentation](https://developers.openai.com/codex/noninteractive/).

## 4. Continue through `run_skill`

Both entrypoints accept the same `c2ops.codex.session/v1` contract. A recipe can
continue from `codex` into `run_skill`, or from `run_skill` back into `codex`:

```yaml
- id: implement
  op: ./codex/run_skill
  inputs:
    skill: implement-fix
    session: "${{ sequence.investigate.outputs.session }}"
    input:
      requirement: Fix the identified issue and report the validation results.
    output:
      path: implementation/result.json
      schema:
        type: object
        required: [summary]
        properties:
          summary: {type: string}
    env:
      CODEX_API_KEY: "${{ secrets.openai_api_key }}"
```

This is a sequence-node fragment for the preceding recipe. The requested skill
must exist in the worktree's `.agents/skills` or in supplied `skills` sources.
Automatic output repair runs against the same private session and produces one
final checkpoint. Route `sequence.implement.outputs.session` onward. Parsed
outputs, validation results, status contracts, and deliverable artifacts retain
their existing roles.

## 5. Forward a session to another job or child recipe

Persist the complete root `session` output, then submit that value as the next
recipe's `session` input in the same tenant. The receiving recipe declares the
object type and binds it into the root scope:

```yaml
id: continue-codex-session
version: "1"
input_schema:
  session:
    type: object
    object_type: c2ops.codex.session/v1
    required: true
inputs:
  session: "${{ inputs.session }}"
sequence:
  - id: continue_work
    op: ./codex
    inputs:
      prompt: Continue the investigation from the saved conversation.
      session: "${{ inputs.session }}"
      env:
        CODEX_API_KEY: "${{ secrets.openai_api_key }}"
outputs:
  session: "${{ sequence.continue_work.outputs.session }}"
```

Use the same complete-value forwarding for child inputs/results or state outputs.
The backing artifact must remain available: forwarding a reference to another
job does not create a retention pin. Retain originating job/task artifacts for
as long as their sessions are needed. Missing, corrupted, wrong-type, or
cross-tenant checkpoints fail instead of starting a new conversation.

## Branching, failures, and visible outputs

Passing A's `session` to two calls creates independent continuations B and C.
To continue B, explicitly pass B's reference. There is no implicit “latest”
session. `sessionId` remains a diagnostic output and may be identical for A, B,
and C; it must not be used as a checkpoint key.

The object restores **agent state**, not repository contents. c2j's Git workspace
snapshots still determine each invocation's code. Arrange the intended workspace
when branching or moving a conversation between jobs.

A clean `completed` or `incomplete` return includes a session. A CLI error,
timeout, failed required validation, or export error fails the operation and
does not provide a successful successor. Retry from the last successful
checkpoint. Diagnostic logs may still be available through normal artifacts.

The object pack does not appear in `sequence.node.artifacts`. Continue reading
`stdout.jsonl`, `stderr.txt`, and user deliverables there. Authorized JobDB
debugging can inspect backing object artifacts; opacity is not encryption.

## Troubleshooting and custom process runners

For a hanging invocation, start with the live `codex-progress.jsonl` file in the
invocation's artifact outbox and the [diagnostic steps](./TROUBLESHOOTING.md).

| Error | Action |
| --- | --- |
| `sessionId` / `resume_context` no longer supported | Remove the input and route `outputs.session`. |
| `C2J_OBJECT_OUTBOX` missing | Upgrade/configure the c2j worker, not only the submitting client. |
| Codex version below minimum / unparseable | Install 0.148.0 or later in the actual execution environment. |
| `env.CODEX_HOME` is managed by the session object | Remove the override; supply credentials/configuration separately. |
| `codex-home-state` no longer supported | Remove the legacy artifact binding and begin a new object session. |
| Invalid metadata, missing files, unsupported format | Use an intact checkpoint from these migrated ops and the supported CLI version. Do not edit its reference or home contents. |

If you invoke `go run .` directly rather than through c2j, your runner must
implement the [extension object protocol](../GUIDE-Op-Object-Checkpoints.md): supply
an absolute object outbox, hydrate references into independent writable files,
read the `output` plus `objects` envelope, persist drafts, and replace `$object`
markers with durable references. The returned marker is not itself resumable.
Export paths must remain available until the runner has frozen their contents.
Using c2j handles this lifecycle automatically.

Go callers of `Run` / `RunSkill` likewise consume the returned `Objects` map with
the output marker. Low-level `Execute` requires an explicit private `CodexHome`;
it does not persist checkpoints or look up state by session ID.

The production ops do not import c2j's Go packages. Their manifests declare the
`c2ops.codex.session/v1` contract, and all framework communication uses the JSON
extension protocol. `SessionInput.Ref` is opaque `json.RawMessage`; custom
runners must validate references and their type before providing hydrated
inputs. Codex validates its own session metadata and files. c2j remains a Go
dependency only for tests that exercise the actual object store and recipes.
