package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunSkillGeneratesPromptAndValidatesOutputArtifact(t *testing.T) {
	originalExecute := executeLibrary
	defer func() {
		executeLibrary = originalExecute
	}()

	root := testSkillRunRoot(t)
	createLocalSkill(t, root.worktree, "ticket-intake")

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		seedTestSession(t, opts.CodexHome, "session-1")
		if !strings.Contains(opts.Prompt, "Run the Codex skill `ticket-intake`") {
			t.Fatalf("prompt did not request skill:\n%s", opts.Prompt)
		}
		if !strings.Contains(opts.Prompt, "codex-run-skill/contract.json") {
			t.Fatalf("prompt did not include contract path:\n%s", opts.Prompt)
		}
		if !strings.Contains(opts.Prompt, "ticket/result.json") {
			t.Fatalf("prompt did not include output path:\n%s", opts.Prompt)
		}
		writeTestJSON(t, filepath.Join(opts.ArtifactOutbox, "ticket", "result.json"), map[string]any{
			"summary": "parsed result",
		})
		writeTestJSON(t, filepath.Join(opts.ArtifactOutbox, "ticket", "latest-status.json"), map[string]any{
			"status": "completed",
			"summary": map[string]any{
				"human":  "status summary",
				"reason": "done",
			},
			"checkpoint": map[string]any{
				"status": "completed",
				"scope":  "top_level",
			},
		})
		return Result{
			Status:           StatusCompleted,
			SessionID:        "session-1",
			AssistantSummary: "assistant summary",
		}, "", "", "", nil
	}

	output, err := RunSkill(context.Background(), SkillRunInput{
		Skill: "ticket-intake",
		Input: map[string]any{
			"ticket_prompt": "triage this",
		},
		StatusContract: StatusContractRef{Path: "ticket/latest-status.json"},
		Output: SkillRunOutputSpec{
			Path: "ticket/result.json",
			Schema: json.RawMessage(`{
				"type": "object",
				"required": ["summary"],
				"properties": {"summary": {"type": "string"}}
			}`),
		},
		WorkdirPath:        root.workdir,
		WorktreePath:       root.worktree,
		ArtifactInboxPath:  root.inbox,
		ArtifactOutboxPath: root.outbox,
	})
	if err != nil {
		t.Fatalf("RunSkill returned error: %v", err)
	}
	if output.Status != string(StatusCompleted) {
		t.Fatalf("expected completed status, got %q", output.Status)
	}
	if !output.OutputSchemaValid {
		t.Fatalf("expected valid output schema, got errors %v", output.OutputSchemaErrors)
	}
	parsed, ok := output.ParsedOutput.(map[string]interface{})
	if !ok || parsed["summary"] != "parsed result" {
		t.Fatalf("unexpected parsed output: %#v", output.ParsedOutput)
	}
	if !output.StatusContractPresent || !output.StatusContractValid {
		t.Fatalf("expected valid status contract, present=%v valid=%v errors=%v", output.StatusContractPresent, output.StatusContractValid, output.StatusContractErrors)
	}
	assertFileContent(t, filepath.Join(root.outbox, skillRunDiagnosticsDir, "contract.json"), `{
  "skill": "ticket-intake",
  "artifact_inbox_path": "`+root.inbox+`",
  "artifact_outbox_path": "`+root.outbox+`",
  "status_contract": {
    "path": "ticket/latest-status.json"
  },
  "output": {
    "from": "artifact",
    "path": "ticket/result.json",
    "format": "json",
    "schema": {
      "properties": {
        "summary": {
          "type": "string"
        }
      },
      "required": [
        "summary"
      ],
      "type": "object"
    }
  },
  "input": {
    "ticket_prompt": "triage this"
  }
}
`)
}

func TestRunSkillRequiresRequestedSkillBeforeExecutingCodex(t *testing.T) {
	originalExecute := executeLibrary
	defer func() {
		executeLibrary = originalExecute
	}()

	root := testSkillRunRoot(t)
	called := false
	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		seedTestSession(t, opts.CodexHome, "session-1")
		called = true
		return Result{}, "", "", "", nil
	}

	_, err := RunSkill(context.Background(), SkillRunInput{
		Skill:              "missing-skill",
		WorkdirPath:        root.workdir,
		WorktreePath:       root.worktree,
		ArtifactInboxPath:  root.inbox,
		ArtifactOutboxPath: root.outbox,
	})
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("expected missing skill error, got %v", err)
	}
	if called {
		t.Fatalf("executeLibrary was called for a missing skill")
	}
}

