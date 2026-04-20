package llmadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"golang.org/x/time/rate"
)

// OpenAIAdapter implements the Adapter interface for OpenAI API
type OpenAIAdapter struct {
	client      openai.Client
	rateLimiter *rate.Limiter
}

// NewOpenAIAdapter creates a new OpenAI adapter
func NewOpenAIAdapter(apiKey string) (*OpenAIAdapter, error) {
	if apiKey == "" {
		// Try to get from environment variable
		apiKey = os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, ErrAPIKeyMissing
		}
	}

	client := openai.NewClient(
		option.WithAPIKey(apiKey),
	)
	
	// Create rate limiter: 60 requests per minute
	rateLimiter := rate.NewLimiter(rate.Limit(1), 60)

	return &OpenAIAdapter{
		client:      client,
		rateLimiter: rateLimiter,
	}, nil
}

// Generate creates a completion for the given prompt
func (a *OpenAIAdapter) Generate(ctx context.Context, prompt string, config Config) (Response, error) {
	// Apply rate limiting
	if a.rateLimiter != nil {
		if err := a.rateLimiter.Wait(ctx); err != nil {
			return Response{}, fmt.Errorf("rate limiter error: %w", err)
		}
	}

	// Validate configuration
	if err := a.validateConfig(config); err != nil {
		return Response{}, err
	}

	// Build messages
	messages := []openai.ChatCompletionMessageParamUnion{}
	
	if config.SystemPrompt != "" {
		messages = append(messages, openai.SystemMessage(config.SystemPrompt))
	}
	
	messages = append(messages, openai.UserMessage(prompt))

	// Build request parameters
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(a.mapModel(config.Model)),
		Messages: messages,
	}
	
	// Some models (o4, o1) don't support temperature parameter
	if !strings.HasPrefix(config.Model, "o4") && !strings.HasPrefix(config.Model, "o1") {
		params.Temperature = openai.Float(float64(config.Temperature))
	}

	if config.MaxTokens > 0 {
		// Some models use max_completion_tokens instead of max_tokens
		if strings.HasPrefix(config.Model, "o4") || strings.HasPrefix(config.Model, "o1") {
			params.MaxCompletionTokens = openai.Int(int64(config.MaxTokens))
		} else {
			params.MaxTokens = openai.Int(int64(config.MaxTokens))
		}
	}

	if config.TopP > 0 {
		params.TopP = openai.Float(float64(config.TopP))
	}

	if len(config.StopSequences) > 0 {
		// OpenAI API accepts stop sequences as a slice or a single string
		if len(config.StopSequences) == 1 {
			params.Stop = openai.ChatCompletionNewParamsStopUnion{OfString: openai.String(config.StopSequences[0])}
		} else {
			params.Stop = openai.ChatCompletionNewParamsStopUnion{OfStringArray: config.StopSequences}
		}
	}

	// Handle response format
	if config.ResponseFormat == "json" {
		if len(config.ResponseSchema) > 0 {
			// Use structured output with JSON schema
			var schema interface{}
			if err := json.Unmarshal(config.ResponseSchema, &schema); err != nil {
				return Response{}, fmt.Errorf("invalid response schema: %w", err)
			}
			
			params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
					Type: "json_schema",
					JSONSchema: openai.ResponseFormatJSONSchemaJSONSchemaParam{
						Name:   "response",
						Strict: openai.Bool(true),
						Schema: schema,
					},
				},
			}
		} else {
			// Simple JSON mode
			params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONObject: &openai.ResponseFormatJSONObjectParam{
					Type: "json_object",
				},
			}
		}
	}

	// Make API call
	completion, err := a.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return Response{}, a.handleError(err)
	}

	if len(completion.Choices) == 0 {
		return Response{}, ErrInvalidResponse
	}

	choice := completion.Choices[0]
	
	// Get content, checking for refusal
	content := choice.Message.Content
	if content == "" && choice.Message.Refusal != "" {
		content = choice.Message.Refusal
	}
	
	return Response{
		Content: content,
		Usage: Usage{
			PromptTokens:     int(completion.Usage.PromptTokens),
			CompletionTokens: int(completion.Usage.CompletionTokens),
			TotalTokens:      int(completion.Usage.TotalTokens),
		},
		FinishReason: string(choice.FinishReason),
		Model:        completion.Model,
	}, nil
}

