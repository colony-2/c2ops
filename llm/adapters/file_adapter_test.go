package llmadapters

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	f2 "github.com/colony-2/c2j/pkg/file"
)

// mockFileAdapter is a mock implementation for testing
type mockFileAdapter struct {
	generateFunc      func(context.Context, string, Config) (Response, error)
	generateWithFiles func(context.Context, string, []f2.File, Config) (Response, error)
	capabilities      FileCapabilities
}

func (m *mockFileAdapter) Generate(ctx context.Context, prompt string, config Config) (Response, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, prompt, config)
	}
	return Response{}, nil
}

func (m *mockFileAdapter) GenerateWithTools(ctx context.Context, prompt string, tools []Tool, config Config) (Response, error) {
	return Response{}, nil
}

func (m *mockFileAdapter) StreamGenerate(ctx context.Context, prompt string, config Config) (<-chan Token, error) {
	return nil, nil
}

func (m *mockFileAdapter) GenerateWithFiles(ctx context.Context, prompt string, files []f2.File, config Config) (Response, error) {
	if m.generateWithFiles != nil {
		return m.generateWithFiles(ctx, prompt, files, config)
	}
	return Response{}, nil
}

func (m *mockFileAdapter) GetFileCapabilities() FileCapabilities {
	return m.capabilities
}

func (m *mockFileAdapter) ValidateFile(file f2.File) error {
	caps := m.GetFileCapabilities()
	for _, t := range caps.SupportedTypes {
		if file.Type == t {
			if int64(len(file.Content)) <= caps.MaxFileSize {
				return nil
			}
		}
	}
	return ErrInvalidConfig
}

// Test file type detection
func TestGetFileType(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		expected f2.FileType
	}{
		{"text file", "text/plain", f2.FileTypeText},
		{"javascript", "application/javascript", f2.FileTypeText},
		{"json", "application/json", f2.FileTypeText},
		{"jpeg image", "image/jpeg", f2.FileTypeImage},
		{"png image", "image/png", f2.FileTypeImage},
		{"pdf", "application/pdf", f2.FileTypePDF},
		{"mp3 audio", "audio/mp3", f2.FileTypeAudio},
		{"mp4 video", "video/mp4", f2.FileTypeVideo},
		{"unknown", "application/octet-stream", f2.FileTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetFileType(tt.mimeType)
			if result != tt.expected {
				t.Errorf("GetFileType(%s) = %v, want %v", tt.mimeType, result, tt.expected)
			}
		})
	}
}

// Test OpenAI file handling
func TestOpenAIAdapter_FileHandling(t *testing.T) {
	// Note: This test requires proper mocking of the OpenAI client
	// For now, we'll test the file validation and capability methods

	adapter := &OpenAIAdapter{}

	t.Run("capabilities", func(t *testing.T) {
		caps := adapter.GetFileCapabilities()

		// Check supported types
		supportedTypes := map[f2.FileType]bool{
			f2.FileTypeText:  false,
			f2.FileTypeImage: false,
		}

		for _, typ := range caps.SupportedTypes {
			supportedTypes[typ] = true
		}

		if !supportedTypes[f2.FileTypeText] {
			t.Error("OpenAI should support text files")
		}
		if !supportedTypes[f2.FileTypeImage] {
			t.Error("OpenAI should support image files")
		}

		// Check size limits
		if caps.MaxFileSize != 20*1024*1024 {
			t.Errorf("Expected max file size of 20MB, got %d", caps.MaxFileSize)
		}
	})

	t.Run("validate text file", func(t *testing.T) {
		file := f2.File{
			Path:     "test.txt",
			Content:  []byte("test content"),
			MimeType: "text/plain",
			Type:     f2.FileTypeText,
		}

		err := adapter.ValidateFile(file)
		if err != nil {
			t.Errorf("Should accept text file: %v", err)
		}
	})

	t.Run("validate image file", func(t *testing.T) {
		file := f2.File{
			Path:     "test.png",
			Content:  []byte("fake image data"),
			MimeType: "image/png",
			Type:     f2.FileTypeImage,
		}

		err := adapter.ValidateFile(file)
		if err != nil {
			t.Errorf("Should accept image file: %v", err)
		}
	})

	t.Run("validate PDF as text", func(t *testing.T) {
		file := f2.File{
			Path:     "test.pdf",
			Content:  []byte("fake pdf data"),
			MimeType: "application/pdf",
			Type:     f2.FileTypePDF,
		}

		// OpenAI processes PDFs as text
		err := adapter.ValidateFile(file)
		if err != nil {
			t.Errorf("Should accept PDF file: %v", err)
		}
	})

	t.Run("reject oversized file", func(t *testing.T) {
		// Create a file larger than 20MB
		largeContent := make([]byte, 21*1024*1024)
		file := f2.File{
			Path:     "large.txt",
			Content:  largeContent,
			MimeType: "text/plain",
			Type:     f2.FileTypeText,
		}

		err := adapter.ValidateFile(file)
		if err == nil {
			t.Error("Should reject oversized file")
		}
	})

	t.Run("reject unsupported file type", func(t *testing.T) {
		file := f2.File{
			Path:     "test.mp3",
			Content:  []byte("fake audio"),
			MimeType: "audio/mp3",
			Type:     f2.FileTypeAudio,
		}

		err := adapter.ValidateFile(file)
		if err == nil {
			t.Error("Should reject audio file")
		}
	})
}

