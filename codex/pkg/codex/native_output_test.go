package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const answerSchema = `{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":false}`

func TestPublicOutputDoesNotDuplicateLargeTrace(t *testing.T) {
	toolOutput := strings.Repeat("large tool output\n", 10000)
	item, err := json.Marshal(map[string]any{"id": "tool", "type": "command_execution", "aggregated_output": toolOutput})
	require.NoError(t, err)
	native := parseStream(t, `{"type":"item.completed","item":`+string(item)+"}\n"+nativeTurn("done")).NativeOutput
	for name, output := range map[string]any{
		"codex":     ExecOpOutput{NativeOutput: native, Status: "completed"},
		"skill-run": SkillRunOutput{NativeOutput: native, Status: "completed"},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(output)
			require.NoError(t, err)
			require.Less(t, len(encoded), 2048, "inline size must not grow with trace size")
			var public map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &public))
			for _, field := range []string{"events", "items", "response_text", "terminal_event"} {
				require.NotContains(t, public, field)
			}
			require.JSONEq(t, `"done"`, string(public["response"]))
			require.JSONEq(t, string(native.Usage), string(public["usage"]))
		})
	}
	require.Contains(t, string(native.Items[0]), "large tool output", "Go callers retain diagnostics")
}

func nativeTurn(answer string) string {
	item, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"id": "answer", "type": "agent_message", "text": answer}})
	return "{\"type\":\"thread.started\",\"thread_id\":\"test-session\"}\n{\"type\":\"turn.started\"}\n" + string(item) + "\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}\n"
}

func parseStream(t *testing.T, stream string) Result {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(stream), 0600))
	parsed, err := parseJSONL(path)
	require.NoError(t, err)
	return parsed.Result
}

func TestNativeOutputPreservesAllEventFields(t *testing.T) {
	stream := `{"type":"thread.started","thread_id":"thread-1","future_thread_field":true}
{"type":"turn.started"}
{"type":"item.started","item":{"id":"cmd","type":"command_execution","status":"in_progress","command":"test"}}
{"type":"item.updated","item":{"id":"cmd","type":"command_execution","status":"in_progress","aggregated_output":"partial"}}
{"type":"item.completed","item":{"id":"cmd","type":"command_execution","status":"failed","exit_code":2,"aggregated_output":"diagnostic","unknown":{"nested":9007199254740993}}}
{"type":"item.completed","item":{"id":"reason","type":"reasoning","text":"trying another approach"}}
{"type":"item.completed","item":{"id":"files","type":"file_change","status":"completed","changes":[{"path":"a","kind":"update"}]}}
{"type":"item.completed","item":{"id":"mcp","type":"mcp_tool_call","server":"docs","tool":"search","result":{"content":[]}}}
{"type":"item.completed","item":{"id":"plan","type":"todo_list","items":[{"text":"fix","completed":true}]}}
{"type":"future.event","future_payload":{"x":1}}
{"type":"error","message":"Reconnecting..."}
{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":"Which file should I edit?"}}
{"type":"turn.completed","usage":{"input_tokens":8,"cached_input_tokens":4,"cache_write_input_tokens":1,"output_tokens":3,"reasoning_output_tokens":2,"future_tokens":9},"future_terminal_field":"kept"}
`
	r := parseStream(t, stream)
	require.Equal(t, StatusCompleted, r.Status)
	require.Empty(t, r.ErrorMessage)
	require.Equal(t, "thread-1", r.SessionID)
	require.Equal(t, "Which file should I edit?", r.ResponseText)
	require.JSONEq(t, `"Which file should I edit?"`, string(r.Response))
	require.Len(t, r.Events, 13)
	require.Len(t, r.Items, 6)
	require.Contains(t, string(r.Items[0]), `9007199254740993`)
	require.Contains(t, string(r.TerminalEvent), `"future_terminal_field":"kept"`)
	require.Contains(t, string(r.Usage), `"future_tokens":9`)
	// A question remains answer text; we do not infer a workflow state from prose.
	require.Equal(t, string(r.Events[9]), `{"type":"future.event","future_payload":{"x":1}}`)
}

