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

func TestCodexExecOpRequiresExplicitContextOnStdin(t *testing.T) {
	stdout, stderr, err := runEntrypoint(t, "./cmd/codex-exec-op", map[string]any{
		"prompt": "say hello",
	})

	require.Error(t, err)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "codex.exec: worktree_path is required")
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
