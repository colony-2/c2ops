package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func installWorkflowFixture(t *testing.T, h *harness) {
	raw, err := os.ReadFile("../workflow/op.json")
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(raw, &manifest))
	manifest["command"] = []string{workflowBinary}
	delete(manifest, "dependencies")
	raw, err = json.Marshal(manifest)
	require.NoError(t, err)
	h.seed(map[string]string{".c2/test-ops/gha-workflow/op.yaml": string(raw)})
}

func workflowRecipe(t *testing.T, extra string) string {
	return fmt.Sprintf(`id: full-workflow
sequence:
  - id: launch
    op: ./.c2/test-ops/gha-workflow
    inputs:
      workflow: .github/workflows/fixture.yml
      helper_op: ./.c2/test-ops/gha-workflow
      executor_op: ./.c2/test-ops/gha-job
      runner_profiles:
        - labels: [local]
          execution_needs: {platform: %s/%s}
%s
  - id: done
    op: recipe.await_result
    inputs:
      target: {job: '${{ sequence.launch.outputs.job_id }}'}
      wait: {on: job}
outputs:
  run: '${{ sequence.done.outputs.outputs }}'
`, runtime.GOOS, runtime.GOARCH, extra)
}
func TestSubmittedWorkflowRunsWholeDAGAndSurvivesRestart(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, fanInWorkflow)
	installWorkflowFixture(t, h)
	root := h.submit(workflowRecipe(t, ""), nil)
	require.Equal(t, workflow.JobRunSuspended, h.step(root).Status)
	require.Len(t, h.jobs(), 5, "caller, three logical jobs, result collector")
	h.reopen()
	result := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "success", result["status"])
	jobs := result["jobs"].(map[string]any)
	require.Equal(t, "alpha-beta", jobs["combine"].(map[string]any)["outputs"].(map[string]any)["combined"])
	count := len(h.jobs())
	calls := len(h.calls)
	h.reopen()
	require.Equal(t, result, h.result(root)["run"])
	require.Equal(t, workflow.JobRunCompleted, h.step(root).Status)
	require.Len(t, h.jobs(), count)
	require.Len(t, h.calls, calls)
}

func TestSubmittedWorkflowDynamicMatrixAndNamedSecret(t *testing.T) {
	h := newHarness(t, nil)
	h.secrets["build_key"] = "synthetic-workflow-key"
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  plan:
    runs-on: local
    outputs:
      matrix: ${{ steps.out.outputs.matrix }}
    steps:
      - id: out
        run: echo 'matrix={"version":[20,24]}' >> "$GITHUB_OUTPUT"
  build:
    needs: plan
    runs-on: local
    strategy:
      fail-fast: false
      matrix: ${{ fromJSON(needs.plan.outputs.matrix) }}
    steps:
      - env:
          TOKEN: ${{ secrets.CLIENT_KEY }}
          VERSION: ${{ matrix.version }}
        run: |
          test "$TOKEN" = synthetic-workflow-key
          test "$VERSION" = 20 -o "$VERSION" = 24
  combine:
    needs: build
    runs-on: local
    steps:
      - run: echo joined
`)
	installWorkflowFixture(t, h)
	root := h.submit("perms: {jobdb: {jobs: {submit: true}, secrets: {resolve: [build_key]}}}\n"+workflowRecipe(t, "      secret_bindings: {CLIENT_KEY: build_key}"), nil)
	result := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "success", result["status"])
	require.Len(t, h.jobs(), 12, "caller + 3 coordinators + root collector + 4 matrix instances + 3 per-job collectors")
	reads := h.secretReads
	require.Positive(t, reads)
	h.reopen()
	require.Equal(t, workflow.JobRunCompleted, h.step(root).Status)
	require.Equal(t, reads, h.secretReads)
}
func TestSubmittedWorkflowFailureStillRunsReporter(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  build:
    runs-on: local
    steps: [{run: 'exit 7'}]
  skipped:
    needs: build
    runs-on: local
    steps: [{run: 'exit 9'}]
  report:
    needs: build
    if: always()
    runs-on: local
    steps: [{run: 'echo reported'}]
`)
	installWorkflowFixture(t, h)
	root := h.submit(workflowRecipe(t, ""), nil)
	run := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "failure", run["status"])
	jobs := run["jobs"].(map[string]any)
	for name, want := range map[string]string{"build": "failure", "skipped": "skipped", "report": "success"} {
		require.Equal(t, want, jobs[name].(map[string]any)["conclusion"])
	}
}
func TestSubmittedWorkflowFiltersPushBranches(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, `on:
  push:
    branches: [main]
jobs:
  build:
    runs-on: local
    steps: [{run: 'exit 9'}]
`)
	installWorkflowFixture(t, h)
	root := h.submit(workflowRecipe(t, "      event_name: push\n      event: {ref: refs/heads/feature}"), nil)
	run := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "skipped", run["status"])
	require.Empty(t, run["jobs"])
	require.Len(t, h.jobs(), 2)
}
