package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	jsonschemav6 "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	defaultSkillRunOutputPath  = "skill-output/result.json"
	skillRunContractRelPath    = "codex-run-skill/contract.json"
	skillRunDiagnosticsDir     = "_skill_run"
	outputSourceArtifact       = "artifact"
	outputSourceSummary        = "assistantSummary"
	outputValidationIncomplete = "incomplete"
	outputValidationFail       = "fail"
	outputValidationWarn       = "warn"
)

// SkillRunInput defines the codex/run_skill extension op input.
type SkillRunInput struct {
	Skill              string                 `json:"skill"`
	Skills             []string               `json:"skills,omitempty"`
	Input              map[string]interface{} `json:"input,omitempty"`
	Prompt             string                 `json:"prompt,omitempty"`
	SessionID          string                 `json:"sessionId,omitempty"`
	Model              string                 `json:"model,omitempty"`
	ReturnOn           []string               `json:"return_on,omitempty"`
	StatusContract     StatusContractRef      `json:"status_contract,omitempty"`
	Output             SkillRunOutputSpec     `json:"output,omitempty"`
	IdleTimeout        string                 `json:"idle_timeout,omitempty" default:"5m"`
	WorkdirPath        string                 `json:"workdir_path,omitempty" default:"{{ context.environment.op.workdir }}"`
	WorktreePath       string                 `json:"worktree_path" default:"{{ context.environment.op.worktree_path }}"`
	ArtifactInboxPath  string                 `json:"artifact_inbox_path,omitempty" default:"{{ context.environment.op.inbox }}"`
	ArtifactOutboxPath string                 `json:"artifact_outbox_path,omitempty" default:"{{ context.environment.op.outbox }}"`
}

type SkillRunOutputSpec struct {
	From                   string                         `json:"from,omitempty"`
	Path                   string                         `json:"path,omitempty"`
	Format                 string                         `json:"format,omitempty"`
	Schema                 json.RawMessage                `json:"schema,omitempty"`
	RequiredWhenIncomplete bool                           `json:"required_when_incomplete,omitempty"`
	Validation             SkillRunOutputValidationConfig `json:"validation,omitempty"`
}

type SkillRunOutputValidationConfig struct {
	OnError string                     `json:"on_error,omitempty"`
	Repair  SkillRunOutputRepairConfig `json:"repair,omitempty"`
}

type SkillRunOutputRepairConfig struct {
	Enabled     *bool `json:"enabled,omitempty"`
	MaxAttempts int   `json:"max_attempts,omitempty"`
}

// SkillRunOutput preserves codex outputs and adds structured artifact validation.
type SkillRunOutput struct {
	Status              string       `json:"status"`
	SessionID           string       `json:"sessionId"`
	Outcome             ExecOutcome  `json:"outcome"`
	AssistantSummary    string       `json:"assistantSummary"`
	IncompleteReason    string       `json:"incompleteReason"`
	IncompleteCategory  string       `json:"incompleteCategory"`
	PendingDependencies []Dependency `json:"pendingDependencies"`
	SkillsInstalled     []string     `json:"skills_installed,omitempty"`

	Skill                 string              `json:"skill"`
	RawSummary            string              `json:"raw_summary"`
	OutputSource          string              `json:"output_source"`
	OutputPath            string              `json:"output_path"`
	RawOutput             string              `json:"raw_output"`
	ParsedOutput          interface{}         `json:"parsed_output"`
	OutputSchemaValid     bool                `json:"output_schema_valid"`
	OutputSchemaErrors    []string            `json:"output_schema_errors"`
	OutputRepairAttempts  int                 `json:"output_repair_attempts"`
	StatusContractPath    string              `json:"status_contract_path"`
	StatusContractPresent bool                `json:"status_contract_present"`
	StatusContractValid   bool                `json:"status_contract_valid"`
	StatusContractJSON    interface{}         `json:"status_contract_json"`
	StatusContractErrors  []string            `json:"status_contract_errors"`
	Diagnostics           SkillRunDiagnostics `json:"diagnostics"`
}

