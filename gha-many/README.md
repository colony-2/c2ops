# `gha-many`

Go-backed op for running multiple GitHub Actions workflows in one invocation.

## Selector

Use this op from the repo root as:

```yaml
op: ./gha-many
```

Nix package selector (replace `<commit>` with a published CI revision):

```yaml
op: nix:github:colony-2/c2ops/<commit>#gha-many
```

## What It Does

- Reads one JSON payload from stdin
- Runs a prebuilt Go executable from the Nix package (local source selectors use `go run .`)
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
    op: nix:github:colony-2/c2ops/<commit>#gha-many
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
