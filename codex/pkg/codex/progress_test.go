package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProgressIsReadableWhileCLIIsRunning(t *testing.T) {
	installFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
test -s "$PROGRESS_FILE"
grep -q 'exec.start' "$PROGRESS_FILE"
printf '%s\n' '{"type":"session.created","session_id":"test-session"}'
printf '%s\n' '{"type":"item.completed","item":{"item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"done\"}"}}'
`)
	opts := testExecuteOptions(t)
	opts.Prompt = "sensitive prompt must not be in progress logs"
	progressFile := filepath.Join(opts.ArtifactOutbox, "codex-progress.jsonl")
	opts.ExtraEnv = map[string]string{"PROGRESS_FILE": progressFile, "SECRET_TOKEN": "secret-value"}
	ctx, closeLog, err := withProgress(context.Background(), opts.ArtifactOutbox)
	require.NoError(t, err)
	defer closeLog()
	result, stdout, stderr, dir, err := Execute(ctx, opts)
	defer cleanupArtifacts(stdout, stderr, dir)
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, result.Status)
	data, err := os.ReadFile(progressFile)
	require.NoError(t, err)
	require.NotContains(t, string(data), opts.Prompt)
	require.NotContains(t, string(data), "secret-value")
	var phases []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		phases = append(phases, entry["phase"].(string))
	}
	require.Equal(t, []string{"invocation.start", "exec.start", "exec.running", "exec.finished"}, phases)
}
