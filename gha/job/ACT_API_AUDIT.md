# act API usage audit

Audited against `github.com/nektos/act v0.2.89` on 2026-10-30.

The adapter uses act's exported planner and job executor. It does not replace
the step executor or concurrently call one `RunContext` from multiple goroutines.
The low-level API is not a documented remote-scheduler plugin; compatibility is
checked against the pinned implementation and regression tests.

## Comparison with act's own runner

[`runner.NewPlanExecutor`](https://github.com/nektos/act/blob/v0.2.89/pkg/runner/runner.go#L122-L218)
expands matrices, builds a `RunContext`, obtains `rc.Executor()`, and calls it
with a job logger and `common.WithJobErrorContainer`. The adapter uses those same
executor and context facilities for one explicit matrix assignment. Its context
initialization matches `newRunContext`: config, selected `model.Run`, event JSON,
step results, matrix, and expression evaluator. Reusable-workflow caller state
is not needed because reusable workflows are rejected.

The intentional adapter responsibilities are:

- Read the workflow with act's model/planner and preserve original `needs` edges.
  Populate recorded dependency results and outputs on the same workflow model.
  Do not run `PlanJob` as a whole: that would also execute predecessor jobs.
- Select one already assigned matrix combination, validate it against act's
  expansion, and map evaluated runner labels to host execution. Placement and
  OS isolation belong to the worker provisioner.
- Start one `RunContext` per invocation with private state paths. Blank ambient
  environment entries because act host execution otherwise inherits missing
  keys from its process. Add only the caller/workflow environment and tools.
- Prepend a source-preparation checkout using act's local checkout shortcut.
  Host mode creates an empty execution workspace; the shortcut copies the
  supplied source snapshot. Authored remote actions remain rejected by preflight.
- Capture the model's final result and resolved job outputs. A shell failure can
  leave `rc.Executor()` without a Go error while setting `Job.Result` to failure;
  act's outer `handleFailure` normally inspects that field. The adapter returns
  that conclusion as collectable workflow data.
- Use an invocation-local logger. Redact before formatting and omit raw
  structured log fields, since act's normal masking formatter only masks the
  message. Publish logs as diagnostics and suppress secret-bearing job outputs.

The regression suite covers dependency status functions, skipped jobs,
`always()`, shell failures, explicit matrices, local composite and JavaScript
actions, event context, secret masking, and workspace isolation. The embedded
suite executes compiled helper processes through real c2j workers and durable
child waits, including independent dependency progress after a restart.

## Host-runner race validation

Host mode is an explicit documented act option:
[`-P ubuntu-latest=-self-hosted`](https://nektosact.com/usage/runners.html).
Building the unmodified act CLI with `-race` and running a single `printf` step
reproduces the same `ptyWriter.AutoStop` race. The workflow succeeds but the race
detector exits 66. The [standalone report](../../BUG_REPORT_ACT_HOST_EXECUTOR_RACE.md)
requires only stock act and a tiny workflow.

This establishes that the reported PTY race occurs in act's normal CLI path; it
is not evidence of an invalid adapter invocation. It does not establish that
all act host behavior is correct, that the Go API is stable across versions, or
that cancellation cleanup is qualified. Keep those as separate release checks.
