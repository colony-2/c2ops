package llm

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// OperationMode defines the mode of operation for the LLM activity
type OperationMode string

const (
	// ModeSimple is the default mode with direct LLM interaction
	ModeSimple OperationMode = "simple"
	// ModePersona enables persona-based interaction with enhanced configuration
	ModePersona OperationMode = "persona"
)

// PersonaConfig defines the configuration for persona mode
type PersonaConfig struct {
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities,omitempty"`
	Goals        []string `json:"goals,omitempty"`
	Constraints  []string `json:"constraints,omitempty"`
	Context      []string `json:"context,omitempty"`
}

// FileContext defines file inclusion configuration
type FileContext struct {
	// Static file paths
	Artifacts []ArtifactPath `json:"artifacts,omitempty"`
	
	// Dynamic artifacts from previous activity outputs
	ArtifactsFromOutput string `json:"artifacts_from_output,omitempty"`
	
	// Glob patterns for file matching
	ArtifactsGlob []GlobPattern `json:"artifacts_glob,omitempty"`
	
	// Directory listing
	ArtifactsDirectory *DirectoryConfig `json:"artifacts_directory,omitempty"`
	
	// Combined resolution with automatic deduplication
	ArtifactsResolution *ResolutionConfig `json:"artifacts_resolution,omitempty"`
	
	// File handling limits
	FileLimits *FileLimits `json:"file_limits,omitempty"`
	
	// File handling configuration
	FileHandling *FileHandlingConfig `json:"file_handling,omitempty"`
}

// ArtifactPath represents a static file path with optional label
type ArtifactPath struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
}

// GlobPattern represents a glob pattern with exclusions
type GlobPattern struct {
	Pattern string   `json:"pattern"`
	Exclude []string `json:"exclude,omitempty"`
}

// DirectoryConfig defines directory listing configuration
type DirectoryConfig struct {
	Path       string   `json:"path"`
	Recursive  bool     `json:"recursive,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
}

// ResolutionConfig defines combined artifact resolution
type ResolutionConfig struct {
	Sources []ResolutionSource `json:"sources"`
}

// ResolutionSource defines a source for artifact resolution
type ResolutionSource struct {
	Type      string   `json:"type"` // "activity_output", "glob", "directory", "static"
	Activity  string   `json:"activity,omitempty"`
	Field     string   `json:"field,omitempty"`
	Patterns  []string `json:"patterns,omitempty"`
	Path      string   `json:"path,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Recursive bool     `json:"recursive,omitempty"`
	Filter    string   `json:"filter,omitempty"`
}

// FileLimits defines file size and count limits
type FileLimits struct {
	MaxFileSize        int64  `json:"max_file_size,omitempty"`
	MaxTotalSize       int64  `json:"max_total_size,omitempty"`
	MaxFileCount       int    `json:"max_file_count,omitempty"`
	TruncateLargeFiles bool   `json:"truncate_large_files,omitempty"`
	TruncationMessage  string `json:"truncation_message,omitempty"`
}

// FileHandlingConfig defines provider-specific file handling
type FileHandlingConfig struct {
	ProviderMode   string `json:"provider_mode,omitempty"`   // "native", "text_fallback", "hybrid"
	ImageHandling  string `json:"image_handling,omitempty"`  // "base64", "url", "upload"
	PDFHandling    string `json:"pdf_handling,omitempty"`    // "native", "text_extraction", "ocr"
	MaxImageSize   int64  `json:"max_image_size,omitempty"`
	OptimizeImages bool   `json:"optimize_images,omitempty"`
}

// ResolvedFile represents a file that has been resolved and read
type ResolvedFile struct {
	Path     string
	Label    string
	Content  []byte
	MimeType string
	Size     int64
	Metadata map[string]interface{}
}

