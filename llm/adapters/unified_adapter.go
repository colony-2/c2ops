package llmadapters

import (
	"context"
	"encoding/json"
	"fmt"

	f2 "github.com/colony-2/c2j/pkg/file"
)

// UnifiedAdapter combines file and tool capabilities
type UnifiedAdapter interface {
	FileAdapter
	ExecutableToolAdapter

	// GenerateWithFilesAndTools combines both capabilities
	GenerateWithFilesAndTools(
		ctx context.Context,
		prompt string,
		files []f2.File,
		tools []Tool,
		config UnifiedConfig,
	) (UnifiedResponse, error)
}

// UnifiedConfig combines all configuration options
type UnifiedConfig struct {
	ExecutableToolConfig
	FileHandling FileHandlingMode `json:"file_handling"`
}

// UnifiedResponse combines all response types
type UnifiedResponse struct {
	ExecutableToolResponse
	FilesProcessed []string `json:"files_processed"`
	FilesCreated   []string `json:"files_created,omitempty"`
	FilesModified  []string `json:"files_modified,omitempty"`
	FilesDeleted   []string `json:"files_deleted,omitempty"`
}

// BaseUnifiedAdapter provides a base implementation of UnifiedAdapter
type BaseUnifiedAdapter struct {
	fileAdapter   FileAdapter
	execAdapter   ExecutableToolAdapter
	fileCollector FileCollector
	secretFilter  SecretFilter
}

// NewUnifiedAdapter creates a unified adapter from file and executable adapters
func NewUnifiedAdapter(fileAdapter FileAdapter, execAdapter ExecutableToolAdapter) UnifiedAdapter {
	// If the adapters are the same instance (provider-specific unified adapter),
	// just return it wrapped
	if unifiedProvider, ok := fileAdapter.(UnifiedAdapter); ok {
		return unifiedProvider
	}

	return &BaseUnifiedAdapter{
		fileAdapter:   fileAdapter,
		execAdapter:   execAdapter,
		fileCollector: NewFileCollector(),
		secretFilter:  NewSecretFilter(),
	}
}

// Delegated methods from FileAdapter
func (a *BaseUnifiedAdapter) Generate(ctx context.Context, prompt string, config Config) (Response, error) {
	return a.fileAdapter.Generate(ctx, prompt, config)
}

func (a *BaseUnifiedAdapter) GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error) {
	return a.fileAdapter.GenerateWithTools(ctx, prompt, tools, config)
}

func (a *BaseUnifiedAdapter) StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error) {
	return a.fileAdapter.StreamGenerate(ctx, prompt, config)
}

func (a *BaseUnifiedAdapter) GenerateWithFiles(ctx context.Context, prompt string, files []f2.File, config Config) (Response, error) {
	return a.fileAdapter.GenerateWithFiles(ctx, prompt, files, config)
}

func (a *BaseUnifiedAdapter) GetFileCapabilities() FileCapabilities {
	return a.fileAdapter.GetFileCapabilities()
}

func (a *BaseUnifiedAdapter) ValidateFile(file f2.File) error {
	return a.fileAdapter.ValidateFile(file)
}

// Delegated methods from ExecutableToolAdapter
func (a *BaseUnifiedAdapter) GenerateAndExecuteTools(ctx context.Context, prompt string, tools []Tool, config ExecutableToolConfig) (ExecutableToolResponse, error) {
	return a.execAdapter.GenerateAndExecuteTools(ctx, prompt, tools, config)
}

func (a *BaseUnifiedAdapter) SetToolExecutor(executor ToolExecutor) {
	a.execAdapter.SetToolExecutor(executor)
}