// GenerateWithTools creates a completion with tool/function calling support
func (a *OpenAIAdapter) GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error) {
	// Apply rate limiting
	if a.rateLimiter != nil {
		if err := a.rateLimiter.Wait(ctx); err != nil {
			return Response{}, fmt.Errorf("rate limiter error: %w", err)
		}
	}

	// Validate configuration
	if err := a.validateConfig(config); err != nil {
		return Response{}, err
	}

	// Build messages
	messages := []openai.ChatCompletionMessageParamUnion{}
	
	if config.SystemPrompt != "" {
		messages = append(messages, openai.SystemMessage(config.SystemPrompt))
	}
	
	messages = append(messages, openai.UserMessage(prompt))

	// Convert tools to OpenAI format
	openaiTools := make([]openai.ChatCompletionToolParam, len(tools))
	for i, tool := range tools {
		var params interface{}
		if err := json.Unmarshal(tool.Parameters, &params); err != nil {
			return Response{}, fmt.Errorf("failed to parse tool parameters: %w", err)
		}

		openaiTools[i] = openai.ChatCompletionToolParam{
			Type: "function",
			Function: openai.FunctionDefinitionParam{
				Name:        tool.Name,
				Description: openai.String(tool.Description),
				Parameters:  openai.FunctionParameters(params.(map[string]interface{})),
			},
		}
	}

	// Build request parameters
	reqParams := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(a.mapModel(config.Model)),
		Messages: messages,
		Tools:    openaiTools,
	}
	
	// Some models (o4, o1) don't support temperature parameter
	if !strings.HasPrefix(config.Model, "o4") && !strings.HasPrefix(config.Model, "o1") {
		reqParams.Temperature = openai.Float(float64(config.Temperature))
	}

	if config.MaxTokens > 0 {
		// Some models use max_completion_tokens instead of max_tokens
		if strings.HasPrefix(config.Model, "o4") || strings.HasPrefix(config.Model, "o1") {
			reqParams.MaxCompletionTokens = openai.Int(int64(config.MaxTokens))
		} else {
			reqParams.MaxTokens = openai.Int(int64(config.MaxTokens))
		}
	}

	if config.TopP > 0 {
		reqParams.TopP = openai.Float(float64(config.TopP))
	}

	// Make API call
	completion, err := a.client.Chat.Completions.New(ctx, reqParams)
	if err != nil {
		return Response{}, a.handleError(err)
	}

	if len(completion.Choices) == 0 {
		return Response{}, ErrInvalidResponse
	}

	choice := completion.Choices[0]
	
	// Convert tool calls
	var toolCalls []ToolCall
	if len(choice.Message.ToolCalls) > 0 {
		toolCalls = make([]ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			toolCalls[i] = ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			}
		}
	}
	
	return Response{
		Content:   choice.Message.Content,
		ToolCalls: toolCalls,
		Usage: Usage{
			PromptTokens:     int(completion.Usage.PromptTokens),
			CompletionTokens: int(completion.Usage.CompletionTokens),
			TotalTokens:      int(completion.Usage.TotalTokens),
		},
		FinishReason: string(choice.FinishReason),
		Model:        completion.Model,
	}, nil
}

