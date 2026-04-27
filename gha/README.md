# `gha`

Go-backed op for running one GitHub Actions workflow.

## Selector

Use this op from the repo root as:

```yaml
op: ./gha
```

Git selector example:

```yaml
op: git+https://github.com/colony-2/c2ops.git//gha@main
```

## What It Does

- Reads one JSON payload from stdin
- Runs the Go implementation in `cmd/gha-run-op`
- Returns workflow execution details in the extension-op `{"output": ...}` envelope

## Inputs

- `workflow`
- `with`
- `env`
- `secrets`
- `backend`
- `runner_image`
- `container_architecture`
- `timeout`
- `continue_on_error`
- `remote`
- `git_context`

## Recipe

```yaml
sequence:
  - id: ci
    op: git+https://github.com/colony-2/c2ops.git//gha@main
    inputs:
      workflow: .github/workflows/ci.yml
```

## Outputs

- `status`
- `exit_code`
- `duration_seconds`
- `error_message`
- `workflow`
- `jobs`
