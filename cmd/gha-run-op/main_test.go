package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGhaRunOpRequiresExplicitGitContextOnStdin(t *testing.T) {
	stdout, stderr, err := runEntrypoint(t, "./cmd/gha-run-op", map[string]any{
		"workflow": "ci.yml",
	})

	require.Error(t, err)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "gha.run: git_context.worktree_path is required")
}

func runEntrypoint(t *testing.T, selector string, input any) (string, string, error) {
	t.Helper()

	payload, err := json.Marshal(input)
	require.NoError(t, err)

	cmd := exec.Command("go", "run", selector)
	cmd.Dir = repoRoot(t)
	cmd.Stdin = bytes.NewReader(payload)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
