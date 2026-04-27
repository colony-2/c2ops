# c2ops

Collection of extension ops for c2j-style workflows.

Each op follows the same basic contract:

- read one JSON input object from stdin
- execute the op implementation
- write one JSON output object to stdout as `{"output": ...}`

For the underlying op model and selector behavior, see [EXTENSION_OPS_GUIDE.md](./EXTENSION_OPS_GUIDE.md).

## Using an Op in a Recipe

Local selector:

```yaml
sequence:
  - id: step
    op: ./litellm
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Say hello.
```

Git selector:

```yaml
sequence:
  - id: step
    op: git+https://github.com/colony-2/c2ops.git//litellm@main
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Say hello.
```

## Op Catalog

| Op name | What it is for | Local selector | Recipe URI | Typical inputs |
| --- | --- | --- | --- | --- |
| `llm_inference` | Legacy Go op for one-shot LLM calls with optional structured output. | `./pkg/llm/extension-llm-inference` | `git+https://github.com/colony-2/c2ops.git//pkg/llm/extension-llm-inference@main` | `provider`, `model`, `prompt`, `system_prompt`, `response_schema` |
| `llm_inference2` | Legacy Go op for richer LLM flows with files, tools, sandbox options, and structured output. | `./pkg/llm/extension-llm-inference2` | `git+https://github.com/colony-2/c2ops.git//pkg/llm/extension-llm-inference2@main` | `default_provider`, `default_model`, `prompt`, `files`, `tools`, `execute_tools`, `response_schema` |
| `gha.run` | Run one GitHub Actions workflow from an op. | `./pkg/gha/extension-gha-run` | `git+https://github.com/colony-2/c2ops.git//pkg/gha/extension-gha-run@main` | `workflow`, `with`, `env`, `secrets`, `backend`, `remote` |
| `gha.runs` | Run multiple GitHub Actions workflows in one op invocation. | `./pkg/gha/extension-gha-runs` | `git+https://github.com/colony-2/c2ops.git//pkg/gha/extension-gha-runs@main` | `workflows`, `timeout`, `continue_on_error`, `git_context` |
| `codex.exec` | Run Codex through the selector-backed op interface with resumable sessions and outbox artifacts. | `./pkg/codex/extension-codex-exec` | `git+https://github.com/colony-2/c2ops.git//pkg/codex/extension-codex-exec@main` | `prompt`, `sessionId`, `model`, `env`, `worktree_path`, `artifact_outbox_path` |
| `llm_inference_litellm` | Python op that keeps the `llm_inference` shape but routes requests through LiteLLM. | `./litellm` | `git+https://github.com/colony-2/c2ops.git//litellm@main` | `provider`, `model`, `prompt`, `system_prompt`, `response_schema` |
| `llm_inference2_pydantic` | Python op that keeps the `llm_inference2` shape but routes requests through PydanticAI. | `./pydantic` | `git+https://github.com/colony-2/c2ops.git//pydantic@main` | `default_provider`, `default_model`, `prompt`, `files`, `tools`, `execute_tools`, `response_schema` |
| `aider.exec` | Python op that exposes Aider behind a `codex.exec`-like contract. | `./aider` | `git+https://github.com/colony-2/c2ops.git//aider@main` | `prompt`, `sessionId`, `model`, `env`, `worktree_path`, `artifact_outbox_path` |

## Short Usage Notes

### `llm_inference`

Use for simple prompt-in / response-out model calls.

```yaml
sequence:
  - id: ask
    op: git+https://github.com/colony-2/c2ops.git//pkg/llm/extension-llm-inference@main
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Give me a one-sentence summary of this repo.
```

### `llm_inference2`

Use for richer LLM requests that need file context, tool definitions, or tool execution.

```yaml
sequence:
  - id: plan
    op: git+https://github.com/colony-2/c2ops.git//pkg/llm/extension-llm-inference2@main
    inputs:
      default_provider: openai
      default_model: gpt-4.1
      prompt: Review these files and propose a patch plan.
      files:
        - path: README.md
          type: markdown
          content: "${{ files.readme_b64 }}"
```

### `gha.run`

Use for one workflow dispatch.

```yaml
sequence:
  - id: ci
    op: git+https://github.com/colony-2/c2ops.git//pkg/gha/extension-gha-run@main
    inputs:
      workflow: .github/workflows/ci.yml
```

### `gha.runs`

Use when you want to run several workflows and collect results together.

```yaml
sequence:
  - id: ci_matrix
    op: git+https://github.com/colony-2/c2ops.git//pkg/gha/extension-gha-runs@main
    inputs:
      workflows:
        - workflow: .github/workflows/ci.yml
        - workflow: .github/workflows/lint.yml
```

### `codex.exec`

Use for Codex-driven coding loops with resumable sessions.

```yaml
sequence:
  - id: code_task
    op: git+https://github.com/colony-2/c2ops.git//pkg/codex/extension-codex-exec@main
    inputs:
      prompt: Fix the failing tests in this repository.
      worktree_path: "{{ context.environment.worktree_path }}"
      artifact_outbox_path: "{{ context.environment.outbox }}"
```

### `llm_inference_litellm`

Use when you want the `llm_inference` interface on top of LiteLLM and its provider abstraction.

```yaml
sequence:
  - id: ask
    op: git+https://github.com/colony-2/c2ops.git//litellm@main
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Give me a one-sentence summary of this repo.
```

### `llm_inference2_pydantic`

Use when you want the `llm_inference2` interface on top of PydanticAI.

```yaml
sequence:
  - id: plan
    op: git+https://github.com/colony-2/c2ops.git//pydantic@main
    inputs:
      default_provider: openai
      default_model: gpt-4.1
      prompt: Review these files and propose a patch plan.
```

### `aider.exec`

Use when you want an Aider-backed coding op with `codex.exec`-style inputs and outputs.

```yaml
sequence:
  - id: code_task
    op: git+https://github.com/colony-2/c2ops.git//aider@main
    inputs:
      prompt: Fix the failing tests in this repository.
      model: sonnet
      env:
        ANTHROPIC_API_KEY: "${{ secrets.anthropic_api_key }}"
      worktree_path: "{{ context.environment.worktree_path }}"
```

## Implementation Notes

- The legacy ops under `pkg/...` are Go-backed.
- The `litellm`, `pydantic`, and `aider` selectors are Python-backed and run with `uv run --script`.
- The Python ops have per-op guides in:
  - [`litellm/README.md`](./litellm/README.md)
  - [`pydantic/README.md`](./pydantic/README.md)
  - [`aider/README.md`](./aider/README.md)
