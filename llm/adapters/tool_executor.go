package llmadapters

import (
	"context"
	"encoding/json"
	"time"
)

// ToolExecutor handles the execution of tool calls
type ToolExecutor interface {
	// ExecuteTool executes a single tool call
	ExecuteTool(ctx context.Context, call ToolCall, config ToolExecutionConfig) (ToolResult, error)
	
	// ValidateTool validates a tool call against its schema
	ValidateTool(call ToolCall, tool Tool) error
	
	// GetAvailableTools returns the list of executable tools
	GetAvailableTools() []Tool
}

// ToolResult represents the result of a tool execution
type ToolResult struct {
	ToolCallID string                 `json:"tool_call_id"`
	ToolName   string                 `json:"tool_name"`
	Result     json.RawMessage        `json:"result,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Success    bool                   `json:"success"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// ToolExecutionConfig configures individual tool execution
type ToolExecutionConfig struct {
	WorkingDirectory string            `json:"working_directory"`
	Timeout          time.Duration     `json:"timeout"`
	Environment      map[string]string `json:"environment,omitempty"`
	Sandbox          bool              `json:"sandbox,omitempty"`
}

// ToolExecutionError provides detailed error information
type ToolExecutionError struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Error      string `json:"error"`
	Timestamp  int64  `json:"timestamp"`
}

// CompositeToolExecutor combines multiple tool executors
type CompositeToolExecutor struct {
	executors map[string]ToolExecutor
}

// NewCompositeToolExecutor creates a new composite tool executor
func NewCompositeToolExecutor() *CompositeToolExecutor {
	return &CompositeToolExecutor{
		executors: make(map[string]ToolExecutor),
	}
}

// RegisterExecutor registers a tool executor for a specific tool pattern
func (c *CompositeToolExecutor) RegisterExecutor(pattern string, executor ToolExecutor) {
	c.executors[pattern] = executor
}

// ExecuteTool executes a tool by delegating to the appropriate executor
func (c *CompositeToolExecutor) ExecuteTool(ctx context.Context, call ToolCall, config ToolExecutionConfig) (ToolResult, error) {
	// Find the appropriate executor based on tool name
	for pattern, executor := range c.executors {
		if matchesPattern(call.Name, pattern) {
			return executor.ExecuteTool(ctx, call, config)
		}
	}
	
	return ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Success:    false,
		Error:      "no executor found for tool: " + call.Name,
	}, nil
}

// ValidateTool validates a tool call
func (c *CompositeToolExecutor) ValidateTool(call ToolCall, tool Tool) error {
	for pattern, executor := range c.executors {
		if matchesPattern(call.Name, pattern) {
			return executor.ValidateTool(call, tool)
		}
	}
	return nil
}

// GetAvailableTools returns all available tools from all executors
func (c *CompositeToolExecutor) GetAvailableTools() []Tool {
	tools := []Tool{}
	seen := make(map[string]bool)
	
	for _, executor := range c.executors {
		for _, tool := range executor.GetAvailableTools() {
			if !seen[tool.Name] {
				tools = append(tools, tool)
				seen[tool.Name] = true
			}
		}
	}
	
	return tools
}

// matchesPattern checks if a tool name matches a pattern
func matchesPattern(name, pattern string) bool {
	// Simple pattern matching - can be enhanced with wildcards
	if pattern == "*" {
		return true
	}
	if pattern == name {
		return true
	}
	// Check for prefix match with wildcard
	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(name) >= len(prefix) && name[:len(prefix)] == prefix
	}
	return false
}