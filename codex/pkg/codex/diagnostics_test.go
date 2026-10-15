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

func TestLaunchDiagnosticsSurviveFailureWithoutCredentials(t *testing.T) {
	installFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
grep -q 'exec.config' "$PROGRESS_FILE"
printf '%s\n' '{"type":"turn.failed","error":{"message":"401 Unauthorized"}}'
exit 1
`)
	t.Setenv("PROXY_TEST_KEY", "worker-private-key")
	secret := "resolved-private-key"
	for _, tc := range []struct {
		name                 string
		api                  *APIConfig
		env                  map[string]string
		mode, reason, source string
	}{
		{name: "disabled", mode: "default", reason: "api_not_supplied_or_disabled", source: "codex_authentication"},
		{name: "both missing", api: &APIConfig{}, mode: "default", reason: "missing_base_url_and_api_key", source: "codex_authentication"},
		{name: "URL missing", api: &APIConfig{APIKey: &secret}, mode: "default", reason: "missing_base_url", source: "codex_authentication"},
		{name: "key missing", api: &APIConfig{BaseURL: "https://proxy.example/v1"}, mode: "default", reason: "missing_api_key", source: "codex_authentication"},
		{name: "ignored unsafe URL", api: &APIConfig{BaseURL: "https://user:password@proxy.example/v1?token=private"}, mode: "default", reason: "missing_api_key", source: "codex_authentication"},
		{name: "resolved secret", api: &APIConfig{BaseURL: "https://proxy.example/v1/", APIKey: &secret}, mode: "external_api", reason: "url_and_credentials_available", source: "api.api_key"},
		{name: "worker environment overrides secret", api: &APIConfig{BaseURL: "https://proxy.example/v1", APIKey: &secret, APIKeyEnv: "PROXY_TEST_KEY"}, mode: "external_api", reason: "url_and_credentials_available", source: "worker_environment"},
		{name: "input environment", api: &APIConfig{BaseURL: "https://proxy.example/v1", APIKeyEnv: "PROXY_TEST_KEY"}, env: map[string]string{"PROXY_TEST_KEY": "input-private-key"}, mode: "external_api", reason: "url_and_credentials_available", source: "input.env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := testExecuteOptions(t)
			opts.API = tc.api
			opts.Model = "test-model"
			opts.Prompt = "private prompt"
			opts.ExtraEnv = tc.env
			if opts.ExtraEnv == nil {
				opts.ExtraEnv = map[string]string{}
			}
			path := filepath.Join(opts.ArtifactOutbox, "codex-progress.jsonl")
			opts.ExtraEnv["PROGRESS_FILE"] = path
			opts.HostCodexHome = t.TempDir()
			for _, file := range codexCredentialFileNames {
				require.NoError(t, os.WriteFile(filepath.Join(opts.HostCodexHome, file), []byte("private host credentials"), 0600))
			}
			ctx, closeLog, err := withProgress(context.Background(), opts.ArtifactOutbox)
			require.NoError(t, err)
			defer closeLog()
			result, stdout, stderr, dir, err := Execute(ctx, opts)
			defer cleanupArtifacts(stdout, stderr, dir)
			require.NoError(t, err)
			require.Equal(t, StatusError, result.Status)
			require.Contains(t, result.ErrorMessage, "401 Unauthorized")
			configs := readConfigEvents(t, opts.ArtifactOutbox)
			require.Len(t, configs, 1)
			record := configs[0]
			require.Equal(t, tc.mode, record["mode"])
			require.Equal(t, tc.reason, record["reason"])
			require.Equal(t, tc.source, record["credential_source"])
			require.Equal(t, "test-model", record["model_override"])
			require.Equal(t, tc.mode == "default", record["auth_json_present"])
			require.Equal(t, tc.mode == "default", record["config_toml_present"])
			if tc.mode == "external_api" {
				require.Equal(t, "https://proxy.example/v1", record["base_url"])
				require.Equal(t, "c2ops_external", record["provider"])
				require.Equal(t, "responses", record["wire_api"])
				require.Equal(t, false, record["supports_websockets"])
				require.Equal(t, false, record["requires_openai_auth"])
				require.Equal(t, true, record["credential_present"])
				if tc.source == "api.api_key" {
					require.Equal(t, resolvedAPIKeyEnv, record["credential_env"])
				} else {
					require.Equal(t, "PROXY_TEST_KEY", record["credential_env"])
				}
			} else {
				require.Equal(t, "codex_config_or_default", record["provider"])
				require.NotContains(t, record, "base_url")
				require.NotContains(t, record, "credential_present", "default authentication is delegated to Codex")
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, private := range []string{secret, "worker-private-key", "input-private-key", opts.Prompt, "private host credentials", "password", "token=private"} {
				require.NotContains(t, string(data), private)
			}
		})
	}
}

func readConfigEvents(t *testing.T, outbox string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(outbox, "codex-progress.jsonl"))
	require.NoError(t, err)
	var configs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry["phase"] == "exec.config" {
			configs = append(configs, entry)
		}
	}
	return configs
}
