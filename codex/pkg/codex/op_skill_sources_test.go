package codex

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRunMakesListedSkillsAvailableWithoutPromptEnforcement(t *testing.T) {
	installFakeCodex(t, "#!/usr/bin/env bash\nset -euo pipefail\n")
	t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
	originalMaterializer := materializeSkillRefsFn
	originalExecute := executeLibrary
	defer func() {
		materializeSkillRefsFn = originalMaterializer
		executeLibrary = originalExecute
	}()

	skillRef := "example.com/org/repo/skills@main"
	resolvedSkillRef := "example.com/org/repo/skills@abc123"
	skillBody := `---
name: available-skill
description: Available when useful.
---

Create .c2/available-skill/result.json when useful.
`

	materializeSkillRefsFn = func(ctx context.Context, skillRefs []string, stageRoot string, skillsRoot string) ([]string, error) {
		if !reflect.DeepEqual(skillRefs, []string{skillRef}) {
			t.Fatalf("expected skill refs %v, got %v", []string{skillRef}, skillRefs)
		}
		skillDir := filepath.Join(skillsRoot, "available-skill")
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("create materialized skill: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
			t.Fatalf("write materialized skill: %v", err)
		}
		return []string{resolvedSkillRef}, nil
	}

	prompt := "Use whatever skills are useful for this request."
	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		seedTestSession(t, opts.CodexHome, "session-with-available-skill")
		if opts.Prompt != prompt {
			t.Fatalf("expected prompt to remain %q, got %q", prompt, opts.Prompt)
		}
		if len(opts.ConfiguredSkillDirs) != 1 {
			t.Fatalf("expected one configured skill dir, got %v", opts.ConfiguredSkillDirs)
		}
		assertFileContent(t, filepath.Join(opts.ConfiguredSkillDirs[0], "available-skill", "SKILL.md"), skillBody)
		return Result{
			Status:           StatusCompleted,
			SessionID:        "session-with-available-skill",
			AssistantSummary: "done",
		}, "", "", "", nil
	}

	root := t.TempDir()
	workdir := filepath.Join(root, "workdir")
	worktree := filepath.Join(root, "worktree")
	inbox := filepath.Join(root, "inbox")
	outbox := filepath.Join(root, "outbox")

	output, err := Run(context.Background(), ExecOpInput{
		Prompt:             prompt,
		Skills:             []string{skillRef},
		WorkdirPath:        workdir,
		WorktreePath:       worktree,
		ArtifactInboxPath:  inbox,
		ArtifactOutboxPath: outbox,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !reflect.DeepEqual(output.SkillsInstalled, []string{resolvedSkillRef}) {
		t.Fatalf("expected installed skills %v, got %v", []string{resolvedSkillRef}, output.SkillsInstalled)
	}
}
