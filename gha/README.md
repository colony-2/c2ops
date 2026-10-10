# `gha`

Go-backed op for running one GitHub Actions workflow.

## Selector

Use this op from the repo root as:

```yaml
op: ./gha
```

Nix package selector:

```yaml
op: nix:github:colony-2/c2ops/main#gha
```

## What It Does

- Reads one JSON payload from stdin
- Runs a prebuilt Go executable from the Nix package (local source selectors use `go run .`)
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
    op: nix:github:colony-2/c2ops/main#gha
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
