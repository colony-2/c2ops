package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareSkillExecutionConfigRejectsUnsupportedSkillMode(t *testing.T) {
	_, err := prepareSkillExecutionConfig(ExecOpInput{
		Skill:     "skill-a",
		SkillMode: "prefer",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "skill_mode")
}

func TestPrepareSkillExecutionConfigAllowsUnknownSkillWhenEnforced(t *testing.T) {
	cfg, err := prepareSkillExecutionConfig(ExecOpInput{
		Skill:     "missing-skill",
		SkillMode: "enforce",
	})
	require.NoError(t, err)
	require.Equal(t, "missing-skill", cfg.SelectedSkill)
}

func TestRunCodexActivityBuildsOutcomeFromStatusArtifact(t *testing.T) {
	workdir := t.TempDir()
	worktree := filepath.Join(workdir, "worktree")
	inbox := filepath.Join(workdir, "inbox")
	outbox := filepath.Join(workdir, "outbox")
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, "cells", "alpha"), 0o755))
	require.NoError(t, os.MkdirAll(inbox, 0o755))
	require.NoError(t, os.MkdirAll(outbox, 0o755))

	statusPath := filepath.Join(outbox, "implementation", "latest-status.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(statusPath), 0o755))
	require.NoError(t, os.WriteFile(statusPath, []byte(`{
	  "summary": {"human": "Need user input", "reason": "awaiting response"},
	  "next_skill_candidates": ["follow-up-skill"],
	  "checkpoint": {
	    "status": "needs_user_input",
	    "scope": "nested",
	    "blocking_skill": "child-skill",
	    "stack": [
	      {"skill": "my-skill", "scope": "top_level"},
	      {"skill": "child-skill", "scope": "nested"}
	    ]
	  }
	}`), 0o644))

	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.jsonl")
	stderrPath := filepath.Join(tempDir, "stderr.txt")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("test output"), 0o644))
	require.NoError(t, os.WriteFile(stderrPath, []byte(""), 0o644))

	var captured Options
	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		captured = opts
		return Result{
			Status:              StatusCompleted,
			SessionID:           "sess-123",
			AssistantSummary:    "base summary",
			PendingDependencies: []Dependency{},
		}, stdoutPath, stderrPath, tempDir, nil
	}
	defer func() { executeLibrary = Execute }()

	input := ExecOpInput{
		Prompt:             "implement this",
		Skill:              "my-skill",
		SkillMode:          "enforce",
		WorkdirPath:        workdir,
		WorktreePath:       worktree,
		ArtifactInboxPath:  inbox,
		ArtifactOutboxPath: outbox,
		CellRelativePath:   filepath.Join("cells", "alpha"),
		StatusContract:     StatusContractRef{Path: "implementation/latest-status.json"},
	}

	out, err := runCodexActivity(&fakeOpDependencies{}, context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, string(StatusIncomplete), out.Status)
	require.Equal(t, "sess-123", out.SessionID)
	require.Equal(t, "Need user input", out.AssistantSummary)

	require.Equal(t, "Need user input", out.Outcome.Summary.Human)
	require.Equal(t, "awaiting response", out.Outcome.Summary.Reason)
	require.Equal(t, "my-skill", out.Outcome.Skill.Executed)
	require.Equal(t, skillSelectionModeAdaptive, out.Outcome.Skill.SelectionMode)
	require.Equal(t, "follow-up-skill", out.Outcome.Skill.NextCandidate)
	require.Equal(t, "needs_user_input", out.Outcome.Checkpoint.Status)
	require.Equal(t, checkpointScopeNested, out.Outcome.Checkpoint.Scope)
	require.Equal(t, "child-skill", out.Outcome.Checkpoint.BlockingSkill)
	require.True(t, out.Outcome.Checkpoint.ReturnTriggered)
	require.Equal(t, "needs_user_input", out.Outcome.Checkpoint.ReturnReason)
	require.Equal(t, routingReturnToCheckpoint, out.Outcome.Routing.NextAction)

	require.Contains(t, captured.Prompt, "execute exactly one top-level skill segment: my-skill")
	require.Contains(t, captured.Prompt, "nested skill checkpoints are checkpoints")
}

