package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	llmadapters "github.com/colony-2/colony2/server/llm/adapters"
)

// LLMTaskInput represents the input parameters for the LLMTask activity
type LLMTaskInput struct {
	// Required fields
	Prompt      string `json:"prompt"`
	ModelName   string `json:"modelName"`
	AdapterName string `json:"adapterName"` // e.g., "openai", "anthropic", "bedrock"

	// Optional fields
	SystemPrompt  string   `json:"systemPrompt,omitempty"`
	Temperature   float64  `json:"temperature,omitempty"`
	MaxTokens     int      `json:"maxTokens,omitempty"`
	TopP          float64  `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`

	// Response structure - if provided, will request JSON response format
	// This should be a pointer to a struct with json tags
	ResponseStructure     interface{}     `json:"-"`
	ResponseStructureJSON json.RawMessage `json:"responseStructure,omitempty"`

	// Additional metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// LLMTaskOutput represents the output of the LLMTask activity
type LLMTaskOutput struct {
	// Response content - always string (plain text or stringified JSON)
	Response string `json:"response"`

	// Telemetry information
	Telemetry LLMTelemetry `json:"telemetry"`

	// Model information
	Model        string `json:"model"`
	FinishReason string `json:"finishReason"`
}

// LLMTelemetry contains usage and cost information
type LLMTelemetry struct {
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	TotalTokens      int     `json:"totalTokens"`
	EstimatedCostUSD float64 `json:"estimatedCostUSD,omitempty"`
}

// ModelPricing contains pricing information per model
var ModelPricing = map[string]struct {
	InputPricePer1K  float64
	OutputPricePer1K float64
}{
	// OpenAI models
	"gpt-4":         {InputPricePer1K: 0.03, OutputPricePer1K: 0.06},
	"gpt-4-turbo":   {InputPricePer1K: 0.01, OutputPricePer1K: 0.03},
	"gpt-3.5-turbo": {InputPricePer1K: 0.0005, OutputPricePer1K: 0.0015},

	// Anthropic models
	"claude-3-opus":   {InputPricePer1K: 0.015, OutputPricePer1K: 0.075},
	"claude-3-sonnet": {InputPricePer1K: 0.003, OutputPricePer1K: 0.015},
	"claude-3-haiku":  {InputPricePer1K: 0.00025, OutputPricePer1K: 0.00125},

	// Bedrock models (example pricing)
	"anthropic.claude-v2":          {InputPricePer1K: 0.008, OutputPricePer1K: 0.024},
	"amazon.titan-text-express-v1": {InputPricePer1K: 0.0002, OutputPricePer1K: 0.0006},
}

// LLMTask executes an LLM generation task with optional structured response support
func LLMTask(ctx context.Context, input LLMTaskInput, registry llmadapters.Registry) (*LLMTaskOutput, error) {
	// Validate input
	if input.Prompt == "" {
		return nil, fmt.Errorf("prompt cannot be empty")
	}
	if input.ModelName == "" {
		return nil, fmt.Errorf("model name cannot be empty")
	}
	if input.AdapterName == "" {
		return nil, fmt.Errorf("adapter name cannot be empty")
	}

	// Get adapter from registry
	adapter, err := registry.Get(input.AdapterName)
	if err != nil {
		return nil, fmt.Errorf("failed to get adapter: %w", err)
	}

	// Build configuration
	config := llmadapters.Config{
		Model:         input.ModelName,
		SystemPrompt:  input.SystemPrompt,
		Temperature:   input.Temperature,
		MaxTokens:     input.MaxTokens,
		TopP:          input.TopP,
		StopSequences: input.StopSequences,
		Metadata:      input.Metadata,
	}

	// Handle structured response if provided
	var responseStruct interface{}
	if input.ResponseStructure != nil {
		responseStruct = input.ResponseStructure
		config.ResponseFormat = "json"

		// Add schema information to the prompt
		schemaJSON, err := generateJSONSchema(input.ResponseStructure)
		if err != nil {
			return nil, fmt.Errorf("failed to generate JSON schema: %w", err)
		}

		input.Prompt = fmt.Sprintf("%s\n\nPlease respond with a JSON object that matches this schema:\n%s",
			input.Prompt, string(schemaJSON))
	} else if len(input.ResponseStructureJSON) > 0 {
		// If JSON schema was provided directly
		config.ResponseFormat = "json"
		input.Prompt = fmt.Sprintf("%s\n\nPlease respond with a JSON object that matches this schema:\n%s",
			input.Prompt, string(input.ResponseStructureJSON))
	}

	// Generate response
	response, err := adapter.Generate(ctx, input.Prompt, config)
	if err != nil {
		return nil, fmt.Errorf("generation failed: %w", err)
	}

	// Process response (always return a string)
	var finalResponse string
	if config.ResponseFormat == "json" && responseStruct != nil {
		// Parse JSON into the provided structure for validation, then re-marshal to canonical string
		if err := json.Unmarshal([]byte(response.Content), responseStruct); err != nil {
			return nil, fmt.Errorf("failed to parse JSON response: %w", err)
		}
		b, err := json.Marshal(responseStruct)
		if err != nil {
			return nil, fmt.Errorf("failed to normalize JSON response: %w", err)
		}
		finalResponse = string(b)
	} else if config.ResponseFormat == "json" {
		// Parse as generic JSON to validate, then re-marshal to string
		var jsonResponse interface{}
		if err := json.Unmarshal([]byte(response.Content), &jsonResponse); err != nil {
			return nil, fmt.Errorf("failed to parse JSON response: %w", err)
		}
		b, err := json.Marshal(jsonResponse)
		if err != nil {
			return nil, fmt.Errorf("failed to normalize JSON response: %w", err)
		}
		finalResponse = string(b)
	} else {
		// Plain text response
		finalResponse = response.Content
	}

	// Calculate estimated cost
	estimatedCost := calculateCost(input.ModelName, response.Usage)

	// Build output
	output := &LLMTaskOutput{
		Response: finalResponse,
		Telemetry: LLMTelemetry{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
			EstimatedCostUSD: estimatedCost,
		},
		Model:        response.Model,
		FinishReason: response.FinishReason,
	}

	return output, nil
}

// calculateCost estimates the cost based on model and token usage
func calculateCost(model string, usage llmadapters.Usage) float64 {
	pricing, ok := ModelPricing[model]
	if !ok {
		// Unknown model, return 0
		return 0
	}

	inputCost := float64(usage.PromptTokens) / 1000.0 * pricing.InputPricePer1K
	outputCost := float64(usage.CompletionTokens) / 1000.0 * pricing.OutputPricePer1K

	return inputCost + outputCost
}

// generateJSONSchema generates a simple JSON schema from a Go struct
func generateJSONSchema(v interface{}) ([]byte, error) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": make(map[string]interface{}),
	}

	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct, got %s", t.Kind())
	}

	properties := schema["properties"].(map[string]interface{})
	required := []string{}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Parse JSON tag
		tagName := jsonTag
		if idx := len(jsonTag); idx > 0 {
			for j, c := range jsonTag {
				if c == ',' {
					tagName = jsonTag[:j]
					break
				}
			}
		}

		// Skip omitempty fields from required
		if !contains(jsonTag, "omitempty") {
			required = append(required, tagName)
		}

		// Add field to schema
		properties[tagName] = getFieldSchema(field.Type)
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	return json.Marshal(schema)
}

// getFieldSchema returns JSON schema type for a Go type
func getFieldSchema(t reflect.Type) map[string]interface{} {
	switch t.Kind() {
	case reflect.String:
		return map[string]interface{}{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]interface{}{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]interface{}{"type": "number"}
	case reflect.Bool:
		return map[string]interface{}{"type": "boolean"}
	case reflect.Slice, reflect.Array:
		return map[string]interface{}{
			"type":  "array",
			"items": getFieldSchema(t.Elem()),
		}
	case reflect.Map:
		return map[string]interface{}{"type": "object"}
	case reflect.Struct:
		return map[string]interface{}{"type": "object"}
	default:
		return map[string]interface{}{"type": "string"}
	}
}

// contains checks if a string contains a substring
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
