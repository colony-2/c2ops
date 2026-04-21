package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codexpkg "github.com/colony-2/c2ops/pkg/codex"
	"github.com/stretchr/testify/require"
)

type codexCommandEnvelope struct {
	Output codexpkg.ExecOpOutput `json:"output"`
}

type codexCommandLayout struct {
	workdir  string
	worktree string
	inbox    string
	outbox   string
	cellRel  string
}

func TestCodexExecOpResumeFlow(t *testing.T) {
	ensureCodexAvailable(t)

	layout := newCodexCommandLayout(t)

	baseInput := codexpkg.ExecOpInput{
		WorkdirPath:        layout.workdir,
		WorktreePath:       layout.worktree,
		ArtifactInboxPath:  layout.inbox,
		ArtifactOutboxPath: layout.outbox,
		CellRelativePath:   layout.cellRel,
		Env: map[string]string{
			"RUST_LOG": "codex_core::rollout::list=off",
		},
	}

	firstInput := baseInput
	firstInput.Prompt = "Respond strictly with JSON matching the schema: status 'completed', assistantSummary 'First turn', incompleteReason '', incompleteCategory '', errorMessage '', pendingDependencies []."

	first, stdout, stderr, err := runCodexExecOp(t, firstInput)
	require.NoError(t, err, "stdout=%s\nstderr=%s", stdout, stderr)
	require.Equal(t, string(codexpkg.StatusCompleted), first.Output.Status)
	require.Equal(t, "First turn", first.Output.AssistantSummary)
	require.NotEmpty(t, first.Output.SessionID)
	assertCodexArtifacts(t, layout.outbox)

	promoteCodexState(t, layout)

	secondInput := baseInput
	secondInput.SessionID = first.Output.SessionID
	secondInput.Prompt = "Resume the previous session. Respond strictly with JSON matching the schema: status 'completed', assistantSummary 'Second turn', incompleteReason '', incompleteCategory '', errorMessage '', pendingDependencies []."

	second, stdout, stderr, err := runCodexExecOp(t, secondInput)
	require.NoError(t, err, "stdout=%s\nstderr=%s", stdout, stderr)
	require.Equal(t, string(codexpkg.StatusCompleted), second.Output.Status)
	require.Equal(t, "Second turn", second.Output.AssistantSummary)
	require.NotEmpty(t, second.Output.SessionID)
	assertCodexArtifacts(t, layout.outbox)
}

func TestCodexExecOpMultiTurnSkillLoop(t *testing.T) {
	ensureCodexAvailable(t)

	layout := newCodexCommandLayout(t)
	const token = "amber-lion-42"
	createMultiSkillDelegationRepo(t, layout.worktree, layout.cellRel, token)

	baseInput := codexpkg.ExecOpInput{
		Skill:              "software-dev-orchestrator",
		SkillMode:          "enforce",
		SkillSelectionMode: "ordered",
		WorkdirPath:        layout.workdir,
		WorktreePath:       layout.worktree,
		ArtifactInboxPath:  layout.inbox,
		ArtifactOutboxPath: layout.outbox,
		CellRelativePath:   layout.cellRel,
		Env: map[string]string{
			"RUST_LOG": "codex_core::rollout::list=off",
		},
	}

	steps := []struct {
		prompt     string
		status     string
		summary    string
		incomplete string
	}{
		{
			prompt:     fmt.Sprintf("Start the software-development flow. previous_incomplete_reason=none. previous_summary=none. Execute exactly one delegated step, then return. Remember this token for follow-up turns: %s. Return only schema JSON.", token),
			status:     string(codexpkg.StatusIncomplete),
			summary:    "planning complete",
			incomplete: "next:execute",
		},
		{
			prompt:     "continue. previous_incomplete_reason=next:execute. previous_summary=planning complete. Execute exactly one delegated step, then return. Return only schema JSON.",
			status:     string(codexpkg.StatusIncomplete),
			summary:    fmt.Sprintf("execution complete token:%s", token),
			incomplete: "next:validate",
		},
		{
			prompt:     fmt.Sprintf("continue. previous_incomplete_reason=next:validate. previous_summary=execution complete token:%s. Execute exactly one delegated step, then return. Return only schema JSON.", token),
			status:     string(codexpkg.StatusCompleted),
			summary:    "validation complete",
			incomplete: "",
		},
	}

	sessionID := ""
	for i, step := range steps {
		input := baseInput
		input.SessionID = sessionID
		input.Prompt = step.prompt

		out, stdout, stderr, err := runCodexExecOp(t, input)
		require.NoError(t, err, "step=%d stdout=%s\nstderr=%s", i+1, stdout, stderr)
		require.Equal(t, step.status, out.Output.Status)
		require.Equal(t, step.summary, out.Output.AssistantSummary)
		require.Equal(t, step.incomplete, out.Output.IncompleteReason)
		require.NotEmpty(t, out.Output.SessionID)
		assertCodexArtifacts(t, layout.outbox)

		sessionID = out.Output.SessionID
		if i < len(steps)-1 {
			promoteCodexState(t, layout)
		}
	}
}

