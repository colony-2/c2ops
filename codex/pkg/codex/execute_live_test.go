package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLiveExecuteWithConfiguredSkillCreatesMarker(t *testing.T) {
	requireCodexCLI(t)
	api := newMockCodexAPI(t, []mockCodexTurn{
		{
			Command: `test -f "$CODEX_HOME/skills/live-smoke/SKILL.md" && mkdir -p .c2/live-codex-skill-execution && printf '%s' '{"ok":true}' > .c2/live-codex-skill-execution/result.json`,
		},
		{
			Summary: "skill executed",
		},
	})
	defer api.Close()

	opts := liveExecuteOptions(t, api)

	skillsRoot := filepath.Join(opts.WorkDirRoot, "configured-skills")
	skillDir := filepath.Join(skillsRoot, "live-smoke")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create live skill: %v", err)
	}
	skillBody := `---
name: live-smoke
description: Use when asked to run live-smoke.
---

When this skill is used, create .c2/live-codex-skill-execution/result.json
with exactly {"ok":true} and then return a completed structured response.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatalf("write live skill: %v", err)
	}
	opts.ConfiguredSkillDirs = []string{skillsRoot}
	opts.Prompt = "Use live-smoke."

	result, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("Execute returned setup error: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %q: %s", result.Status, result.ErrorMessage)
	}

	marker := filepath.Join(opts.WorktreeRoot, ".c2", "live-codex-skill-execution", "result.json")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker %s: %v", marker, err)
	}
	if strings.TrimSpace(string(data)) != `{"ok":true}` {
		t.Fatalf("unexpected marker content %q", data)
	}
}

func TestLiveExecuteResumesSessionKnowledge(t *testing.T) {
	requireCodexCLI(t)

	name := "Joe-" + strings.ReplaceAll(t.Name(), "/", "-")
	api := newMockCodexAPI(t, []mockCodexTurn{
		{Summary: "stored"},
		{Command: "pwd > resumed-path.txt"},
		{Summary: name},
	})
	defer api.Close()

	first := liveExecuteOptions(t, api)
	first.Prompt = "Remember this exact fact for the next turn in this session: my name is " + name + ". Do not modify files. Return a completed structured response whose assistantSummary says you stored that exact name."

	firstResult, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), first)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("first Execute returned setup error: %v", err)
	}
	if firstResult.Status != StatusCompleted {
		t.Fatalf("expected first execution to complete, got %q: %s", firstResult.Status, firstResult.ErrorMessage)
	}
	if strings.TrimSpace(firstResult.SessionID) == "" {
		t.Fatalf("expected first execution to return a session id")
	}
	exported := filepath.Join(t.TempDir(), "home")
	if err := exportSessionHome(first.CodexHome, exported); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(first.CodexHome); err != nil {
		t.Fatal(err)
	}

	second := liveExecuteOptions(t, api)
	second.SessionID = firstResult.SessionID
	if err := copyDirContents(exported, second.CodexHome); err != nil {
		t.Fatal(err)
	}
	if err := relocateSessionIndex(second.CodexHome, second.SessionID, second.WorktreeRoot, false); err != nil {
		t.Fatal(err)
	}
	second.Prompt = "Using the existing conversation in this resumed session, answer this question: what is my name? Do not modify files. Return a completed structured response with assistantSummary containing only the exact name."

	secondResult, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), second)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("second Execute returned setup error: %v", err)
	}
	if secondResult.Status != StatusCompleted {
		t.Fatalf("expected second execution to complete, got %q: %s", secondResult.Status, secondResult.ErrorMessage)
	}
	assertFileContent(t, filepath.Join(second.WorktreeRoot, "resumed-path.txt"), second.WorktreeRoot+"\n")
	if _, err := os.Stat(first.CodexHome); !os.IsNotExist(err) {
		t.Fatal("resume recreated the old home")
	}
	if secondResult.SessionID != firstResult.SessionID {
		t.Fatalf("expected resumed session id %q, got %q", firstResult.SessionID, secondResult.SessionID)
	}
	api.mu.Lock()
	requestJSON, marshalErr := json.Marshal(api.requests[len(api.requests)-1]["input"])
	api.mu.Unlock()
	if marshalErr != nil || !strings.Contains(string(requestJSON), "Remember this exact fact") {
		t.Fatalf("resume did not send the earlier conversation: %s, %v", requestJSON, marshalErr)
	}
	if !strings.Contains(strings.ToLower(secondResult.AssistantSummary), strings.ToLower(name)) {
		t.Fatalf("expected resumed assistant summary to contain %q, got %q", name, secondResult.AssistantSummary)
	}
}

func requireCodexCLI(t *testing.T) {
	t.Helper()
	if _, err := readCodexVersion(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("declared Codex CLI not available: %v", err)
	}
}

func liveExecuteOptions(t *testing.T, api *mockCodexAPI) Options {
	t.Helper()

	opts := testExecuteOptions(t)
	opts.IdleTimeout = 5 * time.Minute
	opts.Model = os.Getenv("C2OPS_CODEX_LIVE_MODEL")
	configureMockCodexProvider(t, &opts, api.URL())
	return opts
}

func configureMockCodexProvider(t *testing.T, opts *Options, baseURL string) {
	t.Helper()

	hostHome := filepath.Join(opts.WorkDirRoot, "host-codex-home")
	if err := os.MkdirAll(hostHome, 0o755); err != nil {
		t.Fatalf("create host codex home: %v", err)
	}
	config := fmt.Sprintf(`model_provider = "mock"
