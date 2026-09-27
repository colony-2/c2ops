package llmadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openai "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
	"google.golang.org/genai"
)

// Exercise the real SDK serializers and response parsers without provider keys.
func TestProviderSDKCompatibility(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch provider {
				case "openai":
					if r.URL.Path != "/chat/completions" || body["model"] != "gpt-4o-mini" {
						t.Errorf("unexpected request: %s %v", r.URL.Path, body)
					}
					fmt.Fprint(w, `{"id":"chat-1","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hello","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"test\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
				case "anthropic":
					if r.URL.Path != "/v1/messages" || body["max_tokens"] != float64(64) {
						t.Errorf("unexpected request: %s %v", r.URL.Path, body)
					}
					fmt.Fprint(w, `{"id":"msg-1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"call-1","name":"lookup","input":{"query":"test"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`)
				case "gemini":
					if !strings.HasSuffix(r.URL.Path, ":generateContent") || body["contents"] == nil {
						t.Errorf("unexpected request: %s %v", r.URL.Path, body)
					}
					fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"},{"functionCall":{"name":"lookup","args":{"query":"test"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}`)
				}
				if body["tools"] == nil {
					t.Error("tool definitions missing from request")
				}
			}))
			defer server.Close()
			var adapter Adapter
			model := ""
			switch provider {
			case "openai":
				adapter = &OpenAIAdapter{client: openai.NewClient(openaioption.WithAPIKey("dummy"), openaioption.WithBaseURL(server.URL))}
				model = "gpt-4o-mini"
			case "anthropic":
				adapter = &AnthropicAdapter{client: anthropic.NewClient(anthropicoption.WithAPIKey("dummy"), anthropicoption.WithBaseURL(server.URL))}
				model = "claude-test"
			case "gemini":
				client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: "dummy", Backend: genai.BackendGeminiAPI, HTTPOptions: genai.HTTPOptions{BaseURL: server.URL}})
				if err != nil {
					t.Fatal(err)
				}
				adapter = &GeminiAdapter{client: client}
				model = "gemini-test"
			}
			tools := []Tool{{Name: "lookup", Description: "Look up a query", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}}
			response, err := adapter.GenerateWithTools(ctx, "hello", tools, Config{Model: model, MaxTokens: 64})
			if err != nil {
				t.Fatal(err)
			}
			if response.Content != "hello" || response.Usage.TotalTokens != 5 {
				t.Fatalf("unexpected response: %+v", response)
			}
			if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "lookup" {
				t.Fatalf("tool call lost: %+v", response.ToolCalls)
			}
			var args map[string]any
			if err := json.Unmarshal(response.ToolCalls[0].Arguments, &args); err != nil || args["query"] != "test" {
				t.Fatalf("bad tool arguments: %s", response.ToolCalls[0].Arguments)
			}
		})
	}
}
