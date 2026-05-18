package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveExecuteWithConfiguredSkillCreatesMarker(t *testing.T) {
	requireCodexCLI(t)

	opts := liveExecuteOptions(t)

	skillsRoot := filepath.Join(opts.WorkDirRoot, "configured-skills")
	skillDir := filepath.Join(skillsRoot, "live-smoke")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create live skill: %v", err)
	}
	skillBody := `---
name: live-smoke
description: Use when asked to run live-smoke.
---

When this skill is used, create .c2/live-codex-skill-execution/result.json
with exactly {"ok":true} and then return a completed structured response.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatalf("write live skill: %v", err)
	}
	opts.ConfiguredSkillDirs = []string{skillsRoot}
	opts.Prompt = "Use live-smoke."

	result, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("Execute returned setup error: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %q: %s", result.Status, result.ErrorMessage)
	}

	marker := filepath.Join(opts.WorktreeRoot, ".c2", "live-codex-skill-execution", "result.json")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker %s: %v", marker, err)
	}
	if strings.TrimSpace(string(data)) != `{"ok":true}` {
		t.Fatalf("unexpected marker content %q", data)
	}
}

func TestLiveExecuteResumesSessionKnowledge(t *testing.T) {
	requireCodexCLI(t)

	name := "Joe-" + strings.ReplaceAll(t.Name(), "/", "-")

	first := liveExecuteOptions(t)
	first.Prompt = "Remember this exact fact for the next turn in this session: my name is " + name + ". Do not modify files. Return a completed structured response whose assistantSummary says you stored that exact name."

	firstResult, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), first)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("first Execute returned setup error: %v", err)
	}
	if firstResult.Status != StatusCompleted {
		t.Fatalf("expected first execution to complete, got %q: %s", firstResult.Status, firstResult.ErrorMessage)
	}
	if strings.TrimSpace(firstResult.SessionID) == "" {
		t.Fatalf("expected first execution to return a session id")
	}
	cleanupSessionPersistence(t, firstResult.SessionID)

	second := liveExecuteOptions(t)
	second.SessionID = firstResult.SessionID
	second.Prompt = "Using the existing conversation in this resumed session, answer this question: what is my name? Do not modify files. Return a completed structured response with assistantSummary containing only the exact name."

	secondResult, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), second)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("second Execute returned setup error: %v", err)
	}
	if secondResult.Status != StatusCompleted {
		t.Fatalf("expected second execution to complete, got %q: %s", secondResult.Status, secondResult.ErrorMessage)
	}
	if secondResult.SessionID != firstResult.SessionID {
		t.Fatalf("expected resumed session id %q, got %q", firstResult.SessionID, secondResult.SessionID)
	}
	if !strings.Contains(strings.ToLower(secondResult.AssistantSummary), strings.ToLower(name)) {
		t.Fatalf("expected resumed assistant summary to contain %q, got %q", name, secondResult.AssistantSummary)
	}
}

func requireCodexCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatalf("codex CLI not found: %v", err)
	}
}

func liveExecuteOptions(t *testing.T) Options {
	t.Helper()

	opts := testExecuteOptions(t)
	opts.IdleTimeout = 5 * time.Minute
	opts.Model = os.Getenv("C2OPS_CODEX_LIVE_MODEL")
	return opts
}
