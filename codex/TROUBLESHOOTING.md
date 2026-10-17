# Troubleshoot Codex ops

Applies to `codex` and `codex/run_skill`. Historical investigation baseline: c2j
**v0.0.61**, Codex CLI **0.157.1**. Current default tests use native nixpkgs Codex **0.161.0**.
Production accepts
**0.148.0 or later**. A dependency update alone does not establish
the cause of a particular deployment's hang.

## Check the actual execution environment

Upgrade the deployed c2j worker as well as submission tools. This repository's
`codex/go.mod` controls its linked library and fixture tests; it cannot replace
an already running worker. Use `nix:github:colony-2/c2ops/published#codex` (or
`#skill-run`) after CI has published its packages. Check the resolved store path
in setup diagnostics; see [Nix packages](../NIX_PACKAGES.md) for worker setup.

Run these checks in the worker environment, and inside the execution container
when using a sandbox:

```sh
go version -m "$(command -v c2j)"
nix run github:colony-2/c2ops/published#codex-cli -- --version
# Must report: codex-cli <version>, with version >= 0.148.0
```

Nix selectors execute a prebuilt wrapper. Check tool-setup diagnostics for
package substitution or declared CLI dependency failures before execution.

Local/Git source selectors start with `go run .`. Module downloads and compilation happen
before any wrapper diagnostics. Check worker launch
errors and, from the resolved op checkout in the same environment, run
`go mod download` and `go build ./...`. Check Go availability, cache permissions,
and module-network access if either stalls. Recipe `inputs.env` configures the
Codex child; it cannot fix PATH or module access needed to start the wrapper or
its preliminary version probe.

## Diagnose a custom API 401

Both ops write an `exec.config` record to the **`codex-progress.jsonl` output
artifact** after preparing the private Codex home and before starting the CLI.
It remains in the outbox when the CLI returns 401 or the op fails; it is also
copied to parent stderr. Artifact upload/visibility on failed tasks is controlled
by the c2j runner. Inspect the local outbox if the failed task's artifacts are not
available through your UI.

```sh
jq 'select(.phase == "exec.config")' codex-progress.jsonl
```

For a custom URL with a secret input, the record includes:

```json
{
  "phase": "exec.config",
  "mode": "external_api",
  "reason": "url_and_credentials_available",
  "provider": "c2ops_external",
  "base_url": "https://proxy.example/v1",
  "wire_api": "responses",
  "requires_openai_auth": false,
  "supports_websockets": false,
  "credential_source": "api.api_key",
  "credential_env": "C2OPS_CODEX_RESOLVED_API_KEY",
  "credential_present": true,
  "model_override": "gpt-5.3-codex",
  "resuming": false,
  "auth_json_present": false,
  "config_toml_present": false
}
```

- `mode: external_api` identifies the wrapper's external provider override.
  Check `base_url` against the intended proxy API root, including `/v1` where
  required. Codex appends `/responses`.
- `credential_source` is `api.api_key`, `input.env`, or `worker_environment` in
  external mode. `credential_env` is the variable name passed to Codex;
  `credential_present` confirms a nonempty value is available, **not that the
  proxy accepts it**. An explicit `api_key_env` takes precedence over `api_key`.
- `mode: default` means no external provider override was passed. `reason`
  distinguishes `missing_base_url`, `missing_api_key`,
  `missing_base_url_and_api_key`, and `api_not_supplied_or_disabled`.
  `input_presence` records which API fields reached the op, as booleans.
  Check config `codex_api_base_url`, secret `codex_api_key`, recipe overrides,
  and the worker's reference-default support.
- `auth_json_present` and `config_toml_present` indicate regular files in the
  prepared private home at launch. Their contents are never recorded. In default
  mode, provider/authentication are delegated to Codex; this record does not
  inspect its merged configuration or establish whether credentials are valid.
- `model_override` is the model input passed by the op; an empty string means
  Codex chooses it from its configuration/defaults. The earlier `version.ready`
  record contains the actual CLI version. `exec.running` confirms launch.

Every execution, including a skill repair, appends its own configuration record.
No key values, hashes, prompts, full environment, or full command arguments are
included. The URL and model are public diagnostic fields. A 401 with the expected
URL and `credential_present: true` still requires checking the proxy's client key
and authentication configuration; these diagnostics cannot determine the
server's reason for rejecting the request.

If CLIProxyAPI returns `{"error":"Missing API key"}` despite
`credential_present: true`, check for a trailing newline in the stored
`codex_api_key` secret. Codex 0.161.0 silently omits the Authorization header
when its key contains a newline or another character invalid in an HTTP header.
The op now trims surrounding whitespace from both resolved secrets and selected
environment credentials, and rejects embedded whitespace, control characters,
and non-ASCII characters before launching Codex. Older packages need a clean,
single-line secret or an update to a published package containing this fix.
Never paste the key into logs when checking it. If a clean key still produces
this error, check whether redirects or an intervening proxy strip Authorization;
the launch diagnostic does not observe the headers received by the server.

