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

`nix_process` invokes the same check scripts as `nix/checks.nix` directly and
realizes only native cached package outputs with local and remote builds
disabled. A missing cache output is a failed check, not a skip. It does not
build the `runCommand` check derivations: those need local builds, prohibited
by the worker's `max-jobs=0` policy. Metadata and installed process checks
remain mandatory; no policy override is attempted.

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

The manifests task also runs `uv run scripts/check-manifest-schemas.py`, with
its independently declared `jsonschema==4.26.0` dependency. It applies the
Draft 2020-12 meta-schema assertion from `nix/checks.nix` to both input and
output schemas of every `nix/ops.json` entry. This runs natively even while
Nix derivation builds remain held.
