package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	f2 "github.com/colony-2/c2j/pkg/file"
	llmadapters "github.com/colony-2/colony2/server/llm/adapters"
)

// EnhancedLLMTask executes an LLM generation task with persona mode and file inclusion support
func EnhancedLLMTask(ctx context.Context, input EnhancedLLMTaskInput, registry llmadapters.Registry, previousOutputs map[string]interface{}) (*LLMTaskOutput, error) {
	// Validate basic input
	if input.Prompt == "" {
		return nil, fmt.Errorf("prompt cannot be empty")
	}
	if input.ModelName == "" {
		return nil, fmt.Errorf("model name cannot be empty")
	}
	if input.AdapterName == "" {
		return nil, fmt.Errorf("adapter name cannot be empty")
	}

	// Validate mode-specific configuration early
	if input.Mode == ModePersona {
		if err := ValidatePersonaConfig(input.Persona); err != nil {
			return nil, fmt.Errorf("persona validation failed: %w", err)
		}
	}

	// Get adapter from registry
	adapter, err := registry.Get(input.AdapterName)
	if err != nil {
		return nil, fmt.Errorf("failed to get adapter: %w", err)
	}

	// Check if adapter supports file handling
	fileAdapter, supportsFiles := adapter.(llmadapters.FileAdapter)

	// Build base configuration
	config := llmadapters.Config{
		Model:         input.ModelName,
		Temperature:   input.Temperature,
		MaxTokens:     input.MaxTokens,
		TopP:          input.TopP,
		StopSequences: input.StopSequences,
		Metadata:      input.Metadata,
	}

	// Handle mode-specific configuration
	var finalPrompt string
	switch input.Mode {
	case ModePersona:

		// Build system prompt from persona
		config.SystemPrompt = BuildPersonaSystemPrompt(input.Persona)
		finalPrompt = input.Prompt

	case ModeSimple:
		// Use direct system prompt if provided
		config.SystemPrompt = input.SystemPrompt
		finalPrompt = input.Prompt

	default:
		// Default to simple mode
		config.SystemPrompt = input.SystemPrompt
		finalPrompt = input.Prompt
	}

	// Resolve files if context is provided
	var files []f2.File
	if input.Context != nil {
		resolvedFiles, err := resolveArtifacts(ctx, input.Context, previousOutputs)
		if err != nil {
			// Log warning but continue
			fmt.Printf("Warning: failed to resolve artifacts: %v\n", err)
		} else if len(resolvedFiles) > 0 {
			// Convert resolved files to adapter format
			files = convertToAdapterFiles(resolvedFiles)

			// If adapter doesn't support files, fall back to text inclusion
			if !supportsFiles && len(files) > 0 {
				fileContext := buildFileContext(resolvedFiles, input.AdapterName)
				if fileContext != "" {
					finalPrompt = fmt.Sprintf("%s\n\n%s", finalPrompt, fileContext)
				}
				files = nil // Clear files since we're using text fallback
			}
		}
	}

	// Handle structured response if provided
	if input.ResponseStructure != nil {
		config.ResponseFormat = "json"

		// Add schema information to the prompt
		schemaJSON, err := generateJSONSchema(input.ResponseStructure)
		if err != nil {
			return nil, fmt.Errorf("failed to generate JSON schema: %w", err)
		}

		finalPrompt = fmt.Sprintf("%s\n\nPlease respond with a JSON object that matches this schema:\n%s",
			finalPrompt, string(schemaJSON))
	} else if len(input.ResponseStructureJSON) > 0 {
		// If JSON schema was provided directly
		config.ResponseFormat = "json"
		finalPrompt = fmt.Sprintf("%s\n\nPlease respond with a JSON object that matches this schema:\n%s",
			finalPrompt, string(input.ResponseStructureJSON))
	}

	// Generate response
	var response llmadapters.Response
	if supportsFiles && len(files) > 0 {
		// Use file-aware generation
		response, err = fileAdapter.GenerateWithFiles(ctx, finalPrompt, files, config)
		if err != nil {
			return nil, fmt.Errorf("generation with files failed: %w", err)
		}
	} else {
		// Use standard generation
		response, err = adapter.Generate(ctx, finalPrompt, config)
		if err != nil {
			return nil, fmt.Errorf("generation failed: %w", err)
		}
	}

	// Process response (always return a string)
	var finalResponse string
	if config.ResponseFormat == "json" && input.ResponseStructure != nil {
		// Parse JSON into the provided structure for validation, then re-marshal to canonical string
		if err := json.Unmarshal([]byte(response.Content), input.ResponseStructure); err != nil {
			return nil, fmt.Errorf("failed to parse JSON response: %w", err)
		}
		b, err := json.Marshal(input.ResponseStructure)
		if err != nil {
			return nil, fmt.Errorf("failed to normalize JSON response: %w", err)
		}
		finalResponse = string(b)
	} else if config.ResponseFormat == "json" {
		// Parse as generic JSON to validate, then re-marshal to string
		var jsonResponse interface{}
		if err := json.Unmarshal([]byte(response.Content), &jsonResponse); err != nil {
			return nil, fmt.Errorf("failed to parse JSON response: %w", err)
		}
		b, err := json.Marshal(jsonResponse)
		if err != nil {
			return nil, fmt.Errorf("failed to normalize JSON response: %w", err)
		}
		finalResponse = string(b)
	} else {
		// Plain text response
		finalResponse = response.Content
	}

	// Calculate estimated cost
	estimatedCost := calculateCost(input.ModelName, response.Usage)

	// Build output
	output := &LLMTaskOutput{
		Response: finalResponse,
		Telemetry: LLMTelemetry{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
			EstimatedCostUSD: estimatedCost,
		},
		Model:        response.Model,
		FinishReason: response.FinishReason,
	}

	return output, nil
}

