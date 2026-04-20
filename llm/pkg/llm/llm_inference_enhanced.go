package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	f2 "github.com/colony-2/c2j/pkg/file"
	"github.com/colony-2/c2j/pkg/ops"
	llmadapters "github.com/colony-2/colony2/server/llm/adapters"
	"github.com/mitchellh/mapstructure"
	jsonschemav6 "github.com/santhosh-tekuri/jsonschema/v6"
)

// Note: All configuration is provided via LLMInferenceInput.

// LLMInferenceInput defines enhanced input (backward compatible)
type LLMInferenceInput struct {
	Provider      string            `yaml:"default_provider" json:"default_provider,omitempty" validate:"required,oneof=openai anthropic gemini"`
	Model         string            `yaml:"default_model" json:"default_model,omitempty" validate:"required"`
	APIKeys       map[string]string `yaml:"api_keys" json:"api_keys,omitempty"`
	Temperature   float64           `json:"temperature,omitempty" validate:"omitempty,gte=0,lte=2"`
	MaxTokens     int               `json:"max_tokens,omitempty" validate:"omitempty,gte=1"`
	TopP          float64           `json:"top_p,omitempty" validate:"omitempty,gte=0,lte=1"`
	StopSequences []string          `json:"stop_sequences,omitempty"`

	EnableSandbox   bool     `yaml:"enable_sandbox" json:"enable_sandbox,omitempty"`
	AllowedPaths    []string `yaml:"allowed_paths" json:"allowed_paths,omitempty"`
	RestrictedPaths []string `yaml:"restricted_paths" json:"restricted_paths,omitempty"`

	// File handling settings
	DefaultFileHandling string `yaml:"default_file_handling" json:"default_file_handling,omitempty"`
	MaxFileContextSize  int    `yaml:"max_file_context_size" json:"max_file_context_size,omitempty" validate:"omitempty,gte=0"`

	Prompt         string         `json:"prompt,omitempty" validate:"required_without=Files"`
	SystemPrompt   string         `json:"system_prompt,omitempty"`
	ResponseSchema JSONRawMessage `json:"response_schema,omitempty"`

	// Enhanced fields (new)
	Files               []f2.File              `json:"files,omitempty" validate:"required_without=Prompt"`
	FileHandling        string                 `json:"file_handling,omitempty" validate:"omitempty,oneof=native text_fallback hybrid"` // native, text_fallback, hybrid
	Tools               []ToolDefinition       `json:"tools,omitempty" validate:"omitempty,dive"`
	ExecuteTools        bool                   `json:"execute_tools,omitempty"`
	DefaultWorkingDir   string                 `yaml:"default_working_dir" json:"default_working_dir,omitempty" default:"{{ context.environment.worktree_path }}"`
	ToolWorkingDir      string                 `json:"tool_working_dir,omitempty" default:"{{ context.environment.worktree_path }}"`
	EnableToolExecution bool                   `yaml:"enable_tool_execution" json:"enable_tool_execution,omitempty"`
	MaxToolRounds       int                    `yaml:"max_tool_rounds" json:"max_tool_rounds,omitempty" validate:"omitempty,gte=0"`
	ContinueOnToolError bool                   `json:"continue_on_tool_error,omitempty"`
	ToolTimeout         string                 `json:"tool_timeout,omitempty"`
	Metadata            map[string]interface{} `json:"metadata,omitempty"`
}

// ToolDefinition defines a tool for LLM
type ToolDefinition struct {
	Name        string          `json:"name" validate:"required"`
	Description string          `json:"description" validate:"required"`
	Parameters  json.RawMessage `json:"parameters" validate:"required"`
}

// JSONRawMessage behaves like json.RawMessage but accepts arbitrary JSON/YAML objects during mapstructure decode.
type JSONRawMessage json.RawMessage

func (m *JSONRawMessage) DecodeFromMap(input any) error {
	switch v := input.(type) {
	case nil:
		*m = nil
		return nil
	case json.RawMessage:
		*m = JSONRawMessage(v)
		return nil
	case []byte:
		*m = JSONRawMessage(v)
		return nil
	case string:
		if !json.Valid([]byte(v)) {
			return fmt.Errorf("response_schema string is not valid JSON")
		}
		*m = JSONRawMessage([]byte(v))
		return nil
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("encode response_schema: %w", err)
		}
		*m = JSONRawMessage(encoded)
		return nil
	}
}