func TestCodexExecOpReadsInboxWritesGitAndOutbox(t *testing.T) {
	ensureCodexAvailable(t)

	layout := newCodexCommandLayout(t)
	runGit(t, layout.worktree, "init")
	runGit(t, layout.worktree, "config", "user.email", "test@example.com")
	runGit(t, layout.worktree, "config", "user.name", "Test User")
	writeRepoFile(t, filepath.Join(layout.worktree, "README.md"), "fixture\n")
	runGit(t, layout.worktree, "add", ".")
	runGit(t, layout.worktree, "commit", "-m", "init")

	require.NoError(t, os.WriteFile(filepath.Join(layout.inbox, "input.txt"), []byte("fixture-token"), 0o644))

	prompt := fmt.Sprintf(
		"Run this exact shell command and do not alter it:\n"+
			"bash -lc 'set -euo pipefail; token=$(cat \"../inbox/input.txt\"); mkdir -p %q; printf \"from_inbox=%%s\" \"$token\" > %q; printf \"outbox_from_inbox=%%s\" \"$token\" > \"../outbox/codex-generated.txt\"; test -s %q; test -s \"../outbox/codex-generated.txt\"'\n"+
			"After running it, provide your normal structured response.",
		layout.cellRel,
		layout.cellRel+"/codex-fixture.txt",
		layout.cellRel+"/codex-fixture.txt",
	)

	out, stdout, stderr, err := runCodexExecOp(t, codexpkg.ExecOpInput{
		Prompt:             prompt,
		WorkdirPath:        layout.workdir,
		WorktreePath:       layout.worktree,
		ArtifactInboxPath:  layout.inbox,
		ArtifactOutboxPath: layout.outbox,
		CellRelativePath:   layout.cellRel,
		Env: map[string]string{
			"RUST_LOG": "codex_core::rollout::list=off",
		},
	})
	require.NoError(t, err, "stdout=%s\nstderr=%s", stdout, stderr)
	require.Equal(t, string(codexpkg.StatusCompleted), out.Output.Status)
	assertCodexArtifacts(t, layout.outbox)

	gitContent, err := os.ReadFile(filepath.Join(layout.worktree, layout.cellRel, "codex-fixture.txt"))
	require.NoError(t, err)
	require.Equal(t, "from_inbox=fixture-token", string(gitContent))

	outboxContent, err := os.ReadFile(filepath.Join(layout.outbox, "codex-generated.txt"))
	require.NoError(t, err)
	require.Equal(t, "outbox_from_inbox=fixture-token", string(outboxContent))
}

func ensureCodexAvailable(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping codex integration tests in short mode")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not available in PATH")
	}
}

func newCodexCommandLayout(t *testing.T) codexCommandLayout {
	t.Helper()

	workdir := t.TempDir()
	worktree := filepath.Join(workdir, "worktree")
	inbox := filepath.Join(workdir, "inbox")
	outbox := filepath.Join(workdir, "outbox")
	cellRel := filepath.Join("cells", "alpha")

	require.NoError(t, os.MkdirAll(filepath.Join(worktree, cellRel), 0o755))
	require.NoError(t, os.MkdirAll(inbox, 0o755))
	require.NoError(t, os.MkdirAll(outbox, 0o755))

	return codexCommandLayout{
		workdir:  workdir,
		worktree: worktree,
		inbox:    inbox,
		outbox:   outbox,
		cellRel:  cellRel,
	}
}

func runCodexExecOp(t *testing.T, input codexpkg.ExecOpInput) (codexCommandEnvelope, string, string, error) {
	t.Helper()

	payload, err := json.Marshal(input)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/codex-exec-op")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "C2J_CODEX_USE_DIRECT=1")
	cmd.Stdin = bytes.NewReader(payload)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	var env codexCommandEnvelope
	if strings.TrimSpace(stdout.String()) != "" {
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &env), "stdout=%s", stdout.String())
	}

	return env, stdout.String(), stderr.String(), err
}

func assertCodexArtifacts(t *testing.T, outbox string) {
	t.Helper()

	stdout, err := os.ReadFile(filepath.Join(outbox, "stdout.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, stdout, "missing stdout.jsonl artifact")
	validateJSONL(t, stdout)

	stderr, err := os.ReadFile(filepath.Join(outbox, "stderr.txt"))
	require.NoError(t, err)
	require.Empty(t, filterIgnorableCodexStderr(stderr), "stderr.txt should be empty")
}

func validateJSONL(t *testing.T, content []byte) {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	require.NotEmpty(t, lines, "stdout jsonl is empty")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatalf("invalid jsonl at line %d: %v\nline=%q", i+1, err, line)
		}
	}
}

