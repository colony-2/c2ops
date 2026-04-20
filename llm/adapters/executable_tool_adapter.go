package llmadapters

import (
	"context"
	"encoding/json"
	"time"
)

// ExecutableToolAdapter extends the base Adapter with tool execution capabilities
type ExecutableToolAdapter interface {
	Adapter
	
	// GenerateAndExecuteTools generates a response and automatically executes any tool calls
	GenerateAndExecuteTools(
		ctx context.Context,
		prompt string,
		tools []Tool,
		config ExecutableToolConfig,
	) (ExecutableToolResponse, error)
	
	// SetToolExecutor sets the executor for handling tool calls
	SetToolExecutor(executor ToolExecutor)
}

// ExecutableToolConfig extends Config with execution settings
type ExecutableToolConfig struct {
	Config
	
	// Whether to automatically execute tool calls
	AutoExecute bool `json:"auto_execute"`
	
	// Maximum number of tool execution rounds
	MaxToolRounds int `json:"max_tool_rounds,omitempty"`
	
	// Working directory for tool execution
	WorkingDirectory string `json:"working_directory,omitempty"`
	
	// Timeout for individual tool executions
	ToolTimeout Duration `json:"tool_timeout,omitempty"`
	
	// Whether to continue conversation after tool execution
	ContinueAfterTools bool `json:"continue_after_tools,omitempty"`
}

// ExecutableToolResponse extends Response with execution details
type ExecutableToolResponse struct {
	Response
	
	// Tool execution results
	ToolResults []ToolResult `json:"tool_results,omitempty"`
	
	// Execution errors (non-fatal)
	ExecutionErrors []ToolExecutionError `json:"execution_errors,omitempty"`
	
	// Execution metadata
	ExecutionMetadata map[string]interface{} `json:"execution_metadata,omitempty"`
}

// Duration is a custom type for JSON marshaling of time.Duration
type Duration struct {
	time.Duration
}

// MarshalJSON implements json.Marshaler
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Duration.String() + `"`), nil
}

// UnmarshalJSON implements json.Unmarshaler
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}