// Raw converts to standard json.RawMessage
func (m JSONRawMessage) Raw() json.RawMessage {
	return json.RawMessage(m)
}

// DecodeFromMap ensures parameters are preserved as JSON, even when empty objects are provided.
func (t *ToolDefinition) DecodeFromMap(input any) error {
	rawMap, ok := input.(map[string]interface{})
	if !ok {
		return fmt.Errorf("tool definition must be an object")
	}

	if raw, exists := rawMap["parameters"]; exists {
		switch v := raw.(type) {
		case json.RawMessage:
			rawMap["parameters"] = v
		case []byte:
			rawMap["parameters"] = json.RawMessage(v)
		case string:
			if !json.Valid([]byte(v)) {
				return fmt.Errorf("tool parameters string is not valid JSON")
			}
			rawMap["parameters"] = json.RawMessage(v)
		default:
			encoded, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("encode tool parameters: %w", err)
			}
			rawMap["parameters"] = json.RawMessage(encoded)
		}
	}

	type toolAlias ToolDefinition
	var decoded toolAlias
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName:     "json",
		Result:      &decoded,
		ErrorUnused: true,
	})
	if err != nil {
		return err
	}

	if err := decoder.Decode(rawMap); err != nil {
		return err
	}

	*t = ToolDefinition(decoded)
	return nil
}

// LLMInferenceOutput defines enhanced output (backward compatible)
type LLMInferenceOutput struct {
	// Standard fields (existing)
	Response     string `json:"response"`
	Model        string `json:"model"`
	FinishReason string `json:"finish_reason"`
	Usage        Usage  `json:"usage"`

	// Tool execution fields (new)
	ToolCalls           []ToolCallInfo `json:"tool_calls,omitempty"`
	ToolResults         []ToolResult   `json:"tool_results,omitempty"`
	ToolExecutionErrors []string       `json:"tool_execution_errors,omitempty"`
	ToolRoundsUsed      int            `json:"tool_rounds_used,omitempty"`

	// File operation tracking (new)
	FilesWritten []string `json:"files_written,omitempty"`
	FilesRead    []string `json:"files_read,omitempty"`
	FilesDeleted []string `json:"files_deleted,omitempty"`

	// Enhanced metadata
	ExecutionTime    int64                  `json:"execution_time_ms,omitempty"`
	ProviderMetadata map[string]interface{} `json:"provider_metadata,omitempty"`

	// Backward compatibility fields
	Telemetry LLMTelemetry `json:"telemetry,omitempty"`
}

// Usage tracks token usage
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ToolCallInfo describes a tool call
type ToolCallInfo struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
	Timestamp int64                  `json:"timestamp"`
}

// ToolResult describes the result of a tool execution
type ToolResult struct {
	ToolCallID string      `json:"tool_call_id"`
	ToolName   string      `json:"tool_name"`
	Success    bool        `json:"success"`
	Result     interface{} `json:"result,omitempty"`
	Error      string      `json:"error,omitempty"`
	Duration   int64       `json:"duration_ms"`
}

// EnhancedLLMInferenceActivity implements RegisterableOp
type EnhancedLLMInferenceActivity struct {
	registry     llmadapters.Registry
	toolExecutor *FileToolExecutor
	sandbox      *SecuritySandbox
}

type responseSchemaInfo struct {
	raw          json.RawMessage
	compiled     *jsonschemav6.Schema
	expectedType string
}

func (i responseSchemaInfo) hasSchema() bool {
	return len(i.raw) > 0
}

func (i responseSchemaInfo) expectedOrDefault() string {
	if strings.TrimSpace(i.expectedType) == "" {
		return "structured JSON value"
	}
	return i.expectedType
}

// NewEnhancedLLMInferenceActivity constructs a new activity instance for tests and registration.
func NewEnhancedLLMInferenceActivity() *EnhancedLLMInferenceActivity {
	return &EnhancedLLMInferenceActivity{}
}

