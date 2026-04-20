package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type capture struct {
	options Options
}

var _ ops.OpDependencies = (*fakeOpDependencies)(nil)

type fakeOpDependencies struct {
	inputArtifacts    []swf.Artifact
	outputArtifacts   []swf.Artifact
	externalArtifacts map[string]recipeartifacts.Ref
}

func (f *fakeOpDependencies) SetNextTaskType(taskType string) {
}

func (f *fakeOpDependencies) JobTool() ops.JobTool {
	return nil
}

func (f *fakeOpDependencies) FindArtifact(key swf.ArtifactKey) (swf.Artifact, error) {
	for _, artifact := range f.inputArtifacts {
		if artifact.Name() == key.Name {
			return artifact, nil
		}
	}
	return nil, errors.New("artifact not found")
}

func (f *fakeOpDependencies) AddOutputArtifact(artifact swf.Artifact) error {
	f.outputArtifacts = append(f.outputArtifacts, artifact)
	return nil
}

func (f *fakeOpDependencies) AddExternalArtifact(name string, url string, expand bool) error {
	artifactRef := recipeartifacts.NewExternalRef(name, url, expand)
	if err := artifactRef.Validate(); err != nil {
		return err
	}
	if f.externalArtifacts == nil {
		f.externalArtifacts = make(map[string]recipeartifacts.Ref)
	}
	f.externalArtifacts[name] = artifactRef
	return nil
}

func (f *fakeOpDependencies) GetInputArtifacts() []swf.Artifact {
	return f.inputArtifacts
}

func (f *fakeOpDependencies) GetOutputArtifacts() []swf.Artifact {
	return f.outputArtifacts
}

func (f *fakeOpDependencies) GetExternalArtifacts() map[string]recipeartifacts.Ref {
	if len(f.externalArtifacts) == 0 {
		return nil
	}
	out := make(map[string]recipeartifacts.Ref, len(f.externalArtifacts))
	for name, artifactRef := range f.externalArtifacts {
		out[name] = artifactRef
	}
	return out
}

func (f *fakeOpDependencies) Database() *gorm.DB {
	return nil
}

func (f *fakeOpDependencies) WorkflowControl() workflowctl.WorkflowControl {
	return nil
}

func (f *fakeOpDependencies) WorktreePath() string {
	return ""
}

func (f *fakeOpDependencies) GitContext() ops.GitExecutionContext {
	return ops.GitExecutionContext{}
}

func TestRunCodexActivitySuccess(t *testing.T) {
	workdir := t.TempDir()
	worktree := filepath.Join(workdir, "worktree")
	cellDir := filepath.Join(worktree, "cells", "alpha")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workdir, "inbox"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workdir, "outbox"), 0o755))

	// Create temp files for stdout and stderr
	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.jsonl")
	stderrPath := filepath.Join(tempDir, "stderr.txt")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("test output"), 0o644))
	require.NoError(t, os.WriteFile(stderrPath, []byte("test errors"), 0o644))

	var cap capture
	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		cap.options = opts
		return Result{
			Status:              StatusCompleted,
			SessionID:           "sess-123",
			AssistantSummary:    "all good",
			PendingDependencies: []Dependency{},
		}, stdoutPath, stderrPath, tempDir, nil
	}
	defer func() { executeLibrary = Execute }()

	inv := &fakeOpDependencies{}
	input := ExecOpInput{
		Prompt:             "do something",
		Env:                map[string]string{"FOO": "BAR"},
		WorkdirPath:        workdir,
		WorktreePath:       worktree,
		ArtifactInboxPath:  filepath.Join(workdir, "inbox"),
		ArtifactOutboxPath: filepath.Join(workdir, "outbox"),
		CellRelativePath:   filepath.Join("cells", "alpha"),
	}

	out, err := runCodexActivity(inv, context.Background(), input)
	require.NoError(t, err)

	require.Equal(t, StatusCompleted, Status(out.Status))
	require.Equal(t, "sess-123", out.SessionID)
	require.Equal(t, "all good", out.AssistantSummary)

	// Verify artifacts were created
	require.Len(t, inv.outputArtifacts, 2)
	require.Equal(t, "stdout.jsonl", inv.outputArtifacts[0].Name())
	require.Equal(t, "stderr.txt", inv.outputArtifacts[1].Name())

	require.Equal(t, "do something", cap.options.Prompt)
	require.Equal(t, workdir, cap.options.WorkDirRoot)
	require.Equal(t, worktree, cap.options.WorktreeRoot)
	require.Equal(t, filepath.Join(workdir, "inbox"), cap.options.ArtifactInbox)
	require.Equal(t, filepath.Join(workdir, "outbox"), cap.options.ArtifactOutbox)
	require.Equal(t, filepath.Join("cells", "alpha"), cap.options.CellRelativePath)
	require.Equal(t, "BAR", cap.options.ExtraEnv["FOO"])

	stdoutBytes, err := inv.outputArtifacts[0].Bytes(context.Background())
	require.NoError(t, err)
	require.Equal(t, "test output", string(stdoutBytes))

	stderrBytes, err := inv.outputArtifacts[1].Bytes(context.Background())
	require.NoError(t, err)
	require.Equal(t, "test errors", string(stderrBytes))
}

