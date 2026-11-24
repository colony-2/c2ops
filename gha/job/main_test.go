package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/colony-2/c2ops/gha/internal/execution"
	"github.com/colony-2/c2ops/gha/internal/jobexec"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNegotiationPrecedesSideEffects(t *testing.T) {
	root := t.TempDir()
	outbox := filepath.Join(root, "outbox")
	require.NoError(t, os.Mkdir(outbox, 0700))
	t.Setenv("C2J_OBJECT_OUTBOX", outbox)
	require.NoError(t, os.WriteFile(filepath.Join(root, "ci.yml"), []byte(`on: workflow_dispatch
jobs:
  test:
    runs-on: {labels: ubuntu-24.04-16core}
    steps:
      - run: exit 75
`), 0600))
	in := input{Request: jobexec.Request{Workflow: "ci.yml", JobID: "test", ExecutionMode: "negotiate", Env: map[string]string{"C2J_EXECUTION_ALLOCATION": `{"schema_version":2,"base":"ubuntu-26.04-amd64","resources":{"cpu":"96","memory":"384G"}}`}}, WorktreePath: root}
	t.Setenv("C2J_EXECUTION_ALLOCATION", `{"schema_version":2,"base":"ubuntu-26.04-amd64","resources":{"cpu":"4","memory":"16Gi"}}`)
	for i := 0; i < 2; i++ {
		out, err := run(context.Background(), in)
		require.NoError(t, err)
		require.NotNil(t, out.ExecutionRequired)
		require.Equal(t, "16", *out.ExecutionRequired.Resources.CPU)
		require.Empty(t, out.Objects)
		require.Empty(t, out.Output.JobID)
		entries, err := os.ReadDir(outbox)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
	t.Run("matching allocation executes", func(t *testing.T) {
		t.Setenv("C2J_EXECUTION_ALLOCATION", `{"schema_version":2,"base":"ubuntu-26.04-amd64","resources":{"cpu":"16","memory":"64G"}}`)
		out, err := run(context.Background(), in)
		require.NoError(t, err)
		require.Nil(t, out.ExecutionRequired)
		require.Equal(t, "failure", out.Output.Conclusion, "shell exit 75 is not negotiation")
	})
}
func TestSkippedJobDoesNotNeedAllocation(t *testing.T) {
	root := t.TempDir()
	outbox := t.TempDir()
	t.Setenv("C2J_OBJECT_OUTBOX", outbox)
	t.Setenv("C2J_EXECUTION_ALLOCATION", "invalid")
	require.NoError(t, os.WriteFile(filepath.Join(root, "ci.yml"), []byte("on: workflow_dispatch\njobs:\n  skip:\n    if: false\n    runs-on: windows-latest\n    steps:\n      - run: exit 1\n"), 0600))
	out, err := run(context.Background(), input{Request: jobexec.Request{Workflow: "ci.yml", JobID: "skip", ExecutionMode: "negotiate"}, WorktreePath: root})
	require.NoError(t, err)
	require.Nil(t, out.ExecutionRequired)
	require.Equal(t, "skipped", out.Output.Conclusion)
	require.Contains(t, out.Objects, "diagnostics")
}
func TestPayloadPlacementConflict(t *testing.T) {
	payload, _ := json.Marshal(jobexec.Request{Workflow: "ci.yml", JobID: "test", ExecutionMode: "negotiate"})
	_, err := run(context.Background(), input{Payload: base64.StdEncoding.EncodeToString(payload), Request: jobexec.Request{Required: &execution.Requirements{Base: execution.String("*")}}})
	require.ErrorContains(t, err, "cannot be combined")
}

func TestWorkflowDigestDriftFailsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "workflow.yml"), []byte("on: workflow_dispatch\njobs: {}\n"), 0600))
	_, err := run(t.Context(), input{Request: jobexec.Request{Workflow: "workflow.yml", JobID: "probe"}, WorkflowSHA256: strings.Repeat("0", 64), WorktreePath: root})
	require.ErrorContains(t, err, "workflow changed from the planned snapshot")
}
