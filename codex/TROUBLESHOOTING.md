# Troubleshoot a hanging Codex op

Applies to `codex` and `codex/run_skill`. Tested dependency baseline: c2j
**v0.0.61**, Codex CLI **0.157.1**. A dependency update alone does not establish
the cause of a particular deployment's hang.

## Check the actual execution environment

Upgrade the deployed c2j worker as well as submission tools. This repository's
`codex/go.mod` controls its linked library and fixture tests; it cannot replace
an already running worker. Pin the recipe's c2ops Git selectors to the revision
containing these diagnostics so that an old cached selector is not executed.

Run these checks in the worker environment, and inside the execution container
when using a sandbox:

```sh
go version -m "$(command -v c2j)"
command -v codex
codex --version
# Must report: codex-cli 0.157.1
```

The extension starts with `go run .`. Module downloads and compilation happen
before any wrapper diagnostics or its startup timeouts. Check worker launch
errors and, from the resolved op checkout in the same environment, run
`go mod download` and `go build ./...`. Check Go availability, cache permissions,
and module-network access if either stalls. Recipe `inputs.env` configures the
Codex child; it cannot fix PATH or module access needed to start the wrapper or
its preliminary version probe.

## Find the last completed phase

After input decoding and path normalization, the wrapper writes
`codex-progress.jsonl` into `artifact_outbox_path` (normally
`context.environment.op.outbox`). Inspect the invocation's local outbox on the
worker while it runs:

```sh
tail -f /actual/invocation/outbox/codex-progress.jsonl
```

c2j v0.0.61 buffers extension stdout/stderr until execution finishes, so a quiet
worker log does not establish that the process is inactive. Progress is also
copied to stderr, but the outbox file is the live source. Uploaded artifacts may
only appear after completion. For Shai, map the container path to its mounted
host invocation directory.

| Last phase / symptom | What to inspect |
| --- | --- |
| No progress file | The wrapper may not have started, may be waiting for stdin, or may have rejected input/paths. Check startup and process stderr. |
| `waiting for JSON input on stdin` on stderr | Runner must deliver a complete JSON object. The wrapper fails after 30 seconds without one. |
| `invocation.start` | Input decoded and paths normalized; check session configuration errors. |
| `version.check` | `codex --version` has a 15-second deadline. Check the executable in the actual environment. |
| `session.restore` | A supplied checkpoint is being checked/copied and SQLite paths relocated. Check filesystem availability and checkpoint size. |
| `skills.prepare` | Git skill sources are being fetched. Check network/authentication; the preparation context has a two-minute deadline. |
| `exec.start` | Captures are open; includes `stdout_path`, `stderr_path`, and the configured idle timeout. Home preparation/process launch follows. |
| `exec.running` / `exec.wait` | CLI started. Wait records appear every 30 seconds with PID, elapsed/idle seconds and stdout/stderr byte counts. Inspect the capture paths. |
| `exec.finished` | CLI wait completed; output sanitization/parsing and artifact publication follow. `failed` refers to process execution, not the final op outcome. |
| `skill.validate` / `skill.repair` | `run_skill` validates output or starts a repair attempt. Each repair has its own execution records. |
| `session.export` | Conversation files and SQLite state are being exported. Inspect disk availability and session size. |
| `session.exported` / `invocation.end` | Local export or wrapper work completed. If the job still hangs, inspect c2j object freezing/artifact upload and worker completion. |

`idle_timeout` defaults to `5m`, counts **both** CLI stdout and stderr, and starts
when the CLI launches. Periodic CLI stderr keeps it alive even without an answer.
`idle_timeout: "0"` disables it. Wrapper progress records do not reset it. Use
`idle_timeout: "30s"` for a short diagnostic run only if that limit suits the task.
The manifest's `30m` timeout covers extension execution; the CLI idle timer does
not cover startup, checkpoint I/O, or object publication. Filesystem operations
are not all context-interruptible.

