package codex

import (
	"context"
	"os"
	"path/filepath"
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

func installFakeCodex(t *testing.T, script string) {
	t.Helper()

	binDir := t.TempDir()
	path := filepath.Join(binDir, "codex")
	script = strings.Replace(script, "set -euo pipefail", "set -euo pipefail\nif [[ ${1:-} == --version ]]; then echo codex-cli 0.157.1; exit 0; fi", 1)
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
		CodexHome:      filepath.Join(workdir, ".codex"),
		Prompt:         "test prompt",
		WorkDirRoot:    workdir,
		WorktreeRoot:   worktree,
		ArtifactInbox:  inbox,
		ArtifactOutbox: outbox,
	}
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
