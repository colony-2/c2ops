package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	llmadapters "github.com/colony-2/colony2/server/llm/adapters"
)

// LLMActivity represents the input for the  activity wrapper
type LLMActivity struct {
	// Core fields
	Prompt       string `json:"prompt"`
	SystemPrompt string `json:"systemPrompt,omitempty"`
	ModelName    string `json:"modelName"`
	AdapterName  string `json:"adapterName"`

	// Configuration
	Temperature   float64  `json:"temperature,omitempty"`
	MaxTokens     int      `json:"maxTokens,omitempty"`
	TopP          float64  `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`

	// For structured responses - provide JSON schema
	ResponseSchema json.RawMessage `json:"responseSchema,omitempty"`

	// Additional options
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// LLMActivityOutput wraps the task output
type LLMActivityOutput struct {
	Response     json.RawMessage `json:"response"`
	Telemetry    LLMTelemetry    `json:"telemetry"`
	Model        string          `json:"model"`
	FinishReason string          `json:"finishReason"`
}

// globalRegistry holds the adapter registry
var globalRegistry llmadapters.Registry

// InitializeRegistry sets up the LLM adapter registry
// This should be called during worker initialization
func InitializeRegistry() error {
	globalRegistry = llmadapters.NewRegistry()

	// Register OpenAI adapter if API key is available
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		adapter, err := llmadapters.NewOpenAIAdapter(apiKey)
		if err != nil {
			return fmt.Errorf("failed to create OpenAI adapter: %w", err)
		}
		if err := globalRegistry.Register("openai", adapter); err != nil {
			return fmt.Errorf("failed to register OpenAI adapter: %w", err)
		}
	}

	// Register Anthropic adapter if API key is available
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		adapter, err := llmadapters.NewAnthropicAdapter(apiKey)
		if err != nil {
			return fmt.Errorf("failed to create Anthropic adapter: %w", err)
		}
		if err := globalRegistry.Register("anthropic", adapter); err != nil {
			return fmt.Errorf("failed to register Anthropic adapter: %w", err)
		}
	}

	// Register Gemini adapter if API key is available
	if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
		adapter, err := llmadapters.NewGeminiAdapter(apiKey)
		if err != nil {
			return fmt.Errorf("failed to create Gemini adapter: %w", err)
		}
		if err := globalRegistry.Register("gemini", adapter); err != nil {
			return fmt.Errorf("failed to register Gemini adapter: %w", err)
		}
	}

	return nil
}

// GetRegistry returns the global registry
func GetRegistry() llmadapters.Registry {
	return globalRegistry
}

// SetRegistry allows setting a custom registry (useful for testing)
func SetRegistry(registry llmadapters.Registry) {
	globalRegistry = registry
}

// ExecuteLLMTask is the  activity function
func ExecuteLLMTask(ctx context.Context, input LLMActivity) (*LLMActivityOutput, error) {
	if globalRegistry == nil {
		return nil, fmt.Errorf("LLM registry not initialized. Call InitializeRegistry() first")
	}

	// Convert activity input to task input
	taskInput := LLMTaskInput{
		Prompt:        input.Prompt,
		SystemPrompt:  input.SystemPrompt,
		ModelName:     input.ModelName,
		AdapterName:   input.AdapterName,
		Temperature:   input.Temperature,
		MaxTokens:     input.MaxTokens,
		TopP:          input.TopP,
		StopSequences: input.StopSequences,
		Metadata:      input.Metadata,
	}

	// If response schema is provided, include it
	if len(input.ResponseSchema) > 0 {
		taskInput.ResponseStructureJSON = input.ResponseSchema
	}

	// Execute the task
	output, err := LLMTask(ctx, taskInput, globalRegistry)
	if err != nil {
		return nil, err
	}

	// Marshal the response to JSON, preserving structured JSON when present
	var responseJSON []byte
	if json.Valid([]byte(output.Response)) {
		responseJSON = []byte(output.Response)
	} else {
		var err error
		responseJSON, err = json.Marshal(output.Response)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal response: %w", err)
		}
	}

	return &LLMActivityOutput{
		Response:     responseJSON,
		Telemetry:    output.Telemetry,
		Model:        output.Model,
		FinishReason: output.FinishReason,
	}, nil
}
