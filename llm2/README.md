# `llm2`

Legacy Go-backed op for the enhanced `llm_inference2` interface.

## Selector

Use this op from the repo root as:

```yaml
op: ./llm2
```

Git selector example:

```yaml
op: git+https://github.com/colony-2/c2ops.git//llm2@main
```

## What It Does

- Reads one JSON payload from stdin
- Runs the Go implementation in `cmd/llm-inference2-op`
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
    op: git+https://github.com/colony-2/c2ops.git//llm2@main
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
