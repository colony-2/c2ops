# `gha`

Go-backed op for running one GitHub Actions workflow.

The default `backend: c2j` submits each Actions job as independently scheduled c2j work,
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
Each job has its conclusion, string outputs, durable diagnostics/artifact
references, and an ordered `instances` array with one record per planned
assignment, including singleton jobs (`matrix: {}`). Each record contains the
complete typed `matrix`, evaluated `labels`, selected `execution_needs`, actual
`execution_job_id`, terminal child `status`, nullable executor `conclusion`,
typed `diagnostics` reference or null, and `diagnostics_unavailable`.
Execution needs describe placement requests, not observed worker placement.

The diagnostic reason is null with a reference, `executor_not_completed` for a
non-completed child, or `not_returned` for a completed child without a reference.
Missing executor conclusions remain null; they never inherit aggregate success.
An aggregate can still succeed with skipped instances. Verification requiring
complete success must check individual membership and outcomes.

A completed zero-assignment collector returns `instances: []`. Failed expansion
or collection may leave `instances` absent, meaning unavailable proof. An
unmatched trigger returns workflow `status: skipped` and an empty jobs map.
These fields extend the awaited result; the immediate submission is unchanged.
A failed workflow is collected as data: check `status` before
performing a release or another dependent operation.

See [workflow semantics](workflow/README.md), the
[executor compatibility contract](job/README.md), and the
[container-free test harness](integration/README.md). This backend remains
experimental pending native worker qualification; the host-executor correction is consumed from the immutable private fork recorded in [the source pin](../nix/act-source.json). Source builders must follow the [private-prefetch instructions](../NIX_PACKAGES.md#private-gha-build-input).

## Execution negotiation

With c2j v0.0.120 or a compatible newer runtime, set `execution_mode: negotiate`
to resolve `runs-on` through the built-in catalog. Each job or matrix assignment
checks `C2J_EXECUTION_ALLOCATION` before execution. An insufficient allocation
returns an exit-75 handoff; c2j resumes the same task on a suitable worker.

```yaml
sequence:
  - id: ci
    op: nix:github:colony-2/c2ops/published#gha
    inputs:
      backend: c2j
      execution_mode: negotiate
      workflow: .github/workflows/ci.yml
  - id: result
    op: recipe.await_result
    inputs:
      target: {job: '${{ sequence.ci.outputs.job_id }}'}
      wait: {on: job}
```

Catalog `2026-11-06.1` maps Ubuntu 24.04/26.04 AMD64 and ARM64 labels to
`ubuntu-26.04-amd64` and `ubuntu-26.04-arm64`. ARM labels use the `-arm` suffix.
Ubuntu `latest` aliases are pinned to this catalog. macOS 14/15 ARM64 map to
`macos-sequoia-15.7.7-arm64`. These are compatibility substitutions, not copies
of GitHub's complete software images. Diagnostics record the authored labels,
selected requirements, and mapping source.

Ubuntu larger-runner convenience labels add `-Ncore`, for example
`ubuntu-24.04-16core` or `ubuntu-26.04-arm-64core`:

| CPU | AMD64 memory | ARM64 memory |
| --- | --- | --- |
| 2 | 8G | 8G |
| 4 | 16G | 16G |
| 8 | 32G | 32G |
| 16 | 64G | 64G |
| 32 | 128G | 128G |
| 64 | 256G | 208G |
| 96 | 384G | unsupported |

These convenience names are local conventions; GitHub organizations can assign
other meanings to custom names. Standard Ubuntu defaults to 4 CPU / 16Gi;
standard macOS to 3 CPU / 8Gi. `macos-14-xlarge`, `macos-15-xlarge`, and the
separately pinned `macos-latest-xlarge` request 5 CPU / 14G on Sequoia. `G` is
decimal and `Gi` is binary. CPU/memory matching does not promise a particular
chip, GPU, SDK, or SSD size.

Windows, Intel macOS (including `-large`), newer macOS releases, GPU types,
Ubuntu 22.04, and unknown labels have no default mapping. Standard `macos-latest`
denotes a newer OS and is unsupported by this catalog. Profiles can explicitly
override release choices while preserving known OS/architecture constraints:

```yaml
runner_profiles:
  - labels: [my-large-builder]
    runner_type: {image: ubuntu-26.04, architecture: arm64, size: 64core}
  - labels: [macos-latest]
    execution_required:
      base: macos-sequoia-15.7.7-arm64
      resources: {cpu: "8", memory: "32Gi", ephemeral-storage: "40Gi"}
      capabilities: [network]
```

A matching profile replaces the catalog mapping in full; ambiguous matches fail.
Use exactly one of `runner_type` or `execution_required` per negotiated profile.
Multi-label selectors need an explicit profile. Labels-only `runs-on` objects
are supported; runner groups remain unsupported.

Requirements accept only `base`, `resources`, and `capabilities`. The allocation's
`packages` array is informational prepared-dependency metadata. Declare software
dependencies through recipes or extension manifests. Missing allocation metadata
or an unsupported schema fails before execution. Skipped jobs need no allocation.

The default `execution_mode: static` preserves the existing required
`runner_profiles[].execution_needs` configuration. Mode and profile forms must
agree. Local/GitHub backends do not accept `execution_mode`.

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
- `backend` (`c2j` by default; `local` and `github` are explicit alternatives)
- For `c2j`: `runner_profiles`, `secret_bindings`, optional `source_credential`, `event_name`, `event`,
  `changed_paths`, `vars`; optional `helper_op` and `executor_op` overrides
- `runner_image`
- `container_architecture`
- `timeout` (optional Go duration; omitted or zero means no op-imposed limit)
- `continue_on_error`
- `remote`
- `git_context`

For `backend: c2j`, omitted or exactly empty worker selectors use the submitting
package's clean release revision. Selecting `nix:github:colony-2/c2ops/<revision>#gha`
automatically selects `gha-workflow` and `gha-job` at that revision, independently
of the workflow checkout. Each nonempty explicit override is preserved verbatim;
mutable and mixed-release overrides remain supported. The resolved pair is stored
in submitted children and reused by expansion and collection.

Raw Go, dirty Git and `path:` builds lack release metadata and require both worker
selectors explicitly. A partial override still requires metadata for the other
worker; missing metadata fails before any child submission. Local/Git manifests
invoke a packaged binary, whose release determines the defaults. A clean local
Git build names the canonical repository; a fork-only revision must be available
there or ordinary package preparation fails without fallback.

## Recipe

The default backend submits asynchronous c2j work. Use catalog-based negotiation
and await the returned job ID:

```yaml
sequence:
  - id: ci
    op: nix:github:colony-2/c2ops/published#gha
    inputs:
      workflow: .github/workflows/ci.yml
      execution_mode: negotiate
  - id: result
    op: recipe.await_result
    inputs:
      target: {job: '${{ sequence.ci.outputs.job_id }}'}
      wait: {on: job}
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
