package llmadapters

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "net/http/httptest"
    "os"
    "testing"
    "time"

    "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestNewOpenAIAdapter(t *testing.T) {
	tests := []struct {
		name      string
		apiKey    string
		envKey    string
		wantErr   bool
	}{
		{
			name:    "with api key",
			apiKey:  "test-api-key",
			wantErr: false,
		},
		{
			name:    "with env var",
			apiKey:  "",
			envKey:  "env-api-key",
			wantErr: false,
		},
		{
			name:    "no api key",
			apiKey:  "",
			envKey:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set env var if needed
			if tt.envKey != "" {
				os.Setenv("OPENAI_API_KEY", tt.envKey)
				defer os.Unsetenv("OPENAI_API_KEY")
			}

			adapter, err := NewOpenAIAdapter(tt.apiKey)
			
			if tt.wantErr {
				assert.Error(t, err)
				assert.Equal(t, ErrAPIKeyMissing, err)
				assert.Nil(t, adapter)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, adapter)
				assert.NotNil(t, adapter.client)
				assert.NotNil(t, adapter.rateLimiter)
			}
		})
	}
}

func TestOpenAIAdapter_Generate(t *testing.T) {
    // Create test server
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Verify request
        assert.Equal(t, "POST", r.Method)
        assert.Equal(t, "/chat/completions", r.URL.Path)
        assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

        // Parse request body
        var req map[string]interface{}
        err := json.NewDecoder(r.Body).Decode(&req)
        require.NoError(t, err)

        // Send response
        resp := map[string]interface{}{
            "id":      "chatcmpl-123",
            "object":  "chat.completion",
            "created": time.Now().Unix(),
            "model":   req["model"],
            "choices": []map[string]interface{}{
                {
                    "index": 0,
                    "message": map[string]interface{}{
                        "role":    "assistant",
                        "content": "Test response",
                    },
                    "finish_reason": "stop",
                },
            },
            "usage": map[string]interface{}{
                "prompt_tokens":     10,
                "completion_tokens": 5,
                "total_tokens":      15,
            },
        }

        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(resp)
    }))
    defer server.Close()

    // Create adapter with test server
    adapter := &OpenAIAdapter{
        client: openai.NewClient(
            option.WithAPIKey("test-key"),
            option.WithBaseURL(server.URL),
        ),
    }

	config := Config{
		Model:        "gpt-3.5-turbo",
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
	assert.Equal(t, "stop", resp.FinishReason)
}

func TestOpenAIAdapter_Generate_JSONMode(t *testing.T) {
    // Create test server
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Parse request body
        var req map[string]interface{}
        err := json.NewDecoder(r.Body).Decode(&req)
        require.NoError(t, err)

        // Verify JSON mode is set
        responseFormat := req["response_format"].(map[string]interface{})
        assert.Equal(t, "json_object", responseFormat["type"])

        // Send response
        resp := map[string]interface{}{
            "id":      "chatcmpl-123",
            "object":  "chat.completion",
            "created": time.Now().Unix(),
            "model":   req["model"],
            "choices": []map[string]interface{}{
                {
                    "index": 0,
                    "message": map[string]interface{}{
                        "role":    "assistant",
                        "content": `{"result": "test"}`,
                    },
                    "finish_reason": "stop",
                },
            },
            "usage": map[string]interface{}{
                "prompt_tokens":     10,
                "completion_tokens": 5,
                "total_tokens":      15,
            },
        }

        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(resp)
    }))
    defer server.Close()

    // Create adapter with test server
    adapter := &OpenAIAdapter{
        client: openai.NewClient(
            option.WithAPIKey("test-key"),
            option.WithBaseURL(server.URL),
        ),
    }

	config := Config{
		Model:          "gpt-3.5-turbo",
		Temperature:    0.7,
		ResponseFormat: "json",
	}

	resp, err := adapter.Generate(context.Background(), "Return JSON", config)
	require.NoError(t, err)

	assert.Equal(t, `{"result": "test"}`, resp.Content)
}

