# Embedded GHA integration harness

Run `make -C gha test-integration` from the repository root. The GHA `test` and
`test-fast` targets also run this suite. CI exercises it on Linux and macOS.
Dependencies are Python 3, Go 1.26.10 or newer, Git, Bash, coreutils and Node; Go may need
the repository's existing private-module credentials on the first download. No Docker daemon,
Nix build, GitHub API or model credentials are needed.

`harness_test.go` supplies a temporary Git repository, SQLite JobDB with blob
storage, real c2j recipe/compiler/activity workers, schema-1 or schema-2 allocation facts, and
a synthetic secret provider. `submit`, `step`, `drain`, and `reopen` let tests
pause at durable boundaries and resume with new workers. The pump calls
JobDB's `GetJobForRun`; JobDB owns leasing/readiness and c2j owns child submission
and waits. There is no second scheduler in this fixture.

The basic runtime tests cover real commands, durable child waits, reopen without
duplicate submission, shell failures, and an incompatible platform yielding
before executing a task. Test deadlines and iteration limits detect stalls;
they do not set default production op timeouts.

`act_test.go` compiles `gha-job` once, overrides public selectors to the actual
manifest with that executable, then runs it through c2j. Cases cover fan-out and
fan-in across a SQLite reopen, overlapping children synchronized through a
loopback HTTP barrier, failed/skipped jobs and `always()` reporting, named
secrets/config, and diagnostics hydrated from JobDB after worker deletion.
Each fixture has a Docker CLI sentinel and an unusable Docker socket.

`selection_test.go` uses two additional submitting binaries with controlled release
metadata. A recording broker transport captures original recipes before mapping
only operation nodes to local manifests. Encoded payloads retain the original
selectors, and expansion uses an unversioned helper. Cases cover automatic and
explicit selectors through both entrypoints, source-manifest binding fixtures,
dynamic matrices, source bootstrap, repeated submission, worker deletion and
reopen. Automatic pending work survives movement of mutable fixture package
aliases; completed replay adds no tasks, children, broker submissions or secret
reads. These fixtures establish selection and persistence, without claiming
ordinary published-package preparation or cross-platform availability.

`dependencies_test.go` starts A, B, and a wrapper for C. C waits only for A,
survives a SQLite reopen, and finishes before B has executed. This demonstrates
incremental dependency scheduling with existing native child starts/waits;
neither a batch barrier nor a new wait-any operation is required.

The harness creates a private temporary root under `/tmp`, independent of the
inherited `TMPDIR`, to keep Unix credential-socket paths within macOS's limit.
Worktrees, act state and helper staging files all live below the worker root;
tests can destroy it
without deleting the database or source repository. Secret resolutions are
counted outside SQLite to prove completed replay makes no further reads.

`go test -race ./...` in this module checks the harness/worker processes. It does
not instrument the normally compiled helper. Use `C2OPS_TEST_GHA_RACE=1 go test -race -count=1 ./...` to instrument all five
compiled executables too, including the versioned submitting variants. The [immutable private act dependency](../../nix/act-source.json)
is shared by direct and embedded probes. Building these helpers requires Go
1.26.10 or newer and an authorized private Git route; Nix source builds also
require the [prefetch step](../../NIX_PACKAGES.md#private-gha-build-input).

Make and the native verification hook use the same seven test groups. Each test
runs once, and a catch-all group includes new tests. Make prints each test name
and allows ten minutes per group. The verification hook runs each group with
`-race` and an eight-minute deadline. Both report failure if any group fails.

The separate module keeps c2j/JobDB test dependencies out of the GHA executable's
Nix closure. Tests added here should use public op coordinates and replace only
their selectors with local manifests for compiled helpers. Do not mock a job's
successful execution when testing the act bridge, or template GHA expressions
through c2j. Public JSON dependency results can cross recipe string inputs using
`json_stringify` / `json_parse`; secret references must use c2j's secret contract.

Directories isolate trusted fixtures' files, not untrusted processes. This
suite cannot qualify Docker networking, OS sandboxing, native remote placement,
VM provisioning, or nested virtualization. Those remain provisioned tests.

`workflow_test.go` tests the public `gha` c2j backend and the generated recipes:
dynamic matrices from job outputs, runner profiles, named secret bindings,
failed/skipped/reporting jobs, push filtering, duplicate broker submissions,
restart replay, and unpublished source commits. The v4 protocol test deletes
worker directories before downloading the accepted artifact from JobDB.

CI sets `C2OPS_TEST_GHA_ACTIONS=1` on all three platforms. This additionally
fetches the pinned public upload/download actions and executes their real Node
bundles through the same distributed path. Run it locally with
`C2OPS_TEST_GHA_ACTIONS=1 make -C gha test-fast`. The default fast suite retains
its no-external-actions contract. All fixture op references are authored using
published coordinates and overridden explicitly to the compiled local binaries.


The harness uses c2j v0.0.120, registers a fixture cell with trusted local
embedding authority, and maps that identity to its disposable Git repository
through an explicit Git executable fixture. This accommodates c2j's current
registered-cell contract without a public/private network dependency.

`identity_test.go` executes this test binary inside gha, creates independent
embedded jobs and checks replay, both with ambient isolation and the supplied
Bash unsets. `diagnostics_test.go` covers matrix failure/summary, masked persisted
logs, service-denied/missing named bindings, infrastructure nulls and cancellation
before invocation. `qualification_test.go` preserves the supplied workflow bytes
and checks all three native base demands at admission; it does not run native
workers, setup actions or the complete c2j candidate. `C2OPS_GHA_EVIDENCE_DIR` can
select an absolute scratch directory for actual result/object JSON and logs.

`instances_test.go` checks ordered planned identity against actual submitted child
IDs, duplicate typed matrices, selected requirements, completion order and replay.
Its mixed-conclusion fixture invokes the real compiled executor through a small
wrapper that sets a fixture variable from the assigned shard before execution;
the authored job condition skips the middle instance. It never replaces executor
results. Both forward and reverse completion orders retain the same canonical
matrix-to-child-to-reference association within each run.

The opaque-string fixture transports matrix/label strings through real generated
recipes and verifies a literal expression-like base at admission, then cancels
before invocation because that fixture base has no worker. Three normal base
fixtures likewise test admission and collected identity, not native execution.
Condition-disabled singleton and matrix jobs exercise completed empty collection.
Separately labelled fault fixtures replace helper processes or relax only a test
executor's output schema to reach missing, unknown, malformed and absent-diagnostic
collection paths. Those fixtures are not positive execution evidence.

`negotiation_test.go` exercises actual exit-75 handoff and resume with c2j
v0.0.120, including insufficient replacement allocation, SQLite reopen, deletion
of worker directories, completed-step replay, and public `gha` matrix execution.
`matcher_test.go` compares the production matcher's validation and compatibility
decisions with c2j's matcher; production binaries do not import private runtime
modules. The harness explicitly registers a fixture cell and maps its portable
repository identity to a local source using a Git CLI wrapper that delegates to
real Git. Grouped fixture jobs use distinct step names to avoid collisions in
c2j's native execution-log artifact names.