func TestRunCodexActivitySupportsConfiguredSkillRefs(t *testing.T) {
	workdir := t.TempDir()
	worktree := filepath.Join(workdir, "worktree")
	inbox := filepath.Join(workdir, "inbox")
	outbox := filepath.Join(workdir, "outbox")
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, "cells", "alpha"), 0o755))
	require.NoError(t, os.MkdirAll(inbox, 0o755))
	require.NoError(t, os.MkdirAll(outbox, 0o755))

	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.jsonl")
	stderrPath := filepath.Join(tempDir, "stderr.txt")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("test output"), 0o644))
	require.NoError(t, os.WriteFile(stderrPath, []byte(""), 0o644))

	var captured Options
	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		captured = opts
		require.Len(t, opts.ConfiguredSkillDirs, 1)

		artifactSkillPath := filepath.Join(opts.ConfiguredSkillDirs[0], "artifact-skill", "SKILL.md")
		blobSkillPath := filepath.Join(opts.ConfiguredSkillDirs[0], "blob-skill", "SKILL.md")
		require.FileExists(t, artifactSkillPath)
		require.FileExists(t, blobSkillPath)

		return Result{
			Status:              StatusCompleted,
			SessionID:           "sess-123",
			AssistantSummary:    "configured skills loaded",
			PendingDependencies: []Dependency{},
		}, stdoutPath, stderrPath, tempDir, nil
	}
	defer func() { executeLibrary = Execute }()

	originalMaterializer := materializeSkillRefsFn
	materializeSkillRefsFn = func(ctx context.Context, skillRefs []string, stageRoot string, skillsRoot string) ([]string, error) {
		require.Equal(t, []string{"github.com/acme/platform-skills/.agents/skills@v12"}, skillRefs)
		require.NoError(t, os.MkdirAll(filepath.Join(skillsRoot, "artifact-skill"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(skillsRoot, "artifact-skill", "SKILL.md"),
			[]byte("---\nname: artifact-skill\ndescription: from skills ref\n---\n"),
			0o644,
		))
		require.NoError(t, os.MkdirAll(filepath.Join(skillsRoot, "blob-skill"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(skillsRoot, "blob-skill", "SKILL.md"),
			[]byte("---\nname: blob-skill\ndescription: from skills ref\n---\n"),
			0o644,
		))
		return []string{"github.com/acme/platform-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4"}, nil
	}
	defer func() { materializeSkillRefsFn = originalMaterializer }()

	inv := &fakeOpDependencies{}
	input := ExecOpInput{
		Prompt:             "implement this",
		Skill:              "artifact-skill",
		SkillMode:          "enforce",
		Skills:             []string{"github.com/acme/platform-skills/.agents/skills@v12"},
		WorkdirPath:        workdir,
		WorktreePath:       worktree,
		ArtifactInboxPath:  inbox,
		ArtifactOutboxPath: outbox,
		CellRelativePath:   filepath.Join("cells", "alpha"),
	}

	out, err := runCodexActivity(inv, context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, string(StatusCompleted), out.Status)
	require.Equal(t, "configured skills loaded", out.AssistantSummary)
	require.Equal(t, []string{"github.com/acme/platform-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4"}, out.SkillsInstalled)
	require.Contains(t, captured.Prompt, "execute exactly one top-level skill segment: artifact-skill")
}

func TestRunCodexActivityMissingStatusContractProducesBlockedCheckpoint(t *testing.T) {
	workdir := t.TempDir()
	worktree := filepath.Join(workdir, "worktree")
	inbox := filepath.Join(workdir, "inbox")
	outbox := filepath.Join(workdir, "outbox")
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, "cells", "alpha"), 0o755))
	require.NoError(t, os.MkdirAll(inbox, 0o755))
	require.NoError(t, os.MkdirAll(outbox, 0o755))

	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.jsonl")
	stderrPath := filepath.Join(tempDir, "stderr.txt")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("test output"), 0o644))
	require.NoError(t, os.WriteFile(stderrPath, []byte(""), 0o644))

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		return Result{
			Status:              StatusCompleted,
			SessionID:           "sess-123",
			AssistantSummary:    "base summary",
			PendingDependencies: []Dependency{},
		}, stdoutPath, stderrPath, tempDir, nil
	}
	defer func() { executeLibrary = Execute }()

	input := ExecOpInput{
		Prompt:             "implement this",
		Skill:              "my-skill",
		SkillMode:          "enforce",
		WorkdirPath:        workdir,
		WorktreePath:       worktree,
		ArtifactInboxPath:  inbox,
		ArtifactOutboxPath: outbox,
		CellRelativePath:   filepath.Join("cells", "alpha"),
		StatusContract:     StatusContractRef{Path: "implementation/missing.json"},
	}

	out, err := runCodexActivity(&fakeOpDependencies{}, context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, string(StatusIncomplete), out.Status)
	require.Equal(t, checkpointStatusBlocked, out.Outcome.Checkpoint.Status)
	require.True(t, out.Outcome.Checkpoint.ReturnTriggered)
	require.Equal(t, "contract_error", out.Outcome.Checkpoint.ReturnReason)
	require.Equal(t, routingReturnToCheckpoint, out.Outcome.Routing.NextAction)
	require.NotEmpty(t, out.Outcome.Checkpoint.ContractErrors)
}

