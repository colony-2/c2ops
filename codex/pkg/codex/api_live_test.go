package codex

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Codex 0.161.0 silently drops Authorization if the token contains a newline.
// Exercise the real CLI so nonempty environment values alone cannot pass this test.
func TestExternalAPITrimsCredentialWhitespace(t *testing.T) {
	requireCodexCLI(t)
	for _, source := range []string{"secret", "worker_environment", "input_environment"} {
		t.Run(source, func(t *testing.T) {
			provider := newMockCodexAPI(t, []mockCodexTurn{{Summary: "authenticated"}})
			defer provider.Close()
			opts := testExecuteOptions(t)
			opts.Model = "gpt-5.3-codex"
			key := " \tpadded-proxy-key\r\n"
			opts.API = &APIConfig{BaseURL: provider.URL() + "/v1", APIKey: &key}
			if source != "secret" {
				opts.API.APIKeyEnv = "PADDED_PROXY_KEY"
				// The environment credential must still override the tenant secret.
				opts.API.APIKey = apiTestKey("unused-tenant-key")
				t.Setenv("PADDED_PROXY_KEY", key)
				if source == "input_environment" {
					t.Setenv("PADDED_PROXY_KEY", "wrong-worker-key")
					opts.ExtraEnv = map[string]string{"PADDED_PROXY_KEY": key}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			result, stdout, stderr, artifacts, err := Execute(ctx, opts)
			defer cleanupArtifacts(stdout, stderr, artifacts)
			require.NoError(t, err)
			require.Equal(t, StatusCompleted, result.Status, result.ErrorMessage)
			assertProxyRequests(t, provider, "padded-proxy-key", 1)
		})
	}
}

func TestExternalAPIMissingConfigOrSecretUsesDefaultProvider(t *testing.T) {
	requireCodexCLI(t)
	for _, payload := range []string{
		`{"base_url":null,"api_key":"unused-secret"}`,
		`{"base_url":"http://127.0.0.1:1/v1"}`,
		`{"base_url":null}`,
	} {
		t.Run(payload, func(t *testing.T) {
			provider := newMockCodexAPI(t, []mockCodexTurn{{Summary: "default provider preserved"}})
			defer provider.Close()
			opts := liveExecuteOptions(t, provider)
			require.NoError(t, json.Unmarshal([]byte(payload), &opts.API))
			result, stdout, stderr, artifacts, err := Execute(context.Background(), opts)
			defer cleanupArtifacts(stdout, stderr, artifacts)
			require.NoError(t, err)
			require.Equal(t, StatusCompleted, result.Status, result.ErrorMessage)
			assertProxyRequests(t, provider, "dummy", 1)
			logs, err := os.ReadFile(stderr)
			require.NoError(t, err)
			require.NotContains(t, string(logs), "unused-secret")
		})
	}
}

// Runs in the normal CLI-backed suite on both CI architectures. No provider
// account, subscription login, or external network service is needed.
func TestExternalAPIRunAndResumeSkillWithRepair(t *testing.T) {
	requireCodexCLI(t)
	t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
	firstKey := "first-proxy-key"
	t.Setenv(resolvedAPIKeyEnv, "stale-ambient-key")
	hostHome := filepath.Join(t.TempDir(), "missing-codex-home")
	t.Setenv("CODEX_HOME", hostHome)
	firstAPI := newMockCodexAPI(t, []mockCodexTurn{{Summary: "remember proxy-memory-42"}})
	defer firstAPI.Close()
	first := testExecuteOptions(t)
	output, err := Run(context.Background(), ExecOpInput{
		Prompt: "Remember proxy-memory-42.", Model: "gpt-5.3-codex",
		API:         &APIConfig{BaseURL: firstAPI.URL() + "/v1/", APIKey: &firstKey},
		WorkdirPath: first.WorkDirRoot, WorktreePath: first.WorktreeRoot,
		ArtifactInboxPath: first.ArtifactInbox, ArtifactOutboxPath: first.ArtifactOutbox,
		IdleTimeout: "15s",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", output.Status)
	require.NotNil(t, output.Session)
	require.NoDirExists(t, hostHome, "external mode needs no host auth or config")
	assertProxyRequests(t, firstAPI, "first-proxy-key", 1)
	firstConfig := readConfigEvents(t, first.ArtifactOutbox)
	require.Len(t, firstConfig, 1)
	require.Equal(t, "external_api", firstConfig[0]["mode"])
	require.Equal(t, firstAPI.URL()+"/v1", firstConfig[0]["base_url"])
	assertNoProxyKeys(t, first.ArtifactOutbox, os.Getenv("C2J_OBJECT_OUTBOX"))

	store := checkpointStore(t)
	draft := output.Objects[output.Session.Name]
	ref, err := store.Publish(context.Background(), draft.Type, draft.Metadata, draft.Files)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(first.WorkDirRoot))
	firstAPI.Close() // Resume must use the new endpoint, not the checkpoint's old one.

	// Host settings must not override the requested proxy, even if present.
	require.NoError(t, os.MkdirAll(hostHome, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(hostHome, "config.toml"), []byte("invalid host config"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(hostHome, "auth.json"), []byte("subscription-secret"), 0600))
	secondKey := "second-proxy-key"
	secondAPI := newMockCodexAPI(t, []mockCodexTurn{
		{Summary: "output needs repair"},
		{Command: `test ! -e "$CODEX_HOME/auth.json" && test ! -e "$CODEX_HOME/config.toml" && printf '%s' '{"summary":"proxy-memory-42"}' > ../outbox/proxy-result.json`},
		{Summary: "repaired proxy-memory-42"},
	})
	defer secondAPI.Close()
	second := testExecuteOptions(t)
	createLocalSkill(t, second.WorktreeRoot, "proxy-smoke")
	skillOutput, err := RunSkill(context.Background(), SkillRunInput{
		Skill: "proxy-smoke", Session: hydrateSession(t, store, ref),
		Prompt: "Recall the remembered fact and write proxy-result.json.", Model: "gpt-5.3-codex",
		API:         &APIConfig{BaseURL: secondAPI.URL() + "/v1", APIKey: &secondKey},
		WorkdirPath: second.WorkDirRoot, WorktreePath: second.WorktreeRoot,
		ArtifactInboxPath: second.ArtifactInbox, ArtifactOutboxPath: second.ArtifactOutbox,
		IdleTimeout: "15s",
		Output: SkillRunOutputSpec{
			Path:   "proxy-result.json",
			Schema: json.RawMessage(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`),
		},
	})
	require.NoError(t, err)
	require.Equal(t, "completed", skillOutput.Status)
	require.Equal(t, output.SessionID, skillOutput.SessionID)
	require.NotNil(t, skillOutput.Session)
	require.True(t, skillOutput.OutputSchemaValid)
	require.Equal(t, 1, skillOutput.OutputRepairAttempts)
	transcript, err := os.ReadFile(filepath.Join(second.ArtifactOutbox, "stdout.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(transcript), "output needs repair")
	require.Contains(t, string(transcript), "repaired proxy-memory-42")
	require.Contains(t, string(transcript), "command_execution")
	nativeEvents, err := json.Marshal(skillOutput.Events)
	require.NoError(t, err)
	require.Contains(t, string(nativeEvents), "output needs repair")
	require.Contains(t, string(nativeEvents), "repaired proxy-memory-42")
	assertFileContent(t, filepath.Join(second.ArtifactOutbox, "proxy-result.json"), `{"summary":"proxy-memory-42"}`)
	assertProxyRequests(t, secondAPI, "second-proxy-key", 3)
	secondConfigs := readConfigEvents(t, second.ArtifactOutbox)
	require.Len(t, secondConfigs, 2, "resume and repair each retain launch settings")
	for _, config := range secondConfigs {
		require.Equal(t, secondAPI.URL()+"/v1", config["base_url"])
		require.Equal(t, true, config["resuming"])
	}
	assertNoProxyKeys(t, second.ArtifactOutbox, os.Getenv("C2J_OBJECT_OUTBOX"))
	secondAPI.mu.Lock()
	history, err := json.Marshal(secondAPI.requests[0]["input"])
	secondAPI.mu.Unlock()
	require.NoError(t, err)
	require.Contains(t, string(history), "Remember proxy-memory-42.")
	assertFileContent(t, filepath.Join(hostHome, "auth.json"), "subscription-secret")
}

func assertProxyRequests(t *testing.T, api *mockCodexAPI, key string, count int) {
	t.Helper()
	api.mu.Lock()
	defer api.mu.Unlock()
	require.Len(t, api.requests, count)
	for i, request := range api.requests {
		require.Equal(t, "gpt-5.3-codex", request["model"])
		if key == "" {
			require.NotContains(t, api.headers[i], "Authorization")
		} else {
			require.Equal(t, "Bearer "+key, api.headers[i].Get("Authorization"))
		}
		require.Empty(t, api.headers[i].Get("ChatGPT-Account-Id"))
	}
}

func assertNoProxyKeys(t *testing.T, roots ...string) {
	t.Helper()
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, key := range []string{"first-proxy-key", "second-proxy-key", "subscription-secret"} {
				require.NotContains(t, string(data), key, "credential leaked into %s", path)
			}
			return nil
		}))
	}
}

// The diagnostic must survive an actual CLI 401 and an op error for both entrypoints.
func TestExternalAPIUnauthorizedKeepsLaunchDiagnostic(t *testing.T) {
	requireCodexCLI(t)
	t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "unused-home"))
	for _, skill := range []bool{false, true} {
		name := "codex"
		if skill {
			name = "skill-run"
		}
		t.Run(name, func(t *testing.T) {
			requests := make(chan string, 32)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case requests <- r.Header.Get("Authorization"):
				default:
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid proxy client key","type":"authentication_error"}}`))
			}))
			defer server.Close()
			opts := testExecuteOptions(t)
			key := "rejected-private-key"
			api := &APIConfig{BaseURL: server.URL + "/v1", APIKey: &key}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var err error
			if skill {
				createLocalSkill(t, opts.WorktreeRoot, "auth-test")
				_, err = RunSkill(ctx, SkillRunInput{Skill: "auth-test", Prompt: "test", Model: "gpt-5.3-codex", API: api, WorkdirPath: opts.WorkDirRoot, WorktreePath: opts.WorktreeRoot, ArtifactInboxPath: opts.ArtifactInbox, ArtifactOutboxPath: opts.ArtifactOutbox})
			} else {
				_, err = Run(ctx, ExecOpInput{Prompt: "test", Model: "gpt-5.3-codex", API: api, WorkdirPath: opts.WorkDirRoot, WorktreePath: opts.WorktreeRoot, ArtifactInboxPath: opts.ArtifactInbox, ArtifactOutboxPath: opts.ArtifactOutbox})
			}
			require.ErrorContains(t, err, "401")
			require.NotContains(t, err.Error(), key)
			select {
			case auth := <-requests:
				require.Equal(t, "Bearer "+key, auth)
			default:
				t.Fatal("CLI did not contact the proxy")
			}
			configs := readConfigEvents(t, opts.ArtifactOutbox)
			require.Len(t, configs, 1)
			require.Equal(t, "external_api", configs[0]["mode"])
			require.Equal(t, api.BaseURL, configs[0]["base_url"])
			require.Equal(t, true, configs[0]["credential_present"])
			require.Equal(t, "api.api_key", configs[0]["credential_source"])
			require.FileExists(t, filepath.Join(opts.ArtifactOutbox, "stderr.txt"))
			require.FileExists(t, filepath.Join(opts.ArtifactOutbox, "stdout.jsonl"))
			require.NoError(t, filepath.WalkDir(opts.ArtifactOutbox, func(path string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				data, err := os.ReadFile(path)
				require.NotContains(t, string(data), key)
				return err
			}))
		})
	}
}
