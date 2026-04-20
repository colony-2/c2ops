package llmadapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicAdapter_Generate(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/v1/messages", r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("X-API-Key"))

		// Parse request body
		var req map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		// Send response
		resp := map[string]interface{}{
			"id":    "msg_123",
			"type":  "message",
			"role":  "assistant",
			"model": req["model"],
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "Test response",
				},
			},
			"stop_reason": "end_turn",
			"usage": map[string]interface{}{
				"input_tokens":  10,
				"output_tokens": 5,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create adapter with test server
	adapter := &AnthropicAdapter{
		client: anthropic.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(server.URL),
		),
	}

	config := Config{
		Model:        "claude-3-haiku",
		Temperature:  0.7,
		MaxTokens:    100,
		SystemPrompt: "You are a helpful assistant",
	}

	resp, err := adapter.Generate(context.Background(), "Hello", config)
	require.NoError(t, err)

	assert.Equal(t, "Test response", resp.Content)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 5, resp.Usage.CompletionTokens)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
	assert.Equal(t, "end_turn", resp.FinishReason)
}

func TestAnthropicAdapter_GenerateWithTools(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse request body
		var req map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		// Verify tools are present
		tools := req["tools"].([]interface{})
		assert.Len(t, tools, 1)

		// Send response with tool call
		resp := map[string]interface{}{
			"id":    "msg_123",
			"type":  "message",
			"role":  "assistant",
			"model": req["model"],
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "I'll check the weather for you.",
				},
				{
					"type": "tool_use",
					"id":   "tool_123",
					"name": "get_weather",
					"input": map[string]interface{}{
						"location": "San Francisco",
					},
				},
			},
			"stop_reason": "tool_use",
			"usage": map[string]interface{}{
				"input_tokens":  10,
				"output_tokens": 5,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create adapter with test server
	adapter := &AnthropicAdapter{
		client: anthropic.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(server.URL),
		),
	}

	tools := []Tool{
		{
			Name:        "get_weather",
			Description: "Get weather information",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"location": {"type": "string"}
				},
				"required": ["location"]
			}`),
		},
	}

	config := Config{
		Model:       "claude-3-haiku",
		Temperature: 0.7,
		MaxTokens:   100,
	}

	resp, err := adapter.GenerateWithTools(context.Background(), "What's the weather?", tools, config)
	require.NoError(t, err)

	assert.Equal(t, "I'll check the weather for you.", resp.Content)
	assert.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "tool_123", resp.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
	assert.JSONEq(t, `{"location": "San Francisco"}`, string(resp.ToolCalls[0].Arguments))
}

func TestAnthropicAdapter_StreamGenerate(t *testing.T) {
	// Create test server that sends SSE
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Send events
		events := []string{
			`event: message_start
data: {"type": "message_start", "message": {"model": "claude-3-haiku"}}`,

			`event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}`,

			`event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}`,

			`event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": " "}}`,

			`event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "World"}}`,

			`event: content_block_stop
data: {"type": "content_block_stop", "index": 0}`,

			`event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": null}}`,

			`event: message_stop
data: {"type": "message_stop"}`,
		}

		for _, event := range events {
			fmt.Fprintf(w, "%s\n\n", event)
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()

	// Create adapter with test server
	adapter := &AnthropicAdapter{
		client: anthropic.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(server.URL),
		),
	}

	config := Config{
		Model:       "claude-3-haiku",
		Temperature: 0.7,
		MaxTokens:   100,
	}

	ctx := context.Background()
	tokenChan, err := adapter.StreamGenerate(ctx, "Hello", config)
	require.NoError(t, err)

	// Collect tokens
	var content string
	for token := range tokenChan {
		if token.Error != nil {
			t.Fatalf("unexpected error: %v", token.Error)
		}
		content += token.Content
	}

	assert.Equal(t, "Hello World", content)
}

func TestAnthropicAdapter_ValidateConfig(t *testing.T) {
	adapter := &AnthropicAdapter{}

	tests := []struct {
		name    string
		config  Config
		wantErr bool
		check   func(t *testing.T, config Config)
	}{
		{
			name: "valid config",
			config: Config{
				Model:       "claude-3-haiku",
				Temperature: 0.7,
				TopP:        0.9,
				MaxTokens:   100,
			},
			wantErr: false,
		},
		{
			name: "missing model",
			config: Config{
				Temperature: 0.7,
			},
			wantErr: true,
		},
		{
			name: "invalid temperature too high",
			config: Config{
				Model:       "claude-3-haiku",
				Temperature: 2.0,
			},
			wantErr: true,
		},
		{
			name: "invalid top_p",
			config: Config{
				Model: "claude-3-haiku",
				TopP:  1.5,
			},
			wantErr: true,
		},
		{
			name: "zero max_tokens gets default",
			config: Config{
				Model:     "claude-3-haiku",
				MaxTokens: 0,
			},
			wantErr: false,
			check: func(t *testing.T, config Config) {
				assert.Equal(t, 1024, config.MaxTokens)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := tt.config
			err := adapter.validateConfig(&config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			if tt.check != nil {
				tt.check(t, config)
			}
		})
	}
}

func TestAnthropicAdapter_MapModel(t *testing.T) {
	adapter := &AnthropicAdapter{}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "claude-3-opus",
			input:    "claude-3-opus",
			expected: "claude-3-opus-20240229",
		},
		{
			name:     "claude-3-sonnet",
			input:    "claude-3-sonnet",
			expected: "claude-3-5-sonnet-20241022",
		},
		{
			name:     "claude-3-haiku",
			input:    "claude-3-haiku",
			expected: "claude-3-5-haiku-20241022",
		},
		{
			name:     "claude-2.1",
			input:    "claude-2.1",
			expected: "claude-2.1",
		},
		{
			name:     "claude-2",
			input:    "claude-2",
			expected: "claude-2.0",
		},
		{
			name:     "claude-instant",
			input:    "claude-instant",
			expected: "claude-instant-1.2",
		},
		{
			name:     "custom-model",
			input:    "custom-model-123",
			expected: "custom-model-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.mapModel(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAnthropicAdapter_HandleError(t *testing.T) {
	adapter := &AnthropicAdapter{}

	tests := []struct {
		name     string
		err      error
		expected error
	}{
		{
			name:     "rate limit error",
			err:      errors.New("rate_limit_exceeded"),
			expected: ErrRateLimitExceeded,
		},
		{
			name:     "invalid request error",
			err:      errors.New("invalid_request_error: bad input"),
			expected: ErrInvalidConfig,
		},
		{
			name:     "model not found error",
			err:      errors.New("model_not_found"),
			expected: ErrModelNotSupported,
		},
		{
			name:     "authentication error",
			err:      errors.New("authentication_error"),
			expected: ErrAPIKeyMissing,
		},
		{
			name:     "generic error",
			err:      errors.New("some other error"),
			expected: fmt.Errorf("anthropic api error: %w", errors.New("some other error")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.handleError(tt.err)
			if errors.Is(tt.expected, ErrRateLimitExceeded) ||
				errors.Is(tt.expected, ErrInvalidConfig) ||
				errors.Is(tt.expected, ErrModelNotSupported) ||
				errors.Is(tt.expected, ErrAPIKeyMissing) {
				assert.Equal(t, tt.expected, result)
			} else {
				assert.Contains(t, result.Error(), "anthropic api error")
			}
		})
	}
}
