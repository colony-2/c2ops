# gha-workflow

The internal orchestrator behind `gha` with `backend: c2j` can also be called
directly to submit an Actions workflow as c2j child recipes. The result contains `job_id`;
await it with `recipe.await_result` to obtain the final `status` and `jobs` map.
The invocation broker supplies submission authority. No direct JobDB credentials
or nested worker loop are used.

```yaml
sequence:
  - id: launch
    op: nix:github:colony-2/c2ops/published#gha-workflow
    inputs:
      workflow: .github/workflows/ci.yml
      runner_profiles:
        - labels: [ubuntu-24.04]
          execution_needs: {platform: linux/amd64}
        - labels: [ubuntu-24.04-arm]
          execution_needs: {platform: linux/arm64}
      secret_bindings:
        GITHUB_TOKEN: github_token
  - id: result
    op: recipe.await_result
    inputs:
      target: {job: '${{ sequence.launch.outputs.job_id }}'}
      wait: {on: job}
outputs:
  workflow: '${{ sequence.result.outputs.outputs }}'
```

`secret_bindings` contains c2j secret **names**, never resolved values. Each
executor resolves the named secrets through its secret-marked inputs. If the
caller restricts `perms`, grant `jobdb.jobs.submit: true` as well as the required
`jobdb.secrets.resolve` names.

Workflow jobs wait on their own dependencies. Matrix expressions are expanded
using completed dependency outputs; each combination gets an independent worker
and explicit execution needs. Set `strategy.fail-fast: false`; distributed
cancellation and `max-parallel` are not implemented. Conflicting matrix output
values fail aggregation instead of choosing a completion-order-dependent value.

The default event is `workflow_dispatch`. Use `event_name: push` and
`event: {ref: refs/heads/main}` for push filters. PR metadata requires `number` and `pull_request.base.ref` /
`pull_request.head.ref`; `action` defaults to `synchronize`. Path filters need
an explicit `changed_paths` list. Event selection does not create a GitHub PR or
change the committed source revision being executed.

The source revision is frozen from the prepared Git worktree and the workflow
hash is checked on every child. Workers retrieve the original repository base;
unpublished commits are transferred using c2j's native Git bundle artifacts.
`base_revision` defaults to `context.git.resolved_hash`. A preceding c2j step can
modify the source and let c2j persist it before submission. Uncommitted changes
inside the invoking worktree are rejected. Snapshot bundles larger than 64 MiB
must be published first, to fit the invocation broker's request limit.
Helper/executor selectors default to the published packages. `helper_op` and
`executor_op` support explicit development overrides.

Standard upload/download v4 actions use a private per-job artifact endpoint.
Completed uploads become `c2ops.gha.artifacts/v1` objects in JobDB. Downstream jobs
see the artifacts of their transitive dependencies; unrelated live jobs and
other workflow runs are outside this catalog. Use distinct artifact names for
matrix instances. Repeated names with different contents fail, incomplete
uploads are omitted, and inherited uploads cannot be overwritten. Durable
artifact references remain in the final result after worker cleanup.

Every generated child ID is derived from the tenant, parent job, c2j invocation
hash, and logical job or matrix index. Matrix assignments are canonically ordered
before assigning indices. Retrying the same invocation reuses those IDs and
waits on the existing submissions; a new invocation or parent job starts fresh
children. Broker session IDs and tokens are not part of the identity. Each
logical job waits only for its dependencies; an unrelated slow job does not block it. A failed process
or infrastructure task becomes a dependency failure, allowing `always()` jobs
to report. Matrix output collisions fail explicitly.

Awaited `jobs[logical_id].instances` retains one record per planned assignment,
in the same canonical order as the existing `diagnostics` and `artifacts`
arrays. Equal matrix coordinates remain separate records with distinct child
IDs. Singleton jobs use `matrix: {}`.

