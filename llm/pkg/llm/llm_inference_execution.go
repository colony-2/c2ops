package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	f2 "github.com/colony-2/c2j/pkg/file"
	llmadapters "github.com/colony-2/colony2/server/llm/adapters"
)

// executeBasic handles basic LLM generation without files or tools
func (a *EnhancedLLMInferenceActivity) executeBasic(
	ctx context.Context,
	adapter llmadapters.Adapter,
	input LLMInferenceInput,
	schema responseSchemaInfo,
) (LLMInferenceOutput, error) {
	config := llmadapters.Config{
		Model:         input.Model,
		Temperature:   input.Temperature,
		MaxTokens:     input.MaxTokens,
		TopP:          input.TopP,
		SystemPrompt:  input.SystemPrompt,
		StopSequences: input.StopSequences,
		Metadata:      input.Metadata,
	}

	// Handle structured response
	if len(input.ResponseSchema) > 0 {
		config.ResponseFormat = "json"
		config.ResponseSchema = input.ResponseSchema.Raw()
	}

	// Generate response
	response, err := adapter.Generate(ctx, input.Prompt, config)
	if err != nil {
		return LLMInferenceOutput{}, fmt.Errorf("generation failed: %w", err)
	}

	normalized, err := a.normalizeResponseContent(response.Content, schema)
	if err != nil {
		return LLMInferenceOutput{}, err
	}

	return LLMInferenceOutput{
		Response:     normalized,
		Model:        response.Model,
		FinishReason: response.FinishReason,
		Usage: Usage{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
		},
	}, nil
}

// executeWithFiles handles LLM generation with file context
func (a *EnhancedLLMInferenceActivity) executeWithFiles(
	ctx context.Context,
	adapter llmadapters.Adapter,
	input LLMInferenceInput,
	schema responseSchemaInfo,
) (LLMInferenceOutput, error) {
	// Check if adapter supports file handling
	fileAdapter, ok := adapter.(llmadapters.FileAdapter)
	if !ok {
		// Fall back to text inclusion
		return a.executeWithFilesFallback(ctx, adapter, input, schema)
	}

	config := llmadapters.Config{
		Model:         input.Model,
		Temperature:   input.Temperature,
		MaxTokens:     input.MaxTokens,
		TopP:          input.TopP,
		SystemPrompt:  input.SystemPrompt,
		StopSequences: input.StopSequences,
		Metadata:      input.Metadata,
	}

	// Handle structured response
	if len(input.ResponseSchema) > 0 {
		config.ResponseFormat = "json"
		config.ResponseSchema = input.ResponseSchema.Raw()
	}

	// Generate with files
	response, err := fileAdapter.GenerateWithFiles(ctx, input.Prompt, input.Files, config)
	if err != nil {
		return LLMInferenceOutput{}, fmt.Errorf("generation with files failed: %w", err)
	}

	normalized, err := a.normalizeResponseContent(response.Content, schema)
	if err != nil {
		return LLMInferenceOutput{}, err
	}

	return LLMInferenceOutput{
		Response:     normalized,
		Model:        response.Model,
		FinishReason: response.FinishReason,
		Usage: Usage{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
		},
	}, nil
}

// executeWithFilesFallback handles files by including them as text in the prompt
func (a *EnhancedLLMInferenceActivity) executeWithFilesFallback(
	ctx context.Context,
	adapter llmadapters.Adapter,
	input LLMInferenceInput,
	schema responseSchemaInfo,
) (LLMInferenceOutput, error) {
	// Build file context as text
	fileContext := a.buildFileContext(input.Files)
	enhancedPrompt := fmt.Sprintf("%s\n\n%s", input.Prompt, fileContext)

	// Update input and execute as basic
	input.Prompt = enhancedPrompt
	input.Files = nil

	return a.executeBasic(ctx, adapter, input, schema)
}