// EnhancedLLMTaskInput extends the basic LLMTaskInput with persona mode support
type EnhancedLLMTaskInput struct {
	LLMTaskInput
	
	// Mode of operation
	Mode OperationMode `json:"mode,omitempty"`
	
	// Persona configuration (only when mode is "persona")
	Persona *PersonaConfig `json:"persona,omitempty"`
	
	// File context configuration
	Context *FileContext `json:"context,omitempty"`
	
	// Input field mapping
	InputField string `json:"input_field,omitempty"`
}

// ValidatePersonaConfig validates the persona configuration
func ValidatePersonaConfig(config *PersonaConfig) error {
	if config == nil {
		return fmt.Errorf("persona configuration is required in persona mode")
	}
	
	if config.Role == "" {
		return fmt.Errorf("role is required in persona configuration")
	}
	
	// Validate arrays are not empty if specified
	if config.Capabilities != nil && len(config.Capabilities) == 0 {
		return fmt.Errorf("capabilities array cannot be empty if specified")
	}
	if config.Goals != nil && len(config.Goals) == 0 {
		return fmt.Errorf("goals array cannot be empty if specified")
	}
	if config.Constraints != nil && len(config.Constraints) == 0 {
		return fmt.Errorf("constraints array cannot be empty if specified")
	}
	if config.Context != nil && len(config.Context) == 0 {
		return fmt.Errorf("context array cannot be empty if specified")
	}
	
	return nil
}

// BuildPersonaSystemPrompt constructs the system prompt from persona configuration
func BuildPersonaSystemPrompt(persona *PersonaConfig) string {
	var parts []string
	
	// Add role
	parts = append(parts, fmt.Sprintf("You are a %s.", persona.Role))
	
	// Add capabilities
	if len(persona.Capabilities) > 0 {
		parts = append(parts, "\nYour capabilities include:")
		for _, cap := range persona.Capabilities {
			parts = append(parts, fmt.Sprintf("- %s", cap))
		}
	}
	
	// Add goals
	if len(persona.Goals) > 0 {
		parts = append(parts, "\nYour goals are to:")
		for _, goal := range persona.Goals {
			parts = append(parts, fmt.Sprintf("- %s", goal))
		}
	}
	
	// Add constraints
	if len(persona.Constraints) > 0 {
		parts = append(parts, "\nYou must adhere to these constraints:")
		for _, constraint := range persona.Constraints {
			parts = append(parts, fmt.Sprintf("- %s", constraint))
		}
	}
	
	// Add additional context
	if len(persona.Context) > 0 {
		parts = append(parts, "\nAdditional context:")
		for _, ctx := range persona.Context {
			parts = append(parts, fmt.Sprintf("- %s", ctx))
		}
	}
	
	return strings.Join(parts, "\n")
}

// DeduplicatePaths removes duplicate file paths
func DeduplicatePaths(paths []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(paths))
	
	for _, path := range paths {
		// Normalize path
		normalized, err := filepath.Abs(path)
		if err != nil {
			normalized = path
		}
		
		if !seen[normalized] {
			seen[normalized] = true
			result = append(result, normalized)
		}
	}
	
	return result
}

