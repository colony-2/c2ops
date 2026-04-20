package llmadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

// FileToolExecutor handles file operation tools
type FileToolExecutor struct {
	baseDir     string
	maxFileSize int64
	allowDelete bool
	allowWrite  bool
	allowRead   bool
}

// FileToolOption configures FileToolExecutor
type FileToolOption func(*FileToolExecutor)

// WithMaxFileSize sets the maximum file size limit
func WithMaxFileSize(size int64) FileToolOption {
	return func(e *FileToolExecutor) {
		e.maxFileSize = size
	}
}

// WithAllowDelete enables file deletion
func WithAllowDelete(allow bool) FileToolOption {
	return func(e *FileToolExecutor) {
		e.allowDelete = allow
	}
}

// WithAllowWrite enables file writing
func WithAllowWrite(allow bool) FileToolOption {
	return func(e *FileToolExecutor) {
		e.allowWrite = allow
	}
}

// WithAllowRead enables file reading
func WithAllowRead(allow bool) FileToolOption {
	return func(e *FileToolExecutor) {
		e.allowRead = allow
	}
}

// NewFileToolExecutor creates a new file tool executor
func NewFileToolExecutor(baseDir string, opts ...FileToolOption) *FileToolExecutor {
	executor := &FileToolExecutor{
		baseDir:     baseDir,
		maxFileSize: 10 * 1024 * 1024, // 10MB default
		allowDelete: false,
		allowWrite:  true,
		allowRead:   true,
	}
	
	for _, opt := range opts {
		opt(executor)
	}
	
	return executor
}

// ExecuteTool executes a file operation tool
func (e *FileToolExecutor) ExecuteTool(ctx context.Context, call ToolCall, config ToolExecutionConfig) (ToolResult, error) {
	result := ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Metadata:   make(map[string]interface{}),
	}
	
	// Parse arguments
	var args map[string]interface{}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		result.Error = fmt.Sprintf("failed to parse arguments: %v", err)
		return result, nil
	}
	
	// Execute based on tool name
	switch call.Name {
	case "write_file":
		return e.executeWriteFile(ctx, call, args, config)
	case "read_file":
		return e.executeReadFile(ctx, call, args, config)
	case "delete_file":
		return e.executeDeleteFile(ctx, call, args, config)
	case "list_files":
		return e.executeListFiles(ctx, call, args, config)
	case "create_directory":
		return e.executeCreateDirectory(ctx, call, args, config)
	default:
		result.Error = fmt.Sprintf("unknown tool: %s", call.Name)
		return result, nil
	}
}

// executeWriteFile handles file writing
func (e *FileToolExecutor) executeWriteFile(ctx context.Context, call ToolCall, args map[string]interface{}, config ToolExecutionConfig) (ToolResult, error) {
	result := ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Metadata:   make(map[string]interface{}),
	}
	
	if !e.allowWrite {
		result.Error = "file writing is not allowed"
		return result, nil
	}
	
	path, ok := args["path"].(string)
	if !ok {
		result.Error = "path parameter is required"
		return result, nil
	}
	
	content, ok := args["content"].(string)
	if !ok {
		result.Error = "content parameter is required"
		return result, nil
	}
	
	// Resolve and validate path
	fullPath, err := e.resolvePath(path)
	if err != nil {
		result.Error = fmt.Sprintf("invalid path: %v", err)
		return result, nil
	}
	
	// Check file size
	if int64(len(content)) > e.maxFileSize {
		result.Error = fmt.Sprintf("content exceeds maximum file size of %d bytes", e.maxFileSize)
		return result, nil
	}
	
	// Create directory if it doesn't exist
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		result.Error = fmt.Sprintf("failed to create directory: %v", err)
		return result, nil
	}
	
	// Write file
	if err := ioutil.WriteFile(fullPath, []byte(content), 0644); err != nil {
		result.Error = fmt.Sprintf("failed to write file: %v", err)
		return result, nil
	}
	
	result.Success = true
	result.Result = json.RawMessage(fmt.Sprintf(`{"path": "%s", "size": %d}`, path, len(content)))
	result.Metadata["bytes_written"] = len(content)
	
	return result, nil
}