type SkillRunDiagnostics struct {
	Skill                 string   `json:"skill"`
	SkillsInstalled       []string `json:"skills_installed,omitempty"`
	StatusContractPath    string   `json:"status_contract_path,omitempty"`
	StatusContractPresent bool     `json:"status_contract_present"`
	StatusContractValid   bool     `json:"status_contract_valid"`
	StatusContractErrors  []string `json:"status_contract_errors"`
	OutputSource          string   `json:"output_source"`
	OutputPath            string   `json:"output_path,omitempty"`
	OutputSchemaValid     bool     `json:"output_schema_valid"`
	OutputSchemaErrors    []string `json:"output_schema_errors"`
	OutputRepairAttempts  int      `json:"output_repair_attempts"`
}

type normalizedSkillRunOutput struct {
	Source                 string
	Path                   string
	Format                 string
	Schema                 json.RawMessage
	RequiredWhenIncomplete bool
	OnError                string
	RepairEnabled          bool
	RepairMaxAttempts      int
}

type skillRunContractFile struct {
	Skill              string                 `json:"skill"`
	ArtifactInboxPath  string                 `json:"artifact_inbox_path"`
	ArtifactOutboxPath string                 `json:"artifact_outbox_path"`
	StatusContract     StatusContractRef      `json:"status_contract,omitempty"`
	Output             skillRunContractOutput `json:"output"`
	Input              map[string]interface{} `json:"input,omitempty"`
	Prompt             string                 `json:"prompt,omitempty"`
}

type skillRunContractOutput struct {
	From   string      `json:"from"`
	Path   string      `json:"path,omitempty"`
	Format string      `json:"format"`
	Schema interface{} `json:"schema,omitempty"`
}

type skillRunOutputValidation struct {
	source string
	path   string
	raw    string
	parsed interface{}
	valid  bool
	errors []string
}

type skillRunStatusValidation struct {
	path    string
	present bool
	valid   bool
	parsed  interface{}
	errors  []string
}

// RunSkill runs one requested Codex skill with a generated invocation contract.
func RunSkill(ctx context.Context, input SkillRunInput) (SkillRunOutput, error) {
	skill := strings.TrimSpace(input.Skill)
	if skill == "" {
		return SkillRunOutput{}, fmt.Errorf("skill is required")
	}

	execInput := execInputForSkillRun(input)
	paths, err := normalizeExecRunPaths(execInput)
	if err != nil {
		return SkillRunOutput{}, err
	}

	configuredSkillDirs, skillsInstalled, skillSourcesCleanup, err := prepareConfiguredSkillSources(ctx, execInput, paths.Workdir)
	if err != nil {
		return SkillRunOutput{}, err
	}
	if skillSourcesCleanup != nil {
		defer func() {
			_ = skillSourcesCleanup()
		}()
	}

	if err := ensureRequestedSkillAvailable(skill, paths.Worktree, configuredSkillDirs); err != nil {
		return SkillRunOutput{}, err
	}

	outputSpec, err := normalizeSkillRunOutput(input.Output)
	if err != nil {
		return SkillRunOutput{}, err
	}
	contractDoc := buildSkillRunContract(input, skill, paths, outputSpec)
	contractPath := filepath.Join(paths.Workdir, filepath.FromSlash(skillRunContractRelPath))
	if err := writeJSONFile(contractPath, contractDoc); err != nil {
		return SkillRunOutput{}, err
	}

	prompt := renderRunSkillPrompt(skill, contractPath, paths, input, outputSpec)
	execOutput, err := runCodexActivityPrepared(ctx, execInput, prompt, paths, configuredSkillDirs, skillsInstalled)
	output := newSkillRunOutput(skill, execOutput, outputSpec)
	if err != nil {
		return output, err
	}

	statusValidation := validateSkillRunStatusContract(paths.Outbox, input.StatusContract.Path)
	applyStatusValidation(&output, statusValidation)

	outputValidation := validateSkillRunOutput(paths.Outbox, execOutput.AssistantSummary, outputSpec, shouldSkipOutputRequirement(execOutput, outputSpec))
	repairAttempts := 0
	for shouldRepairSkillRunOutput(outputValidation, outputSpec, execOutput) && repairAttempts < outputSpec.RepairMaxAttempts {
		repairAttempts++
		repairPrompt := renderRunSkillRepairPrompt(skill, outputSpec, outputValidation)
		repairInput := execInput
		repairInput.SessionID = execOutput.SessionID
		repairExecOutput, repairErr := runCodexActivityPrepared(ctx, repairInput, repairPrompt, paths, configuredSkillDirs, skillsInstalled)
		if repairErr != nil {
			output.OutputRepairAttempts = repairAttempts
			output.OutputSchemaErrors = append(output.OutputSchemaErrors, fmt.Sprintf("repair attempt %d failed: %v", repairAttempts, repairErr))
			applyDiagnostics(&output)
			_ = writeSkillRunArtifacts(paths.Outbox, contractDoc, output)
			return output, repairErr
		}
		execOutput = repairExecOutput
		output = newSkillRunOutput(skill, execOutput, outputSpec)
		applyStatusValidation(&output, validateSkillRunStatusContract(paths.Outbox, input.StatusContract.Path))
		outputValidation = validateSkillRunOutput(paths.Outbox, execOutput.AssistantSummary, outputSpec, shouldSkipOutputRequirement(execOutput, outputSpec))
	}

	applyOutputValidation(&output, outputValidation, outputSpec, repairAttempts)
	applyDiagnostics(&output)
	if err := writeSkillRunArtifacts(paths.Outbox, contractDoc, output); err != nil {
		return output, err
	}
	if !output.OutputSchemaValid && outputSpec.OnError == outputValidationFail {
		return output, fmt.Errorf("output schema validation failed: %s", strings.Join(output.OutputSchemaErrors, "; "))
	}
	return output, nil
}