model = "gpt-5.3-codex"

[model_providers.mock]
name = "Mock"
base_url = %q
wire_api = "responses"
env_key = "OPENAI_API_KEY"
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
stream_idle_timeout_ms = 1000
`, strings.TrimRight(baseURL, "/")+"/v1")
	if err := os.WriteFile(filepath.Join(hostHome, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write mock codex config: %v", err)
	}
	opts.HostCodexHome = hostHome
	if opts.ExtraEnv == nil {
		opts.ExtraEnv = map[string]string{}
	}
	opts.ExtraEnv["OPENAI_API_KEY"] = "dummy"
}

type mockCodexTurn struct {
	Command string
	Summary string
}

type mockCodexAPI struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	turns    []mockCodexTurn
	requests []map[string]any
}

func newMockCodexAPI(t *testing.T, turns []mockCodexTurn) *mockCodexAPI {
	t.Helper()

	api := &mockCodexAPI{t: t, turns: turns}
	api.server = httptest.NewServer(http.HandlerFunc(api.handleResponses))
	return api
}

func (api *mockCodexAPI) URL() string {
	return api.server.URL
}

func (api *mockCodexAPI) Close() {
	api.server.Close()
}

func (api *mockCodexAPI) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
		http.NotFound(w, r)
		return
	}
	defer r.Body.Close()

	var request map[string]any
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	idx := len(api.requests)
	api.requests = append(api.requests, request)
	if idx >= len(api.turns) {
		api.mu.Unlock()
		http.Error(w, fmt.Sprintf("unexpected Codex API request %d", idx+1), http.StatusInternalServerError)
		return
	}
	turn := api.turns[idx]
	api.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if turn.Command != "" {
		writeFunctionCallTurn(api.t, w, idx+1, turn.Command)
		return
	}
	writeMessageTurn(api.t, w, idx+1, structuredPayload(turn.Summary))
}

func writeFunctionCallTurn(t *testing.T, w http.ResponseWriter, turn int, command string) {
	t.Helper()

	responseID := fmt.Sprintf("resp_%d", turn)
	itemID := fmt.Sprintf("fc_%d", turn)
	callID := fmt.Sprintf("call_%d", turn)
	args, err := json.Marshal(map[string]any{
		"cmd":               command,
		"yield_time_ms":     1000,
		"max_output_tokens": 2000,
	})
	if err != nil {
		t.Fatalf("marshal function call args: %v", err)
	}
	argsText := string(args)
	item := map[string]any{
		"id":        itemID,
		"type":      "function_call",
		"status":    "completed",
		"name":      "exec_command",
		"call_id":   callID,
		"arguments": argsText,
	}

	writeSSE(t, w, "response.created", map[string]any{
		"type":     "response.created",
		"response": responseEnvelope(responseID, "in_progress", []any{}),
	})
	writeSSE(t, w, "response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": 0,
		"item": map[string]any{
			"id":        itemID,
			"type":      "function_call",
			"status":    "in_progress",
			"name":      "exec_command",
			"call_id":   callID,
			"arguments": "",
		},
	})
	writeSSE(t, w, "response.function_call_arguments.delta", map[string]any{
		"type":         "response.function_call_arguments.delta",
		"item_id":      itemID,
		"output_index": 0,
		"delta":        argsText,
	})
	writeSSE(t, w, "response.function_call_arguments.done", map[string]any{
		"type":         "response.function_call_arguments.done",
		"item_id":      itemID,
		"output_index": 0,
		"arguments":    argsText,
	})
	writeSSE(t, w, "response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": 0,
		"item":         item,
	})
	writeSSE(t, w, "response.completed", map[string]any{
		"type":     "response.completed",
		"response": responseEnvelope(responseID, "completed", []any{item}),
	})
}

func writeMessageTurn(t *testing.T, w http.ResponseWriter, turn int, text string) {
	t.Helper()

	responseID := fmt.Sprintf("resp_%d", turn)
	itemID := fmt.Sprintf("msg_%d", turn)
	item := map[string]any{
		"id":     itemID,
		"type":   "message",
		"status": "completed",
		"role":   "assistant",
		"content": []any{
			map[string]any{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			},
		},
	}

	writeSSE(t, w, "response.created", map[string]any{
		"type":     "response.created",
		"response": responseEnvelope(responseID, "in_progress", []any{}),
	})
	writeSSE(t, w, "response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": 0,
		"item": map[string]any{
			"id":      itemID,
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	})
	writeSSE(t, w, "response.content_part.added", map[string]any{
		"type":          "response.content_part.added",
		"item_id":       itemID,
		"output_index":  0,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        "",
			"annotations": []any{},
		},
	})
	writeSSE(t, w, "response.output_text.delta", map[string]any{
		"type":          "response.output_text.delta",
		"item_id":       itemID,
		"output_index":  0,
		"content_index": 0,
		"delta":         text,
	})
	writeSSE(t, w, "response.output_text.done", map[string]any{
		"type":          "response.output_text.done",
		"item_id":       itemID,
		"output_index":  0,
		"content_index": 0,
		"text":          text,
	})
	writeSSE(t, w, "response.content_part.done", map[string]any{
		"type":          "response.content_part.done",
		"item_id":       itemID,
		"output_index":  0,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        text,
			"annotations": []any{},
		},
	})
	writeSSE(t, w, "response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": 0,
		"item":         item,
	})
	writeSSE(t, w, "response.completed", map[string]any{
		"type":     "response.completed",
		"response": responseEnvelope(responseID, "completed", []any{item}),
	})
}

func writeSSE(t *testing.T, w http.ResponseWriter, event string, payload map[string]any) {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal SSE payload: %v", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		t.Fatalf("write SSE payload: %v", err)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func responseEnvelope(id string, status string, output []any) map[string]any {
	return map[string]any{
		"id":                   id,
		"object":               "response",
		"created_at":           1741290958,
		"status":               status,
		"error":                nil,
		"incomplete_details":   nil,
		"instructions":         nil,
		"max_output_tokens":    nil,
		"model":                "gpt-5.3-codex",
		"output":               output,
		"parallel_tool_calls":  true,
		"previous_response_id": nil,
		"reasoning": map[string]any{
			"effort":  nil,
			"summary": nil,
		},
		"store":       false,
		"temperature": 1.0,
		"text": map[string]any{
			"format": map[string]any{"type": "text"},
		},
		"tool_choice": "auto",
		"tools":       []any{},
		"top_p":       1.0,
		"truncation":  "disabled",
		"usage": map[string]any{
			"input_tokens":  1,
			"output_tokens": 1,
			"output_tokens_details": map[string]any{
				"reasoning_tokens": 0,
			},
			"total_tokens": 2,
		},
		"metadata": map[string]any{},
	}
}

func structuredPayload(summary string) string {
	payload, err := json.Marshal(map[string]any{
		"status":              "completed",
		"assistantSummary":    summary,
		"incompleteReason":    "",
		"incompleteCategory":  "",
		"pendingDependencies": []any{},
		"errorMessage":        "",
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}
