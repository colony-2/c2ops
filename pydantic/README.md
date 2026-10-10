# `pydantic`

PydanticAI-backed op with an interface similar to the existing `llm_inference2` op.

## Selector

Use this op from the repo root as:

```yaml
op: ./pydantic
```

Nix package selector:

```yaml
op: nix:github:colony-2/c2ops/main#pydantic
```

## What It Does

- Reads one JSON payload from stdin
- Runs with a packaged Python interpreter and locked dependencies
- Shares the direct dependency pins in [main.py](./main.py); Nix locks all transitive dependencies
- Returns JSON via the extension-op `{"output": ...}` envelope

## Requirements

- A c2j worker configured for the [`colony2` cache](../NIX_PACKAGES.md); Python and libraries are packaged
- Provider API keys supplied either:
  - in `api_keys`
  - or already present in the process environment

Supported provider key mapping:

- `openai` -> `OPENAI_API_KEY`
- `anthropic` -> `ANTHROPIC_API_KEY`
- `gemini` -> `GEMINI_API_KEY`

## Inputs

This op follows the `llm_inference2` input shape:

- `default_provider`
- `default_model`
- `prompt`
- `system_prompt`
- `api_keys`
- `temperature`, `max_tokens`, `top_p`, `stop_sequences`
- `files`
- `tools`
- `execute_tools`
- `response_schema`
- `enable_sandbox`, `allowed_paths`, `restricted_paths`
- `default_working_dir`, `tool_working_dir`
- `enable_tool_execution`, `max_tool_rounds`, `continue_on_tool_error`, `tool_timeout`
- `metadata`

## File Handling

Files are currently included as prompt context.

- Text-like files are embedded in the prompt body
- Non-text files are represented as placeholders
- `max_file_context_size` is enforced against the supplied file bytes

This is compatibility-oriented, not native multimodal file transport.

## Tool Handling

Tool definitions are exposed to the model through PydanticAI external tools.

Current behavior:

- If `execute_tools` is `false`, tool calls are surfaced in `tool_calls`
- If `execute_tools` is `true`, the script executes a built-in file tool subset

Auto-executed tool names currently supported:

- `write_file`
- `read_file`
- `delete_file`
- `list_files`
- `create_directory`

If you provide other tool names:

- they can still be proposed by the model
- but auto-execution will fail unless you extend the script

## Sandbox

When `enable_sandbox` is true, file operations are validated against:

- `allowed_paths`
- `restricted_paths`
- a built-in restricted-path list
- simple sensitive-path checks

This is path-policy validation inside the op, not OS-level isolation.

## Example

```yaml
sequence:
  - id: plan_change
    op: ./pydantic
    inputs:
      default_provider: openai
      default_model: gpt-4.1
      api_keys:
        openai: "${{ secrets.openai_api_key }}"
      prompt: Review these files and propose a patch plan.
      files:
        - path: README.md
          type: markdown
          content: "${{ files.readme_b64 }}"
```

Git-backed recipe example:

```yaml
sequence:
  - id: plan_change
    op: nix:github:colony-2/c2ops/main#pydantic
    inputs:
      default_provider: openai
      default_model: gpt-4.1
      api_keys:
        openai: "${{ secrets.openai_api_key }}"
      prompt: Review these files and propose a patch plan.
```

Tool execution example:

```yaml
sequence:
  - id: edit_file
    op: ./pydantic
    inputs:
      default_provider: anthropic
      default_model: claude-sonnet-4-5
      prompt: Create a notes file with today's deployment checklist.
      tools:
        - name: write_file
          description: Write or update a file
          parameters:
            type: object
            properties:
              path:
                type: string
              content:
                type: string
            required: [path, content]
      execute_tools: true
      enable_tool_execution: true
      max_tool_rounds: 3
      tool_working_dir: "{{ context.environment.op.worktree_path }}"
```

## Outputs

- `response`
- `model`
- `finish_reason`
- `usage`
- `tool_calls`
- `tool_results`
- `tool_execution_errors`
- `tool_rounds_used`
- `files_written`
- `files_read`
- `files_deleted`
- `execution_time_ms`
- `provider_metadata`
- `telemetry`

## Notes

- This is the best fit for the current `llm2` surface among off-the-shelf frameworks, but it is not feature-identical to the old Go implementation.
- The strongest compatibility today is structured output plus file-tool loops for local file operations.
