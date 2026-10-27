package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Runs against the actual pinned CLI and a local HTTP provider in the normal
// AMD64/ARM64 CI suite. No OpenAI account or external inference is involved.
func TestNativeRunOutputAndResumeSchema(t *testing.T) {
	requireCodexCLI(t)
	t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
	api := newMockCodexAPI(t, []mockCodexTurn{
		{Summary: "Remembered native-output-memory"},
		{Summary: `{"answer":"invalid"}`},
		{Summary: `{"answer":42}`},
	})
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	first := liveExecuteOptions(t, api)
	t.Setenv("CODEX_HOME", first.HostCodexHome)
	runInput := ExecOpInput{
		Prompt:       "Remember native-output-memory. Later return the answer 42.",
		Instructions: "initial-session-guidance: keep quoted \"text\".\nPreserve this guidance.",
		Env:          first.ExtraEnv, WorkdirPath: first.WorkDirRoot, WorktreePath: first.WorktreeRoot,
		ArtifactInboxPath: first.ArtifactInbox, ArtifactOutboxPath: first.ArtifactOutbox,
	}
	output, err := Run(ctx, runInput)
	require.NoError(t, err)
	require.JSONEq(t, `"Remembered native-output-memory"`, string(output.Response))
	require.Equal(t, "completed", output.Status)
	require.NotEmpty(t, output.Events)
	require.NotEmpty(t, output.Items)
	require.NotEmpty(t, output.Usage)
	require.NotEmpty(t, output.TerminalEvent)
	require.Equal(t, 0, *output.ExitCode)
	require.Nil(t, output.Workflow)
	requireArtifactOnlyTrace(t, first.ArtifactOutbox, output.NativeOutput, output)
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	var public map[string]any
	require.NoError(t, json.Unmarshal(encoded, &public))
	for _, field := range []string{"assistantSummary", "incompleteCategory", "pendingDependencies", "outcome"} {
		require.NotContains(t, public, field)
	}
	store := checkpointStore(t)
	draft := output.Objects[output.Session.Name]
	ref, err := store.Publish(ctx, draft.Type, draft.Metadata, draft.Files)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(first.WorkDirRoot))
	second := liveExecuteOptions(t, api)
	t.Setenv("CODEX_HOME", second.HostCodexHome)
	runInput = ExecOpInput{
		Prompt: "Return the remembered answer.", Session: hydrateSession(t, store, ref),
		Instructions: "ignored-continuation-guidance",
		Output:       ResponseOutputSpec{Schema: json.RawMessage(answerSchema)},
		Env:          second.ExtraEnv, WorkdirPath: second.WorkDirRoot, WorktreePath: second.WorktreeRoot,
		ArtifactInboxPath: second.ArtifactInbox, ArtifactOutboxPath: second.ArtifactOutbox,
	}
	repaired, err := Run(ctx, runInput)
	require.NoError(t, err)
	require.Equal(t, output.SessionID, repaired.SessionID)
	require.NotNil(t, repaired.Session)
	require.True(t, *repaired.OutputSchemaValid)
	require.JSONEq(t, `{"answer":42}`, string(repaired.Response))
	require.Equal(t, 1, repaired.OutputRepairAttempts)
	require.Equal(t, "native", repaired.SchemaMode)
	requireArtifactOnlyTrace(t, second.ArtifactOutbox, repaired.NativeOutput, repaired)
	terminalCount := 0
	for _, event := range repaired.Events {
		if strings.Contains(string(event), `"type":"turn.completed"`) {
			terminalCount++
		}
	}
	require.Equal(t, 2, terminalCount)
	api.mu.Lock()
	defer api.mu.Unlock()
	require.Len(t, api.requests, 3)
	for _, request := range api.requests {
		requireInitialInstructions(t, request)
	}
	requireNoNativeSchema(t, api.requests[0])
	for _, request := range api.requests[1:] {
		text := request["text"].(map[string]any)
		format := text["format"].(map[string]any)
		require.Equal(t, "json_schema", format["type"])
		schema, err := json.Marshal(format["schema"])
		require.NoError(t, err)
		require.JSONEq(t, answerSchema, string(schema), "must send exactly the user's schema")
		history, err := json.Marshal(request["input"])
		require.NoError(t, err)
		require.Contains(t, string(history), "native-output-memory")
	}
}

func requireArtifactOnlyTrace(t *testing.T, outbox string, native NativeOutput, output any) {
	t.Helper()
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	var public map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &public))
	for _, field := range []string{"events", "items", "response_text", "terminal_event"} {
		require.NotContains(t, public, field)
	}
	require.JSONEq(t, string(native.Usage), string(public["usage"]))
	restored, err := parseJSONL(filepath.Join(outbox, "stdout.jsonl"))
	require.NoError(t, err)
	require.Equal(t, native.Events, restored.Events)
	require.Equal(t, native.Items, restored.Items)
	require.Equal(t, native.ResponseText, restored.ResponseText)
	require.Equal(t, native.TerminalEvent, restored.TerminalEvent)
}

func TestNativeSchemaRejectionFallsBackToPromptAndLocalValidation(t *testing.T) {
	requireCodexCLI(t)
	api := newMockCodexAPI(t, []mockCodexTurn{
		{SchemaRejection: true}, {Summary: `{"answer":42}`},
	})
	defer api.Close()
	opts := liveExecuteOptions(t, api)
	opts.StructuredSchema = []byte(answerSchema)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, out, errout, dir, err := Execute(ctx, opts)
	defer cleanupArtifacts(out, errout, dir)
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, result.Status, result.ErrorMessage)
	require.True(t, *result.OutputSchemaValid)
	require.JSONEq(t, `{"answer":42}`, string(result.Response))
	require.Equal(t, "prompt", result.SchemaMode)
	require.Equal(t, 1, result.OutputRepairAttempts)
	logs, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Contains(t, string(logs), "invalid_json_schema")
	api.mu.Lock()
	defer api.mu.Unlock()
	require.Len(t, api.requests, 2)
	requireNoNativeSchema(t, api.requests[1])
	prompt, err := json.Marshal(api.requests[1]["input"])
	require.NoError(t, err)
	require.Contains(t, string(prompt), "Native structured output is unavailable")
	require.Contains(t, string(prompt), "answer")
}

func requireNoNativeSchema(t *testing.T, request map[string]any) {
	t.Helper()
	if text, ok := request["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok {
			require.NotEqual(t, "json_schema", format["type"])
		}
	}
}

// Verify the provider sees the original developer message, exactly once, even
// after restoring a checkpoint on a different worker and repairing a response.
func requireInitialInstructions(t *testing.T, request map[string]any) {
	t.Helper()
	history, err := json.Marshal(request["input"])
	require.NoError(t, err)
	require.NotContains(t, string(history), "ignored-continuation-guidance")
	require.Equal(t, 1, strings.Count(string(history), "initial-session-guidance"))
	found := false
	for _, raw := range request["input"].([]any) {
		message, ok := raw.(map[string]any)
		if !ok || message["role"] != "developer" {
			continue
		}
		content, err := json.Marshal(message["content"])
		require.NoError(t, err)
		if strings.Contains(string(content), "initial-session-guidance") {
			found = true
		}
	}
	require.True(t, found, "instructions must be a developer message")
}