| Field | Meaning |
| --- | --- |
| `matrix` | Complete evaluated coordinates, preserving JSON types and expression-like strings. |
| `labels` | Evaluated requested `runs-on` labels. |
| `execution_needs` | Selected public profile requirements, including `base` or legacy image/platform requirements. These request placement; they do not prove observed worker placement. |
| `execution_job_id` | Actual deterministic execution child ID, independent of executor output and distinct from logical `job_id`. |
| `status` | Terminal child status observed by the soft wait, such as `completed`, `failed`, or `cancelled`. |
| `conclusion` | Authored executor conclusion (`success`, `failure`, `cancelled`, `skipped`), or null when none is available. |
| `diagnostics` | Complete existing typed `c2ops.gha.diagnostics/v1` reference, identical to the legacy array entry at this index, or explicit null. |
| `diagnostics_unavailable` | Null with a reference; otherwise `executor_not_completed` for a non-completed child, or `not_returned` for a completed child without a reference. |

Identity survives executor failure or pre-invocation cancellation when the
logical collector runs. The child status is separate from authored conclusion;
missing conclusions never inherit aggregate success. Readable invalid executor
conclusions remain invalid; malformed evidence may fail collection. Aggregate
success with success/skipped/success instances remains compatible with Actions
dependency semantics. Consumers requiring complete success must inspect every
instance and reject missing or invalid evidence.

A completed collector with zero assignments, including a condition-disabled
job, returns `instances: []`. Expansion, coordinator, or collector failure may
leave `instances` absent in the failure/cancellation fallback. Absence is
unavailable proof, not a completed empty plan. An unmatched trigger still yields
workflow `status: skipped` with an empty jobs map. The immediate submission
envelope, aggregate conclusions, string outputs, revision, workflow digest, and
ordered legacy reference arrays retain their contracts.

The executor supports trusted shell and local or SHA-pinned remote
JavaScript/composite steps. Its [compatibility limitations](../job/README.md) apply. VM provisioner
contracts and GitHub OIDC remain outside this implementation.

`gha` with `backend: c2j` rejects the local/GitHub-only `runner_image`,
`container_architecture`, `remote`, `continue_on_error`, and `timeout` settings.
Use `runner_profiles` for placement and inspect the collected conclusion.
`gha-many` retains its existing local/GitHub backends; a c2j recipe can submit
several `gha` steps and await each result without a batch-wide barrier.

Helper actions `expand`, `collect`, and `source` are internal recipe operations.
The `source` action seeds native snapshot forwarding before dependency waits;
`payload` holds opaque public GHA data so c2j does not interpret GHA templates.
Secrets are separate secret-marked executor inputs, never part of the payload.
For `collect`, the payload carries assignment identities; its internal
`assignments` output restores typed public data for native instance assembly.
Diagnostic references stay in native recipe outputs rather than passing through
the helper process.


A runner profile can use `execution_needs: {base: ubuntu-26.04-amd64}` (also
`ubuntu-26.04-arm64` and `macos-26-arm64` for the approved labels). A base must be
nonempty and cannot coexist with legacy `image` or `platform`. Legacy profiles
remain supported. Profiles select requirements, not workers. Runtime c2j must
support base admission; the embedded tests use c2j v0.0.118.

For private full-history preparation, select an existing binding alias:

```yaml
secret_bindings: {SOURCE: authorized_source_name}
source_credential: SOURCE
```

The alias is public; only the executor resolves its value, separately from the
encoded payload. Preparation runs before workflow steps. A later private-module
Git configuration cannot authorize it. Named HTTP(S) credentials use an ephemeral
host/path-scoped helper outside the repository; redirects and other helpers are
disabled for that fetch, and errors exclude transport stderr. No token URL/config
is persisted. Without a selector, an existing authorized scoped Git route may
serve the original repository. Token-bearing repository URLs are rejected.

The original registered cell is forwarded to newer brokers. Source repository
identity remains independent of event metadata. Full published history is fetched
on the worker, preserving the 64 MiB unpublished snapshot limit. This is bounded
frozen-source support, not general actions/checkout compatibility.
