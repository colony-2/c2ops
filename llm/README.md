# `llm`

Legacy Go-backed op for the original `llm_inference` interface.

## Selector

Use this op from the repo root as:

```yaml
op: ./llm
```

Git selector example:

```yaml
op: git+https://github.com/colony-2/c2ops.git//llm@main
```

## What It Does

- Reads one JSON payload from stdin
- Runs the standalone Go project in this directory with `go run .`
- Returns JSON on stdout in the extension-op `{"output": ...}` envelope

## Inputs

- `provider`
- `model`
- `prompt`
- `system_prompt`
- `temperature`, `max_tokens`, `top_p`, `stop_sequences`
- `response_schema`

## Recipe

```yaml
sequence:
  - id: ask
    op: git+https://github.com/colony-2/c2ops.git//llm@main
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Give me a one-sentence summary of Go interfaces.
```

## Outputs

- `response`
- `model`
- `finish_reason`
- `usage`
