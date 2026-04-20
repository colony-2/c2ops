package llmadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// StructuredLogger provides detailed logging
type StructuredLogger interface {
	// LogRequest logs LLM request details
	LogRequest(ctx context.Context, prompt string, config Config)
	
	// LogResponse logs LLM response
	LogResponse(ctx context.Context, response Response)
	
	// LogToolExecution logs tool execution details
	LogToolExecution(ctx context.Context, call ToolCall, result ToolResult)
	
	// LogError logs errors with context
	LogError(ctx context.Context, err error, metadata map[string]interface{})
	
	// SetLogLevel sets the logging level
	SetLogLevel(level LogLevel)
}

// LogLevel represents the severity of a log message
type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

// LogEntry represents a structured log entry
type LogEntry struct {
	Timestamp   time.Time              `json:"timestamp"`
	Level       string                 `json:"level"`
	Message     string                 `json:"message"`
	Context     map[string]interface{} `json:"context,omitempty"`
	RequestID   string                 `json:"request_id,omitempty"`
	Provider    string                 `json:"provider,omitempty"`
	Model       string                 `json:"model,omitempty"`
	Duration    *time.Duration         `json:"duration,omitempty"`
	TokenCount  *int                   `json:"token_count,omitempty"`
	Error       string                 `json:"error,omitempty"`
}

// DefaultStructuredLogger implements StructuredLogger
type DefaultStructuredLogger struct {
	logger   *log.Logger
	logLevel LogLevel
	provider string
}

// NewStructuredLogger creates a new structured logger
func NewStructuredLogger(provider string) StructuredLogger {
	return &DefaultStructuredLogger{
		logger:   log.New(os.Stdout, fmt.Sprintf("[%s] ", provider), log.LstdFlags),
		logLevel: LogLevelInfo,
		provider: provider,
	}
}

// LogRequest logs LLM request details
func (l *DefaultStructuredLogger) LogRequest(ctx context.Context, prompt string, config Config) {
	if l.logLevel > LogLevelDebug {
		return
	}
	
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     "DEBUG",
		Message:   "LLM Request",
		Provider:  l.provider,
		Model:     config.Model,
		Context: map[string]interface{}{
			"prompt_length": len(prompt),
			"temperature":   config.Temperature,
			"max_tokens":    config.MaxTokens,
		},
	}
	
	// Add request ID from context if available
	if reqID := getRequestID(ctx); reqID != "" {
		entry.RequestID = reqID
	}
	
	// Truncate prompt for logging
	if len(prompt) > 200 {
		entry.Context["prompt_preview"] = prompt[:200] + "..."
	} else {
		entry.Context["prompt_preview"] = prompt
	}
	
	l.logJSON(entry)
}

// LogResponse logs LLM response
func (l *DefaultStructuredLogger) LogResponse(ctx context.Context, response Response) {
	if l.logLevel > LogLevelInfo {
		return
	}
	
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     "INFO",
		Message:   "LLM Response",
		Provider:  l.provider,
		Model:     response.Model,
		Context: map[string]interface{}{
			"finish_reason":     response.FinishReason,
			"tool_calls_count":  len(response.ToolCalls),
			"content_length":    len(response.Content),
			"prompt_tokens":     response.Usage.PromptTokens,
			"completion_tokens": response.Usage.CompletionTokens,
			"total_tokens":      response.Usage.TotalTokens,
		},
	}
	
	// Add request ID from context if available
	if reqID := getRequestID(ctx); reqID != "" {
		entry.RequestID = reqID
	}
	
	tokens := response.Usage.TotalTokens
	entry.TokenCount = &tokens
	
	l.logJSON(entry)
}

// LogToolExecution logs tool execution details
func (l *DefaultStructuredLogger) LogToolExecution(ctx context.Context, call ToolCall, result ToolResult) {
	level := LogLevelInfo
	levelStr := "INFO"
	
	if !result.Success {
		level = LogLevelWarn
		levelStr = "WARN"
	}
	
	if l.logLevel > level {
		return
	}
	
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     levelStr,
		Message:   fmt.Sprintf("Tool Execution: %s", call.Name),
		Provider:  l.provider,
		Context: map[string]interface{}{
			"tool_name":    call.Name,
			"tool_call_id": call.ID,
			"success":      result.Success,
		},
	}
	
	// Add request ID from context if available
	if reqID := getRequestID(ctx); reqID != "" {
		entry.RequestID = reqID
	}
	
	// Add error if present
	if result.Error != "" {
		entry.Error = result.Error
	}
	
	// Add metadata if present
	if result.Metadata != nil {
		entry.Context["tool_metadata"] = result.Metadata
	}
	
	// Parse and log arguments (sanitized)
	if len(call.Arguments) > 0 {
		var args map[string]interface{}
		if err := json.Unmarshal(call.Arguments, &args); err == nil {
			// Sanitize arguments before logging
			sanitized := sanitizeForLogging(args)
			entry.Context["arguments"] = sanitized
		}
	}
	
	l.logJSON(entry)
}

