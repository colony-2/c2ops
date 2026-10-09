package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func seedTestSession(t *testing.T, home, id string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "sessions"), 0700))
	for _, name := range sessionDatabases {
		path := filepath.Join(home, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			require.NoError(t, os.WriteFile(path, nil, 0600))
		}
		db, err := openStateDB(path)
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE IF NOT EXISTS threads (id TEXT PRIMARY KEY, rollout_path TEXT, cwd TEXT)")
		require.NoError(t, err)
		_, err = db.Exec("INSERT OR IGNORE INTO threads VALUES (?, ?, ?)", id, filepath.Join(home, "sessions", id+".jsonl"), "old-worktree")
		require.NoError(t, err)
		require.NoError(t, db.Close())
	}
	path := filepath.Join(home, "sessions", id+".jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		require.NoError(t, os.WriteFile(path, []byte("original"), 0600))
	}
}

func checkpointStore(t *testing.T) *objects.Store {
	t.Helper()
	artifacts := map[string]jobdb.Artifact{}
	var mu sync.Mutex
	return objects.NewStore(objects.Config{
		JobKey: jobdb.JobKey{TenantId: "test", JobId: "job"}, TaskOrdinal: 1, Workdir: t.TempDir(),
		AddArtifact: func(a jobdb.Artifact) error {
			b, err := a.Bytes(context.Background())
			if err != nil {
				return err
			}
			a.Cleanup()
			mu.Lock()
			defer mu.Unlock()
			artifacts[a.Name()] = jobdb.NewArtifactFromBytes(a.Name(), b)
			return nil
		},
		GetArtifact: func(k jobdb.ArtifactKey) (jobdb.Artifact, error) {
			mu.Lock()
			defer mu.Unlock()
			a := artifacts[k.Name]
			if a == nil {
				return nil, fmt.Errorf("missing artifact")
			}
			return a, nil
		},
	})
}

func freezeSession(t *testing.T, store *objects.Store, state *sessionExecution) objects.Ref {
	t.Helper()
	marker, drafts, err := state.publish()
	require.NoError(t, err)
	require.Equal(t, "session", marker.Name)
	draft := drafts[marker.Name]
	ref, err := store.Publish(context.Background(), draft.Type, draft.Metadata, draft.Files)
	require.NoError(t, err)
	return ref
}

func hydrateSession(t *testing.T, store *objects.Store, ref objects.Ref) *SessionInput {
	t.Helper()
	snapshot, err := store.Open(context.Background(), ref, SessionObjectType)
	require.NoError(t, err)
	t.Cleanup(func() { snapshot.Close() })
	// Cross the same JSON boundary as an extension process, without assigning
	// framework Go types to the production input structure.
	payload, err := json.Marshal(map[string]any{
		"ref": ref, "metadata": snapshot.Metadata, "files": snapshot.Files,
	})
	require.NoError(t, err)
	var input SessionInput
	require.NoError(t, json.Unmarshal(payload, &input))
	return &input
}

func TestSessionReferenceRemainsOpaque(t *testing.T) {
	ref := json.RawMessage(`{"$c2j_object":"v1","type":"c2ops.codex.session/v1","future_field":{"value":42}}`)
	payload, err := json.Marshal(map[string]any{"session": map[string]any{
		"ref":      ref,
		"metadata": SessionMetadata{SessionID: "id", RuntimeVersion: "0.157.1", StateFormat: sessionStateFormat},
		"files":    map[string]string{"home": "/hydrated/home"},
	}})
	require.NoError(t, err)
	var execInput ExecOpInput
	require.NoError(t, json.Unmarshal(payload, &execInput))
	require.JSONEq(t, string(ref), string(execInput.Session.Ref))
	var skillInput SkillRunInput
	require.NoError(t, json.Unmarshal(payload, &skillInput))
	require.JSONEq(t, string(ref), string(skillInput.Session.Ref))
}

func TestSessionBranchesRestoreExactCheckpoint(t *testing.T) {
	installFakeCodex(t, "#!/usr/bin/env bash\nset -euo pipefail\n")
	t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
	store := checkpointStore(t)
	home := t.TempDir()
	seedTestSession(t, home, "same-id")
	// Secrets and transient files must never enter the exported home.
	require.NoError(t, os.WriteFile(filepath.Join(home, "auth.json"), []byte("secret"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte("secret"), 0600))
	state := &sessionExecution{runtimeVersion: "0.157.1", home: home, id: "same-id", outbox: os.Getenv("C2J_OBJECT_OUTBOX")}
	a := freezeSession(t, store, state)
	require.NoError(t, os.RemoveAll(home))
	resume := func(ref objects.Ref) *sessionExecution {
		input := hydrateSession(t, store, ref)
		_, err := os.Stat(filepath.Join(input.Files["home"], "auth.json"))
		require.True(t, os.IsNotExist(err))
		paths := execRunPaths{Workdir: t.TempDir(), Worktree: t.TempDir(), Inbox: t.TempDir()}
		result, err := prepareSession(context.Background(), input, paths, nil)
		require.NoError(t, err)
		t.Cleanup(result.close)
		return result
	}
	b := resume(a)
	rollout := func(s *sessionExecution) string { return filepath.Join(s.home, "sessions", "same-id.jsonl") }
	assertFileContent(t, rollout(b), "original")
	require.NoError(t, os.WriteFile(rollout(b), []byte("branch B"), 0600))
	bRef := freezeSession(t, store, b)
	c := resume(a)
	assertFileContent(t, rollout(c), "original")
	assertFileContent(t, rollout(resume(bRef)), "branch B")
	// A failed attempt mutates only its private copy; a retry starts from A.
	require.NoError(t, os.WriteFile(rollout(c), []byte("failed attempt"), 0600))
	c.close()
	assertFileContent(t, rollout(resume(a)), "original")
	// Concurrent independent branches use the same logical ID without sharing files.
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			branch := resume(a)
			assertFileContent(t, rollout(branch), "original")
			_ = freezeSession(t, store, branch)
		}()
	}
	wg.Wait()
}

