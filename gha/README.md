# `gha`

Go-backed op for running one GitHub Actions workflow.

`backend: c2j` submits each Actions job as independently scheduled c2j work,
using act to evaluate and execute that job. It returns a result-job ID immediately;
use a subsequent `recipe.await_result` to wait durably. The local and GitHub
backends retain their existing synchronous behavior.

```yaml
sequence:
  - id: ci
    op: nix:github:colony-2/c2ops/published#gha
    inputs:
      backend: c2j
      workflow: .github/workflows/ci.yml
      runner_profiles:
        - labels: [ubuntu-24.04]
          execution_needs: {platform: linux/amd64}
        - labels: [ubuntu-24.04-arm]
          execution_needs: {platform: linux/arm64}
      secret_bindings:
        GITHUB_TOKEN: github_token
      event_name: pull_request
      event:
        number: 42
        action: synchronize
        pull_request:
          base: {ref: main}
          head: {ref: feature/example}
  - id: result
    op: recipe.await_result
    inputs:
      target: {job: '${{ sequence.ci.outputs.job_id }}'}
      wait: {on: job}
outputs:
  workflow: '${{ sequence.result.outputs.outputs }}'
```

The PR metadata selects trigger filters and job conditions; it does not create a
GitHub PR or select different source code. Omit `event_name` and `event` for
`workflow_dispatch`. For a main push, use `event_name: push` and
`event: {ref: refs/heads/main}`. Path filters require `changed_paths`.

`secret_bindings` maps GHA names to c2j secret names. Each executor resolves them
at invocation; pass public configuration through `vars`, `env`, and `with`.
Explicit caller permissions must include `jobdb.jobs.submit: true` and the
necessary `jobdb.secrets.resolve` names. No GitHub token is created automatically.

The submitted output contains `status: submitted`, `job_id`, `goal_id`,
`tenant_id`, and `submitted_jobs`. The awaited output contains `status`
(`success`, `failure`, or `skipped`), `revision`, `workflow_sha256`, and `jobs`.
Each job has its conclusion, string outputs, and durable diagnostics/artifact
references. A failed workflow is collected as data: check `status` before
performing a release or another dependent operation.

See [workflow semantics](workflow/README.md), the
[executor compatibility contract](job/README.md), and the
[container-free test harness](integration/README.md). This backend remains
experimental while the upstream act host-runner race is unresolved.

## Selector

Use this op from the repo root as:

```yaml
op: ./gha
```

Nix package selector:

```yaml
op: nix:github:colony-2/c2ops/published#gha
```

## What It Does

- Reads one JSON payload from stdin
- Runs a prebuilt Go executable from the Nix package, including through local/Git manifests; Go is not required on workers
- Returns workflow execution details in the extension-op `{"output": ...}` envelope

## Inputs

- `workflow`
- `with`
- `env`
- `secrets`
- `backend` (`local`, `github`, or `c2j`)
- For `c2j`: `runner_profiles`, `secret_bindings`, `event_name`, `event`,
  `changed_paths`, `vars`; optional `helper_op` and `executor_op` development overrides
- `runner_image`
- `container_architecture`
- `timeout` (optional Go duration; omitted or zero means no op-imposed limit)
- `continue_on_error`
- `remote`
- `git_context`

## Recipe

```yaml
sequence:
  - id: ci
    op: nix:github:colony-2/c2ops/published#gha
    inputs:
      workflow: .github/workflows/ci.yml
```

## Outputs

The following are the existing local/GitHub outputs. The asynchronous c2j
submission and awaited outputs are described above.

- `status`
- `exit_code`
- `duration_seconds`
- `error_message`
- `workflow`
- `jobs`
