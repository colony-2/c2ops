package llm

import "encoding/json"

// GetBuiltinTools returns the standard built-in tools for file operations
func GetBuiltinTools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "write_file",
			Description: "Write or update a file with the specified content",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "File path relative to working directory"
					},
					"content": {
						"type": "string",
						"description": "Complete file content to write"
					},
					"create_dirs": {
						"type": "boolean",
						"description": "Create parent directories if they don't exist",
						"default": true
					}
				},
				"required": ["path", "content"]
			}`),
		},
		{
			Name:        "read_file",
			Description: "Read the content of a file",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "File path to read relative to working directory"
					}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        "delete_file",
			Description: "Delete a file",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "File path to delete relative to working directory"
					}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        "list_files",
			Description: "List files and directories in a given path",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Directory path relative to working directory",
						"default": "."
					},
					"pattern": {
						"type": "string",
						"description": "Optional glob pattern to filter files (e.g., '*.go', '*.txt')"
					},
					"recursive": {
						"type": "boolean",
						"description": "List files recursively in subdirectories",
						"default": false
					}
				}
			}`),
		},
		{
			Name:        "create_directory",
			Description: "Create a directory (including parent directories if needed)",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Directory path to create relative to working directory"
					}
				},
				"required": ["path"]
			}`),
		},
	}
}

// GetCodeExecutionTools returns tools for code execution (if enabled)
func GetCodeExecutionTools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "execute_bash",
			Description: "Execute a bash command in the working directory",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {
						"type": "string",
						"description": "Bash command to execute"
					},
					"timeout": {
						"type": "integer",
						"description": "Timeout in seconds",
						"default": 30
					}
				},
				"required": ["command"]
			}`),
		},
		{
			Name:        "execute_python",
			Description: "Execute Python code",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"code": {
						"type": "string",
						"description": "Python code to execute"
					},
					"timeout": {
						"type": "integer",
						"description": "Timeout in seconds",
						"default": 30
					}
				},
				"required": ["code"]
			}`),
		},
	}
}

// GetAnalysisTools returns tools for code analysis
func GetAnalysisTools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "analyze_code",
			Description: "Analyze code for issues, patterns, or improvements",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "File path to analyze"
					},
					"analysis_type": {
						"type": "string",
						"enum": ["lint", "security", "performance", "style"],
						"description": "Type of analysis to perform"
					}
				},
				"required": ["path", "analysis_type"]
			}`),
		},
		{
			Name:        "find_pattern",
			Description: "Search for patterns in files",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {
						"type": "string",
						"description": "Pattern to search for (regex supported)"
					},
					"path": {
						"type": "string",
						"description": "Directory or file path to search in",
						"default": "."
					},
					"file_pattern": {
						"type": "string",
						"description": "File pattern to match (e.g., '*.go')"
					}
				},
				"required": ["pattern"]
			}`),
		},
	}
}

// GetToolByName returns a tool definition by name
func GetToolByName(name string) *ToolDefinition {
	allTools := append(GetBuiltinTools(), GetCodeExecutionTools()...)
	allTools = append(allTools, GetAnalysisTools()...)
	
	for _, tool := range allTools {
		if tool.Name == name {
			return &tool
		}
	}
	
	return nil
}

// MergeTools merges multiple tool lists, removing duplicates
func MergeTools(toolLists ...[]ToolDefinition) []ToolDefinition {
	seen := make(map[string]bool)
	merged := []ToolDefinition{}
	
	for _, tools := range toolLists {
		for _, tool := range tools {
			if !seen[tool.Name] {
				merged = append(merged, tool)
				seen[tool.Name] = true
			}
		}
	}
	
	return merged
}