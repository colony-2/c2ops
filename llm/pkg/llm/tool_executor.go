package llm

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"
)

// ToolExecutionRequest describes a tool execution request
type ToolExecutionRequest struct {
	Name       string                 `json:"name"`
	Arguments  map[string]interface{} `json:"arguments"`
	WorkingDir string                 `json:"working_dir"`
	Timeout    time.Duration          `json:"timeout"`
}

// FileToolExecutor handles file operation tools
type FileToolExecutor struct {
	workingDir string
	sandbox    *SecuritySandbox
}

// NewFileToolExecutor creates a new file tool executor
func NewFileToolExecutor(workingDir string, sandbox *SecuritySandbox) *FileToolExecutor {
	if workingDir == "" {
		workingDir = "."
	}
	
	return &FileToolExecutor{
		workingDir: workingDir,
		sandbox:    sandbox,
	}
}

// Execute executes a tool based on its name
func (e *FileToolExecutor) Execute(
	ctx context.Context,
	request ToolExecutionRequest,
) (interface{}, error) {
	// Override working directory if specified in request
	workingDir := e.workingDir
	if request.WorkingDir != "" {
		workingDir = request.WorkingDir
	}
	
	// Apply timeout if specified
	if request.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, request.Timeout)
		defer cancel()
	}
	
	switch request.Name {
	case "write_file":
		return e.executeWriteFile(ctx, request.Arguments, workingDir)
	case "read_file":
		return e.executeReadFile(ctx, request.Arguments, workingDir)
	case "delete_file":
		return e.executeDeleteFile(ctx, request.Arguments, workingDir)
	case "list_files":
		return e.executeListFiles(ctx, request.Arguments, workingDir)
	case "create_directory":
		return e.executeCreateDirectory(ctx, request.Arguments, workingDir)
	default:
		return nil, fmt.Errorf("unknown tool: %s", request.Name)
	}
}

// executeWriteFile writes or updates a file
func (e *FileToolExecutor) executeWriteFile(
	ctx context.Context,
	args map[string]interface{},
	workingDir string,
) (interface{}, error) {
	// Extract arguments
	path, ok := args["path"].(string)
	if !ok {
		return nil, fmt.Errorf("path is required")
	}
	
	content, ok := args["content"].(string)
	if !ok {
		return nil, fmt.Errorf("content is required")
	}
	
	createDirs := true // Default to true
	if val, ok := args["create_dirs"].(bool); ok {
		createDirs = val
	}
	
	// Build full path
	fullPath := filepath.Join(workingDir, path)
	
	// Validate path in sandbox
	if e.sandbox != nil {
		if err := e.sandbox.ValidatePath(fullPath); err != nil {
			return nil, fmt.Errorf("path validation failed: %w", err)
		}
	}
	
	// Create directories if needed
	if createDirs {
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directories: %w", err)
		}
	}
	
	// Write file
	if err := ioutil.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}
	
	return map[string]interface{}{
		"path":  path,
		"bytes": len(content),
		"success": true,
	}, nil
}

// executeReadFile reads a file's content
func (e *FileToolExecutor) executeReadFile(
	ctx context.Context,
	args map[string]interface{},
	workingDir string,
) (interface{}, error) {
	// Extract arguments
	path, ok := args["path"].(string)
	if !ok {
		return nil, fmt.Errorf("path is required")
	}
	
	// Build full path
	fullPath := filepath.Join(workingDir, path)
	
	// Validate path in sandbox
	if e.sandbox != nil {
		if err := e.sandbox.ValidatePath(fullPath); err != nil {
			return nil, fmt.Errorf("path validation failed: %w", err)
		}
	}
	
	// Check if file exists
	if _, err := os.Stat(fullPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", path)
		}
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	
	// Read file
	content, err := ioutil.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	
	return map[string]interface{}{
		"path":    path,
		"content": string(content),
		"size":    len(content),
		"success": true,
	}, nil
}

