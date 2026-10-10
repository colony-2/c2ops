# `rule_gate`

Go-backed op for evaluating deterministic recipe policy rules.

## Selector

Use this op from the repo root as:

```yaml
op: ./rule_gate
```

Nix package selector:

```yaml
op: nix:github:colony-2/c2ops/main#rule_gate
```

## What It Does

- Reads one JSON payload from stdin
- Runs a prebuilt Go executable from the Nix package (local source selectors use `go run .`)
- Evaluates required policy rules in input order
- Returns a normalized decision in the extension-op `{"output": ...}` envelope
- Exits successfully for policy failures with `ok: false`
- Exits nonzero for invalid input or infrastructure failures

## Inputs

- `rules`
- `artifact_inbox_path`
- `artifact_outbox_path`
- `worktree_path`

`artifact_inbox_path`, `artifact_outbox_path`, and `worktree_path` default from
`context.environment.op.*`.

Each rule has:

- `id`
- `type`
- `message`

Rule IDs must be unique. Prefer identifier-safe IDs such as
`review_pack_json` when recipes will access `outputs.results.<rule_id>`.

## Rule Types

- `assert`: checks a rendered boolean `value`
- `artifact_exists`: checks a rendered recipe-context artifact reference
- `file_exists`: checks a file under `worktree_path`
- `json_parse`: parses a JSON file under `artifact_inbox_path`
- `json_schema`: validates a JSON file under `artifact_inbox_path`
- `child_status`: checks a supplied child status string or object

## Artifact And File Boundaries

Recipe artifacts and filesystem files are distinct.

Use `artifact_exists` when the policy only needs to know whether a previous node
published an artifact:

```yaml
rules:
  - id: review_pack_artifact_exists
    type: artifact_exists
    artifact: ${{ states.review.artifacts["reviews/review-pack.json"] }}
    message: Review pack artifact is required.
```

Use `artifacts:` plus `json_parse` or `json_schema` when the policy needs to read
artifact contents:

```yaml
artifacts:
  reviews/review-pack.json: ${{ states.review.artifacts["reviews/review-pack.json"] }}
inputs:
  rules:
    - id: review_pack_json
      type: json_parse
      inbox_path: reviews/review-pack.json
      message: Review pack must be valid JSON.
```

Use `file_exists` for repository files in the current worktree:

```yaml
rules:
  - id: go_mod_exists
    type: file_exists
    path: go.mod
    message: go.mod is required.
```

## Recipe

```yaml
sequence:
  - id: final_gate
    op: nix:github:colony-2/c2ops/main#rule_gate
    artifacts:
      reviews/review-pack.json: ${{ states.review.artifacts["reviews/review-pack.json"] }}
    inputs:
      rules:
        - id: validation_passed
          type: assert
          value: ${{ states.validate.outputs.passed }}
          message: Validation must pass before merge.
        - id: review_pack_json
          type: json_parse
          inbox_path: reviews/review-pack.json
          message: Review pack must be valid JSON.
```

## Outputs

- `version`
- `ok`
- `failed_rule_ids`
- `summary`
- `results`

`results` is a map keyed by failed rule ID. Passed rules are omitted.

Example transition:

```yaml
transitions:
  - to: repair_review_pack
    when: '"review_pack_json" in states.final_gate.outputs.results'
  - to: merge
    when: states.final_gate.outputs.ok
```

For identifier-safe rule IDs, dot access can also be used when the CEL runtime
treats missing fields as `null`:

```yaml
when: states.final_gate.outputs.results.review_pack_json != null
```
