package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ensureCodexAvailable(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping codex CLI integration tests in short mode")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not available in PATH")
	}
}

func writeSchemaFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(path, StructuredOutputSchema, 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	return path
}

func runCodex(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	return output, err
}

func TestCodexCLIProducesStructuredOutput(t *testing.T) {
	ensureCodexAvailable(t)
	schema := writeSchemaFile(t)

	prompt := "Respond strictly with JSON matching the schema: status 'completed', assistantSummary 'All good', incompleteReason '', incompleteCategory '', errorMessage '', pendingDependencies []."
	output, err := runCodex(t,
		"exec",
		"--experimental-json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--output-schema", schema,
		prompt,
	)
	if err != nil {
		t.Fatalf("codex exec failed: %v\noutput:%s", err, string(output))
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		t.Fatalf("no output from codex")
	}

	var assistantMsg map[string]any
	for i := len(lines) - 1; i >= 0; i-- {
		var event map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &event); err != nil {
			continue
		}
		if item, ok := event["item"].(map[string]any); ok {
			itemType := asString(item["item_type"])
			if itemType == "" {
				itemType = asString(item["type"])
			}
			if itemType != "assistant_message" && itemType != "agent_message" {
				continue
			}
			text := asString(item["text"])
			if text == "" {
				continue
			}
			if err := json.Unmarshal([]byte(text), &assistantMsg); err != nil {
				t.Fatalf("assistant text is not valid JSON: %v (%s)", err, text)
			}
			break
		}
	}

	if assistantMsg == nil {
		t.Fatalf("assistant message not found in codex output: %s", string(output))
	}
	if status := assistantMsg["status"]; status != "completed" {
		t.Fatalf("unexpected status %v", status)
	}
	if summary := assistantMsg["assistantSummary"]; summary != "All good" {
		t.Fatalf("unexpected assistantSummary %v", summary)
	}
	if deps, ok := assistantMsg["pendingDependencies"].([]any); !ok || len(deps) != 0 {
		t.Fatalf("expected empty pendingDependencies, got %v", assistantMsg["pendingDependencies"])
	}
}

func TestCodexCLIRejectsInvalidSchema(t *testing.T) {
	ensureCodexAvailable(t)
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "invalid-schema.json")
	invalid := []byte(`{"type":"object","properties":{"status":{"type":"string"}}}`)
	if err := os.WriteFile(schemaPath, invalid, 0o644); err != nil {
		t.Fatalf("write invalid schema: %v", err)
	}

	output, err := runCodex(t,
		"exec",
		"--experimental-json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--output-schema", schemaPath,
		"Say hello",
	)
	if err == nil {
		// Some builds retry and eventually return success despite schema issues; rely on diagnostic output instead.
		if !strings.Contains(string(output), "invalid_json_schema") {
			t.Fatalf("expected invalid_json_schema diagnostic, got: %s", string(output))
		}
		return
	}
	if !strings.Contains(string(output), "invalid_json_schema") {
		t.Fatalf("expected invalid_json_schema diagnostic, got: %s", string(output))
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func TestCodexCLIResumeFlow(t *testing.T) {
	ensureCodexAvailable(t)
	schema := writeSchemaFile(t)

	// First run: request a greeting and capture session id
	output, err := runCodex(t,
		"exec",
		"--experimental-json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--output-schema", schema,
		"Respond strictly with JSON matching the schema: status 'completed', assistantSummary 'First turn', incompleteReason '', incompleteCategory '', errorMessage '', pendingDependencies [].",
	)
	if err != nil {
		t.Fatalf("initial codex exec failed: %v output:%s", err, string(output))
	}

	sessionID := extractSessionID(t, output)
	if sessionID == "" {
		t.Fatalf("expected session id in first run output: %s", string(output))
	}

	// Resume the session with new instructions.
	resumeOutput, err := runCodex(t,
		"exec",
		"--experimental-json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--output-schema", schema,
		"resume", sessionID,
		"Respond strictly with JSON matching the schema: status 'completed', assistantSummary 'Second turn', incompleteReason '', incompleteCategory '', errorMessage '', pendingDependencies [].",
	)
	if err != nil {
		t.Fatalf("resume codex exec failed: %v output:%s", err, string(resumeOutput))
	}

	assistantPayload := extractAssistantPayload(t, resumeOutput)
	if assistantPayload == nil {
		t.Fatalf("expected assistant payload in resume output: %s", string(resumeOutput))
	}
	if assistantPayload["assistantSummary"] != "Second turn" {
		t.Fatalf("expected resume assistantSummary 'Second turn', got %v", assistantPayload["assistantSummary"])
	}
}

func extractSessionID(t *testing.T, output []byte) string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	session := ""
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if sid := asString(event["session_id"]); sid != "" {
			session = sid
		} else if tid := asString(event["thread_id"]); tid != "" {
			session = tid
		}
		if item, ok := event["item"].(map[string]any); ok {
			itemType := asString(item["item_type"])
			if itemType == "" {
				itemType = asString(item["type"])
			}
			if itemType == "assistant_message" || itemType == "agent_message" {
				break
			}
		}
		if eventType := asString(event["type"]); eventType == "item.completed" {
			break
		}
	}
	return session
}

func extractAssistantPayload(t *testing.T, output []byte) map[string]any {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var event map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &event); err != nil {
			continue
		}
		if item, ok := event["item"].(map[string]any); ok {
			itemType := asString(item["item_type"])
			if itemType == "" {
				itemType = asString(item["type"])
			}
			if itemType != "assistant_message" && itemType != "agent_message" {
				continue
			}
			text := asString(item["text"])
			if text == "" {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(text), &payload); err == nil {
				return payload
			}
		}
	}
	return nil
}
