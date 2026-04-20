package llmadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ToolSandbox provides execution isolation
type ToolSandbox interface {
	// Execute runs a tool in a sandboxed environment
	Execute(ctx context.Context, call ToolCall, config SandboxConfig) (ToolResult, error)
	
	// Validate checks if an operation is allowed
	Validate(call ToolCall) error
}

// SandboxConfig configures the sandbox
type SandboxConfig struct {
	AllowedPaths     []string          `json:"allowed_paths"`
	BlockedPaths     []string          `json:"blocked_paths"`
	MaxFileSize      int64             `json:"max_file_size"`
	MaxExecutionTime time.Duration     `json:"max_execution_time"`
	MaxMemory        int64             `json:"max_memory"`
	Environment      map[string]string `json:"environment"`
	NetworkAccess    bool              `json:"network_access"`
}

// DefaultToolSandbox implements ToolSandbox
type DefaultToolSandbox struct {
	config        SandboxConfig
	toolExecutor  ToolExecutor
	pathValidator PathValidator
}

// NewToolSandbox creates a new tool sandbox
func NewToolSandbox(config SandboxConfig, executor ToolExecutor) ToolSandbox {
	return &DefaultToolSandbox{
		config:        config,
		toolExecutor:  executor,
		pathValidator: NewPathValidator(config.AllowedPaths, config.BlockedPaths),
	}
}

// Execute runs a tool in a sandboxed environment
func (s *DefaultToolSandbox) Execute(ctx context.Context, call ToolCall, config SandboxConfig) (ToolResult, error) {
	// Validate the tool call first
	if err := s.Validate(call); err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Success:    false,
			Error:      fmt.Sprintf("validation failed: %v", err),
		}, nil
	}
	
	// Create execution context with timeout
	execCtx := ctx
	if config.MaxExecutionTime > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, config.MaxExecutionTime)
		defer cancel()
	}
	
	// Create sandboxed execution config
	execConfig := ToolExecutionConfig{
		Timeout:     config.MaxExecutionTime,
		Environment: s.filterEnvironment(config.Environment),
		Sandbox:     true,
	}
	
	// Execute with monitoring
	resultChan := make(chan ToolResult, 1)
	errorChan := make(chan error, 1)
	
	go func() {
		result, err := s.toolExecutor.ExecuteTool(execCtx, call, execConfig)
		if err != nil {
			errorChan <- err
		} else {
			resultChan <- result
		}
	}()
	
	// Wait for result or timeout
	select {
	case result := <-resultChan:
		// Add sandbox metadata
		if result.Metadata == nil {
			result.Metadata = make(map[string]interface{})
		}
		result.Metadata["sandboxed"] = true
		result.Metadata["execution_time"] = time.Now().Unix()
		return result, nil
		
	case err := <-errorChan:
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Success:    false,
			Error:      fmt.Sprintf("execution error: %v", err),
		}, err
		
	case <-execCtx.Done():
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Success:    false,
			Error:      "execution timeout exceeded",
		}, fmt.Errorf("execution timeout")
	}
}

// Validate checks if a tool call is allowed
func (s *DefaultToolSandbox) Validate(call ToolCall) error {
	// Parse arguments
	var args map[string]interface{}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	
	// Validate based on tool type
	switch call.Name {
	case "write_file", "read_file", "delete_file", "create_directory":
		// Validate file path
		if pathArg, ok := args["path"].(string); ok {
			if err := s.pathValidator.ValidatePath(pathArg); err != nil {
				return fmt.Errorf("path validation failed: %w", err)
			}
		}
		
		// Check file size for write operations
		if call.Name == "write_file" {
			if content, ok := args["content"].(string); ok {
				if int64(len(content)) > s.config.MaxFileSize {
					return fmt.Errorf("content exceeds maximum file size of %d bytes", s.config.MaxFileSize)
				}
			}
		}
		
	case "execute_code":
		// Validate code execution is allowed
		if !s.config.NetworkAccess && s.containsNetworkCode(args) {
			return fmt.Errorf("network access is not allowed in sandbox")
		}
		
	default:
		// Check if tool is in allowed list
		if !s.isToolAllowed(call.Name) {
			return fmt.Errorf("tool '%s' is not allowed in sandbox", call.Name)
		}
	}
	
	return nil
}

