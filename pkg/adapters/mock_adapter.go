package llmadapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	f2 "github.com/colony-2/c2j/pkg/file"
)

// MockAdapter implements all adapter interfaces for testing
type MockAdapter struct {
	// Configurable responses
	Responses     []Response
	responseIndex int

	// Tool execution simulation
	ToolResults  map[string]ToolResult
	ToolExecutor ToolExecutor

	// File handling
	FileCapabilities FileCapabilities
	FileResponses    []Response

	// Error injection
	ErrorOn      string
	ErrorMessage string

	// Tracking
	mu          sync.Mutex
	CallHistory []MockCall

	// Behavior configuration
	SimulateDelay time.Duration
	StreamTokens  []string
}

// MockCall records a method call for verification
type MockCall struct {
	Method    string                 `json:"method"`
	Timestamp time.Time              `json:"timestamp"`
	Arguments map[string]interface{} `json:"arguments"`
	Response  interface{}            `json:"response,omitempty"`
	Error     error                  `json:"error,omitempty"`
}

// NewMockAdapter creates a new mock adapter
func NewMockAdapter() *MockAdapter {
	return &MockAdapter{
		Responses:   []Response{},
		ToolResults: make(map[string]ToolResult),
		CallHistory: []MockCall{},
		FileCapabilities: FileCapabilities{
			SupportedTypes: []f2.FileType{f2.FileTypeText, f2.FileTypeCode, f2.FileTypeConfig},
			MaxFileSize:    1024 * 1024, // 1MB
			MaxFileCount:   10,
			TotalSizeLimit: 10 * 1024 * 1024, // 10MB
		},
	}
}

// Generate implements Adapter interface
func (m *MockAdapter) Generate(ctx context.Context, prompt string, config Config) (Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record the call
	call := MockCall{
		Method:    "Generate",
		Timestamp: time.Now(),
		Arguments: map[string]interface{}{
			"prompt": prompt,
			"config": config,
		},
	}

	// Simulate delay if configured
	if m.SimulateDelay > 0 {
		time.Sleep(m.SimulateDelay)
	}

	// Check for error injection
	if m.ErrorOn == "Generate" {
		err := errors.New(m.ErrorMessage)
		call.Error = err
		m.CallHistory = append(m.CallHistory, call)
		return Response{}, err
	}

	// Return configured response
	var response Response
	if len(m.Responses) > 0 {
		response = m.Responses[m.responseIndex%len(m.Responses)]
		m.responseIndex++
	} else {
		// Generate default response
		response = Response{
			Content: fmt.Sprintf("Mock response to: %s", prompt),
			Usage: Usage{
				PromptTokens:     len(prompt) / 4,
				CompletionTokens: 50,
				TotalTokens:      len(prompt)/4 + 50,
			},
			FinishReason: "stop",
			Model:        config.Model,
		}
	}

	call.Response = response
	m.CallHistory = append(m.CallHistory, call)

	return response, nil
}

// GenerateWithTools implements Adapter interface
func (m *MockAdapter) GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record the call
	call := MockCall{
		Method:    "GenerateWithTools",
		Timestamp: time.Now(),
		Arguments: map[string]interface{}{
			"prompt":     prompt,
			"tools":      tools,
			"config":     config,
			"tool_count": len(tools),
		},
	}

	// Simulate delay if configured
	if m.SimulateDelay > 0 {
		time.Sleep(m.SimulateDelay)
	}

	// Check for error injection
	if m.ErrorOn == "GenerateWithTools" {
		err := errors.New(m.ErrorMessage)
		call.Error = err
		m.CallHistory = append(m.CallHistory, call)
		return Response{}, err
	}

	// Return configured response or generate one with tool calls
	var response Response
	if len(m.Responses) > 0 {
		response = m.Responses[m.responseIndex%len(m.Responses)]
		m.responseIndex++
	} else {
		// Generate response with simulated tool calls
		response = Response{
			Content: "I'll help you with that task.",
			ToolCalls: []ToolCall{
				{
					ID:        "mock_call_1",
					Name:      tools[0].Name,
					Arguments: json.RawMessage(`{"test": "argument"}`),
				},
			},
			Usage: Usage{
				PromptTokens:     len(prompt) / 4,
				CompletionTokens: 75,
				TotalTokens:      len(prompt)/4 + 75,
			},
			FinishReason: "tool_calls",
			Model:        config.Model,
		}
	}

	call.Response = response
	m.CallHistory = append(m.CallHistory, call)

	return response, nil
}

