# gha-workflow

Submit an Actions workflow as c2j child recipes. The result contains `job_id`;
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
`event: {ref: refs/heads/main}` for push filters. PR metadata requires `number`,
`action`, and `pull_request.base.ref` / `pull_request.head.ref`. Path filters need
an explicit `changed_paths` list. Event selection does not create a GitHub PR or
change the committed source revision being executed.

The source revision is frozen from the prepared Git worktree and the workflow
hash is checked on every child. Workers must be able to retrieve that revision.
Helper/executor selectors default to the published packages. `helper_op` and
`executor_op` support explicit development overrides.

The current executor supports trusted shell and local JavaScript/composite
steps. Its [compatibility limitations](../job/README.md) apply. VM provisioner
contracts and GitHub OIDC remain outside this implementation.