If `exec.config` is absent, check the last phase below and verify that the
resolved c2ops package includes this diagnostic change.

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
| `waiting for JSON input on stdin` on stderr | Runner must deliver a complete JSON object. Input waits until delivery or caller cancellation. |
| `invocation.start` | Input decoded and paths normalized; check session configuration errors. |
| `version.check` | `codex --version` has no wrapper-imposed deadline. Check the executable in the actual environment; caller cancellation still applies. |
| `session.restore` | A supplied checkpoint is being checked/copied and SQLite paths relocated. Check filesystem availability and checkpoint size. |
| `skills.prepare` | Git skill sources are being fetched. Check network/authentication; preparation inherits caller cancellation without adding a deadline. |
| `exec.start` | Captures are open; includes `stdout_path`, `stderr_path`, and the configured idle timeout. Home preparation/process launch follows. |
| `exec.config` | Home preparation finished; inspect the provider mode, credential source, and URL. Process launch follows. |
| `exec.running` / `exec.wait` | CLI started. Wait records appear every 30 seconds with PID, elapsed/idle seconds and stdout/stderr byte counts. Inspect the capture paths. |
| `exec.finished` | CLI wait completed; output sanitization/parsing and artifact publication follow. `failed` refers to process execution, not the final op outcome. |
| `skill.validate` / `skill.repair` | `run_skill` validates output or starts a repair attempt. Each repair has its own execution records. |
| `session.export` | Conversation files and SQLite state are being exported. Inspect disk availability and session size. |
| `session.exported` / `invocation.end` | Local export or wrapper work completed. If the job still hangs, inspect c2j object freezing/artifact upload and worker completion. |

`idle_timeout` is disabled by default. When explicitly set, it counts **both**
CLI stdout and stderr and starts when the CLI launches. Periodic CLI stderr keeps it alive even without an answer.
`idle_timeout: "0"` disables it. Wrapper progress records do not reset it. Use
`idle_timeout: "30s"` for a short diagnostic run only if that limit suits the task.
The manifest sets no execution timeout. An explicitly configured CLI idle timer
does not cover startup, checkpoint I/O, or object publication. Filesystem operations
are not all context-interruptible.

Progress records omit prompts, environment values, and CLI arguments. Raw
`stdout.jsonl` and `stderr.txt` captures can contain task data. Inspect them locally
and redact before sharing. Temporary capture paths are valid during execution;
the wrapper copies captures to the artifact outbox before cleaning up.

## Immediate version-check failure in the minimal Nix worker

The October 10, 2026 report was reproduced on ARM64 using
`ghcr.io/colony-2/base:build-38077543859-1` and the exact reported output,
`/nix/store/czns5caf18is7dfcdlcsr0dy820mfwjp-c2ops-codex-0.1.0`.
Both freshly prepared and reused `pnpm:@openai/codex@0.162.1` installations
failed at `version.check` in about 3 ms. Calling the prepared dependency directly
revealed the stderr hidden by the old version probe:

```text
.../node_modules/.bin/codex: line 30: sed: command not found
.../node_modules/.bin/codex: line 50: cd: null directory
```

pnpm's shell launcher uses `sed` to determine its own directory. The worker
image contains GNU sed in the Nix store, but does not expose it on PATH. Setup
checks that the executable exists; it does not execute the launcher. Adding
only GNU sed's bin directory to PATH made the unchanged package reach
`version.ready` with CLI version `0.162.1`. This reproduction did not involve
an executable-name collision or an incompatible ARM64 Codex binary.

Packages with pnpm dependencies now bind GNU sed and coreutils on PATH. Failed
version probes also report argv, resolved runner path, exit status, stdout and
stderr. No CLI reinstall or version change is needed for this failure.

Codex and skill-run now use a native Nix CLI and no longer need pnpm.
The [worker regression](../scripts/check-codex-worker.py) runs both actual
packages in that image with ambient CLI/package-manager commands disabled. It
deliberately supplies an invalid checkpoint so execution stops immediately
after a successful version probe, without a provider call. CI runs it on native
ARM64 and AMD64 runners. Host-only live tests did not catch this because their
environment already supplied `sed`; recipe fixtures also substitute the CLI.

## Known c2j v0.0.61 Shai stdin issue

Source inspection shows a separate upstream problem:

1. `pkg/ops/extensions/execution_op.go` serializes the extension input into
   `process.RunRequest.Stdin`.
2. `pkg/ops/process/runtime.go:executeOnHost` connects those bytes to the child.
3. `executeInShai` constructs `shai.SandboxConfig.PostSetupExec` without using
   `req.Stdin`. The pinned Shai v0.0.9 `SandboxExec` has no stdin field; its
   `internal/shai/runtime/ephemeral_runner.go` uses the worker's `os.Stdin`.

Consequently, a sandboxed extension can wait on the worker's stdin instead of
receiving its invocation JSON, or encounter immediate EOF. The wrapper logs
that it is waiting for input and honors caller cancellation, but does not impose
an input deadline or repair upstream transport. This source-level defect is a candidate cause
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

Version probing and Git skill preparation inherit caller cancellation without
adding deadlines. Both subprocess paths terminate their process groups on
cancellation and bound pipe draining. SIGINT/SIGTERM also cancel wrapper work.
CLI output capture paths are retained when output parsing fails.

Run all op suites from the repository root:

```sh
nix develop -c make test-local
```

For focused debugging after building packages, run
`C2OPS_TEST_FLAKE=path:/absolute/path/to/c2ops make -C codex test` in the dev shell.
Tests cover missing stdin,
JSON without EOF, stalled version probing, inherited output pipes, progress
visibility, real CLI calls against a local mock API, and object-session resume
fixtures. They do not exercise a real Shai container or paid provider access.

When reporting a remaining hang, include worker/c2ops/CLI versions, sandbox
configuration, elapsed time, last progress records, sanitized capture excerpts,
and whether it reproduces with a fresh session and without external skills.
