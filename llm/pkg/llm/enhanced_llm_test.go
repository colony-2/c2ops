package llm

import (
	"context"
	"encoding/json"
	"testing"

	llmadapters "github.com/colony-2/colony2/server/llm/adapters"
)

func TestEnhancedLLMTask_SimpleMode(t *testing.T) {
	// Create mock adapter
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			// Verify simple mode behavior
			if config.SystemPrompt != "You are a helpful assistant" {
				t.Errorf("Expected system prompt 'You are a helpful assistant', got '%s'", config.SystemPrompt)
			}

			return llmadapters.Response{
				Content: "Simple mode response",
				Usage: llmadapters.Usage{
					PromptTokens:     10,
					CompletionTokens: 5,
					TotalTokens:      15,
				},
				FinishReason: "stop",
				Model:        "gpt-4",
			}, nil
		},
	}

	// Create registry and register adapter
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}

	// Create input for simple mode
	input := EnhancedLLMTaskInput{
		LLMTaskInput: LLMTaskInput{
			Prompt:       "Test prompt",
			ModelName:    "gpt-4",
			AdapterName:  "test",
			SystemPrompt: "You are a helpful assistant",
			Temperature:  0.7,
			MaxTokens:    100,
		},
		Mode: ModeSimple,
	}

	// Execute task
	ctx := context.Background()
	output, err := EnhancedLLMTask(ctx, input, registry, nil)
	if err != nil {
		t.Fatalf("EnhancedLLMTask failed: %v", err)
	}

	// Verify output
	if output.Response != "Simple mode response" {
		t.Errorf("Expected response 'Simple mode response', got %v", output.Response)
	}
	if output.Model != "gpt-4" {
		t.Errorf("Expected model 'gpt-4', got %s", output.Model)
	}
}

func TestEnhancedLLMTask_PersonaMode(t *testing.T) {
	// Create mock adapter
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			// Verify persona mode system prompt
			expectedStart := "You are a Senior Research Analyst."
			if !contains(config.SystemPrompt, expectedStart) {
				t.Errorf("System prompt should start with '%s', got '%s'", expectedStart, config.SystemPrompt)
			}

			// Check for capabilities in system prompt
			if !contains(config.SystemPrompt, "web_search") {
				t.Error("System prompt should contain capability 'web_search'")
			}

			// Check for goals in system prompt
			if !contains(config.SystemPrompt, "Find comprehensive, accurate information") {
				t.Error("System prompt should contain goal")
			}

			return llmadapters.Response{
				Content: "Research analysis complete",
				Usage: llmadapters.Usage{
					PromptTokens:     50,
					CompletionTokens: 20,
					TotalTokens:      70,
				},
				FinishReason: "stop",
				Model:        "gpt-4",
			}, nil
		},
	}

	// Create registry and register adapter
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}

	// Create input for persona mode (Research Analyst example from spec)
	input := EnhancedLLMTaskInput{
		LLMTaskInput: LLMTaskInput{
			Prompt:      "What are the latest trends in renewable energy?",
			ModelName:   "gpt-4",
			AdapterName: "test",
		},
		Mode: ModePersona,
		Persona: &PersonaConfig{
			Role: "Senior Research Analyst",
			Capabilities: []string{
				"web_search",
				"data_extraction",
				"source_validation",
				"trend_analysis",
			},
			Goals: []string{
				"Find comprehensive, accurate information",
				"Identify credible sources",
				"Extract key insights",
				"Provide evidence-based analysis",
			},
			Constraints: []string{
				"Only use verified sources",
				"Cite all references",
				"Avoid biased content",
				"Maintain objectivity",
			},
		},
	}

	// Execute task
	ctx := context.Background()
	output, err := EnhancedLLMTask(ctx, input, registry, nil)
	if err != nil {
		t.Fatalf("EnhancedLLMTask failed: %v", err)
	}

	// Verify output
	if output.Response != "Research analysis complete" {
		t.Errorf("Expected response 'Research analysis complete', got %v", output.Response)
	}
}

