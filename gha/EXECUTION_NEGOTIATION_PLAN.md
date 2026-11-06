# GHA per-job execution negotiation

Status: implemented with opt-in negotiation and catalog `2026-11-06.1`, aligned with the
[implemented execution negotiation spec](../SPEC-Extension-Execution-Negotiation.md)
and its [guide](../GUIDE-Extension-Execution-Negotiation.md). The protocol defines
allocation schema 2 and the exit-75 handoff contract, including read-only
prepared-dependency metadata. Verify the pinned c2j runtime against that contract
when upgrading; the integration suite pins c2j v0.0.120. Production binaries use
a local matcher checked against c2j by conformance tests, keeping private runtime
modules out of the Nix dependency closure. No new JobDB primitive or recipe schema version is needed.

## Recommended design

Use one `gha-job` extension task per concrete Actions job (one task per matrix
assignment). Before doing any execution work, the task derives its requirements
from a built-in runner catalog or an explicit runner profile and compares them with
`C2J_EXECUTION_ALLOCATION`. If the allocation is insufficient, it writes
`{"execution_required": {...}}` and exits 75. c2j suspends that child job and
the dispatcher provides a suitable executor. The same task starts again with
the same logical inputs and new allocation facts.

The existing `backend: c2j` architecture already supplies the required task
boundary. Keep its dependency scheduling, matrix expansion, child identities,
source snapshots, secret bindings, and result collectors. Add negotiation to
the single-job executor. The workflow coordinator does not need to request a
combined environment for the entire workflow.

This first implementation targets `gha` with `backend: c2j`, its
`gha-workflow` helper, and direct `gha-job` calls. Local act execution, GitHub
remote execution, and `gha-many` can be considered separately.

## Current implementation and insertion points

| Component | Current behavior | Proposed change |
| --- | --- | --- |
| `internal/workflow/workflow.go` | Defines profiles, validates inputs, submits logical job recipes with dependency waits | Add catalog resolution, typed negotiated profiles, and placement mode |
| `internal/jobexec/prepare.go` | Evaluates job conditions, dynamic matrices, and `runs-on` after dependencies complete | Reuse the evaluation logic for a read-only, single-assignment preflight |
| `internal/workflow/expand.go` | Matches runner profiles and emits one child recipe per assignment, with `execution_needs` on its `execute` node | Carry negotiated requirements in the executor payload for negotiated jobs |
| `job/main.go` | Reads/verifies the workflow, then creates outbox directories and logs before calling act | Perform validation and negotiation before the first outbox write |
| `internal/jobexec/job.go` | Creates runtime directories and an artifact server, maps labels to act host mode, and executes the job | Execute an already validated assignment after allocation acceptance |
| `internal/extensioncmd/command.go` | Supports success and ordinary error envelopes | Add a distinct execution-required control result and exit 75 |

Today, `runs-on` labels all map to act's `-self-hosted` mode. They do not verify
the assigned host. Negotiation closes that gap for negotiated calls, while the
dispatcher remains responsible for actually supplying the reported environment.

## Public configuration and migration

Introduce `execution_mode: static | negotiate` on `gha`'s c2j inputs and on
`gha-workflow`. Initially default to `static` to preserve existing recipes and
support runtimes that do not implement negotiation.

In `static` mode, profiles retain their existing `execution_needs` contract.
In `negotiate` mode, profiles use `execution_required` with the new protocol
fields: `base`, `resources`, and `capabilities`, or the catalog-backed
`runner_type` shorthand defined below. Profiles become optional
in negotiated mode: the built-in catalog handles supported GitHub labels. Require supplied profiles
to match the selected mode; reject mixed legacy/new requirement objects.

Example of the proposed GHA input, using the currently documented fields:

```yaml
sequence:
  - id: ci
    op: nix:github:colony-2/c2ops/published#gha
    inputs:
      backend: c2j
      execution_mode: negotiate
      workflow: .github/workflows/ci.yml
      runner_profiles:
        - labels: [ubuntu-24.04]
          execution_required:
            base: ubuntu-26.04-amd64
            resources: {cpu: "4", memory: "16Gi"}
            capabilities: [network]
        - labels: [macos-15]
          execution_required:
            base: macos-sequoia-15.7.7-arm64
            resources: {memory: "16Gi"}
  - id: result
    op: recipe.await_result
    inputs:
      target: {job: '${{ sequence.ci.outputs.job_id }}'}
      wait: {on: job}
```