func TestOpenAIAdapter_GenerateWithTools(t *testing.T) {
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
            "id":      "chatcmpl-123",
            "object":  "chat.completion",
            "created": time.Now().Unix(),
            "model":   req["model"],
            "choices": []map[string]interface{}{
                {
                    "index": 0,
                    "message": map[string]interface{}{
                        "role":    "assistant",
                        "content": "",
                        "tool_calls": []map[string]interface{}{
                            {
                                "id": "call_123",
                                "type": "function",
                                "function": map[string]interface{}{
                                    "name":      "get_weather",
                                    "arguments": `{"location": "San Francisco"}`,
                                },
                            },
                        },
                    },
                    "finish_reason": "tool_calls",
                },
            },
            "usage": map[string]interface{}{
                "prompt_tokens":     10,
                "completion_tokens": 5,
                "total_tokens":      15,
            },
        }

        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(resp)
    }))
    defer server.Close()

    // Create adapter with test server
    adapter := &OpenAIAdapter{
        client: openai.NewClient(
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
		Model:       "gpt-3.5-turbo",
		Temperature: 0.7,
	}

	resp, err := adapter.GenerateWithTools(context.Background(), "What's the weather?", tools, config)
	require.NoError(t, err)

	assert.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_123", resp.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
	assert.JSONEq(t, `{"location": "San Francisco"}`, string(resp.ToolCalls[0].Arguments))
}

func TestOpenAIAdapter_StreamGenerate(t *testing.T) {
    // Create test server that sends SSE
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        w.Header().Set("Cache-Control", "no-cache")
        w.Header().Set("Connection", "keep-alive")

        // Send chunks
        chunks := []string{"Hello", " ", "World"}
        for i, chunk := range chunks {
            data := map[string]interface{}{
                "id":      fmt.Sprintf("chatcmpl-%d", i),
                "object":  "chat.completion.chunk",
                "created": time.Now().Unix(),
                "model":   "gpt-3.5-turbo",
                "choices": []map[string]interface{}{
                    {
                        "index": 0,
                        "delta": map[string]interface{}{
                            "content": chunk,
                        },
                        "finish_reason": nil,
                    },
                },
            }

            jsonData, _ := json.Marshal(data)
            fmt.Fprintf(w, "data: %s\n\n", jsonData)
            w.(http.Flusher).Flush()
        }

        // Send done
        fmt.Fprintf(w, "data: [DONE]\n\n")
        w.(http.Flusher).Flush()
    }))
    defer server.Close()

    // Create adapter with test server
    adapter := &OpenAIAdapter{
        client: openai.NewClient(
            option.WithAPIKey("test-key"),
            option.WithBaseURL(server.URL),
        ),
    }

	config := Config{
		Model:       "gpt-3.5-turbo",
		Temperature: 0.7,
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

func TestOpenAIAdapter_validateConfig(t *testing.T) {
	adapter := &OpenAIAdapter{}

	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: Config{
				Model:       "gpt-3.5-turbo",
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
			name: "invalid temperature",
			config: Config{
				Model:       "gpt-3.5-turbo",
				Temperature: 3.0,
			},
			wantErr: true,
		},
		{
			name: "invalid top_p",
			config: Config{
				Model: "gpt-3.5-turbo",
				TopP:  1.5,
			},
			wantErr: true,
		},
		{
			name: "invalid response format",
			config: Config{
				Model:          "gpt-3.5-turbo",
				ResponseFormat: "xml",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adapter.validateConfig(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOpenAIAdapter_handleError(t *testing.T) {
	adapter := &OpenAIAdapter{}

	tests := []struct {
		name     string
		err      error
		expected error
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: nil,
		},
		{
			name:     "rate limit error",
			err:      errors.New("rate_limit_exceeded"),
			expected: ErrRateLimitExceeded,
		},
		{
			name:     "context length error",
			err:      errors.New("context_length_exceeded"),
			expected: ErrContextLengthExceeded,
		},
		{
			name:     "model not found",
			err:      errors.New("model_not_found"),
			expected: ErrModelNotSupported,
		},
		{
			name:     "invalid api key",
			err:      errors.New("invalid_api_key"),
			expected: ErrAPIKeyMissing,
		},
		{
			name:     "generic error",
			err:      errors.New("some other error"),
			expected: fmt.Errorf("openai api error: %w", errors.New("some other error")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.handleError(tt.err)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else if errors.Is(tt.expected, ErrRateLimitExceeded) ||
				errors.Is(tt.expected, ErrContextLengthExceeded) ||
				errors.Is(tt.expected, ErrModelNotSupported) ||
				errors.Is(tt.expected, ErrAPIKeyMissing) {
				assert.Equal(t, tt.expected, result)
			} else {
				assert.Contains(t, result.Error(), "openai api error")
			}
		})
	}
}
