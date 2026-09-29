# Object checkpoint migration scope

The first implementation covers **`codex` and `codex/run_skill` only**. Both use
`c2ops.codex.session/v1` and make a hard break from session-ID/artifact resumption.

- [Consumer migration guide](./codex/MIGRATION_OBJECT_SESSIONS.md): runtime setup,
  recipe changes, session forwarding, failures, and operational requirements.
- [Deferred operation proposals](./FUTURE_OP_OBJECT_CHECKPOINTS.md): Aider, Kimi,
  possible conversation objects, and operations that should remain unchanged.
- [c2j object protocol](./GUIDE-Op-Object-Checkpoints.md): framework behavior.

## Implemented design

An invocation restores the explicit session into a new private home, or creates
an empty home when `session` is omitted. Codex uses that home for both session
files and SQLite databases. The op no longer looks up or writes session-ID caches,
restores `codex-home-state`, or reconstructs rollout files from stdout.

The v1 contract is pinned to Codex CLI 0.157.1. Its `home` part contains session
and archived-session directories, memories when present, and consistent SQLite
copies of state, thread history, goals, queue, and memories. SQLite exports
include committed WAL contents. The stored thread index uses relative rollout
paths; restoration rewrites them into the current invocation's private home.
Credentials, host configuration, installed skills, logs, and transient files are
excluded. Inputs supply current credentials and skill sources each time.

`run_skill` keeps the same private execution state for the initial call and all
repairs. It emits one final draft after validation and diagnostic writing succeed.
A clean `incomplete` return includes a session; errors do not publish a successor.

Session state is distinct from Git workspace state and user artifacts. Routing
fields and structured skill outputs remain ordinary values. Object contents are
private storage artifacts, not entries in recipe artifact maps.
