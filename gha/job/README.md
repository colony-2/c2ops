# `gha-job` (experimental)

Runs one externally scheduled GitHub Actions job using act's exported Go API.
This is the first execution building block for the
[c2j orchestration plan](../../PLAN_GHA_C2J_ORCHESTRATION.md), not the proposed
`gha` `backend: c2j`. The existing `gha` backends keep their behavior.

```yaml
perms:
  jobdb:
    secrets:
      resolve: [build_token]
sequence:
  - id: build
    execution_needs:
      platform: linux/amd64
    op: nix:github:colony-2/c2ops/published#gha-job
    inputs:
      workflow: .github/workflows/build.yml
      job_id: build
      vars:
        RELEASE_CHANNEL: '${{ context.config.release_channel }}'
      secrets:
        CLIENT_KEY: '${{ context.secrets.build_token }}'
```

The package contains a compiled Go executable plus Git, Bash, coreutils and Node.
It uses the same native ARM64/AMD64 Nix build, process-contract checks, Cachix
publication and `published` promotion as the other ops. Workers need no compiler.
The coordinate becomes available after CI publishes this revision.

Inputs:

- `workflow`: relative path inside c2j's prepared worktree; escaping paths fail.
- `job_id`: exactly one workflow job. Predecessors are never executed implicitly.
- `needs`: accepted results keyed by every direct **and transitive** prerequisite.
  Each contains `conclusion` (`success`, `failure`, `cancelled`, `skipped`) and
  public string `outputs`. Transitive statuses are needed by act's status functions.
- `matrix`: one explicit, fully expanded assignment for a matrix job. Include
  values must be present; unknown combinations fail. This helper does not fan out.
- `event_name` (default `workflow_dispatch`) and `event`: caller-supplied GitHub
  evaluation context. It reaches expressions, environment and the event file.
  The helper does not evaluate workflow trigger filters or normalize synthetic
  PR identity. The caller must supply consistent metadata; it does not change
  the checked-out source revision or create a GitHub PR.
- `env` / `vars`: public settings. `secrets`: named c2j secret references,
  materialized only at invocation. Names are case insensitive; collisions fail.
- `worktree_path`: defaults to `context.environment.op.worktree_path`.

Supported execution is trusted shell steps and local JavaScript/composite
actions in act host mode. A built-in local checkout preparation step copies the
prepared source into act's disposable workspace without downloading checkout.
Every invocation gets a separate workspace, HOME and temporary/cache directory.
Ambient credential environment variables are blanked; bind secrets explicitly.

`runs-on` labels do **not** provision a runner here: all labels execute in the
assigned host environment. The calling c2j recipe/provisioner must map them to
matching `execution_needs`. Host mode is not an OS security boundary. It is meant
to execute inside an appropriately isolated worker.

The output has `job_id`, `conclusion`, string `outputs`, and a small `diagnostics`
object reference (`c2ops.gha.diagnostics/v1`). The diagnostics object's `log` file
is backed by JobDB and survives worker deletion. Logs are also streamed to
stderr. Secret-bearing job outputs are suppressed; logs mask named secrets and
dynamic masks, including multiline values. Skipped jobs have empty outputs.
Shell failure is returned as `conclusion: failure` with a successful op envelope
so a parent can schedule `always()` reporting. Invalid input and infrastructure
errors fail the op. No implicit execution timeout is added.

Remote actions, reusable workflows, job/services/Docker actions, permissions
and OIDC, protected environments, concurrency groups, job timeouts and job-level
continue-on-error are rejected pending their orchestration bridges. Standard
upload/download artifact actions, caches, generalized DAG/matrix scheduling,
source/event normalization and the composite public entry point remain planned.
The helper requires c2j's object outbox; an external `file://` reference would
not preserve its diagnostics after cleanup.

Run `make -C gha test-fast` for the container-free tests. See the
[harness README](../integration/README.md) for coverage. The extra direct act
race check currently exposes an [upstream PTY race](../../BUG_REPORT_ACT_HOST_EXECUTOR_RACE.md);
production qualification and cancellation/process-tree cleanup remain gated on
resolving upstream host-executor issues.