func TestRunSkillOutputSchemaFailureReturnsIncomplete(t *testing.T) {
	originalExecute := executeLibrary
	defer func() {
		executeLibrary = originalExecute
	}()

	root := testSkillRunRoot(t)
	createLocalSkill(t, root.worktree, "ticket-intake")
	repairEnabled := false

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		seedTestSession(t, opts.CodexHome, "session-1")
		writeTestJSON(t, filepath.Join(opts.ArtifactOutbox, "ticket", "result.json"), map[string]any{
			"unexpected": true,
		})
		return Result{
			Status:           StatusCompleted,
			SessionID:        "session-1",
			AssistantSummary: "assistant summary",
		}, "", "", "", nil
	}

	output, err := RunSkill(context.Background(), SkillRunInput{
		Skill: "ticket-intake",
		Output: SkillRunOutputSpec{
			Path:   "ticket/result.json",
			Schema: json.RawMessage(`{"type":"object","required":["summary"]}`),
			Validation: SkillRunOutputValidationConfig{
				Repair: SkillRunOutputRepairConfig{Enabled: &repairEnabled},
			},
		},
		WorkdirPath:        root.workdir,
		WorktreePath:       root.worktree,
		ArtifactInboxPath:  root.inbox,
		ArtifactOutboxPath: root.outbox,
	})
	if err != nil {
		t.Fatalf("RunSkill returned error: %v", err)
	}
	if output.Status != string(StatusIncomplete) {
		t.Fatalf("expected incomplete status, got %q", output.Status)
	}
	if output.IncompleteCategory != "output_schema_validation" {
		t.Fatalf("expected output schema category, got %q", output.IncompleteCategory)
	}
	if output.OutputSchemaValid {
		t.Fatalf("expected invalid output schema")
	}
	if len(output.OutputSchemaErrors) == 0 {
		t.Fatalf("expected output schema errors")
	}
}

func TestRunSkillRepairsInvalidOutputWithSameSession(t *testing.T) {
	originalExecute := executeLibrary
	defer func() {
		executeLibrary = originalExecute
	}()

	root := testSkillRunRoot(t)
	createLocalSkill(t, root.worktree, "ticket-intake")
	calls := 0
	var executionHome string

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		seedTestSession(t, opts.CodexHome, "session-1")
		calls++
		switch calls {
		case 1:
			executionHome = opts.CodexHome
			writeTestJSON(t, filepath.Join(opts.CodexHome, "sessions", "repair-progress.json"), "first turn")
			writeTestJSON(t, filepath.Join(opts.ArtifactOutbox, "ticket", "result.json"), map[string]any{
				"unexpected": true,
			})
			return Result{
				Status:           StatusCompleted,
				SessionID:        "session-1",
				AssistantSummary: "first pass",
			}, "", "", "", nil
		case 2:
			if opts.CodexHome != executionHome {
				t.Fatal("repair changed execution home")
			}
			assertFileContent(t, filepath.Join(opts.CodexHome, "sessions", "repair-progress.json"), `"first turn"`)
			if opts.SessionID != "session-1" {
				t.Fatalf("expected repair to resume session-1, got %q", opts.SessionID)
			}
			if !strings.Contains(opts.Prompt, "repair only the declared primary output artifact") {
				t.Fatalf("expected repair prompt, got:\n%s", opts.Prompt)
			}
			writeTestJSON(t, filepath.Join(opts.ArtifactOutbox, "ticket", "result.json"), map[string]any{
				"summary": "repaired",
			})
			return Result{
				Status:           StatusCompleted,
				SessionID:        "session-1",
				AssistantSummary: "repaired",
			}, "", "", "", nil
		default:
			t.Fatalf("unexpected execute call %d", calls)
			return Result{}, "", "", "", nil
		}
	}

	output, err := RunSkill(context.Background(), SkillRunInput{
		Skill: "ticket-intake",
		Output: SkillRunOutputSpec{
			Path:   "ticket/result.json",
			Schema: json.RawMessage(`{"type":"object","required":["summary"]}`),
		},
		WorkdirPath:        root.workdir,
		WorktreePath:       root.worktree,
		ArtifactInboxPath:  root.inbox,
		ArtifactOutboxPath: root.outbox,
	})
	if err != nil {
		t.Fatalf("RunSkill returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected two execute calls, got %d", calls)
	}
	if !output.OutputSchemaValid {
		t.Fatalf("expected repaired output to be valid, got errors %v", output.OutputSchemaErrors)
	}
	if output.Session == nil || len(output.Objects) != 1 {
		t.Fatal("expected exactly one final session draft")
	}
	if output.OutputRepairAttempts != 1 {
		t.Fatalf("expected one repair attempt, got %d", output.OutputRepairAttempts)
	}
}

