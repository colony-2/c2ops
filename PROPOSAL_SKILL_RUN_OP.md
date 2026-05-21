# Proposal: `skill.run`

## Summary

Implement `skill.run` as a thin, selector-backed c2ops wrapper around the
current `codex` op. It should make "run this skill with this artifact contract"
the authored surface, while preserving the existing Codex session, status, and
checkpoint outputs.

The important change from `REQUIREMENTS_SKILL_RUN_OP.md` is that structured
skill output should be artifact-first, not assistant-summary-first. The current
Codex op already uses `--output-schema` to force the final assistant message
into the c2ops wrapper shape:

- `status`
- `assistantSummary`
- `incompleteReason`
- `incompleteCategory`
- `pendingDependencies`
- `errorMessage`

That wrapper payload should remain stable. A skill's primary JSON result should
instead be written to a declared outbox artifact and validated by the op after
Codex exits. Parsing `assistantSummary` can remain as a compatibility mode, but
it should not be the preferred contract for new skills.

## Current Codex Behavior To Preserve

The existing `codex` op:

- Requires a non-empty `prompt`.
- Defaults `workdir_path`, `worktree_path`, `artifact_inbox_path`, and
  `artifact_outbox_path` from `context.environment.op.*`.
- Materializes `skills` git refs into a staging skill root and installs them
  into the invocation Codex home.
- Lets Codex choose whether to use installed skills; it does not currently
  enforce one top-level skill by name.
- Runs `codex exec --experimental-json --output-schema <wrapper-schema>`.
- Parses Codex JSONL stdout into the normalized output shape.
- Writes `stdout.jsonl` and `stderr.txt` to the outbox.
- Loads `status_contract.path` from the outbox, uses it to build
  `outcome.checkpoint`, and returns `incomplete` when the contract is missing
  or requests a return status.
- Supports resumable sessions and persists Codex home state.

`skill.run` should build on this behavior instead of replacing it.

## Proposed Surface

Use a nested c2ops extension directory at `codex/run_skill` with manifest
`name: skill.run`. Recipes can use the selector form immediately:

```yaml
- id: intake
  op: git+https://github.com/colony-2/c2ops.git//codex/run_skill@main
  artifacts:
    submitted/: "${{ context.artifacts }}"
  inputs:
    skill: c2-ticket-intake
    skills:
      - gitl.colony2.com/jnadeau/skills/.agents/skills@main
    input:
      ticket_prompt: "{{ inputs.prompt }}"
    status_contract:
      path: ticket/latest-status.json
    output:
      path: ticket/result.json
      format: json
      schema:
        type: object
        required: [summary]
        properties:
          summary:
            type: string
```

If c2j later supports named op aliases, `op: skill.run` can resolve to the
`//codex/run_skill` selector. That alias should be a c2j convenience layer, not
required for the first c2ops implementation.

## Prompt Model

`skill.run` should not require recipe authors to provide a freeform prompt.
The required input is `skill`. The wrapper should generate the Codex prompt from
the invocation contract.

Recommended inputs:

```yaml
inputs:
  skill: string                  # required
  skills: [string]
  input: object                  # optional structured invocation data
  prompt: string                 # optional compatibility/supplemental text
  sessionId: string
  model: string
  return_on: [string]
  status_contract:
    path: string
  output:
    from: artifact               # default: artifact
    path: string                 # default: skill-output/result.json
    format: json                 # v1 only needs json
    schema: object
    required_when_incomplete: false
    validation:
      on_error: incomplete       # incomplete | fail | warn
      repair:
        enabled: true
        max_attempts: 1
  idle_timeout: string
  workdir_path: string
  worktree_path: string
  artifact_inbox_path: string
  artifact_outbox_path: string
```

The generated prompt should be deterministic and should include:

- The top-level skill name.
- The artifact inbox path.
- The artifact outbox path.
- The status contract path, when configured.
- The primary output artifact path and format, when configured.
- The path to a generated invocation contract file.
- The optional `input` object rendered as JSON.
- The optional `prompt` text under a clearly labeled "supplemental request"
  section.

This gives artifact-only workflows a clean path: a skill can be run with no
freeform prompt when all input is in artifacts and the structured `input` object.