func TestNativeOutputRequiresTerminalEventAndPreservesErrors(t *testing.T) {
	for _, tc := range []struct{ name, stream, errorText string }{
		{"early error", `{"type":"error","message":"Missing API key"}`, "Missing API key"},
		{"failed turn", `{"type":"turn.failed","error":{"message":"request failed","code":"extra"}}`, "request failed"},
		{"truncated", `{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`, "without a terminal"},
		{"new turn truncated", nativeTurn("first") + "{\"type\":\"turn.started\"}\n", "without a terminal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := parseStream(t, tc.stream)
			require.Equal(t, StatusError, r.Status)
			require.Contains(t, r.ErrorMessage, tc.errorText)
			require.NotEmpty(t, r.Events)
		})
	}
}

func TestNativeOutputDoesNotInterpretUserFieldsAsExecutionStatus(t *testing.T) {
	r := parseStream(t, nativeTurn(`{"status":"error","assistantSummary":"user data"}`))
	require.Equal(t, StatusCompleted, r.Status)
	require.JSONEq(t, `"{\"status\":\"error\",\"assistantSummary\":\"user data\"}"`, string(r.Response))
}

func TestNativeOutputKeepsReusedItemIDsAcrossTurns(t *testing.T) {
	r := parseStream(t, nativeTurn("first")+nativeTurn("second"))
	require.Len(t, r.Items, 2)
	require.Equal(t, "second", r.ResponseText)
}

// Each process writes its exact argv and emits one configured native stream.
func installNativeSequence(t *testing.T, streams []string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	var script strings.Builder
	fmt.Fprintf(&script, "#!/usr/bin/env bash\nset -euo pipefail\nroot=%s\nn=0\nif [[ -f $root/count ]]; then n=$(cat \"$root/count\"); fi\nn=$((n+1))\necho \"$n\" > \"$root/count\"\nprintf '%%s\\n' \"$@\" > \"$root/args-$n\"\n", shellQuote(dir))
	script.WriteString("case $n in\n")
	for i, stream := range streams {
		fmt.Fprintf(&script, "%d) printf '%%s' %s ;;\n", i+1, shellQuote(stream))
	}
	fmt.Fprintf(&script, "*) echo 'unexpected repair' >&2; exit 9 ;;\nesac\nexit %d\n", exitCode)
	installFakeCodex(t, script.String())
	return dir
}

func TestOutputSchemaRepairsResumeAndRetainEveryAttempt(t *testing.T) {
	dir := installNativeSequence(t, []string{nativeTurn(`{"answer":"wrong"}`), nativeTurn(`{"answer":9007199254740993}`)}, 0)
	opts := testExecuteOptions(t)
	opts.StructuredSchema = []byte(answerSchema)
	opts.Instructions = "initial instructions"
	r, out, errout, artifacts, err := Execute(context.Background(), opts)
	defer cleanupArtifacts(out, errout, artifacts)
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, r.Status, r.ErrorMessage)
	require.True(t, *r.OutputSchemaValid)
	require.Equal(t, `{"answer":9007199254740993}`, string(r.Response))
	require.Equal(t, 1, r.OutputRepairAttempts)
	require.Len(t, r.Events, 8)
	require.Len(t, r.Items, 2)
	log, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Contains(t, string(log), `wrong`)
	require.Contains(t, string(log), `9007199254740993`)
	args, err := os.ReadFile(filepath.Join(dir, "args-2"))
	require.NoError(t, err)
	require.Contains(t, string(args), "--output-schema\n")
	require.Contains(t, string(args), "resume\ntest-session\n")
	require.NotContains(t, string(args), "developer_instructions=")
	initialArgs, err := os.ReadFile(filepath.Join(dir, "args-1"))
	require.NoError(t, err)
	require.Contains(t, string(initialArgs), `developer_instructions="initial instructions"`)
	require.Contains(t, string(args), "Validation feedback:")
}

func TestOutputSchemaRepairLimitAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   *int
		repairs int
	}{
		{"default", nil, 3}, {"zero", intPtr(0), 0}, {"custom", intPtr(1), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streams := make([]string, tc.repairs+1)
			for i := range streams {
				streams[i] = nativeTurn(`{"answer":"invalid"}`)
			}
			dir := installNativeSequence(t, streams, 0)
			opts := testExecuteOptions(t)
			opts.StructuredSchema = []byte(answerSchema)
			opts.MaxRepairAttempts = tc.limit
			r, out, errout, artifacts, err := Execute(context.Background(), opts)
			defer cleanupArtifacts(out, errout, artifacts)
			require.NoError(t, err)
			require.Equal(t, StatusError, r.Status)
			require.False(t, *r.OutputSchemaValid)
			require.Nil(t, r.Response, "invalid data must not be returned as validated response")
			require.NotEmpty(t, r.ResponseText, "raw diagnostic must remain available")
			require.Equal(t, tc.repairs, r.OutputRepairAttempts)
			assertFileContent(t, filepath.Join(dir, "count"), fmt.Sprintf("%d\n", tc.repairs+1))
		})
	}
}