func execInputForSkillRun(input SkillRunInput) ExecOpInput {
	return ExecOpInput{
		SessionID:          input.SessionID,
		Model:              input.Model,
		Skills:             copyStrings(input.Skills),
		ReturnOn:           copyStrings(input.ReturnOn),
		StatusContract:     input.StatusContract,
		IdleTimeout:        input.IdleTimeout,
		WorkdirPath:        input.WorkdirPath,
		WorktreePath:       input.WorktreePath,
		ArtifactInboxPath:  input.ArtifactInboxPath,
		ArtifactOutboxPath: input.ArtifactOutboxPath,
	}
}

func normalizeSkillRunOutput(spec SkillRunOutputSpec) (normalizedSkillRunOutput, error) {
	source := strings.TrimSpace(spec.From)
	if source == "" {
		source = outputSourceArtifact
	}
	switch strings.ToLower(source) {
	case outputSourceArtifact:
		source = outputSourceArtifact
	case strings.ToLower(outputSourceSummary):
		source = outputSourceSummary
	default:
		return normalizedSkillRunOutput{}, fmt.Errorf("output.from must be %q or %q", outputSourceArtifact, outputSourceSummary)
	}

	format := strings.ToLower(strings.TrimSpace(spec.Format))
	if format == "" {
		format = "json"
	}
	if format != "json" {
		return normalizedSkillRunOutput{}, fmt.Errorf("output.format %q is not supported", spec.Format)
	}

	path := strings.TrimSpace(spec.Path)
	if source == outputSourceArtifact && path == "" {
		path = defaultSkillRunOutputPath
	}

	onError := strings.ToLower(strings.TrimSpace(spec.Validation.OnError))
	if onError == "" {
		onError = outputValidationIncomplete
	}
	switch onError {
	case outputValidationIncomplete, outputValidationFail, outputValidationWarn:
	default:
		return normalizedSkillRunOutput{}, fmt.Errorf("output.validation.on_error must be %q, %q, or %q", outputValidationIncomplete, outputValidationFail, outputValidationWarn)
	}

	repairEnabled := source == outputSourceArtifact
	if spec.Validation.Repair.Enabled != nil {
		repairEnabled = *spec.Validation.Repair.Enabled
	}
	repairMaxAttempts := spec.Validation.Repair.MaxAttempts
	if repairEnabled && repairMaxAttempts <= 0 {
		repairMaxAttempts = 1
	}
	if !repairEnabled {
		repairMaxAttempts = 0
	}

	return normalizedSkillRunOutput{
		Source:                 source,
		Path:                   path,
		Format:                 format,
		Schema:                 append(json.RawMessage(nil), spec.Schema...),
		RequiredWhenIncomplete: spec.RequiredWhenIncomplete,
		OnError:                onError,
		RepairEnabled:          repairEnabled,
		RepairMaxAttempts:      repairMaxAttempts,
	}, nil
}

