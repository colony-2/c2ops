package pydantic

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

func boundInput(server *openAIServer) map[string]any {
	return map[string]any{
		"api":              map[string]any{"base_url": server.BaseURL(), "api_key": "synthetic-process-key"},
		"default_provider": "openai", "default_model": "fixture-model",
		"prompt": "Return the bound answer", "invocation_timeout": 10,
		"response_schema": map[string]any{"type": "object", "additionalProperties": false,
			"properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []string{"answer"}},
	}
}

func TestPydanticOpIntegration(t *testing.T) {
	server := newOpenAIServer(t, func(req openAIRequest, _ int) (int, any) {
		return http.StatusOK, toolCallCompletionResponse(modelName(req.Body), "final", firstToolName(req.Body), `{"answer":"bound"}`)
	})
	defer server.Close()
	t.Run("explicit bound structured response", func(t *testing.T) {
		env, stdout, stderr, err := runPackagedOp(t, boundInput(server), nil)
		require.NoError(t, err, "stdout=%s stderr=%s", stdout, stderr)
		require.Empty(t, filterIgnorablePythonOpStderr(stderr))
		var response map[string]any
		require.NoError(t, json.Unmarshal([]byte(env.Output["response"].(string)), &response))
		require.Equal(t, map[string]any{"answer": "bound"}, response)
		require.Equal(t, "fixture-model", env.Output["model"])
		requests := server.Requests()
		require.Len(t, requests, 1)
		require.Equal(t, "/v1/chat/completions", requests[0].Path)
		require.Equal(t, "fixture-model", requests[0].Body["model"])
		require.True(t, requests[0].Header.Get("Authorization") == "Bearer synthetic-process-key", "explicit credential was not used")
		require.Equal(t, "Return the bound answer", latestUserContent(requests[0].Body))
	})
	for _, field := range []string{"api_keys", "files", "file_handling", "tools", "execute_tools", "enable_tool_execution", "tool_working_dir", "test_loopback", "fixture_loopback"} {
		t.Run("reject removed or untrusted "+field, func(t *testing.T) {
			input := boundInput(server)
			output := filepath.Join(t.TempDir(), "note.txt")
			input[field] = map[string]any{"path": output, "content": "must not exist"}
			before := len(server.Requests())
			env, stdout, stderr, err := runPackagedOp(t, input, nil)
			require.Error(t, err)
			require.Empty(t, stdout)
			require.Nil(t, env.Output)
			require.Equal(t, "pydantic: validation", filterIgnorablePythonOpStderr(stderr))
			require.Len(t, server.Requests(), before)
			require.NoFileExists(t, output)
		})
	}
	for _, field := range []string{"api", "response_schema", "invocation_timeout"} {
		t.Run("missing "+field, func(t *testing.T) {
			input := boundInput(server)
			delete(input, field)
			before := len(server.Requests())
			_, stdout, stderr, err := runPackagedOp(t, input, nil)
			require.Error(t, err)
			require.Empty(t, stdout)
			require.Equal(t, "pydantic: validation", filterIgnorablePythonOpStderr(stderr))
			require.Len(t, server.Requests(), before)
		})
	}
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
		"run", "nix:github:colony-2/c2ops/published#pydantic", "--fixture-loopback")
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

func toolCallCompletionResponse(model string, callID string, toolName string, arguments string) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-tool",
		"object":  "chat.completion",
		"created": 1,
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]any{
						{
							"id":   callID,
							"type": "function",
							"function": map[string]any{
								"name":      toolName,
								"arguments": arguments,
							},
						},
					},
				},
				"finish_reason": "tool_calls",
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

func firstToolName(body map[string]any) string {
	tools, _ := body["tools"].([]any)
	if len(tools) == 0 {
		return ""
	}
	tool, _ := tools[0].(map[string]any)
	function, _ := tool["function"].(map[string]any)
	name, _ := function["name"].(string)
	return name
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
