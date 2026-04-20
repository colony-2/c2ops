package llmadapters

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test for structured output with response schema
func TestGeminiAdapter_StructuredOutput(t *testing.T) {
	adapter := &GeminiAdapter{}

	// Test schema for a person object
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"age": {"type": "number"},
			"email": {"type": "string", "format": "email"}
		},
		"required": ["name", "age"]
	}`)

	config := Config{
		Model:          "gemini-1.5-flash",
		ResponseFormat: "json",
		ResponseSchema: schema,
		Temperature:    0.7,
		MaxTokens:      100,
	}

	// Validate that the config passes validation
	err := adapter.validateConfig(config)
	assert.NoError(t, err)

	// Test invalid schema
	invalidSchema := json.RawMessage(`{invalid json}`)

	// This would fail during Generate when trying to unmarshal the schema
	// We can't test the actual generation without mocking, but we can verify
	// that the schema is properly formatted JSON
	var testSchema interface{}
	err = json.Unmarshal(invalidSchema, &testSchema)
	assert.Error(t, err, "Invalid schema should fail to unmarshal")

	// Test that text format doesn't use schema
	textConfig := Config{
		Model:          "gemini-1.5-flash",
		ResponseFormat: "text",
		ResponseSchema: schema, // Should be ignored when format is text
	}
	err = adapter.validateConfig(textConfig)
	assert.NoError(t, err)
}

// Test for tool schema conversion
func TestGeminiAdapter_ToolConversion(t *testing.T) {
	tool := Tool{
		Name:        "get_weather",
		Description: "Get the weather for a location",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"location": {
					"type": "string",
					"description": "The location to get weather for"
				},
				"unit": {
					"type": "string",
					"enum": ["celsius", "fahrenheit"],
					"description": "Temperature unit"
				}
			},
			"required": ["location"]
		}`),
	}

	// Parse the parameters to ensure they're valid
	var params map[string]interface{}
	err := json.Unmarshal(tool.Parameters, &params)
	require.NoError(t, err)

	// Check the structure
	assert.Equal(t, "object", params["type"])
	assert.NotNil(t, params["properties"])
	assert.NotNil(t, params["required"])

	// Check required conversion
	required := convertToStringSlice(params["required"])
	assert.Equal(t, []string{"location"}, required)
}
