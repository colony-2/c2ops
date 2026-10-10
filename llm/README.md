# `llm`

Legacy Go-backed op for the original `llm_inference` interface.

## Selector

Use this op from the repo root as:

```yaml
op: ./llm
```

Nix package selector (replace `<commit>` with a published CI revision):

```yaml
op: nix:github:colony-2/c2ops/<commit>#llm
```

## What It Does

- Reads one JSON payload from stdin
- Runs a prebuilt Go executable from the Nix package (local source selectors use `go run .`)
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
    op: nix:github:colony-2/c2ops/<commit>#llm
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