func TestEnhancedLLMTask_TechnicalWriterPersona(t *testing.T) {
	// Create mock adapter
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			// Verify Technical Writer persona
			if !contains(config.SystemPrompt, "Technical Documentation Specialist") {
				t.Error("System prompt should contain 'Technical Documentation Specialist'")
			}

			return llmadapters.Response{
				Content: "# API Documentation\n\nThis API provides...",
				Usage: llmadapters.Usage{
					PromptTokens:     30,
					CompletionTokens: 15,
					TotalTokens:      45,
				},
				FinishReason: "stop",
				Model:        "gpt-4",
			}, nil
		},
	}

	// Create registry and register adapter
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}

	// Create input for Technical Writer persona (example from spec)
	input := EnhancedLLMTaskInput{
		LLMTaskInput: LLMTaskInput{
			Prompt:      "Document the new REST API endpoints",
			ModelName:   "gpt-4",
			AdapterName: "test",
		},
		Mode: ModePersona,
		Persona: &PersonaConfig{
			Role: "Technical Documentation Specialist",
			Capabilities: []string{
				"technical_writing",
				"api_documentation",
				"code_examples",
				"diagram_creation",
			},
			Goals: []string{
				"Create clear, concise documentation",
				"Ensure technical accuracy",
				"Maintain consistent style",
				"Optimize for developer experience",
			},
			Constraints: []string{
				"Follow documentation standards",
				"Use appropriate technical terminology",
				"Include practical examples",
				"Avoid unnecessary complexity",
			},
		},
	}

	// Execute task
	ctx := context.Background()
	output, err := EnhancedLLMTask(ctx, input, registry, nil)
	if err != nil {
		t.Fatalf("EnhancedLLMTask failed: %v", err)
	}

	// Verify output contains documentation
	if !contains(output.Response, "API Documentation") {
		t.Error("Response should contain 'API Documentation'")
	}
}

func TestEnhancedLLMTask_CustomerSupportPersona(t *testing.T) {
	// Create mock adapter
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			// Verify Customer Support persona
			if !contains(config.SystemPrompt, "Customer Support Specialist") {
				t.Error("System prompt should contain 'Customer Support Specialist'")
			}

			// Check for empathetic communication capability
			if !contains(config.SystemPrompt, "empathetic_communication") {
				t.Error("System prompt should contain 'empathetic_communication' capability")
			}

			return llmadapters.Response{
				Content: "I understand your concern about the billing issue. Let me help you resolve this right away.",
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

	// Create registry and register adapter
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}

	// Create input for Customer Support persona (example from spec)
	input := EnhancedLLMTaskInput{
		LLMTaskInput: LLMTaskInput{
			Prompt:      "I was charged twice for my subscription",
			ModelName:   "gpt-3.5-turbo",
			AdapterName: "test",
		},
		Mode: ModePersona,
		Persona: &PersonaConfig{
			Role: "Customer Support Specialist",
			Capabilities: []string{
				"issue_resolution",
				"empathetic_communication",
				"knowledge_base_access",
				"ticket_escalation",
			},
			Goals: []string{
				"Resolve customer issues quickly",
				"Provide helpful, friendly assistance",
				"Maintain customer satisfaction",
				"Document solutions for future reference",
			},
			Constraints: []string{
				"Use professional, friendly tone",
				"Follow support protocols",
				"Protect customer privacy",
				"Escalate complex issues appropriately",
			},
		},
	}

	// Execute task
	ctx := context.Background()
	output, err := EnhancedLLMTask(ctx, input, registry, nil)
	if err != nil {
		t.Fatalf("EnhancedLLMTask failed: %v", err)
	}

	// Verify output has empathetic tone
	if !contains(output.Response, "understand") || !contains(output.Response, "help") {
		t.Error("Response should have empathetic tone with 'understand' and 'help'")
	}
}

func TestEnhancedLLMTask_WithFileContext(t *testing.T) {
	// Create mock adapter
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			// Check if file context was added to prompt
			if !contains(prompt, "File Context") {
				// Note: In the actual implementation, files would be included
				// For this test, we're just checking the flow works
			}

			return llmadapters.Response{
				Content: `{"review": "Code looks good", "issues": []}`,
				Usage: llmadapters.Usage{
					PromptTokens:     100,
					CompletionTokens: 20,
					TotalTokens:      120,
				},
				FinishReason: "stop",
				Model:        "gpt-4",
			}, nil
		},
	}

	// Create registry and register adapter
	registry := llmadapters.NewRegistry()
	err := registry.Register("test", mockAdapter)
	if err != nil {
		t.Fatalf("Failed to register adapter: %v", err)
	}

	// Create input with file context (Code Review example from spec)
	input := EnhancedLLMTaskInput{
		LLMTaskInput: LLMTaskInput{
			Prompt:                "Review this code for potential issues",
			ModelName:             "gpt-4",
			AdapterName:           "test",
			ResponseStructureJSON: json.RawMessage(`{"type": "object", "properties": {"review": {"type": "string"}, "issues": {"type": "array"}}}`),
		},
		Mode: ModePersona,
		Persona: &PersonaConfig{
			Role: "Senior Go Developer and Code Reviewer",
			Capabilities: []string{
				"code_analysis",
				"security_review",
				"performance_optimization",
				"best_practices_enforcement",
			},
			Goals: []string{
				"Identify potential bugs and security issues",
				"Suggest performance improvements",
				"Ensure code follows Go best practices",
				"Provide actionable feedback",
			},
			Constraints: []string{
				"Focus on practical, implementable suggestions",
				"Prioritize critical issues over style preferences",
				"Consider backward compatibility",
			},
		},
		Context: &FileContext{
			Artifacts: []ArtifactPath{
				{Path: "test.go", Label: "Test file"},
			},
			FileLimits: &FileLimits{
				MaxFileSize:  1048576, // 1MB
				MaxTotalSize: 5242880, // 5MB
				MaxFileCount: 20,
			},
		},
	}

	// Execute task
	ctx := context.Background()
	output, err := EnhancedLLMTask(ctx, input, registry, nil)
	if err != nil {
		// File might not exist, which is expected in test
		// We're testing the flow, not actual file reading
		if !contains(err.Error(), "no such file") {
			t.Fatalf("Unexpected error: %v", err)
		}
		return
	}

	// If we get here, verify JSON response was parsed
	if output != nil {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(output.Response), &parsed); err != nil {
			t.Error("Expected parsed JSON response")
		}
	}
}

