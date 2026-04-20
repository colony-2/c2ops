package llmadapters

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRegistry(t *testing.T) {
	registry := NewRegistry()
	assert.NotNil(t, registry)
	assert.Empty(t, registry.List())
}

func TestRegistry_Register(t *testing.T) {
	tests := []struct {
		name        string
		adapterName string
		adapter     Adapter
		wantErr     bool
		errMessage  string
	}{
		{
			name:        "valid registration",
			adapterName: "test-adapter",
			adapter:     &mockAdapter{},
			wantErr:     false,
		},
		{
			name:        "empty adapter name",
			adapterName: "",
			adapter:     &mockAdapter{},
			wantErr:     true,
			errMessage:  "adapter name cannot be empty",
		},
		{
			name:        "nil adapter",
			adapterName: "test-adapter",
			adapter:     nil,
			wantErr:     true,
			errMessage:  "adapter cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewRegistry()
			err := registry.Register(tt.adapterName, tt.adapter)
			
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMessage)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRegistry_RegisterDuplicate(t *testing.T) {
	registry := NewRegistry()
	adapter := &mockAdapter{}
	
	// First registration should succeed
	err := registry.Register("test-adapter", adapter)
	require.NoError(t, err)
	
	// Second registration with same name should fail
	err = registry.Register("test-adapter", adapter)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestRegistry_Get(t *testing.T) {
	registry := NewRegistry()
	adapter := &mockAdapter{name: "mock-adapter"}
	
	// Register adapter
	err := registry.Register("test-adapter", adapter)
	require.NoError(t, err)
	
	// Get existing adapter
	retrieved, err := registry.Get("test-adapter")
	assert.NoError(t, err)
	assert.Equal(t, adapter, retrieved)
	
	// Get non-existing adapter
	_, err = registry.Get("non-existent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRegistry_List(t *testing.T) {
	registry := NewRegistry()
	
	// Empty registry
	assert.Empty(t, registry.List())
	
	// Add adapters
	registry.Register("adapter1", &mockAdapter{})
	registry.Register("adapter2", &mockAdapter{})
	registry.Register("adapter3", &mockAdapter{})
	
	// Check list
	names := registry.List()
	assert.Len(t, names, 3)
	assert.Contains(t, names, "adapter1")
	assert.Contains(t, names, "adapter2")
	assert.Contains(t, names, "adapter3")
}

func TestConfig_Validation(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		valid  bool
	}{
		{
			name: "valid config",
			config: Config{
				Model:       "gpt-4",
				Temperature: 0.7,
				MaxTokens:   1000,
			},
			valid: true,
		},
		{
			name: "invalid temperature too high",
			config: Config{
				Model:       "gpt-4",
				Temperature: 2.5,
				MaxTokens:   1000,
			},
			valid: false,
		},
		{
			name: "invalid temperature negative",
			config: Config{
				Model:       "gpt-4",
				Temperature: -0.5,
				MaxTokens:   1000,
			},
			valid: false,
		},
		{
			name: "invalid response format",
			config: Config{
				Model:          "gpt-4",
				Temperature:    0.7,
				MaxTokens:      1000,
				ResponseFormat: "xml",
			},
			valid: false,
		},
		{
			name: "valid json response format",
			config: Config{
				Model:          "gpt-4",
				Temperature:    0.7,
				MaxTokens:      1000,
				ResponseFormat: "json",
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This tests the validation logic that adapters should implement
			err := validateTestConfig(tt.config)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestTool_JSONSchema(t *testing.T) {
	tool := Tool{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"param1": {"type": "string"},
				"param2": {"type": "number"}
			},
			"required": ["param1"]
		}`),
	}

	// Verify the JSON schema is valid
	var schema interface{}
	err := json.Unmarshal(tool.Parameters, &schema)
	assert.NoError(t, err)
}

func TestToolCall_Arguments(t *testing.T) {
	toolCall := ToolCall{
		ID:   "call_123",
		Name: "test_tool",
		Arguments: json.RawMessage(`{
			"param1": "value1",
			"param2": 42
		}`),
	}

	// Verify arguments can be unmarshaled
	var args map[string]interface{}
	err := json.Unmarshal(toolCall.Arguments, &args)
	assert.NoError(t, err)
	assert.Equal(t, "value1", args["param1"])
	assert.Equal(t, float64(42), args["param2"])
}

// Mock adapter for testing
type mockAdapter struct {
	name            string
	generateFunc    func(context.Context, string, Config) (Response, error)
	generateWithToolsFunc func(context.Context, string, []Tool, Config) (Response, error)
	streamFunc      func(context.Context, string, Config) (<-chan Token, error)
}

func (m *mockAdapter) Generate(ctx context.Context, prompt string, config Config) (Response, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, prompt, config)
	}
	return Response{
		Content: "Mock response to: " + prompt,
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
		FinishReason: "stop",
		Model:        config.Model,
	}, nil
}

func (m *mockAdapter) GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error) {
	if m.generateWithToolsFunc != nil {
		return m.generateWithToolsFunc(ctx, prompt, tools, config)
	}
	return Response{
		Content: "Mock response with tools",
		ToolCalls: []ToolCall{
			{
				ID:        "call_1",
				Name:      "mock_tool",
				Arguments: json.RawMessage(`{"param": "value"}`),
			},
		},
		Usage: Usage{
			PromptTokens:     20,
			CompletionTokens: 10,
			TotalTokens:      30,
		},
		FinishReason: "tool_calls",
		Model:        config.Model,
	}, nil
}

func (m *mockAdapter) StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error) {
	if m.streamFunc != nil {
		return m.streamFunc(ctx, prompt, config)
	}
	
	tokenChan := make(chan Token, 3)
	go func() {
		defer close(tokenChan)
		tokens := []string{"Mock ", "streaming ", "response"}
		for _, token := range tokens {
			select {
			case tokenChan <- Token{Content: token}:
			case <-ctx.Done():
				return
			}
		}
	}()
	
	return tokenChan, nil
}

// Helper function to validate config (simulates adapter validation)
func validateTestConfig(config Config) error {
	if config.Model == "" {
		return errors.New("model is required")
	}
	if config.Temperature < 0 || config.Temperature > 2 {
		return errors.New("temperature must be between 0 and 2")
	}
	if config.TopP < 0 || config.TopP > 1 {
		return errors.New("top_p must be between 0 and 1")
	}
	if config.ResponseFormat != "" && config.ResponseFormat != "text" && config.ResponseFormat != "json" {
		return errors.New("response_format must be 'text' or 'json'")
	}
	return nil
}

func TestErrors(t *testing.T) {
	// Test that all predefined errors are defined
	assert.NotNil(t, ErrInvalidConfig)
	assert.NotNil(t, ErrAPIKeyMissing)
	assert.NotNil(t, ErrModelNotSupported)
	assert.NotNil(t, ErrRateLimitExceeded)
	assert.NotNil(t, ErrContextLengthExceeded)
	assert.NotNil(t, ErrInvalidResponse)
	
	// Test error messages
	assert.Contains(t, ErrInvalidConfig.Error(), "invalid configuration")
	assert.Contains(t, ErrAPIKeyMissing.Error(), "API key is missing")
	assert.Contains(t, ErrModelNotSupported.Error(), "model not supported")
	assert.Contains(t, ErrRateLimitExceeded.Error(), "rate limit exceeded")
	assert.Contains(t, ErrContextLengthExceeded.Error(), "context length exceeded")
	assert.Contains(t, ErrInvalidResponse.Error(), "invalid response")
}