package codex

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Runs in the normal CLI-backed suite on both CI architectures. No provider
// account, subscription login, or external network service is needed.
func TestExternalAPIRunAndResumeSkillWithRepair(t *testing.T) {
	requireCodexCLI(t)
	t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
	t.Setenv(defaultAPIKeyEnv, "first-proxy-key")
	hostHome := filepath.Join(t.TempDir(), "missing-codex-home")
	t.Setenv("CODEX_HOME", hostHome)
	firstAPI := newMockCodexAPI(t, []mockCodexTurn{{Summary: "remember proxy-memory-42"}})
	defer firstAPI.Close()
	first := testExecuteOptions(t)
	output, err := Run(context.Background(), ExecOpInput{
		Prompt: "Remember proxy-memory-42.", Model: "gpt-5.3-codex",
		API:         &APIConfig{BaseURL: firstAPI.URL() + "/v1/"},
		WorkdirPath: first.WorkDirRoot, WorktreePath: first.WorktreeRoot,
		ArtifactInboxPath: first.ArtifactInbox, ArtifactOutboxPath: first.ArtifactOutbox,
		IdleTimeout: "15s",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", output.Status)
	require.NotNil(t, output.Session)
	require.NoDirExists(t, hostHome, "external mode needs no host auth or config")
	assertProxyRequests(t, firstAPI, "first-proxy-key", 1)
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
	t.Setenv("RESUMED_PROXY_KEY", "second-proxy-key")
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
		API:         &APIConfig{BaseURL: secondAPI.URL() + "/v1", APIKeyEnv: "RESUMED_PROXY_KEY"},
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
	assertFileContent(t, filepath.Join(second.ArtifactOutbox, "proxy-result.json"), `{"summary":"proxy-memory-42"}`)
	assertProxyRequests(t, secondAPI, "second-proxy-key", 3)
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
		require.Equal(t, "Bearer "+key, api.headers[i].Get("Authorization"))
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
