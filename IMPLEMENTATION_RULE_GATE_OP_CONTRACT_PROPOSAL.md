# `rule_gate` Contract Proposal

Status: proposed simplified update for review

Source documents:

- `REQUIREMENTS_RULE_GATE_OP.md`
- `IMPLEMENTATION_RULE_GATE_OP_REVISED.md`

This proposal keeps the product boundary from the revised spec: `rule_gate`
should be a selector-backed c2ops operation, not a built-in c2j core primitive.
c2j already provides selector execution, input/output validation, artifact inbox
bindings, op-visible paths, recipe-test mocking, and story capture of op
input/output.

This version intentionally keeps the v1 model small:

- all rules are required checks;
- every failed rule makes `ok=false`;
- the op reports failed rule IDs and detailed failed rule results;
- recipes decide routing by inspecting `outputs.ok`, `outputs.failed_rule_ids`,
  or `outputs.results`.

## Product Boundary

The external op owns:

- rule parsing and validation;
- deterministic rule evaluation;
- inbox file path validation;
- JSON parsing and JSON Schema checks;
- child status object evaluation;
- output shaping and diagnostics.

c2j core owns:

- selector resolution and pinning;
- artifact materialization into the op inbox;
- op-visible path mapping;
- extension process execution;
- input and output schema validation;
- recipe transitions that consume gate outputs;
- job story capture of the op input and output.

c2j core should not interpret rule IDs, gate outputs, or policy presets in v1.

## Packaging

Packaging follows the existing selector-op convention in this repository.

For a Go implementation:

```yaml
name: rule_gate
description: Evaluate deterministic recipe policy rules
command: ["go", "run", "."]
timeout: 2m
```

For a Python implementation:

```yaml
name: rule_gate
description: Evaluate deterministic recipe policy rules
command: ["uv", "run", "--script", "./main.py"]
timeout: 2m
env:
  PYTHONUNBUFFERED: "1"
```

The implementation language is not part of the user-facing contract. The op must
receive one JSON object on stdin and write one JSON object on stdout.

## Manifest Contract

`rule_gate/op.yaml` should define a strict enough schema to catch common authoring
errors before execution. Cross-field rules that are awkward in the manifest
schema, such as "exactly one schema source", are enforced by the evaluator before
partial rule evaluation.

```yaml
name: rule_gate
description: Evaluate deterministic recipe policy rules
command: ["go", "run", "."]
timeout: 2m

input_schema:
  type: object
  required: [rules]
  properties:
    artifact_inbox_path:
      type: string
      default: "{{ context.environment.op.inbox }}"
    artifact_outbox_path:
      type: string
      default: "{{ context.environment.op.outbox }}"
    worktree_path:
      type: string
      default: "{{ context.environment.op.worktree_path }}"
    rules:
      type: array
      minItems: 1
      items:
        type: object
        required: [id, type, message]
        properties:
          id:
            type: string
            minLength: 1
          type:
            type: string
            enum:
              - assert
              - artifact_exists
              - file_exists
              - json_parse
              - json_schema
              - child_status
          message:
            type: string
            minLength: 1

          value:
            type: boolean

          artifact: {}
          path:
            type: string
            minLength: 1
          inbox_path:
            type: string
            minLength: 1
          schema:
            type: object
          schema_worktree_path:
            type: string
            minLength: 1
          schema_inbox_path:
            type: string
            minLength: 1

          status: {}
          allow_statuses:
            type: array
            items:
              type: string
              minLength: 1

output_schema:
  type: object
  required:
    - version
    - ok
    - failed_rule_ids
    - summary
    - results
  properties:
    version:
      type: string
    ok:
      type: boolean
    failed_rule_ids:
      type: array
      uniqueItems: true
      items:
        type: string
        minLength: 1
    summary:
      type: object
      required: [total, passed, failed]
      properties:
        total:
          type: integer
        passed:
          type: integer
        failed:
          type: integer
    results:
      type: object
      additionalProperties:
        type: object
        required: [id, type, status, message]
        properties:
          id:
            type: string
          type:
            type: string
          status:
            type: string
            enum: [failed]
          message:
            type: string
          details:
            type: object
```

The reserved `sandbox` recipe input remains controlled by c2j and is not part of
the JSON payload delivered to the extension.

## Input Contract

Top-level input:

```json
{
  "artifact_inbox_path": "/path/visible/to/op/inbox",
  "artifact_outbox_path": "/path/visible/to/op/outbox",
  "worktree_path": "/path/visible/to/op/worktree",
  "rules": [
    {
      "id": "validation_passed",
      "type": "assert",
      "value": true,
      "message": "Validation must pass before merge."
    }
  ]
}
```

Required rule fields:

- `id`;
- `type`;
- `message`.

Rule IDs must be unique within a gate. Rule result ordering must match input rule
ordering.

For ergonomic CEL dot access, prefer rule IDs that are valid identifiers:
`[A-Za-z_][A-Za-z0-9_]*`. Rule IDs with punctuation can still be used, but
recipes should check them with map membership or bracket access rather than dot
selection.

Supported rule result statuses:

- `passed`;
- `failed`.

All rules are required checks. v1 does not include optional/downgrade behavior or
gate-selected routing.

## Artifact References, Inbox Files, And Worktree Files

Recipe artifacts and filesystem files are different things.

`rule_gate` v1 supports three distinct checks:

- `artifact_exists` checks that a recipe-context artifact reference was rendered
  into the rule input. It does not materialize or read the artifact.
- `json_parse` and `json_schema` read files under `artifact_inbox_path`. Use a
  node `artifacts:` binding when the rule needs artifact contents.
- `file_exists` checks files under `worktree_path`. Use it for repository files
  such as `go.mod`, generated source files, or other filesystem paths in the
  current worktree.

A recipe artifact reference, such as
`${{ states.review.artifacts["reviews/review-pack.json"] }}`, is recipe-context
data. A bound inbox file, such as `reviews/review-pack.json` under
`artifact_inbox_path`, is filesystem data visible to the extension process.

For `artifact_exists` to return a normal policy failure, the authored expression
must render a missing artifact as `null` or an empty value instead of failing
template resolution. If direct artifact map indexing hard-fails on missing keys,
c2j should provide a safe artifact lookup helper before recipes rely on
`artifact_exists` for missing-artifact gates.

## Output Contract

The op exits successfully for policy failures and emits a complete decision
object:

```json
{
  "version": "rule_gate/v1",
  "ok": false,
  "failed_rule_ids": ["review_pack_json"],
  "summary": {
    "total": 2,
    "passed": 1,
    "failed": 1
  },
  "results": {
    "review_pack_json": {
      "id": "review_pack_json",
      "type": "json_parse",
      "status": "failed",
      "message": "Review pack must be valid JSON.",
      "details": {
        "inbox_path": "reviews/review-pack.json",
        "error": "invalid character ..."
      }
    }
  }
}
```

Output semantics:

- `ok` is `true` if and only if every rule passes.
- `failed_rule_ids` contains failed rule IDs in input order.
- `results` is an object keyed by failed rule ID.
- `results` contains only failed rules.
- `results[id]` contains the detailed failed rule result for that rule.
- A passed rule is absent from `results`.
- Policy failures exit `0` and should not trigger c2j `catch:`.
- Invalid inputs, invalid rule definitions, and infrastructure failures exit
  nonzero and may trigger c2j `catch:`.

Rule result shape:

```json
{
  "id": "review_pack_json",
  "type": "json_parse",
  "status": "failed",
  "message": "Review pack must be valid JSON.",
  "details": {
    "inbox_path": "reviews/review-pack.json",
    "error": "invalid character ..."
  }
}
```

## Rule Types

### `assert`

Use `assert` for direct boolean checks rendered by c2j before op execution.

```yaml
- id: review_ok
  type: assert
  value: ${{ json_parse(states.review.outputs.review_pack).ok }}
  message: Resolve review issues.
```

Rules:

- `value` is required.
- `value` must be boolean when the op receives it.
- A surviving string such as `"${{ ... }}"` is invalid input.
- The op must not evaluate CEL or recreate c2j's template context.

The original requirements used `type: cel`. In this contract, `assert` replaces
`cel` because c2j already resolves CEL and templates for op inputs before the
extension process runs.

### `artifact_exists`

Checks that a recipe-context artifact reference was provided to the rule.

```yaml
- id: review_pack_artifact_exists
  type: artifact_exists
  artifact: ${{ states.review.artifacts["reviews/review-pack.json"] }}
  message: Review pack artifact is required.
```

Rules:

- `artifact` is required.
- `artifact` may be a rendered artifact reference object or string.
- An omitted `artifact` field is an invalid rule definition.
- A rendered `null` value or empty string fails the rule as a policy failure.
- If the artifact expression fails before op execution, that is a template
  resolution failure, not a `rule_gate` policy failure.
- The rule does not read `artifact_inbox_path`.
- The rule does not materialize artifact contents.
- The rule should not call c2j workflow APIs.

Use `artifact_exists` when the policy only needs to know whether a previous node
published an artifact. Use an `artifacts:` binding plus an inbox-content rule
when the policy needs to parse or validate the artifact contents.

### `file_exists`

Checks that a worktree file exists.

```yaml
- id: go_mod_exists
  type: file_exists
  path: go.mod
  message: go.mod is required.
```

Rules:

- `path` is required.
- `path` is relative to `worktree_path`.
- Absolute paths are invalid.
- Paths with any segment equal to `..` are invalid.

### `json_parse`

Reads an inbox file and checks that it parses as JSON.

```yaml
- id: review_pack_json
  type: json_parse
  inbox_path: reviews/review-pack.json
  message: Review pack must be valid JSON.
```

Rules:

- `inbox_path` is required.
- `inbox_path` is relative to `artifact_inbox_path`.
- Absolute paths are invalid.
- Paths with any segment equal to `..` are invalid.
- Missing files and JSON parse errors are policy failures.
- The process should not fail for malformed JSON in a referenced file.

### `json_schema`

Validates an inbox JSON file against a JSON Schema.

```yaml
- id: dependency_specs_schema
  type: json_schema
  inbox_path: implementation/dependency-job-specs.json
  schema_worktree_path: schemas/dependency-job-specs.schema.json
  message: Dependency job specs must match schema.
```

Rules:

- `inbox_path` is required.
- Exactly one of `schema`, `schema_worktree_path`, or `schema_inbox_path` is
  required.
- `schema` means an inline schema object, not a path string.
- `schema_worktree_path` is repo-relative under `worktree_path`.
- `schema_inbox_path` is inbox-relative under `artifact_inbox_path`.
- HTTP schema references are not supported in v1.
- Multi-file local `$ref` resolution is not supported in v1.
- Internal refs within the supplied schema, such as `#/$defs/...`, are supported.
- Schema validation failures are policy failures with structured details.
- Invalid schema definitions are invalid input and should fail the process before
  partial rule evaluation where possible.

### `child_status`

Checks child status data already supplied by recipe outputs.

```yaml
- id: optional_review_completed
  type: child_status
  status: ${{ states.inspect_review.outputs }}
  allow_statuses: ["completed", "cancelled"]
  message: Optional review should complete or be explicitly cancelled.
```

Rules:

- `status` is required.
- `status` may be a string.
- `status` may be an object with a string `status` field.
- `allow_statuses` defaults to `["completed"]`.
- If status cannot be extracted, the rule fails with details.
- The op must not call c2j workflow APIs.

## Recipe Routing

`rule_gate` does not emit a route or action. Recipes decide routing from the
plain outputs.

Example:

```yaml
transitions:
  - to: validation_repair
    when: '"validation_passed" in states.final_gate.outputs.results'
  - to: review_artifact_repair
    when: '"review_pack_json" in states.final_gate.outputs.results'
  - to: continue_work
    when: states.final_gate.outputs.ok
```

For identifier-safe rule IDs, recipes may also use dot access if c2j's CEL
runtime treats missing map fields as `null`:

```yaml
when: states.final_gate.outputs.results.review_pack_json != null
```

For rule IDs that are not valid CEL identifiers, use bracket access:

```yaml
when: states.final_gate.outputs.results["review-pack-json"] != null
```

The membership form is the canonical v1 example because it does not rely on
missing-key null behavior.

## Authoring Examples

Direct remote selector:

```yaml
- id: final_gate
  op: git+https://github.com/colony-2/c2ops-rule-gate.git//rule_gate@<pinned-sha>
  inputs:
    rules:
      - id: validation_passed
        type: assert
        value: ${{ states.validate.outputs.passed }}
        message: Validation must pass before merge.
```

Gate over a recipe artifact reference without materializing contents:

