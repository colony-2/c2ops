package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	skillModeEnforce            = "enforce"
	skillSelectionModeAdaptive  = "adaptive"
	skillSelectionModeOrdered   = "ordered"
	checkpointStatusBlocked     = "blocked"
	checkpointStatusCompleted   = "completed"
	checkpointStatusReady       = "checkpoint_ready"
	checkpointScopeTopLevel     = "top_level"
	checkpointScopeNested       = "nested"
	routingReturnToCheckpoint   = "return_to_recipe_checkpoint"
	routingCompleteSkillSegment = "complete_skill_segment"
	routingComplete             = "complete"
)

var defaultReturnOnStatuses = []string{
	"needs_user_input",
	"needs_other_cell_changes",
	"needs_dependency_tickets",
	"needs_test_statement_update",
	"blocked",
	"ready_for_validation",
	"ready_for_merge",
	"checkpoint_ready",
}

type skillExecutionConfig struct {
	SelectedSkill      string
	SkillMode          string
	SelectionMode      string
	ReturnOn           []string
	StatusContractPath string
}

type parsedStatusContract struct {
	checkpointStatus string
	scope            string
	blockingSkill    string
	stack            []ExecOutcomeCheckpointFrame
	nextCandidate    string
	summaryHuman     string
	summaryReason    string
}

type statusContractFile struct {
	Status                 string                       `json:"status"`
	Summary                statusContractSummary        `json:"summary"`
	NextSkillCandidates    []string                     `json:"nextSkillCandidates"`
	NextSkillCandidatesAlt []string                     `json:"next_skill_candidates"`
	Checkpoint             statusContractCheckpointFile `json:"checkpoint"`
}

type statusContractSummary struct {
	Human  string `json:"human"`
	Reason string `json:"reason"`
}

type statusContractCheckpointFile struct {
	Status           string                       `json:"status"`
	Scope            string                       `json:"scope"`
	BlockingSkill    string                       `json:"blockingSkill"`
	BlockingSkillAlt string                       `json:"blocking_skill"`
	Stack            []ExecOutcomeCheckpointFrame `json:"stack"`
}

func prepareSkillExecutionConfig(
	input ExecOpInput,
) (skillExecutionConfig, error) {
	selectedSkill, err := resolveSelectedSkill(input.Skill)
	if err != nil {
		return skillExecutionConfig{}, err
	}

	skillMode := normalizeStatus(input.SkillMode)
	if skillMode == "" && selectedSkill != "" {
		skillMode = skillModeEnforce
	}
	if skillMode != "" && skillMode != skillModeEnforce {
		return skillExecutionConfig{}, fmt.Errorf("skill_mode must be %q when provided", skillModeEnforce)
	}
	if skillMode == skillModeEnforce && selectedSkill == "" {
		return skillExecutionConfig{}, fmt.Errorf("skill_mode=enforce requires a skill")
	}

	selectionMode := normalizeStatus(input.SkillSelectionMode)
	if selectionMode == "" {
		selectionMode = skillSelectionModeAdaptive
	}
	if selectionMode != skillSelectionModeAdaptive && selectionMode != skillSelectionModeOrdered {
		return skillExecutionConfig{}, fmt.Errorf("skill_selection_mode must be %q or %q", skillSelectionModeAdaptive, skillSelectionModeOrdered)
	}

	returnOn := normalizeStatuses(input.ReturnOn)
	if len(returnOn) == 0 {
		returnOn = append([]string{}, defaultReturnOnStatuses...)
	}

	cfg := skillExecutionConfig{
		SelectedSkill:      selectedSkill,
		SkillMode:          skillMode,
		SelectionMode:      selectionMode,
		ReturnOn:           returnOn,
		StatusContractPath: strings.TrimSpace(input.StatusContract.Path),
	}

	return cfg, nil
}

func resolveSelectedSkill(skill string) (string, error) {
	return strings.TrimSpace(skill), nil
}

func renderSkillPrompt(prompt string, cfg skillExecutionConfig) string {
	if strings.TrimSpace(cfg.SelectedSkill) == "" {
		return prompt
	}

	var builder strings.Builder
	builder.WriteString("Execution contract:\n")
	builder.WriteString("- skill_mode=enforce.\n")
	builder.WriteString(fmt.Sprintf("- execute exactly one top-level skill segment: %s.\n", cfg.SelectedSkill))
	builder.WriteString(fmt.Sprintf("- selection_mode=%s.\n", cfg.SelectionMode))
	builder.WriteString("- nested skill checkpoints are checkpoints: return immediately when one occurs.\n")
	if cfg.StatusContractPath != "" {
		builder.WriteString(fmt.Sprintf("- write authoritative status JSON to outbox artifact path: %s.\n", cfg.StatusContractPath))
	}
	if len(cfg.ReturnOn) > 0 {
		builder.WriteString(fmt.Sprintf("- if checkpoint status is one of [%s], stop this invocation and return.\n", strings.Join(cfg.ReturnOn, ", ")))
	}
	builder.WriteString("\nUser request:\n")
	builder.WriteString(prompt)
	return builder.String()
}