func TestRunCodexActivityIncludesSkillsInstalled(t *testing.T) {
	workdir := t.TempDir()
	worktree := filepath.Join(workdir, "worktree")
	cellDir := filepath.Join(worktree, "cells", "alpha")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workdir, "inbox"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workdir, "outbox"), 0o755))

	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.jsonl")
	stderrPath := filepath.Join(tempDir, "stderr.txt")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("test output"), 0o644))
	require.NoError(t, os.WriteFile(stderrPath, []byte(""), 0o644))

	var cap capture
	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		cap.options = opts
		return Result{
			Status:              StatusCompleted,
			SessionID:           "sess-123",
			AssistantSummary:    "all good",
			PendingDependencies: []Dependency{},
		}, stdoutPath, stderrPath, tempDir, nil
	}
	defer func() { executeLibrary = Execute }()

	originalMaterializer := materializeSkillRefsFn
	materializeSkillRefsFn = func(ctx context.Context, skillRefs []string, stageRoot string, skillsRoot string) ([]string, error) {
		require.Equal(t, []string{"github.com/acme/codex-platform-skills/.agents/skills@platform-v12"}, skillRefs)
		require.NoError(t, os.MkdirAll(filepath.Join(skillsRoot, "platform-skill"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(skillsRoot, "platform-skill", "SKILL.md"),
			[]byte("---\nname: platform-skill\ndescription: test\n---\n"),
			0o644,
		))
		return []string{"github.com/acme/codex-platform-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4"}, nil
	}
	defer func() { materializeSkillRefsFn = originalMaterializer }()

	inv := &fakeOpDependencies{}
	input := ExecOpInput{
		Prompt:             "do something",
		Skills:             []string{"github.com/acme/codex-platform-skills/.agents/skills@platform-v12"},
		WorkdirPath:        workdir,
		WorktreePath:       worktree,
		ArtifactInboxPath:  filepath.Join(workdir, "inbox"),
		ArtifactOutboxPath: filepath.Join(workdir, "outbox"),
		CellRelativePath:   filepath.Join("cells", "alpha"),
	}

	out, err := runCodexActivity(inv, context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, []string{"github.com/acme/codex-platform-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4"}, out.SkillsInstalled)
	require.Len(t, cap.options.ConfiguredSkillDirs, 1)
}

func TestGetOpAcceptsArtifacts(t *testing.T) {
	op := GetOp()
	require.True(t, op.GetMetadata().AcceptsArtifacts)
}

func TestRunCodexActivityArtifactsRemainUntilBothConsumed(t *testing.T) {
	worktree := t.TempDir()

	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.jsonl")
	stderrPath := filepath.Join(tempDir, "stderr.txt")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("test output"), 0o644))
	require.NoError(t, os.WriteFile(stderrPath, []byte("test errors"), 0o644))

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		return Result{
			Status:              StatusCompleted,
			SessionID:           "sess-123",
			AssistantSummary:    "all good",
			PendingDependencies: []Dependency{},
		}, stdoutPath, stderrPath, tempDir, nil
	}
	defer func() { executeLibrary = Execute }()

	inv := &fakeOpDependencies{}
	input := ExecOpInput{
		Prompt:           "do something",
		WorktreePath:     worktree,
		CellRelativePath: "cells/alpha",
	}

	_, err := runCodexActivity(inv, context.Background(), input)
	require.NoError(t, err)
	require.Len(t, inv.outputArtifacts, 2)

	var stdoutArt, stderrArt swf.Artifact
	for _, art := range inv.outputArtifacts {
		if art.Name() == "stdout.jsonl" {
			stdoutArt = art
		} else if art.Name() == "stderr.txt" {
			stderrArt = art
		}
	}
	require.NotNil(t, stdoutArt)
	require.NotNil(t, stderrArt)

	stdoutBytes, err := stdoutArt.Bytes(context.Background())
	require.NoError(t, err)
	require.Equal(t, "test output", string(stdoutBytes))
	require.NoError(t, stdoutArt.Cleanup())

	_, err = os.Stat(stdoutPath)
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(stderrPath)
	require.NoError(t, err)

	stderrBytes, err := stderrArt.Bytes(context.Background())
	require.NoError(t, err)
	require.Equal(t, "test errors", string(stderrBytes))
	require.NoError(t, stderrArt.Cleanup())

	_, err = os.Stat(stderrPath)
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))
}

