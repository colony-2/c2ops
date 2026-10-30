package integration

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// A dependency wait belongs in the dependent child. A parent does not need a
// wait-any API, or a barrier between scheduler batches, to execute a static DAG.
func TestActDependentRunsBeforeUnrelatedChildFinishes(t *testing.T) {
	child := func(id string) string {
		return localRecipe(t, `id: act-`+id+`
sequence:
  - id: execute
    op: nix:github:colony-2/c2ops/published#gha-job
    inputs:
      workflow: .github/workflows/fixture.yml
      job_id: `+id+`
outputs:
  result: '${{ sequence.execute.outputs }}'
`)
	}
	h := newHarness(t, map[string]string{
		"act-a": child("a"), "act-b": child("b"),
		"act-c": localRecipe(t, `id: act-c
input_schema:
  predecessor: {type: string, required: true}
inputs:
  predecessor: '${{ inputs.predecessor }}'
sequence:
  - id: dependency
    op: recipe.await_result_soft
    inputs:
      target: {job: '${{ inputs.predecessor }}'}
      wait: {on: job}
      return_when: terminal
  - id: execute
    op: nix:github:colony-2/c2ops/published#gha-job
    inputs:
      workflow: .github/workflows/fixture.yml
      job_id: c
      needs:
        a: '${{ sequence.dependency.outputs.outputs.result }}'
outputs:
  result: '${{ sequence.execute.outputs }}'
`),
	})
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  a:
    runs-on: local
    outputs:
      value: ${{ steps.out.outputs.value }}
    steps:
      - id: out
        run: echo 'value=alpha' >> "$GITHUB_OUTPUT"
  b:
    runs-on: local
    steps:
      - run: echo unrelated
  c:
    runs-on: local
    needs: a
    outputs:
      value: ${{ steps.out.outputs.value }}
    steps:
      - id: out
        env:
          VALUE: ${{ needs.a.outputs.value }}
        run: echo "value=$VALUE" >> "$GITHUB_OUTPUT"
`)
	root := h.submit(`id: independent-dependencies
sequence:
  - id: roots
    op: recipes.run
    inputs:
      recipes:
        - {name: act-a}
        - {name: act-b}
  - id: downstream
    op: recipes.run
    inputs:
      recipes:
        - name: act-c
          inputs:
            predecessor: '${{ sequence.roots.outputs.job_ids[0] }}'
  - id: result
    op: recipe.await_result
    inputs:
      target: {job: '${{ sequence.downstream.outputs.job_ids[0] }}'}
      wait: {on: job}
  - id: unrelated
    op: recipe.await_result
    inputs:
      target: {job: '${{ sequence.roots.outputs.job_ids[1] }}'}
      wait: {on: job}
outputs:
  result: '${{ sequence.result.outputs.outputs.result }}'
`, nil)
	require.Equal(t, workflow.JobRunSuspended, h.step(root).Status)
	children := map[string]jobdb.JobKey{}
	for _, job := range h.jobs() {
		if job.JobKey != root {
			var metadata struct {
				Recipe string `json:"recipe"`
			}
			require.NoError(t, json.Unmarshal(job.Metadata, &metadata))
			children[metadata.Recipe] = job.JobKey
		}
	}
	require.Len(t, children, 3)
	for _, name := range []string{"act-a", "act-b", "act-c"} {
		require.Contains(t, children, name)
	}
	require.Equal(t, workflow.JobRunSuspended, h.step(children["act-c"]).Status)
	require.Equal(t, workflow.JobRunCompleted, h.step(children["act-a"]).Status)
	h.reopen()
	require.Equal(t, workflow.JobRunCompleted, h.step(children["act-c"]).Status)
	result := h.result(children["act-c"])["result"].(map[string]any)
	require.Equal(t, "success", result["conclusion"])
	require.Equal(t, "alpha", result["outputs"].(map[string]any)["value"])
	for _, call := range h.calls {
		require.NotContains(t, call, children["act-b"].JobId+":", "unrelated B has not executed")
	}
	require.Equal(t, workflow.JobRunSuspended, h.step(root).Status)
	final := h.drain(root)
	require.Equal(t, result, final["result"])
	require.Len(t, h.jobs(), 4, "restart must not duplicate any child")
}
