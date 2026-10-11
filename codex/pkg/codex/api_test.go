package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExternalAPIValidation(t *testing.T) {
	t.Setenv("CUSTOM_PROXY_KEY", "custom-proxy-key")
	for _, tc := range []struct {
		name string
		api  *APIConfig
		env  map[string]string
		err  string
	}{
		{name: "default mode"},
		{name: "no key", api: &APIConfig{BaseURL: " https://proxy.example/v1/ "}},
		{name: "custom key", api: &APIConfig{BaseURL: "http://localhost:8317/v1", APIKeyEnv: "CUSTOM_PROXY_KEY"}},
		{name: "input environment", api: &APIConfig{BaseURL: "https://proxy.example/v1", APIKeyEnv: "INPUT_ONLY_KEY"}, env: map[string]string{"INPUT_ONLY_KEY": "input-key"}},
		{name: "empty API", api: &APIConfig{}, err: "api.base_url"},
		{name: "relative URL", api: &APIConfig{BaseURL: "/v1"}, err: "api.base_url"},
		{name: "wrong scheme", api: &APIConfig{BaseURL: "file:///tmp/api"}, err: "api.base_url"},
		{name: "URL credentials", api: &APIConfig{BaseURL: "https://user:secret@proxy.example/v1"}, err: "api.base_url"},
		{name: "query", api: &APIConfig{BaseURL: "https://proxy.example/v1?key=secret"}, err: "api.base_url"},
		{name: "fragment", api: &APIConfig{BaseURL: "https://proxy.example/v1#secret"}, err: "api.base_url"},
		{name: "malformed URL", api: &APIConfig{BaseURL: "https://proxy.example/%secret"}, err: "api.base_url"},
		{name: "invalid env name", api: &APIConfig{BaseURL: "https://proxy.example/v1", APIKeyEnv: "key=secret"}, err: "api.api_key_env"},
		{name: "missing key", api: &APIConfig{BaseURL: "https://proxy.example/v1", APIKeyEnv: "MISSING_PROXY_KEY"}, env: map[string]string{"MISSING_PROXY_KEY": ""}, err: "set MISSING_PROXY_KEY"},
		{name: "empty override", api: &APIConfig{BaseURL: "https://proxy.example/v1", APIKeyEnv: "CUSTOM_PROXY_KEY"}, env: map[string]string{"CUSTOM_PROXY_KEY": " "}, err: "set CUSTOM_PROXY_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeAPI(tc.api, tc.env)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				require.NotContains(t, err.Error(), "secret")
				return
			}
			require.NoError(t, err)
			if tc.api == nil {
				require.Nil(t, got)
				return
			}
			require.Equal(t, tc.api.APIKeyEnv, got.APIKeyEnv)
			require.Equal(t, strings.TrimRight(strings.TrimSpace(tc.api.BaseURL), "/"), got.BaseURL)
		})
	}
}

func TestExternalAPIDoesNotImportSubscriptionFiles(t *testing.T) {
	host := t.TempDir()
	for _, name := range codexCredentialFileNames {
		require.NoError(t, os.WriteFile(filepath.Join(host, name), []byte("host-secret"), 0600))
	}
	for _, external := range []bool{false, true} {
		opts := testExecuteOptions(t)
		opts.HostCodexHome = host
		if external {
			opts.API = &APIConfig{BaseURL: "https://proxy.example/v1"}
		}
		require.NoError(t, prepareDirectCodexHome(opts))
		for _, name := range codexCredentialFileNames {
			if external {
				require.NoFileExists(t, filepath.Join(opts.CodexHome, name))
			} else {
				assertFileContent(t, filepath.Join(opts.CodexHome, name), "host-secret")
			}
			assertFileContent(t, filepath.Join(host, name), "host-secret")
		}
	}
}

func TestExternalAPICommandKeepsKeyOutOfArguments(t *testing.T) {
	t.Setenv("C2OPS_CODEX_API_KEY", "private-proxy-key")
	opts := testExecuteOptions(t)
	opts.API = &APIConfig{BaseURL: "https://proxy.example/v1/", APIKeyEnv: "C2OPS_CODEX_API_KEY"}
	opts.SessionID = "resume-id"
	require.NoError(t, opts.validate())
	cmd := buildCommand(opts, "/schema.json")
	joined := strings.Join(cmd, " ")
	require.Contains(t, joined, `base_url="https://proxy.example/v1"`)
	require.Contains(t, joined, `env_key="C2OPS_CODEX_API_KEY"`)
	require.Contains(t, joined, "requires_openai_auth=false")
	require.Contains(t, joined, "supports_websockets=false")
	require.NotContains(t, joined, "private-proxy-key")
	require.Equal(t, []string{"resume", "resume-id", opts.Prompt}, cmd[len(cmd)-3:])
}

func TestExternalAPIInputs(t *testing.T) {
	payload := []byte(`{"api":{"base_url":"https://proxy.example/v1","api_key_env":"PROXY_KEY"}}`)
	var execInput ExecOpInput
	var skillInput SkillRunInput
	require.NoError(t, json.Unmarshal(payload, &execInput))
	require.NoError(t, json.Unmarshal(payload, &skillInput))
	require.Equal(t, execInput.API, execInputForSkillRun(skillInput).API)
	for _, input := range []any{&execInput, &skillInput} {
		require.Error(t, json.Unmarshal([]byte(`{"api":{"base_url":"https://proxy.example/v1","api_key":"secret"}}`), input))
	}
}