## Invocation Contract File

Before launching Codex, `skill.run` should write a deterministic contract file
under the op workdir, for example:

```text
<workdir_path>/codex-run-skill/contract.json
```

The file should contain:

```json
{
  "skill": "c2-ticket-intake",
  "artifact_inbox_path": "...",
  "artifact_outbox_path": "...",
  "status_contract": {
    "path": "ticket/latest-status.json"
  },
  "output": {
    "path": "ticket/result.json",
    "format": "json",
    "schema": {}
  },
  "input": {}
}
```

No new environment variables are required. The extension op receives its inputs
as JSON on stdin, and the Codex process gets the skill invocation contract
through the generated prompt and the contract file path named in that prompt.

## Structured Output

The preferred source is an artifact:

```yaml
output:
  from: artifact
  path: ticket/result.json
  format: json
  schema:
    type: object
    required: [summary]
```

Resolution rules:

- Relative `output.path` is resolved under `artifact_outbox_path`.
- Absolute paths are rejected unless they are still inside `artifact_outbox_path`.
- `outbox/...` prefixes are normalized the same way `status_contract.path` is.
- `format: json` parses the artifact into `parsed_output`.
- If `schema` is present, validate with a deterministic JSON Schema validator
  in the op process.

Compatibility mode:

```yaml
output:
  from: assistantSummary
  format: json
  schema: {}
```

This should parse the normalized `assistantSummary` string after the Codex run,
but new skills should avoid it. It couples the skill result to the wrapper
assistant payload and is fragile across resumed agent loops.

## Validation And Repair

Validation should be deterministic and outside the agent:

1. Run Codex through the existing wrapper schema.
2. Load and parse the configured status contract.
3. Load and parse the configured output artifact.
4. Validate the parsed output against the configured JSON Schema.
5. If validation fails and repair is enabled, resume the same Codex session with
   a repair prompt that includes the validation errors and asks Codex to rewrite
   only the declared output artifact.
6. Revalidate after each repair attempt.
7. Apply `output.validation.on_error`.

Default repair behavior should be conservative:

- Enable one repair attempt by default for `output.from: artifact`.
- Do not run repair when the status contract already returned an incomplete
  checkpoint status, unless `output.required_when_incomplete: true`.
- Do not run repair for `output.from: assistantSummary`; there is no stable
  artifact to fix.

Default validation policy should be `incomplete`, not process failure. That
preserves the current Codex-style output shape and lets recipes inspect
diagnostics. Recipes that need hard failure can set:

```yaml
output:
  validation:
    on_error: fail
```

## Outputs

`skill.run` should preserve the existing Codex outputs:

- `status`
- `sessionId`
- `assistantSummary`
- `incompleteReason`
- `incompleteCategory`
- `pendingDependencies`
- `skills_installed`
- `outcome`

It should add:

- `skill`
- `raw_summary`
- `output_source`
- `output_path`
- `raw_output`
- `parsed_output`
- `output_schema_valid`
- `output_schema_errors`
- `output_repair_attempts`
- `status_contract_path`
- `status_contract_present`
- `status_contract_valid`
- `status_contract_json`
- `status_contract_errors`
- `diagnostics`

When output validation fails with `on_error: incomplete`, set:

- `status: incomplete`
- `incompleteCategory: output_schema_validation`
- `incompleteReason` to a concise validation summary
- `outcome.checkpoint.contractErrors` or a sibling diagnostics field with the
  detailed schema errors

When status contract validation fails, mirror the current Codex behavior:

- `status: incomplete`
- `incompleteCategory` and `incompleteReason` should identify
  `status_contract_validation`
- status validation fields should explain missing file, path escape, invalid
  JSON, or semantic contract parse errors

## Diagnostics Artifacts

In addition to current `stdout.jsonl` and `stderr.txt`, write:

```text
_skill_run/contract.json
_skill_run/validation.json
```

`validation.json` should include the requested skill, resolved skill refs, status
contract validation result, output validation result, repair attempts, and error
categories. This gives job stories and recipe tests one stable artifact to
inspect without parsing Codex stdout.

## Artifact Binding