```yaml
- id: pre_implementation_gate
  op: git+https://github.com/colony-2/c2ops-rule-gate.git//rule_gate@<pinned-sha>
  inputs:
    rules:
      - id: review_pack_artifact_exists
        type: artifact_exists
        artifact: ${{ states.review.artifacts["reviews/review-pack.json"] }}
        message: Review pack artifact is required.
```

Gate over a file materialized from a bound recipe artifact:

```yaml
- id: pre_implementation_gate
  op: git+https://github.com/colony-2/c2ops-rule-gate.git//rule_gate@<pinned-sha>
  artifacts:
    reviews/review-pack.json: ${{ states.review.artifacts["reviews/review-pack.json"] }}
  inputs:
    rules:
      - id: review_pack_json
        type: json_parse
        inbox_path: reviews/review-pack.json
        message: Review pack must be valid JSON.
```

Gate over a worktree file:

```yaml
- id: repo_gate
  op: git+https://github.com/colony-2/c2ops-rule-gate.git//rule_gate@<pinned-sha>
  inputs:
    rules:
      - id: go_mod_exists
        type: file_exists
        path: go.mod
        message: go.mod is required.
```

Sandboxed selector execution:

```yaml
- id: final_gate
  op: git+https://github.com/colony-2/c2ops-rule-gate.git//rule_gate@<pinned-sha>
  inputs:
    sandbox:
      type: shai
    rules:
      - id: validation_passed
        type: assert
        value: ${{ states.validate.outputs.passed }}
        message: Validation must pass before merge.
```

## Implementation Plan

Use Go for v1. The existing Go selector ops in this repository use a small
`main.go`, an `op.yaml` with `command: ["go", "run", "."]`, and a package that
owns the operation logic. `rule_gate` should follow that shape.

Recommended repo layout:

```text
c2ops-rule-gate/
  rule_gate/
    op.yaml
    main.go
    internal/extensioncmd/
      command.go
    pkg/rulegate/
      types.go
      validate.go
      evaluate.go
      paths.go
      json_schema.go
      *_test.go
```

If the op initially lives in a broader `c2ops` repository, use the same layout
under `rule_gate/` and import `github.com/colony-2/c2ops/rule_gate/pkg/rulegate`.
If it is standalone, use module path `github.com/colony-2/c2ops-rule-gate`.

Implementation steps:

1. Add `rule_gate/op.yaml`.
   - Use the manifest schema from this document.
   - Use `command: ["go", "run", "."]`.
   - Default `artifact_inbox_path`, `artifact_outbox_path`, and `worktree_path`
     from `context.environment.op.*`.

2. Add the Go entrypoint.
   - Copy or factor the existing `internal/extensioncmd` stdin/stdout wrapper
     pattern used by `gha` and `codex`.
   - `main.go` should decode `rulegate.Input`, call `rulegate.Evaluate`, and
     write `rulegate.Output`.
   - Policy failures return a normal output and `nil` error.
   - Invalid input and infrastructure failures return an error so the process
     exits nonzero.

3. Define the data model in `pkg/rulegate/types.go`.
   - `Input` contains paths and `[]Rule`.
   - `Rule` contains common fields plus optional per-rule fields.
   - `Output` contains `Version`, `OK`, `FailedRuleIDs`, `Summary`, and
     `Results map[string]RuleResult`.
   - `RuleResult` contains `ID`, `Type`, `Status`, `Message`, and `Details`.
   - Keep JSON tags aligned with the manifest contract.

4. Implement validation in `validate.go`.
   - Reject empty `rules`.
   - Reject empty or duplicate rule IDs.
   - Reject unknown rule types defensively, even though schema should catch them.
   - Validate required fields by rule type.
   - Reject incompatible fields where it prevents ambiguous behavior.
   - Reject invalid schema-source combinations before partial evaluation.

5. Implement safe path resolution in `paths.go`.
   - `resolveWorktreePath(worktreePath, path)` for `file_exists`.
   - `resolveInboxPath(artifactInboxPath, inboxPath)` for `json_parse`,
     `json_schema`, and `schema_inbox_path`.
   - Reject absolute paths.
   - Reject any path segment equal to `..`.
   - Use `filepath.Clean` after checking segments, then verify the final path
     remains under the expected root.

