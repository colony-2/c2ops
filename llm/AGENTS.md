# LLM Adapters Library

## Overview

A unified Go library for interacting with multiple LLM providers (OpenAI, Anthropic, Gemini) through a common interface. Provides standardized abstractions for text generation, file processing, tool execution, and embeddings with support for advanced features like streaming responses and multi-modal capabilities.

## Architecture

### Core Components

- **Adapter Interface**: Base abstraction for LLM providers with `Generate()`, `GenerateWithTools()`, and `StreamGenerate()` methods
- **FileAdapter**: Extension for file handling capabilities with support for text, images, PDFs, and other file types
- **ExecutableToolAdapter**: Extension for automatic tool execution with sandboxing and result tracking
- **UnifiedAdapter**: Combines file and tool capabilities for comprehensive LLM interactions
- **Registry**: Manages multiple adapter instances with name-based lookup and registration
- **Embeddings**: Text embedding functionality with mock and OpenAI implementations

### Provider Implementations

- **OpenAI**: GPT models with native file support and function calling
- **Anthropic**: Claude models with enhanced tool execution and file processing
- **Gemini**: Google's Gemini models with multimodal capabilities
- **Mock**: Testing adapter for development and CI environments

## Key Interfaces

### Base Adapter
```go
type Adapter interface {
    Generate(ctx context.Context, prompt string, config Config) (Response, error)
    GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error)
    StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error)
}
```

### File Processing
```go
type FileAdapter interface {
    Adapter
    GenerateWithFiles(ctx context.Context, prompt string, files []File, config Config) (Response, error)
    GetFileCapabilities() FileCapabilities
    ValidateFile(file File) error
}
```

### Tool Execution
```go
type ExecutableToolAdapter interface {
    Adapter
    GenerateAndExecuteTools(ctx context.Context, prompt string, tools []Tool, config ExecutableToolConfig) (ExecutableToolResponse, error)
    SetToolExecutor(executor ToolExecutor)
}
```

### Registry Management
```go
type Registry interface {
    Register(name string, adapter Adapter) error
    Get(name string) (Adapter, error)
    List() []string
}
```

## Usage Examples

### Basic Text Generation
```go
adapter, err := adapters.NewOpenAIAdapter("")
if err != nil {
    return err
}

config := adapters.Config{
    Model:       "gpt-3.5-turbo",
    Temperature: 0.7,
    MaxTokens:   150,
}

response, err := adapter.Generate(ctx, "Explain Go concurrency", config)
if err != nil {
    return err
}

fmt.Printf("Response: %s\n", response.Content)
```

### File Processing
```go
fileAdapter, err := adapters.NewAnthropicFileAdapter("")
if err != nil {
    return err
}

files := []adapters.File{
    {
        Path:     "document.pdf",
        Content:  fileContent,
        Type:     adapters.FileTypePDF,
        MimeType: "application/pdf",
    },
}

response, err := fileAdapter.GenerateWithFiles(ctx, "Summarize this document", files, config)
```

### Tool Execution
```go
execAdapter, err := adapters.NewAnthropicExecutableToolAdapter("")
if err != nil {
    return err
}

tools := []adapters.Tool{
    {
        Name:        "search_files",
        Description: "Search for files in the project",
        Parameters:  json.RawMessage(`{"type": "object", "properties": {"query": {"type": "string"}}}`),
    },
}

execConfig := adapters.ExecutableToolConfig{
    Config:      config,
    AutoExecute: true,
    MaxToolRounds: 3,
}

response, err := execAdapter.GenerateAndExecuteTools(ctx, "Find Go test files", tools, execConfig)
```

### Registry Usage
```go
registry := adapters.NewRegistry()

// Register multiple providers
openaiAdapter, _ := adapters.NewOpenAIAdapter(openaiKey)
anthropicAdapter, _ := adapters.NewAnthropicAdapter(anthropicKey)

registry.Register("openai", openaiAdapter)
registry.Register("anthropic", anthropicAdapter)

// Use any registered adapter
adapter, err := registry.Get("openai")
if err != nil {
    return err
}

response, err := adapter.Generate(ctx, prompt, config)
```

### Streaming Responses
```go
tokenChan, err := adapter.StreamGenerate(ctx, prompt, config)
if err != nil {
    return err
}

for token := range tokenChan {
    if token.Error != nil {
        log.Printf("Stream error: %v", token.Error)
        break
    }
    fmt.Print(token.Content)
}
```

## Configuration

### Environment Variables
- `OPENAI_API_KEY`: OpenAI API key
- `ANTHROPIC_API_KEY`: Anthropic API key  
- `GEMINI_API_KEY`: Google Gemini API key

### Config Structure
```go
type Config struct {
    Model          string                 `json:"model"`
    Temperature    float64                `json:"temperature,omitempty"`
    MaxTokens      int                    `json:"max_tokens,omitempty"`
    SystemPrompt   string                 `json:"system_prompt,omitempty"`
    ResponseFormat string                 `json:"response_format,omitempty"`
    TopP           float64                `json:"top_p,omitempty"`
    StopSequences  []string               `json:"stop_sequences,omitempty"`
    Metadata       map[string]interface{} `json:"metadata,omitempty"`
}
```

### File Handling Modes
- `FileHandlingNative`: Use provider's native file support
- `FileHandlingTextFallback`: Convert all files to text
- `FileHandlingHybrid`: Native for supported types, fallback for others

### Tool Execution Options
- `AutoExecute`: Automatically execute tool calls
- `MaxToolRounds`: Maximum number of tool execution rounds
- `ToolTimeout`: Timeout for individual tool executions
- `Sandbox`: Enable sandboxed execution environment