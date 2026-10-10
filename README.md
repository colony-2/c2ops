# c2ops

Collection of c2j extension ops.

Every op in this repo reads one JSON object from stdin and writes one JSON object to stdout as `{"output": ...}`. Ops are packaged for `x86_64-linux` and `aarch64-linux`, with CI configured to publish to the **colony2** Cachix cache. See [package setup and publishing](./NIX_PACKAGES.md) and the [extension contract](./EXTENSION_OPS.md).

## Recipe Form

Use an explicit package selector, pinned to a commit whose CI build and Cachix
upload have completed. Replace `<commit>` in these examples with that revision:

```yaml
sequence:
  - id: step
    op: nix:github:colony-2/c2ops/<commit>#llm
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Say hello.
```

Local source selector for development (requires a Go toolchain):

```yaml
sequence:
  - id: step
    op: ./llm
    inputs:
      provider: openai
      model: gpt-4.1-mini
      prompt: Say hello.
```

## Ops

| Op | Short description | Local selector | Nix package selector |
| --- | --- | --- | --- |
| `llm` | Legacy Go-backed one-shot LLM op, compatible with the original `llm_inference` shape. | `./llm` | `nix:github:colony-2/c2ops/<commit>#llm` |
| `llm2` | Legacy Go-backed richer LLM op with files, tools, and structured output, compatible with `llm_inference2`. | `./llm2` | `nix:github:colony-2/c2ops/<commit>#llm2` |
| `codex` | Go-backed Codex op with resumable sessions and outbox artifacts. | `./codex` | `nix:github:colony-2/c2ops/<commit>#codex` |
| `skill.run` | Codex-backed op that runs one requested skill and validates its structured output artifact. | `./codex/run_skill` | `nix:github:colony-2/c2ops/<commit>#skill-run` |
| `gha` | Go-backed op for running one GitHub Actions workflow. | `./gha` | `nix:github:colony-2/c2ops/<commit>#gha` |
| `gha-many` | Go-backed op for running multiple GitHub Actions workflows in one invocation. | `./gha-many` | `nix:github:colony-2/c2ops/<commit>#gha-many` |
| `pydantic` | Python-backed alternative to `llm2`, implemented with PydanticAI. | `./pydantic` | `nix:github:colony-2/c2ops/<commit>#pydantic` |
| `aider` | Python-backed alternative to `codex`, implemented with Aider behind a `codex`-like contract. | `./aider` | `nix:github:colony-2/c2ops/<commit>#aider` |
| `litellm` | Python-backed alternative to `llm`, implemented with LiteLLM. | `./litellm` | `nix:github:colony-2/c2ops/<commit>#litellm` |
| `kimi` | Kimi Code CLI with resumable sessions and execution artifacts. | `./kimi` | `nix:github:colony-2/c2ops/<commit>#kimi` |
| `jev` | TypeSafe System One evaluation with typed questions and probabilities. | `./jev` | `nix:github:colony-2/c2ops/<commit>#jev` |
| `rule_gate` | Deterministic recipe policy checks. | `./rule_gate` | `nix:github:colony-2/c2ops/<commit>#rule_gate` |

The manifest name `skill.run` is packaged under the flake attribute `skill-run`.
Use selectors directly in `op:`; bare names such as `codex` or `skill.run` do not
identify this repository or declare an installation dependency.

## Short Usage

### `llm`

Use for simple prompt-in / response-out calls.

```yaml
sequence:
  - id: ask
    op: nix:github:colony-2/c2ops/<commit>#llm
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
    op: nix:github:colony-2/c2ops/<commit>#llm2
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
    op: nix:github:colony-2/c2ops/<commit>#codex
    inputs:
      prompt: Fix the failing tests in this repository.
      worktree_path: "{{ context.environment.op.worktree_path }}"
      artifact_outbox_path: "{{ context.environment.op.outbox }}"
```

### `skill.run`

Use when a recipe should run one specific Codex skill and validate a JSON output
artifact.

```yaml
sequence:
  - id: intake
    op: nix:github:colony-2/c2ops/<commit>#skill-run
    inputs:
      skill: c2-ticket-intake
      output:
        path: ticket/result.json
        schema:
          type: object
          required: [summary]
```

### `gha`

Use for a single workflow run.

```yaml
sequence:
  - id: ci
    op: nix:github:colony-2/c2ops/<commit>#gha
    inputs:
      workflow: .github/workflows/ci.yml
```

### `gha-many`

Use when you want to run several workflows and collect the results together.

```yaml
sequence:
  - id: ci_matrix
    op: nix:github:colony-2/c2ops/<commit>#gha-many
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
    op: nix:github:colony-2/c2ops/<commit>#pydantic
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
    op: nix:github:colony-2/c2ops/<commit>#aider
    inputs:
      prompt: Fix the failing tests in this repository.
      model: sonnet
      env:
        ANTHROPIC_API_KEY: "${{ secrets.anthropic_api_key }}"
      worktree_path: "{{ context.environment.op.worktree_path }}"
```

### `litellm`

Use when you want an `llm`-style interface on top of LiteLLM.

```yaml
sequence:
  - id: ask
    op: nix:github:colony-2/c2ops/<commit>#litellm
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
- [`kimi/README.md`](./kimi/README.md)
- [`jev/README.md`](./jev/README.md)
- [`rule_gate/README.md`](./rule_gate/README.md)

Dependency versions, compatibility notes, and validation are recorded in
[`DEPENDENCIES.md`](./DEPENDENCIES.md).

## Notes

- Nix packages contain compiled Go binaries or Python wrappers with locked library environments; workers download the finished runtime closures from Cachix.
- Codex and Kimi declare pinned `pnpm:` CLI dependencies, which c2j prepares before execution. Other tools used by these ops are bound in the Nix wrappers.
- Local/Git source selectors remain available for development. Their manifests explicitly declare tool dependencies; Go compilation and `uv run --script` library setup still happen when running from source.
