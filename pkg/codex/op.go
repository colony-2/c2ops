package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// ExecOpInput defines the codex.exec command input.
type ExecOpInput struct {
	Prompt             string            `json:"prompt" validate:"required"`
	SessionID          string            `json:"sessionId,omitempty"`
	Model              string            `json:"model,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	Skill              string            `json:"skill,omitempty"`
	Skills             []string          `json:"skills,omitempty"`
	SkillMode          string            `json:"skill_mode,omitempty"`
	SkillSelectionMode string            `json:"skill_selection_mode,omitempty"`
	ReturnOn           []string          `json:"return_on,omitempty"`
	StatusContract     StatusContractRef `json:"status_contract,omitempty"`
	ResumeContext      map[string]any    `json:"resume_context,omitempty"`
	WorkdirPath        string            `json:"workdir_path,omitempty" default:"{{ context.environment.workdir }}"`
	WorktreePath       string            `json:"worktree_path" default:"{{ context.environment.worktree_path }}" validate:"required"`
	ArtifactInboxPath  string            `json:"artifact_inbox_path,omitempty" default:"{{ context.environment.inbox }}"`
	ArtifactOutboxPath string            `json:"artifact_outbox_path,omitempty" default:"{{ context.environment.outbox }}"`
	CellRelativePath   string            `json:"cell_relative_path" default:"{{ context.workflow.cell_path }}" validate:"required"`
}

// ExecOpOutput mirrors the structured response surfaced by the codex library.
type ExecOpOutput struct {
	Status              string       `json:"status"`
	SessionID           string       `json:"sessionId"`
	Outcome             ExecOutcome  `json:"outcome"`
	AssistantSummary    string       `json:"assistantSummary"`
	IncompleteReason    string       `json:"incompleteReason"`
	IncompleteCategory  string       `json:"incompleteCategory"`
	PendingDependencies []Dependency `json:"pendingDependencies"`
	SkillsInstalled     []string     `json:"skills_installed,omitempty"`
}

type StatusContractRef struct {
	Path string `json:"path,omitempty"`
}

type ExecOutcome struct {
	Summary    ExecOutcomeSummary    `json:"summary"`
	Skill      ExecOutcomeSkill      `json:"skill"`
	Checkpoint ExecOutcomeCheckpoint `json:"checkpoint"`
	Routing    ExecOutcomeRouting    `json:"routing"`
}

type ExecOutcomeSummary struct {
	Human  string `json:"human,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type ExecOutcomeSkill struct {
	Executed      string `json:"executed,omitempty"`
	SelectionMode string `json:"selectionMode,omitempty"`
	NextCandidate string `json:"nextCandidate,omitempty"`
}

type ExecOutcomeCheckpoint struct {
	Status          string                       `json:"status,omitempty"`
	Scope           string                       `json:"scope,omitempty"`
	BlockingSkill   string                       `json:"blockingSkill,omitempty"`
	Stack           []ExecOutcomeCheckpointFrame `json:"stack"`
	StatusArtifact  string                       `json:"statusArtifact,omitempty"`
	ReturnTriggered bool                         `json:"returnTriggered"`
	ReturnReason    string                       `json:"returnReason,omitempty"`
	ContractErrors  []string                     `json:"contractErrors"`
}

type ExecOutcomeCheckpointFrame struct {
	Skill string `json:"skill"`
	Scope string `json:"scope"`
}

type ExecOutcomeRouting struct {
	NextAction string `json:"nextAction,omitempty"`
}

// executeLibrary is replaceable for tests.
var executeLibrary = Execute

// Run executes codex.exec via explicit inputs.
func Run(actx context.Context, input ExecOpInput) (ExecOpOutput, error) {
	return runCodexActivity(actx, input)
}