// DetectMimeType detects the MIME type of a file based on its extension and content
func DetectMimeType(path string, content []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	
	// Common text file extensions
	textExtensions := map[string]string{
		".txt":  "text/plain",
		".md":   "text/markdown",
		".go":   "text/x-go",
		".py":   "text/x-python",
		".js":   "text/javascript",
		".ts":   "text/typescript",
		".jsx":  "text/javascript",
		".tsx":  "text/typescript",
		".java": "text/x-java",
		".c":    "text/x-c",
		".cpp":  "text/x-c++",
		".h":    "text/x-c",
		".hpp":  "text/x-c++",
		".cs":   "text/x-csharp",
		".rb":   "text/x-ruby",
		".php":  "text/x-php",
		".rs":   "text/x-rust",
		".kt":   "text/x-kotlin",
		".swift": "text/x-swift",
		".json": "application/json",
		".xml":  "text/xml",
		".yaml": "text/yaml",
		".yml":  "text/yaml",
		".toml": "text/toml",
		".ini":  "text/plain",
		".conf": "text/plain",
		".cfg":  "text/plain",
		".sh":   "text/x-shellscript",
		".bash": "text/x-shellscript",
		".zsh":  "text/x-shellscript",
		".fish": "text/x-shellscript",
		".ps1":  "text/x-powershell",
		".html": "text/html",
		".css":  "text/css",
		".scss": "text/x-scss",
		".sass": "text/x-sass",
		".less": "text/x-less",
	}
	
	// Image file extensions
	imageExtensions := map[string]string{
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".bmp":  "image/bmp",
		".svg":  "image/svg+xml",
		".webp": "image/webp",
		".ico":  "image/x-icon",
	}
	
	// Document file extensions
	documentExtensions := map[string]string{
		".pdf":  "application/pdf",
		".doc":  "application/msword",
		".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		".xls":  "application/vnd.ms-excel",
		".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		".ppt":  "application/vnd.ms-powerpoint",
		".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	}
	
	// Check text extensions
	if mimeType, ok := textExtensions[ext]; ok {
		return mimeType
	}
	
	// Check image extensions
	if mimeType, ok := imageExtensions[ext]; ok {
		return mimeType
	}
	
	// Check document extensions
	if mimeType, ok := documentExtensions[ext]; ok {
		return mimeType
	}
	
	// Default to text/plain for unknown extensions
	return "text/plain"
}

// TruncateContent truncates content to the specified size
func TruncateContent(content []byte, maxSize int64, message string) []byte {
	if int64(len(content)) <= maxSize {
		return content
	}
	
	if message == "" {
		message = "... [truncated - file exceeds size limit] ..."
	}
	
	// Calculate how much content to keep
	messageBytes := []byte(message)
	keepSize := int(maxSize) - len(messageBytes)
	if keepSize < 0 {
		keepSize = 0
	}
	
	// Keep the beginning of the file and add truncation message
	truncated := make([]byte, 0, int(maxSize))
	if keepSize > 0 && keepSize <= len(content) {
		truncated = append(truncated, content[:keepSize]...)
	}
	truncated = append(truncated, messageBytes...)
	
	return truncated
}

// ApplyTotalLimits applies total size and count limits to resolved files
func ApplyTotalLimits(files []ResolvedFile, limits *FileLimits) []ResolvedFile {
	if limits == nil {
		return files
	}
	
	result := make([]ResolvedFile, 0, len(files))
	var totalSize int64
	
	for _, file := range files {
		// Check file count limit
		if limits.MaxFileCount > 0 && len(result) >= limits.MaxFileCount {
			break
		}
		
		// Check if adding this file would exceed total size limit
		if limits.MaxTotalSize > 0 && totalSize+file.Size > limits.MaxTotalSize {
			// Don't add any more files if the next one would exceed the limit
			break
		}
		
		result = append(result, file)
		totalSize += file.Size
	}
	
	return result
}

// ParseEnhancedInput converts a JSON configuration to EnhancedLLMTaskInput
func ParseEnhancedInput(configJSON json.RawMessage) (*EnhancedLLMTaskInput, error) {
	var input EnhancedLLMTaskInput
	
	if err := json.Unmarshal(configJSON, &input); err != nil {
		return nil, fmt.Errorf("failed to parse enhanced input: %w", err)
	}
	
	// Set default mode if not specified
	if input.Mode == "" {
		input.Mode = ModeSimple
	}
	
	// Validate mode
	if input.Mode != ModeSimple && input.Mode != ModePersona {
		return nil, fmt.Errorf("invalid mode: %s (must be 'simple' or 'persona')", input.Mode)
	}
	
	// Validate persona configuration if in persona mode
	if input.Mode == ModePersona {
		if err := ValidatePersonaConfig(input.Persona); err != nil {
			return nil, fmt.Errorf("persona validation failed: %w", err)
		}
	}
	
	// Set default input field if not specified
	if input.InputField == "" {
		input.InputField = "prompt"
	}
	
	return &input, nil
}