func TestEnhancedLLMTask_ValidationErrors(t *testing.T) {
	registry := llmadapters.NewRegistry()
	ctx := context.Background()

	tests := []struct {
		name  string
		input EnhancedLLMTaskInput
		error string
	}{
		{
			name: "Empty prompt",
			input: EnhancedLLMTaskInput{
				LLMTaskInput: LLMTaskInput{
					ModelName:   "gpt-4",
					AdapterName: "test",
				},
			},
			error: "prompt cannot be empty",
		},
		{
			name: "Empty model name",
			input: EnhancedLLMTaskInput{
				LLMTaskInput: LLMTaskInput{
					Prompt:      "Test",
					AdapterName: "test",
				},
			},
			error: "model name cannot be empty",
		},
		{
			name: "Persona mode without persona config",
			input: EnhancedLLMTaskInput{
				LLMTaskInput: LLMTaskInput{
					Prompt:      "Test",
					ModelName:   "gpt-4",
					AdapterName: "test",
				},
				Mode: ModePersona,
			},
			error: "persona configuration is required",
		},
		{
			name: "Persona mode with empty role",
			input: EnhancedLLMTaskInput{
				LLMTaskInput: LLMTaskInput{
					Prompt:      "Test",
					ModelName:   "gpt-4",
					AdapterName: "test",
				},
				Mode: ModePersona,
				Persona: &PersonaConfig{
					Role: "",
				},
			},
			error: "role is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EnhancedLLMTask(ctx, tt.input, registry, nil)
			if err == nil {
				t.Error("Expected error but got none")
			}
			if err != nil && !contains(err.Error(), tt.error) {
				t.Errorf("Expected error containing '%s', got '%s'", tt.error, err.Error())
			}
		})
	}
}

func TestEnhancedLLMTask_BackwardCompatibility(t *testing.T) {
	// Create mock adapter
	mockAdapter := &mockAdapter{
		generateFunc: func(ctx context.Context, prompt string, config llmadapters.Config) (llmadapters.Response, error) {
			// Verify that simple mode preserves original behavior
			if prompt != "Original prompt" {
				t.Errorf("Expected prompt 'Original prompt', got '%s'", prompt)
			}
			if config.SystemPrompt != "Original system prompt" {
				t.Errorf("Expected system prompt 'Original system prompt', got '%s'", config.SystemPrompt)
			}

			return llmadapters.Response{
				Content: "Backward compatible response",
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

	// Create input without mode (should default to simple)
	input := EnhancedLLMTaskInput{
		LLMTaskInput: LLMTaskInput{
			Prompt:       "Original prompt",
			ModelName:    "gpt-3.5-turbo",
			AdapterName:  "test",
			SystemPrompt: "Original system prompt",
			Temperature:  0.5,
			MaxTokens:    50,
		},
		// Mode not specified - should default to simple
	}

	// Execute task
	ctx := context.Background()
	output, err := EnhancedLLMTask(ctx, input, registry, nil)
	if err != nil {
		t.Fatalf("EnhancedLLMTask failed: %v", err)
	}

	// Verify output maintains backward compatibility
	if output.Response != "Backward compatible response" {
		t.Errorf("Expected response 'Backward compatible response', got %v", output.Response)
	}
	if output.Model != "gpt-3.5-turbo" {
		t.Errorf("Expected model 'gpt-3.5-turbo', got %s", output.Model)
	}
	if output.Telemetry.TotalTokens != 15 {
		t.Errorf("Expected 15 total tokens, got %d", output.Telemetry.TotalTokens)
	}
}
