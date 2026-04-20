package llmadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// OpenAIExecutableAdapter extends OpenAIAdapter with tool execution capabilities
type OpenAIExecutableAdapter struct {
	*OpenAIAdapter
	toolExecutor       ToolExecutor
	parallelToolCalls  bool
}

// NewOpenAIExecutableAdapter creates a new OpenAI adapter with tool execution capabilities
func NewOpenAIExecutableAdapter(apiKey string) (*OpenAIExecutableAdapter, error) {
	baseAdapter, err := NewOpenAIAdapter(apiKey)
	if err != nil {
		return nil, err
	}
	
	return &OpenAIExecutableAdapter{
		OpenAIAdapter:     baseAdapter,
		parallelToolCalls: true, // GPT-4 supports parallel tool calls by default
	}, nil
}

// SetToolExecutor sets the executor for handling tool calls
func (a *OpenAIExecutableAdapter) SetToolExecutor(executor ToolExecutor) {
	a.toolExecutor = executor
}

// EnableParallelToolCalls enables or disables parallel tool calls (GPT-4 feature)
func (a *OpenAIExecutableAdapter) EnableParallelToolCalls(enabled bool) {
	a.parallelToolCalls = enabled
}

// GenerateAndExecuteTools generates a response and automatically executes any tool calls
func (a *OpenAIExecutableAdapter) GenerateAndExecuteTools(
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
	
	// Initial generation with tools
	initialResp, err := a.GenerateWithTools(ctx, prompt, tools, config.Config)
	if err != nil {
		return response, err
	}
	
	response.Response = initialResp
	messages := []interface{}{
		map[string]string{"role": "user", "content": prompt},
		map[string]string{"role": "assistant", "content": initialResp.Content},
	}
	
	// Process tool calls if auto-execute is enabled
	if config.AutoExecute && len(initialResp.ToolCalls) > 0 {
		for rounds < maxRounds && len(response.Response.ToolCalls) > 0 {
			rounds++
			
			// Execute tool calls
			toolResults := a.executeToolCalls(ctx, response.Response.ToolCalls, config)
			response.ToolResults = append(response.ToolResults, toolResults...)
			
			// Check if we should continue after tools
			if !config.ContinueAfterTools {
				break
			}
			
			// Prepare tool results for next round
			toolMessages := a.formatToolResults(toolResults)
			messages = append(messages, toolMessages...)
			
			// Make follow-up call with tool results
			followUpPrompt := a.buildFollowUpPrompt(toolResults)
			followUpResp, err := a.GenerateWithTools(ctx, followUpPrompt, tools, config.Config)
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
			
			messages = append(messages, map[string]string{
				"role": "assistant",
				"content": followUpResp.Content,
			})
		}
	}
	
	// Add execution metadata
	response.ExecutionMetadata["rounds"] = rounds
	response.ExecutionMetadata["parallel_execution"] = a.parallelToolCalls
	response.ExecutionMetadata["tools_executed"] = len(response.ToolResults)
	
	return response, nil
}

// executeToolCalls executes a list of tool calls
func (a *OpenAIExecutableAdapter) executeToolCalls(ctx context.Context, calls []ToolCall, config ExecutableToolConfig) []ToolResult {
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
	
	if a.parallelToolCalls && len(calls) > 1 {
		// Execute tools in parallel
		type resultWithIndex struct {
			result ToolResult
			index  int
		}
		
		resultChan := make(chan resultWithIndex, len(calls))
		
		for i, call := range calls {
			go func(idx int, c ToolCall) {
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
				
				result, _ := a.toolExecutor.ExecuteTool(toolCtx, c, execConfig)
				resultChan <- resultWithIndex{result: result, index: idx}
			}(i, call)
		}
		
		// Collect results
		for i := 0; i < len(calls); i++ {
			res := <-resultChan
			results[res.index] = res.result
		}
	} else {
		// Execute tools sequentially
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
			
			result, _ := a.toolExecutor.ExecuteTool(toolCtx, call, execConfig)
			results[i] = result
		}
	}
	
	return results
}

// formatToolResults formats tool results for inclusion in the conversation
func (a *OpenAIExecutableAdapter) formatToolResults(results []ToolResult) []interface{} {
	messages := []interface{}{}
	
	for _, result := range results {
		var content string
		if result.Success {
			if result.Result != nil {
				content = string(result.Result)
			} else {
				content = "Tool executed successfully"
			}
		} else {
			content = fmt.Sprintf("Error: %s", result.Error)
		}
		
		messages = append(messages, map[string]interface{}{
			"role":         "tool",
			"tool_call_id": result.ToolCallID,
			"name":         result.ToolName,
			"content":      content,
		})
	}
	
	return messages
}

// buildFollowUpPrompt builds a prompt for the follow-up call after tool execution
func (a *OpenAIExecutableAdapter) buildFollowUpPrompt(results []ToolResult) string {
	// Build a summary of tool results
	summary := "Based on the tool execution results:\n"
	
	for _, result := range results {
		if result.Success {
			summary += fmt.Sprintf("- %s executed successfully\n", result.ToolName)
			if result.Result != nil {
				// Try to extract key information from the result
				var resultData map[string]interface{}
				if err := json.Unmarshal(result.Result, &resultData); err == nil {
					// Add relevant details from the result
					for key, value := range resultData {
						if key != "content" { // Skip large content fields
							summary += fmt.Sprintf("  %s: %v\n", key, value)
						}
					}
				}
			}
		} else {
			summary += fmt.Sprintf("- %s failed: %s\n", result.ToolName, result.Error)
		}
	}
	
	summary += "\nPlease continue with the task based on these results."
	return summary
}