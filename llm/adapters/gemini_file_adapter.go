package llmadapters

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	f2 "github.com/colony-2/c2j/pkg/file"
	"google.golang.org/genai"
)

// Ensure GeminiAdapter implements FileAdapter
var _ FileAdapter = (*GeminiAdapter)(nil)

// GenerateWithFiles creates a completion with file context using Gemini's capabilities
func (a *GeminiAdapter) GenerateWithFiles(ctx context.Context, prompt string, files []f2.File, config Config) (Response, error) {
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

	// Build generation config (using GenerateContentConfig directly)
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

	// Handle structured output
	if config.ResponseFormat == "json" {
		genConfig.ResponseMIMEType = "application/json"
	}

	// Build contents with files and system instruction
	if config.SystemPrompt != "" {
		genConfig.SystemInstruction = genai.NewContentFromText(config.SystemPrompt, "")
	}
	contents := a.buildContentsWithFiles(prompt, files)

	// Generate content using the actual SDK method
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

	// Build response
	response := Response{
		Content:      content,
		FinishReason: string(candidate.FinishReason),
		Model:        config.Model,
		Usage: Usage{
			PromptTokens:     int(resp.UsageMetadata.PromptTokenCount),
			CompletionTokens: int(resp.UsageMetadata.CandidatesTokenCount),
			TotalTokens:      int(resp.UsageMetadata.TotalTokenCount),
		},
	}

	return response, nil
}

// buildContentsWithFiles builds the content array with embedded files
func (a *GeminiAdapter) buildContentsWithFiles(prompt string, files []f2.File) []*genai.Content {
	// For Gemini, we need to build the content with embedded file data
	// Since the SDK doesn't expose a direct file upload API in the structure we have,
	// we'll embed files as base64 or text in the content

	var parts []string
	parts = append(parts, prompt)

	// Add files as text or base64 embedded content
	for _, file := range files {
		fileName := file.Name
		if fileName == "" {
			fileName = file.Path
		}

		switch file.Type {
		case f2.FileTypeText:
			// Include text content directly
			parts = append(parts, fmt.Sprintf("\n\nFile: %s\n```\n%s\n```", fileName, string(file.Content)))

		case f2.FileTypeImage:
			// For images, include base64 encoded data
			// Note: Gemini models that support images should process this
			base64Data := base64.StdEncoding.EncodeToString(file.Content)
			// Include a truncated version for context (full data might be too large for direct text embedding)
			if len(base64Data) > 1000 {
				parts = append(parts, fmt.Sprintf("\n\n[Image: %s]\n[Base64 data truncated, %d bytes total]", fileName, len(file.Content)))
			} else {
				parts = append(parts, fmt.Sprintf("\n\n[Image: %s]\nBase64: %s", fileName, base64Data))
			}

		case f2.FileTypePDF:
			// For PDFs, include as reference since we can't directly process without Files API
			parts = append(parts, fmt.Sprintf("\n\n[PDF Document: %s]\n[%d bytes]", fileName, len(file.Content)))
			// If it's small enough, we could include the raw text
			if len(file.Content) < 10000 {
				parts = append(parts, fmt.Sprintf("\nContent (as text):\n%s", string(file.Content)))
			}

		case f2.FileTypeAudio, f2.FileTypeVideo:
			// For media files, include metadata only
			parts = append(parts, fmt.Sprintf("\n\n[%s File: %s]\n[Type: %s, Size: %d bytes]",
				file.Type, fileName, file.MimeType, len(file.Content)))

		default:
			// Unknown file type
			parts = append(parts, fmt.Sprintf("\n\n[File: %s (type: %s)]", fileName, file.Type))
		}
	}

	// Combine all parts into a single text content
	fullContent := strings.Join(parts, "")

	// Return as Gemini content format
	return []*genai.Content{
		genai.NewContentFromText(fullContent, genai.RoleUser),
	}
}

// GetFileCapabilities returns Gemini's file handling capabilities
// Note: Without direct Files API access, we're limited to text embedding
func (a *GeminiAdapter) GetFileCapabilities() FileCapabilities {
	return FileCapabilities{
		SupportedTypes: []f2.FileType{
			f2.FileTypeText,
			f2.FileTypeImage, // Limited support via base64 embedding
			f2.FileTypePDF,   // Limited support, reference only
		},
		MaxFileSize:    5 * 1024 * 1024,  // 5MB per file (for text embedding)
		MaxFileCount:   10,               // Reasonable limit
		TotalSizeLimit: 20 * 1024 * 1024, // 20MB total
	}
}

// ValidateFile checks if a file can be processed by Gemini
func (a *GeminiAdapter) ValidateFile(file f2.File) error {
	caps := a.GetFileCapabilities()

	// Check if file type is supported
	supported := false
	for _, t := range caps.SupportedTypes {
		if file.Type == t {
			supported = true
			break
		}
	}

	// We'll accept other types but with limitations
	if !supported && file.Type != f2.FileTypeAudio && file.Type != f2.FileTypeVideo {
		return fmt.Errorf("file type %s not supported", file.Type)
	}

	// Check file size
	if int64(len(file.Content)) > caps.MaxFileSize {
		return fmt.Errorf("file size %d exceeds maximum %d", len(file.Content), caps.MaxFileSize)
	}

	// Warn about limitations for certain file types
	switch file.Type {
	case f2.FileTypeImage:
		if len(file.Content) > 1024*1024 { // 1MB
			fmt.Printf("Warning: Large image file %s may not be fully processed without Files API\n", file.Path)
		}
	case f2.FileTypePDF:
		fmt.Printf("Warning: PDF file %s will be processed as text reference without Files API\n", file.Path)
	case f2.FileTypeAudio, f2.FileTypeVideo:
		fmt.Printf("Warning: Media file %s will only include metadata without Files API\n", file.Path)
	}

	return nil
}
