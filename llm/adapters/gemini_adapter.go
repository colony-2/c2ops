package llmadapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/genai"
	"golang.org/x/time/rate"
)

// GeminiAdapter implements the Adapter interface for Google Gemini API
type GeminiAdapter struct {
	client      *genai.Client
	rateLimiter *rate.Limiter
}

// NewGeminiAdapter creates a new Gemini adapter
func NewGeminiAdapter(apiKey string) (*GeminiAdapter, error) {
	if apiKey == "" {
		// Try to get from environment variable
		apiKey = os.Getenv("GEMINI_API_KEY")
		if apiKey == "" {
			return nil, ErrAPIKeyMissing
		}
	}

	ctx := context.Background()
	config := &genai.ClientConfig{
		APIKey: apiKey,
	}
	client, err := genai.NewClient(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}
	
	// Create rate limiter: 60 requests per minute
	rateLimiter := rate.NewLimiter(rate.Limit(1), 60)

	return &GeminiAdapter{
		client:      client,
		rateLimiter: rateLimiter,
	}, nil
}

// Generate creates a completion for the given prompt
func (a *GeminiAdapter) Generate(ctx context.Context, prompt string, config Config) (Response, error) {
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

	// Build generation config
	genConfig := &genai.GenerateContentConfig{}
	if config.Temperature > 0 {
		temp := float32(config.Temperature)
		genConfig.Temperature = &temp
	}
	if config.MaxTokens > 0 {
		genConfig.MaxOutputTokens = int32(config.MaxTokens)
	}
	if config.TopP > 0 {
		topP := float32(config.TopP)
		genConfig.TopP = &topP
	}
	if len(config.StopSequences) > 0 {
		genConfig.StopSequences = config.StopSequences
	}
	if config.ResponseFormat == "json" {
		genConfig.ResponseMIMEType = "application/json"
		
		// If a response schema is provided, use native structured output
		if len(config.ResponseSchema) > 0 {
			// Use ResponseJsonSchema for JSON Schema format
			var schema interface{}
			if err := json.Unmarshal(config.ResponseSchema, &schema); err != nil {
				return Response{}, fmt.Errorf("invalid response schema: %w", err)
			}
			genConfig.ResponseJsonSchema = schema
		}
	}

	// Build contents and add system instruction
	if config.SystemPrompt != "" {
		genConfig.SystemInstruction = genai.NewContentFromText(config.SystemPrompt, "")
	}
	contents := []*genai.Content{
		genai.NewContentFromText(prompt, genai.RoleUser),
	}

	// Generate content
	modelName := "models/" + a.mapModel(config.Model)
	resp, err := a.client.Models.GenerateContent(ctx, modelName, contents, genConfig)
	if err != nil {
		return Response{}, a.handleError(err)
	}

	// Extract content from response
	if len(resp.Candidates) == 0 {
		return Response{}, ErrInvalidResponse
	}

	candidate := resp.Candidates[0]
	content := a.extractContent(candidate.Content)

	// Create response
	return Response{
		Content: content,
		Usage: Usage{
			PromptTokens:     int(resp.UsageMetadata.PromptTokenCount),
			CompletionTokens: int(resp.UsageMetadata.CandidatesTokenCount),
			TotalTokens:      int(resp.UsageMetadata.TotalTokenCount),
		},
		FinishReason: string(candidate.FinishReason),
		Model:        config.Model,
	}, nil
}

// GenerateWithTools creates a completion with tool/function calling support
func (a *GeminiAdapter) GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error) {
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

	// Build generation config
	genConfig := &genai.GenerateContentConfig{}
	if config.Temperature > 0 {
		temp := float32(config.Temperature)
		genConfig.Temperature = &temp
	}
	if config.MaxTokens > 0 {
		genConfig.MaxOutputTokens = int32(config.MaxTokens)
	}
	if config.TopP > 0 {
		topP := float32(config.TopP)
		genConfig.TopP = &topP
	}
	if len(config.StopSequences) > 0 {
		genConfig.StopSequences = config.StopSequences
	}

	// Build contents and add system instruction
	if config.SystemPrompt != "" {
		genConfig.SystemInstruction = genai.NewContentFromText(config.SystemPrompt, "")
	}
	contents := []*genai.Content{
		genai.NewContentFromText(prompt, genai.RoleUser),
	}

	// Convert tools to Gemini format
	genaiTools := []*genai.Tool{}
	for _, tool := range tools {
		// Parse parameters as JSON schema
		var params map[string]interface{}
		if err := json.Unmarshal(tool.Parameters, &params); err != nil {
			return Response{}, fmt.Errorf("failed to parse tool parameters: %w", err)
		}

		// Convert properties to Schema format
		properties := make(map[string]*genai.Schema)
		if props, ok := params["properties"].(map[string]interface{}); ok {
			for propName, propDef := range props {
				if propMap, ok := propDef.(map[string]interface{}); ok {
					schema := &genai.Schema{}
					if t, ok := propMap["type"].(string); ok {
						schema.Type = genai.Type(strings.ToUpper(t))
					}
					if desc, ok := propMap["description"].(string); ok {
						schema.Description = desc
					}
					properties[propName] = schema
				}
			}
		}

		genaiTool := &genai.Tool{
			FunctionDeclarations: []*genai.FunctionDeclaration{
				{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters: &genai.Schema{
						Type:       genai.TypeObject,
						Properties: properties,
						Required:   convertToStringSlice(params["required"]),
					},
				},
			},
		}
		genaiTools = append(genaiTools, genaiTool)
	}

	// Set tools in config
	genConfig.Tools = genaiTools

	// Generate content
	modelName := "models/" + a.mapModel(config.Model)
	resp, err := a.client.Models.GenerateContent(ctx, modelName, contents, genConfig)
	if err != nil {
		return Response{}, a.handleError(err)
	}

	// Extract content from response
	if len(resp.Candidates) == 0 {
		return Response{}, ErrInvalidResponse
	}

	candidate := resp.Candidates[0]
	content := a.extractContent(candidate.Content)
	
	// Extract tool calls
	toolCalls := a.extractToolCalls(candidate.Content)

	// Create response
	return Response{
		Content:   content,
		ToolCalls: toolCalls,
		Usage: Usage{
			PromptTokens:     int(resp.UsageMetadata.PromptTokenCount),
			CompletionTokens: int(resp.UsageMetadata.CandidatesTokenCount),
			TotalTokens:      int(resp.UsageMetadata.TotalTokenCount),
		},
		FinishReason: string(candidate.FinishReason),
		Model:        config.Model,
	}, nil
}