func TestOutputSchemaRejectsInvalidInputsBeforeLaunch(t *testing.T) {
	for _, schema := range []string{`{"type":"not-a-type"}`, `{`} {
		opts := testExecuteOptions(t)
		opts.StructuredSchema = []byte(schema)
		_, _, _, _, err := Execute(context.Background(), opts)
		require.Error(t, err)
	}
	opts := testExecuteOptions(t)
	opts.MaxRepairAttempts = intPtr(-1)
	_, _, _, _, err := Execute(context.Background(), opts)
	require.ErrorContains(t, err, "must not be negative")
}

func TestOutputSchemaDoesNotRepairExecutionFailures(t *testing.T) {
	for _, tc := range []struct {
		name, stream string
		code         int
	}{
		{"authentication", `{"type":"error","message":"401 Missing API key"}`, 1},
		{"nonzero exit after answer", nativeTurn(`{"answer":1}`), 2},
		{"failed turn", `{"type":"turn.failed","error":{"message":"refused"}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := installNativeSequence(t, []string{tc.stream}, tc.code)
			opts := testExecuteOptions(t)
			opts.StructuredSchema = []byte(answerSchema)
			r, out, errout, artifacts, err := Execute(context.Background(), opts)
			defer cleanupArtifacts(out, errout, artifacts)
			require.NoError(t, err)
			require.Equal(t, StatusError, r.Status)
			require.Nil(t, r.Response)
			require.Equal(t, 0, r.OutputRepairAttempts)
			require.Equal(t, tc.code, *r.ExitCode)
			assertFileContent(t, filepath.Join(dir, "count"), "1\n")
		})
	}
}

func TestNoSchemaUsesPlainAnswerWithoutNativeSchemaFlag(t *testing.T) {
	dir := installNativeSequence(t, []string{nativeTurn("Hello")}, 0)
	r, out, errout, artifacts, err := Execute(context.Background(), testExecuteOptions(t))
	defer cleanupArtifacts(out, errout, artifacts)
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, r.Status)
	require.JSONEq(t, `"Hello"`, string(r.Response))
	require.Nil(t, r.OutputSchemaValid)
	args, err := os.ReadFile(filepath.Join(dir, "args-1"))
	require.NoError(t, err)
	require.NotContains(t, string(args), "--output-schema")
}

func TestOutputSchemaSupportsScalarAndRejectsTrailingJSON(t *testing.T) {
	for _, tc := range []struct {
		schema, answer string
		valid          bool
	}{
		{`{"type":"integer"}`, `9007199254740993`, true},
		{`{"const":9007199254740993}`, `9007199254740993`, true},
		{`{"const":9007199254740993}`, `9007199254740992`, false},
		{`true`, `null`, true}, {`false`, `null`, false},
		{`{"type":"object"}`, `{} {}`, false},
		{answerSchema, "```json\n{\"answer\":1}\n```", false},
	} {
		t.Run(tc.schema+tc.answer, func(t *testing.T) {
			installNativeSequence(t, []string{nativeTurn(tc.answer)}, 0)
			opts := testExecuteOptions(t)
			opts.StructuredSchema = []byte(tc.schema)
			opts.MaxRepairAttempts = intPtr(0)
			r, out, errout, artifacts, err := Execute(context.Background(), opts)
			defer cleanupArtifacts(out, errout, artifacts)
			require.NoError(t, err)
			require.Equal(t, tc.valid, *r.OutputSchemaValid)
		})
	}
}

func intPtr(n int) *int { return &n }

func TestSchemaSetupErrorKeepsReadableArtifacts(t *testing.T) {
	opts := testExecuteOptions(t)
	opts.Prompt = ""
	opts.StructuredSchema = []byte(answerSchema)
	_, stdout, stderr, dir, err := Execute(context.Background(), opts)
	defer cleanupArtifacts(stdout, stderr, dir)
	require.ErrorIs(t, err, errEmptyPrompt)
	assertFileContent(t, stdout, "")
	assertFileContent(t, stderr, "")
}