// LogError logs errors with context
func (l *DefaultStructuredLogger) LogError(ctx context.Context, err error, metadata map[string]interface{}) {
	if l.logLevel > LogLevelError {
		return
	}
	
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     "ERROR",
		Message:   "Error occurred",
		Provider:  l.provider,
		Error:     err.Error(),
		Context:   metadata,
	}
	
	// Add request ID from context if available
	if reqID := getRequestID(ctx); reqID != "" {
		entry.RequestID = reqID
	}
	
	l.logJSON(entry)
}

// SetLogLevel sets the logging level
func (l *DefaultStructuredLogger) SetLogLevel(level LogLevel) {
	l.logLevel = level
}

// logJSON logs an entry as JSON
func (l *DefaultStructuredLogger) logJSON(entry LogEntry) {
	jsonData, err := json.Marshal(entry)
	if err != nil {
		l.logger.Printf("Failed to marshal log entry: %v", err)
		return
	}
	
	l.logger.Println(string(jsonData))
}

// LoggingMiddleware wraps an adapter with logging
type LoggingMiddleware struct {
	adapter Adapter
	logger  StructuredLogger
}

// NewLoggingMiddleware creates a new logging middleware
func NewLoggingMiddleware(adapter Adapter, logger StructuredLogger) Adapter {
	return &LoggingMiddleware{
		adapter: adapter,
		logger:  logger,
	}
}

// Generate wraps the Generate method with logging
func (m *LoggingMiddleware) Generate(ctx context.Context, prompt string, config Config) (Response, error) {
	// Log request
	m.logger.LogRequest(ctx, prompt, config)
	
	// Execute
	start := time.Now()
	response, err := m.adapter.Generate(ctx, prompt, config)
	duration := time.Since(start)
	
	// Log response or error
	if err != nil {
		m.logger.LogError(ctx, err, map[string]interface{}{
			"method":   "Generate",
			"model":    config.Model,
			"duration": duration.String(),
		})
	} else {
		// Add duration to response context
		if response.Metadata == nil {
			response.Metadata = make(map[string]any)
		}
		response.Metadata["duration_ms"] = duration.Milliseconds()
		
		m.logger.LogResponse(ctx, response)
	}
	
	return response, err
}

// GenerateWithTools wraps the GenerateWithTools method with logging
func (m *LoggingMiddleware) GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error) {
	// Log request with tool info
	m.logger.LogRequest(ctx, prompt, config)
	
	// Execute
	start := time.Now()
	response, err := m.adapter.GenerateWithTools(ctx, prompt, tools, config)
	duration := time.Since(start)
	
	// Log response or error
	if err != nil {
		m.logger.LogError(ctx, err, map[string]interface{}{
			"method":     "GenerateWithTools",
			"model":      config.Model,
			"tool_count": len(tools),
			"duration":   duration.String(),
		})
	} else {
		// Add duration and tool info to response context
		if response.Metadata == nil {
			response.Metadata = make(map[string]any)
		}
		response.Metadata["duration_ms"] = duration.Milliseconds()
		response.Metadata["tools_provided"] = len(tools)
		response.Metadata["tools_called"] = len(response.ToolCalls)
		
		m.logger.LogResponse(ctx, response)
	}
	
	return response, err
}

// StreamGenerate wraps the StreamGenerate method
func (m *LoggingMiddleware) StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error) {
	// Log request
	m.logger.LogRequest(ctx, prompt, config)
	
	// Execute
	stream, err := m.adapter.StreamGenerate(ctx, prompt, config)
	
	if err != nil {
		m.logger.LogError(ctx, err, map[string]interface{}{
			"method": "StreamGenerate",
			"model":  config.Model,
		})
	}
	
	// Note: Streaming responses are not logged token-by-token
	// This could be enhanced to log summary after streaming completes
	
	return stream, err
}

// Helper functions

// getRequestID extracts request ID from context
func getRequestID(ctx context.Context) string {
	// This is a placeholder - in a real implementation,
	// you would extract the request ID from context
	if reqID, ok := ctx.Value("request_id").(string); ok {
		return reqID
	}
	return ""
}

// sanitizeForLogging removes sensitive data from values before logging
func sanitizeForLogging(data map[string]interface{}) map[string]interface{} {
	sanitized := make(map[string]interface{})
	filter := NewSecretFilter()
	
	for key, value := range data {
		// Check if key indicates sensitive data
		lowerKey := strings.ToLower(key)
		if containsSensitiveKeyword(lowerKey) {
			sanitized[key] = "[FILTERED]"
		} else if str, ok := value.(string); ok {
			// Filter string values
			sanitized[key] = filter.FilterContent(str)
		} else if subMap, ok := value.(map[string]interface{}); ok {
			// Recursively sanitize nested maps
			sanitized[key] = sanitizeForLogging(subMap)
		} else {
			sanitized[key] = value
		}
	}
	
	return sanitized
}