// GenerateWithFilesAndTools combines file and tool capabilities
func (a *BaseUnifiedAdapter) GenerateWithFilesAndTools(
	ctx context.Context,
	prompt string,
	files []f2.File,
	tools []Tool,
	config UnifiedConfig,
) (UnifiedResponse, error) {
	response := UnifiedResponse{
		FilesProcessed: []string{},
		FilesCreated:   []string{},
		FilesModified:  []string{},
		FilesDeleted:   []string{},
	}

	// Process files for context
	processedFiles, err := a.processFilesForContext(files, config)
	if err != nil {
		return response, fmt.Errorf("failed to process files: %w", err)
	}

	// Track processed files
	for _, file := range processedFiles {
		response.FilesProcessed = append(response.FilesProcessed, file.Path)
	}

	// Build enhanced prompt with file context
	enhancedPrompt := a.buildPromptWithFileContext(prompt, processedFiles)

	// Add file operation tools if files are provided
	if len(files) > 0 {
		tools = a.addFileOperationTools(tools, config)
	}

	// Execute with tools and files
	if config.AutoExecute {
		// Use tool execution flow
		execResponse, err := a.execAdapter.GenerateAndExecuteTools(
			ctx,
			enhancedPrompt,
			tools,
			config.ExecutableToolConfig,
		)
		if err != nil {
			return response, err
		}

		response.ExecutableToolResponse = execResponse

		// Track file operations from tool results
		a.trackFileOperations(&response, execResponse.ToolResults)

	} else {
		// Just generate with tools, no execution
		baseResponse, err := a.GenerateWithTools(ctx, enhancedPrompt, tools, config.Config)
		if err != nil {
			return response, err
		}

		response.Response = baseResponse
	}

	// Add metadata about the operation
	if response.ExecutionMetadata == nil {
		response.ExecutionMetadata = make(map[string]interface{})
	}
	response.ExecutionMetadata["files_provided"] = len(files)
	response.ExecutionMetadata["files_processed"] = len(response.FilesProcessed)
	response.ExecutionMetadata["file_handling_mode"] = config.FileHandling

	return response, nil
}

// processFilesForContext processes files for inclusion in LLM context
func (a *BaseUnifiedAdapter) processFilesForContext(files []f2.File, config UnifiedConfig) ([]f2.File, error) {
	processed := []f2.File{}
	capabilities := a.fileAdapter.GetFileCapabilities()

	totalSize := int64(0)

	for _, file := range files {
		// Validate file against adapter capabilities
		if err := a.fileAdapter.ValidateFile(file); err != nil {
			// Skip invalid files based on file handling mode
			if config.FileHandling == FileHandlingNative {
				return nil, fmt.Errorf("file validation failed for %s: %w", file.Path, err)
			}
			// In hybrid or fallback mode, try to convert
			file = a.convertFileToText(file)
		}

		// Filter secrets from file content
		if a.secretFilter != nil {
			file = *a.secretFilter.FilterFile(&file)
		}

		// Check size limits
		totalSize += int64(len(file.Content))
		if totalSize > capabilities.TotalSizeLimit {
			return nil, fmt.Errorf("total file size exceeds limit of %d bytes", capabilities.TotalSizeLimit)
		}

		// Add file type metadata if not set
		if file.Type == "" || file.Type == f2.FileTypeUnknown {
			detector := NewFileTypeDetector()
			file.Type = detector.DetectType(file.Content, file.Path)
			file.MimeType = detector.DetectMimeType(file.Content, file.Path)
		}

		processed = append(processed, file)

		// Check file count limit
		if len(processed) >= capabilities.MaxFileCount {
			break
		}
	}

	return processed, nil
}

// buildPromptWithFileContext builds an enhanced prompt with file context
func (a *BaseUnifiedAdapter) buildPromptWithFileContext(prompt string, files []f2.File) string {
	if len(files) == 0 {
		return prompt
	}

	enhanced := prompt + "\n\n### File Context ###\n"

	for _, file := range files {
		enhanced += fmt.Sprintf("\n**File: %s** (Type: %s, Size: %d bytes)\n",
			file.Path, file.Type, len(file.Content))

		// Add file metadata if relevant
		if file.Metadata != nil {
			if lang, ok := file.Metadata["language"].(string); ok {
				enhanced += fmt.Sprintf("Language: %s\n", lang)
			}
		}

		// For text-based files, include a preview
		if file.Type == f2.FileTypeText || file.Type == f2.FileTypeCode ||
			file.Type == f2.FileTypeConfig || file.Type == f2.FileTypeMarkdown {
			preview := string(file.Content)
			if len(preview) > 500 {
				preview = preview[:500] + "...\n[Content truncated for context]"
			}
			enhanced += fmt.Sprintf("```\n%s\n```\n", preview)
		} else {
			enhanced += fmt.Sprintf("[%s file - content available for processing]\n", file.Type)
		}
	}

	enhanced += "\n### End File Context ###\n"

	return enhanced
}