6. Implement the evaluator in `evaluate.go`.
   - Validate all rule definitions before evaluating any rule.
   - Evaluate rules in input order.
   - Build `summary.total`, `summary.passed`, and `summary.failed`.
   - Append failed IDs to `failed_rule_ids` in input order.
   - Add failed rule details to `results[rule.ID]`.
   - Do not include passed rules in `results`.
   - Set `ok = len(failed_rule_ids) == 0`.

7. Implement `assert`.
   - Require `value`.
   - Pass only when `value == true`.
   - Fail when `value == false`.
   - Reject non-bool values as invalid input. In Go, use a pointer field or raw
     JSON decoding so missing, false, and wrong-type values are distinguishable.

8. Implement `artifact_exists`.
   - Require the `artifact` field to be present.
   - Treat rendered `null` or empty string as policy failure.
   - Treat non-empty strings and non-empty objects as present.
   - Do not read from the inbox.
   - Do not call c2j APIs.

9. Implement `file_exists`.
   - Resolve `path` under `worktree_path`.
   - Pass when `os.Stat` succeeds.
   - Fail when the file does not exist.
   - Treat path validation errors as invalid input, not policy failure.

10. Implement `json_parse`.
    - Resolve `inbox_path` under `artifact_inbox_path`.
    - Missing file is a policy failure.
    - Malformed JSON is a policy failure with parse details.
    - Valid JSON passes.

11. Implement `json_schema`.
    - Resolve the target JSON file from `inbox_path`.
    - Load exactly one schema source: inline `schema`, `schema_worktree_path`, or
      `schema_inbox_path`.
    - Use `github.com/santhosh-tekuri/jsonschema/v6`; it is already used in this
      repo and fits Go v1.
    - Support internal refs in the supplied schema.
    - Reject or fail clearly on unsupported remote refs and multi-file local refs.
    - Treat invalid schema definitions as invalid input.
    - Treat schema validation errors as policy failures with structured details.

12. Implement `child_status`.
    - Accept a string status or an object with string field `status`.
    - Default `allow_statuses` to `["completed"]`.
    - Fail when status is missing, not a string, or not allowed.

13. Add tests in layers.
    - Pure unit tests for validation, path resolution, each rule evaluator, and
      result shaping.
    - CLI tests that run `go run .` with JSON stdin and assert stdout/exit code.
    - Selector integration tests that verify schema defaults and op-visible paths.
    - Recipe-test fixtures for artifact reference checks, artifact binding plus
      JSON parsing, worktree `file_exists`, and CEL routing on `outputs.results`.

14. Roll out with one pinned recipe.
    - Start with `artifact_exists`, `json_parse`, and `assert`.
    - Add `json_schema` once schema error formatting is stable.
    - Keep the output shape pinned before expanding rule types.

## Test Plan

Unit tests:

- duplicate rule IDs are invalid;
- unknown rule types are rejected by schema or evaluator;
- `assert` accepts booleans and rejects unresolved strings;
- `artifact_exists` passes for rendered artifact references;
- `artifact_exists` fails for rendered null or empty artifact values;
- `artifact_exists` rejects omitted artifact fields as invalid input;
- worktree file paths reject absolute paths and `..`;
- inbox file paths reject absolute paths and `..`;
- `file_exists` checks under `worktree_path`;
- `json_parse` reports malformed JSON as a policy failure;
- `json_schema` supports inline schema, worktree schema file, and inbox schema
  file;
- `json_schema` rejects multiple schema sources;
- external `$ref` values are rejected or reported as unsupported;
- `child_status` accepts string status and object status;
- `child_status` fails when status cannot be extracted;
- `failed_rule_ids` matches failed rules in input order;
- `results` contains failed rule IDs as object keys;
- `results` omits passed rules;
- `failed_rule_ids` preserves failed rule ordering for consumers that need order.

Selector integration tests:

- local selector receives op-visible default paths;
- sandboxed selector receives op-visible default paths;
- policy failure exits successfully and emits `ok=false`;
- invalid input exits nonzero;
- output schema validates the full decision object.

c2j recipe-test fixtures:

- run `artifact_exists` over a contextual artifact reference without an
  `artifacts:` binding;
- bind a recipe artifact into the gate inbox for a `json_parse` rule;
- run the local rule-gate selector;
- route on `outputs.results`;
- assert `outputs.ok`;
- assert `outputs.failed_rule_ids`;
- assert `outputs.results.<rule_id>` presence for failed rules;
- assert failure messages.
