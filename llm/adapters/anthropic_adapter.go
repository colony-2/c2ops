package llmadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"golang.org/x/time/rate"
)

// AnthropicAdapter implements the Adapter interface for Anthropic Claude API
type AnthropicAdapter struct {
	client      anthropic.Client
	rateLimiter *rate.Limiter
}

// NewAnthropicAdapter creates a new Anthropic adapter
func NewAnthropicAdapter(apiKey string) (*AnthropicAdapter, error) {
	if apiKey == "" {
		// Try to get from environment variable
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return nil, ErrAPIKeyMissing
		}
	}

	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	// Create rate limiter: 50 requests per minute
	rateLimiter := rate.NewLimiter(rate.Limit(50.0/60), 50)

	return &AnthropicAdapter{
		client:      client,
		rateLimiter: rateLimiter,
	}, nil
}

// Generate creates a completion for the given prompt
func (a *AnthropicAdapter) Generate(ctx context.Context, prompt string, config Config) (Response, error) {
	// Apply rate limiting
	if a.rateLimiter != nil {
		if err := a.rateLimiter.Wait(ctx); err != nil {
			return Response{}, fmt.Errorf("rate limiter error: %w", err)
		}
	}

	// Validate and adjust configuration
	if err := a.validateConfig(&config); err != nil {
		return Response{}, err
	}

	// Build messages
	messages := []anthropic.MessageParam{}
	messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)))

	// Build request parameters
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.mapModel(config.Model)),
		Messages:  messages,
		MaxTokens: int64(config.MaxTokens),
	}

	// Set system prompt if provided
	if config.SystemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{
				Text: config.SystemPrompt,
				Type: "text",
			},
		}
	}

	// Set temperature if not default
	if config.Temperature > 0 {
		params.Temperature = param.NewOpt(float64(config.Temperature))
	}

	// Set top_p if provided
	if config.TopP > 0 {
		params.TopP = param.NewOpt(float64(config.TopP))
	}

	// Set stop sequences if provided
	if len(config.StopSequences) > 0 {
		params.StopSequences = config.StopSequences
	}

	// Handle structured output with tool use
	if config.ResponseFormat == "json" && len(config.ResponseSchema) > 0 {
		// Parse the schema
		var schemaMap map[string]interface{}
		if err := json.Unmarshal(config.ResponseSchema, &schemaMap); err != nil {
			return Response{}, fmt.Errorf("invalid response schema: %w", err)
		}

		// Extract properties and required fields from schema
		properties, _ := schemaMap["properties"]
		required := []string{}
		if req, ok := schemaMap["required"].([]interface{}); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
		}

		// Create the input schema
		inputSchema := anthropic.ToolInputSchemaParam{
			Type:       "object",
			Properties: properties,
			Required:   required,
		}

		// Create a tool that enforces the schema
		tool := anthropic.ToolParam{
			Name:        "json_response",
			Description: param.NewOpt("Return the response in the specified JSON format"),
			InputSchema: inputSchema,
		}
		
		params.Tools = []anthropic.ToolUnionParam{
			{OfTool: &tool},
		}
		
		// Force tool use by setting tool_choice
		params.ToolChoice = anthropic.ToolChoiceParamOfTool("json_response")
	}

	// Make API call
	message, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, a.handleError(err)
	}

	// Extract content
	content := a.extractContent(message)
	
	// If we used tool for structured output, extract the tool response
	if config.ResponseFormat == "json" && len(config.ResponseSchema) > 0 {
		toolCalls := a.extractToolCalls(message)
		if len(toolCalls) > 0 && toolCalls[0].Name == "json_response" {
			content = string(toolCalls[0].Arguments)
		}
	}

	return Response{
		Content: content,
		Usage: Usage{
			PromptTokens:     int(message.Usage.InputTokens),
			CompletionTokens: int(message.Usage.OutputTokens),
			TotalTokens:      int(message.Usage.InputTokens + message.Usage.OutputTokens),
		},
		FinishReason: string(message.StopReason),
		Model:        string(message.Model),
	}, nil
}

// GenerateWithTools creates a completion with tool/function calling support
func (a *AnthropicAdapter) GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error) {
	// Apply rate limiting
	if a.rateLimiter != nil {
		if err := a.rateLimiter.Wait(ctx); err != nil {
			return Response{}, fmt.Errorf("rate limiter error: %w", err)
		}
	}

	// Validate and adjust configuration
	if err := a.validateConfig(&config); err != nil {
		return Response{}, err
	}

	// Build messages
	messages := []anthropic.MessageParam{}
	messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)))

	// Convert tools to Anthropic format
	anthropicTools := make([]anthropic.ToolUnionParam, len(tools))
	for i, tool := range tools {
		var schemaMap map[string]interface{}
		if err := json.Unmarshal(tool.Parameters, &schemaMap); err != nil {
			return Response{}, fmt.Errorf("failed to parse tool parameters: %w", err)
		}

		// Extract properties and required fields from schema
		properties, _ := schemaMap["properties"]
		required := []string{}
		if req, ok := schemaMap["required"].([]interface{}); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
		}

		// Create the input schema
		inputSchema := anthropic.ToolInputSchemaParam{
			Type:       "object",
			Properties: properties,
			Required:   required,
		}

		toolParam := anthropic.ToolParam{
			Name:        tool.Name,
			Description: param.NewOpt(tool.Description),
			InputSchema: inputSchema,
		}
		
		anthropicTools[i] = anthropic.ToolUnionParam{
			OfTool: &toolParam,
		}
	}

	// Build request parameters
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.mapModel(config.Model)),
		Messages:  messages,
		MaxTokens: int64(config.MaxTokens),
		Tools:     anthropicTools,
	}

	// Set system prompt if provided
	if config.SystemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{
				Text: config.SystemPrompt,
				Type: "text",
			},
		}
	}

	// Set temperature if not default
	if config.Temperature > 0 {
		params.Temperature = param.NewOpt(float64(config.Temperature))
	}

	// Set top_p if provided
	if config.TopP > 0 {
		params.TopP = param.NewOpt(float64(config.TopP))
	}

	// Make API call
	message, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, a.handleError(err)
	}

	// Extract content and tool calls
	content := a.extractContent(message)
	toolCalls := a.extractToolCalls(message)

	return Response{
		Content:   content,
		ToolCalls: toolCalls,
		Usage: Usage{
			PromptTokens:     int(message.Usage.InputTokens),
			CompletionTokens: int(message.Usage.OutputTokens),
			TotalTokens:      int(message.Usage.InputTokens + message.Usage.OutputTokens),
		},
		FinishReason: string(message.StopReason),
		Model:        string(message.Model),
	}, nil
}