// executeDeleteFile deletes a file
func (e *FileToolExecutor) executeDeleteFile(
	ctx context.Context,
	args map[string]interface{},
	workingDir string,
) (interface{}, error) {
	// Extract arguments
	path, ok := args["path"].(string)
	if !ok {
		return nil, fmt.Errorf("path is required")
	}
	
	// Build full path
	fullPath := filepath.Join(workingDir, path)
	
	// Validate path in sandbox
	if e.sandbox != nil {
		if err := e.sandbox.ValidatePath(fullPath); err != nil {
			return nil, fmt.Errorf("path validation failed: %w", err)
		}
	}
	
	// Check if file exists
	if _, err := os.Stat(fullPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", path)
		}
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	
	// Delete file
	if err := os.Remove(fullPath); err != nil {
		return nil, fmt.Errorf("failed to delete file: %w", err)
	}
	
	return map[string]interface{}{
		"path":    path,
		"deleted": true,
		"success": true,
	}, nil
}

// executeListFiles lists files in a directory
func (e *FileToolExecutor) executeListFiles(
	ctx context.Context,
	args map[string]interface{},
	workingDir string,
) (interface{}, error) {
	// Extract arguments
	path := "."
	if p, ok := args["path"].(string); ok {
		path = p
	}
	
	pattern := ""
	if p, ok := args["pattern"].(string); ok {
		pattern = p
	}
	
	recursive := false
	if r, ok := args["recursive"].(bool); ok {
		recursive = r
	}
	
	// Build full path
	fullPath := filepath.Join(workingDir, path)
	
	// Validate path in sandbox
	if e.sandbox != nil {
		if err := e.sandbox.ValidatePath(fullPath); err != nil {
			return nil, fmt.Errorf("path validation failed: %w", err)
		}
	}
	
	// Check if directory exists
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("directory not found: %s", path)
		}
		return nil, fmt.Errorf("failed to stat directory: %w", err)
	}
	
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", path)
	}
	
	var files []map[string]interface{}
	
	if recursive {
		// Walk directory recursively
		err = filepath.Walk(fullPath, func(walkPath string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip files with errors
			}
			
			// Get relative path
			relPath, err := filepath.Rel(fullPath, walkPath)
			if err != nil {
				return nil
			}
			
			// Skip the root directory itself
			if relPath == "." {
				return nil
			}
			
			// Apply pattern filter if specified
			if pattern != "" {
				matched, err := filepath.Match(pattern, filepath.Base(walkPath))
				if err != nil || !matched {
					return nil
				}
			}
			
			files = append(files, map[string]interface{}{
				"name":     filepath.Base(walkPath),
				"path":     relPath,
				"size":     info.Size(),
				"is_dir":   info.IsDir(),
				"modified": info.ModTime().Unix(),
			})
			
			return nil
		})
		
		if err != nil {
			return nil, fmt.Errorf("failed to walk directory: %w", err)
		}
	} else {
		// List immediate directory contents
		entries, err := ioutil.ReadDir(fullPath)
		if err != nil {
			return nil, fmt.Errorf("failed to list directory: %w", err)
		}
		
		for _, entry := range entries {
			// Apply pattern filter if specified
			if pattern != "" {
				matched, err := filepath.Match(pattern, entry.Name())
				if err != nil || !matched {
					continue
				}
			}
			
			files = append(files, map[string]interface{}{
				"name":     entry.Name(),
				"path":     entry.Name(),
				"size":     entry.Size(),
				"is_dir":   entry.IsDir(),
				"modified": entry.ModTime().Unix(),
			})
		}
	}
	
	return map[string]interface{}{
		"path":    path,
		"files":   files,
		"count":   len(files),
		"success": true,
	}, nil
}

// executeCreateDirectory creates a directory
func (e *FileToolExecutor) executeCreateDirectory(
	ctx context.Context,
	args map[string]interface{},
	workingDir string,
) (interface{}, error) {
	// Extract arguments
	path, ok := args["path"].(string)
	if !ok {
		return nil, fmt.Errorf("path is required")
	}
	
	// Build full path
	fullPath := filepath.Join(workingDir, path)
	
	// Validate path in sandbox
	if e.sandbox != nil {
		if err := e.sandbox.ValidatePath(fullPath); err != nil {
			return nil, fmt.Errorf("path validation failed: %w", err)
		}
	}
	
	// Create directory (including parents)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}
	
	return map[string]interface{}{
		"path":    path,
		"created": true,
		"success": true,
	}, nil
}