func runCodexActivity(actx context.Context, input ExecOpInput) (ExecOpOutput, error) {
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		return ExecOpOutput{}, fmt.Errorf("prompt is required")
	}

	worktree := strings.TrimSpace(input.WorktreePath)
	if worktree == "" {
		return ExecOpOutput{}, fmt.Errorf("worktree_path is required")
	}
	workdir := strings.TrimSpace(input.WorkdirPath)
	if workdir == "" {
		workdir = worktree
	}
	inbox := strings.TrimSpace(input.ArtifactInboxPath)
	if inbox == "" {
		inbox = filepath.Join(workdir, "inbox")
	}
	outbox := strings.TrimSpace(input.ArtifactOutboxPath)
	if outbox == "" {
		outbox = filepath.Join(workdir, "outbox")
	}
	cellRelPath := strings.TrimSpace(input.CellRelativePath)
	if cellRelPath == "" {
		return ExecOpOutput{}, fmt.Errorf("cell_relative_path is required")
	}
	configuredSkillDirs, skillsInstalled, skillSourcesCleanup, err := prepareConfiguredSkillSources(actx, input, workdir)
	if err != nil {
		return ExecOpOutput{}, err
	}
	if skillSourcesCleanup != nil {
		defer func() {
			_ = skillSourcesCleanup()
		}()
	}
	skillCfg, err := prepareSkillExecutionConfig(input)
	if err != nil {
		return ExecOpOutput{}, err
	}
	promptForExec := renderSkillPrompt(prompt, skillCfg)

	opts := Options{
		Prompt:              promptForExec,
		SessionID:           strings.TrimSpace(input.SessionID),
		Model:               strings.TrimSpace(input.Model),
		ExtraEnv:            input.Env,
		WorkDirRoot:         workdir,
		WorktreeRoot:        worktree,
		ArtifactInbox:       inbox,
		ArtifactOutbox:      outbox,
		CellRelativePath:    cellRelPath,
		ConfiguredSkillDirs: configuredSkillDirs,
	}

	result, stdoutPath, stderrPath, artifactDir, executeErr := executeLibrary(actx, opts)
	debugArtifactf("stdout=%q stderr=%q artifact_dir=%q err=%v", stdoutPath, stderrPath, artifactDir, executeErr)
	defer cleanupArtifacts(stdoutPath, stderrPath, artifactDir)
	if err := writeOutputArtifacts(outbox, stdoutPath, stderrPath); err != nil {
		return ExecOpOutput{}, err
	}

	outcome := buildExecOutcome(result, skillCfg, outbox)
	finalStatus := deriveExecOutputStatus(result.Status, outcome)
	output := ExecOpOutput{
		Status:              string(finalStatus),
		SessionID:           safeString(result.SessionID),
		Outcome:             outcome,
		AssistantSummary:    safeString(outcome.Summary.Human),
		IncompleteReason:    safeString(result.IncompleteReason),
		IncompleteCategory:  safeString(result.IncompleteCategory),
		PendingDependencies: copyDependencies(result.PendingDependencies),
		SkillsInstalled:     copyStrings(skillsInstalled),
	}
	if finalStatus == StatusIncomplete {
		if output.IncompleteCategory == "" {
			output.IncompleteCategory = safeString(outcome.Checkpoint.Status)
		}
		if output.IncompleteReason == "" {
			output.IncompleteReason = safeString(outcome.Checkpoint.ReturnReason)
		}
	}

	if executeErr != nil {
		return ExecOpOutput{}, fmt.Errorf("codex execution error: %w", executeErr)
	}
	if result.Status == StatusError {
		msg := strings.TrimSpace(result.ErrorMessage)
		if msg == "" {
			msg = "codex reported an error"
		}
		return ExecOpOutput{}, fmt.Errorf("codex error: %s", msg)
	}
	return output, nil
}

func debugArtifactf(format string, args ...interface{}) {
}

func debugArtifactStat(label, path string) {
}

func writeOutputArtifacts(outbox string, stdoutPath string, stderrPath string) error {
	if strings.TrimSpace(outbox) == "" {
		return nil
	}

	if err := writeOutputArtifact(outbox, "stdout.jsonl", stdoutPath); err != nil {
		return err
	}
	if err := writeOutputArtifact(outbox, "stderr.txt", stderrPath); err != nil {
		return err
	}
	return nil
}

func writeOutputArtifact(outbox string, name string, srcPath string) error {
	if strings.TrimSpace(srcPath) == "" {
		return nil
	}
	dest := filepath.Join(outbox, name)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create artifact directory for %s: %w", name, err)
	}
	debugArtifactStat(name+"-source", srcPath)
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open artifact %s: %w", name, err)
	}
	defer src.Close()
	dst, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create outbox artifact %s: %w", name, err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy artifact %s: %w", name, err)
	}
	debugArtifactStat(name+"-dest", dest)
	return nil
}

func cleanupArtifacts(stdoutPath string, stderrPath string, artifactDir string) {
	for _, path := range []string{stdoutPath, stderrPath} {
		if strings.TrimSpace(path) == "" {
			continue
		}
		err := os.Remove(path)
		debugArtifactf("cleanup path=%q err=%v\nstack=%s", path, err, debug.Stack())
	}
	if strings.TrimSpace(artifactDir) != "" {
		err := os.RemoveAll(artifactDir)
		debugArtifactf("cleanup artifact_dir=%q err=%v\nstack=%s", artifactDir, err, debug.Stack())
	}
}

func copyDependencies(in []Dependency) []Dependency {
	if len(in) == 0 {
		return []Dependency{}
	}
	out := make([]Dependency, len(in))
	copy(out, in)
	return out
}

func copyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func safeString(s string) string {
	if s == "" {
		return ""
	}
	return s
}

func digestPrompt(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:8])
}