// executeReadFile handles file reading
func (e *FileToolExecutor) executeReadFile(ctx context.Context, call ToolCall, args map[string]interface{}, config ToolExecutionConfig) (ToolResult, error) {
	result := ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Metadata:   make(map[string]interface{}),
	}
	
	if !e.allowRead {
		result.Error = "file reading is not allowed"
		return result, nil
	}
	
	path, ok := args["path"].(string)
	if !ok {
		result.Error = "path parameter is required"
		return result, nil
	}
	
	// Resolve and validate path
	fullPath, err := e.resolvePath(path)
	if err != nil {
		result.Error = fmt.Sprintf("invalid path: %v", err)
		return result, nil
	}
	
	// Check file exists
	info, err := os.Stat(fullPath)
	if err != nil {
		result.Error = fmt.Sprintf("file not found: %v", err)
		return result, nil
	}
	
	// Check file size
	if info.Size() > e.maxFileSize {
		result.Error = fmt.Sprintf("file exceeds maximum size of %d bytes", e.maxFileSize)
		return result, nil
	}
	
	// Read file
	content, err := ioutil.ReadFile(fullPath)
	if err != nil {
		result.Error = fmt.Sprintf("failed to read file: %v", err)
		return result, nil
	}
	
	result.Success = true
	resultData := map[string]interface{}{
		"path":    path,
		"content": string(content),
		"size":    info.Size(),
	}
	
	resultJSON, _ := json.Marshal(resultData)
	result.Result = json.RawMessage(resultJSON)
	result.Metadata["bytes_read"] = info.Size()
	
	return result, nil
}

// executeDeleteFile handles file deletion
func (e *FileToolExecutor) executeDeleteFile(ctx context.Context, call ToolCall, args map[string]interface{}, config ToolExecutionConfig) (ToolResult, error) {
	result := ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Metadata:   make(map[string]interface{}),
	}
	
	if !e.allowDelete {
		result.Error = "file deletion is not allowed"
		return result, nil
	}
	
	path, ok := args["path"].(string)
	if !ok {
		result.Error = "path parameter is required"
		return result, nil
	}
	
	// Resolve and validate path
	fullPath, err := e.resolvePath(path)
	if err != nil {
		result.Error = fmt.Sprintf("invalid path: %v", err)
		return result, nil
	}
	
	// Delete file
	if err := os.Remove(fullPath); err != nil {
		result.Error = fmt.Sprintf("failed to delete file: %v", err)
		return result, nil
	}
	
	result.Success = true
	result.Result = json.RawMessage(fmt.Sprintf(`{"path": "%s", "deleted": true}`, path))
	
	return result, nil
}

// executeListFiles handles directory listing
func (e *FileToolExecutor) executeListFiles(ctx context.Context, call ToolCall, args map[string]interface{}, config ToolExecutionConfig) (ToolResult, error) {
	result := ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Metadata:   make(map[string]interface{}),
	}
	
	if !e.allowRead {
		result.Error = "file listing is not allowed"
		return result, nil
	}
	
	path := ""
	if p, ok := args["path"].(string); ok {
		path = p
	}
	
	// Resolve and validate path
	fullPath, err := e.resolvePath(path)
	if err != nil {
		result.Error = fmt.Sprintf("invalid path: %v", err)
		return result, nil
	}
	
	// List files
	entries, err := ioutil.ReadDir(fullPath)
	if err != nil {
		result.Error = fmt.Sprintf("failed to list files: %v", err)
		return result, nil
	}
	
	files := []map[string]interface{}{}
	for _, entry := range entries {
		files = append(files, map[string]interface{}{
			"name":    entry.Name(),
			"size":    entry.Size(),
			"is_dir":  entry.IsDir(),
			"mode":    entry.Mode().String(),
			"modified": entry.ModTime().Unix(),
		})
	}
	
	result.Success = true
	resultJSON, _ := json.Marshal(map[string]interface{}{
		"path":  path,
		"files": files,
		"count": len(files),
	})
	result.Result = json.RawMessage(resultJSON)
	result.Metadata["file_count"] = len(files)
	
	return result, nil
}