// StreamGenerate creates a streaming completion for the given prompt
func (a *GeminiAdapter) StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error) {
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

	// Build generation config
	genConfig := &genai.GenerateContentConfig{}
	if config.Temperature > 0 {
		temp := float32(config.Temperature)
		genConfig.Temperature = &temp
	}
	if config.MaxTokens > 0 {
		genConfig.MaxOutputTokens = int32(config.MaxTokens)
	}
	if config.TopP > 0 {
		topP := float32(config.TopP)
		genConfig.TopP = &topP
	}
	if len(config.StopSequences) > 0 {
		genConfig.StopSequences = config.StopSequences
	}

	// Build contents and add system instruction
	if config.SystemPrompt != "" {
		genConfig.SystemInstruction = genai.NewContentFromText(config.SystemPrompt, "")
	}
	contents := []*genai.Content{
		genai.NewContentFromText(prompt, genai.RoleUser),
	}

	// Create output channel
	tokenChan := make(chan Token)

	// Start goroutine to handle streaming
	go func() {
		defer close(tokenChan)

		// Use regular generation with manual chunking since genai doesn't expose streaming directly
		modelName := "models/" + a.mapModel(config.Model)
		resp, err := a.client.Models.GenerateContent(ctx, modelName, contents, genConfig)
		if err != nil {
			select {
			case tokenChan <- Token{Error: err}:
			case <-ctx.Done():
			}
			return
		}

		// Send content in chunks
		if len(resp.Candidates) > 0 {
			content := a.extractContent(resp.Candidates[0].Content)
			// Simulate streaming by sending content in chunks
			words := strings.Fields(content)
			for i, word := range words {
				if i > 0 {
					word = " " + word
				}
				select {
				case tokenChan <- Token{Content: word}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return tokenChan, nil
}

// validateConfig validates the configuration
func (a *GeminiAdapter) validateConfig(config Config) error {
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

// mapModel maps generic model names to Gemini model names
func (a *GeminiAdapter) mapModel(model string) string {
	// Map common model names to Gemini model names
	modelMap := map[string]string{
		"gemini-pro":        "gemini-pro",
		"gemini-pro-vision": "gemini-pro-vision",
		"gemini-1.5-pro":    "gemini-1.5-pro",
		"gemini-1.5-flash":  "gemini-1.5-flash",
		"gemini-2.0-flash":  "gemini-2.0-flash",
	}

	if mapped, ok := modelMap[model]; ok {
		return mapped
	}
	return model
}

// extractContent extracts text content from Gemini response
func (a *GeminiAdapter) extractContent(content *genai.Content) string {
	if content == nil || content.Parts == nil {
		return ""
	}

	var result string
	for _, part := range content.Parts {
		// Check for text part
		if part.Text != "" {
			result += part.Text
		}
	}
	return result
}

// extractToolCalls extracts tool calls from Gemini response
func (a *GeminiAdapter) extractToolCalls(content *genai.Content) []ToolCall {
	if content == nil || content.Parts == nil {
		return nil
	}

	var toolCalls []ToolCall
	for _, part := range content.Parts {
		if part.FunctionCall != nil {
			args, _ := json.Marshal(part.FunctionCall.Args)
			toolCalls = append(toolCalls, ToolCall{
				ID:        fmt.Sprintf("call_%d", len(toolCalls)),
				Name:      part.FunctionCall.Name,
				Arguments: json.RawMessage(args),
			})
		}
	}
	return toolCalls
}

// handleError converts Gemini errors to adapter errors
func (a *GeminiAdapter) handleError(err error) error {
	if err == nil {
		return nil
	}

	// Check for specific error patterns
	errStr := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("request timeout: %w", err)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("request canceled: %w", err)
	case containsStr(errStr, "quota"):
		return ErrRateLimitExceeded
	case containsStr(errStr, "model"):
		return ErrModelNotSupported
	case containsStr(errStr, "context"):
		return ErrContextLengthExceeded
	default:
		return fmt.Errorf("gemini api error: %w", err)
	}
}

// containsStr checks if a string contains a substring (case-insensitive)
func containsStr(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// convertToStringSlice converts an interface{} to []string
func convertToStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	
	slice, ok := v.([]interface{})
	if !ok {
		return nil
	}
	
	result := make([]string, 0, len(slice))
	for _, item := range slice {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}