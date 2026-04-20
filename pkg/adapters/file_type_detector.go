package llmadapters

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"

	f2 "github.com/colony-2/c2j/pkg/file"
)

// FileTypeDetector provides enhanced file type detection
type FileTypeDetector interface {
	// DetectType determines the FileType from content and metadata
	DetectType(content []byte, path string) f2.FileType

	// DetectMimeType determines the MIME type
	DetectMimeType(content []byte, path string) string

	// IsTextFile checks if content is text-based
	IsTextFile(content []byte) bool

	// GetLanguage detects programming language for source files
	GetLanguage(content []byte, path string) string
}

// DefaultFileTypeDetector implements FileTypeDetector
type DefaultFileTypeDetector struct {
	extensionMap map[string]f2.FileType
	languageMap  map[string]string
}

// NewFileTypeDetector creates a new file type detector
func NewFileTypeDetector() FileTypeDetector {
	return &DefaultFileTypeDetector{
		extensionMap: initExtensionMap(),
		languageMap:  initLanguageMap(),
	}
}

// DetectType determines the FileType from content and metadata
func (d *DefaultFileTypeDetector) DetectType(content []byte, path string) f2.FileType {
	ext := strings.ToLower(filepath.Ext(path))

	// Check extension-based detection first
	if fileType, ok := d.extensionMap[ext]; ok {
		return fileType
	}

	// Check if it's a text file
	if !d.IsTextFile(content) {
		return f2.FileTypeBinary
	}

	// Content-based detection for text files
	contentStr := string(content[:min(1000, len(content))])

	// Check for code patterns
	if d.looksLikeCode(contentStr, path) {
		return f2.FileTypeCode
	}

	// Check for configuration patterns
	if d.looksLikeConfig(contentStr, path) {
		return f2.FileTypeConfig
	}

	// Check for data format patterns
	if d.looksLikeData(contentStr) {
		return f2.FileTypeData
	}

	// Check for markdown
	if d.looksLikeMarkdown(contentStr) {
		return f2.FileTypeMarkdown
	}

	// Default to text for text-based files
	return f2.FileTypeText
}

// DetectMimeType determines the MIME type
func (d *DefaultFileTypeDetector) DetectMimeType(content []byte, path string) string {
	ext := strings.ToLower(filepath.Ext(path))

	// Extension-based MIME type detection
	mimeType := getMimeTypeFromExtension(ext)
	if mimeType != "" {
		return mimeType
	}

	// Content-based detection for common formats
	if len(content) > 0 {
		// Check for JSON
		if json.Valid(content) {
			return "application/json"
		}

		// Check for XML
		if bytes.HasPrefix(bytes.TrimSpace(content), []byte("<?xml")) {
			return "application/xml"
		}

		// Check for HTML
		if d.looksLikeHTML(string(content[:min(500, len(content))])) {
			return "text/html"
		}
	}

	// Check if it's text
	if d.IsTextFile(content) {
		return "text/plain"
	}

	return "application/octet-stream"
}

// IsTextFile checks if content is text-based
func (d *DefaultFileTypeDetector) IsTextFile(content []byte) bool {
	if len(content) == 0 {
		return true
	}

	// Check first 8192 bytes for binary content
	checkLen := min(8192, len(content))
	for i := 0; i < checkLen; i++ {
		b := content[i]
		// Allow common text characters and control characters
		if b < 0x20 && b != 0x09 && b != 0x0A && b != 0x0D {
			// Found non-text byte
			if b == 0x00 {
				return false // Null byte is strong indicator of binary
			}
		}
		if b > 0x7E && b < 0x80 {
			// Non-ASCII, non-UTF8 continuation byte
			return false
		}
	}

	// Check for UTF-8 validity
	return isValidUTF8(content[:checkLen])
}

