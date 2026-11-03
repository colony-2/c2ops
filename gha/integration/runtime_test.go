package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestHarnessExecutesRealRecipeAndReopensCompletedResult(t *testing.T) {
	// macOS supplies a long TMPDIR; worker credential sockets must still fit
	// within Unix socket path limits. Exceed Linux's limit too for coverage there.
	longTemp := filepath.Join(t.TempDir(), strings.Repeat("long-temp-", 12))
	require.NoError(t, os.Mkdir(longTemp, 0700))
	t.Setenv("TMPDIR", longTemp)
	h := newHarness(t, nil)
	root := h.submit(`id: root
sequence:
  - id: shell
    op: command_execution
    inputs:
      run: printf 'executed by a real c2j worker'
outputs:
  text: '${{ sequence.shell.outputs.stdout }}'
`, nil)
	result := h.drain(root)
	require.Equal(t, "executed by a real c2j worker", result["text"])
	calls := len(h.calls)
	require.Positive(t, calls)
	h.reopen()
	out := h.step(root)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.False(t, out.LeaseAcquired, "completed job must not acquire a new lease")
	require.Equal(t, calls, len(h.calls), "completed result must not execute any task")
	require.Equal(t, result, h.result(root))
}

func TestHarnessResumesParentAfterChildrenAndDatabaseReopen(t *testing.T) {
	h := newHarness(t, map[string]string{"child": `id: child
input_schema:
  label: {type: string}
inputs:
  label: '${{ inputs.label }}'
sequence:
  - id: shell
    op: command_execution
    inputs:
      run: 'printf "%s" "$LABEL"'
      env:
        LABEL: '${{ inputs.label }}'
outputs:
  label: '${{ sequence.shell.outputs.stdout }}'
`})
	root := h.submit(`id: root
sequence:
  - id: children
    child_group:
      children:
        - key: first
          recipe: child
          inputs: {label: alpha}
        - key: second
          recipe: child
          inputs: {label: beta}
outputs:
  children: '${{ sequence.children.outputs }}'
`, nil)
	out := h.step(root)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	jobs := h.jobs()
	require.Len(t, jobs, 3, "parent must submit real child jobs before suspending")
	var children []jobdb.JobKey
	for _, job := range jobs {
		if job.JobKey != root {
			require.Equal(t, root.JobId, job.ParentJobID)
			children = append(children, job.JobKey)
		}
	}
	require.Equal(t, jobworkflow.JobRunCompleted, h.step(children[0]).Status)
	calls := len(h.calls)
	h.reopen()
	result := h.drain(root)
	require.NotEmpty(t, result["children"])
	require.Len(t, h.jobs(), 3, "replay must not submit duplicate children")
	require.ElementsMatch(t, []any{"alpha", "beta"}, []any{h.result(children[0])["label"], h.result(children[1])["label"]})
	require.Greater(t, len(h.calls), calls)
}

func TestHarnessReportsFailedShell(t *testing.T) {
	h := newHarness(t, nil)
	root := h.submit(`id: root
sequence:
  - id: shell
    op: command_execution
    inputs: {run: 'exit 7'}
`, nil)
	out := h.step(root)
	require.Equal(t, jobworkflow.JobRunFailed, out.Status)
	require.Error(t, out.JobError)
	require.Contains(t, out.JobError.Error(), "exit status 7")
}

func TestHarnessYieldsBeforeAnIncompatibleAllocationRuns(t *testing.T) {
	h := newHarness(t, nil)
	other := "linux/arm64"
	if runtime.GOARCH == "arm64" {
		other = "linux/amd64"
	}
	marker := filepath.Join(h.root, "must-not-run")
	root := h.submit(fmt.Sprintf(`id: admission
execution_needs:
  platform: %s
sequence:
  - id: shell
    op: command_execution
    inputs:
      run: 'touch "$MARKER"'
      env:
        MARKER: %q
`, other, marker), nil)
	out := h.step(root)
	handoff, err := compiler.ExecutionHandoffFromOutcome(out)
	require.NoError(t, err)
	require.NotNil(t, handoff, "runtime must ask the provisioner for a different allocation")
	require.Equal(t, other, *handoff.Demand.Effective.Platform)
	_, err = os.Stat(marker)
	require.True(t, os.IsNotExist(err))
	require.Empty(t, h.calls)
}
