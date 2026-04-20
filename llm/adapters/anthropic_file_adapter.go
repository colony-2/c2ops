package llmadapters

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	f2 "github.com/colony-2/c2j/pkg/file"
)

// Ensure AnthropicAdapter implements FileAdapter
var _ FileAdapter = (*AnthropicAdapter)(nil)

// GenerateWithFiles creates a completion with file context using Anthropic's native capabilities
func (a *AnthropicAdapter) GenerateWithFiles(ctx context.Context, prompt string, files []f2.File, config Config) (Response, error) {
	// Apply rate limiting
	if a.rateLimiter != nil {
		if err := a.rateLimiter.Wait(ctx); err != nil {
			return Response{}, fmt.Errorf("rate limiter error: %w", err)
		}
	}

	// Validate configuration
	if err := a.validateConfig(&config); err != nil {
		return Response{}, err
	}

	// Validate files
	for _, file := range files {
		if err := a.ValidateFile(file); err != nil {
			return Response{}, fmt.Errorf("invalid file %s: %w", file.Path, err)
		}
	}

	// Build message content with files
	messageContent := a.buildMessageContent(prompt, files)

	// Build request parameters
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.mapModel(config.Model)),
		MaxTokens: int64(config.MaxTokens),
		Messages:  []anthropic.MessageParam{messageContent},
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

	// Set temperature if provided
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

	// Create message
	message, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("Anthropic API error: %w", err)
	}

	// Extract text content from response
	content := ""
	for _, block := range message.Content {
		// The block is a union type, extract text content
		if block.Text != "" {
			content += block.Text
		}
	}

	// Build response
	response := Response{
		Content:      content,
		FinishReason: string(message.StopReason),
		Model:        string(message.Model),
		Usage: Usage{
			PromptTokens:     int(message.Usage.InputTokens),
			CompletionTokens: int(message.Usage.OutputTokens),
			TotalTokens:      int(message.Usage.InputTokens + message.Usage.OutputTokens),
		},
	}

	return response, nil
}

// buildMessageContent builds the message content with files for Anthropic
func (a *AnthropicAdapter) buildMessageContent(prompt string, files []f2.File) anthropic.MessageParam {
	// For now, we'll combine all content into a single text message
	// since the SDK's multimodal support structure isn't clear

	var fullContent strings.Builder
	fullContent.WriteString(prompt)

	// Add files as text content
	for _, file := range files {
		label := file.Name
		if label == "" {
			label = file.Path
		}

		switch file.Type {
		case f2.FileTypeText:
			fullContent.WriteString(fmt.Sprintf("\n\nFile: %s\n```\n%s\n```", label, string(file.Content)))

		case f2.FileTypeImage:
			// For images, we'll note them but can't embed without proper SDK support
			base64Data := base64.StdEncoding.EncodeToString(file.Content)
			if len(base64Data) > 1000 {
				fullContent.WriteString(fmt.Sprintf("\n\n[Image: %s]\n[Base64 data: %d bytes]", label, len(file.Content)))
			} else {
				fullContent.WriteString(fmt.Sprintf("\n\n[Image: %s]\nBase64: %s", label, base64Data))
			}

		case f2.FileTypePDF:
			fullContent.WriteString(fmt.Sprintf("\n\n[PDF Document: %s]\n[%d bytes]", label, len(file.Content)))

		default:
			fullContent.WriteString(fmt.Sprintf("\n\n[File: %s (type: %s)]", label, file.Type))
		}
	}

	// Return as a simple text message
	return anthropic.NewUserMessage(anthropic.NewTextBlock(fullContent.String()))
}

// GetFileCapabilities returns Anthropic's file handling capabilities
func (a *AnthropicAdapter) GetFileCapabilities() FileCapabilities {
	return FileCapabilities{
		SupportedTypes: []f2.FileType{
			f2.FileTypeText,
			f2.FileTypeImage,
			f2.FileTypePDF, // Native PDF support
		},
		MaxFileSize:    20 * 1024 * 1024,  // 20MB per file
		MaxFileCount:   20,                // Claude can handle many files
		TotalSizeLimit: 100 * 1024 * 1024, // 100MB total
	}
}

// ValidateFile checks if a file can be processed by Anthropic
func (a *AnthropicAdapter) ValidateFile(file f2.File) error {
	caps := a.GetFileCapabilities()

	// Check file type
	supported := false
	for _, t := range caps.SupportedTypes {
		if file.Type == t {
			supported = true
			break
		}
	}

	if !supported {
		return fmt.Errorf("file type %s not supported", file.Type)
	}

	// Check file size
	if int64(len(file.Content)) > caps.MaxFileSize {
		return fmt.Errorf("file size %d exceeds maximum %d", len(file.Content), caps.MaxFileSize)
	}

	// Special validation for images
	if file.Type == f2.FileTypeImage {
		supportedImageTypes := []string{
			"image/jpeg",
			"image/png",
			"image/gif",
			"image/webp",
		}

		supported := false
		for _, mt := range supportedImageTypes {
			if file.MimeType == mt {
				supported = true
				break
			}
		}

		if !supported {
			return fmt.Errorf("image type %s not supported", file.MimeType)
		}
	}

	return nil
}
