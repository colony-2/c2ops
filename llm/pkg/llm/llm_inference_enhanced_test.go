package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	f2 "github.com/colony-2/c2j/pkg/file"
	recipeops "github.com/colony-2/c2j/pkg/ops"
	llmadapters "github.com/colony-2/colony2/server/llm/adapters"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnhancedLLMInferenceActivity_BackwardCompatibility(t *testing.T) {
	// Setup
	activity := NewEnhancedLLMInferenceActivity()

	// Create mock registry
	mockRegistry := llmadapters.NewRegistry()
	mockAdapter := llmadapters.NewMockAdapter()
	mockAdapter.SetResponse(llmadapters.Response{
		Content:      "Hello, World!",
		Model:        "gpt-3.5-turbo",
		FinishReason: "stop",
		Usage: llmadapters.Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	})
	mockRegistry.Register("openai", mockAdapter)

	// Replace registry in activity
	activity.registry = mockRegistry

	t.Run("OldFormatWithModelName", func(t *testing.T) {
		// Test old format with modelName and adapterName fields
		input := LLMInferenceInput{
			Prompt:   "Hello",
			Model:    "gpt-3.5-turbo",
			Provider: "openai",
		}

		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)

		// Check output
		assert.NotEmpty(t, output.Response)
		assert.Equal(t, "gpt-3.5-turbo", output.Model)
		assert.Equal(t, "stop", output.FinishReason)

		// Check backward compatibility telemetry field
		assert.Equal(t, 10, output.Telemetry.PromptTokens)
		assert.Equal(t, 5, output.Telemetry.CompletionTokens)
		assert.Equal(t, 15, output.Telemetry.TotalTokens)
	})

	t.Run("NewFormatWithProviderModel", func(t *testing.T) {
		// Test new format with provider and model fields
		input := LLMInferenceInput{
			Prompt:   "Hello",
			Model:    "gpt-3.5-turbo",
			Provider: "openai",
		}

		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)

		// Check output
		assert.NotEmpty(t, output.Response)
		assert.Equal(t, "gpt-3.5-turbo", output.Model)

		// Check new usage field
		assert.Equal(t, 10, output.Usage.PromptTokens)
		assert.Equal(t, 5, output.Usage.CompletionTokens)
		assert.Equal(t, 15, output.Usage.TotalTokens)
	})

	t.Run("DefaultsFromConfig", func(t *testing.T) {
		input := LLMInferenceInput{
			Prompt: "Hello",
		}

		// Provide provider and model directly in input since config is removed
		input.Provider = "openai"
		input.Model = "gpt-3.5-turbo"
		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)

		assert.NotEmpty(t, output.Response)
		assert.Equal(t, "gpt-3.5-turbo", output.Model)
	})
}

func TestEnhancedLLMInferenceActivity_ConstructsAdapterLocally(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "") // force constructor to rely on provided key

	activity := NewEnhancedLLMInferenceActivity()
	input := LLMInferenceInput{
		Prompt:   "Hello",
		Model:    "gpt-4.1",
		Provider: "openai",
	}

	_, err := activity.Execute(nil, context.Background(), input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key is missing")
}

