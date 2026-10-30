# Embedded GHA integration harness

Run `make -C gha test-fast` from the repository root. The existing `make test`
target also runs this suite in both native Linux CI jobs (ARM64 and AMD64).
Dependencies are Go, Git, Bash, coreutils and Node; Go may need the repository's
existing private-module credentials on the first download. No Docker daemon,
Nix build, GitHub API or model credentials are needed.

`harness_test.go` supplies a temporary Git repository, SQLite JobDB with blob
storage, real c2j recipe/compiler/activity workers, native allocation facts, and
a synthetic secret provider. `submit`, `step`, `drain`, and `reopen` let tests
pause at durable boundaries and resume with new workers. The pump calls
JobDB's `GetJobForRun`; JobDB owns leasing/readiness and c2j owns child submission
and waits. There is no second scheduler in this fixture.

The basic runtime tests cover real commands, durable child waits, reopen without
duplicate submission, shell failures, and an incompatible platform yielding
before executing a task. Test deadlines and iteration limits detect stalls;
they do not set default production op timeouts.

The separate module keeps c2j/JobDB test dependencies out of the GHA executable's
Nix closure. Tests added here should use public op coordinates and replace only
their selectors with local manifests for compiled helpers. Do not mock a job's
successful execution when testing the act bridge, or template GHA expressions
through c2j. Public JSON dependency results can cross recipe string inputs using
`json_stringify` / `json_parse`; secret references must use c2j's secret contract.

Directories isolate trusted fixtures' files, not untrusted processes. This
suite cannot qualify Docker networking, OS sandboxing, native remote placement,
VM provisioning, or nested virtualization. Those remain provisioned tests.