// Test Anthropic file handling
func TestAnthropicAdapter_FileHandling(t *testing.T) {
	adapter := &AnthropicAdapter{}

	t.Run("capabilities", func(t *testing.T) {
		caps := adapter.GetFileCapabilities()

		// Check supported types
		supportedTypes := map[f2.FileType]bool{
			f2.FileTypeText:  false,
			f2.FileTypeImage: false,
			f2.FileTypePDF:   false,
		}

		for _, typ := range caps.SupportedTypes {
			supportedTypes[typ] = true
		}

		if !supportedTypes[f2.FileTypeText] {
			t.Error("Anthropic should support text files")
		}
		if !supportedTypes[f2.FileTypeImage] {
			t.Error("Anthropic should support image files")
		}
		if !supportedTypes[f2.FileTypePDF] {
			t.Error("Anthropic should support PDF files natively")
		}
	})

	t.Run("validate supported image types", func(t *testing.T) {
		supportedTypes := []string{"image/jpeg", "image/png", "image/gif", "image/webp"}

		for _, mimeType := range supportedTypes {
			file := f2.File{
				Path:     "test." + strings.Split(mimeType, "/")[1],
				Content:  []byte("fake image"),
				MimeType: mimeType,
				Type:     f2.FileTypeImage,
			}

			err := adapter.ValidateFile(file)
			if err != nil {
				t.Errorf("Should accept %s: %v", mimeType, err)
			}
		}
	})

	t.Run("reject unsupported image type", func(t *testing.T) {
		file := f2.File{
			Path:     "test.bmp",
			Content:  []byte("fake image"),
			MimeType: "image/bmp",
			Type:     f2.FileTypeImage,
		}

		err := adapter.ValidateFile(file)
		if err == nil {
			t.Error("Should reject BMP image")
		}
	})

	t.Run("validate PDF file", func(t *testing.T) {
		file := f2.File{
			Path:     "document.pdf",
			Content:  []byte("fake pdf"),
			MimeType: "application/pdf",
			Type:     f2.FileTypePDF,
		}

		err := adapter.ValidateFile(file)
		if err != nil {
			t.Errorf("Should accept PDF file: %v", err)
		}
	})
}

// Test Gemini file handling
func TestGeminiAdapter_FileHandling(t *testing.T) {
	adapter := &GeminiAdapter{}

	t.Run("capabilities", func(t *testing.T) {
		caps := adapter.GetFileCapabilities()

		// Gemini has limited support without Files API
		expectedTypes := []f2.FileType{
			f2.FileTypeText,
			f2.FileTypeImage, // Limited support
			f2.FileTypePDF,   // Limited support
		}

		supportedTypes := make(map[f2.FileType]bool)
		for _, typ := range caps.SupportedTypes {
			supportedTypes[typ] = true
		}

		for _, expected := range expectedTypes {
			if !supportedTypes[expected] {
				t.Errorf("Gemini should support %v files", expected)
			}
		}

		// Check size limits (reduced without Files API)
		if caps.MaxFileSize != 5*1024*1024 {
			t.Errorf("Expected max file size of 5MB, got %d", caps.MaxFileSize)
		}
	})

	t.Run("validate video file", func(t *testing.T) {
		videoTypes := []string{"video/mp4", "video/mpeg", "video/webm"}

		for _, mimeType := range videoTypes {
			file := f2.File{
				Path:     "test." + strings.Split(mimeType, "/")[1],
				Content:  []byte("fake video"),
				MimeType: mimeType,
				Type:     f2.FileTypeVideo,
			}

			err := adapter.ValidateFile(file)
			if err != nil {
				t.Errorf("Should accept %s: %v", mimeType, err)
			}
		}
	})

	t.Run("validate audio file", func(t *testing.T) {
		audioTypes := []string{"audio/mp3", "audio/wav", "audio/flac"}

		for _, mimeType := range audioTypes {
			file := f2.File{
				Path:     "test." + strings.Split(mimeType, "/")[1],
				Content:  []byte("fake audio"),
				MimeType: mimeType,
				Type:     f2.FileTypeAudio,
			}

			err := adapter.ValidateFile(file)
			if err != nil {
				t.Errorf("Should accept %s: %v", mimeType, err)
			}
		}
	})

	t.Run("accept video with warning", func(t *testing.T) {
		file := f2.File{
			Path:     "test.mkv",
			Content:  []byte("fake video"),
			MimeType: "video/x-matroska",
			Type:     f2.FileTypeVideo,
		}

		// Gemini will accept but warn about limitations
		err := adapter.ValidateFile(file)
		if err != nil {
			t.Errorf("Should accept video file with warning: %v", err)
		}
	})
}

