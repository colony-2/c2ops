package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	llmadapters "github.com/colony-2/colony2/server/llm/adapters"
	"github.com/stretchr/testify/require"
)

// mockAdapter implements llmadapters.Adapter for testing
type mockAdapter struct {
	generateFunc      func(context.Context, string, llmadapters.Config) (llmadapters.Response, error)
	generateWithTools func(context.Context, string, []llmadapters.Tool, llmadapters.Config) (llmadapters.Response, error)
	streamGenerate    func(context.Context, string, llmadapters.Config) (<-chan llmadapters.Token, error)
}

func (m *mockAdapter) Generate(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, prompt, config)
	}
	return llmadapters.Response{}, errors.New("not implemented")
}

func (m *mockAdapter) GenerateWithTools(ctx context.Context, prompt string, tools []llmadapters.Tool, config llmadapters.Config) (llmadapters.Response, error) {
	if m.generateWithTools != nil {
		return m.generateWithTools(ctx, prompt, tools, config)
	}
	return llmadapters.Response{}, errors.New("not implemented")
}

func (m *mockAdapter) StreamGenerate(ctx context.Context, prompt string, config llmadapters.Config) (<-chan llmadapters.Token, error) {
	if m.streamGenerate != nil {
		return m.streamGenerate(ctx, prompt, config)
	}
	return nil, errors.New("not implemented")
}

func TestLLMTask_TextResponse(t *testing.T) {
	// Create mock adapter
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			return llmadapters.Response{
				Content: "This is a test response",
				Usage: llmadapters.Usage{
					PromptTokens:     10,
					CompletionTokens: 5,
					TotalTokens:      15,
				},
				FinishReason: "stop",
				Model:        "gpt-3.5-turbo",
			}, nil
		},
	}

	// Create registry and register adapter
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}

	// Create input
	input := LLMTaskInput{
		Prompt:       "Test prompt",
		ModelName:    "gpt-3.5-turbo",
		AdapterName:  "test",
		SystemPrompt: "You are a helpful assistant",
		Temperature:  0.7,
		MaxTokens:    100,
	}

	// Execute task
	ctx := context.Background()
	output, err := LLMTask(ctx, input, registry)
	if err != nil {
		t.Fatalf("LLMTask failed: %v", err)
	}

	// Verify output
	if output.Response != "This is a test response" {
		t.Errorf("Expected response 'This is a test response', got %v", output.Response)
	}
	if output.Telemetry.TotalTokens != 15 {
		t.Errorf("Expected 15 total tokens, got %d", output.Telemetry.TotalTokens)
	}
	if output.Model != "gpt-3.5-turbo" {
		t.Errorf("Expected model 'gpt-3.5-turbo', got %s", output.Model)
	}
}

func TestLLMTask_StructuredResponse(t *testing.T) {
	// Define a test structure
	type TestResponse struct {
		Name   string `json:"name"`
		Age    int    `json:"age"`
		Active bool   `json:"active"`
	}

	// Create mock adapter that returns JSON
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			// Verify JSON format was requested
			if config.ResponseFormat != "json" {
				t.Error("Expected ResponseFormat to be 'json'")
			}

			return llmadapters.Response{
				Content: `{"name": "John Doe", "age": 30, "active": true}`,
				Usage: llmadapters.Usage{
					PromptTokens:     20,
					CompletionTokens: 10,
					TotalTokens:      30,
				},
				FinishReason: "stop",
				Model:        "gpt-3.5-turbo",
			}, nil
		},
	}

	// Create registry and register adapter
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}

	// Create response structure
	var responseStruct TestResponse

	// Create input with structure
	input := LLMTaskInput{
		Prompt:            "Generate a user object",
		ModelName:         "gpt-3.5-turbo",
		AdapterName:       "test",
		ResponseStructure: &responseStruct,
	}

	// Execute task
	ctx := context.Background()
	output, err := LLMTask(ctx, input, registry)
	if err != nil {
		t.Fatalf("LLMTask failed: %v", err)
	}

	// Verify structured response
	var resp TestResponse
	require.NoError(t, json.Unmarshal([]byte(output.Response), &resp))
	if resp.Name != "John Doe" {
		t.Errorf("Expected name 'John Doe', got %s", resp.Name)
	}
	if resp.Age != 30 {
		t.Errorf("Expected age 30, got %d", resp.Age)
	}
	if !resp.Active {
		t.Error("Expected active to be true")
	}
}

