package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecuteWithConfiguredSkillCreatesMarker(t *testing.T) {
	installFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
[[ "$*" == *"Use configured-smoke."* ]]
test -f "$CODEX_HOME/skills/configured-smoke/SKILL.md"
mkdir -p .c2/codex-skill-execution
printf '{"ok":true}' > .c2/codex-skill-execution/result.json
printf '%s\n' '{"type":"session.created","session_id":"configured-skill-session"}'
printf '%s\n' '{"type":"item.completed","item":{"item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"skill executed\",\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"pendingDependencies\":[],\"errorMessage\":\"\"}"}}'
`)

	opts := testExecuteOptions(t)
	opts.IdleTimeout = time.Second

	skillsRoot := filepath.Join(opts.WorkDirRoot, "configured-skills")
	skillDir := filepath.Join(skillsRoot, "configured-smoke")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create configured skill: %v", err)
	}
	skillBody := `---
name: configured-smoke
description: Use when asked to run configured-smoke.
---

When this skill is used, create .c2/codex-skill-execution/result.json
with exactly {"ok":true} and then return a completed structured response.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatalf("write configured skill: %v", err)
	}
	opts.ConfiguredSkillDirs = []string{skillsRoot}
	opts.Prompt = "Use configured-smoke."

	result, stdoutPath, stderrPath, artifactDir, err := Execute(context.Background(), opts)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)

	if err != nil {
		t.Fatalf("Execute returned setup error: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %q: %s", result.Status, result.ErrorMessage)
	}

	marker := filepath.Join(opts.WorktreeRoot, ".c2", "codex-skill-execution", "result.json")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker %s: %v", marker, err)
	}
	if strings.TrimSpace(string(data)) != `{"ok":true}` {
		t.Fatalf("unexpected marker content %q", data)
	}
}

func TestExecuteResumesSessionKnowledge(t *testing.T) {
	sessionID := uniqueSessionID(t)
	cleanupSessionPersistence(t, sessionID)
	installFakeCodex(t, `#!/usr/bin/env bash
set -euo pipefail
session_id=""
previous_arg=""
for arg in "$@"; do
  if [[ "${previous_arg}" == "resume" ]]; then
    session_id="${arg}"
    break
  fi
  previous_arg="${arg}"
done

if [[ -n "${session_id}" ]]; then
  test "${session_id}" = "`+sessionID+`"
  name="$(cat "$CODEX_HOME/session-name.txt")"
  printf '%s\n' '{"type":"session.created","session_id":"`+sessionID+`"}'
  printf '%s\n' "{\"type\":\"item.completed\",\"item\":{\"item_type\":\"assistant_message\",\"text\":\"{\\\"status\\\":\\\"completed\\\",\\\"assistantSummary\\\":\\\"${name}\\\",\\\"incompleteReason\\\":\\\"\\\",\\\"incompleteCategory\\\":\\\"\\\",\\\"pendingDependencies\\\":[],\\\"errorMessage\\\":\\\"\\\"}\"}}"
  exit 0
fi

prompt="${@: -1}"
name="${prompt##*my name is }"
name="${name%%.*}"
printf '%s' "${name}" > "$CODEX_HOME/session-name.txt"
printf '%s\n' '{"type":"session.created","session_id":"`+sessionID+`"}'
printf '%s\n' '{"type":"item.completed","item":{"item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"stored\",\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"pendingDependencies\":[],\"errorMessage\":\"\"}"}}'
`)

	name := "Joe-" + strings.ReplaceAll(t.Name(), "/", "-")

	first := testExecuteOptions(t)
	first.IdleTimeout = time.Second
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

	second := testExecuteOptions(t)
	second.IdleTimeout = time.Second
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
