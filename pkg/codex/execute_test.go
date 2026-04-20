package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/shai/pkg/shai"
	"github.com/stretchr/testify/require"
)

type fakeClock struct{ ts time.Time }

func (f fakeClock) Now() time.Time { return f.ts }

type fakeRunner struct {
	runFn  func(ctx context.Context) error
	closed bool
}

func (f *fakeRunner) Run(ctx context.Context) error {
	if f.runFn != nil {
		return f.runFn(ctx)
	}
	return nil
}

func (f *fakeRunner) Close() error {
	f.closed = true
	return nil
}

type runnerHarness struct {
	t      *testing.T
	lines  []string
	stderr []string
	runErr error
	config *shai.SandboxConfig
	runner *fakeRunner
}

func (h *runnerHarness) factory(cfg *shai.SandboxConfig) (Runner, error) {
	h.config = cfg
	h.runner = &fakeRunner{runFn: func(ctx context.Context) error {
		for _, line := range h.lines {
			_, _ = cfg.Stdout.Write([]byte(line + "\n"))
		}
		for _, line := range h.stderr {
			_, _ = cfg.Stderr.Write([]byte(line + "\n"))
		}
		return h.runErr
	}}
	return h.runner, nil
}

type opWorkLayout struct {
	workdir  string
	worktree string
	inbox    string
	outbox   string
}

func setupOpWorkLayout(t *testing.T) opWorkLayout {
	t.Helper()
	workdir := t.TempDir()
	worktree := filepath.Join(workdir, "worktree")
	inbox := filepath.Join(workdir, "inbox")
	outbox := filepath.Join(workdir, "outbox")
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.MkdirAll(inbox, 0o755))
	require.NoError(t, os.MkdirAll(outbox, 0o755))
	return opWorkLayout{
		workdir:  workdir,
		worktree: worktree,
		inbox:    inbox,
		outbox:   outbox,
	}
}

func TestExecuteCompleted(t *testing.T) {
	layout := setupOpWorkLayout(t)
	cellRel := "cells/a"
	require.NoError(t, os.MkdirAll(filepath.Join(layout.worktree, cellRel), 0o755))

	harness := &runnerHarness{
		lines: []string{
			`{"type":"session.created","session_id":"sess-123"}`,
			`{"type":"item.completed","item":{"id":"item_1","item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"All done\",\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"pendingDependencies\":[],\"errorMessage\":\"\"}"}}`,
		},
	}

	opts := Options{
		Prompt:           "do the task",
		WorkDirRoot:      layout.workdir,
		WorktreeRoot:     layout.worktree,
		ArtifactInbox:    layout.inbox,
		ArtifactOutbox:   layout.outbox,
		CellRelativePath: cellRel,
		Clock:            fakeClock{ts: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)},
		RunnerFactory:    harness.factory,
	}

	res, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer os.RemoveAll(artifactDir)
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, res.Status)
	require.Equal(t, "All done", res.AssistantSummary)
	require.Equal(t, "sess-123", res.SessionID)
	require.Empty(t, res.ErrorMessage)
	require.Equal(t, 0, len(res.PendingDependencies))

	// Verify paths are returned
	require.NotEmpty(t, stdoutPath)
	require.NotEmpty(t, stderrPath)
	require.NotEmpty(t, artifactDir)
	require.FileExists(t, stdoutPath)
	require.FileExists(t, stderrPath)

	require.NotNil(t, harness.config)
	require.NotNil(t, harness.config.PostSetupExec)
	require.False(t, harness.config.PostSetupExec.UseTTY)

	// Verify command uses the output schema path and prompt.
	cmd := harness.config.PostSetupExec.Command
	idx := indexOf(cmd, "--output-schema")
	require.NotEqual(t, -1, idx)
	require.True(t, strings.HasPrefix(cmd[idx+1], "/tmp/codex-schema-"))
	require.Equal(t, "do the task", cmd[len(cmd)-1])

	require.NotNil(t, harness.config.PrependResourceSet)
	require.Len(t, harness.config.PrependResourceSet.RootCommands, 3)
	require.Contains(t, harness.config.PrependResourceSet.RootCommands[0], "/tmp/codex-schema-")
	require.Contains(t, harness.config.PrependResourceSet.RootCommands[1], "/src/.codex")
	require.Contains(t, harness.config.PrependResourceSet.RootCommands[1], "codex-sessions")
	require.Contains(t, harness.config.PrependResourceSet.RootCommands[2], "/src/.codex/.agents/skills")
	require.False(t, harness.config.ShowScriptOutput)
	require.Contains(t, harness.config.PostSetupExec.Env, "CODEX_APPROVAL_POLICY")
	require.Equal(t, "never", harness.config.PostSetupExec.Env["CODEX_APPROVAL_POLICY"])
	require.Contains(t, harness.config.PostSetupExec.Env, "CODEX_HOME")
	require.Equal(t, "/src/.codex", harness.config.PostSetupExec.Env["CODEX_HOME"])
	require.Equal(t, layout.workdir, harness.config.WorkingDir)
	require.Equal(t, []string{filepath.Join("worktree", cellRel), codexHomeDirName}, harness.config.ReadWritePaths)
	require.NotNil(t, harness.config.PrependResourceSet)

	mountByTarget := map[string]shai.Mount{}
	for _, m := range harness.config.PrependResourceSet.Mounts {
		mountByTarget[m.Target] = m
	}
	require.Equal(t, "ro", mountByTarget["/src/inbox"].Mode)
	require.Equal(t, "inbox", mountByTarget["/src/inbox"].Source)
	require.Equal(t, "rw", mountByTarget["/src/outbox"].Mode)
	require.Equal(t, "outbox", mountByTarget["/src/outbox"].Source)

	require.True(t, harness.runner.closed)
}