// executeCreateDirectory handles directory creation
func (e *FileToolExecutor) executeCreateDirectory(ctx context.Context, call ToolCall, args map[string]interface{}, config ToolExecutionConfig) (ToolResult, error) {
	result := ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Metadata:   make(map[string]interface{}),
	}
	
	if !e.allowWrite {
		result.Error = "directory creation is not allowed"
		return result, nil
	}
	
	path, ok := args["path"].(string)
	if !ok {
		result.Error = "path parameter is required"
		return result, nil
	}
	
	// Resolve and validate path
	fullPath, err := e.resolvePath(path)
	if err != nil {
		result.Error = fmt.Sprintf("invalid path: %v", err)
		return result, nil
	}
	
	// Create directory
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		result.Error = fmt.Sprintf("failed to create directory: %v", err)
		return result, nil
	}
	
	result.Success = true
	result.Result = json.RawMessage(fmt.Sprintf(`{"path": "%s", "created": true}`, path))
	
	return result, nil
}

// ValidateTool validates a tool call against its schema
func (e *FileToolExecutor) ValidateTool(call ToolCall, tool Tool) error {
	// Basic validation - can be enhanced with JSON schema validation
	var args map[string]interface{}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	
	switch call.Name {
	case "write_file", "read_file", "delete_file", "create_directory":
		if _, ok := args["path"]; !ok {
			return fmt.Errorf("path parameter is required")
		}
		if call.Name == "write_file" {
			if _, ok := args["content"]; !ok {
				return fmt.Errorf("content parameter is required")
			}
		}
	case "list_files":
		// path is optional for list_files
	default:
		return fmt.Errorf("unknown tool: %s", call.Name)
	}
	
	return nil
}

// GetAvailableTools returns the list of file operation tools
func (e *FileToolExecutor) GetAvailableTools() []Tool {
	tools := []Tool{}
	
	if e.allowWrite {
		tools = append(tools, Tool{
			Name:        "write_file",
			Description: "Write or update a file with the specified content",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "The file path relative to the base directory"},
					"content": {"type": "string", "description": "The content to write to the file"}
				},
				"required": ["path", "content"]
			}`),
		})
		
		tools = append(tools, Tool{
			Name:        "create_directory",
			Description: "Create a directory at the specified path",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "The directory path relative to the base directory"}
				},
				"required": ["path"]
			}`),
		})
	}
	
	if e.allowRead {
		tools = append(tools, Tool{
			Name:        "read_file",
			Description: "Read the content of a file",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "The file path relative to the base directory"}
				},
				"required": ["path"]
			}`),
		})
		
		tools = append(tools, Tool{
			Name:        "list_files",
			Description: "List files and directories in a given path",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "The directory path relative to the base directory (optional, defaults to base)"}
				}
			}`),
		})
	}
	
	if e.allowDelete {
		tools = append(tools, Tool{
			Name:        "delete_file",
			Description: "Delete a file at the specified path",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "The file path relative to the base directory"}
				},
				"required": ["path"]
			}`),
		})
	}
	
	return tools
}

// resolvePath resolves and validates a path within the base directory
func (e *FileToolExecutor) resolvePath(path string) (string, error) {
	// Clean the path
	cleaned := filepath.Clean(path)
	
	// Ensure it doesn't escape the base directory
	if strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("path escapes base directory")
	}
	
	// Join with base directory
	fullPath := filepath.Join(e.baseDir, cleaned)
	
	// Verify it's still within base directory (double check)
	absBase, err := filepath.Abs(e.baseDir)
	if err != nil {
		return "", err
	}
	
	absFull, err := filepath.Abs(fullPath)
	if err != nil {
		return "", err
	}
	
	if !strings.HasPrefix(absFull, absBase) {
		return "", fmt.Errorf("path escapes base directory")
	}
	
	return absFull, nil
}