// executeWithTools handles LLM generation with tool support
func (a *EnhancedLLMInferenceActivity) executeWithTools(
	ctx context.Context,
	adapter llmadapters.Adapter,
	input LLMInferenceInput,
	schema responseSchemaInfo,
) (LLMInferenceOutput, error) {
	// Convert tool definitions to adapter format
	tools := a.convertTools(input.Tools)

	config := llmadapters.Config{
		Model:         input.Model,
		Temperature:   input.Temperature,
		MaxTokens:     input.MaxTokens,
		TopP:          input.TopP,
		SystemPrompt:  input.SystemPrompt,
		StopSequences: input.StopSequences,
		Metadata:      input.Metadata,
	}

	// Track tool execution
	var allToolResults []ToolResult
	toolRounds := 0
	currentPrompt := input.Prompt

	// Initial generation with tools
	response, err := adapter.GenerateWithTools(ctx, currentPrompt, tools, config)
	if err != nil {
		return LLMInferenceOutput{}, fmt.Errorf("generation with tools failed: %w", err)
	}

	// Process tool calls if present and execution is enabled
	if len(response.ToolCalls) > 0 && input.ExecuteTools {
		for toolRounds < input.MaxToolRounds && len(response.ToolCalls) > 0 {
			// Execute tool calls
			toolResults, err := a.executeToolCalls(ctx, response.ToolCalls, input)
			if err != nil && !input.ContinueOnToolError {
				return LLMInferenceOutput{}, fmt.Errorf("tool execution failed: %w", err)
			}

			allToolResults = append(allToolResults, toolResults...)
			toolRounds++

			// Check if we need another round
			if toolRounds >= input.MaxToolRounds || response.FinishReason != "tool_calls" {
				break
			}

			// Update prompt with tool results for next round
			currentPrompt = a.formatToolResults(toolResults)

			// Make another call with updated context
			response, err = adapter.GenerateWithTools(ctx, currentPrompt, tools, config)
			if err != nil {
				return LLMInferenceOutput{}, fmt.Errorf("follow-up generation failed: %w", err)
			}
		}
	}

	// Final response after all tool rounds
	finalResponse, err := adapter.Generate(ctx, currentPrompt, config)
	if err != nil {
		return LLMInferenceOutput{}, fmt.Errorf("final generation failed: %w", err)
	}

	normalized, err := a.normalizeResponseContent(finalResponse.Content, schema)
	if err != nil {
		return LLMInferenceOutput{}, err
	}

	return LLMInferenceOutput{
		Response:     normalized,
		Model:        finalResponse.Model,
		FinishReason: finalResponse.FinishReason,
		Usage: Usage{
			PromptTokens:     finalResponse.Usage.PromptTokens,
			CompletionTokens: finalResponse.Usage.CompletionTokens,
			TotalTokens:      finalResponse.Usage.TotalTokens,
		},
		ToolResults:    allToolResults,
		ToolRoundsUsed: toolRounds,
	}, nil
}