func TestExecuteIncompleteDependencies(t *testing.T) {
	layout := setupOpWorkLayout(t)
	require.NoError(t, os.MkdirAll(filepath.Join(layout.worktree, "cell"), 0o755))

	harness := &runnerHarness{
		lines: []string{
			`{"type":"session.created","session_id":"sess-456"}`,
			`{"type":"item.completed","item":{"id":"item_1","item_type":"assistant_message","text":"{\"status\":\"incomplete\",\"assistantSummary\":\"Needs follow-up\",\"incompleteReason\":\"update components\",\"incompleteCategory\":\"dependency_blockers\",\"pendingDependencies\":[{\"component\":\"server/git\",\"requestedChanges\":\"update refs\"}],\"errorMessage\":\"\" }"}}`,
		},
	}

	opts := Options{
		Prompt:           "analyze",
		WorkDirRoot:      layout.workdir,
		WorktreeRoot:     layout.worktree,
		ArtifactInbox:    layout.inbox,
		ArtifactOutbox:   layout.outbox,
		CellRelativePath: "cell",
		Clock:            fakeClock{ts: time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)},
		RunnerFactory:    harness.factory,
	}

	res, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer os.RemoveAll(artifactDir)
	require.NoError(t, err)
	require.Equal(t, StatusIncomplete, res.Status)
	require.Equal(t, "Needs follow-up", res.AssistantSummary)
	require.Equal(t, "update components", res.IncompleteReason)
	require.Equal(t, "dependency_blockers", res.IncompleteCategory)
	require.Len(t, res.PendingDependencies, 1)
	require.Equal(t, "server/git", res.PendingDependencies[0].Component)
	require.Equal(t, "update refs", res.PendingDependencies[0].RequestedChanges)

	// Verify paths are returned
	require.NotEmpty(t, stdoutPath)
	require.NotEmpty(t, stderrPath)
	require.NotEmpty(t, artifactDir)
}

func TestExecuteStructuredPayloadError(t *testing.T) {
	layout := setupOpWorkLayout(t)
	require.NoError(t, os.MkdirAll(filepath.Join(layout.worktree, "cell"), 0o755))

	harness := &runnerHarness{
		lines: []string{
			`{"type":"session.created","session_id":"sess-789"}`,
			`{"type":"item.completed","item":{"id":"item_1","item_type":"assistant_message","text":"{\"status\":\"completed\",\"pendingDependencies\":[],\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"errorMessage\":\"\"}"}}`,
		},
		stderr: []string{"warning: schema mismatch"},
	}

	opts := Options{
		Prompt:           "go",
		WorkDirRoot:      layout.workdir,
		WorktreeRoot:     layout.worktree,
		ArtifactInbox:    layout.inbox,
		ArtifactOutbox:   layout.outbox,
		CellRelativePath: "cell",
		Clock:            fakeClock{ts: time.Date(2024, 7, 8, 9, 10, 11, 0, time.UTC)},
		RunnerFactory:    harness.factory,
	}

	res, _, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer os.RemoveAll(artifactDir)
	require.NoError(t, err)
	require.Equal(t, StatusError, res.Status)
	require.Contains(t, res.ErrorMessage, "assistant payload missing assistantSummary")
	require.Equal(t, "sess-789", res.SessionID)

	// Verify stderr was written to file
	require.NotEmpty(t, stderrPath)
	stderrContent, readErr := os.ReadFile(stderrPath)
	require.NoError(t, readErr)
	require.Contains(t, string(stderrContent), "warning: schema mismatch")
}