The example shows optional profile overrides using our actual bases. Ordinary
supported workflows need only `execution_mode: negotiate`; their `runs-on`
labels resolve through the catalog described below.

Explicit profile matching remains deterministic: every evaluated `runs-on`
label must be present in a single profile, using the existing case-insensitive
label comparison. Multiple matching profiles are an error. One matching profile
takes precedence over the catalog; zero matches falls back to the catalog.
Base matching itself follows the protocol's case-sensitive rules.

Existing `platform`, `image`, and `packages` requirements must not be blindly
copied into the new envelope. In particular, `linux/arm64` does not identify an
Ubuntu release, and an image name is not necessarily a dispatcher base name.
Migration requires explicit base mappings. Legacy recipe-level package setup
stays outside extension negotiation; reject `packages` in negotiated requirement
objects rather than silently dropping or translating it.

For direct `gha-job` use, add `execution_mode` plus optional `execution_required`
and `runner_profiles` inputs. Supplying either requirement source opts into
negotiation; reject supplying both. `execution_mode: negotiate` without either
uses the built-in catalog. Direct profiles use the negotiated profile shape. A
direct fixed requirement object is an explicit caller assertion about the job's
environment, equivalent to a caller-provided mapping. Calls with neither a requirement source nor negotiated mode retain the existing
caller-managed placement behavior during migration.

Generated negotiated calls carry the catalog version, mapping provenance,
resolved requirement object, and evaluated labels in their opaque public payload. The executor re-evaluates the selected
assignment's labels against the frozen workflow and verifies that they match
the planned labels. Keep these placement fields distinct from secrets and
reject conflicting top-level versus payload requirement sources.

## Built-in GitHub runner catalog

Implement a versioned catalog in `internal/runners`, embedded in the GHA binary.
Separate GitHub runner descriptions (label, OS release, architecture, resource
class) from our inventory and compatibility decisions. A known GitHub label can
still be unsupported locally. No network lookup is needed to select a machine.

Our initial inventory is exactly:

| Base | OS | Architecture | Sizing |
| --- | --- | --- | --- |
| `ubuntu-26.04-arm64` | Ubuntu 26.04 | ARM64 | Independently requested CPU/memory |
| `ubuntu-26.04-amd64` | Ubuntu 26.04 | AMD64 | Independently requested CPU/memory |
| `macos-sequoia-15.7.7-arm64` | macOS Sequoia 15.7.7 | ARM64 | Independently requested CPU/memory |

Use exact base names in resolved requests. CPU and memory are resource minima,
not additional machine names or fixed SKUs. Available CPU/memory sizing does not
imply support for GPUs, Intel emulation, nested virtualization, or a particular
Xcode/SDK version.

### Initial compatibility policy

Use an explicit allowlist of substitutions. The following is a proposed local
policy to qualify with integration workloads, not a claim of image equivalence:

| GitHub label | Local result | Classification |
| --- | --- | --- |
| `ubuntu-26.04` | `ubuntu-26.04-amd64` | Same OS release and architecture |
| `ubuntu-26.04-arm` | `ubuntu-26.04-arm64` | Same OS release and architecture |
| `ubuntu-24.04` | `ubuntu-26.04-amd64` | Allowed newer-Ubuntu substitution |
| `ubuntu-24.04-arm` | `ubuntu-26.04-arm64` | Allowed newer-Ubuntu substitution |
| `ubuntu-latest` | Resolve its pinned catalog alias, then apply that entry's mapping | Currently maps to the available AMD64 Ubuntu base |
| `macos-15` | `macos-sequoia-15.7.7-arm64` | Same major release and architecture |
| `macos-14` | `macos-sequoia-15.7.7-arm64` | Explicit legacy alias; allowed newer-macOS substitution |
| `ubuntu-22.04`, `ubuntu-22.04-arm`, older Ubuntu labels | Unsupported by default | Larger release gap; explicit profile required |
| `macos-26`, `macos-latest`, `xcode-27` | Unsupported by default in this catalog snapshot | Available base cannot satisfy the newer OS/toolchain expectation |
| Windows labels | Unsupported | No Windows base or executor |
| Intel macOS labels, including `macos-15-intel` and `macos-26-intel` | Unsupported | No Intel macOS base; no transparent emulation |
| Supported Ubuntu larger-runner names and ARM64 macOS XLarge labels | Resolve through the size catalog below | Existing base plus CPU/memory minima |
| `ubuntu-slim`, GPU runners, unrecognized custom runner names | Explicit profile required where compatible | Execution model or machine requirements need separate qualification |
| Unknown or future labels | Unsupported until catalog update or explicit profile | No prefix guessing |

