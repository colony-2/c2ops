package codex

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExecuteReturnsIdleTimeoutError(t *testing.T) {
	installFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
sleep 2
`)

	opts := testExecuteOptions(t)
	opts.IdleTimeout = 50 * time.Millisecond

	result, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("Execute returned setup error: %v", err)
	}
	if result.Status != StatusError {
		t.Fatalf("expected error status, got %q", result.Status)
	}
	if !strings.Contains(result.ErrorMessage, "idle timeout") {
		t.Fatalf("expected idle timeout error, got %q", result.ErrorMessage)
	}
}

func TestExecuteAcceptsStructuredOutputBeforeIdleTimeout(t *testing.T) {
	installFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' '{"type":"session.created","session_id":"test-session"}'
printf '%s\n' '{"type":"item.completed","item":{"item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"done\",\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"pendingDependencies\":[],\"errorMessage\":\"\"}"}}'
`)

	opts := testExecuteOptions(t)
	opts.IdleTimeout = time.Second

	result, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("Execute returned setup error: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %q: %s", result.Status, result.ErrorMessage)
	}
	if result.SessionID != "test-session" {
		t.Fatalf("expected session id, got %q", result.SessionID)
	}
}

func TestExecutePersistsCodexHomeStateForSession(t *testing.T) {
	sessionID := uniqueSessionID(t)
	cleanupSessionPersistence(t, sessionID)
	installFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
printf 'state-from-run' > "$CODEX_HOME/session-state.txt"
printf '%s\n' '{"type":"session.created","session_id":"`+sessionID+`"}'
printf '%s\n' '{"type":"item.completed","item":{"item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"done\",\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"pendingDependencies\":[],\"errorMessage\":\"\"}"}}'
`)

	opts := testExecuteOptions(t)
	opts.IdleTimeout = time.Second

	result, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("Execute returned setup error: %v", err)
	}
	if result.SessionID != sessionID {
		t.Fatalf("expected session id %q, got %q", sessionID, result.SessionID)
	}
	assertFileContent(t, filepath.Join(opts.ArtifactOutbox, codexHomeStateArtifactDirName, "session-state.txt"), "state-from-run")
	assertFileContent(t, filepath.Join(sessionCodexHomeStatePath(sessionID), "session-state.txt"), "state-from-run")

	resolvedHome, err := loadSessionCodexHomePath(sessionID)
	if err != nil {
		t.Fatalf("load session codex home path: %v", err)
	}
	if resolvedHome != filepath.Join(opts.WorkDirRoot, codexHomeDirName) {
		t.Fatalf("expected persisted codex home %q, got %q", filepath.Join(opts.WorkDirRoot, codexHomeDirName), resolvedHome)
	}
}

func TestExecuteRestoresCachedSessionStateForResume(t *testing.T) {
	sessionID := uniqueSessionID(t)
	cleanupSessionPersistence(t, sessionID)
	if err := os.MkdirAll(sessionCodexHomeStatePath(sessionID), 0o755); err != nil {
		t.Fatalf("create session state cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionCodexHomeStatePath(sessionID), "session-state.txt"), []byte("state-from-cache"), 0o644); err != nil {
		t.Fatalf("write session state cache: %v", err)
	}
	installFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
test "$(cat "$CODEX_HOME/session-state.txt")" = "state-from-cache"
printf '%s\n' '{"type":"item.completed","item":{"item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"resumed\",\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"pendingDependencies\":[],\"errorMessage\":\"\"}"}}'
`)

	opts := testExecuteOptions(t)
	opts.SessionID = sessionID
	opts.IdleTimeout = time.Second

	result, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("Execute returned setup error: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %q: %s", result.Status, result.ErrorMessage)
	}
	if result.SessionID != sessionID {
		t.Fatalf("expected resumed session id %q, got %q", sessionID, result.SessionID)
	}
	assertFileContent(t, filepath.Join(opts.WorkDirRoot, codexHomeDirName, "session-state.txt"), "state-from-cache")
}

func TestExecuteRestoresCodexHomeStateFromInbox(t *testing.T) {
	installFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
test "$(cat "$CODEX_HOME/session-state.txt")" = "state-from-inbox"
printf '%s\n' '{"type":"session.created","session_id":"inbox-session"}'
printf '%s\n' '{"type":"item.completed","item":{"item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"restored\",\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"pendingDependencies\":[],\"errorMessage\":\"\"}"}}'
`)

	opts := testExecuteOptions(t)
	opts.IdleTimeout = time.Second
	if err := os.MkdirAll(filepath.Join(opts.ArtifactInbox, codexHomeStateArtifactDirName), 0o755); err != nil {
		t.Fatalf("create inbox codex home state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opts.ArtifactInbox, codexHomeStateArtifactDirName, "session-state.txt"), []byte("state-from-inbox"), 0o644); err != nil {
		t.Fatalf("write inbox codex home state: %v", err)
	}

	result, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("Execute returned setup error: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %q: %s", result.Status, result.ErrorMessage)
	}
	assertFileContent(t, filepath.Join(opts.WorkDirRoot, codexHomeDirName, "session-state.txt"), "state-from-inbox")
}

func TestMaterializeSessionRolloutFilesCopiesStdoutToDiscoveredSessionPath(t *testing.T) {
	codexHome := t.TempDir()
	sessionID := uniqueSessionID(t)
	rolloutPath := filepath.Join(t.TempDir(), ".codex", "sessions", "2026", "05", sessionID+".jsonl")
	statePayload := "prefix\x00" + rolloutPath + "\x00suffix"
	if err := os.WriteFile(filepath.Join(codexHome, "state_5.sqlite"), []byte(statePayload), 0o644); err != nil {
		t.Fatalf("write fake state db: %v", err)
	}
	stdoutPath := filepath.Join(t.TempDir(), "stdout.jsonl")
	if err := os.WriteFile(stdoutPath, []byte("session-jsonl"), 0o644); err != nil {
		t.Fatalf("write stdout capture: %v", err)
	}

	if err := materializeSessionRolloutFiles(codexHome, sessionID, stdoutPath); err != nil {
		t.Fatalf("materialize session rollout files: %v", err)
	}
	assertFileContent(t, rolloutPath, "session-jsonl")
}

func installFakeCodex(t *testing.T, script string) {
	t.Helper()

	binDir := t.TempDir()
	path := filepath.Join(binDir, "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "host-codex-home"))
}

func testExecuteOptions(t *testing.T) Options {
	t.Helper()

	root := t.TempDir()
	workdir := filepath.Join(root, "workdir")
	worktree := filepath.Join(workdir, "worktree")
	inbox := filepath.Join(workdir, "inbox")
	outbox := filepath.Join(workdir, "outbox")
	for _, dir := range []string{worktree, inbox, outbox} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	return Options{
		Prompt:           "test prompt",
		WorkDirRoot:      workdir,
		WorktreeRoot:     worktree,
		ArtifactInbox:    inbox,
		ArtifactOutbox:   outbox,
		CellRelativePath: ".",
	}
}

func uniqueSessionID(t *testing.T) string {
	t.Helper()
	return "test-session-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func cleanupSessionPersistence(t *testing.T, sessionID string) {
	t.Helper()
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Dir(sessionCodexHomePathRecord(sessionID)))
		_ = os.RemoveAll(sessionCodexHomeStatePath(sessionID))
	})
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("expected %s content %q, got %q", path, want, data)
	}
}