func filterIgnorableCodexStderr(stderr []byte) []byte {
	if len(stderr) == 0 {
		return nil
	}

	lines := strings.Split(string(stderr), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "WARNING: proceeding, even though we could not update PATH: Refusing to create helper binaries under temporary dir") {
			continue
		}
		if trimmed == "Reading additional input from stdin..." {
			continue
		}
		filtered = append(filtered, line)
	}
	if len(filtered) == 0 {
		return nil
	}
	return []byte(strings.Join(filtered, "\n"))
}

func promoteCodexState(t *testing.T, layout codexCommandLayout) {
	t.Helper()

	for _, name := range []string{"codex-home-state", "codex-sessions"} {
		sourceDir := filepath.Join(layout.outbox, name)
		targetDir := filepath.Join(layout.inbox, name)

		info, err := os.Stat(sourceDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			require.NoError(t, err)
		}
		if !info.IsDir() {
			continue
		}

		require.NoError(t, os.RemoveAll(targetDir))
		copyDirContents(t, sourceDir, targetDir)
	}

	require.NoError(t, os.RemoveAll(filepath.Join(layout.workdir, ".codex")))
}

func copyDirContents(t *testing.T, sourceDir string, targetDir string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(targetDir, 0o755))

	entries, err := os.ReadDir(sourceDir)
	require.NoError(t, err)

	for _, entry := range entries {
		sourcePath := filepath.Join(sourceDir, entry.Name())
		targetPath := filepath.Join(targetDir, entry.Name())

		info, err := entry.Info()
		require.NoError(t, err)

		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.IsDir() {
			copyDirContents(t, sourcePath, targetPath)
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		copyFile(t, sourcePath, targetPath, info.Mode().Perm())
	}
}

func copyFile(t *testing.T, sourcePath string, targetPath string, mode os.FileMode) {
	t.Helper()

	sourceFile, err := os.Open(sourcePath)
	require.NoError(t, err)
	defer sourceFile.Close()

	require.NoError(t, os.MkdirAll(filepath.Dir(targetPath), 0o755))

	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	require.NoError(t, err)
	defer targetFile.Close()

	_, err = io.Copy(targetFile, sourceFile)
	require.NoError(t, err)
}

func createMultiSkillDelegationRepo(t *testing.T, worktree string, cellRel string, token string) {
	t.Helper()

	runGit(t, worktree, "init")
	runGit(t, worktree, "config", "user.email", "test@example.com")
	runGit(t, worktree, "config", "user.name", "Test User")

	writeRepoFile(t, filepath.Join(worktree, "README.md"), "multi-skill integration fixture\n")
	writeRepoFile(t, filepath.Join(worktree, cellRel, "README.md"), "fixture cell\n")

	skillsDir := filepath.Join(worktree, ".agents", "skills")
	writeRepoFile(t, filepath.Join(skillsDir, "software-dev-orchestrator", "SKILL.md"), `---
name: software-dev-orchestrator
description: Delegates software development phases to planning, execution, and validation skills.
---
# Software Development Orchestrator

Execute exactly one phase per invocation.

1. Determine phase from current user request fields:
   - if previous_incomplete_reason is none, phase is plan
   - if previous_incomplete_reason is next:execute, phase is execute
   - if previous_incomplete_reason is next:validate, phase is validate
   - if previous_summary is validation complete, return completed JSON with assistantSummary "already complete", empty incompleteReason, empty incompleteCategory
2. Delegate by invoking exactly one skill by name:
   - software-dev-plan
   - software-dev-execute
   - software-dev-validate
3. Do not run shell commands in this skill.
4. Return only schema JSON.
`)

	writeRepoFile(t, filepath.Join(skillsDir, "software-dev-plan", "SKILL.md"), `---
name: software-dev-plan
description: Planning phase for the software development workflow.
---
# Planning Skill

Return only schema JSON exactly:
{"status":"incomplete","assistantSummary":"planning complete","incompleteReason":"next:execute","incompleteCategory":"","errorMessage":"","pendingDependencies":[]}
`)

	writeRepoFile(t, filepath.Join(skillsDir, "software-dev-execute", "SKILL.md"), fmt.Sprintf(`---
name: software-dev-execute
description: Execution phase for the software development workflow.
---
# Execution Skill

Return only schema JSON exactly:
{"status":"incomplete","assistantSummary":"execution complete token:%s","incompleteReason":"next:validate","incompleteCategory":"","errorMessage":"","pendingDependencies":[]}
`, token))

	writeRepoFile(t, filepath.Join(skillsDir, "software-dev-validate", "SKILL.md"), `---
name: software-dev-validate
description: Validation phase for the software development workflow.
---
# Validation Skill

Return only schema JSON exactly:
{"status":"completed","assistantSummary":"validation complete","incompleteReason":"","incompleteCategory":"","errorMessage":"","pendingDependencies":[]}
`)

	runGit(t, worktree, "add", ".")
	runGit(t, worktree, "commit", "-m", "seed multi-skill fixtures")
}

func writeRepoFile(t *testing.T, path string, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(output))
}
