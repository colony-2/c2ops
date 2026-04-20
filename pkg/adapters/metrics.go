package llmadapters

import (
	"context"
	"sync"
	"time"

	f2 "github.com/colony-2/c2j/pkg/file"
)

// MetricsCollector tracks adapter usage
type MetricsCollector interface {
	// RecordGeneration tracks basic generation metrics
	RecordGeneration(provider string, model string, duration time.Duration, tokens int)

	// RecordToolExecution tracks tool execution
	RecordToolExecution(toolName string, success bool, duration time.Duration)

	// RecordFileOperation tracks file operations
	RecordFileOperation(operation string, fileType f2.FileType, size int64)

	// GetMetrics returns current metrics
	GetMetrics() Metrics

	// Reset resets all metrics
	Reset()
}

// Metrics holds collected metrics data
type Metrics struct {
	Generations      GenerationMetrics    `json:"generations"`
	ToolExecutions   ToolExecutionMetrics `json:"tool_executions"`
	FileOperations   FileOperationMetrics `json:"file_operations"`
	CollectionPeriod CollectionPeriod     `json:"collection_period"`
}

// GenerationMetrics tracks LLM generation statistics
type GenerationMetrics struct {
	TotalRequests   int64                       `json:"total_requests"`
	TotalTokens     int64                       `json:"total_tokens"`
	TotalDuration   time.Duration               `json:"total_duration"`
	AverageDuration time.Duration               `json:"average_duration"`
	ByProvider      map[string]*ProviderMetrics `json:"by_provider"`
	ByModel         map[string]*ModelMetrics    `json:"by_model"`
	ErrorCount      int64                       `json:"error_count"`
	SuccessRate     float64                     `json:"success_rate"`
}

// ProviderMetrics tracks metrics per provider
type ProviderMetrics struct {
	Requests        int64         `json:"requests"`
	Tokens          int64         `json:"tokens"`
	TotalDuration   time.Duration `json:"total_duration"`
	AverageDuration time.Duration `json:"average_duration"`
	ErrorCount      int64         `json:"error_count"`
}

// ModelMetrics tracks metrics per model
type ModelMetrics struct {
	Requests        int64         `json:"requests"`
	Tokens          int64         `json:"tokens"`
	TotalDuration   time.Duration `json:"total_duration"`
	AverageDuration time.Duration `json:"average_duration"`
}

// ToolExecutionMetrics tracks tool execution statistics
type ToolExecutionMetrics struct {
	TotalExecutions int64                   `json:"total_executions"`
	SuccessCount    int64                   `json:"success_count"`
	FailureCount    int64                   `json:"failure_count"`
	TotalDuration   time.Duration           `json:"total_duration"`
	AverageDuration time.Duration           `json:"average_duration"`
	ByTool          map[string]*ToolMetrics `json:"by_tool"`
	SuccessRate     float64                 `json:"success_rate"`
}

// ToolMetrics tracks metrics per tool
type ToolMetrics struct {
	Executions      int64         `json:"executions"`
	SuccessCount    int64         `json:"success_count"`
	FailureCount    int64         `json:"failure_count"`
	TotalDuration   time.Duration `json:"total_duration"`
	AverageDuration time.Duration `json:"average_duration"`
	LastExecution   time.Time     `json:"last_execution"`
}

// FileOperationMetrics tracks file operation statistics
type FileOperationMetrics struct {
	TotalOperations int64                            `json:"total_operations"`
	TotalBytes      int64                            `json:"total_bytes"`
	ByOperation     map[string]*FileOperationDetail  `json:"by_operation"`
	ByFileType      map[f2.FileType]*FileTypeMetrics `json:"by_file_type"`
}

// FileOperationDetail tracks details per operation type
type FileOperationDetail struct {
	Count      int64 `json:"count"`
	TotalBytes int64 `json:"total_bytes"`
}

// FileTypeMetrics tracks metrics per file type
type FileTypeMetrics struct {
	Operations  int64 `json:"operations"`
	TotalBytes  int64 `json:"total_bytes"`
	AverageSize int64 `json:"average_size"`
}