func TestExecuteRunError(t *testing.T) {
	layout := setupOpWorkLayout(t)
	require.NoError(t, os.MkdirAll(filepath.Join(layout.worktree, "cell"), 0o755))

	harness := &runnerHarness{
		lines: []string{
			`{"type":"session.created","session_id":"sess"}`,
			`{"type":"item.completed","item":{"id":"item_1","item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"Done\",\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"pendingDependencies\":[],\"errorMessage\":\"\"}"}}`,
		},
		runErr: errors.New("codex exit 1"),
	}

	opts := Options{
		Prompt:           "task",
		WorkDirRoot:      layout.workdir,
		WorktreeRoot:     layout.worktree,
		ArtifactInbox:    layout.inbox,
		ArtifactOutbox:   layout.outbox,
		CellRelativePath: "cell",
		Clock:            fakeClock{ts: time.Date(2024, 9, 10, 11, 12, 13, 0, time.UTC)},
		RunnerFactory:    harness.factory,
	}

	res, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer os.RemoveAll(artifactDir)
	require.NoError(t, err)
	require.Equal(t, StatusError, res.Status)
	require.Contains(t, res.ErrorMessage, "codex exit 1")
	require.Equal(t, "sess", res.SessionID)

	// Verify paths are returned even on error
	require.NotEmpty(t, stdoutPath)
	require.NotEmpty(t, stderrPath)
	require.NotEmpty(t, artifactDir)
}

func indexOf(haystack []string, needle string) int {
	for i, v := range haystack {
		if v == needle {
			return i
		}
	}
	return -1
}

func TestInstallSkillSourcesIfExistsCopiesWorktreeAndConfiguredSkillsIntoAgentsSkills(t *testing.T) {
	workdir := t.TempDir()
	worktree := filepath.Join(workdir, "worktree")
	configuredSkills := filepath.Join(workdir, ".codex-skill-inputs", "skills")
	codexHome := filepath.Join(workdir, codexHomeDirName)
	worktreeSkills := filepath.Join(worktree, ".agents", "skills")
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.MkdirAll(configuredSkills, 0o755))
	require.NoError(t, os.MkdirAll(codexHome, 0o755))

	localShared := filepath.Join(worktreeSkills, "shared-skill", "SKILL.md")
	localOnly := filepath.Join(worktreeSkills, "local-only-skill", "SKILL.md")
	configuredShared := filepath.Join(configuredSkills, "shared-skill", "SKILL.md")
	configuredOnly := filepath.Join(configuredSkills, "configured-only-skill", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(localShared), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(localOnly), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(configuredShared), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(configuredOnly), 0o755))
	require.NoError(t, os.WriteFile(localShared, []byte("local-shared"), 0o644))
	require.NoError(t, os.WriteFile(localOnly, []byte("local-only"), 0o644))
	require.NoError(t, os.WriteFile(configuredShared, []byte("configured-shared"), 0o644))
	require.NoError(t, os.WriteFile(configuredOnly, []byte("configured-only"), 0o644))

	err := installSkillSourcesIfExists(Options{
		WorktreeRoot:        worktree,
		ConfiguredSkillDirs: []string{configuredSkills},
		CodexHome:           codexHome,
	})
	require.NoError(t, err)

	sharedPayload, err := os.ReadFile(filepath.Join(codexHome, ".agents", "skills", "shared-skill", "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, "configured-shared", string(sharedPayload))

	localOnlyPayload, err := os.ReadFile(filepath.Join(codexHome, ".agents", "skills", "local-only-skill", "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, "local-only", string(localOnlyPayload))

	configuredOnlyPayload, err := os.ReadFile(filepath.Join(codexHome, ".agents", "skills", "configured-only-skill", "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, "configured-only", string(configuredOnlyPayload))
}