func buildSkillRunContract(input SkillRunInput, skill string, paths execRunPaths, output normalizedSkillRunOutput) skillRunContractFile {
	return skillRunContractFile{
		Skill:              skill,
		ArtifactInboxPath:  paths.Inbox,
		ArtifactOutboxPath: paths.Outbox,
		StatusContract:     input.StatusContract,
		Output: skillRunContractOutput{
			From:   output.Source,
			Path:   output.Path,
			Format: output.Format,
			Schema: decodeRawJSONForContract(output.Schema),
		},
		Input:  input.Input,
		Prompt: strings.TrimSpace(input.Prompt),
	}
}

func renderRunSkillPrompt(skill string, contractPath string, paths execRunPaths, input SkillRunInput, output normalizedSkillRunOutput) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Run the Codex skill `%s` for this invocation.\n\n", skill))
	builder.WriteString("Skill invocation contract:\n")
	builder.WriteString(fmt.Sprintf("- Use this top-level skill: `%s`.\n", skill))
	builder.WriteString(fmt.Sprintf("- Invocation contract file: %s.\n", contractPath))
	builder.WriteString(fmt.Sprintf("- Artifact inbox path: %s.\n", paths.Inbox))
	builder.WriteString(fmt.Sprintf("- Artifact outbox path: %s.\n", paths.Outbox))
	if strings.TrimSpace(input.StatusContract.Path) != "" {
		builder.WriteString(fmt.Sprintf("- Write authoritative status JSON to outbox artifact path: %s.\n", input.StatusContract.Path))
	}
	if output.Source == outputSourceArtifact {
		builder.WriteString(fmt.Sprintf("- Write the primary structured output as %s to outbox artifact path: %s.\n", output.Format, output.Path))
		builder.WriteString("- Do not put the primary structured output JSON in assistantSummary; assistantSummary should be a concise human summary.\n")
	} else {
		builder.WriteString("- Put the primary structured output JSON in assistantSummary.\n")
	}
	builder.WriteString("\nStructured input JSON:\n")
	builder.WriteString(renderJSONBlock(input.Input))
	if supplemental := strings.TrimSpace(input.Prompt); supplemental != "" {
		builder.WriteString("\nSupplemental request:\n")
		builder.WriteString(supplemental)
		builder.WriteString("\n")
	}
	return builder.String()
}

func renderRunSkillRepairPrompt(skill string, output normalizedSkillRunOutput, validation skillRunOutputValidation) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Resume the `%s` skill invocation and repair only the declared primary output artifact.\n\n", skill))
	builder.WriteString(fmt.Sprintf("Output artifact path: %s\n", output.Path))
	builder.WriteString("Validation errors:\n")
	for _, msg := range validation.errors {
		builder.WriteString(fmt.Sprintf("- %s\n", msg))
	}
	builder.WriteString("\nRewrite the output artifact so it is valid JSON and satisfies the configured schema. Return the normal c2ops Codex structured response after writing the artifact.\n")
	return builder.String()
}

func ensureRequestedSkillAvailable(skill string, worktree string, configuredSkillDirs []string) error {
	if strings.Contains(skill, "/") || strings.Contains(skill, "\\") || skill == "." || skill == ".." {
		return fmt.Errorf("skill %q is not a valid skill directory name", skill)
	}
	opts := Options{WorktreeRoot: worktree, ConfiguredSkillDirs: configuredSkillDirs}
	for _, root := range opts.skillSourceDirs() {
		candidate := filepath.Join(root, skill, "SKILL.md")
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return nil
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("stat requested skill %q: %w", candidate, err)
		}
	}
	return fmt.Errorf("requested skill %q was not found in configured skill sources", skill)
}