Progress records omit prompts, environment values, and CLI arguments. Raw
`stdout.jsonl` and `stderr.txt` captures can contain task data. Inspect them locally
and redact before sharing. Temporary capture paths are valid during execution;
the wrapper copies captures to the artifact outbox before cleaning up.

## Known c2j v0.0.61 Shai stdin issue

Source inspection shows a separate upstream problem:

1. `pkg/ops/extensions/execution_op.go` serializes the extension input into
   `process.RunRequest.Stdin`.
2. `pkg/ops/process/runtime.go:executeOnHost` connects those bytes to the child.
3. `executeInShai` constructs `shai.SandboxConfig.PostSetupExec` without using
   `req.Stdin`. The pinned Shai v0.0.9 `SandboxExec` has no stdin field; its
   `internal/shai/runtime/ephemeral_runner.go` uses the worker's `os.Stdin`.

Consequently, a sandboxed extension can wait on the worker's stdin instead of
receiving its invocation JSON, or encounter immediate EOF. The wrapper's new
30-second input deadline turns the wait into an actionable failure, but does
not repair upstream transport. This source-level defect is a candidate cause
when `sandbox.type: shai` is used; it has not been reproduced here in a running
Shai container or confirmed as the cause of the reported deployment hang.

To isolate it, use a minimal extension that reads stdin and returns a fixed JSON
envelope, with a short outer timeout and no credentials/provider call. Compare
the intended host and Shai execution environments where permitted. Record
whether input arrives and whether stdin closes. A successful host-only Codex
fixture does not validate the sandbox transport.

For example, create a temporary `stdin-probe/op.yaml` in a diagnostic workspace:

```yaml
name: stdin-probe
timeout: 10s
command:
  - sh
  - -c
  - |
    payload=$(cat)
    case "$payload" in
      *stdin-probe*) printf '%s\n' '{"output":{"received":true}}' ;;
      *) echo 'missing invocation payload' >&2; exit 1 ;;
    esac
input_schema:
  type: object
  properties:
    marker: {type: string}
```

Invoke `op: ./stdin-probe` with `inputs.marker: stdin-probe` and the reserved
`inputs.sandbox: {type: shai}` (or `{type: none}` for the host comparison). `cat`
deliberately requires EOF, so this tests both payload delivery and stream closure.
It returns only a boolean and does not print the input. Use the deployment's
usual Shai configuration; no Codex installation is needed for this probe.

The upstream fix needs an explicit per-invocation stdin reader/payload passed
through c2j into Shai's container exec, with EOF after the payload. Do not change
the worker's global `os.Stdin`: concurrent invocations need independent inputs.
Retest the same sandbox probe and a real checkpoint-resume recipe after that fix.

## Wrapper fixes and regression checks

Two hangs were reproduced locally and fixed:

- The decoder previously waited for EOF after a complete JSON object. It now
  consumes one object without waiting for the runner to close stdin.
- A CLI descendant could keep inherited stdout/stderr pipes open after the main
  process exited. A two-second `exec.Cmd.WaitDelay` now bounds pipe draining.

Version probing is bounded to 15 seconds. Git skill preparation gets a two-minute
context deadline. Both subprocess paths terminate their process groups on
cancellation and bound pipe draining. SIGINT/SIGTERM also cancel wrapper work.
CLI output capture paths are retained when output parsing fails.

Run all op suites from the repository root:

```sh
npm_config_cache=/tmp/c2ops-npm-cache make test
```

For focused debugging, run `make test` from `codex/`. Tests cover missing stdin,
JSON without EOF, stalled version probing, inherited output pipes, progress
visibility, real CLI calls against a local mock API, and object-session resume
fixtures. They do not exercise a real Shai container or paid provider access.

When reporting a remaining hang, include worker/c2ops/CLI versions, sandbox
configuration, elapsed time, last progress records, sanitized capture excerpts,
and whether it reproduces with a fresh session and without external skills.
