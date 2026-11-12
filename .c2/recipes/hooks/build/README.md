# Native verification

`verify.yaml` is loaded from the managed candidate through the standard
`c2.build-hook/v1` caller. It verifies the entire cell even when a build request
names a component directory. Request and directory strings are data; they do
not select executable commands. It accepts the caller's settings artifact
without binding or reading it, so absent and null settings need no artifact.

Each named const operation independently declares its pinned tools. It copies
candidate files (including untracked inputs, excluding `.git`) into a temporary
directory and isolates generated outputs and caches there. No global Git or Go
configuration is changed. Private-module access uses the worker's supplied Git
credentials. Nix process tests select the scratch candidate through
`C2OPS_TEST_FLAKE`, rather than testing the published branch.

GHA source and embedded runtime tests belong here in c2ops. Consumers use the
delivered operations; they do not duplicate these suites. The candidate hook
checks this branch before merge and reports failure to the build caller.
The `gha` and `gha_runtime` commands in `verify.yaml` explicitly pass `--race`.
Both GHA suites then run with `-race` using the candidate's pinned GHA compiler.
Omitting `--race` produces a failed task receipt and failed aggregate before
any compiler or suite runs, so the pre-merge hook cannot silently lose races.
The runtime task also sets `C2OPS_TEST_GHA_RACE=1` so all three separately
compiled helpers are instrumented, and includes their build identities in its
JSON receipt. A missing compiler blocks the tests instead of using worker Go.

Each operation exports a log and structured result. Ordinary check failures
produce `failed` evidence and allow subsequent independent operations to run.
The final report validates every result and its log digest before returning
`passed`. Infrastructure failures before evidence export fail recipe execution;
they cannot produce a passing report. Cancellation terminates the active
process group. A command has a 15-minute wall timeout and Go test binaries have
an eight-minute timeout. Package preparation is managed by c2j.

The task mapping follows `.github/workflows/test.yml` and all operation
Makefiles:

| Workflow/check behavior | Hook task |
| --- | --- |
| `make check-manifests` | `manifests` |
| `make test-tools` | `tooling` (including hook failure regressions) |
| llm / llm2 native Go contracts | `llm`, `llm2` |
| gha / gha-many native Go contracts | `gha`, `gha_many` |
| Nested gha/integration SQLite/runtime/host-process fixtures | `gha_runtime` |
| Codex, skill.run, CLI compatibility, test-fixtures and manifest contracts | `codex` (with-codex wrapper, entire module) |
| rule_gate Go unit/process contracts | `rule_gate` |
| aider / litellm / pydantic packaged process suites | `aider`, `litellm`, `pydantic` |
| jev / kimi Python unit/process contracts | `jev`, `kimi` |
| Native flake metadata without building | `nix_metadata` |
| Installed native package process contracts, payload architecture, Python dependency/provider contracts and CLI versions | `nix_process` |

`pydantic` builds the current scratch checkout's native package and package check
with `--max-jobs 1 --cores 1 --no-link`, retaining normal substitutes and
sandboxing. In that same task it runs the deterministic suite against source
and the installed payload/launcher and records output and closure identities.
Local store paths establish local verification, not portable delivery.

`nix_process` retains cache-only realization for the other operation process
checks. Publication, immutable selectors and clean-worker ordinary resolution
remain post-merge obligations (P02/P03).

Held coverage: other platform matrix entries; Docker-based minimal Codex worker
image tests; opt-in public GHA/service actions and remote CI; the four real Nix
cache-publication tests gated by `C2OPS_TEST_NIX_PUBLICATION`; package publication,
cache download certification and pinning; branch promotion, signing, release,
and deployment. These are held even if credentials or Docker are available.
Mocked publication-planning/tooling tests and local GHA runtime tests remain
included. Existing Actions workflows and suites are unchanged.

For bounded caller/artifact regression checks, run
`python3 scripts/verify_hook_harness.py --out /tmp/c2ops-hook-tests`.
It fixtures the independent checks and executes the report operation through
c2j's real local runtime. `--case native` additionally runs the actual manifest
check with operation-declared tools. These harness cases do not replace the
full check baseline. Infrastructure and evidence failures are covered by
`scripts/test_verify.py`, included in `tooling`.

For the actual GHA checks through this branch's hook, run
`python3 scripts/verify_hook_harness.py --case gha --out /tmp/c2ops-gha-hook-tests`.
It passes through tooling, GHA source and embedded runtime tasks, including
their declared tools and the real final report. Other tasks use explicit
fixtures, so this bounded run proves the affected tasks and hook wiring, not
a new full-cell baseline. It snapshots current working files, including
uncommitted changes; the candidate metadata records their parent HEAD.

The manifests task also runs `uv run scripts/check-manifest-schemas.py`, with
its independently declared `jsonschema==4.26.0` dependency. It applies the
Draft 2020-12 meta-schema assertion from `nix/checks.nix` to both input and
output schemas of every `nix/ops.json` entry. This runs natively even while
Nix derivation builds remain held.

The Pydantic task invokes `make -C pydantic test`, the same entrypoint as CI.
It runs mandatory Go packaged process tests and source Python binding tests;
the installed-byte Python suite, launcher identity and closure checks remain separate.
Both suites run even when one fails, and either failure fails task and aggregate evidence.
