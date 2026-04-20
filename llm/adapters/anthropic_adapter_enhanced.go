package llmadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AnthropicExecutableAdapter extends AnthropicAdapter with tool execution capabilities
type AnthropicExecutableAdapter struct {
	*AnthropicAdapter
	toolExecutor        ToolExecutor
	toolDescriptionMode string // "concise" or "detailed"
}

// NewAnthropicExecutableAdapter creates a new Anthropic adapter with tool execution capabilities
func NewAnthropicExecutableAdapter(apiKey string) (*AnthropicExecutableAdapter, error) {
	baseAdapter, err := NewAnthropicAdapter(apiKey)
	if err != nil {
		return nil, err
	}
	
	return &AnthropicExecutableAdapter{
		AnthropicAdapter:    baseAdapter,
		toolDescriptionMode: "detailed", // Claude benefits from detailed descriptions
	}, nil
}

// SetToolExecutor sets the executor for handling tool calls
func (a *AnthropicExecutableAdapter) SetToolExecutor(executor ToolExecutor) {
	a.toolExecutor = executor
}

// SetToolDescriptionMode sets the tool description mode ("concise" or "detailed")
func (a *AnthropicExecutableAdapter) SetToolDescriptionMode(mode string) {
	if mode == "concise" || mode == "detailed" {
		a.toolDescriptionMode = mode
	}
}

// GenerateAndExecuteTools generates a response and automatically executes any tool calls
func (a *AnthropicExecutableAdapter) GenerateAndExecuteTools(
	ctx context.Context,
	prompt string,
	tools []Tool,
	config ExecutableToolConfig,
) (ExecutableToolResponse, error) {
	response := ExecutableToolResponse{
		ToolResults:       []ToolResult{},
		ExecutionErrors:   []ToolExecutionError{},
		ExecutionMetadata: make(map[string]interface{}),
	}
	
	// Track execution rounds
	rounds := 0
	maxRounds := config.MaxToolRounds
	if maxRounds <= 0 {
		maxRounds = 5 // Default max rounds
	}
	
	// Enhance tool descriptions if in detailed mode
	enhancedTools := a.enhanceToolDescriptions(tools)
	
	// Build conversation with chain-of-thought prompting for tools
	conversationPrompt := prompt
	if len(tools) > 0 && a.toolDescriptionMode == "detailed" {
		conversationPrompt = fmt.Sprintf(
			"%s\n\nYou have access to the following tools. Think step-by-step about which tools to use and in what order to accomplish the task effectively.",
			prompt,
		)
	}
	
	// Initial generation with tools
	initialResp, err := a.GenerateWithTools(ctx, conversationPrompt, enhancedTools, config.Config)
	if err != nil {
		return response, err
	}
	
	response.Response = initialResp
	
	// Track conversation history for Claude's context
	conversationHistory := []map[string]interface{}{
		{"role": "user", "content": prompt},
		{"role": "assistant", "content": initialResp.Content},
	}
	
	// Process tool calls if auto-execute is enabled
	if config.AutoExecute && len(initialResp.ToolCalls) > 0 {
		for rounds < maxRounds && len(response.Response.ToolCalls) > 0 {
			rounds++
			
			// Execute tool calls sequentially (Claude typically expects sequential execution)
			toolResults := a.executeToolCalls(ctx, response.Response.ToolCalls, config)
			response.ToolResults = append(response.ToolResults, toolResults...)
			
			// Add tool results to conversation history
			for _, result := range toolResults {
				conversationHistory = append(conversationHistory, a.formatToolResult(result))
			}
			
			// Check if we should continue after tools
			if !config.ContinueAfterTools {
				break
			}
			
			// Build follow-up prompt with chain-of-thought
			followUpPrompt := a.buildFollowUpPrompt(toolResults, rounds < maxRounds)
			
			// Make follow-up call with tool results
			followUpResp, err := a.GenerateWithTools(ctx, followUpPrompt, enhancedTools, config.Config)
			if err != nil {
				response.ExecutionErrors = append(response.ExecutionErrors, ToolExecutionError{
					ToolCallID: "",
					ToolName:   "follow_up",
					Error:      err.Error(),
					Timestamp:  time.Now().Unix(),
				})
				break
			}
			
			// Update response with the latest content
			response.Response.Content += "\n\n" + followUpResp.Content
			response.Response.ToolCalls = followUpResp.ToolCalls
			response.Response.Usage.PromptTokens += followUpResp.Usage.PromptTokens
			response.Response.Usage.CompletionTokens += followUpResp.Usage.CompletionTokens
			response.Response.Usage.TotalTokens += followUpResp.Usage.TotalTokens
			
			conversationHistory = append(conversationHistory, map[string]interface{}{
				"role":    "assistant",
				"content": followUpResp.Content,
			})
		}
	}
	
	// Add execution metadata
	response.ExecutionMetadata["rounds"] = rounds
	response.ExecutionMetadata["description_mode"] = a.toolDescriptionMode
	response.ExecutionMetadata["tools_executed"] = len(response.ToolResults)
	response.ExecutionMetadata["conversation_turns"] = len(conversationHistory)
	
	return response, nil
}