// resolveArtifacts resolves all file paths from configuration
func resolveArtifacts(ctx context.Context, config *FileContext, previousOutputs map[string]interface{}) ([]ResolvedFile, error) {
	paths := []string{}
	labels := map[string]string{}

	// Static artifacts
	for _, artifact := range config.Artifacts {
		paths = append(paths, artifact.Path)
		if artifact.Label != "" {
			labels[artifact.Path] = artifact.Label
		}
	}

	// Get artifacts from previous activity output
	if ref := config.ArtifactsFromOutput; ref != "" {
		parts := strings.Split(ref, ".")
		if len(parts) == 2 {
			if output, ok := previousOutputs[parts[0]]; ok {
				// Try to extract file paths from the output
				if outputMap, ok := output.(map[string]interface{}); ok {
					if files, ok := outputMap[parts[1]].([]string); ok {
						paths = append(paths, files...)
					} else if files, ok := outputMap[parts[1]].([]interface{}); ok {
						for _, f := range files {
							if path, ok := f.(string); ok {
								paths = append(paths, path)
							}
						}
					}
				}
			}
		}
	}

	// Apply glob patterns
	for _, glob := range config.ArtifactsGlob {
		matches, err := filepath.Glob(glob.Pattern)
		if err != nil {
			continue // Skip invalid patterns
		}
		for _, match := range matches {
			// Check exclusions
			excluded := false
			for _, exclude := range glob.Exclude {
				if matched, _ := filepath.Match(exclude, match); matched {
					excluded = true
					break
				}
			}
			if !excluded {
				paths = append(paths, match)
			}
		}
	}

	// Directory listing
	if dir := config.ArtifactsDirectory; dir != nil {
		dirPaths, err := listDirectory(dir.Path, dir.Recursive, dir.Extensions)
		if err == nil {
			paths = append(paths, dirPaths...)
		}
	}

	// Combined resolution
	if res := config.ArtifactsResolution; res != nil {
		for _, source := range res.Sources {
			sourcePaths := resolveSource(source, previousOutputs)
			paths = append(paths, sourcePaths...)
		}
	}

	// Deduplicate paths
	paths = DeduplicatePaths(paths)

	// Read files and create ResolvedFile objects
	resolvedFiles := []ResolvedFile{}
	for _, path := range paths {
		content, err := ioutil.ReadFile(path)
		if err != nil {
			continue // Skip files that can't be read
		}

		mimeType := DetectMimeType(path, content)

		resolved := ResolvedFile{
			Path:     path,
			Label:    labels[path],
			Content:  content,
			MimeType: mimeType,
			Size:     int64(len(content)),
		}

		// Apply size limits
		if config.FileLimits != nil {
			if resolved.Size > config.FileLimits.MaxFileSize {
				if config.FileLimits.TruncateLargeFiles {
					resolved.Content = TruncateContent(resolved.Content, config.FileLimits.MaxFileSize, config.FileLimits.TruncationMessage)
					resolved.Size = int64(len(resolved.Content))
				} else {
					continue // Skip file
				}
			}
		}

		resolvedFiles = append(resolvedFiles, resolved)
	}

	// Apply total limits
	if config.FileLimits != nil {
		resolvedFiles = ApplyTotalLimits(resolvedFiles, config.FileLimits)
	}

	return resolvedFiles, nil
}