func GetEnhancedOp() ops.RegisterableOp {
	e := &EnhancedLLMInferenceActivity{}
	return ops.NewActivityMappedOpV2[LLMInferenceInput, LLMInferenceOutput](
		ops.OpMetadata{
			Type:           "llm_inference2",
			Description:    "Executes LLM inference with various providers (OpenAI, Anthropic, Gemini)",
			Version:        "1.0.0",
			DefaultTimeout: 5 * time.Minute,
		},
		e.Execute)
}

// GetMetadata returns activity metadata
func (a *EnhancedLLMInferenceActivity) GetMetadata() ops.OpMetadata {
	return ops.OpMetadata{
		Type:        "llm_inference", // Same type for backward compatibility
		Description: "Enhanced LLM inference with file and tool support",
		Version:     "2.0.0",
	}
}

// Execute runs the enhanced LLM inference activity (input carries all config)
func (a *EnhancedLLMInferenceActivity) Execute(
	_ ops.OpDependencies,
	ctx context.Context,
	input LLMInferenceInput,
) (LLMInferenceOutput, error) {

	startTime := time.Now()

	// Validate input
	if err := a.validateInput(input); err != nil {
		return LLMInferenceOutput{}, fmt.Errorf("validation failed: %w", err)
	}

	// Normalize and compile response schema (if provided) for both provider hints and runtime validation
	schemaInfo, err := a.normalizeResponseSchema(input.ResponseSchema)
	if err != nil {
		return LLMInferenceOutput{}, err
	}
	if schemaInfo.hasSchema() {
		input.ResponseSchema = JSONRawMessage(schemaInfo.raw)
	}

	// Initialize components
	if err := a.initializeComponents(input); err != nil {
		return LLMInferenceOutput{}, fmt.Errorf("initialization failed: %w", err)
	}

	// Acquire adapter for provider (use injected registry when present, otherwise construct locally)
	adapter, err := a.getAdapter(input)
	if err != nil {
		return LLMInferenceOutput{}, fmt.Errorf("failed to get adapter: %w", err)
	}

	// Execute based on capabilities
	var output LLMInferenceOutput

	if len(input.Files) > 0 && len(input.Tools) > 0 {
		output, err = a.executeWithFilesAndTools(ctx, adapter, input, schemaInfo)
	} else if len(input.Files) > 0 {
		output, err = a.executeWithFiles(ctx, adapter, input, schemaInfo)
	} else if len(input.Tools) > 0 {
		output, err = a.executeWithTools(ctx, adapter, input, schemaInfo)
	} else {
		output, err = a.executeBasic(ctx, adapter, input, schemaInfo)
	}

	if err != nil {
		return output, err
	}

	output.ExecutionTime = time.Since(startTime).Milliseconds()

	// Add backward compatibility telemetry
	output.Telemetry = LLMTelemetry{
		PromptTokens:     output.Usage.PromptTokens,
		CompletionTokens: output.Usage.CompletionTokens,
		TotalTokens:      output.Usage.TotalTokens,
	}

	return output, nil
}

// validateInput validates the input parameters
func (a *EnhancedLLMInferenceActivity) validateInput(input LLMInferenceInput) error {
	// Validate provider
	if input.Provider == "" {
		return fmt.Errorf("provider is required")
	}

	// Validate model
	if input.Model == "" {
		return fmt.Errorf("model is required")
	}

	// Validate prompt or files
	if input.Prompt == "" && len(input.Files) == 0 {
		return fmt.Errorf("prompt or files required")
	}

	// Validate tool configuration
	if input.ExecuteTools {
		if input.ToolWorkingDir == "" {
			input.ToolWorkingDir = "."
		}

		if !input.EnableToolExecution {
			return fmt.Errorf("tool execution is disabled in configuration")
		}

		if len(input.Tools) == 0 {
			return fmt.Errorf("tools are required when execute_tools is true")
		}

		for _, tool := range input.Tools {
			if err := a.validateToolDefinition(tool); err != nil {
				return fmt.Errorf("invalid tool %s: %w", tool.Name, err)
			}
		}
	}

	// Validate file handling
	if len(input.Files) > 0 && input.MaxFileContextSize > 0 {
		totalSize := 0
		for _, file := range input.Files {
			totalSize += len(file.Content)
		}

		if totalSize > input.MaxFileContextSize {
			return fmt.Errorf("total file size %d exceeds limit %d", totalSize, input.MaxFileContextSize)
		}
	}

	return nil
}