func TestLLMTask_JSONSchemaResponse(t *testing.T) {
	// Create mock adapter
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			return llmadapters.Response{
				Content: `{"items": ["apple", "banana"], "count": 2}`,
				Usage: llmadapters.Usage{
					PromptTokens:     15,
					CompletionTokens: 8,
					TotalTokens:      23,
				},
				FinishReason: "stop",
				Model:        "gpt-3.5-turbo",
			}, nil
		},
	}

	// Create registry and register adapter
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}

	// Create JSON schema
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"items": {"type": "array", "items": {"type": "string"}},
			"count": {"type": "integer"}
		},
		"required": ["items", "count"]
	}`)

	// Create input with JSON schema
	input := LLMTaskInput{
		Prompt:                "Generate a list of fruits",
		ModelName:             "gpt-3.5-turbo",
		AdapterName:           "test",
		ResponseStructureJSON: schema,
	}

	// Execute task
	ctx := context.Background()
	output, err := LLMTask(ctx, input, registry)
	if err != nil {
		t.Fatalf("LLMTask failed: %v", err)
	}

	// Verify generic JSON response
	var respMap map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(output.Response), &respMap))

	items, ok := respMap["items"].([]interface{})
	if !ok || len(items) != 2 {
		t.Error("Expected items array with 2 elements")
	}

	count, ok := respMap["count"].(float64) // JSON numbers are float64
	if !ok || count != 2 {
		t.Error("Expected count to be 2")
	}
}

func TestLLMTask_CostCalculation(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		usage    llmadapters.Usage
		expected float64
	}{
		{
			name:  "GPT-3.5 Turbo",
			model: "gpt-3.5-turbo",
			usage: llmadapters.Usage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
			expected: 0.0005 + 0.00075, // $0.00125
		},
		{
			name:  "GPT-4",
			model: "gpt-4",
			usage: llmadapters.Usage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
			expected: 0.03 + 0.03, // $0.06
		},
		{
			name:  "Claude 3 Haiku",
			model: "claude-3-haiku",
			usage: llmadapters.Usage{
				PromptTokens:     2000,
				CompletionTokens: 1000,
				TotalTokens:      3000,
			},
			expected: 0.0005 + 0.00125, // $0.00175
		},
		{
			name:  "Unknown Model",
			model: "unknown-model",
			usage: llmadapters.Usage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
			expected: 0, // No pricing data
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := calculateCost(tt.model, tt.usage)
			if cost != tt.expected {
				t.Errorf("Expected cost %f, got %f", tt.expected, cost)
			}
		})
	}
}

func TestLLMTask_ValidationErrors(t *testing.T) {
	registry := llmadapters.NewRegistry()
	ctx := context.Background()

	tests := []struct {
		name  string
		input LLMTaskInput
		error string
	}{
		{
			name: "Empty prompt",
			input: LLMTaskInput{
				ModelName:   "gpt-3.5-turbo",
				AdapterName: "test",
			},
			error: "prompt cannot be empty",
		},
		{
			name: "Empty model name",
			input: LLMTaskInput{
				Prompt:      "Test",
				AdapterName: "test",
			},
			error: "model name cannot be empty",
		},
		{
			name: "Empty adapter name",
			input: LLMTaskInput{
				Prompt:    "Test",
				ModelName: "gpt-3.5-turbo",
			},
			error: "adapter name cannot be empty",
		},
		{
			name: "Adapter not found",
			input: LLMTaskInput{
				Prompt:      "Test",
				ModelName:   "gpt-3.5-turbo",
				AdapterName: "nonexistent",
			},
			error: "failed to get adapter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LLMTask(ctx, tt.input, registry)
			if err == nil {
				t.Error("Expected error but got none")
			}
			if err != nil && !contains(err.Error(), tt.error) {
				t.Errorf("Expected error containing '%s', got '%s'", tt.error, err.Error())
			}
		})
	}
}

func TestGenerateJSONSchema(t *testing.T) {
	type TestStruct struct {
		Name     string                 `json:"name"`
		Age      int                    `json:"age"`
		Email    string                 `json:"email,omitempty"`
		Tags     []string               `json:"tags"`
		Active   bool                   `json:"active"`
		Metadata map[string]interface{} `json:"metadata,omitempty"`
		ignored  string                 // No JSON tag
		Ignored2 string                 `json:"-"` // Explicitly ignored
	}

	schema, err := generateJSONSchema(&TestStruct{})
	if err != nil {
		t.Fatalf("Failed to generate schema: %v", err)
	}

	var schemaMap map[string]interface{}
	err = json.Unmarshal(schema, &schemaMap)
	if err != nil {
		t.Fatalf("Failed to unmarshal schema: %v", err)
	}

	// Verify schema structure
	if schemaMap["type"] != "object" {
		t.Error("Expected type to be 'object'")
	}

	properties, ok := schemaMap["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Properties not found or wrong type")
	}

	// Check each field
	expectedFields := []string{"name", "age", "email", "tags", "active", "metadata"}
	for _, field := range expectedFields {
		if _, exists := properties[field]; !exists {
			t.Errorf("Field %s not found in schema", field)
		}
	}

	// Check that ignored fields are not included
	if _, exists := properties["ignored"]; exists {
		t.Error("Field 'ignored' should not be in schema")
	}
	if _, exists := properties["Ignored2"]; exists {
		t.Error("Field 'Ignored2' should not be in schema")
	}

	// Check required fields (non-omitempty)
	required, ok := schemaMap["required"].([]interface{})
	if !ok {
		t.Fatal("Required fields not found or wrong type")
	}

	// Should have name, age, tags, active (not email or metadata which have omitempty)
	if len(required) != 4 {
		t.Errorf("Expected 4 required fields, got %d", len(required))
	}
}
