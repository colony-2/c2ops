package codex

import (
	"path/filepath"
	"strings"
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

func TestModelEffortNormalization(t *testing.T) {
	for _, value := range []string{"", "  ", " high "} {
		opts := testExecuteOptions(t)
		opts.Model = value
		opts.ReasoningEffort = value
		if err := opts.validate(); err != nil {
			t.Fatal(err)
		}
		if opts.Model != strings.TrimSpace(value) || opts.ReasoningEffort != strings.TrimSpace(value) {
			t.Fatalf("untrimmed values: %q %q", opts.Model, opts.ReasoningEffort)
		}
	}
}
