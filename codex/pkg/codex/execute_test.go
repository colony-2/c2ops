package codex

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWatchdogBoundsPipeDrainAfterProcessExit(t *testing.T) {
	cmd := exec.Command("bash", "-c", "sleep 5 & exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	collector := newOutputCollector(io.Discard, io.Discard)
	cmd.Stdout = collector.stdoutWriter()
	cmd.Stderr = collector.stderrWriter()
	started := time.Now()
	err := runCommandWithWatchdog(context.Background(), cmd, 10*time.Second, collector)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("expected bounded pipe-drain error; elapsed=%s err=%v", time.Since(started), err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("waited for an inherited pipe indefinitely")
	}
}

func TestVersionProbeIsBoundedBeforeIdleWatchdog(t *testing.T) {
	// Deliberately omit the helper's --version fast path.
	installFakeCodex(t, "#!/usr/bin/env bash\nsleep 5\n")
	started := time.Now()
	_, err := readCodexVersion(context.Background(), 25*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected version probe deadline, got %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("version check did not stop its process group")
	}
}

func TestExecutePreservesLogsOnOversizedOutput(t *testing.T) {
	installFakeCodex(t, "#!/usr/bin/env bash\nset -euo pipefail\nprintf '%*s\\n' 8388608 ''\nprintf 'diagnostic\\n' >&2\n")
	_, stdout, stderr, dir, err := Execute(context.Background(), testExecuteOptions(t))
	defer cleanupArtifacts(stdout, stderr, dir)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	info, statErr := os.Stat(stdout)
	if statErr != nil || info.Size() != 8388609 {
		t.Fatalf("stdout capture was lost: info=%v err=%v", info, statErr)
	}
	assertFileContent(t, stderr, "diagnostic\n")
}

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