// StreamGenerate creates a streaming completion for the given prompt
func (a *OpenAIAdapter) StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error) {
	// Apply rate limiting
	if a.rateLimiter != nil {
		if err := a.rateLimiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter error: %w", err)
		}
	}

	// Validate configuration
	if err := a.validateConfig(config); err != nil {
		return nil, err
	}

	// Build messages
	messages := []openai.ChatCompletionMessageParamUnion{}
	
	if config.SystemPrompt != "" {
		messages = append(messages, openai.SystemMessage(config.SystemPrompt))
	}
	
	messages = append(messages, openai.UserMessage(prompt))

	// Build request parameters
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(a.mapModel(config.Model)),
		Messages: messages,
	}
	
	// Some models (o4, o1) don't support temperature parameter
	if !strings.HasPrefix(config.Model, "o4") && !strings.HasPrefix(config.Model, "o1") {
		params.Temperature = openai.Float(float64(config.Temperature))
	}

	if config.MaxTokens > 0 {
		// Some models use max_completion_tokens instead of max_tokens
		if strings.HasPrefix(config.Model, "o4") || strings.HasPrefix(config.Model, "o1") {
			params.MaxCompletionTokens = openai.Int(int64(config.MaxTokens))
		} else {
			params.MaxTokens = openai.Int(int64(config.MaxTokens))
		}
	}

	if config.TopP > 0 {
		params.TopP = openai.Float(float64(config.TopP))
	}

	if len(config.StopSequences) > 0 {
		// OpenAI API accepts stop sequences as a slice or a single string
		if len(config.StopSequences) == 1 {
			params.Stop = openai.ChatCompletionNewParamsStopUnion{OfString: openai.String(config.StopSequences[0])}
		} else {
			params.Stop = openai.ChatCompletionNewParamsStopUnion{OfStringArray: config.StopSequences}
		}
	}

	// Create stream
	stream := a.client.Chat.Completions.NewStreaming(ctx, params)

	// Create output channel
	tokenChan := make(chan Token)

	// Start goroutine to read from stream
	go func() {
		defer close(tokenChan)

		for stream.Next() {
			chunk := stream.Current()
			
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				select {
				case tokenChan <- Token{Content: chunk.Choices[0].Delta.Content}:
				case <-ctx.Done():
					return
				}
			}
		}

		if err := stream.Err(); err != nil {
			select {
			case tokenChan <- Token{Error: err}:
			case <-ctx.Done():
			}
		}
	}()

	return tokenChan, nil
}

// validateConfig validates the configuration
func (a *OpenAIAdapter) validateConfig(config Config) error {
	if config.Model == "" {
		return fmt.Errorf("%w: model is required", ErrInvalidConfig)
	}

	if config.Temperature < 0 || config.Temperature > 2 {
		return fmt.Errorf("%w: temperature must be between 0 and 2", ErrInvalidConfig)
	}

	if config.TopP < 0 || config.TopP > 1 {
		return fmt.Errorf("%w: top_p must be between 0 and 1", ErrInvalidConfig)
	}

	if config.MaxTokens < 0 {
		return fmt.Errorf("%w: max_tokens must be non-negative", ErrInvalidConfig)
	}

	if config.ResponseFormat != "" && config.ResponseFormat != "text" && config.ResponseFormat != "json" {
		return fmt.Errorf("%w: response_format must be 'text' or 'json'", ErrInvalidConfig)
	}

	return nil
}

// mapModel maps generic model names to OpenAI model names
func (a *OpenAIAdapter) mapModel(model string) string {
	// The new SDK uses string types for models, so we can just return the model name
	// All model names are passed through directly
	return model
}

// handleError converts OpenAI errors to adapter errors
func (a *OpenAIAdapter) handleError(err error) error {
	if err == nil {
		return nil
	}

	// Check for specific error patterns in the error message
	errStr := err.Error()
	switch {
	case strings.Contains(errStr, "rate_limit"):
		return ErrRateLimitExceeded
	case strings.Contains(errStr, "context_length_exceeded"):
		return ErrContextLengthExceeded
	case strings.Contains(errStr, "model_not_found"):
		return ErrModelNotSupported
	case strings.Contains(errStr, "invalid_api_key"):
		return ErrAPIKeyMissing
	default:
		return fmt.Errorf("openai api error: %w", err)
	}
}