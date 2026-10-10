# `llm2`

Legacy Go-backed op for the enhanced `llm_inference2` interface.

## Selector

Use this op from the repo root as:

```yaml
op: ./llm2
```

Nix package selector (replace `<commit>` with a published CI revision):

```yaml
op: nix:github:colony-2/c2ops/<commit>#llm2
```

## What It Does

- Reads one JSON payload from stdin
- Runs a prebuilt Go executable from the Nix package (local source selectors use `go run .`)
- Returns JSON on stdout in the extension-op `{"output": ...}` envelope

## Inputs

- `default_provider`
- `default_model`
- `prompt`
- `system_prompt`
- `api_keys`
- `files`
- `tools`
- `execute_tools`
- `response_schema`
- sandbox and working-directory fields

## Recipe

```yaml
sequence:
  - id: plan
    op: nix:github:colony-2/c2ops/<commit>#llm2
    inputs:
      default_provider: openai
      default_model: gpt-4.1
      prompt: Review these files and propose a patch plan.
```

## Outputs

- `response`
- `model`
- `finish_reason`
- `usage`
- `tool_calls`
- `tool_results`
- `files_written`
- `files_read`
- `files_deleted`
