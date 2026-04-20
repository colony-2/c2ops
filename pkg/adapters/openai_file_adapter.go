package llmadapters

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	f2 "github.com/colony-2/c2j/pkg/file"
	"github.com/openai/openai-go"
)

// Ensure OpenAIAdapter implements FileAdapter
var _ FileAdapter = (*OpenAIAdapter)(nil)

// GenerateWithFiles creates a completion with file context using OpenAI's native capabilities
func (a *OpenAIAdapter) GenerateWithFiles(ctx context.Context, prompt string, files []f2.File, config Config) (Response, error) {
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

	// Validate files
	for _, file := range files {
		if err := a.ValidateFile(file); err != nil {
			return Response{}, fmt.Errorf("invalid file %s: %w", file.Path, err)
		}
	}

	// Check if we need to use vision model for images
	hasImages := false
	for _, file := range files {
		if file.Type == f2.FileTypeImage {
			hasImages = true
			break
		}
	}

	// Auto-upgrade to vision model if needed
	modelName := config.Model
	if hasImages && !isVisionModel(modelName) {
		modelName = upgradeToVisionModel(modelName)
	}

	// Build messages with file content
	messages := []openai.ChatCompletionMessageParamUnion{}

	if config.SystemPrompt != "" {
		messages = append(messages, openai.SystemMessage(config.SystemPrompt))
	}

	// Build user message with files
	userMessage := a.buildUserMessageWithFiles(prompt, files)
	messages = append(messages, userMessage)

	// Build request parameters
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(a.mapModel(modelName)),
		Messages: messages,
	}

	// Set other parameters
	if config.Temperature > 0 {
		params.Temperature = openai.Float(float64(config.Temperature))
	}
	if config.MaxTokens > 0 {
		params.MaxTokens = openai.Int(int64(config.MaxTokens))
	}
	if config.TopP > 0 {
		params.TopP = openai.Float(float64(config.TopP))
	}

	// Handle response format
	if config.ResponseFormat == "json" {
		// Simple JSON mode
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &openai.ResponseFormatJSONObjectParam{
				Type: "json_object",
			},
		}
	}

	// Create completion
	completion, err := a.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("OpenAI API error: %w", err)
	}

	// Build response
	if len(completion.Choices) == 0 {
		return Response{}, ErrInvalidResponse
	}

	choice := completion.Choices[0]
	response := Response{
		Content:      choice.Message.Content,
		FinishReason: string(choice.FinishReason),
		Model:        completion.Model,
		Usage: Usage{
			PromptTokens:     int(completion.Usage.PromptTokens),
			CompletionTokens: int(completion.Usage.CompletionTokens),
			TotalTokens:      int(completion.Usage.TotalTokens),
		},
	}

	return response, nil
}

// buildUserMessageWithFiles creates a user message with file content
func (a *OpenAIAdapter) buildUserMessageWithFiles(prompt string, files []f2.File) openai.ChatCompletionMessageParamUnion {
	// For simplicity and compatibility, we'll combine everything into text
	// The OpenAI SDK's multimodal API structure varies between versions

	var fullPrompt strings.Builder
	fullPrompt.WriteString(prompt)

	// Add files as text content
	for _, file := range files {
		label := file.Name
		if label == "" {
			label = file.Path
		}

		switch file.Type {
		case f2.FileTypeText:
			fullPrompt.WriteString(fmt.Sprintf("\n\nFile: %s\n```\n%s\n```", label, string(file.Content)))

		case f2.FileTypeImage:
			// For vision models, we'd need to use the vision API
			// For now, we'll note the image
			base64Data := base64.StdEncoding.EncodeToString(file.Content)
			if len(base64Data) > 1000 {
				fullPrompt.WriteString(fmt.Sprintf("\n\n[Image: %s]\n[Base64 data: %d bytes]", label, len(file.Content)))
			} else {
				fullPrompt.WriteString(fmt.Sprintf("\n\n[Image: %s]\nBase64: %s", label, base64Data))
			}

		case f2.FileTypePDF:
			// Add PDF as text reference
			fullPrompt.WriteString(fmt.Sprintf("\n\n[PDF Document: %s]\n[%d bytes]", label, len(file.Content)))
			// If small enough, include raw content
			if len(file.Content) < 5000 {
				fullPrompt.WriteString(fmt.Sprintf("\nContent (as text):\n```\n%s\n```", string(file.Content)))
			}

		default:
			// Unsupported file type - add as reference
			fullPrompt.WriteString(fmt.Sprintf("\n\n[File: %s (type: %s)]", label, file.Type))
		}
	}

	return openai.UserMessage(fullPrompt.String())
}

// GetFileCapabilities returns OpenAI's file handling capabilities
func (a *OpenAIAdapter) GetFileCapabilities() FileCapabilities {
	return FileCapabilities{
		SupportedTypes: []f2.FileType{
			f2.FileTypeText,
			f2.FileTypeImage, // Only with vision models
		},
		MaxFileSize:    20 * 1024 * 1024,  // 20MB for images
		MaxFileCount:   10,                // Reasonable limit for context
		TotalSizeLimit: 100 * 1024 * 1024, // 100MB total
	}
}

// ValidateFile checks if a file can be processed by OpenAI
func (a *OpenAIAdapter) ValidateFile(file f2.File) error {
	caps := a.GetFileCapabilities()

	// Check file type
	supported := false
	for _, t := range caps.SupportedTypes {
		if file.Type == t {
			supported = true
			break
		}
	}

	// PDF files can be processed as text
	if file.Type == f2.FileTypePDF {
		supported = true
	}

	if !supported {
		return fmt.Errorf("file type %s not supported", file.Type)
	}

	// Check file size
	if int64(len(file.Content)) > caps.MaxFileSize {
		return fmt.Errorf("file size %d exceeds maximum %d", len(file.Content), caps.MaxFileSize)
	}

	return nil
}

// isVisionModel checks if a model supports vision/images
func isVisionModel(model string) bool {
	visionModels := []string{
		"gpt-4-vision-preview",
		"gpt-4-turbo",
		"gpt-4o",
		"gpt-4o-mini",
	}

	for _, vm := range visionModels {
		if model == vm {
			return true
		}
	}
	return false
}

// upgradeToVisionModel upgrades a model to its vision-capable variant
func upgradeToVisionModel(model string) string {
	switch model {
	case "gpt-4":
		return "gpt-4-turbo"
	case "gpt-3.5-turbo":
		return "gpt-4o-mini" // Most cost-effective vision model
	default:
		return "gpt-4o-mini"
	}
}
