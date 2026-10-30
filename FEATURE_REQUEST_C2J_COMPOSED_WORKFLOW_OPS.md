# c2j: composition and incremental child waits for workflow-backed ops

Verified against c2j `v0.0.110` and JobDB `v0.0.65` while implementing the
[GHA orchestration plan](PLAN_GHA_C2J_ORCHESTRATION.md). This is an upstream
contract request, not an implemented `backend: c2j` interface.

The embedded c2ops harness already proves real child submission, suspension,
replay, per-node platform admission, secret resolution, and durable object
publication. Execution needs is the correct placement mechanism. The remaining
gaps below concern composition and workflow control, not a new scheduler inside
an extension subprocess.

## Trusted packaged composition with reference-preserving inputs

`pkg/ops/extensions.ExtensionOpSpec` currently describes subprocess execution
(`run`, `command`, schemas, dependencies); it has no packaged recipe entry point.
Compiler composite-input support does not establish a selector-resolved,
recipe-backed extension contract.

Allow a resolved package to identify a versioned wrapper recipe, helper selectors
and result projection. Load and record that trusted recipe before materializing
secrets. Bind normal typed inputs, object references and arbitrary named secret
maps into the wrapper/children without resolving them in a planner process.
Preserve caller execution scope, permission checks, cancellation and artifact
visibility. Record the package/recipe identity for replay.

Recipe `input_schema` also cannot currently express an arbitrary JSON map with
secret-marked leaves: its `object` type denotes a durable c2j object. This works
for public fixture data via `json_stringify` / `json_parse`, but that is not an
acceptable secret transport. Either support structured recipe schemas with
reference forwarding or allow a trusted composition compiler to declare concrete
per-secret scalar fields. Do not execute YAML emitted by ordinary op output.

Acceptance: one public op call produces the same graph as an explicit wrapper;
secret values are fetched only for live executor invocations; no values appear
in the parent plan or public history; completed replay is offline from secret
storage; arbitrary secret names survive parent/child boundaries.

## Durable wait for any child change, plus cancellation

The inspected `recipe.await_result[_soft]` APIs target one job/goal. `child_group`
and multiple-recipe waits join groups; soft current-status inspection is useful
but is not a durable wait for a change among a set. JobDB's `DependencyWait` has
a dependency list and deadline, without an exposed any/all mode. No public
recipe-level wait-any/cancel operation was found in `pkg/ops/recipe.GetOps`.

Expose a generic durable wait-set operation that resumes when any relevant child
changes/terminates, returning a recorded observation for deterministic replay.
Avoid the lost-wakeup window between status inspection and registering a wait.
Add idempotent, scoped child cancellation with observable acknowledgement and
failure results, suitable for parent cancellation and matrix fail-fast.

Acceptance: independent A and slow B start; C needs only A and becomes runnable
before B finishes. Reopening the controller does not duplicate children or change
the accepted completion observation. Cancellation reaches active children and
survives controller restart. Failure-reporting jobs still see dependency outcomes.

A batch barrier is sufficient for the current three-job fixture, but cannot be
advertised as general dependency scheduling. The extension binary should not
compensate with a nested engine, ambient JobDB credentials or a polling service.

## Separate provisioner work

VM isolation and nested virtualization still need generic allocation/demand
fields and provisioner support as specified in plan section 5.3. Current execution
requirements expose packages, image, platform and resources. Local admission
tests prove refusal of incompatible platforms; they cannot prove VM/Docker
capabilities or remote ARM/AMD placement. GitHub OIDC remains out of scope.