// validateToolDefinition validates a tool definition
func (a *EnhancedLLMInferenceActivity) validateToolDefinition(tool ToolDefinition) error {
	if tool.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if tool.Description == "" {
		return fmt.Errorf("tool description is required")
	}
	if len(tool.Parameters) == 0 {
		return fmt.Errorf("tool parameters are required")
	}

	// Validate JSON schema
	var schema map[string]interface{}
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		return fmt.Errorf("invalid tool parameters schema: %w", err)
	}

	return nil
}

// initializeComponents initializes tool executor and sandbox
func (a *EnhancedLLMInferenceActivity) initializeComponents(input LLMInferenceInput) error {
	// Initialize sandbox if enabled
	if input.EnableSandbox {
		a.sandbox = NewSecuritySandbox(SandboxConfig{
			AllowedPaths:    input.AllowedPaths,
			RestrictedPaths: input.RestrictedPaths,
		})
	}

	// Initialize tool executor if tools are provided
	if input.ExecuteTools && len(input.Tools) > 0 {
		a.toolExecutor = NewFileToolExecutor(input.ToolWorkingDir, a.sandbox)
	}

	return nil
}

// getAdapter returns an adapter for the requested provider.
// If a registry is injected (tests), it is used; otherwise we construct locally.
func (a *EnhancedLLMInferenceActivity) getAdapter(input LLMInferenceInput) (llmadapters.Adapter, error) {
	if a.registry != nil {
		return a.registry.Get(input.Provider)
	}

	// Build an adapter directly using input-provided API keys (falling back to env inside constructors).
	apiKey := ""
	if input.APIKeys != nil {
		apiKey = input.APIKeys[input.Provider]
	}

	var (
		adapter llmadapters.Adapter
		err     error
	)

	switch input.Provider {
	case "openai":
		adapter, err = llmadapters.NewOpenAIAdapter(apiKey)
	case "anthropic":
		adapter, err = llmadapters.NewAnthropicAdapter(apiKey)
	case "gemini":
		adapter, err = llmadapters.NewGeminiAdapter(apiKey)
	default:
		err = fmt.Errorf("unsupported provider: %s", input.Provider)
	}

	return adapter, err
}

func (a *EnhancedLLMInferenceActivity) normalizeResponseSchema(raw JSONRawMessage) (responseSchemaInfo, error) {
	if len(raw) == 0 {
		return responseSchemaInfo{}, nil
	}

	var schemaValue interface{}
	if err := json.Unmarshal(raw.Raw(), &schemaValue); err != nil {
		return responseSchemaInfo{}, fmt.Errorf("response_schema is not valid JSON: %w", err)
	}

	if arr, ok := schemaValue.([]interface{}); ok && len(arr) == 1 {
		schemaValue = arr[0]
	}

	normalized, err := json.Marshal(schemaValue)
	if err != nil {
		return responseSchemaInfo{}, fmt.Errorf("failed to normalize response_schema: %w", err)
	}

	compiled, err := compileSchema(schemaValue)
	if err != nil {
		return responseSchemaInfo{}, fmt.Errorf("response_schema failed to compile: %w", err)
	}

	return responseSchemaInfo{
		raw:          normalized,
		compiled:     compiled,
		expectedType: describeSchemaType(schemaValue),
	}, nil
}

func compileSchema(doc interface{}) (*jsonschemav6.Schema, error) {
	comp := jsonschemav6.NewCompiler()
	comp.DefaultDraft(jsonschemav6.Draft2020)
	if err := comp.AddResource("inmem://response-schema.json", doc); err != nil {
		return nil, fmt.Errorf("add schema resource: %w", err)
	}

	compiled, err := comp.Compile("inmem://response-schema.json")
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}

	return compiled, nil
}

func describeSchemaType(v interface{}) string {
	if m, ok := v.(map[string]interface{}); ok {
		if t, ok := m["type"]; ok {
			switch tt := t.(type) {
			case string:
				return tt
			case []interface{}:
				var parts []string
				for _, el := range tt {
					if s, ok := el.(string); ok {
						parts = append(parts, s)
					}
				}
				if len(parts) > 0 {
					return strings.Join(parts, "|")
				}
			}
		}
	}
	return ""
}