// StreamGenerate implements Adapter interface
func (m *MockAdapter) StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record the call
	call := MockCall{
		Method:    "StreamGenerate",
		Timestamp: time.Now(),
		Arguments: map[string]interface{}{
			"prompt": prompt,
			"config": config,
		},
	}

	// Check for error injection
	if m.ErrorOn == "StreamGenerate" {
		err := errors.New(m.ErrorMessage)
		call.Error = err
		m.CallHistory = append(m.CallHistory, call)
		return nil, err
	}

	// Create stream channel
	stream := make(chan Token, len(m.StreamTokens))

	// Send configured tokens or generate default ones
	go func() {
		defer close(stream)

		tokens := m.StreamTokens
		if len(tokens) == 0 {
			tokens = []string{"Mock", " streaming", " response", " to:", " ", prompt}
		}

		for _, token := range tokens {
			select {
			case <-ctx.Done():
				return
			case stream <- Token{Content: token}:
				if m.SimulateDelay > 0 {
					time.Sleep(m.SimulateDelay / time.Duration(len(tokens)))
				}
			}
		}
	}()

	m.CallHistory = append(m.CallHistory, call)

	return stream, nil
}

// GenerateWithFiles implements FileAdapter interface
func (m *MockAdapter) GenerateWithFiles(ctx context.Context, prompt string, files []f2.File, config Config) (Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record the call
	call := MockCall{
		Method:    "GenerateWithFiles",
		Timestamp: time.Now(),
		Arguments: map[string]interface{}{
			"prompt":     prompt,
			"file_count": len(files),
			"config":     config,
		},
	}

	// Check for error injection
	if m.ErrorOn == "GenerateWithFiles" {
		err := errors.New(m.ErrorMessage)
		call.Error = err
		m.CallHistory = append(m.CallHistory, call)
		return Response{}, err
	}

	// Return configured response
	var response Response
	if len(m.FileResponses) > 0 {
		response = m.FileResponses[0]
		m.FileResponses = m.FileResponses[1:]
	} else {
		response = Response{
			Content: fmt.Sprintf("Processed %d files: %s", len(files), prompt),
			Usage: Usage{
				PromptTokens:     len(prompt) / 4,
				CompletionTokens: 60,
				TotalTokens:      len(prompt)/4 + 60,
			},
			FinishReason: "stop",
			Model:        config.Model,
		}
	}

	call.Response = response
	m.CallHistory = append(m.CallHistory, call)

	return response, nil
}

// GetFileCapabilities implements FileAdapter interface
func (m *MockAdapter) GetFileCapabilities() FileCapabilities {
	return m.FileCapabilities
}

// ValidateFile implements FileAdapter interface
func (m *MockAdapter) ValidateFile(file f2.File) error {
	// Check file type
	supported := false
	for _, t := range m.FileCapabilities.SupportedTypes {
		if file.Type == t {
			supported = true
			break
		}
	}

	if !supported {
		return fmt.Errorf("file type %s not supported", file.Type)
	}

	// Check file size
	if int64(len(file.Content)) > m.FileCapabilities.MaxFileSize {
		return fmt.Errorf("file exceeds maximum size")
	}

	return nil
}

// SetToolExecutor implements ExecutableToolAdapter interface
func (m *MockAdapter) SetToolExecutor(executor ToolExecutor) {
	m.ToolExecutor = executor
}

