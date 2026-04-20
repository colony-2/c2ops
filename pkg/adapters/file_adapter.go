package llmadapters

import (
	"context"

	"github.com/colony-2/c2j/pkg/file"
)

// FileCapabilities describes what file types an adapter supports
type FileCapabilities struct {
	SupportedTypes []file.FileType `json:"supported_types"`
	MaxFileSize    int64           `json:"max_file_size"`
	MaxFileCount   int             `json:"max_file_count"`
	TotalSizeLimit int64           `json:"total_size_limit"`
}

// FileAdapter extends the base Adapter interface with file handling capabilities
type FileAdapter interface {
	Adapter

	// GenerateWithFiles creates a completion with file context
	GenerateWithFiles(ctx context.Context, prompt string, files []file.File, config Config) (Response, error)

	// GetFileCapabilities returns the file handling capabilities of this adapter
	GetFileCapabilities() FileCapabilities

	// ValidateFile checks if a file can be processed by this adapter
	ValidateFile(file file.File) error
}

// FileConfig extends Config with file-specific options
type FileConfig struct {
	Config

	// FileHandling specifies how to handle files
	FileHandling FileHandlingMode `json:"file_handling,omitempty"`

	// OptimizeImages indicates whether to resize/compress images
	OptimizeImages bool `json:"optimize_images,omitempty"`

	// ExtractTextFromPDF indicates whether to extract text from PDFs
	ExtractTextFromPDF bool `json:"extract_text_from_pdf,omitempty"`
}

// FileHandlingMode specifies how files should be handled
type FileHandlingMode string

const (
	// FileHandlingNative uses the provider's native file handling
	FileHandlingNative FileHandlingMode = "native"

	// FileHandlingTextFallback converts all files to text
	FileHandlingTextFallback FileHandlingMode = "text_fallback"

	// FileHandlingHybrid uses native for supported types, fallback for others
	FileHandlingHybrid FileHandlingMode = "hybrid"
)

// GetFileType determines the type of a file based on its MIME type
func GetFileType(mimeType string) file.FileType {
	switch {
	case isTextMimeType(mimeType):
		return file.FileTypeText
	case isImageMimeType(mimeType):
		return file.FileTypeImage
	case mimeType == "application/pdf":
		return file.FileTypePDF
	case isAudioMimeType(mimeType):
		return file.FileTypeAudio
	case isVideoMimeType(mimeType):
		return file.FileTypeVideo
	default:
		return file.FileTypeUnknown
	}
}

func isTextMimeType(mimeType string) bool {
	textTypes := []string{
		"text/", "application/json", "application/xml",
		"application/javascript", "application/typescript",
		"application/x-yaml", "application/toml",
	}
	for _, prefix := range textTypes {
		if len(mimeType) >= len(prefix) && mimeType[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func isImageMimeType(mimeType string) bool {
	return len(mimeType) >= 6 && mimeType[:6] == "image/"
}

func isAudioMimeType(mimeType string) bool {
	return len(mimeType) >= 6 && mimeType[:6] == "audio/"
}

func isVideoMimeType(mimeType string) bool {
	return len(mimeType) >= 6 && mimeType[:6] == "video/"
}