func buildExecOutcome(result Result, cfg skillExecutionConfig, outboxPath string) ExecOutcome {
	outcome := ExecOutcome{
		Summary: ExecOutcomeSummary{
			Human:  safeString(result.AssistantSummary),
			Reason: safeString(result.IncompleteReason),
		},
		Skill: ExecOutcomeSkill{
			Executed:      cfg.SelectedSkill,
			SelectionMode: cfg.SelectionMode,
		},
		Checkpoint: ExecOutcomeCheckpoint{
			Status:         deriveCheckpointStatus(result),
			Scope:          checkpointScopeTopLevel,
			Stack:          []ExecOutcomeCheckpointFrame{},
			ContractErrors: []string{},
		},
		Routing: ExecOutcomeRouting{
			NextAction: routingComplete,
		},
	}

	if strings.TrimSpace(outcome.Summary.Reason) == "" {
		outcome.Summary.Reason = safeString(result.IncompleteCategory)
	}

	if cfg.SelectedSkill == "" {
		outcome.Skill.SelectionMode = ""
	} else {
		outcome.Checkpoint.Stack = append(outcome.Checkpoint.Stack, ExecOutcomeCheckpointFrame{
			Skill: cfg.SelectedSkill,
			Scope: checkpointScopeTopLevel,
		})
	}

	if strings.TrimSpace(cfg.StatusContractPath) != "" {
		outcome.Checkpoint.StatusArtifact = filepath.ToSlash(cfg.StatusContractPath)
		contract, err := loadStatusContract(outboxPath, cfg.StatusContractPath)
		if err != nil {
			outcome.Checkpoint.ContractErrors = append(outcome.Checkpoint.ContractErrors, err.Error())
		} else {
			if contract.summaryHuman != "" {
				outcome.Summary.Human = contract.summaryHuman
			}
			if contract.summaryReason != "" {
				outcome.Summary.Reason = contract.summaryReason
			}
			if contract.checkpointStatus != "" {
				outcome.Checkpoint.Status = contract.checkpointStatus
			}
			if contract.scope != "" {
				outcome.Checkpoint.Scope = contract.scope
			}
			if contract.blockingSkill != "" {
				outcome.Checkpoint.BlockingSkill = contract.blockingSkill
			}
			if len(contract.stack) > 0 {
				outcome.Checkpoint.Stack = contract.stack
			}
			if contract.nextCandidate != "" {
				outcome.Skill.NextCandidate = contract.nextCandidate
			}
		}
	}

	if len(outcome.Checkpoint.ContractErrors) > 0 {
		outcome.Checkpoint.Status = checkpointStatusBlocked
	}
	if outcome.Checkpoint.Scope == "" {
		outcome.Checkpoint.Scope = inferCheckpointScope(outcome.Checkpoint.Stack)
	}
	if outcome.Checkpoint.Scope == "" {
		outcome.Checkpoint.Scope = checkpointScopeTopLevel
	}

	outcome.Checkpoint.ReturnTriggered = shouldReturnOnStatus(outcome.Checkpoint.Status, cfg.ReturnOn)
	if outcome.Checkpoint.ReturnTriggered {
		if len(outcome.Checkpoint.ContractErrors) > 0 {
			outcome.Checkpoint.ReturnReason = "contract_error"
		} else {
			outcome.Checkpoint.ReturnReason = outcome.Checkpoint.Status
		}
	}
	outcome.Routing.NextAction = determineRoutingAction(outcome.Checkpoint, cfg.SelectedSkill)

	return outcome
}

func determineRoutingAction(checkpoint ExecOutcomeCheckpoint, selectedSkill string) string {
	if checkpoint.Scope == checkpointScopeNested {
		return routingReturnToCheckpoint
	}
	if checkpoint.ReturnTriggered {
		return routingReturnToCheckpoint
	}
	if strings.TrimSpace(selectedSkill) != "" {
		return routingCompleteSkillSegment
	}
	return routingComplete
}

func deriveExecOutputStatus(resultStatus Status, outcome ExecOutcome) Status {
	if resultStatus == StatusError {
		return StatusError
	}
	if len(outcome.Checkpoint.ContractErrors) > 0 {
		return StatusIncomplete
	}
	checkpointStatus := normalizeStatus(outcome.Checkpoint.Status)
	if checkpointStatus == "" || checkpointStatus == checkpointStatusCompleted {
		return resultStatus
	}
	if outcome.Checkpoint.ReturnTriggered {
		return StatusIncomplete
	}
	return resultStatus
}

