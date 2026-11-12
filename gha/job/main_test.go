package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2ops/gha/internal/jobexec"
	"github.com/stretchr/testify/require"
)

func TestWorkflowDigestDriftFailsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "workflow.yml"), []byte("on: workflow_dispatch\njobs: {}\n"), 0600))
	_, err := run(t.Context(), input{Request: jobexec.Request{Workflow: "workflow.yml", JobID: "probe"}, WorkflowSHA256: strings.Repeat("0", 64), WorktreePath: root})
	require.ErrorContains(t, err, "workflow changed from the planned snapshot")
}