func newSkillRunOutput(skill string, execOutput ExecOpOutput, outputSpec normalizedSkillRunOutput) SkillRunOutput {
	return SkillRunOutput{
		Status:               execOutput.Status,
		SessionID:            execOutput.SessionID,
		Outcome:              execOutput.Outcome,
		AssistantSummary:     execOutput.AssistantSummary,
		IncompleteReason:     execOutput.IncompleteReason,
		IncompleteCategory:   execOutput.IncompleteCategory,
		PendingDependencies:  copyDependencies(execOutput.PendingDependencies),
		SkillsInstalled:      copyStrings(execOutput.SkillsInstalled),
		Skill:                skill,
		RawSummary:           execOutput.AssistantSummary,
		OutputSource:         outputSpec.Source,
		OutputPath:           outputSpec.Path,
		OutputSchemaErrors:   []string{},
		StatusContractPath:   execOutput.Outcome.Checkpoint.StatusArtifact,
		StatusContractErrors: []string{},
	}
}

func validateSkillRunOutput(outbox string, assistantSummary string, spec normalizedSkillRunOutput, skipRequired bool) skillRunOutputValidation {
	result := skillRunOutputValidation{
		source: spec.Source,
		path:   spec.Path,
		valid:  false,
		errors: []string{},
	}

	var raw []byte
	switch spec.Source {
	case outputSourceSummary:
		raw = []byte(strings.TrimSpace(assistantSummary))
	case outputSourceArtifact:
		resolved, err := resolveOutboxArtifactPath(outbox, spec.Path, "output artifact")
		if err != nil {
			result.errors = append(result.errors, err.Error())
			return result
		}
		payload, err := os.ReadFile(resolved)
		if err != nil {
			if skipRequired && os.IsNotExist(err) {
				result.valid = true
				return result
			}
			result.errors = append(result.errors, fmt.Sprintf("read output artifact %q: %v", spec.Path, err))
			return result
		}
		raw = payload
	}

	result.raw = string(raw)
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		result.errors = append(result.errors, "output JSON is empty")
		return result
	}
	var parsed interface{}
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		result.errors = append(result.errors, fmt.Sprintf("parse output JSON: %v", err))
		return result
	}
	result.parsed = parsed

	compiled, err := compileSkillRunSchema(spec.Schema)
	if err != nil {
		result.errors = append(result.errors, err.Error())
		return result
	}
	if compiled != nil {
		if err := compiled.Validate(parsed); err != nil {
			result.errors = append(result.errors, fmt.Sprintf("output does not match schema: %v", err))
			return result
		}
	}

	result.valid = true
	return result
}

func compileSkillRunSchema(raw json.RawMessage) (*jsonschemav6.Schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("output schema is not valid JSON: %w", err)
	}
	compiler := jsonschemav6.NewCompiler()
	compiler.DefaultDraft(jsonschemav6.Draft2020)
	if err := compiler.AddResource("inmem://skill-run-output-schema.json", doc); err != nil {
		return nil, fmt.Errorf("add output schema resource: %w", err)
	}
	compiled, err := compiler.Compile("inmem://skill-run-output-schema.json")
	if err != nil {
		return nil, fmt.Errorf("compile output schema: %w", err)
	}
	return compiled, nil
}

func validateSkillRunStatusContract(outbox string, statusPath string) skillRunStatusValidation {
	statusPath = strings.TrimSpace(statusPath)
	result := skillRunStatusValidation{
		path:   filepath.ToSlash(statusPath),
		errors: []string{},
	}
	if statusPath == "" {
		return result
	}
	resolved, err := resolveStatusContractPath(outbox, statusPath)
	if err != nil {
		result.errors = append(result.errors, err.Error())
		return result
	}
	payload, err := os.ReadFile(resolved)
	if err != nil {
		result.errors = append(result.errors, fmt.Sprintf("read status artifact %q: %v", statusPath, err))
		return result
	}
	result.present = true
	if err := json.Unmarshal(payload, &result.parsed); err != nil {
		result.errors = append(result.errors, fmt.Sprintf("parse status artifact %q: %v", statusPath, err))
		return result
	}
	if _, err := loadStatusContract(outbox, statusPath); err != nil {
		result.errors = append(result.errors, err.Error())
		return result
	}
	result.valid = true
	return result
}

