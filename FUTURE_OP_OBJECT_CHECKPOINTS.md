# Future object-checkpoint migrations

Deferred work; none of these operations are changed by the Codex migration.
The current implementation is limited to `codex` and `codex/run_skill`.
See [the Codex consumer migration guide](./codex/MIGRATION_OBJECT_SESSIONS.md)
and [the c2j protocol guide](./GUIDE-Op-Object-Checkpoints.md).

## Recommended next operations

| Operation | Proposed contract | Reason |
| --- | --- | --- |
| `aider` | `c2ops.aider.session/v1` | Chat history currently depends on the original workdir. |
| `kimi` | `c2ops.kimi.session/v1` | CLI session IDs currently depend on a local Kimi home. |

Both should accept an optional `session` reference and publish a successor on
success, with a private writable restore per invocation. Keep workspace changes
in c2j Git snapshots and deliverables/diagnostics in ordinary artifacts. State
objects must exclude credentials and transient files. Their runtime file formats
are separate from Codex's; do not accept Codex sessions simply because the field
name matches.

### Aider: package and restore chat history

Update [op.yaml](./aider/op.yaml) and [main.py](./aider/main.py) to select a private
history directory from the object, independently of the wrapper's session ID.
Restore `chat.history.md` and invoke `--restore-chat-history` only when a valid
object was supplied; new sessions use `--no-restore-chat-history`.

Start the contract from the three files the wrapper currently manages:
`chat.history.md`, `input.history`, and `llm.history.jsonl`. Establish which are
required by the pinned Aider version for conversation restoration. Include chat
history; include the other histories only where needed for the supported resume
contract, otherwise retain them as diagnostics. Do not archive `prompt.txt` just
because it shares the current session directory.

Keep history outside the Git worktree and ordinary outbox. Preserve the same
logical session ID in successor metadata, but never concatenate caller-supplied
IDs into a storage path. A missing chat-history part must fail before launching
Aider. Tests must prove that earlier messages reach the next model request after
the original workdir has been deleted, not just that the returned IDs match.

### Kimi: export the actual CLI session store

Update [op.yaml](./kimi/op.yaml) and [main.py](./kimi/main.py) to restore/export a
private home and force `KIMI_CODE_HOME` to it. The current `env.setdefault` allows
`env.KIMI_CODE_HOME` to select a shared directory; reject that override in the
migrated contract. Continue injecting provider credentials through invocation
configuration and obtaining the session ID from `session.resume_hint`.

The wrapper does not document the CLI's internal resume layout. Before fixing
the v1 exporter, inspect the pinned `@moonshot-ai/kimi-code@2.1.1` store and prove
which files/indexes are needed, how a missing requested session is handled, and
whether state is tied to a worktree path. Implement any relocation in the adapter
and verify it in a different workdir/worktree. Do not assume an entire Kimi home
is safe to archive or that `--session <id>` alone guarantees a successful restore.

Keep the current process-group timeout handling. Publish only after clean CLI
completion, valid parsed output, and successful state validation/export; preserve
stdout/stderr diagnostics on failures without making partial state a successor.

## Deferred conversation objects

`pydantic` is a useful second-stage enhancement because its internal message
history already has a clear role. A future object could store serialized message
history in a `messages` file, plus pending tool calls and continuation metadata.
It would need to distinguish a new user turn from supplying results for pending
calls. In particular, its current `prompt if message_history is None else None`
logic cannot simply be reused for a resumed conversation with a new prompt.

Before implementing this or an `llm2` conversation object, specify handling of
tool-call/result IDs, completed versus pending calls, round budgets, and changes
to models, system prompts, and tool definitions. Checkpoint restoration must not
implicitly repeat completed external side effects. Use separate runtime contracts
until a portable message/tool schema is explicitly designed and tested. Neither
op should gain an object merely to wrap its existing response JSON.

## Operations to leave unchanged

| Operation | Reason |
| --- | --- |
| `llm` | Prompt/config to response/usage; no durable conversation contract. |
| `litellm` | Builds fresh system/user messages per request. |
| `gha` | Workflow execution results and artifacts are deliverables, not resumable agent state. |
| `gha-many` | Aggregates workflow results; no suspended op-owned session. |
| `jev` | Its `state` input is evaluation content that callers need to inspect. |
| `rule_gate` | Evaluates explicit rules and artifacts; framework object validation is separate. |

## Acceptance criteria for a future migration

- Branch checkpoint A into B and C, including concurrent consumers sharing one
  logical session ID. Each must restore exactly A; resuming B must see B.
- Resume on a different worker after deleting all original local state. Use a
  pinned real CLI with a mock provider and verify actual conversation continuity.
- Fail after modifying a private restore, then retry from the original checkpoint.
- Test malformed/unsupported state, missing artifacts, credential exclusion,
  relocated paths, and required files represented by symlinks or special files.
- Exercise c2j publication, hydration, child/root forwarding, and sandbox paths;
  verify private packs do not appear in ordinary recipe artifact maps.
- Specify the consumer migration and minimum c2j/CLI versions before release.
  A future migration's compatibility policy should be decided explicitly; Codex's
  current change is a hard break with no legacy importer or fallback.