// addFileOperationTools adds file-specific tools when files are provided
func (a *BaseUnifiedAdapter) addFileOperationTools(tools []Tool, config UnifiedConfig) []Tool {
	// Check if file tools are already present
	hasFileTools := false
	for _, tool := range tools {
		if tool.Name == "write_file" || tool.Name == "read_file" {
			hasFileTools = true
			break
		}
	}

	if !hasFileTools {
		// Add basic file operation tools
		fileTools := []Tool{
			{
				Name:        "analyze_file",
				Description: "Analyze a file from the provided context",
				Parameters: jsonRawMessage(`{
					"type": "object",
					"properties": {
						"path": {"type": "string", "description": "Path of the file to analyze"},
						"analysis_type": {"type": "string", "enum": ["summary", "issues", "suggestions"]}
					},
					"required": ["path", "analysis_type"]
				}`),
			},
			{
				Name:        "modify_file",
				Description: "Suggest modifications to a file from the context",
				Parameters: jsonRawMessage(`{
					"type": "object",
					"properties": {
						"path": {"type": "string", "description": "Path of the file to modify"},
						"modifications": {"type": "array", "items": {
							"type": "object",
							"properties": {
								"line": {"type": "number"},
								"change": {"type": "string"}
							}
						}}
					},
					"required": ["path", "modifications"]
				}`),
			},
		}

		tools = append(tools, fileTools...)
	}

	return tools
}

// trackFileOperations tracks file operations from tool execution results
func (a *BaseUnifiedAdapter) trackFileOperations(response *UnifiedResponse, results []ToolResult) {
	for _, result := range results {
		if !result.Success {
			continue
		}

		switch result.ToolName {
		case "write_file", "create_file":
			if path := a.extractPathFromResult(result); path != "" {
				// Check if file existed before
				if a.fileExisted(path, response.FilesProcessed) {
					response.FilesModified = append(response.FilesModified, path)
				} else {
					response.FilesCreated = append(response.FilesCreated, path)
				}
			}

		case "delete_file", "remove_file":
			if path := a.extractPathFromResult(result); path != "" {
				response.FilesDeleted = append(response.FilesDeleted, path)
			}

		case "modify_file", "update_file":
			if path := a.extractPathFromResult(result); path != "" {
				response.FilesModified = append(response.FilesModified, path)
			}
		}
	}
}

// convertFileToText converts non-text files to text representation
func (a *BaseUnifiedAdapter) convertFileToText(file f2.File) f2.File {
	// Create a text representation based on file type
	switch file.Type {
	case f2.FileTypeImage:
		file.Content = []byte(fmt.Sprintf("[Image file: %s, Size: %d bytes, Type: %s]",
			file.Path, len(file.Content), file.MimeType))
		file.Type = f2.FileTypeText

	case f2.FileTypePDF:
		// In a real implementation, we would extract text from PDF
		file.Content = []byte(fmt.Sprintf("[PDF file: %s, Size: %d bytes]",
			file.Path, len(file.Content)))
		file.Type = f2.FileTypeText

	case f2.FileTypeBinary:
		file.Content = []byte(fmt.Sprintf("[Binary file: %s, Size: %d bytes, Type: %s]",
			file.Path, len(file.Content), file.MimeType))
		file.Type = f2.FileTypeText

	case f2.FileTypeAudio, f2.FileTypeVideo:
		file.Content = []byte(fmt.Sprintf("[Media file: %s, Size: %d bytes, Type: %s]",
			file.Path, len(file.Content), file.MimeType))
		file.Type = f2.FileTypeText
	}

	return file
}

// extractPathFromResult extracts file path from tool result
func (a *BaseUnifiedAdapter) extractPathFromResult(result ToolResult) string {
	if result.Result == nil {
		return ""
	}

	var resultData map[string]interface{}
	if err := json.Unmarshal(result.Result, &resultData); err == nil {
		if path, ok := resultData["path"].(string); ok {
			return path
		}
	}

	return ""
}

// fileExisted checks if a file was in the original processed files
func (a *BaseUnifiedAdapter) fileExisted(path string, processedFiles []string) bool {
	for _, processed := range processedFiles {
		if processed == path {
			return true
		}
	}
	return false
}

// jsonRawMessage is a helper to create json.RawMessage
func jsonRawMessage(s string) json.RawMessage {
	return json.RawMessage(s)
}