// CollectionPeriod tracks the time period for metrics
type CollectionPeriod struct {
	StartTime time.Time     `json:"start_time"`
	EndTime   time.Time     `json:"end_time"`
	Duration  time.Duration `json:"duration"`
}

// DefaultMetricsCollector implements MetricsCollector
type DefaultMetricsCollector struct {
	mu        sync.RWMutex
	metrics   Metrics
	startTime time.Time
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector() MetricsCollector {
	return &DefaultMetricsCollector{
		metrics: Metrics{
			Generations: GenerationMetrics{
				ByProvider: make(map[string]*ProviderMetrics),
				ByModel:    make(map[string]*ModelMetrics),
			},
			ToolExecutions: ToolExecutionMetrics{
				ByTool: make(map[string]*ToolMetrics),
			},
			FileOperations: FileOperationMetrics{
				ByOperation: make(map[string]*FileOperationDetail),
				ByFileType:  make(map[f2.FileType]*FileTypeMetrics),
			},
		},
		startTime: time.Now(),
	}
}

// RecordGeneration tracks basic generation metrics
func (m *DefaultMetricsCollector) RecordGeneration(provider string, model string, duration time.Duration, tokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Update total metrics
	m.metrics.Generations.TotalRequests++
	m.metrics.Generations.TotalTokens += int64(tokens)
	m.metrics.Generations.TotalDuration += duration

	// Update provider metrics
	if m.metrics.Generations.ByProvider[provider] == nil {
		m.metrics.Generations.ByProvider[provider] = &ProviderMetrics{}
	}
	providerMetrics := m.metrics.Generations.ByProvider[provider]
	providerMetrics.Requests++
	providerMetrics.Tokens += int64(tokens)
	providerMetrics.TotalDuration += duration
	providerMetrics.AverageDuration = time.Duration(int64(providerMetrics.TotalDuration) / providerMetrics.Requests)

	// Update model metrics
	if m.metrics.Generations.ByModel[model] == nil {
		m.metrics.Generations.ByModel[model] = &ModelMetrics{}
	}
	modelMetrics := m.metrics.Generations.ByModel[model]
	modelMetrics.Requests++
	modelMetrics.Tokens += int64(tokens)
	modelMetrics.TotalDuration += duration
	modelMetrics.AverageDuration = time.Duration(int64(modelMetrics.TotalDuration) / modelMetrics.Requests)

	// Calculate averages
	if m.metrics.Generations.TotalRequests > 0 {
		m.metrics.Generations.AverageDuration = time.Duration(
			int64(m.metrics.Generations.TotalDuration) / m.metrics.Generations.TotalRequests,
		)
		m.metrics.Generations.SuccessRate = float64(m.metrics.Generations.TotalRequests-m.metrics.Generations.ErrorCount) /
			float64(m.metrics.Generations.TotalRequests)
	}
}

// RecordToolExecution tracks tool execution
func (m *DefaultMetricsCollector) RecordToolExecution(toolName string, success bool, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Update total metrics
	m.metrics.ToolExecutions.TotalExecutions++
	m.metrics.ToolExecutions.TotalDuration += duration

	if success {
		m.metrics.ToolExecutions.SuccessCount++
	} else {
		m.metrics.ToolExecutions.FailureCount++
	}

	// Update tool-specific metrics
	if m.metrics.ToolExecutions.ByTool[toolName] == nil {
		m.metrics.ToolExecutions.ByTool[toolName] = &ToolMetrics{}
	}
	toolMetrics := m.metrics.ToolExecutions.ByTool[toolName]
	toolMetrics.Executions++
	toolMetrics.TotalDuration += duration
	toolMetrics.LastExecution = time.Now()

	if success {
		toolMetrics.SuccessCount++
	} else {
		toolMetrics.FailureCount++
	}

	// Calculate averages
	if toolMetrics.Executions > 0 {
		toolMetrics.AverageDuration = time.Duration(int64(toolMetrics.TotalDuration) / toolMetrics.Executions)
	}

	if m.metrics.ToolExecutions.TotalExecutions > 0 {
		m.metrics.ToolExecutions.AverageDuration = time.Duration(
			int64(m.metrics.ToolExecutions.TotalDuration) / m.metrics.ToolExecutions.TotalExecutions,
		)
		m.metrics.ToolExecutions.SuccessRate = float64(m.metrics.ToolExecutions.SuccessCount) /
			float64(m.metrics.ToolExecutions.TotalExecutions)
	}
}

// RecordFileOperation tracks file operations
func (m *DefaultMetricsCollector) RecordFileOperation(operation string, fileType f2.FileType, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Update total metrics
	m.metrics.FileOperations.TotalOperations++
	m.metrics.FileOperations.TotalBytes += size

	// Update operation-specific metrics
	if m.metrics.FileOperations.ByOperation[operation] == nil {
		m.metrics.FileOperations.ByOperation[operation] = &FileOperationDetail{}
	}
	opDetail := m.metrics.FileOperations.ByOperation[operation]
	opDetail.Count++
	opDetail.TotalBytes += size

	// Update file type metrics
	if m.metrics.FileOperations.ByFileType[fileType] == nil {
		m.metrics.FileOperations.ByFileType[fileType] = &FileTypeMetrics{}
	}
	typeMetrics := m.metrics.FileOperations.ByFileType[fileType]
	typeMetrics.Operations++
	typeMetrics.TotalBytes += size
	if typeMetrics.Operations > 0 {
		typeMetrics.AverageSize = typeMetrics.TotalBytes / typeMetrics.Operations
	}
}

// GetMetrics returns current metrics
func (m *DefaultMetricsCollector) GetMetrics() Metrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Create a copy of metrics
	metricsCopy := m.metrics

	// Update collection period
	now := time.Now()
	metricsCopy.CollectionPeriod = CollectionPeriod{
		StartTime: m.startTime,
		EndTime:   now,
		Duration:  now.Sub(m.startTime),
	}

	return metricsCopy
}