func deriveCheckpointStatus(result Result) string {
	category := normalizeStatus(result.IncompleteCategory)
	switch category {
	case "dependency_blockers":
		return "needs_dependency_tickets"
	case "":
	default:
		return category
	}

	switch result.Status {
	case StatusError:
		return checkpointStatusBlocked
	case StatusIncomplete:
		return checkpointStatusReady
	default:
		return checkpointStatusCompleted
	}
}

func loadStatusContract(outboxPath string, statusPath string) (parsedStatusContract, error) {
	resolvedPath, err := resolveStatusContractPath(outboxPath, statusPath)
	if err != nil {
		return parsedStatusContract{}, err
	}
	payload, err := os.ReadFile(resolvedPath)
	if err != nil {
		return parsedStatusContract{}, fmt.Errorf("read status artifact %q: %w", statusPath, err)
	}

	var doc statusContractFile
	if err := json.Unmarshal(payload, &doc); err != nil {
		return parsedStatusContract{}, fmt.Errorf("parse status artifact %q: %w", statusPath, err)
	}

	checkpointStatus := normalizeStatus(doc.Checkpoint.Status)
	if checkpointStatus == "" {
		checkpointStatus = normalizeStatus(doc.Status)
	}
	nextCandidates := doc.NextSkillCandidates
	if len(nextCandidates) == 0 {
		nextCandidates = doc.NextSkillCandidatesAlt
	}

	out := parsedStatusContract{
		checkpointStatus: checkpointStatus,
		scope:            normalizeCheckpointScope(doc.Checkpoint.Scope),
		blockingSkill:    strings.TrimSpace(firstNonEmpty(doc.Checkpoint.BlockingSkill, doc.Checkpoint.BlockingSkillAlt)),
		nextCandidate:    strings.TrimSpace(firstNonEmpty(nextCandidates...)),
		summaryHuman:     strings.TrimSpace(doc.Summary.Human),
		summaryReason:    strings.TrimSpace(doc.Summary.Reason),
	}

	for _, frame := range doc.Checkpoint.Stack {
		skill := strings.TrimSpace(frame.Skill)
		if skill == "" {
			continue
		}
		scope := normalizeCheckpointScope(frame.Scope)
		if scope == "" {
			scope = checkpointScopeNested
		}
		out.stack = append(out.stack, ExecOutcomeCheckpointFrame{Skill: skill, Scope: scope})
	}
	if out.scope == "" {
		out.scope = inferCheckpointScope(out.stack)
	}

	return out, nil
}

func resolveStatusContractPath(outboxPath string, configuredPath string) (string, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		return "", fmt.Errorf("status contract path is empty")
	}
	if strings.TrimSpace(outboxPath) == "" {
		return "", fmt.Errorf("artifact outbox path is empty")
	}

	normalized := filepath.ToSlash(filepath.Clean(configuredPath))
	if strings.HasPrefix(normalized, "outbox/") {
		normalized = strings.TrimPrefix(normalized, "outbox/")
	}

	var resolved string
	if filepath.IsAbs(configuredPath) {
		resolved = filepath.Clean(configuredPath)
	} else {
		resolved = filepath.Join(outboxPath, filepath.FromSlash(normalized))
	}
	resolved = filepath.Clean(resolved)

	if err := ensureDescendantPath(outboxPath, resolved, "status contract artifact"); err != nil {
		return "", err
	}
	return resolved, nil
}

func inferCheckpointScope(stack []ExecOutcomeCheckpointFrame) string {
	if len(stack) == 0 {
		return ""
	}
	if len(stack) > 1 {
		return checkpointScopeNested
	}
	scope := normalizeCheckpointScope(stack[len(stack)-1].Scope)
	if scope != "" {
		return scope
	}
	return checkpointScopeTopLevel
}

func normalizeCheckpointScope(scope string) string {
	normalized := normalizeStatus(scope)
	switch normalized {
	case checkpointScopeTopLevel, "top", "toplevel":
		return checkpointScopeTopLevel
	case checkpointScopeNested, "nested_skill", "nestedskill":
		return checkpointScopeNested
	default:
		return ""
	}
}

func shouldReturnOnStatus(status string, returnOn []string) bool {
	normalized := normalizeStatus(status)
	if normalized == "" {
		return false
	}
	for _, candidate := range returnOn {
		if normalized == normalizeStatus(candidate) {
			return true
		}
	}
	return false
}

func normalizeStatuses(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalizeStatus(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeStatus(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	return normalized
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