func TestRunCodexActivityMissingPrompt(t *testing.T) {
	input := ExecOpInput{Prompt: ""}
	_, err := runCodexActivity(nil, context.Background(), input)
	require.Error(t, err)
}

func TestRunCodexActivityMissingWorktreePath(t *testing.T) {
	input := ExecOpInput{
		Prompt:           "ok",
		CellRelativePath: "cells/alpha",
	}
	_, err := runCodexActivity(nil, context.Background(), input)
	require.Error(t, err)
}

func TestRunCodexActivityMissingCellRelativePath(t *testing.T) {
	input := ExecOpInput{
		Prompt:       "ok",
		WorktreePath: "/tmp",
	}
	_, err := runCodexActivity(nil, context.Background(), input)
	require.Error(t, err)
}

func TestRunCodexActivityLibraryError(t *testing.T) {
	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		return Result{}, "", "", "", errors.New("boom")
	}
	defer func() { executeLibrary = Execute }()

	worktree := t.TempDir()
	input := ExecOpInput{
		Prompt:           "ok",
		WorktreePath:     worktree,
		CellRelativePath: "cells/alpha",
	}
	_, err := runCodexActivity(nil, context.Background(), input)
	require.Error(t, err)
}

func TestRunCodexActivityRegistersArtifactsOnTimeout(t *testing.T) {
	worktree := t.TempDir()
	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.jsonl")
	stderrPath := filepath.Join(tempDir, "stderr.txt")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("partial output before timeout"), 0o644))
	require.NoError(t, os.WriteFile(stderrPath, []byte("error logs before timeout"), 0o644))

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		// Simulate timeout: return error but with valid stdout/stderr paths
		return Result{Status: StatusError}, stdoutPath, stderrPath, tempDir, errors.New("context deadline exceeded")
	}
	defer func() { executeLibrary = Execute }()

	inv := &fakeOpDependencies{}
	input := ExecOpInput{
		Prompt:           "do something",
		WorktreePath:     worktree,
		CellRelativePath: "cells/alpha",
	}

	// Activity should return error but still register artifacts
	_, err := runCodexActivity(inv, context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "context deadline exceeded")

	// Verify artifacts were still registered despite the error
	require.Len(t, inv.outputArtifacts, 2)
	require.Equal(t, "stdout.jsonl", inv.outputArtifacts[0].Name())
	require.Equal(t, "stderr.txt", inv.outputArtifacts[1].Name())

	// Verify artifact contents are accessible
	stdoutBytes, err := inv.outputArtifacts[0].Bytes(context.Background())
	require.NoError(t, err)
	require.Equal(t, "partial output before timeout", string(stdoutBytes))

	stderrBytes, err := inv.outputArtifacts[1].Bytes(context.Background())
	require.NoError(t, err)
	require.Equal(t, "error logs before timeout", string(stderrBytes))
}

func TestRunCodexActivityReturnsErrorOnStatusError(t *testing.T) {
	worktree := t.TempDir()
	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.jsonl")
	stderrPath := filepath.Join(tempDir, "stderr.txt")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("{\"status\":\"error\"}\n"), 0o644))
	require.NoError(t, os.WriteFile(stderrPath, []byte(""), 0o644))

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		return Result{
			Status:       StatusError,
			ErrorMessage: "codex failed",
		}, stdoutPath, stderrPath, tempDir, nil
	}
	defer func() { executeLibrary = Execute }()

	input := ExecOpInput{
		Prompt:           "do something",
		WorktreePath:     worktree,
		CellRelativePath: "cells/alpha",
	}
	_, err := runCodexActivity(&fakeOpDependencies{}, context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "codex failed")
}

func TestRunCodexActivityAllowsIncomplete(t *testing.T) {
	worktree := t.TempDir()
	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.jsonl")
	stderrPath := filepath.Join(tempDir, "stderr.txt")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("{\"status\":\"incomplete\"}\n"), 0o644))
	require.NoError(t, os.WriteFile(stderrPath, []byte(""), 0o644))

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		return Result{
			Status:             StatusIncomplete,
			AssistantSummary:   "needs follow-up",
			IncompleteReason:   "blocked",
			IncompleteCategory: "dependency_blockers",
		}, stdoutPath, stderrPath, tempDir, nil
	}
	defer func() { executeLibrary = Execute }()

	input := ExecOpInput{
		Prompt:           "do something",
		WorktreePath:     worktree,
		CellRelativePath: "cells/alpha",
	}
	out, err := runCodexActivity(&fakeOpDependencies{}, context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, string(StatusIncomplete), out.Status)
	require.Equal(t, "needs follow-up", out.AssistantSummary)
}

func TestDigestPromptDeterministic(t *testing.T) {
	a := digestPrompt("hello")
	b := digestPrompt("hello")
	c := digestPrompt("world")
	require.Equal(t, a, b)
	require.NotEqual(t, a, c)
}
