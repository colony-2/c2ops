# `litellm`

LiteLLM-backed replacement-style op for the existing `llm_inference` interface.

## Selector

Use this op from the repo root as:

```yaml
op: ./litellm
```

Nix package selector (replace `<commit>` with a published CI revision):

```yaml
op: nix:github:colony-2/c2ops/<commit>#litellm
```

## What It Does

- Reads one JSON payload from stdin
- Runs with a packaged Python interpreter and locked dependencies
- Shares the direct dependency pins in [main.py](./main.py); Nix locks all transitive dependencies
- Returns JSON on stdout using the extension-op `{"output": ...}` envelope

## Requirements

- A c2j worker configured for the [`colony2` cache](../NIX_PACKAGES.md); Python and libraries are packaged
- A provider API key available to LiteLLM through the normal environment for your chosen provider

Common cases:

- OpenAI: `OPENAI_API_KEY`
- Anthropic: `ANTHROPIC_API_KEY`
- Gemini: `GEMINI_API_KEY`

## Inputs

This op follows the `llm_inference` shape:

- `provider`: provider name such as `openai`, `anthropic`, or `gemini`
- `model`: model name
- `prompt`: user prompt
- `system_prompt`: optional system prompt
- `temperature`, `max_tokens`, `top_p`, `stop_sequences`
- `response_schema`: optional JSON schema for structured output

If `model` is already fully qualified, it is passed through unchanged. Otherwise the script builds `provider/model`.

## Example

```yaml
sequence:
  - id: ask_model
    op: nix:github:colony-2/c2ops/<commit>#litellm
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Give me a one sentence summary of Go interfaces.
```

Git-backed recipe example:

```yaml
sequence:
  - id: ask_model
    op: nix:github:colony-2/c2ops/<commit>#litellm
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Give me a one sentence summary of Go interfaces.
```

Structured output example:

```yaml
sequence:
  - id: ask_model
    op: nix:github:colony-2/c2ops/<commit>#litellm
    inputs:
      provider: anthropic
      model: claude-sonnet-4-5
      prompt: Return a deployment summary.
      response_schema:
        type: object
        properties:
          summary:
            type: string
          risk:
            type: string
        required: [summary, risk]
```

## Outputs

- `response`
- `model`
- `finish_reason`
- `usage`

## Notes

- This is intentionally thin. It does not add tool execution or file handling.
- For structured output, the script first tries LiteLLM’s schema hinting and then falls back to prompt-only schema instructions if needed.