// GetLanguage detects programming language for source files
func (d *DefaultFileTypeDetector) GetLanguage(content []byte, path string) string {
	ext := strings.ToLower(filepath.Ext(path))

	// Extension-based language detection
	if lang, ok := d.languageMap[ext]; ok {
		return lang
	}

	// Shebang-based detection
	if len(content) > 2 && content[0] == '#' && content[1] == '!' {
		firstLine := string(bytes.Split(content, []byte("\n"))[0])
		if strings.Contains(firstLine, "python") {
			return "python"
		}
		if strings.Contains(firstLine, "node") {
			return "javascript"
		}
		if strings.Contains(firstLine, "ruby") {
			return "ruby"
		}
		if strings.Contains(firstLine, "bash") || strings.Contains(firstLine, "/sh") {
			return "shell"
		}
		if strings.Contains(firstLine, "perl") {
			return "perl"
		}
	}

	// Content-based detection
	contentStr := string(content[:min(1000, len(content))])

	// Python
	if strings.Contains(contentStr, "import ") || strings.Contains(contentStr, "from ") ||
		strings.Contains(contentStr, "def ") || strings.Contains(contentStr, "class ") {
		if strings.Contains(contentStr, "print(") || strings.Contains(contentStr, "__init__") {
			return "python"
		}
	}

	// JavaScript/TypeScript
	if strings.Contains(contentStr, "function ") || strings.Contains(contentStr, "const ") ||
		strings.Contains(contentStr, "let ") || strings.Contains(contentStr, "var ") {
		if strings.Contains(contentStr, "=>") || strings.Contains(contentStr, "console.") {
			if strings.Contains(contentStr, ": string") || strings.Contains(contentStr, ": number") {
				return "typescript"
			}
			return "javascript"
		}
	}

	// Go
	if strings.Contains(contentStr, "package ") && strings.Contains(contentStr, "func ") {
		return "go"
	}

	// Java
	if strings.Contains(contentStr, "public class ") || strings.Contains(contentStr, "private ") {
		if strings.Contains(contentStr, "import java.") {
			return "java"
		}
	}

	// C/C++
	if strings.Contains(contentStr, "#include ") {
		if strings.Contains(contentStr, "iostream") || strings.Contains(contentStr, "namespace") {
			return "cpp"
		}
		return "c"
	}

	// Rust
	if strings.Contains(contentStr, "fn ") && (strings.Contains(contentStr, "let ") || strings.Contains(contentStr, "mut ")) {
		return "rust"
	}

	return ""
}

// Helper methods

func (d *DefaultFileTypeDetector) looksLikeCode(content, path string) bool {
	// Check file extension
	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := d.languageMap[ext]; ok {
		return true
	}

	// Check for common code patterns
	codePatterns := []string{
		"function ", "def ", "class ", "import ", "package ",
		"const ", "let ", "var ", "public ", "private ",
		"#include", "namespace", "func ", "fn ", "impl ",
	}

	for _, pattern := range codePatterns {
		if strings.Contains(content, pattern) {
			return true
		}
	}

	return false
}

func (d *DefaultFileTypeDetector) looksLikeConfig(content, path string) bool {
	// Check filename patterns
	baseName := strings.ToLower(filepath.Base(path))
	configNames := []string{
		"config", "settings", ".env", "dockerfile", "makefile",
		"package.json", "tsconfig.json", "webpack.config", ".gitignore",
		"requirements.txt", "cargo.toml", "go.mod", "pom.xml",
	}

	for _, name := range configNames {
		if strings.Contains(baseName, name) {
			return true
		}
	}

	// Check for config-like content patterns
	if strings.Contains(content, "=") && strings.Contains(content, "\n") {
		// Likely a properties or env file
		lines := strings.Split(content, "\n")
		configLineCount := 0
		for _, line := range lines[:min(10, len(lines))] {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && strings.Contains(line, "=") {
				configLineCount++
			}
		}
		if configLineCount > 2 {
			return true
		}
	}

	return false
}

func (d *DefaultFileTypeDetector) looksLikeData(content string) bool {
	// Check for JSON
	if strings.HasPrefix(strings.TrimSpace(content), "{") || strings.HasPrefix(strings.TrimSpace(content), "[") {
		return json.Valid([]byte(content))
	}

	// Check for XML
	if strings.HasPrefix(strings.TrimSpace(content), "<?xml") || strings.HasPrefix(strings.TrimSpace(content), "<") {
		return true
	}

	// Check for CSV (simple heuristic)
	lines := strings.Split(content, "\n")
	if len(lines) > 1 {
		firstLine := lines[0]
		commaCount := strings.Count(firstLine, ",")
		if commaCount > 0 {
			// Check if other lines have similar comma counts
			similarLines := 0
			for i := 1; i < min(5, len(lines)); i++ {
				if strings.Count(lines[i], ",") == commaCount {
					similarLines++
				}
			}
			if similarLines >= 2 {
				return true
			}
		}
	}

	// Check for YAML
	if strings.Contains(content, ":\n") || strings.Contains(content, ": ") {
		yamlPatterns := []string{"---", "- ", "  - "}
		for _, pattern := range yamlPatterns {
			if strings.Contains(content, pattern) {
				return true
			}
		}
	}

	return false
}

