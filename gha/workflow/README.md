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

Every generated child ID is derived from the c2j invocation and logical job or
matrix index. Retrying submission reuses those IDs. Each logical job waits only
for its dependencies; an unrelated slow job does not block it. A failed process
or infrastructure task becomes a dependency failure, allowing `always()` jobs
to report. Matrix output collisions fail explicitly.

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
