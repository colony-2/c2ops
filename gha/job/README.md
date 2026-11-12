# `gha-job` (experimental)

Runs one externally scheduled GitHub Actions job using act's exported Go API.
The [API audit](ACT_API_AUDIT.md) compares this adapter with act's own runner and
records the stock-CLI reproduction of the host-executor race.
The [`gha` c2j backend](../README.md) generates calls to this executor with
explicit matrix assignments, dependency results, artifacts, and execution needs.
It can also be called directly as shown below.

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

Supported execution is trusted shell steps and local or full-SHA-pinned remote
JavaScript/composite actions in act host mode. Nested remote action references
must also use full commit SHAs. Docker and symlinked remote action manifests fail. A built-in local checkout preparation step copies the
prepared source into act's disposable workspace without downloading checkout.
Authored checkout steps accept no options or exactly literal `fetch-depth: 0`.
All other options/depths/expressions fail before commands run. Full history
requires `repository` (the authorized source identity/transport) and optionally
`source_credential`, a resolved secret alias from explicit bindings. The workflow
helper forwards these fields and the expected `revision` automatically. Preparation
copies candidate objects and local refs into owned storage, fetches advertised
published branches/tags, verifies completeness and preserves HEAD/tree. It never
asks the original remote to supply the unpublished candidate. Tag conflicts,
unavailable history and shallow/incomplete objects fail before authored steps.
The input checkout is unchanged. No-option checkout adds no fetch prerequisite.
Planning and execution preserve the input checkout's repository context before
history preparation removes its remote. This keeps `github.repository`, its
owner and the corresponding environment variables consistent for PR conditions;
an explicit `env.GITHUB_REPOSITORY` retains precedence. Only repository metadata
is forwarded, with URL credentials, queries and fragments removed. The prepared
history repository retains no origin or preparation credential configuration.
Every invocation gets a separate workspace, HOME and temporary/cache directory.
Ambient credential environment variables are blanked; bind secrets explicitly.

`runs-on` labels do **not** provision a runner here: all labels execute in the
assigned host environment. The calling c2j recipe/provisioner must map them to
matching `execution_needs`. Host mode is not an OS security boundary. It is meant
to execute inside an appropriately isolated worker.

The output has `job_id`, `conclusion`, string `outputs`, and a small `diagnostics`
object reference (`c2ops.gha.diagnostics/v1`), plus `artifacts` references
(`c2ops.gha.artifacts/v1`). `artifact_inputs` accepts hydrated dependency artifact
objects for the private v4 upload/download service. The diagnostics object's `log` file
is backed by JobDB and survives worker deletion. Logs are also streamed to
stderr. Secret-bearing job outputs are suppressed; logs mask named secrets and
dynamic masks, including multiline values. Skipped jobs have empty outputs.
Shell failure is returned as `conclusion: failure` with a successful op envelope
so a parent can schedule `always()` reporting. Invalid input and infrastructure
errors fail the op. No implicit execution timeout is added.

Reusable workflows, job/services/Docker actions, workflow/job `permissions`,
OIDC, protected environments, concurrency groups, job timeouts and job-level
continue-on-error are rejected. Distributed matrices require `fail-fast: false`;
`max-parallel` and sibling cancellation are not implemented. This initial backend
does not implement Actions caches, cross-run artifact downloads or live discovery
of unrelated jobs' artifacts. Run ordinary Docker build commands only on workers
whose explicit profile supplies a usable isolated Docker daemon.

Artifact compatibility is tested with `actions/upload-artifact` v4 at
`ea165f8d65b6e75b540449e92b4886f43607fa02` and `actions/download-artifact` v4 at
`d3f86a106a0bac45b974a628896c90dbdf5c8093`. Pin these complete SHAs in `uses`.
Artifact names must be unique across producing jobs; immutable dependency
snapshots cannot be overwritten. The c2j object outbox preserves logs and
uploads after cleanup. Artifact download URLs returned by the Actions protocol
are local and temporary; use the durable object references in the op result.

Run `make -C gha test-fast` for the container-free tests. See the
[harness README](../integration/README.md) for coverage. The [immutable private act dependency](../../nix/act-source.json) fixes the demonstrated
PTY race, drains failure/cancellation output, and cleans the command process group
on Linux/macOS. Run `go test -race -count=1 ./internal/jobexec` from gha. Descendants
that escape that group still need the provisioner's boundary; native placement
and outer isolation qualification remain pending.
