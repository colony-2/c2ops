package codex

import (
	"path/filepath"
	"testing"
)

func TestCodexHomeSkillInstallPathMatchesCodexCLI(t *testing.T) {
	opts := Options{CodexHome: filepath.Join("tmp", "codex-home")}

	got := filepath.ToSlash(opts.codexHomeSkillsPath())
	want := "tmp/codex-home/skills"
	if got != want {
		t.Fatalf("expected Codex home skills path %q, got %q", want, got)
	}
}

func TestProjectSkillSourceKeepsLegacyAgentsPath(t *testing.T) {
	opts := Options{WorktreeRoot: filepath.Join("tmp", "worktree")}

	got := filepath.ToSlash(opts.worktreeAgentsSkillsPath())
	want := "tmp/worktree/.agents/skills"
	if got != want {
		t.Fatalf("expected project skills path %q, got %q", want, got)
	}
}