// filterEnvironment filters environment variables for sandbox
func (s *DefaultToolSandbox) filterEnvironment(env map[string]string) map[string]string {
	filtered := make(map[string]string)
	
	// Blocklist of sensitive environment variables
	blocklist := []string{
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"AZURE_CLIENT_SECRET",
		"DATABASE_URL",
		"API_KEY",
		"SECRET",
		"PASSWORD",
		"TOKEN",
		"PRIVATE_KEY",
	}
	
	for key, value := range env {
		blocked := false
		upperKey := strings.ToUpper(key)
		
		for _, blockPattern := range blocklist {
			if strings.Contains(upperKey, blockPattern) {
				blocked = true
				break
			}
		}
		
		if !blocked {
			filtered[key] = value
		}
	}
	
	// Add safe defaults
	filtered["SANDBOX"] = "true"
	filtered["EXECUTION_MODE"] = "restricted"
	
	return filtered
}

// containsNetworkCode checks if code contains network operations
func (s *DefaultToolSandbox) containsNetworkCode(args map[string]interface{}) bool {
	code, ok := args["code"].(string)
	if !ok {
		return false
	}
	
	// Check for common network-related patterns
	networkPatterns := []string{
		"http://", "https://",
		"urllib", "requests", "httpx",
		"socket", "connect",
		"fetch", "XMLHttpRequest",
		"net.", "http.",
	}
	
	lowerCode := strings.ToLower(code)
	for _, pattern := range networkPatterns {
		if strings.Contains(lowerCode, pattern) {
			return true
		}
	}
	
	return false
}

// isToolAllowed checks if a tool is in the allowed list
func (s *DefaultToolSandbox) isToolAllowed(toolName string) bool {
	// Default allowed tools
	allowedTools := []string{
		"write_file", "read_file", "list_files", "create_directory",
		"analyze_file", "modify_file",
	}
	
	for _, allowed := range allowedTools {
		if toolName == allowed {
			return true
		}
	}
	
	return false
}

// PathValidator validates file paths against allowed/blocked lists
type PathValidator interface {
	ValidatePath(path string) error
}

// DefaultPathValidator implements PathValidator
type DefaultPathValidator struct {
	allowedPaths []string
	blockedPaths []string
}

// NewPathValidator creates a new path validator
func NewPathValidator(allowed, blocked []string) PathValidator {
	return &DefaultPathValidator{
		allowedPaths: allowed,
		blockedPaths: blocked,
	}
}

// ValidatePath validates a path against rules
func (v *DefaultPathValidator) ValidatePath(path string) error {
	// Clean and resolve the path
	cleanPath := filepath.Clean(path)
	absPath, _ := filepath.Abs(cleanPath)
	
	// Check blocked paths first
	for _, blocked := range v.blockedPaths {
		blockedAbs, _ := filepath.Abs(blocked)
		if strings.HasPrefix(absPath, blockedAbs) {
			return fmt.Errorf("path '%s' is blocked", path)
		}
		
		// Check pattern matching
		if matched, _ := filepath.Match(blocked, cleanPath); matched {
			return fmt.Errorf("path '%s' matches blocked pattern '%s'", path, blocked)
		}
	}
	
	// If allowed paths are specified, path must be within one of them
	if len(v.allowedPaths) > 0 {
		allowed := false
		for _, allowedPath := range v.allowedPaths {
			allowedAbs, _ := filepath.Abs(allowedPath)
			if strings.HasPrefix(absPath, allowedAbs) {
				allowed = true
				break
			}
			
			// Check pattern matching
			if matched, _ := filepath.Match(allowedPath, cleanPath); matched {
				allowed = true
				break
			}
		}
		
		if !allowed {
			return fmt.Errorf("path '%s' is not in allowed paths", path)
		}
	}
	
	// Check for dangerous patterns
	dangerousPatterns := []string{
		"..",           // Parent directory traversal
		"~",            // Home directory expansion
		"/etc/passwd",  // System files
		"/etc/shadow",
		"/proc",        // Process information
		"/sys",         // System information
		"/dev",         // Device files
		".ssh",         // SSH keys
		".aws",         // AWS credentials
		".kube",        // Kubernetes configs
	}
	
	for _, pattern := range dangerousPatterns {
		if strings.Contains(cleanPath, pattern) {
			return fmt.Errorf("path '%s' contains dangerous pattern '%s'", path, pattern)
		}
	}
	
	return nil
}