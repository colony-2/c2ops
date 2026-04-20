package llmadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// GeminiExecutableAdapter extends GeminiAdapter with tool execution capabilities
type GeminiExecutableAdapter struct {
	*GeminiAdapter
	toolExecutor      ToolExecutor
	codeExecution     bool
	parallelCalls     bool
}

// NewGeminiExecutableAdapter creates a new Gemini adapter with tool execution capabilities
func NewGeminiExecutableAdapter(apiKey string) (*GeminiExecutableAdapter, error) {
	baseAdapter, err := NewGeminiAdapter(apiKey)
	if err != nil {
		return nil, err
	}
	
	return &GeminiExecutableAdapter{
		GeminiAdapter: baseAdapter,
		codeExecution: false, // Disabled by default for safety
		parallelCalls: true,  // Gemini supports parallel function calls
	}, nil
}

// SetToolExecutor sets the executor for handling tool calls
func (a *GeminiExecutableAdapter) SetToolExecutor(executor ToolExecutor) {
	a.toolExecutor = executor
}

// EnableCodeExecution enables Gemini's native code execution capability
func (a *GeminiExecutableAdapter) EnableCodeExecution(enabled bool) {
	a.codeExecution = enabled
}

// EnableParallelCalls enables or disables parallel function calls
func (a *GeminiExecutableAdapter) EnableParallelCalls(enabled bool) {
	a.parallelCalls = enabled
}

// GenerateAndExecuteTools generates a response and automatically executes any tool calls
func (a *GeminiExecutableAdapter) GenerateAndExecuteTools(
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
	
	// Enhance tools with Gemini-specific features
	enhancedTools := a.enhanceToolsForGemini(tools)
	
	// Add code execution tool if enabled
	if a.codeExecution {
		enhancedTools = append(enhancedTools, a.getCodeExecutionTool())
	}
	
	// Initial generation with tools
	initialResp, err := a.GenerateWithTools(ctx, prompt, enhancedTools, config.Config)
	if err != nil {
		return response, err
	}
	
	response.Response = initialResp
	
	// Track multi-modal context if files are involved
	multiModalContext := []map[string]interface{}{
		{"type": "text", "content": prompt},
	}
	
	// Process tool calls if auto-execute is enabled
	if config.AutoExecute && len(initialResp.ToolCalls) > 0 {
		for rounds < maxRounds && len(response.Response.ToolCalls) > 0 {
			rounds++
			
			// Execute tool calls (parallel or sequential based on configuration)
			toolResults := a.executeToolCalls(ctx, response.Response.ToolCalls, config)
			response.ToolResults = append(response.ToolResults, toolResults...)
			
			// Process results for multi-modal context
			for _, result := range toolResults {
				multiModalContext = append(multiModalContext, a.formatToolResultForContext(result))
			}
			
			// Check if we should continue after tools
			if !config.ContinueAfterTools {
				break
			}
			
			// Build follow-up prompt with multi-modal awareness
			followUpPrompt := a.buildMultiModalFollowUp(toolResults, multiModalContext)
			
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
		}
	}
	
	// Add execution metadata
	response.ExecutionMetadata["rounds"] = rounds
	response.ExecutionMetadata["code_execution_enabled"] = a.codeExecution
	response.ExecutionMetadata["parallel_calls_enabled"] = a.parallelCalls
	response.ExecutionMetadata["tools_executed"] = len(response.ToolResults)
	response.ExecutionMetadata["multi_modal_context_items"] = len(multiModalContext)
	
	return response, nil
}

// executeToolCalls executes tool calls either in parallel or sequentially
func (a *GeminiExecutableAdapter) executeToolCalls(ctx context.Context, calls []ToolCall, config ExecutableToolConfig) []ToolResult {
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
	
	if a.parallelCalls && len(calls) > 1 {
		// Execute tools in parallel (Gemini's strength)
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
				
				// Special handling for code execution tool
				if c.Name == "execute_code" && a.codeExecution {
					result := a.executeCodeTool(toolCtx, c, execConfig)
					resultChan <- resultWithIndex{result: result, index: idx}
				} else {
					result, _ := a.toolExecutor.ExecuteTool(toolCtx, c, execConfig)
					resultChan <- resultWithIndex{result: result, index: idx}
				}
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
			
			// Special handling for code execution tool
			if call.Name == "execute_code" && a.codeExecution {
				results[i] = a.executeCodeTool(toolCtx, call, execConfig)
			} else {
				result, _ := a.toolExecutor.ExecuteTool(toolCtx, call, execConfig)
				results[i] = result
			}
		}
	}
	
	return results
}

// enhanceToolsForGemini optimizes tools for Gemini's capabilities
func (a *GeminiExecutableAdapter) enhanceToolsForGemini(tools []Tool) []Tool {
	enhanced := make([]Tool, len(tools))
	
	for i, tool := range tools {
		enhanced[i] = tool
		
		// Optimize for Gemini's multi-modal capabilities
		switch tool.Name {
		case "write_file":
			// Enhance for file types that Gemini handles natively
			enhanced[i].Description += " Gemini can handle text, images, and various file formats natively."
		case "read_file":
			// Enhance for multi-modal file reading
			enhanced[i].Description += " Gemini can process text, images, PDFs, and other formats directly."
		}
		
		// Add Gemini-specific parameter hints
		var params map[string]interface{}
		if err := json.Unmarshal(enhanced[i].Parameters, &params); err == nil {
			if properties, ok := params["properties"].(map[string]interface{}); ok {
				// Add format hints for better Gemini integration
				for propName, prop := range properties {
					if propMap, ok := prop.(map[string]interface{}); ok {
						if propName == "path" {
							propMap["x-gemini-hint"] = "file-path"
						} else if propName == "content" {
							propMap["x-gemini-hint"] = "multi-modal-content"
						}
					}
				}
			}
			// Marshal back the modified parameters
			if modifiedParams, err := json.Marshal(params); err == nil {
				enhanced[i].Parameters = modifiedParams
			}
		}
	}
	
	return enhanced
}