// listDirectory lists files in a directory
func listDirectory(path string, recursive bool, extensions []string) ([]string, error) {
	var files []string

	if recursive {
		err := filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip errors
			}

			// Skip directories
			if info.IsDir() {
				return nil
			}

			// Check extensions if specified
			if len(extensions) > 0 {
				ext := filepath.Ext(filePath)
				found := false
				for _, allowedExt := range extensions {
					if ext == allowedExt {
						found = true
						break
					}
				}
				if !found {
					return nil
				}
			}

			files = append(files, filePath)
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		// Non-recursive directory listing
		entries, err := ioutil.ReadDir(path)
		if err != nil {
			return nil, err
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			filePath := filepath.Join(path, entry.Name())

			// Check extensions if specified
			if len(extensions) > 0 {
				ext := filepath.Ext(entry.Name())
				found := false
				for _, allowedExt := range extensions {
					if ext == allowedExt {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}

			files = append(files, filePath)
		}
	}

	return files, nil
}

// resolveSource resolves paths from a single source
func resolveSource(source ResolutionSource, previousOutputs map[string]interface{}) []string {
	var paths []string

	switch source.Type {
	case "activity_output":
		if output, ok := previousOutputs[source.Activity]; ok {
			if outputMap, ok := output.(map[string]interface{}); ok {
				if files, ok := outputMap[source.Field].([]string); ok {
					paths = append(paths, files...)
				} else if files, ok := outputMap[source.Field].([]interface{}); ok {
					for _, f := range files {
						if path, ok := f.(string); ok {
							paths = append(paths, path)
						}
					}
				}
			}
		}

	case "glob":
		for _, pattern := range source.Patterns {
			matches, err := filepath.Glob(pattern)
			if err == nil {
				paths = append(paths, matches...)
			}
		}

	case "directory":
		if source.Path != "" {
			dirPaths, err := listDirectory(source.Path, source.Recursive, nil)
			if err == nil {
				// Apply filter if specified
				if source.Filter != "" {
					filtered := []string{}
					for _, path := range dirPaths {
						if matched, _ := filepath.Match(source.Filter, filepath.Base(path)); matched {
							filtered = append(filtered, path)
						}
					}
					dirPaths = filtered
				}
				paths = append(paths, dirPaths...)
			}
		}

	case "static":
		paths = append(paths, source.Paths...)
	}

	return paths
}

// buildFileContext builds the file context string for the prompt
func buildFileContext(files []ResolvedFile, adapterName string) string {
	if len(files) == 0 {
		return ""
	}

	var parts []string
	parts = append(parts, "### File Context ###")

	for _, file := range files {
		label := file.Label
		if label == "" {
			label = file.Path
		}

		// Check if it's an image or binary file
		if strings.HasPrefix(file.MimeType, "image/") {
			// For providers that support images, we would handle this differently
			// For now, just note that an image is included
			parts = append(parts, fmt.Sprintf("\n[Image: %s]", label))
		} else if file.MimeType == "application/pdf" {
			// For PDFs, note that it's included
			parts = append(parts, fmt.Sprintf("\n[PDF Document: %s]", label))
		} else {
			// Text content - include directly
			parts = append(parts, fmt.Sprintf("\nFile: %s", label))
			parts = append(parts, "```")
			parts = append(parts, string(file.Content))
			parts = append(parts, "```")
		}
	}

	return strings.Join(parts, "\n")
}

// ProviderFacade interface for provider-specific file handling
type ProviderFacade interface {
	// AddFileContext adds file context to the provider
	AddFileContext(ctx context.Context, files []ResolvedFile) error

	// SupportsFileType checks if the provider supports a specific file type
	SupportsFileType(mimeType string) bool

	// GetMaxFileSize returns the maximum file size supported
	GetMaxFileSize() int64

	// ClearContext clears any stored file context
	ClearContext() error
}

// convertToAdapterFiles converts resolved files to adapter format
func convertToAdapterFiles(resolvedFiles []ResolvedFile) []f2.File {
	files := make([]f2.File, 0, len(resolvedFiles))

	for _, rf := range resolvedFiles {
		file := f2.File{
			Path:     rf.Path,
			Name:     rf.Label,
			Content:  rf.Content,
			MimeType: rf.MimeType,
			Type:     llmadapters.GetFileType(rf.MimeType),
			Metadata: rf.Metadata,
		}

		// Use label as name if available
		if file.Name == "" {
			file.Name = filepath.Base(rf.Path)
		}

		files = append(files, file)
	}

	return files
}
