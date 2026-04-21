# `aider.exec`

Aider-backed op with a `codex.exec`-like input and output shape.

## Selector

Use this op from the repo root as:

```yaml
op: ./aider
```

Git selector example:

```yaml
op: git+https://github.com/colony-2/c2ops.git//aider@main
```

## What It Does

- Reads one JSON payload from stdin
- Runs `uv run --script ./main.py`
- Uses PEP 723 inline dependencies pinned in [main.py](./main.py)
- Invokes the `aider` CLI as a subprocess
- Returns a `codex.exec`-shaped JSON result in the extension-op `{"output": ...}` envelope

## Requirements

- `uv` installed on the machine running the op
- A supported model configuration for `aider`

You can provide provider-specific environment variables through the op input `env`, for example:

- `OPENAI_API_KEY`
- `ANTHROPIC_API_KEY`
- `AIDER_MODEL`
- `OPENAI_API_BASE`

## Inputs

This op accepts the same main fields as `codex.exec`:

- `prompt`
- `sessionId`
- `model`
- `env`
- `skill`, `skills`
- `skill_mode`, `skill_selection_mode`
- `workdir_path`
- `worktree_path`
- `artifact_inbox_path`
- `artifact_outbox_path`
- `cell_relative_path`

## Compatibility Notes

This is intentionally similar to `codex.exec`, not identical.

Current behavior:

- `sessionId` resumes the same Aider chat history under `.aider-op/sessions/<sessionId>`
- `skill` and `skills` are added to the prompt as hints
- `status_contract`, `resume_context`, and `return_on` are accepted by schema but not deeply implemented
- output artifacts are written to:
  - `stdout.jsonl`
  - `stderr.txt`

If the worktree is not a git repo, the op runs Aider with `--no-git`.

## Example

```yaml
sequence:
  - id: apply_fix
    op: ./aider
    inputs:
      prompt: Fix the failing unit tests in this repository.
      model: sonnet
      env:
        ANTHROPIC_API_KEY: "${{ secrets.anthropic_api_key }}"
      workdir_path: "{{ context.environment.workdir }}"
      worktree_path: "{{ context.environment.worktree_path }}"
      artifact_outbox_path: "{{ context.environment.outbox }}"
```

Git-backed recipe example:

```yaml
sequence:
  - id: apply_fix
    op: git+https://github.com/colony-2/c2ops.git//aider@main
    inputs:
      prompt: Fix the failing unit tests in this repository.
      model: sonnet
      env:
        ANTHROPIC_API_KEY: "${{ secrets.anthropic_api_key }}"
      worktree_path: "{{ context.environment.worktree_path }}"
```

Resume example:

```yaml
sequence:
  - id: continue_fix
    op: ./aider
    inputs:
      sessionId: "${{ sequence.apply_fix.outputs.sessionId }}"
      prompt: Continue and summarize what changed.
      env:
        ANTHROPIC_API_KEY: "${{ secrets.anthropic_api_key }}"
      worktree_path: "{{ context.environment.worktree_path }}"
```

## Outputs

- `status`
- `sessionId`
- `assistantSummary`
- `incompleteReason`
- `incompleteCategory`
- `pendingDependencies`
- `skills_installed`
- `outcome`

## Artifacts

The op writes:

- `stdout.jsonl`: line-wrapped stdout events from the Aider process
- `stderr.txt`: raw stderr from Aider

## Notes

- This is a pragmatic adapter for “use Aider behind a c2j op”, not a full reproduction of Codex runtime semantics.
- The main compatibility target is stdin/stdout structure plus resumable sessions and outbox artifacts.