// getCodeExecutionTool returns Gemini's native code execution tool
func (a *GeminiExecutableAdapter) getCodeExecutionTool() Tool {
	return Tool{
		Name:        "execute_code",
		Description: "Execute Python code in a sandboxed environment (Gemini native feature)",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"code": {
					"type": "string",
					"description": "Python code to execute"
				},
				"language": {
					"type": "string",
					"enum": ["python"],
					"default": "python",
					"description": "Programming language (currently only Python is supported)"
				}
			},
			"required": ["code"]
		}`),
	}
}

// executeCodeTool handles Gemini's native code execution
func (a *GeminiExecutableAdapter) executeCodeTool(ctx context.Context, call ToolCall, config ToolExecutionConfig) ToolResult {
	result := ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Metadata:   make(map[string]interface{}),
	}
	
	// Parse arguments
	var args map[string]interface{}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		result.Error = fmt.Sprintf("failed to parse arguments: %v", err)
		return result
	}
	
	code, ok := args["code"].(string)
	if !ok {
		result.Error = "code parameter is required"
		return result
	}
	
	// In a real implementation, this would call Gemini's code execution API
	// For now, we'll return a simulated result
	result.Success = true
	result.Result = json.RawMessage(fmt.Sprintf(`{
		"output": "Code execution simulated for: %s",
		"language": "python",
		"execution_time_ms": 100
	}`, code[:min(50, len(code))]))
	
	result.Metadata["language"] = "python"
	result.Metadata["code_length"] = len(code)
	
	return result
}

// formatToolResultForContext formats tool results for Gemini's multi-modal context
func (a *GeminiExecutableAdapter) formatToolResultForContext(result ToolResult) map[string]interface{} {
	context := map[string]interface{}{
		"type":         "tool_result",
		"tool_call_id": result.ToolCallID,
		"tool_name":    result.ToolName,
		"success":      result.Success,
	}
	
	if result.Success && result.Result != nil {
		// Check if the result contains file or image data
		var resultData map[string]interface{}
		if err := json.Unmarshal(result.Result, &resultData); err == nil {
			// Check for file operations that might produce multi-modal content
			if result.ToolName == "read_file" {
				if content, ok := resultData["content"].(string); ok {
					// Detect if content might be an image or other media
					if path, ok := resultData["path"].(string); ok {
						if isImagePath(path) {
							context["type"] = "image_result"
							context["mime_type"] = getMimeTypeFromPath(path)
						}
					}
					context["content"] = content
				}
			} else {
				context["result"] = resultData
			}
		} else {
			context["result"] = string(result.Result)
		}
	} else if !result.Success {
		context["error"] = result.Error
	}
	
	return context
}

// buildMultiModalFollowUp builds a follow-up prompt with multi-modal awareness
func (a *GeminiExecutableAdapter) buildMultiModalFollowUp(results []ToolResult, context []map[string]interface{}) string {
	prompt := "Based on the executed operations:\n\n"
	
	// Group results by type
	fileOps := 0
	codeExecs := 0
	otherOps := 0
	
	for _, result := range results {
		switch result.ToolName {
		case "write_file", "read_file", "delete_file", "list_files", "create_directory":
			fileOps++
		case "execute_code":
			codeExecs++
		default:
			otherOps++
		}
		
		if result.Success {
			prompt += fmt.Sprintf("✓ %s completed\n", result.ToolName)
		} else {
			prompt += fmt.Sprintf("✗ %s failed: %s\n", result.ToolName, result.Error)
		}
	}
	
	// Add context-aware guidance
	if fileOps > 0 {
		prompt += fmt.Sprintf("\n%d file operation(s) completed. ", fileOps)
		prompt += "Gemini can directly process any created or modified files including images and documents.\n"
	}
	
	if codeExecs > 0 {
		prompt += fmt.Sprintf("\n%d code execution(s) completed. ", codeExecs)
		prompt += "Results are available for analysis.\n"
	}
	
	prompt += "\nPlease continue with the task, leveraging Gemini's multi-modal capabilities as needed."
	
	return prompt
}

// Helper functions
func isImagePath(path string) bool {
	imageExtensions := []string{".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".svg"}
	for _, ext := range imageExtensions {
		if len(path) >= len(ext) && path[len(path)-len(ext):] == ext {
			return true
		}
	}
	return false
}

func getMimeTypeFromPath(path string) string {
	mimeTypes := map[string]string{
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".gif":  "image/gif",
		".bmp":  "image/bmp",
		".webp": "image/webp",
		".svg":  "image/svg+xml",
		".pdf":  "application/pdf",
		".json": "application/json",
		".xml":  "application/xml",
		".txt":  "text/plain",
	}
	
	for ext, mimeType := range mimeTypes {
		if len(path) >= len(ext) && path[len(path)-len(ext):] == ext {
			return mimeType
		}
	}
	
	return "application/octet-stream"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}