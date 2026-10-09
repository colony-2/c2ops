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

// Opt-in experiment: the matrix runner supplies a real CLI behind a shim that
// changes only --version. Production version checks and storage code stay intact.
func TestCompatibilityRunAndRunSkill(t *testing.T) {
	if os.Getenv("C2OPS_CODEX_COMPAT") != "1" {
		t.Skip("run with scripts/compatibility_matrix.py")
	}
	api := newMockCodexAPI(t, []mockCodexTurn{
		{Summary: "stored compatibility-memory-42"},
		{Command: "pwd > resumed-path.txt; printf '%s' '{\"summary\":\"skill result\"}' > ../outbox/skill-result.json"},
		{Summary: "compatibility-memory-42"},
	})
	defer api.Close()
	t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
	first := liveExecuteOptions(t, api)
	t.Setenv("CODEX_HOME", first.HostCodexHome)
	output, err := Run(context.Background(), ExecOpInput{
		Prompt: "Remember compatibility-memory-42 for the next turn.", Env: first.ExtraEnv,
		WorkdirPath: first.WorkDirRoot, WorktreePath: first.WorktreeRoot,
		ArtifactInboxPath: first.ArtifactInbox, ArtifactOutboxPath: first.ArtifactOutbox,
		IdleTimeout: "15s",
	})
	require.NoError(t, err, "fresh Run including checkpoint export")
	require.Equal(t, "completed", output.Status)
	require.NotNil(t, output.Session)
	store := checkpointStore(t)
	draft := output.Objects[output.Session.Name]
	ref, err := store.Publish(context.Background(), draft.Type, draft.Metadata, draft.Files)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(first.WorkDirRoot))
	second := liveExecuteOptions(t, api)
	t.Setenv("CODEX_HOME", second.HostCodexHome)
	if resumePath := os.Getenv("C2OPS_CODEX_COMPAT_RESUME_PATH"); resumePath != "" {
		t.Setenv("PATH", resumePath+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	createLocalSkill(t, second.WorktreeRoot, "compat-smoke")
	skillOutput, err := RunSkill(context.Background(), SkillRunInput{
		Skill: "compat-smoke", Session: hydrateSession(t, store, ref),
		Prompt: "Use the remembered fact and write skill-result.json.", Env: second.ExtraEnv,
		WorkdirPath: second.WorkDirRoot, WorktreePath: second.WorktreeRoot,
		ArtifactInboxPath: second.ArtifactInbox, ArtifactOutboxPath: second.ArtifactOutbox,
		IdleTimeout: "15s",
		Output: SkillRunOutputSpec{
			Path:   "skill-result.json",
			Schema: json.RawMessage(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`),
		},
	})
	require.NoError(t, err, "RunSkill from hydrated checkpoint, including successor export")
	require.Equal(t, "completed", skillOutput.Status)
	require.True(t, skillOutput.OutputSchemaValid)
	require.Equal(t, output.SessionID, skillOutput.SessionID)
	require.NotNil(t, skillOutput.Session)
	assertFileContent(t, filepath.Join(second.WorktreeRoot, "resumed-path.txt"), second.WorktreeRoot+"\n")
	assertFileContent(t, filepath.Join(second.ArtifactOutbox, "skill-result.json"), `{"summary":"skill result"}`)
	_, err = os.Stat(first.WorkDirRoot)
	require.True(t, os.IsNotExist(err), "resume must not recreate old paths")
	api.mu.Lock()
	request, marshalErr := json.Marshal(api.requests[len(api.requests)-1]["input"])
	api.mu.Unlock()
	require.NoError(t, marshalErr)
	require.True(t, strings.Contains(string(request), "Remember compatibility-memory-42"), "history must reach provider")
}