Keep recipe artifact binding outside op inputs. The op should consume artifacts
through `artifact_inbox_path`, exactly as the current Codex op does.

Good recipe shape:

```yaml
- id: build_outcome_bundle
  op: git+https://github.com/colony-2/c2ops.git//codex/run_skill@main
  artifacts:
    submitted/: "${{ context.artifacts }}"
    requirements/plan.json: "${{ states.requirements.artifacts['requirements/plan.json'] }}"
  inputs:
    skill: c2-test-statement-curator
    skills:
      - gitl.colony2.com/jnadeau/skills/.agents/skills@main
    input:
      ticket_prompt: "{{ inputs.prompt }}"
    status_contract:
      path: outcome/latest-status.json
    output:
      path: outcome/result.json
      schema:
        type: object
        required: [summary]
```

Future syntax such as:

```yaml
artifacts:
  use:
    - submitted
    - planning
```

belongs in c2j's artifact binding layer. `skill.run` should not assume submitted
file contents are embedded in the prompt.

## Implementation Plan

1. Add `SkillRunInput` and `SkillRunOutput` types to `codex/pkg/codex`.
2. Factor the current `Run(ctx, ExecOpInput)` flow so skill-source
   materialization and Codex execution can be shared without fetching the same
   `skills` refs twice.
3. Add `RunSkill(ctx, SkillRunInput) (SkillRunOutput, error)` in the same
   package so it can reuse existing unexported path, status-contract, and skill
   source helpers.
4. Generate the invocation contract file and deterministic prompt.
5. Validate that the requested top-level skill exists in the materialized skill
   sources or worktree `.agents/skills` before launching Codex. If not found,
   return a skill resolution error before launching Codex.
6. Execute Codex through the shared internal runner with the generated prompt,
   prepared skill dirs, and normal Codex execution options.
7. Add output artifact loading, JSON parsing, schema compilation, and schema
   validation. Reuse `github.com/santhosh-tekuri/jsonschema/v6`, already present
   in the dependency graph.
8. Add optional repair by calling the shared runner again with `sessionId` from
   the previous run and a repair-specific prompt.
9. Add a new `codex/run_skill` extension op directory whose `main.go` calls
   `codex.RunSkill`.
10. Add `codex/run_skill/op.yaml` with op-visible path defaults and an output schema
   covering the preserved Codex fields plus the new validation fields.
11. Document selector usage in `README.md`.

## Testing Plan

Unit tests with fake Codex execution should cover:

- No `prompt` input still generates a valid Codex prompt from `skill`, `input`,
  inbox, outbox, status path, and output path.
- Missing requested skill fails before launching Codex.
- Installed skill refs are surfaced as `skills_installed`.
- A valid output artifact is parsed into `parsed_output` and validates against
  schema.
- Invalid JSON and schema mismatches produce `output_schema_errors`.
- `on_error: incomplete` returns normalized incomplete output.
- `on_error: fail` returns a process error while still emitting any partial
  output supported by `extensioncmd`.
- Repair resumes the same session and rewrites the output artifact before
  revalidation.
- Status contract presence, JSON parsing, and error fields mirror current
  checkpoint behavior.
- `output.from: assistantSummary` remains available for compatibility.

Recipe tests should mock `codex/run_skill` by node path or op selector and
provide the same normalized output shape plus `parsed_output` and validation
fields.

## Non-Goals

- Do not add skill-internal subagent orchestration.
- Do not replace recipes as the durable orchestration layer.
- Do not embed submitted artifacts into prompts.
- Do not make every current `codex` invocation migrate to `skill.run`.
- Do not replace the Codex wrapper response schema with a skill-specific schema.

## Open Product Decisions

These are not implementation blockers for the first proposal:

- Whether c2j should support `op: skill.run` as a built-in alias for the
  selector-backed `codex/run_skill` extension op.
- Whether the default output path should be `skill-output/result.json` or a
  status-path sibling such as `ticket/latest-output.json`.
- Whether schema validation failure should default to `incomplete` or `fail` in
  enforcement-mode recipe tests. This proposal recommends `incomplete` for
  runtime compatibility and explicit `fail` for hard CI behavior.
