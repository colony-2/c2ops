package llmadapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Adapter defines the interface for LLM providers
type Adapter interface {
	// Generate creates a completion for the given prompt
	Generate(ctx context.Context, prompt string, config Config) (Response, error)
	
	// GenerateWithTools creates a completion with tool/function calling support
	GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error)
	
	// StreamGenerate creates a streaming completion for the given prompt
	StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error)
}

// Config defines the configuration for LLM generation
type Config struct {
	Model          string                 `json:"model"`
	Temperature    float64                `json:"temperature,omitempty"`
	MaxTokens      int                    `json:"max_tokens,omitempty"`
	SystemPrompt   string                 `json:"system_prompt,omitempty"`
	ResponseFormat string                 `json:"response_format,omitempty"` // "text" or "json"
	ResponseSchema json.RawMessage        `json:"response_schema,omitempty"`  // JSON Schema for structured output
	TopP           float64                `json:"top_p,omitempty"`
	StopSequences  []string               `json:"stop_sequences,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// Response represents the LLM generation response
type Response struct {
	Content      string         `json:"content"`
	ToolCalls    []ToolCall     `json:"tool_calls,omitempty"`
	Usage        Usage          `json:"usage"`
	FinishReason string         `json:"finish_reason"`
	Model        string         `json:"model"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// Token represents a single token in a streaming response
type Token struct {
	Content string `json:"content"`
	Error   error  `json:"error,omitempty"`
}

// Usage tracks token usage for billing purposes
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Tool represents a tool/function that can be called by the LLM
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// ToolCall represents a tool call request from the LLM
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Registry manages multiple LLM adapters
type Registry interface {
	// Register adds a new adapter to the registry
	Register(name string, adapter Adapter) error
	
	// Get retrieves an adapter by name
	Get(name string) (Adapter, error)
	
	// List returns all registered adapter names
	List() []string
}

// DefaultRegistry implements the Registry interface
type DefaultRegistry struct {
	adapters map[string]Adapter
}

// NewRegistry creates a new adapter registry
func NewRegistry() Registry {
	return &DefaultRegistry{
		adapters: make(map[string]Adapter),
	}
}

// Register adds a new adapter to the registry
func (r *DefaultRegistry) Register(name string, adapter Adapter) error {
	if name == "" {
		return errors.New("adapter name cannot be empty")
	}
	if adapter == nil {
		return errors.New("adapter cannot be nil")
	}
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("adapter with name %s already registered", name)
	}
	r.adapters[name] = adapter
	return nil
}

// Get retrieves an adapter by name
func (r *DefaultRegistry) Get(name string) (Adapter, error) {
	adapter, exists := r.adapters[name]
	if !exists {
		return nil, fmt.Errorf("adapter with name %s not found", name)
	}
	return adapter, nil
}

// List returns all registered adapter names
func (r *DefaultRegistry) List() []string {
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	return names
}

// Common errors
var (
	ErrInvalidConfig      = errors.New("invalid configuration")
	ErrAPIKeyMissing      = errors.New("API key is missing")
	ErrModelNotSupported  = errors.New("model not supported")
	ErrRateLimitExceeded  = errors.New("rate limit exceeded")
	ErrContextLengthExceeded = errors.New("context length exceeded")
	ErrInvalidResponse    = errors.New("invalid response from API")
)