func TestLLMInferenceInputValidation_EmptyToolsAndNoTools(t *testing.T) {
	activity := NewEnhancedLLMInferenceActivity()
	makeInput := func() map[string]interface{} {
		return map[string]interface{}{
			"default_provider": "openai",
			"default_model":    "gpt-4.1",
			"temperature":      0,
			"system_prompt":    `Return {"ok": true}`,
			"prompt":           "hi",
		}
	}

	t.Run("no tools accepted", func(t *testing.T) {
		raw := makeInput()

		var decoded LLMInferenceInput
		err := recipeops.DecodeWithJsonTags(raw, &decoded)
		require.NoError(t, err)

		v := validator.New()
		require.NoError(t, v.Struct(decoded))
	})

	t.Run("empty tool parameters accepted", func(t *testing.T) {
		raw := makeInput()
		raw["execute_tools"] = true
		raw["tools"] = []interface{}{
			map[string]interface{}{
				"name":        "respond",
				"description": "Return JSON verdict",
				"parameters":  map[string]interface{}{},
			},
		}

		var decoded LLMInferenceInput
		err := recipeops.DecodeWithJsonTags(raw, &decoded)
		require.NoError(t, err)

		v := validator.New()
		require.NoError(t, v.Struct(decoded))
		assert.Equal(t, []byte("{}"), []byte(decoded.Tools[0].Parameters))
	})

	t.Run("execute_tools_without_tools_rejected", func(t *testing.T) {
		raw := makeInput()
		raw["execute_tools"] = true
		raw["enable_tool_execution"] = true

		var decoded LLMInferenceInput
		err := recipeops.DecodeWithJsonTags(raw, &decoded)
		require.NoError(t, err)

		v := validator.New()
		err = v.Struct(decoded)
		require.NoError(t, err)

		err = activity.validateInput(decoded)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tools are required")
	})

	t.Run("response_schema_accepts_object", func(t *testing.T) {
		raw := makeInput()
		raw["response_schema"] = []interface{}{
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"foo": map[string]interface{}{
						"type": "string",
					},
				},
				"required": []interface{}{"foo"},
			},
		}

		var decoded LLMInferenceInput
		err := recipeops.DecodeWithJsonTags(raw, &decoded)
		require.NoError(t, err)

		require.True(t, json.Valid(decoded.ResponseSchema.Raw()))

		var schema []map[string]interface{}
		require.NoError(t, json.Unmarshal(decoded.ResponseSchema.Raw(), &schema))
		require.Len(t, schema, 1)
		assert.Equal(t, "object", schema[0]["type"])
	})
}

func TestEnhancedLLMInferenceActivity_WithFiles(t *testing.T) {
	// Setup
	activity := NewEnhancedLLMInferenceActivity()

	// Create mock registry with file support
	mockRegistry := llmadapters.NewRegistry()
	mockAdapter := llmadapters.NewMockAdapter()
	mockAdapter.FileCapabilities = llmadapters.FileCapabilities{
		SupportedTypes: []f2.FileType{
			f2.FileTypeText,
			f2.FileTypeCode,
		},
		MaxFileSize:    1024 * 1024,
		MaxFileCount:   10,
		TotalSizeLimit: 10 * 1024 * 1024,
	}
	mockAdapter.SetResponse(llmadapters.Response{
		Content:      "Analyzed the files",
		Model:        "gpt-4",
		FinishReason: "stop",
		Usage: llmadapters.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	})
	mockRegistry.Register("openai", mockAdapter)

	activity.registry = mockRegistry

	t.Run("FilesWithNativeHandling", func(t *testing.T) {
		input := LLMInferenceInput{
			Prompt:   "Analyze these files",
			Provider: "openai",
			Model:    "gpt-4",
			Files: []f2.File{
				{
					Path:    "main.go",
					Content: []byte("package main\n\nfunc main() {}"),
					Type:    f2.FileTypeCode,
				},
				{
					Path:    "README.md",
					Content: []byte("# Test Project"),
					Type:    f2.FileTypeMarkdown,
				},
			},
			FileHandling: "native",
		}

		input.MaxFileContextSize = 10 * 1024 * 1024
		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)

		assert.NotEmpty(t, output.Response)
		assert.Equal(t, "gpt-4", output.Model)
	})
}