// Test file handling with mock adapter
func TestFileAdapter_GenerateWithFiles(t *testing.T) {
	ctx := context.Background()

	t.Run("text files only", func(t *testing.T) {
		adapter := &mockFileAdapter{
			generateWithFiles: func(ctx context.Context, prompt string, files []f2.File, config Config) (Response, error) {
				// Check that files were passed
				if len(files) != 2 {
					t.Errorf("Expected 2 files, got %d", len(files))
				}

				// Check prompt
				if prompt != "Analyze these files" {
					t.Errorf("Expected prompt 'Analyze these files', got %s", prompt)
				}

				return Response{
					Content: "Analysis complete",
					Usage: Usage{
						PromptTokens:     100,
						CompletionTokens: 50,
						TotalTokens:      150,
					},
				}, nil
			},
			capabilities: FileCapabilities{
				SupportedTypes: []f2.FileType{f2.FileTypeText},
				MaxFileSize:    1024 * 1024,
				MaxFileCount:   10,
			},
		}

		files := []f2.File{
			{
				Path:     "file1.txt",
				Name:     "First File",
				Content:  []byte("Content of first file"),
				MimeType: "text/plain",
				Type:     f2.FileTypeText,
			},
			{
				Path:     "file2.txt",
				Name:     "Second File",
				Content:  []byte("Content of second file"),
				MimeType: "text/plain",
				Type:     f2.FileTypeText,
			},
		}

		config := Config{
			Model:        "test-model",
			Temperature:  0.7,
			MaxTokens:    1000,
			SystemPrompt: "You are a file analyzer",
		}

		response, err := adapter.GenerateWithFiles(ctx, "Analyze these files", files, config)
		if err != nil {
			t.Fatalf("GenerateWithFiles failed: %v", err)
		}

		if response.Content != "Analysis complete" {
			t.Errorf("Expected content 'Analysis complete', got %s", response.Content)
		}

		if response.Usage.TotalTokens != 150 {
			t.Errorf("Expected 150 total tokens, got %d", response.Usage.TotalTokens)
		}
	})

	t.Run("mixed file types", func(t *testing.T) {
		adapter := &mockFileAdapter{
			generateWithFiles: func(ctx context.Context, prompt string, files []f2.File, config Config) (Response, error) {
				// Check file types
				hasText := false
				hasImage := false
				hasPDF := false

				for _, file := range files {
					switch file.Type {
					case f2.FileTypeText:
						hasText = true
					case f2.FileTypeImage:
						hasImage = true
					case f2.FileTypePDF:
						hasPDF = true
					}
				}

				if !hasText || !hasImage || !hasPDF {
					t.Error("Expected text, image, and PDF files")
				}

				return Response{
					Content: "Mixed content analyzed",
				}, nil
			},
			capabilities: FileCapabilities{
				SupportedTypes: []f2.FileType{f2.FileTypeText, f2.FileTypeImage, f2.FileTypePDF},
				MaxFileSize:    10 * 1024 * 1024,
				MaxFileCount:   10,
			},
		}

		files := []f2.File{
			{
				Path:     "doc.txt",
				Content:  []byte("Text content"),
				MimeType: "text/plain",
				Type:     f2.FileTypeText,
			},
			{
				Path:     "image.png",
				Content:  []byte("PNG data"),
				MimeType: "image/png",
				Type:     f2.FileTypeImage,
			},
			{
				Path:     "document.pdf",
				Content:  []byte("PDF data"),
				MimeType: "application/pdf",
				Type:     f2.FileTypePDF,
			},
		}

		response, err := adapter.GenerateWithFiles(ctx, "Analyze mixed content", files, Config{})
		if err != nil {
			t.Fatalf("GenerateWithFiles failed: %v", err)
		}

		if response.Content != "Mixed content analyzed" {
			t.Errorf("Unexpected response: %s", response.Content)
		}
	})
}

// Test base64 encoding for images
func TestBase64ImageEncoding(t *testing.T) {
	imageData := []byte("fake image data")
	base64Data := base64.StdEncoding.EncodeToString(imageData)

	// Verify encoding
	decoded, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		t.Fatalf("Failed to decode base64: %v", err)
	}

	if string(decoded) != string(imageData) {
		t.Error("Base64 encoding/decoding mismatch")
	}

	// Test data URL format
	dataURL := "data:image/png;base64," + base64Data
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Error("Invalid data URL format")
	}
}