type skillRunRoot struct {
	workdir  string
	worktree string
	inbox    string
	outbox   string
}

func testSkillRunRoot(t *testing.T) skillRunRoot {
	t.Helper()
	installFakeCodex(t, "#!/usr/bin/env bash\nset -euo pipefail\n")
	t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())

	root := t.TempDir()
	paths := skillRunRoot{
		workdir:  filepath.Join(root, "workdir"),
		worktree: filepath.Join(root, "workdir", "worktree"),
		inbox:    filepath.Join(root, "workdir", "inbox"),
		outbox:   filepath.Join(root, "workdir", "outbox"),
	}
	for _, dir := range []string{paths.worktree, paths.inbox, paths.outbox} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	return paths
}

func createLocalSkill(t *testing.T, worktree string, name string) {
	t.Helper()

	skillDir := filepath.Join(worktree, ".agents", "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: Test skill.\n---\n\nRun the test skill.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func writeTestJSON(t *testing.T, path string, value interface{}) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRunSkillOnErrorFailReturnsPartialOutputAndError(t *testing.T) {
	originalExecute := executeLibrary
	defer func() {
		executeLibrary = originalExecute
	}()

	root := testSkillRunRoot(t)
	createLocalSkill(t, root.worktree, "ticket-intake")
	repairEnabled := false

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		seedTestSession(t, opts.CodexHome, "session-1")
		writeTestJSON(t, filepath.Join(opts.ArtifactOutbox, "ticket", "result.json"), map[string]any{
			"unexpected": true,
		})
		return Result{
			Status:           StatusCompleted,
			SessionID:        "session-1",
			AssistantSummary: "assistant summary",
		}, "", "", "", nil
	}

	output, err := RunSkill(context.Background(), SkillRunInput{
		Skill: "ticket-intake",
		Output: SkillRunOutputSpec{
			Path:   "ticket/result.json",
			Schema: json.RawMessage(`{"type":"object","required":["summary"]}`),
			Validation: SkillRunOutputValidationConfig{
				OnError: outputValidationFail,
				Repair:  SkillRunOutputRepairConfig{Enabled: &repairEnabled},
			},
		},
		WorkdirPath:        root.workdir,
		WorktreePath:       root.worktree,
		ArtifactInboxPath:  root.inbox,
		ArtifactOutboxPath: root.outbox,
	})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if output.Status != string(StatusError) {
		t.Fatalf("expected error status, got %q", output.Status)
	}
	if output.Session != nil || len(output.Objects) != 0 {
		t.Fatal("failed validation published a session")
	}
	if output.SessionID != "session-1" {
		t.Fatalf("expected partial output session id, got %q", output.SessionID)
	}
}

func TestRunSkillAcceptsAssistantSummaryOutputCompatibility(t *testing.T) {
	originalExecute := executeLibrary
	defer func() {
		executeLibrary = originalExecute
	}()

	root := testSkillRunRoot(t)
	createLocalSkill(t, root.worktree, "ticket-intake")

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		seedTestSession(t, opts.CodexHome, "session-1")
		return Result{
			Status:           StatusCompleted,
			SessionID:        "session-1",
			AssistantSummary: `{"summary":"from summary"}`,
		}, "", "", "", nil
	}

	output, err := RunSkill(context.Background(), SkillRunInput{
		Skill: "ticket-intake",
		Output: SkillRunOutputSpec{
			From:   outputSourceSummary,
			Schema: json.RawMessage(`{"type":"object","required":["summary"]}`),
		},
		WorkdirPath:        root.workdir,
		WorktreePath:       root.worktree,
		ArtifactInboxPath:  root.inbox,
		ArtifactOutboxPath: root.outbox,
	})
	if err != nil {
		t.Fatalf("RunSkill returned error: %v", err)
	}
	if !output.OutputSchemaValid {
		t.Fatalf("expected assistantSummary output to validate, errors=%v", output.OutputSchemaErrors)
	}
	if output.OutputSource != outputSourceSummary {
		t.Fatalf("expected assistantSummary source, got %q", output.OutputSource)
	}
}

func TestRunSkillIdleTimeoutValidation(t *testing.T) {
	_, err := parseDurationInput("idle_timeout", "not-a-duration", 5*time.Minute)
	if err == nil {
		t.Fatalf("expected parse duration error")
	}
}