func (d *DefaultFileTypeDetector) looksLikeMarkdown(content string) bool {
	// Check for markdown patterns
	mdPatterns := []string{
		"# ", "## ", "### ", // Headers
		"```",            // Code blocks
		"* ", "- ", "+ ", // Lists
		"[", "](", // Links
		"**", "__", "*", "_", // Emphasis
	}

	matchCount := 0
	for _, pattern := range mdPatterns {
		if strings.Contains(content, pattern) {
			matchCount++
			if matchCount >= 2 {
				return true
			}
		}
	}

	return false
}

func (d *DefaultFileTypeDetector) looksLikeHTML(content string) bool {
	htmlPatterns := []string{
		"<!DOCTYPE", "<html", "<head", "<body", "<div", "<p>", "<a ",
	}

	for _, pattern := range htmlPatterns {
		if strings.Contains(strings.ToLower(content), strings.ToLower(pattern)) {
			return true
		}
	}

	return false
}

// Helper functions

func initExtensionMap() map[string]f2.FileType {
	return map[string]f2.FileType{
		// Code files
		".go":    f2.FileTypeCode,
		".py":    f2.FileTypeCode,
		".js":    f2.FileTypeCode,
		".ts":    f2.FileTypeCode,
		".jsx":   f2.FileTypeCode,
		".tsx":   f2.FileTypeCode,
		".java":  f2.FileTypeCode,
		".c":     f2.FileTypeCode,
		".cpp":   f2.FileTypeCode,
		".cc":    f2.FileTypeCode,
		".h":     f2.FileTypeCode,
		".hpp":   f2.FileTypeCode,
		".cs":    f2.FileTypeCode,
		".rb":    f2.FileTypeCode,
		".php":   f2.FileTypeCode,
		".swift": f2.FileTypeCode,
		".kt":    f2.FileTypeCode,
		".rs":    f2.FileTypeCode,
		".scala": f2.FileTypeCode,
		".r":     f2.FileTypeCode,
		".m":     f2.FileTypeCode,
		".mm":    f2.FileTypeCode,
		".pl":    f2.FileTypeCode,
		".sh":    f2.FileTypeCode,
		".bash":  f2.FileTypeCode,
		".zsh":   f2.FileTypeCode,
		".fish":  f2.FileTypeCode,
		".ps1":   f2.FileTypeCode,
		".lua":   f2.FileTypeCode,
		".vim":   f2.FileTypeCode,

		// Form files
		".json":       f2.FileTypeConfig,
		".yaml":       f2.FileTypeConfig,
		".yml":        f2.FileTypeConfig,
		".toml":       f2.FileTypeConfig,
		".ini":        f2.FileTypeConfig,
		".conf":       f2.FileTypeConfig,
		".cfg":        f2.FileTypeConfig,
		".env":        f2.FileTypeConfig,
		".properties": f2.FileTypeConfig,

		// Data files
		".xml": f2.FileTypeData,
		".csv": f2.FileTypeData,
		".tsv": f2.FileTypeData,
		".sql": f2.FileTypeData,

		// Markdown files
		".md":       f2.FileTypeMarkdown,
		".markdown": f2.FileTypeMarkdown,
		".rst":      f2.FileTypeMarkdown,
		".adoc":     f2.FileTypeMarkdown,

		// Text files
		".txt": f2.FileTypeText,
		".log": f2.FileTypeText,
		".out": f2.FileTypeText,

		// Image files
		".jpg":  f2.FileTypeImage,
		".jpeg": f2.FileTypeImage,
		".png":  f2.FileTypeImage,
		".gif":  f2.FileTypeImage,
		".bmp":  f2.FileTypeImage,
		".svg":  f2.FileTypeImage,
		".webp": f2.FileTypeImage,
		".ico":  f2.FileTypeImage,

		// PDF files
		".pdf": f2.FileTypePDF,

		// Audio files
		".mp3":  f2.FileTypeAudio,
		".wav":  f2.FileTypeAudio,
		".ogg":  f2.FileTypeAudio,
		".m4a":  f2.FileTypeAudio,
		".flac": f2.FileTypeAudio,

		// Video files
		".mp4":  f2.FileTypeVideo,
		".avi":  f2.FileTypeVideo,
		".mov":  f2.FileTypeVideo,
		".wmv":  f2.FileTypeVideo,
		".flv":  f2.FileTypeVideo,
		".webm": f2.FileTypeVideo,
		".mkv":  f2.FileTypeVideo,

		// Binary files
		".exe":   f2.FileTypeBinary,
		".dll":   f2.FileTypeBinary,
		".so":    f2.FileTypeBinary,
		".dylib": f2.FileTypeBinary,
		".a":     f2.FileTypeBinary,
		".o":     f2.FileTypeBinary,
		".jar":   f2.FileTypeBinary,
		".class": f2.FileTypeBinary,
		".pyc":   f2.FileTypeBinary,
		".pyo":   f2.FileTypeBinary,
		".wasm":  f2.FileTypeBinary,
		".zip":   f2.FileTypeBinary,
		".tar":   f2.FileTypeBinary,
		".gz":    f2.FileTypeBinary,
		".rar":   f2.FileTypeBinary,
		".7z":    f2.FileTypeBinary,
	}
}

