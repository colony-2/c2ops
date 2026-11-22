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
The runtime task separates artifact/history workflows, other submitted workflows,
the matching-worker matrix, Act, instances, and diagnostic/identity fixtures into
selected processes, with a complementary process covering all remaining and new
tests. Seven complementary `-skip` and `-run` processes cover every test exactly
once. Each retains the eight-minute limit; any failure fails the task. This avoids
exhausting the cumulative deadline, which also includes TestMain helper builds.
Verbose output retains per-test timings. Regression checks inject a failure into
every partition and verify dispatch coverage and failure reporting.
Omitting `--race` produces a failed task receipt and failed aggregate before
any compiler or suite runs, so the pre-merge hook cannot silently lose races.
The runtime task also sets `C2OPS_TEST_GHA_RACE=1` so all separately
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
| `make test-tools` | `tooling` (including real operation Make entrypoints and hook failure regressions) |
| llm / llm2 native Go contracts | `llm`, `llm2` |
| gha / gha-many native Go contracts | `gha`, `gha_many` |
| Nested gha/integration SQLite/runtime/host-process fixtures | `gha_runtime` |
| Codex, skill.run, CLI compatibility, test-fixtures and manifest contracts | `codex` (with-codex wrapper, entire module) |
| rule_gate Go unit/process contracts | `rule_gate` |
| GitHub release Go unit/process contracts | `github_release` |
| Go release unit, package and real SDK/source contracts | `go_build` |
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

Prepare missing candidate inputs through the existing source-build route before
retrying a cache-only failure. In the source builder, prepare the pinned private
GHA input and the inputs reported missing by this repair's native hook:

```sh
python3 scripts/prefetch-act.py
nix build --max-jobs 1 --cores 2 --no-link \
  .#gha.buildCompiler .#gha .#gha-workflow \
  .#github-release .#release-pack .#npm-publish .#go-build
```

Codex's manifest fixture exercises every
Go operation, so preparing only `codex` and `skill-run` does not satisfy it.
Use the candidate checkout, retaining the pinned compiler and normal sandboxing;
do not substitute the published branch or waive the dependent tests. These local
store paths must be available to the verification worker; a source build does
not establish cache publication or availability on another worker.

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

The `go_build` task runs the [Go release builder checks](../../../../go_build/TESTING.md):
unit tests, candidate Nix package/manifest validation and the real SDK/executable
suite through that package. This task builds its own op locally; the op itself
can only acquire prebuilt mapped SDK closures. Both Go 1.26.1 and 1.26.8 must be
available through prepared paths or the worker caches. Private Git/proxy and
other native hosts remain explicitly unverified when their fixtures are absent.
The real suite runs verbosely so the exported log retains the assembly escape
controls, their executable results and explicit skips for missing prerequisites.

The tooling suite discovers every immediate `*/op.yaml` directory independently
of `OP_DIRS` and requires `build`, `test`, and `install-test-deps` in its Makefile.
It probes GNU Make dry runs and checks for side effects in a scratch inventory.
Direct Go source tests alone did not catch the missing `github_release/Makefile`;
the former synthetic dispatch fixtures supplied their own complete Makefiles.

For the real tooling task and report through the local runtime, run
`python3 scripts/verify_hook_harness.py --case tooling --out /tmp/c2ops-tooling-hook`.
Run `--case tooling-missing-makefile` with a separate output directory to omit
only `github_release/Makefile` from the runtime snapshot. That case requires
failed tooling and aggregate evidence; the harness succeeds when the expected
failure is observed. Other tasks are explicit fixtures in both cases.

These cases require a c2j `file_sources` importer that preserves executable bits
in the initial fixture Git tree. Older importers write files as `0644`, losing
the npm fixture shim's executable bit and failing its projection test. Both
cases pass locally using the c2j owner's delivered `unstable-1b22ca4` CLI:
positive tooling and aggregation pass; the missing-Makefile control exports
failed tooling and aggregate results. Both exported-artifact readers pass,
and both seeded repositories remain clean with zero new commits. Other tasks
are fixtures, so these results establish tooling/report wiring only.

A native hook pass still covers Linux arm64 only. Go on macOS ignores
`SSL_CERT_FILE` by default; the GitHub release client now loads an explicit
bundle there. The helper's Linux tests exercise that branch, but native macOS
installed tests and the existing three-platform CI matrix remain required to
validate the platform-specific repair.

The `opentofu` task builds the unchanged production package/process contract,
prepares the test helper pinned to c2j `b399a9afb40d`, and runs the real OpenTofu
and Google SDK fixtures plus installed/native recovery, replay, secret ceilings
and cancellation. It retains its own log/receipt and is required by aggregation.
`python3 scripts/verify_hook_harness.py --case opentofu --out /tmp/c2ops-tofu-hook-tests`
executes this task with the candidate's declared tools and real aggregation;
other tasks are fixtures. GHA's current partitions and eight-minute limits stay
unchanged. Live Google qualification remains separately gated.

Publication is confirmed by a person after the job containing the managed merge
has completed. There is no automatic publish hook or additional observation
credential requirement. Pre-merge review assesses the candidate and its local
checks; it cannot require evidence from the release that merge will trigger.
The existing main-branch Actions workflow remains the release path.

After job completion, ask the operator to confirm the actual merged revision's
three native test and verification jobs, signed empty-store substitution,
retained closures and promotion disposition. Then prepare and invoke the
full-commit `opentofu-up` selector. See the
[publication confirmation steps](../../../../NIX_PACKAGES.md#opentofu-publication-confirmation).
A merge or skipped publish report does not establish publication. Live Google
qualification remains separate and requires its own disposable target and
credential delegation.

The qualifier runs `TestNativePackagedRecovery` and its complement in separate
processes, each retaining the eight-minute Go test limit. The isolated native
fixture measured 370 seconds; combining it with authentication renewal and lock
checks exceeded the shared alarm. The complement includes future tests, and
partition/failure regressions require every current test exactly once and failure
from either process (or the scanner suite) to fail qualification.

The non-constant `opentofu_credentials` task is separate. An absent/null public
`settings.variables.opentofu_google` records pending without Google credential
resolution or traffic; pending local candidate verification is not live acceptance.
Its baseline literal grant retains `gitvend_base_token`: workspace preparation
clones the private repository before running even an unconfigured command. An
empty grant blocked that clone in the managed route; local seeded-repository
fixtures did not exercise this prerequisite. Permission harness cases now probe
the Git grant separately from Google grants and run the actual pending command.
Configured malformed, denied or unsuccessful checks fail aggregation. The
[operator script guide](../../../../scripts/opentofu-google-testing.md) covers
private setup, exact leaf/caller ceilings, administrative hook grants with source
trust, registration, refresh and cleanup. Successful receipts bind candidate/base,
production package, public profile and fixed public-log digests. Fresh invocation
is required; completed replay cannot qualify credentials.