func TestRunCodexActivityMultiSkillSequenceWithNestedCheckpoint(t *testing.T) {
	workdir := t.TempDir()
	worktree := filepath.Join(workdir, "worktree")
	inbox := filepath.Join(workdir, "inbox")
	outbox := filepath.Join(workdir, "outbox")
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, "cells", "alpha"), 0o755))
	require.NoError(t, os.MkdirAll(inbox, 0o755))
	require.NoError(t, os.MkdirAll(outbox, 0o755))

	type scriptedStep struct {
		skill            string
		statusPath       string
		statusPayload    string
		expectedStatus   string
		expectedScope    string
		expectedBlocking string
		expectedStackLen int
	}
	steps := []scriptedStep{
		{
			skill:      "example-plan-step",
			statusPath: "implementation/step-1-status.json",
			statusPayload: `{
  "summary": {"human": "plan checkpoint", "reason": "step complete"},
  "next_skill_candidates": ["example-parent-step"],
  "checkpoint": {
    "status": "checkpoint_ready",
    "scope": "top_level",
    "stack": [{"skill":"example-plan-step","scope":"top_level"}]
  }
}`,
			expectedStatus:   "checkpoint_ready",
			expectedScope:    checkpointScopeTopLevel,
			expectedBlocking: "",
			expectedStackLen: 1,
		},
		{
			skill:      "example-parent-step",
			statusPath: "implementation/step-2-status.json",
			statusPayload: `{
  "summary": {"human": "waiting on user", "reason": "nested skill blocked"},
  "next_skill_candidates": ["example-validate-step"],
  "checkpoint": {
    "status": "needs_user_input",
    "scope": "nested",
    "blocking_skill": "example-child-step",
    "stack": [
      {"skill":"example-parent-step","scope":"top_level"},
      {"skill":"example-child-step","scope":"nested"}
    ]
  }
}`,
			expectedStatus:   "needs_user_input",
			expectedScope:    checkpointScopeNested,
			expectedBlocking: "example-child-step",
			expectedStackLen: 2,
		},
		{
			skill:      "example-validate-step",
			statusPath: "implementation/step-3-status.json",
			statusPayload: `{
  "summary": {"human": "ready for validation", "reason": "handoff"},
  "checkpoint": {
    "status": "ready_for_validation",
    "scope": "top_level",
    "stack": [{"skill":"example-validate-step","scope":"top_level"}]
  }
}`,
			expectedStatus:   "ready_for_validation",
			expectedScope:    checkpointScopeTopLevel,
			expectedBlocking: "",
			expectedStackLen: 1,
		},
	}

	tempDir := t.TempDir()
	callIndex := 0
	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		require.Less(t, callIndex, len(steps))
		step := steps[callIndex]
		expectedSession := ""
		if callIndex > 0 {
			expectedSession = fmt.Sprintf("sess-%d", callIndex)
		}
		require.Equal(t, expectedSession, opts.SessionID)
		require.Contains(t, opts.Prompt, fmt.Sprintf("execute exactly one top-level skill segment: %s.", step.skill))

		statusAbsPath := filepath.Join(opts.ArtifactOutbox, step.statusPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(statusAbsPath), 0o755))
		require.NoError(t, os.WriteFile(statusAbsPath, []byte(step.statusPayload), 0o644))

		stdoutPath := filepath.Join(tempDir, fmt.Sprintf("stdout-%d.jsonl", callIndex+1))
		stderrPath := filepath.Join(tempDir, fmt.Sprintf("stderr-%d.txt", callIndex+1))
		require.NoError(t, os.WriteFile(stdoutPath, []byte("test output"), 0o644))
		require.NoError(t, os.WriteFile(stderrPath, []byte(""), 0o644))

		callIndex++
		return Result{
			Status:              StatusCompleted,
			SessionID:           fmt.Sprintf("sess-%d", callIndex),
			AssistantSummary:    "base summary",
			PendingDependencies: []Dependency{},
		}, stdoutPath, stderrPath, tempDir, nil
	}
	defer func() { executeLibrary = Execute }()

	sessionID := ""
	var executedSkills []string
	for _, step := range steps {
		out, err := runCodexActivity(&fakeOpDependencies{}, context.Background(), ExecOpInput{
			Prompt:             "continue",
			SessionID:          sessionID,
			Skill:              step.skill,
			SkillMode:          skillModeEnforce,
			StatusContract:     StatusContractRef{Path: step.statusPath},
			WorkdirPath:        workdir,
			WorktreePath:       worktree,
			ArtifactInboxPath:  inbox,
			ArtifactOutboxPath: outbox,
			CellRelativePath:   filepath.Join("cells", "alpha"),
		})
		require.NoError(t, err)
		require.Equal(t, string(StatusIncomplete), out.Status)
		require.Equal(t, step.skill, out.Outcome.Skill.Executed)
		require.Equal(t, step.expectedStatus, out.Outcome.Checkpoint.Status)
		require.Equal(t, step.expectedScope, out.Outcome.Checkpoint.Scope)
		require.Equal(t, step.expectedBlocking, out.Outcome.Checkpoint.BlockingSkill)
		require.Len(t, out.Outcome.Checkpoint.Stack, step.expectedStackLen)
		require.True(t, out.Outcome.Checkpoint.ReturnTriggered)
		require.Equal(t, step.expectedStatus, out.Outcome.Checkpoint.ReturnReason)
		require.Equal(t, routingReturnToCheckpoint, out.Outcome.Routing.NextAction)

		executedSkills = append(executedSkills, out.Outcome.Skill.Executed)
		sessionID = out.SessionID
	}

	require.Equal(t, []string{
		"example-plan-step",
		"example-parent-step",
		"example-validate-step",
	}, executedSkills)
	require.Equal(t, len(steps), callIndex)
	require.Equal(t, "sess-3", sessionID)
}