// executeToolCalls executes a list of tool calls sequentially
func (a *AnthropicExecutableAdapter) executeToolCalls(ctx context.Context, calls []ToolCall, config ExecutableToolConfig) []ToolResult {
	if a.toolExecutor == nil {
		// Return error results if no executor is set
		results := make([]ToolResult, len(calls))
		for i, call := range calls {
			results[i] = ToolResult{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Success:    false,
				Error:      "no tool executor configured",
			}
		}
		return results
	}
	
	results := make([]ToolResult, len(calls))
	
	// Execute tools sequentially (Claude's preference for sequential execution)
	for i, call := range calls {
		// Create a timeout context for individual tool execution
		toolCtx := ctx
		if config.ToolTimeout.Duration > 0 {
			var cancel context.CancelFunc
			toolCtx, cancel = context.WithTimeout(ctx, config.ToolTimeout.Duration)
			defer cancel()
		}
		
		execConfig := ToolExecutionConfig{
			WorkingDirectory: config.WorkingDirectory,
			Timeout:          config.ToolTimeout.Duration,
		}
		
		// Add chain-of-thought context to metadata
		result, _ := a.toolExecutor.ExecuteTool(toolCtx, call, execConfig)
		
		// Enhance result metadata with execution context
		if result.Metadata == nil {
			result.Metadata = make(map[string]interface{})
		}
		result.Metadata["execution_order"] = i + 1
		result.Metadata["execution_time"] = time.Now().Unix()
		
		results[i] = result
	}
	
	return results
}

// enhanceToolDescriptions enhances tool descriptions based on the mode
func (a *AnthropicExecutableAdapter) enhanceToolDescriptions(tools []Tool) []Tool {
	if a.toolDescriptionMode != "detailed" {
		return tools
	}
	
	enhanced := make([]Tool, len(tools))
	for i, tool := range tools {
		enhanced[i] = tool
		
		// Add detailed descriptions for common tools
		switch tool.Name {
		case "write_file":
			enhanced[i].Description = "Write or create a file with specified content. " +
				"Use this when you need to save data, create configuration files, or generate output. " +
				"The file will be created if it doesn't exist, or overwritten if it does."
		case "read_file":
			enhanced[i].Description = "Read the contents of a file. " +
				"Use this to examine existing files, load configuration, or inspect previous outputs. " +
				"Returns the complete file content as a string."
		case "list_files":
			enhanced[i].Description = "List all files and directories in a specified path. " +
				"Use this to explore the file structure, find relevant files, or verify file existence. " +
				"Returns a list with file names, sizes, and types."
		case "delete_file":
			enhanced[i].Description = "Delete a file permanently. " +
				"Use this carefully to remove unnecessary files or clean up temporary data. " +
				"This operation cannot be undone."
		case "create_directory":
			enhanced[i].Description = "Create a new directory at the specified path. " +
				"Use this to organize files into folders or prepare directory structures. " +
				"Creates parent directories if they don't exist."
		}
	}
	
	return enhanced
}

// formatToolResult formats a tool result for Claude's context
func (a *AnthropicExecutableAdapter) formatToolResult(result ToolResult) map[string]interface{} {
	content := ""
	
	if result.Success {
		if result.Result != nil {
			// Try to format JSON results nicely
			var jsonData interface{}
			if err := json.Unmarshal(result.Result, &jsonData); err == nil {
				if formatted, err := json.MarshalIndent(jsonData, "", "  "); err == nil {
					content = fmt.Sprintf("Tool '%s' executed successfully:\n%s", result.ToolName, string(formatted))
				} else {
					content = fmt.Sprintf("Tool '%s' executed successfully:\n%s", result.ToolName, string(result.Result))
				}
			} else {
				content = fmt.Sprintf("Tool '%s' executed successfully:\n%s", result.ToolName, string(result.Result))
			}
		} else {
			content = fmt.Sprintf("Tool '%s' executed successfully", result.ToolName)
		}
	} else {
		content = fmt.Sprintf("Tool '%s' failed: %s", result.ToolName, result.Error)
	}
	
	return map[string]interface{}{
		"role":         "tool",
		"tool_call_id": result.ToolCallID,
		"name":         result.ToolName,
		"content":      content,
	}
}

// buildFollowUpPrompt builds a chain-of-thought prompt for the follow-up call
func (a *AnthropicExecutableAdapter) buildFollowUpPrompt(results []ToolResult, moreToolsAvailable bool) string {
	prompt := "Based on the tool execution results:\n\n"
	
	// Summarize results with chain-of-thought
	successCount := 0
	failureCount := 0
	
	for _, result := range results {
		if result.Success {
			successCount++
			prompt += fmt.Sprintf("✓ %s completed successfully", result.ToolName)
			
			// Add key information from successful results
			if result.Result != nil {
				var resultData map[string]interface{}
				if err := json.Unmarshal(result.Result, &resultData); err == nil {
					// Extract and display relevant details
					if path, ok := resultData["path"].(string); ok {
						prompt += fmt.Sprintf(" (path: %s)", path)
					}
					if count, ok := resultData["count"].(float64); ok {
						prompt += fmt.Sprintf(" (count: %d)", int(count))
					}
					if size, ok := resultData["size"].(float64); ok {
						prompt += fmt.Sprintf(" (size: %d bytes)", int(size))
					}
				}
			}
			prompt += "\n"
		} else {
			failureCount++
			prompt += fmt.Sprintf("✗ %s failed: %s\n", result.ToolName, result.Error)
		}
	}
	
	prompt += fmt.Sprintf("\nSummary: %d successful, %d failed\n\n", successCount, failureCount)
	
	// Add guidance based on results
	if failureCount > 0 {
		prompt += "Some tools failed. Please consider alternative approaches or provide guidance on how to proceed.\n"
	}
	
	if moreToolsAvailable {
		prompt += "You may continue using tools if needed to complete the task. Think step-by-step about what needs to be done next.\n"
	} else {
		prompt += "Please provide a final summary of what was accomplished.\n"
	}
	
	return prompt
}