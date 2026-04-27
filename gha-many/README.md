# `gha-many`

Go-backed op for running multiple GitHub Actions workflows in one invocation.

## Selector

Use this op from the repo root as:

```yaml
op: ./gha-many
```

Git selector example:

```yaml
op: git+https://github.com/colony-2/c2ops.git//gha-many@main
```

## What It Does

- Reads one JSON payload from stdin
- Runs the Go implementation in `cmd/gha-runs-op`
- Returns aggregated workflow results in the extension-op `{"output": ...}` envelope

## Inputs

- `workflows`
- `timeout`
- `continue_on_error`
- `git_context`

## Recipe

```yaml
sequence:
  - id: ci_matrix
    op: git+https://github.com/colony-2/c2ops.git//gha-many@main
    inputs:
      workflows:
        - workflow: .github/workflows/ci.yml
        - workflow: .github/workflows/lint.yml
```

## Outputs

- `status`
- `all_passed`
- `error_message`
- `results`
