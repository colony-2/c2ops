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

func TestPydanticOpIntegration(t *testing.T) {
	t.Run("structured response with file context", func(t *testing.T) {
		server := newOpenAIServer(t, func(req openAIRequest, _ int) (int, any) {
			sawFileContext := strings.Contains(latestUserContent(req.Body), "### File Context ###")
			return http.StatusOK, toolCallCompletionResponse(
				modelName(req.Body),
				"call_final",
				"final_result",
				fmt.Sprintf(`{"saw_file_context":%t}`, sawFileContext),
			)
		})
		defer server.Close()

		env, stdout, stderr, err := runPackagedOp(t, map[string]any{
			"default_provider": "openai",
			"default_model":    "gpt-4.1",
			"prompt":           "summarize the supplied file",
			"api_keys": map[string]any{
				"openai": "dummy",
			},
			"response_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"saw_file_context": map[string]any{"type": "boolean"},
				},
				"required": []string{"saw_file_context"},
			},
			"files": []map[string]any{
				{
					"path":    "README.md",
					"type":    "markdown",
					"content": "fixture file body",
				},
			},
		}, localOpenAIEnv(server))
		require.NoError(t, err, "stdout=%s stderr=%s", stdout, stderr)
		require.Empty(t, filterIgnorablePythonOpStderr(stderr))

		responseText := env.Output["response"].(string)
		var response map[string]any
		require.NoError(t, json.Unmarshal([]byte(responseText), &response))
		require.Equal(t, true, response["saw_file_context"])
		require.Equal(t, "gpt-4.1", env.Output["model"])

		requests := server.Requests()
		require.Len(t, requests, 1)
		require.Equal(t, "required", requests[0].Body["tool_choice"])
		require.Equal(t, "final_result", firstToolName(requests[0].Body))
		require.Contains(t, latestUserContent(requests[0].Body), "fixture file body")
	})

	t.Run("tool execution writes file", func(t *testing.T) {
		server := newOpenAIServer(t, func(req openAIRequest, index int) (int, any) {
			if index == 0 {
				return http.StatusOK, toolCallCompletionResponse(
					modelName(req.Body),
					"call_1",
					"write_file",
					`{"path":"note.txt","content":"hello from tool"}`,
				)
			}
			return http.StatusOK, chatCompletionResponse(modelName(req.Body), "completed after 1 tool results")
		})
		defer server.Close()

		workdir := t.TempDir()
		env, stdout, stderr, err := runPackagedOp(t, map[string]any{
			"default_provider":      "openai",
			"default_model":         "gpt-4.1",
			"prompt":                "create a note using the write_file tool",
			"api_keys":              map[string]any{"openai": "dummy"},
			"enable_tool_execution": true,
			"execute_tools":         true,
			"max_tool_rounds":       1,
			"default_working_dir":   workdir,
			"tool_working_dir":      workdir,
			"tools": []map[string]any{
				{
					"name":        "write_file",
					"description": "Write a file",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path":    map[string]any{"type": "string"},
							"content": map[string]any{"type": "string"},
						},
						"required": []string{"path", "content"},
					},
				},
			},
		}, localOpenAIEnv(server))
		require.NoError(t, err, "stdout=%s stderr=%s", stdout, stderr)
		require.Empty(t, filterIgnorablePythonOpStderr(stderr))

		notePath := filepath.Join(workdir, "note.txt")
		content, readErr := os.ReadFile(notePath)
		require.NoError(t, readErr)
		require.Equal(t, "hello from tool", string(content))

		require.Equal(t, "completed after 1 tool results", env.Output["response"])
		require.Len(t, env.Output["tool_calls"].([]any), 1)
		require.Len(t, env.Output["tool_results"].([]any), 1)
		require.Contains(t, stringifySlice(env.Output["files_written"].([]any)), "note.txt")

		requests := server.Requests()
		require.Len(t, requests, 2)
		require.Equal(t, "write_file", firstToolName(requests[0].Body))
		require.Contains(t, toolMessageContent(requests[1].Body), `"path":"note.txt"`)
	})
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
		"run", "nix:github:colony-2/c2ops/main#pydantic")
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

func localOpenAIEnv(server *openAIServer) map[string]string {
	return map[string]string{
		"OPENAI_BASE_URL": server.BaseURL(),
		"OPENAI_API_BASE": server.BaseURL(),
	}
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

func toolMessageContent(body map[string]any) string {
	messages, _ := body["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		content, _ := message["content"].(string)
		return content
	}
	return ""
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

func stringifySlice(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