// executeWithFilesAndTools handles LLM generation with both files and tools
func (a *EnhancedLLMInferenceActivity) executeWithFilesAndTools(
	ctx context.Context,
	adapter llmadapters.Adapter,
	input LLMInferenceInput,
	schema responseSchemaInfo,
) (LLMInferenceOutput, error) {
	// Check if adapter supports unified operations
	unifiedAdapter, ok := adapter.(llmadapters.UnifiedAdapter)
	if !ok {
		// Create unified adapter from existing adapters
		fileAdapter, hasFiles := adapter.(llmadapters.FileAdapter)
		execAdapter, hasExec := adapter.(llmadapters.ExecutableToolAdapter)

		if hasFiles && hasExec {
			unifiedAdapter = llmadapters.NewUnifiedAdapter(fileAdapter, execAdapter)
		} else {
			// Fall back to sequential execution
			return a.executeSequential(ctx, adapter, input, schema)
		}
	}

	// Convert tools to adapter format
	tools := a.convertTools(input.Tools)

	// Build unified config
	unifiedConfig := llmadapters.UnifiedConfig{
		ExecutableToolConfig: llmadapters.ExecutableToolConfig{
			Config: llmadapters.Config{
				Model:         input.Model,
				Temperature:   input.Temperature,
				MaxTokens:     input.MaxTokens,
				TopP:          input.TopP,
				SystemPrompt:  input.SystemPrompt,
				StopSequences: input.StopSequences,
				Metadata:      input.Metadata,
			},
			AutoExecute:        input.ExecuteTools,
			MaxToolRounds:      input.MaxToolRounds,
			WorkingDirectory:   input.ToolWorkingDir,
			ContinueAfterTools: true,
		},
		FileHandling: llmadapters.FileHandlingMode(input.FileHandling),
	}

	// Handle structured response
	if len(input.ResponseSchema) > 0 {
		unifiedConfig.ExecutableToolConfig.Config.ResponseFormat = "json"
		unifiedConfig.ExecutableToolConfig.Config.ResponseSchema = input.ResponseSchema.Raw()
	}

	// Parse tool timeout
	if input.ToolTimeout != "" {
		if duration, err := time.ParseDuration(input.ToolTimeout); err == nil {
			unifiedConfig.ExecutableToolConfig.ToolTimeout = llmadapters.Duration{Duration: duration}
		}
	}

	// Execute with files and tools
	response, err := unifiedAdapter.GenerateWithFilesAndTools(
		ctx,
		input.Prompt,
		input.Files,
		tools,
		unifiedConfig,
	)
	if err != nil {
		return LLMInferenceOutput{}, fmt.Errorf("unified generation failed: %w", err)
	}

	normalized, err := a.normalizeResponseContent(response.Response.Content, schema)
	if err != nil {
		return LLMInferenceOutput{}, err
	}

	// Convert tool results
	var toolResults []ToolResult
	for _, result := range response.ToolResults {
		toolResults = append(toolResults, ToolResult{
			ToolCallID: result.ToolCallID,
			ToolName:   result.ToolName,
			Success:    result.Success,
			Result:     result.Result,
			Error:      result.Error,
		})
	}

	return LLMInferenceOutput{
		Response:     normalized,
		Model:        response.Response.Model,
		FinishReason: response.Response.FinishReason,
		Usage: Usage{
			PromptTokens:     response.Response.Usage.PromptTokens,
			CompletionTokens: response.Response.Usage.CompletionTokens,
			TotalTokens:      response.Response.Usage.TotalTokens,
		},
		ToolResults:    toolResults,
		ToolRoundsUsed: len(response.ToolResults),
		FilesWritten:   response.FilesCreated,
		FilesRead:      response.FilesProcessed,
		FilesDeleted:   response.FilesDeleted,
	}, nil
}

// executeSequential handles files then tools sequentially
func (a *EnhancedLLMInferenceActivity) executeSequential(
	ctx context.Context,
	adapter llmadapters.Adapter,
	input LLMInferenceInput,
	schema responseSchemaInfo,
) (LLMInferenceOutput, error) {
	// First handle files
	fileOutput, err := a.executeWithFiles(ctx, adapter, input, schema)
	if err != nil {
		return LLMInferenceOutput{}, err
	}

	// Then handle tools with the file-enriched context
	input.Prompt = fileOutput.Response
	input.Files = nil // Clear files since they've been processed

	return a.executeWithTools(ctx, adapter, input, schema)
}

// executeToolCalls executes a list of tool calls
func (a *EnhancedLLMInferenceActivity) executeToolCalls(
	ctx context.Context,
	toolCalls []llmadapters.ToolCall,
	input LLMInferenceInput,
) ([]ToolResult, error) {
	var results []ToolResult

	for _, call := range toolCalls {
		startTime := time.Now()

		// Parse arguments
		var args map[string]interface{}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			results = append(results, ToolResult{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Success:    false,
				Error:      fmt.Sprintf("failed to parse arguments: %v", err),
			})
			continue
		}

		// Execute tool
		result, err := a.toolExecutor.Execute(ctx, ToolExecutionRequest{
			Name:       call.Name,
			Arguments:  args,
			WorkingDir: input.ToolWorkingDir,
		})

		results = append(results, ToolResult{
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Success:    err == nil,
			Result:     result,
			Error:      a.errorString(err),
			Duration:   time.Since(startTime).Milliseconds(),
		})
	}

	return results, nil
}