func TestEnhancedLLMInferenceActivity_WithTools(t *testing.T) {
	// Create temp directory for file operations
	tmpDir := t.TempDir()

	// Setup
	activity := NewEnhancedLLMInferenceActivity()

	// Create mock registry
	mockRegistry := llmadapters.NewRegistry()
	mockAdapter := llmadapters.NewMockAdapter()

	// Set up mock to return tool calls first, then final response
	mockAdapter.Responses = []llmadapters.Response{
		{
			Content: "I'll create the file for you",
			ToolCalls: []llmadapters.ToolCall{
				{
					ID:   "call_1",
					Name: "write_file",
					Arguments: json.RawMessage(`{
						"path": "hello.txt",
						"content": "Hello, World!"
					}`),
				},
			},
			Model:        "gpt-4",
			FinishReason: "tool_calls",
			Usage: llmadapters.Usage{
				PromptTokens:     50,
				CompletionTokens: 25,
				TotalTokens:      75,
			},
		},
		{
			Content:      "File created successfully",
			Model:        "gpt-4",
			FinishReason: "stop",
			Usage: llmadapters.Usage{
				PromptTokens:     20,
				CompletionTokens: 10,
				TotalTokens:      30,
			},
		},
	}
	mockRegistry.Register("openai", mockAdapter)

	activity.registry = mockRegistry

	t.Run("ExecuteFileWriteTool", func(t *testing.T) {
		input := LLMInferenceInput{
			Prompt:         "Create a hello.txt file with 'Hello, World!' content",
			Provider:       "openai",
			Model:          "gpt-4",
			Tools:          GetBuiltinTools()[:1], // Just write_file tool
			ExecuteTools:   true,
			ToolWorkingDir: tmpDir,
			MaxToolRounds:  1, // Limit to 1 round
		}

		input.EnableToolExecution = true
		input.EnableSandbox = true
		input.AllowedPaths = []string{tmpDir}
		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)

		// Check response
		assert.NotEmpty(t, output.Response)
		assert.Equal(t, "gpt-4", output.Model)

		// Check tool results
		assert.Len(t, output.ToolResults, 1)
		assert.Equal(t, "write_file", output.ToolResults[0].ToolName)
		assert.True(t, output.ToolResults[0].Success)

		// Verify file was created
		filePath := filepath.Join(tmpDir, "hello.txt")
		content, err := os.ReadFile(filePath)
		require.NoError(t, err)
		assert.Equal(t, "Hello, World!", string(content))
	})

	t.Run("ListFilesTool", func(t *testing.T) {
		// Clean up tmpDir and create fresh test environment
		tmpDir2 := t.TempDir()

		// Create some test files
		os.WriteFile(filepath.Join(tmpDir2, "test1.txt"), []byte("test1"), 0644)
		os.WriteFile(filepath.Join(tmpDir2, "test2.txt"), []byte("test2"), 0644)
		os.Mkdir(filepath.Join(tmpDir2, "subdir"), 0755)
		os.WriteFile(filepath.Join(tmpDir2, "subdir", "test3.txt"), []byte("test3"), 0644)

		// Create a new mock adapter for this test
		mockAdapter2 := llmadapters.NewMockAdapter()
		mockAdapter2.Responses = []llmadapters.Response{
			{
				Content: "I'll list the files",
				ToolCalls: []llmadapters.ToolCall{
					{
						ID:        "call_list",
						Name:      "list_files",
						Arguments: json.RawMessage(`{"path": ".", "recursive": true}`),
					},
				},
				Model:        "gpt-4",
				FinishReason: "tool_calls",
			},
			{
				Content:      "Files listed successfully",
				Model:        "gpt-4",
				FinishReason: "stop",
			},
		}

		// Create new registry for this test
		mockRegistry2 := llmadapters.NewRegistry()
		mockRegistry2.Register("openai", mockAdapter2)

		// Create new activity with fresh registry
		activity2 := NewEnhancedLLMInferenceActivity()
		activity2.registry = mockRegistry2

		input := LLMInferenceInput{
			Prompt:         "List all files in the directory",
			Provider:       "openai",
			Model:          "gpt-4",
			Tools:          []ToolDefinition{GetBuiltinTools()[3]}, // list_files tool
			ExecuteTools:   true,
			ToolWorkingDir: tmpDir2,
			MaxToolRounds:  1, // Limit to 1 round
		}

		// Move config into input
		input.EnableToolExecution = true
		input.EnableSandbox = true
		input.AllowedPaths = []string{tmpDir2}

		output, err := activity2.Execute(nil, context.Background(), input)
		require.NoError(t, err)

		// Check tool results - should have exactly 1
		require.Len(t, output.ToolResults, 1)
		assert.Equal(t, "list_files", output.ToolResults[0].ToolName)
		assert.True(t, output.ToolResults[0].Success)

		// Check that files were found
		if output.ToolResults[0].Result != nil {
			result := output.ToolResults[0].Result.(map[string]interface{})
			if files, ok := result["files"].([]map[string]interface{}); ok {
				assert.GreaterOrEqual(t, len(files), 3) // At least our test files
			}
		}
	})
}

