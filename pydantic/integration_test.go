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
	"gopkg.in/yaml.v3"
)

const uvVersion = "0.11.7"
const useSystemUVEnv = "C2OPS_USE_SYSTEM_UV"

type opManifest struct {
	WorkingDirectory string            `yaml:"working_directory"`
	Command          []string          `yaml:"command"`
	Env              map[string]string `yaml:"env"`
}

type opEnvelope struct {
	Output map[string]any `json:"output"`
}

type uvToolchain struct {
	root      string
	uvPath    string
	homeDir   string
	cacheDir  string
	configDir string
	isolated  bool
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

var bootstrap struct {
	once      sync.Once
	toolchain uvToolchain
	err       error
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

		env, stdout, stderr, err := runManifestOp(t, map[string]any{
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
		env, stdout, stderr, err := runManifestOp(t, map[string]any{
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

func runManifestOp(t *testing.T, input any, extraEnv map[string]string) (opEnvelope, string, string, error) {
	t.Helper()

	toolchain := ensureUVToolchain(t)
	manifest := loadManifest(t, filepath.Join(opDir(t), "op.yaml"))
	payload, err := json.Marshal(input)
	require.NoError(t, err)

	workingDir := opDir(t)
	if strings.TrimSpace(manifest.WorkingDirectory) != "" && manifest.WorkingDirectory != "." {
		workingDir = filepath.Join(workingDir, manifest.WorkingDirectory)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	command := append([]string(nil), manifest.Command...)
	if len(command) > 0 && command[0] == "uv" {
		command[0] = toolchain.uvPath
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = workingDir
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = manifestEnv(toolchain, manifest.Env, extraEnv)

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

func ensureUVToolchain(t *testing.T) uvToolchain {
	t.Helper()

	if shouldUseSystemUV() {
		uvPath, err := exec.LookPath("uv")
		require.NoError(t, err, "%s is set but uv is not on PATH", useSystemUVEnv)
		return uvToolchain{uvPath: uvPath}
	}

	bootstrap.once.Do(func() {
		root, err := os.MkdirTemp("", "pydantic-op-test-*")
		if err != nil {
			bootstrap.err = err
			return
		}

		toolchain := uvToolchain{
			root:      root,
			homeDir:   filepath.Join(root, "home"),
			cacheDir:  filepath.Join(root, "cache"),
			configDir: filepath.Join(root, "config"),
			isolated:  true,
		}
		bootstrap.err = os.MkdirAll(toolchain.homeDir, 0o755)
		if bootstrap.err != nil {
			return
		}
		bootstrap.err = os.MkdirAll(toolchain.cacheDir, 0o755)
		if bootstrap.err != nil {
			return
		}
		bootstrap.err = os.MkdirAll(toolchain.configDir, 0o755)
		if bootstrap.err != nil {
			return
		}

		venvDir := filepath.Join(root, "uv-venv")
		pythonExe := "python3"
		if runtime.GOOS == "windows" {
			pythonExe = "python"
		}

		if _, err := runCommand(root, 5*time.Minute, nil, pythonExe, "-m", "venv", venvDir); err != nil {
			bootstrap.err = err
			return
		}

		pipPath := filepath.Join(venvBinDir(venvDir), executableName("pip"))
		if _, err := runCommand(root, 5*time.Minute, nil, pipPath, "install", "--disable-pip-version-check", "-q", "uv=="+uvVersion); err != nil {
			bootstrap.err = err
			return
		}

		toolchain.uvPath = filepath.Join(venvBinDir(venvDir), executableName("uv"))
		bootstrap.toolchain = toolchain
	})

	require.NoError(t, bootstrap.err)
	return bootstrap.toolchain
}

func runCommand(workdir string, timeout time.Duration, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workdir
	if env != nil {
		cmd.Env = env
	}

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return output.String(), fmt.Errorf("%s timed out: %w\n%s", name, ctx.Err(), output.String())
	}
	if err != nil {
		return output.String(), fmt.Errorf("%s %v failed: %w\n%s", name, args, err, output.String())
	}
	return output.String(), nil
}

func manifestEnv(toolchain uvToolchain, manifestValues map[string]string, extraEnv map[string]string) []string {
	envMap := map[string]string{}
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	pathValue := envMap["PATH"]
	envMap["PATH"] = filepath.Dir(toolchain.uvPath) + string(os.PathListSeparator) + pathValue
	envMap["UV_NO_PROGRESS"] = "1"
	envMap["PYTHONDONTWRITEBYTECODE"] = "1"
	if toolchain.isolated {
		envMap["HOME"] = toolchain.homeDir
		envMap["XDG_CACHE_HOME"] = toolchain.cacheDir
		envMap["XDG_CONFIG_HOME"] = toolchain.configDir
		envMap["UV_CACHE_DIR"] = filepath.Join(toolchain.cacheDir, "uv")
	}

	for key, value := range manifestValues {
		envMap[key] = value
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

func shouldUseSystemUV() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(useSystemUVEnv)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func loadManifest(t *testing.T, path string) opManifest {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var manifest opManifest
	require.NoError(t, yaml.Unmarshal(data, &manifest))
	require.NotEmpty(t, manifest.Command)
	return manifest
}

func opDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Dir(file)
}

func venvBinDir(venvDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts")
	}
	return filepath.Join(venvDir, "bin")
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
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
