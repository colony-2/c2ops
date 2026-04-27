# c2ops

Collection of c2j extension ops.

Every op in this repo reads one JSON object from stdin and writes one JSON object to stdout as `{"output": ...}`. For the op model itself, see [EXTENSION_OPS_GUIDE.md](./EXTENSION_OPS_GUIDE.md).

## Recipe Form

Local selector:

```yaml
sequence:
  - id: step
    op: ./llm
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Say hello.
```

Git selector:

```yaml
sequence:
  - id: step
    op: git+https://github.com/colony-2/c2ops.git//llm@main
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Say hello.
```

## Ops

| Op | Short description | Local selector | Recipe URI |
| --- | --- | --- | --- |
| `llm` | Legacy Go-backed one-shot LLM op, compatible with the original `llm_inference` shape. | `./llm` | `git+https://github.com/colony-2/c2ops.git//llm@main` |
| `llm2` | Legacy Go-backed richer LLM op with files, tools, and structured output, compatible with `llm_inference2`. | `./llm2` | `git+https://github.com/colony-2/c2ops.git//llm2@main` |
| `codex` | Go-backed Codex op with resumable sessions and outbox artifacts. | `./codex` | `git+https://github.com/colony-2/c2ops.git//codex@main` |
| `gha` | Go-backed op for running one GitHub Actions workflow. | `./gha` | `git+https://github.com/colony-2/c2ops.git//gha@main` |
| `gha-many` | Go-backed op for running multiple GitHub Actions workflows in one invocation. | `./gha-many` | `git+https://github.com/colony-2/c2ops.git//gha-many@main` |
| `pydantic` | Python-backed alternative to `llm2`, implemented with PydanticAI. | `./pydantic` | `git+https://github.com/colony-2/c2ops.git//pydantic@main` |
| `aider` | Python-backed alternative to `codex`, implemented with Aider behind a `codex`-like contract. | `./aider` | `git+https://github.com/colony-2/c2ops.git//aider@main` |
| `litellm` | Python-backed alternative to `llm`, implemented with LiteLLM. | `./litellm` | `git+https://github.com/colony-2/c2ops.git//litellm@main` |

## Short Usage

### `llm`

Use for simple prompt-in / response-out calls.

```yaml
sequence:
  - id: ask
    op: git+https://github.com/colony-2/c2ops.git//llm@main
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Give me a one-sentence summary of this repo.
```

### `llm2`

Use for richer LLM flows that need files, tools, or structured output.

```yaml
sequence:
  - id: plan
    op: git+https://github.com/colony-2/c2ops.git//llm2@main
    inputs:
      default_provider: openai
      default_model: gpt-4.1
      prompt: Review these files and propose a patch plan.
```

### `codex`

Use for Codex-driven coding loops with resumable sessions.

```yaml
sequence:
  - id: code_task
    op: git+https://github.com/colony-2/c2ops.git//codex@main
    inputs:
      prompt: Fix the failing tests in this repository.
      worktree_path: "{{ context.environment.worktree_path }}"
      artifact_outbox_path: "{{ context.environment.outbox }}"
```

### `gha`

Use for a single workflow run.

```yaml
sequence:
  - id: ci
    op: git+https://github.com/colony-2/c2ops.git//gha@main
    inputs:
      workflow: .github/workflows/ci.yml
```

### `gha-many`

Use when you want to run several workflows and collect the results together.

```yaml
sequence:
  - id: ci_matrix
    op: git+https://github.com/colony-2/c2ops.git//gha-many@main
    inputs:
      workflows:
        - workflow: .github/workflows/ci.yml
        - workflow: .github/workflows/lint.yml
```

### `pydantic`

Use when you want an `llm2`-style interface on top of PydanticAI.

```yaml
sequence:
  - id: plan
    op: git+https://github.com/colony-2/c2ops.git//pydantic@main
    inputs:
      default_provider: openai
      default_model: gpt-4.1
      prompt: Review these files and propose a patch plan.
```

### `aider`

Use when you want an Aider-backed coding op with `codex`-style inputs and outputs.

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

### `litellm`

Use when you want an `llm`-style interface on top of LiteLLM.

```yaml
sequence:
  - id: ask
    op: git+https://github.com/colony-2/c2ops.git//litellm@main
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Give me a one-sentence summary of this repo.
```

## Per-Op Guides

- [`llm/README.md`](./llm/README.md)
- [`llm2/README.md`](./llm2/README.md)
- [`codex/README.md`](./codex/README.md)
- [`gha/README.md`](./gha/README.md)
- [`gha-many/README.md`](./gha-many/README.md)
- [`pydantic/README.md`](./pydantic/README.md)
- [`aider/README.md`](./aider/README.md)
- [`litellm/README.md`](./litellm/README.md)

## Notes

- `llm`, `llm2`, `codex`, `gha`, and `gha-many` are Go-backed ops, each housed as a standalone Go project inside its own top-level op directory.
- `pydantic`, `aider`, and `litellm` are Python-backed ops that run through `uv run --script`.
