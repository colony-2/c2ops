# Plan: Remove the `c2j` Module Dependency

## Goal

Remove the root module's direct dependency on `github.com/colony-2/c2j` while preserving the current extension-op behavior:

- keep the existing extension manifests under `pkg/*/extension-*/op.yaml`
- keep invoking ops through `go run ./cmd/...`
- stop exposing `c2j`-specific registration APIs from the library
- move `c2j`-specific runtime concerns behind private/internal code or eliminate them entirely

## Current Dependency Surface

The current `c2j` dependency is broader than just `RegisterableOp`.

### 1. Public `RegisterableOp` exports

These packages currently expose `c2j`-facing ops as public API:

- `pkg/codex/op.go`
- `pkg/gha/op.go`
- `pkg/llm/wrapper.go`
- `pkg/llm/llm_inference_enhanced.go`
- `pkg/export/exports.go`

They are used by:

- `cmd/codex-exec-op/main.go`
- `cmd/gha-run-op/main.go`
- `cmd/gha-runs-op/main.go`
- `cmd/llm-inference-op/main.go`
- `cmd/llm-inference2-op/main.go`
- multiple tests that call `GetOp()` directly or register ops in-process

### 2. Public file types in the adapter API

The public adapter layer depends on `c2j/pkg/file`:

- `pkg/adapters/file_adapter.go`
- `pkg/adapters/file_collector.go`
- `pkg/adapters/file_type_detector.go`
- `pkg/adapters/metrics.go`
- `pkg/adapters/mock_adapter.go`
- `pkg/adapters/openai_file_adapter.go`
- `pkg/adapters/anthropic_file_adapter.go`
- `pkg/adapters/gemini_file_adapter.go`
- `pkg/adapters/unified_adapter.go`
- `pkg/adapters/secret_filter.go`
- parts of `pkg/llm/*`

This is a public API dependency, not just an internal implementation detail.

### 3. Internal execution context and error helpers

Some code depends on `c2j/pkg/ops.GitExecutionContext` and `c2j/pkg/workflow`:

- `pkg/gha/types.go`
- `pkg/gha/selector.go`
- `pkg/gha/act_backend.go`
- `pkg/gha/github_backend.go`
- `pkg/gha/op.go`
- `pkg/codex/op.go`

These are mostly internal concerns and should be replaced with local types/helpers.

### 4. Test-only `c2j` usage

The test suite is a major removal blocker:

- `pkg/codex/op_integration_test.go`
- `pkg/codex/op_resume_integration_test.go`
- `pkg/codex/op_multi_skill_integration_test.go`
- `pkg/codex/op_test.go`
- `pkg/gha/*_test.go`
- `pkg/llm/llm_inference_enhanced_test.go`
- `pkg/adapters/file_adapter_test.go`
- `test-fixtures/recipe_fixtures_test.go`

Some of these only need local type replacements. The current `c2j` recipe/worker integration tests should be replaced with black-box integration tests that invoke the `cmd/*` entrypoints directly.

## Key Observation

The `cmd/*` binaries do not need the full `c2j` op system.

Today they use `c2j/pkg/ops.CommandMain(...)`, but that helper is only a thin wrapper around:

- JSON input on stdin
- JSON output envelope on stdout
- optional artifact writing to the outbox path

The target replacement should be stricter than the current wrapper:

- all command inputs should arrive explicitly in stdin JSON
- contextual values such as worktree path, cell path, or git metadata should be expressed as normal input fields
- those contextual input fields should use `default` values in `op.yaml`, including CEL/template expressions where needed

That lets the commands stay pure and testable while keeping contextual wiring in the extension definition layer.

## Target Architecture

### Public surface after the change

Keep the public library focused on the reusable LLM functionality:

- `pkg/adapters`
- `pkg/embeddings`
- any other non-`c2j` reusable helpers that are genuinely part of the library API

Remove `c2j`-facing public APIs:

- `pkg/export`
- exported `GetOp()` / `GetRunsOp()` / `GetEnhancedOp()` style functions

### Private/internal surface after the change

Move extension-op implementation code behind internal packages. Recommended shape:

- `internal/extensioncmd`
  - stdin/stdout envelope handling
  - artifact/outbox helpers
  - external artifact ref envelope support
- `internal/runtimectx`
  - local execution context structs decoded from explicit command inputs
- `internal/ops/codex`
- `internal/ops/gha`
- `internal/ops/llm`

The `cmd/*` entrypoints should import only internal packages plus the public library packages they actually need.

### Local replacement types

Replace shared `c2j` types with local equivalents:

- `File`
- `FileType`
- execution/git context structs carried in explicit inputs
- extension command envelope types
- local error helpers or plain typed errors instead of `workflow.NewNonRetryableApplicationError`

For the file types, the cleanest option is to define a local public type in the library itself, either:

- in `pkg/adapters`, or
- in a small new public package such as `pkg/files`

The important point is that the public adapter API stops importing `c2j/pkg/file`.

## Recommended Migration Sequence

### Phase 1: Introduce local primitives first

Add the minimum local primitives needed to make the later refactor mechanical:

1. Add a local extension command runner package that mirrors the current extension process contract.
2. Add local `File` / `FileType` definitions.
3. Add local execution context/input structs for any contextual values commands still need, with the expectation that those values come from stdin fields populated by `op.yaml` defaults.
4. Add local envelope structs for:
   - command output
   - external artifact refs
   - optional outbox artifact publication

This phase should be additive and should not yet change op behavior.

### Phase 2: Move op implementations behind internal boundaries

Refactor the current exported op packages so that `c2j` is no longer part of their public API.

Recommended approach:

1. Extract the business logic from:
   - `pkg/codex/op.go`
   - `pkg/gha/op.go`
   - `pkg/llm/wrapper.go`
   - `pkg/llm/llm_inference_enhanced.go`
2. Re-home that logic under `internal/ops/...`.
3. Rewrite `cmd/*/main.go` to:
   - decode stdin
   - call the internal handler
   - emit the current envelope format on stdout
4. Delete `pkg/export`.
5. Delete exported `GetOp()`-style functions once all callers have moved off them.

If any reusable non-op logic in `pkg/codex`, `pkg/gha`, or `pkg/llm` is still valuable as library API, split it from the extension handler code instead of keeping the whole package public.

As part of this phase, update each `op.yaml` so any runtime-dependent values are explicit inputs with `default` expressions instead of implicit command-side environment reads.

### Phase 3: Remove `c2j/pkg/file` from the public adapter API

This is a breaking API change and should be treated explicitly.

1. Replace `c2j/pkg/file.File` and `c2j/pkg/file.FileType` with local types in:
   - `pkg/adapters`
   - `pkg/llm`
   - related tests
2. Add any needed conversion helpers during the transition.
3. Update examples and docs to use the local types.

This phase is required even if the op wrappers are removed, because the adapter API itself currently pulls `c2j` into the module.

### Phase 4: Replace internal `c2j` runtime helpers

Remove the remaining internal uses of `c2j` runtime types:

1. Replace `ops.GitExecutionContext` with a local execution context type.
2. Replace `workflow.NewNonRetryableApplicationError(...)` with local typed errors or plain errors.
3. Replace any remaining `ops.OpDependencies`-style behavior with local handler inputs/context.

Notes by area:

- `gha`: most of the `c2j` dependency is context/error plumbing, not core backend logic.
- `gha`: this likely requires new explicit input fields for worktree/git context in `op.yaml`, each with `default` expressions.
- `codex`: artifact handling needs a local replacement because it currently depends on `OpDependencies` to register stdout/stderr artifacts.
- `codex`: it already exposes several contextual fields (`worktree_path`, `workdir_path`, `cell_relative_path`, artifact paths); keep leaning into that explicit-input shape.
- `llm`: once the file types and command wrapper are local, the remaining work is mostly endpoint extraction.

### Phase 5: Migrate tests away from `c2j`

This is the phase most likely to take real time.

#### Unit tests

Rewrite unit tests to use local helpers instead of `c2j` builders and interfaces.

Examples:

- replace `coreops.NewOpDependenciesBuilder()` with local test contexts
- replace `GetOp()` assertions with command/handler-level assertions
- switch `c2j/pkg/file` fixtures to the new local file types

#### Command-level integration tests

Add direct tests for each `cmd/*` binary that:

- feed JSON over stdin
- validate stdout envelope shape
- validate outbox/external artifact behavior

This should replace the current `c2j` integration coverage. These tests should be the primary integration layer for the repo after the refactor.

#### Existing `c2j` workflow tests

The current recipe-worker integration tests are the hardest dependency blocker:

- `pkg/codex/op_integration_test.go`
- `pkg/codex/op_resume_integration_test.go`
- `pkg/codex/op_multi_skill_integration_test.go`
- `test-fixtures/recipe_fixtures_test.go`

Recommended replacement:

1. Rewrite them as black-box integration tests that execute the relevant `cmd/*` entrypoint.
2. Use fixture inputs, `default`-resolved contextual fields, temporary worktrees, and outbox assertions to preserve the behavior being tested today.
3. Delete the old in-process `c2j` worker/recipe tests once equivalent command coverage exists.

