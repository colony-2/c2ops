package aider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type opEnvelope struct {
	Output map[string]any `json:"output"`
}

type openAIRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
	Raw    string
}

type openAIServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []openAIRequest
	handler  func(openAIRequest, int) (int, any)
}

func TestAiderOpIntegration(t *testing.T) {
	server := newOpenAIServer(t, func(req openAIRequest, _ int) (int, any) {
		latest := latestUserContent(req.Body)
		content := "prompt:" + latest
		if strings.Contains(latest, "Continue and summarize") {
			content = "resumed: Continue and summarize"
		}
		return http.StatusOK, chatCompletionResponse(modelName(req.Body), content)
	})
	defer server.Close()

	workdir := t.TempDir()
	worktree := filepath.Join(workdir, "worktree")
	outbox := filepath.Join(workdir, "outbox")
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.MkdirAll(outbox, 0o755))

	first, stdout, stderr, err := runPackagedOp(t, map[string]any{
		"prompt":               "Fix the test suite",
		"model":                "gpt-4o-mini",
		"workdir_path":         workdir,
		"worktree_path":        worktree,
		"artifact_outbox_path": outbox,
		"env": map[string]string{
			"AIDER_OPENAI_API_KEY":  "dummy",
			"AIDER_OPENAI_API_BASE": server.BaseURL(),
		},
	}, nil)
	require.NoError(t, err, "stdout=%s stderr=%s", stdout, stderr)
	require.Empty(t, filterIgnorablePythonOpStderr(stderr))
	require.Equal(t, "completed", first.Output["status"])

	sessionID, ok := first.Output["sessionId"].(string)
	require.True(t, ok)
	require.NotEmpty(t, sessionID)
	require.Contains(t, first.Output["assistantSummary"].(string), "prompt:Fix the test suite")

	stdoutJSONL, err := os.ReadFile(filepath.Join(outbox, "stdout.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(stdoutJSONL), "prompt:Fix the test suite")

	stderrText, err := os.ReadFile(filepath.Join(outbox, "stderr.txt"))
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(string(stderrText)))

	second, stdout, stderr, err := runPackagedOp(t, map[string]any{
		"prompt":               "Continue and summarize",
		"sessionId":            sessionID,
		"model":                "gpt-4o-mini",
		"workdir_path":         workdir,
		"worktree_path":        worktree,
		"artifact_outbox_path": outbox,
		"env": map[string]string{
			"AIDER_OPENAI_API_KEY":  "dummy",
			"AIDER_OPENAI_API_BASE": server.BaseURL(),
		},
	}, nil)
	require.NoError(t, err, "stdout=%s stderr=%s", stdout, stderr)
	require.Empty(t, filterIgnorablePythonOpStderr(stderr))
	require.Equal(t, "completed", second.Output["status"])
	require.Equal(t, sessionID, second.Output["sessionId"])
	require.Contains(t, second.Output["assistantSummary"].(string), "resumed: Continue and summarize")

	stdoutJSONL, err = os.ReadFile(filepath.Join(outbox, "stdout.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(stdoutJSONL), "resumed: Continue and summarize")

	requests := server.Requests()
	require.GreaterOrEqual(t, len(requests), 2)
	require.Contains(t, latestUserContent(requests[0].Body), "Fix the test suite")
	require.Contains(t, latestUserContent(requests[len(requests)-1].Body), "Continue and summarize")
	require.Contains(t, requests[len(requests)-1].Raw, "Fix the test suite")
}

func newOpenAIServer(t *testing.T, handler func(openAIRequest, int) (int, any)) *openAIServer {
	t.Helper()

	s := &openAIServer{handler: handler}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			writeJSONResponse(w, http.StatusOK, map[string]any{
				"object": "list",
				"data": []map[string]any{
					{"id": "gpt-4.1", "object": "model"},
					{"id": "gpt-4.1-mini", "object": "model"},
					{"id": "gpt-4o-mini", "object": "model"},
				},
			})
			return
		}

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
			return
		}

		var body map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
				return
			}
		}

		req := openAIRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   body,
			Raw:    string(raw),
		}

		s.mu.Lock()
		index := len(s.requests)
		s.requests = append(s.requests, req)
		s.mu.Unlock()

		status, response := s.handler(req, index)
		writeJSONResponse(w, status, response)
	}))
	return s
}

func (s *openAIServer) BaseURL() string {
	return s.server.URL + "/v1"
}

func (s *openAIServer) Requests() []openAIRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]openAIRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *openAIServer) Close() {
	s.server.Close()
}

func runPackagedOp(t *testing.T, input any, extraEnv map[string]string) (opEnvelope, string, string, error) {
	t.Helper()

	payload, err := json.Marshal(input)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", filepath.Join(opDir(t), "..", "scripts", "op_test.py"),
		"run", "nix:github:colony-2/c2ops/main#aider")
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = testEnv(extraEnv)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	var env opEnvelope
	if strings.TrimSpace(stdout.String()) != "" {
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &env), "stdout=%s", stdout.String())
	}

	return env, stdout.String(), stderr.String(), err
}

func testEnv(extraEnv map[string]string) []string {
	envMap := map[string]string{}
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	for key, value := range extraEnv {
		envMap[key] = value
	}
	env := make([]string, 0, len(envMap))
	for key, value := range envMap {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}
	return env
}

func opDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Dir(file)
}

func writeJSONResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func chatCompletionResponse(model string, content string) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-1",
		"object":  "chat.completion",
		"created": 1,
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     5,
			"completion_tokens": 7,
			"total_tokens":      12,
		},
	}
}

func modelName(body map[string]any) string {
	if model, ok := body["model"].(string); ok && model != "" {
		return model
	}
	return "gpt-4.1"
}

func latestUserContent(body map[string]any) string {
	messages, _ := body["messages"].([]any)
	latest := ""
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "user" {
			continue
		}
		if content, ok := message["content"].(string); ok {
			latest = content
		}
	}
	return latest
}

func filterIgnorablePythonOpStderr(stderr string) string {
	lines := strings.Split(stderr, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Downloading ") {
			continue
		}
		if strings.HasPrefix(trimmed, "Downloaded ") {
			continue
		}
		if strings.HasPrefix(trimmed, "Installed ") {
			continue
		}
		filtered = append(filtered, trimmed)
	}
	return strings.Join(filtered, "\n")
}