func TestSessionExportIncludesWALAndRejectsIncompleteState(t *testing.T) {
	home := t.TempDir()
	seedTestSession(t, home, "id")
	db, err := openStateDB(filepath.Join(home, "goals_1.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE goal(value TEXT); INSERT INTO goal VALUES ('from WAL')")
	require.NoError(t, err)
	exported := filepath.Join(t.TempDir(), "home")
	require.NoError(t, exportSessionHome(home, exported))
	copyDB, err := openStateDB(filepath.Join(exported, "goals_1.sqlite"))
	require.NoError(t, err)
	defer copyDB.Close()
	var value string
	require.NoError(t, copyDB.QueryRow("SELECT value FROM goal").Scan(&value))
	require.Equal(t, "from WAL", value)
	require.NoError(t, os.Remove(filepath.Join(home, "sessions", "id.jsonl")))
	s := &sessionExecution{runtimeVersion: "0.157.1", home: home, id: "id", outbox: t.TempDir()}
	marker, drafts, err := s.publish()
	require.Error(t, err)
	require.Nil(t, marker)
	require.Nil(t, drafts)
}

func TestSessionRejectsLegacyInputsAndInvalidState(t *testing.T) {
	for _, payload := range []string{`{"sessionId":"old"}`, `{"sessionId":""}`, `{"resume_context":{}}`, `{"session":null}`} {
		var execInput ExecOpInput
		require.Error(t, json.Unmarshal([]byte(payload), &execInput))
		var skillInput SkillRunInput
		require.Error(t, json.Unmarshal([]byte(payload), &skillInput))
	}
	installFakeCodex(t, "#!/usr/bin/env bash\nset -euo pipefail\n")
	paths := execRunPaths{Workdir: t.TempDir(), Worktree: t.TempDir(), Inbox: t.TempDir()}
	t.Setenv("C2J_OBJECT_OUTBOX", "")
	_, err := prepareSession(context.Background(), nil, paths, nil)
	require.ErrorContains(t, err, "C2J_OBJECT_OUTBOX")
	t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
	_, err = prepareSession(context.Background(), nil, paths, map[string]string{"CODEX_HOME": "/shared"})
	require.ErrorContains(t, err, "managed")
	require.NoError(t, os.Mkdir(filepath.Join(paths.Inbox, legacyHomeArtifact), 0700))
	_, err = prepareSession(context.Background(), nil, paths, nil)
	require.ErrorContains(t, err, "no longer supported")
	require.NoError(t, os.Remove(filepath.Join(paths.Inbox, legacyHomeArtifact)))
	_, err = prepareSession(context.Background(), &SessionInput{}, paths, nil)
	require.Error(t, err)
	home := t.TempDir()
	seedTestSession(t, home, "id")
	require.NoError(t, os.Symlink("/missing", filepath.Join(home, "sessions", "link")))
	require.Error(t, exportSessionHome(home, filepath.Join(t.TempDir(), "export")))
}

func TestRunFailureDoesNotPublish(t *testing.T) {
	installFakeCodex(t, "#!/usr/bin/env bash\nset -euo pipefail\n")
	outbox := t.TempDir()
	t.Setenv("C2J_OBJECT_OUTBOX", outbox)
	original := executeLibrary
	t.Cleanup(func() { executeLibrary = original })
	var home string
	executeLibrary = func(_ context.Context, opts Options) (Result, string, string, string, error) {
		home = opts.CodexHome
		seedTestSession(t, home, "failed-session")
		return Result{}, "", "", "", errors.New("process failed after mutation")
	}
	output, err := Run(context.Background(), ExecOpInput{Prompt: "fail", WorkdirPath: t.TempDir(), WorktreePath: t.TempDir()})
	require.ErrorContains(t, err, "process failed")
	require.Nil(t, output.Session)
	require.Empty(t, output.Objects)
	entries, err := os.ReadDir(outbox)
	require.NoError(t, err)
	require.Empty(t, entries)
	_, err = os.Stat(home)
	require.True(t, os.IsNotExist(err), "private credentials/state must be cleaned on failure")
}

func TestSessionRejectsUnsupportedMetadataAndCorruptDatabase(t *testing.T) {
	installFakeCodex(t, "#!/usr/bin/env bash\nset -euo pipefail\n")
	t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
	store := checkpointStore(t)
	home := t.TempDir()
	seedTestSession(t, home, "id")
	ref := freezeSession(t, store, &sessionExecution{runtimeVersion: "0.157.1", home: home, id: "id", outbox: t.TempDir()})
	paths := execRunPaths{Workdir: t.TempDir(), Worktree: t.TempDir(), Inbox: t.TempDir()}
	for _, change := range []func(*SessionInput){
		func(in *SessionInput) { in.Metadata.RuntimeVersion = "future" },
		func(in *SessionInput) { in.Metadata.StateFormat = "unknown/v2" },
		func(in *SessionInput) { in.Metadata.SessionID = "absent" },
		func(in *SessionInput) {
			require.NoError(t, os.WriteFile(filepath.Join(in.Files["home"], "queue_1.sqlite"), []byte("broken"), 0600))
		},
		func(in *SessionInput) {
			require.NoError(t, os.Remove(filepath.Join(in.Files["home"], "sessions", "id.jsonl")))
		},
	} {
		in := hydrateSession(t, store, ref)
		change(in)
		_, err := prepareSession(context.Background(), in, paths, nil)
		require.Error(t, err)
	}
}