// GenerateAndExecuteTools implements ExecutableToolAdapter interface
func (m *MockAdapter) GenerateAndExecuteTools(
	ctx context.Context,
	prompt string,
	tools []Tool,
	config ExecutableToolConfig,
) (ExecutableToolResponse, error) {
	// Generate with tools first
	baseResponse, err := m.GenerateWithTools(ctx, prompt, tools, config.Config)
	if err != nil {
		return ExecutableToolResponse{}, err
	}

	response := ExecutableToolResponse{
		Response:          baseResponse,
		ToolResults:       []ToolResult{},
		ExecutionErrors:   []ToolExecutionError{},
		ExecutionMetadata: make(map[string]interface{}),
	}

	// Execute tools if configured
	if config.AutoExecute && len(baseResponse.ToolCalls) > 0 {
		for _, call := range baseResponse.ToolCalls {
			// Use configured results or generate mock results
			if result, ok := m.ToolResults[call.Name]; ok {
				result.ToolCallID = call.ID
				response.ToolResults = append(response.ToolResults, result)
			} else {
				// Generate mock result
				mockResult := ToolResult{
					ToolCallID: call.ID,
					ToolName:   call.Name,
					Success:    true,
					Result:     json.RawMessage(`{"mock": "result"}`),
					Metadata:   map[string]interface{}{"executed_at": time.Now().Unix()},
				}
				response.ToolResults = append(response.ToolResults, mockResult)
			}
		}
	}

	response.ExecutionMetadata["mock"] = true
	response.ExecutionMetadata["tools_executed"] = len(response.ToolResults)

	return response, nil
}

// MockToolExecutor implements ToolExecutor for testing
type MockToolExecutor struct {
	// Predefined results
	Results map[string]interface{}

	// Execution tracking
	ExecutedCalls []ToolCall

	// Error simulation
	ErrorOn string

	// Available tools
	Tools []Tool
}

// NewMockToolExecutor creates a new mock tool executor
func NewMockToolExecutor() *MockToolExecutor {
	return &MockToolExecutor{
		Results:       make(map[string]interface{}),
		ExecutedCalls: []ToolCall{},
		Tools: []Tool{
			{
				Name:        "mock_tool",
				Description: "A mock tool for testing",
				Parameters:  json.RawMessage(`{"type": "object", "properties": {}}`),
			},
		},
	}
}

// ExecuteTool implements ToolExecutor interface
func (e *MockToolExecutor) ExecuteTool(ctx context.Context, call ToolCall, config ToolExecutionConfig) (ToolResult, error) {
	// Track execution
	e.ExecutedCalls = append(e.ExecutedCalls, call)

	// Check for error simulation
	if e.ErrorOn == call.Name {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Success:    false,
			Error:      "simulated error",
		}, fmt.Errorf("simulated error for tool: %s", call.Name)
	}

	// Return configured result or generate mock
	result := ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Success:    true,
		Metadata:   map[string]interface{}{"mock": true},
	}

	if res, ok := e.Results[call.Name]; ok {
		resultJSON, _ := json.Marshal(res)
		result.Result = json.RawMessage(resultJSON)
	} else {
		result.Result = json.RawMessage(`{"status": "executed", "mock": true}`)
	}

	return result, nil
}

// ValidateTool implements ToolExecutor interface
func (e *MockToolExecutor) ValidateTool(call ToolCall, tool Tool) error {
	// Simple validation - just check if tool name matches
	for _, t := range e.Tools {
		if t.Name == call.Name {
			return nil
		}
	}
	return fmt.Errorf("unknown tool: %s", call.Name)
}

// GetAvailableTools implements ToolExecutor interface
func (e *MockToolExecutor) GetAvailableTools() []Tool {
	return e.Tools
}

// Helper methods for test assertions

// GetCallCount returns the number of times a method was called
func (m *MockAdapter) GetCallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := 0
	for _, call := range m.CallHistory {
		if call.Method == method {
			count++
		}
	}
	return count
}

// GetLastCall returns the most recent call of a specific method
func (m *MockAdapter) GetLastCall(method string) *MockCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := len(m.CallHistory) - 1; i >= 0; i-- {
		if m.CallHistory[i].Method == method {
			call := m.CallHistory[i]
			return &call
		}
	}
	return nil
}

// Reset clears all history and resets the mock
func (m *MockAdapter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.CallHistory = []MockCall{}
	m.responseIndex = 0
	m.ErrorOn = ""
	m.ErrorMessage = ""
}

// SetResponse configures the next response to return
func (m *MockAdapter) SetResponse(response Response) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Responses = append(m.Responses, response)
}

// SetError configures error injection
func (m *MockAdapter) SetError(method, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ErrorOn = method
	m.ErrorMessage = message
	if m.ErrorMessage == "" {
		m.ErrorMessage = "mock error"
	}
}