func initLanguageMap() map[string]string {
	return map[string]string{
		".go":    "go",
		".py":    "python",
		".js":    "javascript",
		".ts":    "typescript",
		".jsx":   "javascript",
		".tsx":   "typescript",
		".java":  "java",
		".c":     "c",
		".cpp":   "cpp",
		".cc":    "cpp",
		".h":     "c",
		".hpp":   "cpp",
		".cs":    "csharp",
		".rb":    "ruby",
		".php":   "php",
		".swift": "swift",
		".kt":    "kotlin",
		".rs":    "rust",
		".scala": "scala",
		".r":     "r",
		".m":     "objective-c",
		".mm":    "objective-c++",
		".pl":    "perl",
		".sh":    "shell",
		".bash":  "bash",
		".zsh":   "zsh",
		".fish":  "fish",
		".ps1":   "powershell",
		".lua":   "lua",
		".vim":   "vimscript",
		".sql":   "sql",
		".html":  "html",
		".css":   "css",
		".scss":  "scss",
		".sass":  "sass",
		".less":  "less",
		".xml":   "xml",
		".yaml":  "yaml",
		".yml":   "yaml",
		".json":  "json",
		".toml":  "toml",
		".md":    "markdown",
	}
}

func getMimeTypeFromExtension(ext string) string {
	mimeTypes := map[string]string{
		".html": "text/html",
		".css":  "text/css",
		".js":   "application/javascript",
		".json": "application/json",
		".xml":  "application/xml",
		".pdf":  "application/pdf",
		".zip":  "application/zip",
		".tar":  "application/x-tar",
		".gz":   "application/gzip",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".svg":  "image/svg+xml",
		".mp3":  "audio/mpeg",
		".wav":  "audio/wav",
		".mp4":  "video/mp4",
		".avi":  "video/x-msvideo",
		".txt":  "text/plain",
		".csv":  "text/csv",
		".yaml": "application/x-yaml",
		".yml":  "application/x-yaml",
		".toml": "application/toml",
	}

	if mimeType, ok := mimeTypes[ext]; ok {
		return mimeType
	}

	return ""
}

func isValidUTF8(data []byte) bool {
	for i := 0; i < len(data); {
		if data[i] < 0x80 {
			// ASCII
			i++
			continue
		}

		// Multi-byte sequence
		var size int
		if data[i]&0xE0 == 0xC0 {
			size = 2
		} else if data[i]&0xF0 == 0xE0 {
			size = 3
		} else if data[i]&0xF8 == 0xF0 {
			size = 4
		} else {
			return false
		}

		if i+size > len(data) {
			return false
		}

		for j := 1; j < size; j++ {
			if data[i+j]&0xC0 != 0x80 {
				return false
			}
		}

		i += size
	}

	return true
}
