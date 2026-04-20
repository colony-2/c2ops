package llm

import (
	"context"
	"encoding/json"
	"testing"

	llmadapters "github.com/colony-2/colony2/server/llm/adapters"
)

func TestExecuteLLMTask(t *testing.T) {
	// Create mock adapter
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			return llmadapters.Response{
				Content: "Test response from activity",
				Usage: llmadapters.Usage{
					PromptTokens:     25,
					CompletionTokens: 15,
					TotalTokens:      40,
				},
				FinishReason: "stop",
				Model:        "gpt-3.5-turbo",
			}, nil
		},
	}

	// Create and set registry
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}
	SetRegistry(registry)

	// Create activity input
	input := LLMActivity{
		Prompt:       "Test prompt from activity",
		SystemPrompt: "You are a test assistant",
		ModelName:    "gpt-3.5-turbo",
		AdapterName:  "test",
		Temperature:  0.7,
		MaxTokens:    150,
	}

	// Execute activity
	ctx := context.Background()
	output, err := ExecuteLLMTask(ctx, input)
	if err != nil {
		t.Fatalf("ExecuteLLMTask failed: %v", err)
	}

	// Verify output
	var response string
	err = json.Unmarshal(output.Response, &response)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response != "Test response from activity" {
		t.Errorf("Expected response 'Test response from activity', got %s", response)
	}

	if output.Telemetry.TotalTokens != 40 {
		t.Errorf("Expected 40 total tokens, got %d", output.Telemetry.TotalTokens)
	}

	if output.Model != "gpt-3.5-turbo" {
		t.Errorf("Expected model 'gpt-3.5-turbo', got %s", output.Model)
	}
}

func TestExecuteLLMTask_WithResponseSchema(t *testing.T) {
	// Create mock adapter that returns JSON
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			// Verify prompt includes schema
			if !contains(prompt, "JSON object") {
				t.Error("Expected prompt to include JSON schema instruction")
			}

			return llmadapters.Response{
				Content: `{"status": "success", "count": 42}`,
				Usage: llmadapters.Usage{
					PromptTokens:     30,
					CompletionTokens: 10,
					TotalTokens:      40,
				},
				FinishReason: "stop",
				Model:        "gpt-3.5-turbo",
			}, nil
		},
	}

	// Create and set registry
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}
	SetRegistry(registry)

	// Create schema
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"status": {"type": "string"},
			"count": {"type": "integer"}
		},
		"required": ["status", "count"]
	}`)

	// Create activity input with schema
	input := LLMActivity{
		Prompt:         "Generate a status response",
		ModelName:      "gpt-3.5-turbo",
		AdapterName:    "test",
		ResponseSchema: schema,
	}

	// Execute activity
	ctx := context.Background()
	output, err := ExecuteLLMTask(ctx, input)
	if err != nil {
		t.Fatalf("ExecuteLLMTask failed: %v", err)
	}

	// Verify response is valid JSON
	var responseMap map[string]interface{}
	err = json.Unmarshal([]byte(output.Response), &responseMap)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if responseMap["status"] != "success" {
		t.Errorf("Expected status 'success', got %v", responseMap["status"])
	}

	count, ok := responseMap["count"].(float64)
	if !ok || count != 42 {
		t.Errorf("Expected count 42, got %v", responseMap["count"])
	}
}

func TestExecuteLLMTask_RegistryNotInitialized(t *testing.T) {
	// Clear the registry
	SetRegistry(nil)

	input := LLMActivity{
		Prompt:      "Test",
		ModelName:   "gpt-3.5-turbo",
		AdapterName: "test",
	}

	ctx := context.Background()
	_, err := ExecuteLLMTask(ctx, input)
	if err == nil {
		t.Error("Expected error when registry is not initialized")
	}

	expectedError := "LLM registry not initialized"
	if !contains(err.Error(), expectedError) {
		t.Errorf("Expected error containing '%s', got '%s'", expectedError, err.Error())
	}
}

func TestExecuteLLMTask_CostTracking(t *testing.T) {
	// Create mock adapter
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			return llmadapters.Response{
				Content: "Response with cost tracking",
				Usage: llmadapters.Usage{
					PromptTokens:     1000,
					CompletionTokens: 500,
					TotalTokens:      1500,
				},
				FinishReason: "stop",
				Model:        "gpt-3.5-turbo",
			}, nil
		},
	}

	// Create and set registry
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}
	SetRegistry(registry)

	// Create activity input
	input := LLMActivity{
		Prompt:      "Calculate cost for this request",
		ModelName:   "gpt-3.5-turbo",
		AdapterName: "test",
	}

	// Execute activity
	ctx := context.Background()
	output, err := ExecuteLLMTask(ctx, input)
	if err != nil {
		t.Fatalf("ExecuteLLMTask failed: %v", err)
	}

	// Verify cost calculation
	expectedCost := 0.0005 + 0.00075 // $0.00125
	if output.Telemetry.EstimatedCostUSD != expectedCost {
		t.Errorf("Expected cost $%.6f, got $%.6f", expectedCost, output.Telemetry.EstimatedCostUSD)
	}
}

func TestGetRegistry(t *testing.T) {
	// Create a test registry
	registry := llmadapters.NewRegistry()
	SetRegistry(registry)

	// Get registry
	retrieved := GetRegistry()
	if retrieved != registry {
		t.Error("GetRegistry did not return the set registry")
	}
}

// Note: InitializeRegistry() test would require environment variables
// and actual adapter creation, so it's omitted from unit tests.
// It should be tested in integration tests instead.