// convertTools converts tool definitions to adapter format
func (a *EnhancedLLMInferenceActivity) convertTools(tools []ToolDefinition) []llmadapters.Tool {
	adapterTools := make([]llmadapters.Tool, len(tools))
	for i, tool := range tools {
		adapterTools[i] = llmadapters.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		}
	}
	return adapterTools
}

// buildFileContext builds a text representation of files
func (a *EnhancedLLMInferenceActivity) buildFileContext(files []f2.File) string {
	context := "### File Context ###\n\n"

	for _, file := range files {
		context += fmt.Sprintf("**File: %s** (Type: %s, Size: %d bytes)\n",
			file.Path, file.Type, len(file.Content))

		// Include text content
		if file.Type == f2.FileTypeText ||
			file.Type == f2.FileTypeCode ||
			file.Type == f2.FileTypeConfig ||
			file.Type == f2.FileTypeMarkdown {
			content := string(file.Content)
			if len(content) > 1000 {
				content = content[:1000] + "...\n[Content truncated]"
			}
			context += fmt.Sprintf("```\n%s\n```\n\n", content)
		} else {
			context += fmt.Sprintf("[%s file - content not displayed]\n\n", file.Type)
		}
	}

	return context
}

// formatToolResults formats tool results for the next prompt
func (a *EnhancedLLMInferenceActivity) formatToolResults(results []ToolResult) string {
	prompt := "Based on the tool execution results:\n\n"

	for _, result := range results {
		if result.Success {
			prompt += fmt.Sprintf("✓ %s executed successfully\n", result.ToolName)
			if result.Result != nil {
				resultJSON, _ := json.MarshalIndent(result.Result, "  ", "  ")
				prompt += fmt.Sprintf("  Result: %s\n", string(resultJSON))
			}
		} else {
			prompt += fmt.Sprintf("✗ %s failed: %s\n", result.ToolName, result.Error)
		}
	}

	prompt += "\nPlease continue with the task based on these results."
	return prompt
}

// errorString converts an error to string, handling nil
func (a *EnhancedLLMInferenceActivity) errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (a *EnhancedLLMInferenceActivity) normalizeResponseContent(content string, schema responseSchemaInfo) (string, error) {
	if !schema.hasSchema() {
		return content, nil
	}

	parsed, err := parseStructuredJSON(content)
	if err != nil {
		return "", fmt.Errorf("response is not valid JSON for response_schema (expected %s): %w", schema.expectedOrDefault(), err)
	}

	if err := schema.compiled.Validate(parsed); err != nil {
		return "", fmt.Errorf(
			"response does not match response_schema (expected %s, got %s): %w",
			schema.expectedOrDefault(),
			describeValueType(parsed),
			err,
		)
	}

	normalized, err := json.Marshal(parsed)
	if err != nil {
		return "", fmt.Errorf("failed to normalize structured response: %w", err)
	}

	return string(normalized), nil
}

func parseStructuredJSON(raw string) (interface{}, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("response was empty")
	}

	var parsed interface{}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, err
	}

	parsed = unwrapSingleElementArray(parsed)

	if s, ok := parsed.(string); ok {
		inner := strings.TrimSpace(s)
		if inner != "" && (strings.HasPrefix(inner, "{") || strings.HasPrefix(inner, "[")) {
			var innerParsed interface{}
			if err := json.Unmarshal([]byte(inner), &innerParsed); err == nil {
				parsed = unwrapSingleElementArray(innerParsed)
			}
		}
	}

	return parsed, nil
}

func unwrapSingleElementArray(v interface{}) interface{} {
	if arr, ok := v.([]interface{}); ok && len(arr) == 1 {
		return arr[0]
	}
	return v
}

func describeValueType(v interface{}) string {
	switch v.(type) {
	case map[string]interface{}:
		return "object"
	case []interface{}:
		return "array"
	case string:
		return "string"
	case float64, int, int64, uint64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}