If additional end-to-end coverage is still needed later, it should sit above the command layer, not inside this module as `c2j`-bound integration tests.

### Phase 6: Remove the dependency and tighten verification

Once production code and tests are migrated:

1. remove `github.com/colony-2/c2j` from `go.mod`
2. run `go mod tidy`
3. verify there are no remaining imports:

```bash
rg -n "github.com/colony-2/c2j" . --glob '!go.sum'
```

4. run targeted validation:

```bash
go test ./...
go run ./cmd/codex-exec-op
go run ./cmd/gha-run-op
go run ./cmd/gha-runs-op
go run ./cmd/llm-inference-op
go run ./cmd/llm-inference2-op
```

The command checks should be done with realistic stdin payloads so they validate the extension-op contract, not just compilation.
The command checks should use realistic stdin payloads that include the same fields the extension layer will provide after applying `default` expressions.

## Package-by-Package Action List

### Remove or internalize

- `pkg/export`
- `pkg/codex` extension entrypoint code
- `pkg/gha` extension entrypoint code
- `pkg/llm` extension entrypoint code

### Keep public, but replace shared types

- `pkg/adapters`
- any reusable non-op portions of `pkg/llm`, if they are still intended as library API

### Keep stable

- `cmd/*`
- `pkg/*/extension-*/op.yaml`

The `op.yaml` files should remain the compatibility contract for external recipe usage. Prefer to preserve op names and schemas so downstream recipe changes are unnecessary.

## Risks and Decisions to Make Up Front

### 1. Public API break

Removing these APIs is a breaking change:

- `pkg/export`
- `pkg/codex.GetOp()`
- `pkg/gha.GetOp()`
- `pkg/gha.GetRunsOp()`
- `pkg/llm.GetOp()`
- `pkg/llm.GetEnhancedOp()`
- any public API that currently mentions `c2j/pkg/file`

If external consumers exist, this needs a migration note and possibly a version bump.

### 2. Scope of `pkg/codex`, `pkg/gha`, and `pkg/llm`

Decide early whether these packages are:

- still intended as public reusable libraries, or
- only extension-op implementations

If they are only extension-op implementations, move them wholesale to `internal/...` and simplify aggressively.

### 3. Artifact handling contract

`codex` currently uses `OpDependencies` to publish stdout/stderr artifacts. The replacement should be defined early:

- either write outbox files directly from the handler, or
- introduce a small local artifact sink abstraction in the command layer

The choice affects both code shape and tests.

### 4. Test strategy

The root module cannot become `c2j`-free if the current `c2j` worker/recipe integration tests remain in the same module. The intended replacement is direct integration testing of the `cmd/*` entrypoints.

This is the main structural decision in the change.

## Exit Criteria

The change is complete when all of the following are true:

- `go.mod` no longer requires `github.com/colony-2/c2j`
- `rg -n "github.com/colony-2/c2j" . --glob '!go.sum'` returns no hits in the root module
- `pkg/export` is gone
- there are no exported `GetOp()` / `GetRunsOp()` / `GetEnhancedOp()` registration helpers
- the adapter/file API no longer imports `c2j/pkg/file`
- the `cmd/*` binaries satisfy the extension-op stdin/stdout contract without relying on special runtime env vars
- the main module test suite passes without `c2j`

## Recommended First Implementation Slice

To de-risk the refactor, start with the smallest vertical slice:

1. add the local command runner
2. migrate one command end-to-end, preferably `cmd/llm-inference-op`
3. remove its `GetOp()` path
4. prove the command still works with the existing `op.yaml`
5. then apply the same pattern to `llm-inference2`, `gha`, and `codex`

That will validate the architecture before touching the more complex codex artifact flow and the broader adapter/file API migration.

## Command Test Harness Notes

The replacement integration tests should be simple black-box tests around the command contract:

1. construct JSON input fixtures matching the current `op.yaml` schemas
2. run the entrypoint via `go run ./cmd/...` or a prebuilt test binary
3. assert on:
   - exit code
   - stdout JSON envelope
   - stderr on failure paths
   - files written to the outbox
   - any external artifact refs emitted in the envelope

Where commands need contextual values such as worktree path or git metadata, include those as ordinary input fields in the fixture payload. In production, those fields should be populated by `default` expressions in `op.yaml`, not by command-specific env variables.

For `codex`, this should include fixture coverage for:

- stdout/stderr artifact handling
- resume/session flows
- skill installation/materialization behavior

For `gha`, this should include fixture coverage for:

- workflow selector resolution
- local backend error paths
- remote/GitHub input validation paths

For `llm`, this should include fixture coverage for:

- basic generation
- file input handling
- tool execution envelope behavior
