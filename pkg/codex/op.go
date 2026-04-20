package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/google/uuid"
)

// ExecOpInput defines the codex.exec activity inputs expected from recipe-worker.
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

// GetOp exposes the codex.exec activity as a RegisterableOp.
func GetOp() ops.RegisterableOp {
	return ops.NewActivityMappedOpV2[ExecOpInput, ExecOpOutput](ops.OpMetadata{
		Type:             "codex.exec",
		Description:      "Runs Codex CLI in non-interactive mode with structured output capture",
		Version:          "1.0.0",
		DefaultTimeout:   30 * time.Minute,
		AcceptsArtifacts: true,
	}, runCodexActivity)
}

func runCodexActivity(inv ops.OpDependencies, actx context.Context, input ExecOpInput) (ExecOpOutput, error) {

	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		return ExecOpOutput{}, workflow.NewNonRetryableApplicationError("prompt is required")
	}

	worktree := strings.TrimSpace(input.WorktreePath)
	if worktree == "" {
		return ExecOpOutput{}, workflow.NewNonRetryableApplicationError("worktree_path is required")
	}
	workdir := strings.TrimSpace(input.WorkdirPath)
	if workdir == "" {
		// Backward-compatible fallback for direct tests/callers that only pass worktree.
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
		return ExecOpOutput{}, workflow.NewNonRetryableApplicationError("cell_relative_path is required")
	}
	configuredSkillDirs, skillsInstalled, skillSourcesCleanup, err := prepareConfiguredSkillSources(actx, input, workdir)
	if err != nil {
		return ExecOpOutput{}, workflow.NewNonRetryableApplicationError("%s", err.Error())
	}
	if skillSourcesCleanup != nil {
		defer func() {
			_ = skillSourcesCleanup()
		}()
	}
	skillCfg, err := prepareSkillExecutionConfig(input)
	if err != nil {
		return ExecOpOutput{}, workflow.NewNonRetryableApplicationError("%s", err.Error())
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

	executionID := uuid.NewString()
	debugArtifactf("execution_id=%s stdout=%q stderr=%q artifact_dir=%q err=%v", executionID, stdoutPath, stderrPath, artifactDir, executeErr)

	// Always create cleanup functions for artifacts
	stdoutCleanup := func() error {
		if stdoutPath != "" {
			err := os.Remove(stdoutPath)
			debugArtifactf("cleanup stdout path=%q err=%v\nstack=%s", stdoutPath, err, debug.Stack())
			return err
		}
		return nil
	}
	stderrCleanup := func() error {
		if stderrPath != "" {
			err := os.Remove(stderrPath)
			debugArtifactf("cleanup stderr path=%q err=%v\nstack=%s", stderrPath, err, debug.Stack())
			return err
		}
		return nil
	}

	// Always register stdout/stderr artifacts if files were created, even on timeout/error
	// This ensures we capture any output that was written before the timeout occurred
	if stdoutPath != "" {
		stdoutArtifact := swf.NewArtifact(
			"stdout.jsonl",
			func() (io.ReadCloser, int64, error) {
				debugArtifactStat("stdout", stdoutPath)
				f, err := os.Open(stdoutPath)
				if err != nil {
					return nil, 0, fmt.Errorf("open stdout: %w", err)
				}
				info, err := f.Stat()
				if err != nil {
					f.Close()
					return nil, 0, fmt.Errorf("stat stdout: %w", err)
				}
				return f, info.Size(), nil
			},
			stdoutCleanup,
		)
		if err := inv.AddOutputArtifact(stdoutArtifact); err != nil {
			os.RemoveAll(artifactDir) // Clean up on artifact error
			return ExecOpOutput{}, fmt.Errorf("add stdout artifact: %w", err)
		}
		debugArtifactStat("stdout-before-return", stdoutPath)
	}

	if stderrPath != "" {
		stderrArtifact := swf.NewArtifact(
			"stderr.txt",
			func() (io.ReadCloser, int64, error) {
				debugArtifactStat("stderr", stderrPath)
				f, err := os.Open(stderrPath)
				if err != nil {
					return nil, 0, fmt.Errorf("open stderr: %w", err)
				}
				info, err := f.Stat()
				if err != nil {
					f.Close()
					return nil, 0, fmt.Errorf("stat stderr: %w", err)
				}
				return f, info.Size(), nil
			},
			stderrCleanup,
		)
		if err := inv.AddOutputArtifact(stderrArtifact); err != nil {
			os.RemoveAll(artifactDir) // Clean up on artifact error
			return ExecOpOutput{}, fmt.Errorf("add stderr artifact: %w", err)
		}
		debugArtifactStat("stderr-before-return", stderrPath)
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

	// Check for errors after artifacts are registered
	// This ensures stdout/stderr are available even on timeout/error
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
	// Artifact dir cleaned up by artifact cleanup callbacks (after both artifacts consumed)
}

var artifactDebugOnce sync.Once
var artifactDebugEnabled bool

func artifactDebug() bool {
	artifactDebugOnce.Do(func() {
		val := strings.TrimSpace(os.Getenv("VIBETHIS_CODEX_ARTIFACT_DEBUG"))
		if val == "1" || strings.EqualFold(val, "true") || strings.EqualFold(val, "yes") {
			artifactDebugEnabled = true
		}
	})
	return artifactDebugEnabled
}

func debugArtifactf(format string, args ...interface{}) {
	if artifactDebug() {
		log.Printf("codex.exec artifacts: "+format, args...)
	}
}

func debugArtifactStat(label, path string) {
	if !artifactDebug() {
		return
	}
	if path == "" {
		log.Printf("codex.exec artifacts: %s path empty", label)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		log.Printf("codex.exec artifacts: %s stat path=%q err=%v", label, path, err)
		return
	}
	log.Printf("codex.exec artifacts: %s stat path=%q size=%d", label, path, info.Size())
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