func TestSecuritySandbox(t *testing.T) {
	t.Run("BlockRestrictedPaths", func(t *testing.T) {
		sandbox := NewSecuritySandbox(SandboxConfig{
			RestrictedPaths: []string{"/etc", "/usr"},
		})

		// These should be blocked
		assert.Error(t, sandbox.ValidatePath("/etc/passwd"))
		assert.Error(t, sandbox.ValidatePath("/usr/bin/ls"))
		assert.Error(t, sandbox.ValidatePath("../../../etc/passwd"))
	})

	t.Run("AllowOnlySpecificPaths", func(t *testing.T) {
		tmpDir := t.TempDir()
		sandbox := NewSecuritySandbox(SandboxConfig{
			AllowedPaths: []string{tmpDir},
		})

		// These should be allowed
		assert.NoError(t, sandbox.ValidatePath(filepath.Join(tmpDir, "file.txt")))
		assert.NoError(t, sandbox.ValidatePath(filepath.Join(tmpDir, "subdir", "file.txt")))

		// These should be blocked
		assert.Error(t, sandbox.ValidatePath("/tmp/other"))
		assert.Error(t, sandbox.ValidatePath("/home/user"))
	})

	t.Run("BlockDangerousPatterns", func(t *testing.T) {
		sandbox := NewSecuritySandbox(SandboxConfig{})

		// Block parent directory traversal
		assert.Error(t, sandbox.ValidatePath("../../sensitive"))
		assert.Error(t, sandbox.ValidatePath("./../../etc/passwd"))

		// Block sensitive files
		assert.Error(t, sandbox.ValidatePath("~/.ssh/id_rsa"))
		assert.Error(t, sandbox.ValidatePath(".aws/credentials"))
	})
}

func TestToolExecutor(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("WriteFile", func(t *testing.T) {
		executor := NewFileToolExecutor(tmpDir, nil)

		result, err := executor.Execute(context.Background(), ToolExecutionRequest{
			Name: "write_file",
			Arguments: map[string]interface{}{
				"path":    "test.txt",
				"content": "Test content",
			},
		})

		require.NoError(t, err)
		assert.NotNil(t, result)

		// Verify file was created
		content, err := os.ReadFile(filepath.Join(tmpDir, "test.txt"))
		require.NoError(t, err)
		assert.Equal(t, "Test content", string(content))
	})

	t.Run("ReadFile", func(t *testing.T) {
		// Create a test file
		testFile := filepath.Join(tmpDir, "read_test.txt")
		os.WriteFile(testFile, []byte("Read test content"), 0644)

		executor := NewFileToolExecutor(tmpDir, nil)

		result, err := executor.Execute(context.Background(), ToolExecutionRequest{
			Name: "read_file",
			Arguments: map[string]interface{}{
				"path": "read_test.txt",
			},
		})

		require.NoError(t, err)
		resultMap := result.(map[string]interface{})
		assert.Equal(t, "Read test content", resultMap["content"])
	})

	t.Run("CreateDirectory", func(t *testing.T) {
		executor := NewFileToolExecutor(tmpDir, nil)

		result, err := executor.Execute(context.Background(), ToolExecutionRequest{
			Name: "create_directory",
			Arguments: map[string]interface{}{
				"path": "new_dir/sub_dir",
			},
		})

		require.NoError(t, err)
		assert.NotNil(t, result)

		// Verify directory was created
		info, err := os.Stat(filepath.Join(tmpDir, "new_dir", "sub_dir"))
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})
}

