package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2ops/codex/pkg/codex"
	"github.com/stretchr/testify/require"
)

func TestRunRetainsNativeDiagnosticsOnFailure(t *testing.T) {
	for _, schemaFailure := range []bool{false, true} {
		name := "execution failure"
		if schemaFailure {
			name = "schema failure"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("C2J_OBJECT_OUTBOX", filepath.Join(root, "objects"))
			t.Setenv("CODEX_HOME", filepath.Join(root, "missing-host-home"))
			script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo codex-cli 0.161.0; exit 0; fi
printf '%s\n' '{"type":"thread.started","thread_id":"diagnostic-session"}'
printf '%s\n' '{"type":"turn.started"}'
`
			if schemaFailure {
				script += `printf '%s\n' '{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":"not JSON"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1}}'
`
			} else {
				script += `printf '%s\n' '{"type":"error","message":"Missing API key"}'
printf '%s\n' '{"type":"turn.failed","error":{"message":"Missing API key"}}'
exit 1
`
			}
			binary := filepath.Join(root, "fake-codex")
			require.NoError(t, os.WriteFile(binary, []byte(script), 0755))
			t.Setenv("C2OPS_CODEX_BIN", binary)
			input := codex.ExecOpInput{Prompt: "test", WorkdirPath: root, WorktreePath: filepath.Join(root, "worktree")}
			if schemaFailure {
				zero := 0
				input.Output = codex.ResponseOutputSpec{Schema: json.RawMessage(`{"type":"object"}`), MaxRepairAttempts: &zero}
			}
			result, err := run(context.Background(), input)
			require.Error(t, err)
			require.Equal(t, "error", result.Output.Status)
			require.Equal(t, "diagnostic-session", result.Output.SessionID)
			require.NotEmpty(t, result.Output.Events)
			require.NotEmpty(t, result.Output.TerminalEvent)
			require.NotEmpty(t, result.Output.ErrorMessage)
			require.Nil(t, result.Output.Session)
			require.Empty(t, result.Objects)
			encoded, err := json.Marshal(result.Output)
			require.NoError(t, err)
			var public map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &public))
			for _, field := range []string{"events", "items", "response_text", "terminal_event"} {
				require.NotContains(t, public, field)
			}
			transcript, err := os.ReadFile(filepath.Join(root, "outbox", "stdout.jsonl"))
			require.NoError(t, err)
			for _, event := range result.Output.Events {
				require.Contains(t, string(transcript), string(event))
			}
			if schemaFailure {
				require.Nil(t, result.Output.Response)
				require.Equal(t, "not JSON", result.Output.ResponseText)
				require.False(t, *result.Output.OutputSchemaValid)
			}
		})
	}
}
