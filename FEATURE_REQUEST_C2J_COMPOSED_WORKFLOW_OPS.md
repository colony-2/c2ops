# c2j workflow composition: existing support and optional improvements

Corrected 2026-10-30 after auditing c2j `v0.0.110`, including its
[`C2J_CHILD_JOBS_GUIDE.md`](https://github.com/colony-2/c2j/blob/v0.0.110/C2J_CHILD_JOBS_GUIDE.md),
CLI submit implementation, invocation broker, and native recipe operations.

**The original request overstated the missing functionality. Child submission
and ordinary dependency scheduling already exist. They are not blockers for the
[GHA orchestration implementation](PLAN_GHA_C2J_ORCHESTRATION.md).**

## What already works

- An extension can invoke `c2j submit` repeatedly. With the injected current-job
  and broker environment, each submission becomes a formal child of the running
  job, attributed to the launching op invocation. This does not require direct
  JobDB credentials or a nested worker engine in the extension.
- `--advanced-recipe-file` transports a local recipe snapshot through the broker;
  `--artifact` transports attached bytes. The receiving worker does not need the
  submitter's local paths. `--inputs-file` accepts recipe inputs.
- The runtime records launched children under `sequence.<step>.jobs` (or the
  equivalent state context). Submission alone does not wait or execute children.
- `recipe.await_result` and `recipe.await_result_soft` provide durable waits and
  result collection. The soft form lets failure/cancellation become data for
  downstream `if: always()` and failure-reporting jobs.
- Native `recipes.run` and `child_group` starts use deterministic child identities.
  Native recipe inputs support secret-reference forwarding. Known binding names
  can use scalar secret inputs; alternatively, generated executor recipes can
  reference explicitly selected c2j secret names at the live executor boundary.
- Each dependent wrapper can wait on its own prerequisites. Given independent A
  and B, with C depending only on A, C need not wait for B. A central wait-any
  operation is not necessary for this DAG representation.

The integration regression `TestActDependentRunsBeforeUnrelatedChildFinishes`
exercises that last arrangement with real act jobs, SQLite, worker suspension,
and a database reopen. It leaves B unexecuted while C completes.

## Implementation obligations, not missing submission APIs

Preserve the injected broker/current-job environment, configure the CLI's JobDB
target and tenant, and make the declared `c2j` executable available. The broker
is invocation-scoped; finish submitting before the launching process returns.
Use independent workers and native recipe waits rather than `submit --run` in a
long-lived controller process.

CLI submissions generate fresh IDs: repeating a successful submit creates
another child. The invocation hash is attribution, not an idempotency key.
Prefer native deterministic starts for replay; if a submit response is lost,
reconcile recorded children instead of blindly repeating it. Distinguish replay
of a completed task from retry after a partially completed submission.

A normal extension receives materialized secrets. It must not write those
values into generated recipe YAML, child inputs, or public output. Build the
graph from public metadata and secret binding names; retain references in native
recipe composition and resolve them only at each executing `gha-job` task.
Recipe `input_schema` currently uses `object` for durable objects, so arbitrary
named JSON maps are not interchangeable with scalar secret recipe inputs.

The upstream guide cautions that subprocess parent/child workflows are not
generally supported by the standalone embedded CLI mode. Our stepped harness
tests the exact runtime path it exercises; it does not establish support for
every `c2j submit --embed` arrangement.

## Optional upstream improvements

A packaged recipe entry point could make a synchronous, one-selector workflow
call more convenient. Reference-preserving structured recipe schemas could make
arbitrary secret maps easier to pass. Neither is required to submit a generated
wrapper recipe through the existing broker and await it explicitly.

A durable wait-any operation could simplify a centralized controller. Scoped
cancellation may be useful for matrix fail-fast and parent cancellation. Audit
those contracts separately when implementing those policies; absence from the
recipe op registry alone is not evidence that c2j cannot orchestrate a DAG.

VM isolation and nested virtualization remain separate provisioner work. Current
execution requirements expose packages, image, platform, and resources; proposed
VM/capability fields still need upstream implementation and qualification.
GitHub OIDC remains outside the agreed scope.