func TestBuiltinTools(t *testing.T) {
	t.Run("GetBuiltinTools", func(t *testing.T) {
		tools := GetBuiltinTools()
		assert.Len(t, tools, 5)

		// Check tool names
		expectedNames := []string{
			"write_file",
			"read_file",
			"delete_file",
			"list_files",
			"create_directory",
		}

		for i, tool := range tools {
			assert.Equal(t, expectedNames[i], tool.Name)
			assert.NotEmpty(t, tool.Description)
			assert.NotEmpty(t, tool.Parameters)
		}
	})

	t.Run("GetToolByName", func(t *testing.T) {
		tool := GetToolByName("write_file")
		assert.NotNil(t, tool)
		assert.Equal(t, "write_file", tool.Name)

		tool = GetToolByName("nonexistent")
		assert.Nil(t, tool)
	})

	t.Run("MergeTools", func(t *testing.T) {
		tools1 := []ToolDefinition{
			{Name: "tool1", Description: "Tool 1"},
			{Name: "tool2", Description: "Tool 2"},
		}

		tools2 := []ToolDefinition{
			{Name: "tool2", Description: "Tool 2 duplicate"},
			{Name: "tool3", Description: "Tool 3"},
		}

		merged := MergeTools(tools1, tools2)
		assert.Len(t, merged, 3) // tool2 should not be duplicated

		// Check that we have tool1, tool2 (first version), and tool3
		names := make(map[string]bool)
		for _, tool := range merged {
			names[tool.Name] = true
		}
		assert.True(t, names["tool1"])
		assert.True(t, names["tool2"])
		assert.True(t, names["tool3"])
	})
}

func TestStructuredResponseNormalizationAndValidation(t *testing.T) {
	activity := NewEnhancedLLMInferenceActivity()

	mockRegistry := llmadapters.NewRegistry()
	mockAdapter := llmadapters.NewMockAdapter()
	mockAdapter.SetResponse(llmadapters.Response{
		Content:      `{"cell_is_appropriate":false}`,
		Model:        "gpt-4.1",
		FinishReason: "stop",
	})
	mockRegistry.Register("openai", mockAdapter)
	activity.registry = mockRegistry

	input := LLMInferenceInput{
		Prompt:   "classify",
		Model:    "gpt-4.1",
		Provider: "openai",
		ResponseSchema: JSONRawMessage(`[
			{
				"type": "object",
				"properties": {
					"cell_is_appropriate": { "type": "boolean" }
				},
				"required": ["cell_is_appropriate"]
			}
		]`),
	}

	output, err := activity.Execute(nil, context.Background(), input)
	require.NoError(t, err)

	assert.Equal(t, `{"cell_is_appropriate":false}`, output.Response)
}

func TestStructuredResponseValidationFailureIncludesType(t *testing.T) {
	activity := NewEnhancedLLMInferenceActivity()

	mockRegistry := llmadapters.NewRegistry()
	mockAdapter := llmadapters.NewMockAdapter()
	mockAdapter.SetResponse(llmadapters.Response{
		Content:      `[]`,
		Model:        "gpt-4.1",
		FinishReason: "stop",
	})
	mockRegistry.Register("openai", mockAdapter)
	activity.registry = mockRegistry

	input := LLMInferenceInput{
		Prompt:   "classify",
		Model:    "gpt-4.1",
		Provider: "openai",
		ResponseSchema: JSONRawMessage(`{
			"type": "object",
			"properties": {
				"cell_is_appropriate": { "type": "boolean" }
			},
			"required": ["cell_is_appropriate"]
		}`),
	}

	_, err := activity.Execute(nil, context.Background(), input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response does not match response_schema")
	assert.Contains(t, err.Error(), "expected object")
	assert.Contains(t, err.Error(), "got array")
}