func shouldRepairSkillRunOutput(validation skillRunOutputValidation, spec normalizedSkillRunOutput, execOutput ExecOpOutput) bool {
	if validation.valid || !spec.RepairEnabled || spec.Source != outputSourceArtifact {
		return false
	}
	if execOutput.Status == string(StatusIncomplete) && !spec.RequiredWhenIncomplete {
		return false
	}
	return true
}

func shouldSkipOutputRequirement(execOutput ExecOpOutput, spec normalizedSkillRunOutput) bool {
	return execOutput.Status == string(StatusIncomplete) && !spec.RequiredWhenIncomplete
}

func applyStatusValidation(output *SkillRunOutput, validation skillRunStatusValidation) {
	output.StatusContractPath = validation.path
	output.StatusContractPresent = validation.present
	output.StatusContractValid = validation.valid
	output.StatusContractJSON = validation.parsed
	output.StatusContractErrors = append([]string{}, validation.errors...)
}

func applyOutputValidation(output *SkillRunOutput, validation skillRunOutputValidation, spec normalizedSkillRunOutput, repairAttempts int) {
	output.OutputSource = validation.source
	output.OutputPath = validation.path
	output.RawOutput = validation.raw
	output.ParsedOutput = validation.parsed
	output.OutputSchemaValid = validation.valid
	output.OutputSchemaErrors = append([]string{}, validation.errors...)
	output.OutputRepairAttempts = repairAttempts
	if validation.valid {
		return
	}
	summary := strings.Join(validation.errors, "; ")
	if summary == "" {
		summary = "output schema validation failed"
	}
	if spec.OnError == outputValidationWarn {
		return
	}
	if spec.OnError == outputValidationFail {
		output.Status = string(StatusError)
		output.IncompleteCategory = "output_schema_validation"
		output.IncompleteReason = summary
		return
	}
	if output.Status != string(StatusError) {
		output.Status = string(StatusIncomplete)
	}
	output.IncompleteCategory = "output_schema_validation"
	output.IncompleteReason = summary
	output.Outcome.Checkpoint.Status = checkpointStatusBlocked
	output.Outcome.Checkpoint.ReturnTriggered = true
	output.Outcome.Checkpoint.ReturnReason = "output_schema_validation"
	output.Outcome.Checkpoint.ContractErrors = append(output.Outcome.Checkpoint.ContractErrors, validation.errors...)
	output.Outcome.Routing.NextAction = routingReturnToCheckpoint
}

func applyDiagnostics(output *SkillRunOutput) {
	output.Diagnostics = SkillRunDiagnostics{
		Skill:                 output.Skill,
		SkillsInstalled:       copyStrings(output.SkillsInstalled),
		StatusContractPath:    output.StatusContractPath,
		StatusContractPresent: output.StatusContractPresent,
		StatusContractValid:   output.StatusContractValid,
		StatusContractErrors:  append([]string{}, output.StatusContractErrors...),
		OutputSource:          output.OutputSource,
		OutputPath:            output.OutputPath,
		OutputSchemaValid:     output.OutputSchemaValid,
		OutputSchemaErrors:    append([]string{}, output.OutputSchemaErrors...),
		OutputRepairAttempts:  output.OutputRepairAttempts,
	}
}

func resolveOutboxArtifactPath(outboxPath string, configuredPath string, label string) (string, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		return "", fmt.Errorf("%s path is empty", label)
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
	if err := ensureDescendantPath(outboxPath, resolved, label); err != nil {
		return "", err
	}
	return resolved, nil
}

func renderJSONBlock(v interface{}) string {
	if v == nil {
		return "{}\n"
	}
	payload, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(payload) + "\n"
}

func decodeRawJSONForContract(raw json.RawMessage) interface{} {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return decoded
}

func writeSkillRunArtifacts(outbox string, contract skillRunContractFile, output SkillRunOutput) error {
	if strings.TrimSpace(outbox) == "" {
		return nil
	}
	if err := writeJSONFile(filepath.Join(outbox, skillRunDiagnosticsDir, "contract.json"), contract); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(outbox, skillRunDiagnosticsDir, "validation.json"), output.Diagnostics); err != nil {
		return err
	}
	return nil
}

func writeJSONFile(path string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %q: %w", path, err)
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON for %q: %w", path, err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	return nil
}