// Reset resets all metrics
func (m *DefaultMetricsCollector) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.metrics = Metrics{
		Generations: GenerationMetrics{
			ByProvider: make(map[string]*ProviderMetrics),
			ByModel:    make(map[string]*ModelMetrics),
		},
		ToolExecutions: ToolExecutionMetrics{
			ByTool: make(map[string]*ToolMetrics),
		},
		FileOperations: FileOperationMetrics{
			ByOperation: make(map[string]*FileOperationDetail),
			ByFileType:  make(map[f2.FileType]*FileTypeMetrics),
		},
	}
	m.startTime = time.Now()
}

// MetricsMiddleware wraps an adapter with metrics collection
type MetricsMiddleware struct {
	adapter   Adapter
	collector MetricsCollector
	provider  string
}

// NewMetricsMiddleware creates a new metrics middleware
func NewMetricsMiddleware(adapter Adapter, collector MetricsCollector, provider string) Adapter {
	return &MetricsMiddleware{
		adapter:   adapter,
		collector: collector,
		provider:  provider,
	}
}

// Generate wraps the Generate method with metrics collection
func (m *MetricsMiddleware) Generate(ctx context.Context, prompt string, config Config) (Response, error) {
	start := time.Now()

	response, err := m.adapter.Generate(ctx, prompt, config)

	duration := time.Since(start)
	tokens := 0
	if err == nil {
		tokens = response.Usage.TotalTokens
	}

	m.collector.RecordGeneration(m.provider, config.Model, duration, tokens)

	return response, err
}

// GenerateWithTools wraps the GenerateWithTools method with metrics collection
func (m *MetricsMiddleware) GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error) {
	start := time.Now()

	response, err := m.adapter.GenerateWithTools(ctx, prompt, tools, config)

	duration := time.Since(start)
	tokens := 0
	if err == nil {
		tokens = response.Usage.TotalTokens
	}

	m.collector.RecordGeneration(m.provider, config.Model, duration, tokens)

	return response, err
}

// StreamGenerate wraps the StreamGenerate method
func (m *MetricsMiddleware) StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error) {
	// For streaming, we don't collect metrics in the same way
	// This would need a more sophisticated approach to track streaming metrics
	return m.adapter.StreamGenerate(ctx, prompt, config)
}