// StreamGenerate creates a streaming completion for the given prompt
func (a *AnthropicAdapter) StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error) {
	// Apply rate limiting
	if a.rateLimiter != nil {
		if err := a.rateLimiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter error: %w", err)
		}
	}

	// Validate and adjust configuration
	if err := a.validateConfig(&config); err != nil {
		return nil, err
	}

	// Build messages
	messages := []anthropic.MessageParam{}
	messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)))

	// Build request parameters
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.mapModel(config.Model)),
		Messages:  messages,
		MaxTokens: int64(config.MaxTokens),
	}

	// Set system prompt if provided
	if config.SystemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{
				Text: config.SystemPrompt,
				Type: "text",
			},
		}
	}

	// Set temperature if not default
	if config.Temperature > 0 {
		params.Temperature = param.NewOpt(float64(config.Temperature))
	}

	// Set top_p if provided
	if config.TopP > 0 {
		params.TopP = param.NewOpt(float64(config.TopP))
	}

	// Set stop sequences if provided
	if len(config.StopSequences) > 0 {
		params.StopSequences = config.StopSequences
	}

	// Create stream
	stream := a.client.Messages.NewStreaming(ctx, params)

	// Create output channel
	tokenChan := make(chan Token)

	// Start goroutine to read from stream
	go func() {
		defer close(tokenChan)

		for stream.Next() {
			event := stream.Current()

			// Handle different event types
			switch event.Type {
			case "content_block_delta":
				if event.Delta.Text != "" {
					select {
					case tokenChan <- Token{Content: event.Delta.Text}:
					case <-ctx.Done():
						return
					}
				}
			case "message_stop":
				// Stream is ending
				return
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

// validateConfig validates and adjusts the configuration
func (a *AnthropicAdapter) validateConfig(config *Config) error {
	if config.Model == "" {
		return fmt.Errorf("%w: model is required", ErrInvalidConfig)
	}

	if config.Temperature < 0 || config.Temperature > 1 {
		return fmt.Errorf("%w: temperature must be between 0 and 1", ErrInvalidConfig)
	}

	if config.TopP < 0 || config.TopP > 1 {
		return fmt.Errorf("%w: top_p must be between 0 and 1", ErrInvalidConfig)
	}

	// Anthropic requires max_tokens to be set
	if config.MaxTokens == 0 {
		config.MaxTokens = 1024 // Default value
	}

	if config.MaxTokens < 1 {
		return fmt.Errorf("%w: max_tokens must be at least 1", ErrInvalidConfig)
	}

	return nil
}

// mapModel maps generic model names to Anthropic model names
func (a *AnthropicAdapter) mapModel(model string) string {
	// Map common model names to Anthropic model IDs
	switch model {
	case "claude-3-opus":
		return "claude-3-opus-20240229"
	case "claude-3-sonnet":
		return "claude-3-5-sonnet-20241022"
	case "claude-3-haiku":
		return "claude-3-5-haiku-20241022"
	case "claude-2.1":
		return "claude-2.1"
	case "claude-2":
		return "claude-2.0"
	case "claude-instant":
		return "claude-instant-1.2"
	default:
		// Pass through any specific model version
		return model
	}
}

// extractContent extracts text content from Anthropic message
func (a *AnthropicAdapter) extractContent(message *anthropic.Message) string {
	var content strings.Builder
	
	for _, block := range message.Content {
		if block.Type == "text" {
			content.WriteString(block.Text)
		}
	}
	
	return content.String()
}

// extractToolCalls extracts tool calls from Anthropic message
func (a *AnthropicAdapter) extractToolCalls(message *anthropic.Message) []ToolCall {
	var toolCalls []ToolCall
	
	for _, block := range message.Content {
		if block.Type == "tool_use" {
			// Convert input to JSON
			inputJSON, err := json.Marshal(block.Input)
			if err != nil {
				// If we can't marshal, use empty JSON
				inputJSON = []byte("{}")
			}
			
			toolCalls = append(toolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: json.RawMessage(inputJSON),
			})
		}
	}
	
	return toolCalls
}

// handleError converts Anthropic errors to adapter errors
func (a *AnthropicAdapter) handleError(err error) error {
	if err == nil {
		return nil
	}

	// Check for specific error patterns
	errStr := err.Error()
	switch {
	case strings.Contains(errStr, "rate_limit"):
		return ErrRateLimitExceeded
	case strings.Contains(errStr, "invalid_request_error"):
		return ErrInvalidConfig
	case strings.Contains(errStr, "model_not_found"):
		return ErrModelNotSupported
	case strings.Contains(errStr, "authentication_error"):
		return ErrAPIKeyMissing
	default:
		return fmt.Errorf("anthropic api error: %w", err)
	}
}