The GitHub label/architecture descriptions and alias snapshot should be sourced
from the [GitHub runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
and its linked runner-image manifests. Checked 2026-11-05: the reference links
`ubuntu-latest` to Ubuntu 24.04 and `macos-latest` to macOS 26. Retain `macos-14`
as an explicitly documented legacy entry even when it leaves the current
standard-runner list. These source facts and our compatibility choices are
separate catalog fields.

Do not generically map `ubuntu-*` or `macos-*`, choose a base by string distance,
or assume that every newer OS is compatible. A Ubuntu 24.04 job on 26.04 can
observe different libraries; a macOS 14 job on 15 can observe different SDKs.
Record the authored label, selected base, and substitution reason in stderr and
successful diagnostics. Jobs that require exact releases must use an exact
profile requirement and an inventory that can supply it.

Pin `*-latest` aliases to a catalog release; never reinterpret them by asking
GitHub during a retry. In particular, do not silently redefine `macos-latest` to
mean our newest available Mac. A caller that accepts Sequoia for that label can
supply an explicit ARM64 profile override. Explicit profiles may change the
release policy but must not silently change the OS family or architecture of a
recognized GitHub label. Windows and Intel macOS remain unsupported with this
inventory, including when a contradictory profile is supplied.

### Resource defaults and overrides

Keep our sizing policy separate from GitHub's hardware descriptions. Proposed
initial defaults are CPU `4`, memory `16Gi` for standard Linux entries, and CPU
`3`, memory `8Gi` for standard ARM64 macOS entries. These are local provisioning
minima, not promises of identical GitHub hardware/performance. Do not infer
repository visibility to choose a size. A matching explicit profile can request
any supported CPU/memory minima and declare additional capabilities.

An explicit profile supplies the complete requirement object; it does not
partially merge with a catalog entry. This preserves predictable precedence.
For known labels, validate its base against the supported inventory and the
label's OS/architecture constraints. Version substitutions are intentional
caller policy. Unknown custom labels require an explicit profile with either
complete requirements or a valid catalog shorthand.

Profiles may also use the `runner_type` shorthand below instead of a complete
`execution_required` object. Reject profiles containing both; expand the shorthand
to a complete requirement object before applying the usual validation.

The catalog must not imply that all of GitHub's preinstalled software is present.
Use the reported prepared-dependency list for dependency diagnostics. It is
not a complete inventory of host software or the active version of each command. Toolchain setup belongs to the
base image, existing dependency setup, or workflow steps; OS family and enough
RAM alone do not supply it. Do not encode package requests as capabilities.

### Larger-runner sizes

Represent a larger runner as `(image, architecture, size)` and resolve it into
one existing base plus CPU and memory minima. Include these sizes in the first
catalog release. The following hardware sizes come from GitHub's
[larger-runner reference](https://docs.github.com/en/actions/reference/runners/larger-runners),
checked 2026-11-05:

| CPU | AMD64 Ubuntu memory | ARM64 Ubuntu memory |
| --- | --- | --- |
| 2 | `8G` | `8G` |
| 4 | `16G` | `16G` |
| 8 | `32G` | `32G` |
| 16 | `64G` | `64G` |
| 32 | `128G` | `128G` |
| 64 | `256G` | `208G` |
| 96 | `384G` | No catalog size |

Each supported AMD64 row maps to `ubuntu-26.04-amd64`; each ARM64 row maps to
`ubuntu-26.04-arm64`. Apply the existing Ubuntu image compatibility policy before
selecting a size. Store explicit table entries, including the ARM64 64-core
exception; do not calculate RAM as four times CPU or assume every size exists
on every architecture. The values use decimal `G` to encode the reference's GB
figures; they are distinct from the deliberately rounded local `Gi` defaults
for standard runners. Preserve the chosen quantity units in fixtures.

| macOS larger label | Local base | CPU | Memory |
| --- | --- | --- | --- |
| `macos-15-xlarge` | `macos-sequoia-15.7.7-arm64` | `5` | `14G` |
| `macos-14-xlarge` | Same base, approved newer-release substitution | `5` | `14G` |
| `macos-latest-xlarge` | Same base via separately pinned `macos-15-xlarge` alias | `5` | `14G` |
| `macos-26-xlarge`, `xcode-27-xlarge` | Unsupported newer OS/toolchain | — | — |
| `macos-14-large`, `macos-15-large`, `macos-26-large`, `macos-latest-large` | Unsupported Intel macOS | `12` | `30G` |

The larger-runner reference currently marks 15 as latest for these larger Mac
labels. Pin each alias independently; do not derive `macos-latest-xlarge` from
the standard `macos-latest` alias. Revalidate alias targets on catalog updates.
The Mac XLarge mapping provides CPU/memory compatibility, not a guarantee of
the same Apple chip or GPU acceleration. GPU-dependent workloads need an
explicit supported capability. Windows and GPU-specific larger types remain
unsupported by the built-in mapping.

This initial translation requests CPU and memory. It does not copy advertised
SSD sizes into every request: disk capacity is a separate
`resources.ephemeral-storage` requirement when a profile specifies it. Report
this scope as CPU/memory compatibility in the catalog metadata.

### Larger-runner names and custom aliases

GitHub's Linux larger-runner labels come from configured runner names; a name
such as `ubuntu-24.04-16core` is not a globally reserved hardware type. GitHub
documents that naming example in
[running jobs on larger runners](https://docs.github.com/en/actions/how-tos/manage-runners/larger-runners/use-larger-runners).
Our catalog will explicitly support the following naming convention for
convenience, while profiles can override it for an organization's actual setup:

- AMD64: `ubuntu-{24.04,26.04,latest}-{2,4,8,16,32,64,96}core`.
- ARM64: `ubuntu-{24.04,26.04,latest}-arm-{2,4,8,16,32,64}core`.

These braces describe finite sets, not glob matching at execution time. Generate
and test exact catalog entries. The ARM `latest` aliases are explicitly local
conventions, pinned to the supported ARM Ubuntu image mapping. Do not advertise
these convenience names as GitHub-defined labels. Unknown size suffixes require
a profile; no unbounded suffix parser should silently accept them.

For arbitrary organization names, add a profile shorthand that reuses the size
catalog without duplicating its numeric requirements:

```yaml
runner_profiles:
  - labels: [build-linux-big]
    runner_type:
      image: ubuntu-24.04
      architecture: amd64
      size: 32core
  - labels: [build-arm-big]
    runner_type:
      image: ubuntu-26.04
      architecture: arm64
      size: 64core
```

`runner_type` accepts a versioned catalog image, an explicit architecture, and a
size key. For Mac it accepts `image: macos-15`, `architecture: arm64`, and
`size: xlarge`. Invalid image/architecture/size combinations are configuration
errors. A profile needing arbitrary resource values or capabilities
uses the existing complete `execution_required` form instead.

For example, `runs-on: ubuntu-24.04-16core` resolves to:

```json
{
  "execution_required": {
    "base": "ubuntu-26.04-amd64",
    "resources": {"cpu": "16", "memory": "64G"}
  }
}
```

That envelope is emitted only if the current allocation is insufficient.
Freeze the resolved numeric requirements, size key, and catalog version in the
child payload. A compatible base with too little memory still needs a handoff;
a larger allocation satisfies the minimum without changing the selected type.

### Resolution and persistence

Resolve after job conditions and matrix expressions, once `runs-on` is concrete:

1. Validate label syntax and supported `runs-on` shape.
2. Try explicit profiles; reject ambiguity or contradictory OS/architecture.
3. Otherwise resolve an exact catalog label or versioned alias. Allow a string,
   one-element label array, or a `runs-on: {labels: ...}` object containing only
   that single label. Normalize the labels-only object before act evaluation;
   add this supported shape to shared validation and preparation. Multi-label
   self-hosted selectors require an explicit profile; never discard extra labels.
   Runner-group objects remain unsupported until group semantics are implemented.
4. Return either a complete requirement object plus provenance or an ordinary
   unsupported-runner error with the label, reason, and available bases.
5. Persist that resolution in the child payload before submission. Resume uses
   the stored requirements and catalog identity, even after a catalog upgrade;
   it does not recompute `latest` with a newer table. Direct calls also need a
   pinned op version or persisted resolution to guarantee this behavior.

Only a supported mapping with an insufficient allocation produces exit 75.
Unsupported labels fail before job execution rather than waiting forever for a
base our dispatcher cannot provide. Catalog updates require review and fixtures
for added labels, changed aliases, compatibility choices, and resource sizes.

## Prepared dependencies are read-only allocation facts

`C2J_EXECUTION_ALLOCATION.packages` is an informational array of dependency
strings, for example:

```json
{
  "schema_version": 2,
  "base": "ubuntu-26.04-arm64",
  "resources": {"memory": "16Gi"},
  "capabilities": ["network"],
  "packages": ["uv:ruff==0.12.0", "nix:nixpkgs#git"]
}
```

It contains the full deduplicated set successfully prepared for this invocation across
repository, recipe, enclosing/current node, job, and extension dependency
declarations. Multiple prepared versions may appear. Existing scope precedence
selects the executable used by a bare command; array order or membership alone
must not be used to choose its version. This list does not enumerate arbitrary
software installed on the host.

The GHA behavior is:

1. Decode the string array when present. An absent field is unknown; a present
   empty array means no reported prepared dependencies, not an empty host.
2. Expose relevant entries in dependency diagnostics. Do not infer that an
   executable is unavailable because it is absent from this list, or that a
   particular version wins on `PATH` merely because it appears in the array.
3. Compare only base, resources, and capabilities when deciding whether to emit
   an execution-required response. Prepared dependencies do not contribute to
   that comparison and are never copied into the control envelope.
4. After placement is accepted, use existing executable validation or ordinary
   step execution to detect missing tools. An unavailable tool produces an
   ordinary validation/execution error when needed, not a package-driven handoff.
   Do not reject a job because a tool installed by a later setup step is initially
   absent, or disguise a request for a package as a capability.
5. Continue declaring dependencies through recipes or the extension manifest.
   Base preparation and explicitly authored workflow setup steps can also
   supply software outside this reported list. GHA adds no package-installation
   or package-negotiation loop.

Reject `packages` in negotiated profile requirements and direct
`execution_required` inputs. Keep allocation facts distinct from the writable
requirement type so serialization cannot turn the informational array into a
request. No new package-needs input or package-constraint language is introduced.

Read the newly reported array on every invocation. Preserve the existing
recipe/manifest dependency declarations through normal c2j setup and replay;
do not turn an earlier allocation's array into new declarations or scheduling
requirements.

## Single-job execution sequence

Refactor the executor into a read-only preflight followed by execution:

1. Decode inputs and the opaque payload. Preserve separately supplied secrets.
2. Resolve the workflow path safely, read the frozen source, and verify its hash.
3. Validate supported features, dependency results, event context, and the
   explicit matrix assignment. Reuse act's evaluator and existing validation
   instead of implementing a second expression engine.
4. Evaluate the job condition. A skipped job returns the existing skipped result
   without requesting a runner. Preserve the current diagnostics schema by
   emitting minimal skipped diagnostics; no act execution is needed.
5. Resolve and validate the complete requirement object. For generated calls,
   verify the planned assignment/labels; for direct calls, resolve the selected assignment through the same
   profile/catalog resolver. Never fan out inside `gha-job`.
6. Decode allocation schema 2 and compare every required field. Missing
   allocation, malformed facts, or unsupported schema are ordinary compatibility
   errors in negotiated mode. Missing individual facts follow protocol rules.
7. If insufficient, return the control result immediately, before creating logs,
   runtime directories, artifact servers, fetching actions, or invoking steps.
8. If sufficient, create diagnostics and artifact state and execute the job with
   act. Return the existing success envelope, conclusion, outputs, and objects.

The existing `Prepare` function handles all matrix assignments, whereas the
executor receives exactly one. Extract shared evaluation helpers so both paths
use consistent defaults and dependency/status semantics. Preserve rejection of
missing or invalid explicit matrix assignments. Placement evaluation must not
depend on secrets or the current host, so a handoff cannot change the assignment.

The preflight boundary applies to effects owned by the extension. c2j may have
already prepared packages, the worktree, hydrated artifacts, or resolved secrets
before starting it. Verify those setup and cleanup semantics against the final
c2j version selected for integration; do not promise that an insufficient worker performs no
runtime preparation at all.

## Protocol handling

Add a small typed `internal/execution` package for requirement validation,
allocation decoding, and comparison. Prefer a reusable c2j contract library if
one is available without importing the worker/runtime into the production
binary. Otherwise use shared conformance fixtures against c2j's authoritative
matcher, including base, capability, and quantity cases. Test prepared-dependency
array decoding separately from requirement matching.

Read allocation facts only from `C2J_EXECUTION_ALLOCATION`. c2j injects this
protected runtime context after inherited, manifest, and invocation environment
settings. Do not add it to the op input schema, encoded job payload, or durable
task-input hash, and do not allow `env` or profile inputs to replace it. Facts
describe this attempt; never synthesize them from desired requirements or local
host inspection. Fresh facts must not change invocation identity on resume.

Follow the spec and guide exactly:

- Require a nonempty requirement object with only `base`, `resources`, and/or
  `capabilities`. Reject unknown fields, legacy OS/arch/image fields, and
  `packages`; use c2j's validation for their values and units.
- Only `*` is a wildcard in base selectors; comparison covers the whole name
  and is case-sensitive. `?` and brackets have no special meaning.
- Exactly `base: "*"` accepts an unknown base. Other selectors need a reported
  base; there are no implicit `linux`/`ubuntu` or architecture aliases. A request
  containing only `base: "*"` is already satisfied and must never be emitted.
- CPU accepts cores/millicores; memory and ephemeral storage use c2j SI/IEC
  quantities. Compare normalized values without floating-point rounding.
- Requested capabilities must be satisfied according to the
  protocol. Unknown required facts do not satisfy a requirement.
- Omit unneeded fields. When a handoff is needed, emit the complete requirement
  object, including already satisfied fields, so the replacement executor must
  satisfy the entire job contract.

Extend `extensioncmd` with a typed `ExecutionRequired` control outcome, recognized
before ordinary output/error serialization. Emit exactly one JSON object with
only `execution_required`, then return 75. Reject an attempt to combine this
outcome with output, objects, or artifact references. Encoding failures remain
ordinary failures. Logs explaining the mismatch go to stderr and contain no
secrets. A shell command exiting 75 remains an Actions job failure; it must not
be converted into extension negotiation.

The exit code and valid control envelope are both required. Unknown fields,
trailing JSON, truncated output, or mixed result/control data on exit 75 must
fail rather than yield. Exit 0 keeps ordinary success semantics, including an
output property named `execution_required`; other nonzero exits remain failures.
Timeout, cancellation, and lease loss never become handoff requests. In the
command helper, check cancellation before emitting control and leave the
runtime authoritative about the actual process outcome.

The control result bypasses successful-output schema validation, artifact
publication, and Git-result acceptance. Keep `gha-job`'s normal output schema
intact; do not add a success-schema alternative for handoff or manufacture
diagnostics/output objects to satisfy it. No successful Git/output checkpoint
is accepted for a yielding attempt.

Never request a handoff if the requirements are already satisfied. A local
executable failure after accepting allocation is an execution/infrastructure
failure, not grounds to return the same requirements again. c2j performs the
authoritative comparison and handles conflicting job-wide overrides. An
already-satisfied or malformed request is a protocol/operation error: normal
recipe catch and failure-retry policies apply. Do not turn it into another
successful handoff.

The already-satisfied check concerns all needs in the extension's request,
not a locally reconstructed effective enclosing demand. A request already
satisfied by this allocation is invalid even if some enclosing demand would
require additional resources. Leave enclosing-demand enforcement to c2j.

c2j also combines the request with enclosing requirements. It preserves enclosing
resource minima and capability needs. Requested base selectors must refine or
encompass enclosing selectors; overlapping patterns alone do not establish
compatibility. Let c2j perform that authoritative composition rather than trying
to weaken or replace recipe constraints in GHA. For example, a request for 4 CPU
must not lower an enclosing minimum of 8, and an exact Ubuntu base can refine
`ubuntu*` but conflicts with an enclosing `macos*` selector.

## Generated recipes, replay, and scheduling

In negotiated mode, replace `expand.go`'s generated step-level
`execution_needs` with the resolved requirements in the `gha-job` input payload.
Retain the existing one-step shard recipe. Static mode keeps its current recipe
shape. Avoid attaching the same requirements through both mechanisms in this
first implementation; that would hide the negotiation path during testing.

The parent workflow, dependency waiters, expanders, and collectors only require
their own bootstrap environment. A macOS execution requirement belongs to its
individual job task, not to the coordinator or sibling Linux tasks.

On a valid exit-75 handoff, c2j publishes scheduling demand and the shard stays pending. Existing terminal-result waits must remain
blocked; collectors must not convert the handoff into `failure`, and `catch` or
`always()` handling must not fire merely because placement changed. Independent
siblings can continue. Without a compatible executor, the shard remains pending
under the runtime's normal cancellation/deadline policies. A valid handoff consumes
no failure retry.

c2j owns demand publication via
`RescheduleExecutionRequest.ClientPayloadUpdate` followed by `TaskContext.Yield`,
including payload-revision checks and preservation of unrelated client state.
GHA does not call these APIs or create a pending-needs record. Existing wrappers
must propagate confirmed suspension. Only JobDB-confirmed rescheduling counts
as a handoff; if publication fails or its acknowledgement is lost, stop the
attempt and leave recovery to the normal lease machinery. Recipe recovery must
not continue under an uncertain lease.

On resume, preserve child IDs, source revision/hash, matrix assignment,
dependency results, artifact references, and execution requirements. Runtime-local
paths and secret materialization may be recreated. Earlier completed tasks
replay from history; JobDB handles the continuation and the pending extension
starts again. Recheck the fresh allocation every time. If it is still insufficient,
return the same complete requirements with exit 75 again, before side effects.
This is a new valid handoff, not an internal retry/provisioning loop or a consumed
failure retry. Do not resubmit a child or introduce extra recovery state.

The request affects this invocation's scheduling demand. It does not change the
requirements of later nodes or child jobs. The frozen GHA input payload makes
requirement derivation deterministic on each invocation; it is not a new runtime
recovery record or a persistent override for subsequent work.

Read-only replay must neither launch `gha-job` nor publish scheduling demand.
Explicit restarts use existing c2j restart rules. `ops.SuspendExecution` remains
the separate successful-result directive for changing subsequent work; it is
not the mechanism for retrying this unfinished single-job invocation.

This prevents negotiation from duplicating job effects. It does not establish
exactly-once external side effects after execution has begun; process crashes
retain c2j's existing recovery semantics.

## Implementation stages

1. **Verify and pin runtime support.** Confirm the chosen c2j version implements
   the guide's schema-2 allocation, prepared-dependency reporting, requirement
   composition, and JobDB continuation behavior. Obtain matching/validation
   fixtures and update the GHA integration module's c2j dependency as needed.
   Use the companion spec's process-validation and recovery cases to qualify
   that pinned version, including uncertain reschedule acknowledgement and
   read-only replay. No new runtime negotiation protocol needs designing here.
2. **Implement contract types and the command outcome.** Add requirement and
   allocation types, matching, and control-envelope support with process tests.
3. **Refactor job preflight.** Move side effects behind the allocation check;
   add direct-call requirements and shared assignment evaluation. Preserve
   existing skipped/failure results and diagnostics.
4. **Implement the runner catalog and wire generated payloads.** Add the
   versioned `internal/runners` inventory, compatibility and larger-size tables,
   convenience aliases, `runner_type` profile shorthand, labels-only object
   normalization, resource defaults, profile precedence, and mapping diagnostics. Make profiles optional in
   negotiated mode. Update `pkg/gha/types.go`,
   `pkg/gha/op.go`, workflow inputs, profile validation/matching, and `expand.go`.
   Keep deterministic child IDs and opaque transport of GHA expressions.
5. **Update public contracts.** Change both `op.yaml` and `op.json` for `gha`,
   `gha-workflow`, and `gha-job`. Document mode selection, prepared-dependency facts,
   migration, compatible c2j versions, and executor inventory examples.
6. **Qualify resume and real placement.** Run the integration cases below, then
   provisioned native Linux/macOS tests. Keep negotiation opt-in until the
   dispatcher, published packages, and supported runtime agree on the contract.

Negotiation cannot rescue an executor that cannot start the extension at all.
The initial and replacement workers must support the published GHA binary and
bootstrap dependencies. Existing packages cover Linux AMD64/ARM64 and Apple
Silicon macOS; Windows runner labels need a separate execution implementation.
The existing upstream act host-executor race remains a qualification issue.

## Validation and acceptance criteria

Use `make -C gha test-fast` for unit/process and embedded runtime coverage after
the implementation. Extend the real c2j harness rather than mocking successful
negotiation. Specifically cover:

| Case | Required result |
| --- | --- |
| All facts satisfy the request | Normal output, one job execution, no exit 75 |
| Wrong base, insufficient resources, or missing capability | Exact control envelope and exit 75; no extension-owned execution effects |
| Missing/malformed allocation or wrong schema | Clear compatibility error; no execution |
| Manifest/invocation environment attempts to override allocation | Runtime-supplied facts win; fresh allocation never changes durable task-input identity |
| Empty/unknown requirement fields, trailing JSON, truncated output, or mixed control/result envelopes on exit 75 | Diagnostic failure; no handoff or result publication |
| Exit 0 with an ordinary output property named `execution_required` | Normal success validation, not negotiation |
| Timeout, cancellation, lease loss, or another nonzero exit with control-looking stdout | Normal runtime failure/cancellation semantics; no handoff |
| Valid exit-75 response without ordinary outputs | Success schema is bypassed; no artifact or Git-result acceptance |
| Wildcards, quantities, and capabilities | Same decisions as c2j's authoritative matcher |
| Invalid profile, ambiguous labels, invalid matrix, or unsupported workflow | Ordinary error before execution |
| Every supported catalog label and alias | Expected exact base, resource minima, and mapping provenance |
| Every larger Ubuntu size on each supported architecture | Correct CPU/memory table entry; ARM64 64-core requests `208G`, AMD64 requests `256G` |
| Mac XLarge versus Large | Supported ARM64 XLarge requests 5 CPU / `14G`; Intel Large is rejected |
| Larger-runner custom profile and convenience name | Same complete requirements; explicit profile wins over the convenience name |
| Unknown size or invalid architecture/size combination | Configuration error; no invented hardware type |
| Labels-only `runs-on` object | Same resolution as its scalar label; group constraints are never discarded |
| Windows, Intel macOS, too-new OS, or unknown label | Clear unsupported-runner error; no exit 75 |
| Explicit profile override | Deterministic full replacement; OS/architecture conflicts rejected |
| Multi-label selectors or runner-group objects | No ignored constraints; require supported explicit semantics |
| Catalog upgrade between yield and resume | Existing child retains its original resolved base and requirements |
| Resource and capability overrides on the same base | Independent requirements; no proliferation of machine names |
| Skipped job on an insufficient worker | Skipped result without provisioning |
| Handoff followed by compatible resume | Same child and assignment; one execution; normal artifacts and outputs |
| SQLite reopen and deletion of initial worker directories during handoff | Existing continuation resumes the same task with source and dependency artifacts; requirements are derived again from frozen inputs |
| Mixed requirements across matrix assignments | Each shard requests its own environment; ready siblings proceed independently |
| Prepared-dependency array is present, absent, empty, or refreshed on resume | Decode current invocation facts; never treat it as all installed host software |
| Multiple versions of a dependency appear | Preserve entries; bare-command selection remains governed by scope precedence |
| Host tool is absent from the dependency array | Absence alone does not fail validation or trigger a handoff |
| Missing required tool with otherwise sufficient allocation | Ordinary validation/execution error when needed; no package-driven handoff |
| Workflow installs a tool before using it | Initial absence alone does not fail preflight |
| `packages` supplied in negotiated requirements | Reject input; never emit a package request or convert it to a capability |
| Handoff while a collector or dependent waits | Neither observes a terminal failure until the shard actually terminates |
| Enclosing resource minima and capabilities | c2j preserves stronger minima and all enclosing capability needs |
| Refined/encompassing versus incompatible base selectors | c2j accepts compatible composition and rejects conflicts |
| Conflicting overrides, malformed requests, or already-satisfied requests (including only `base: "*"`) | Ordinary operation error; normal catch/retry behavior |
| Resume on another insufficient executor, then a sufficient one | Repeated valid handoffs; no failure retries consumed; exactly one execution |
| Later node or child after a handoff | Original requirements apply; negotiated demand does not leak into subsequent work |
| Reschedule publication failure or lost acknowledgement | Attempt stops; no recipe recovery under uncertain lease; normal lease recovery can retry |
| Demand publication with unrelated client state | Revision checks and unrelated state remain intact |
| Read-only replay | No extension process launch and no handoff publication |
| Job shell exits 75 or fails after preflight | Ordinary job failure; no negotiation |
| Static mode and existing local/GitHub calls | Existing behavior and output contracts remain compatible |

For process-level handoff tests, assert stdout contains no ordinary output or
object markers and use sentinels to detect outbox writes, action fetches,
artifact-service startup, and shell execution. Simulated allocation facts test
protocol behavior; native provisioned tests must establish that requested OS,
architecture, resources, and capabilities are actually supplied, and reported
prepared-dependency facts describe successful invocation setup with the
documented scope precedence.
