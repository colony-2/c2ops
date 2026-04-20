package llmadapters

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	f2 "github.com/colony-2/c2j/pkg/file"
)

// SecretFilter removes sensitive information
type SecretFilter interface {
	// FilterContent removes secrets from text
	FilterContent(content string) string

	// FilterFile removes secrets from file content
	FilterFile(file *f2.File) *f2.File

	// AddPattern adds a secret pattern to filter
	AddPattern(pattern string)
}

// DefaultSecretFilter implements SecretFilter
type DefaultSecretFilter struct {
	patterns []*regexp.Regexp
	masks    map[string]string
}

// NewSecretFilter creates a new secret filter with default patterns
func NewSecretFilter() SecretFilter {
	filter := &DefaultSecretFilter{
		patterns: []*regexp.Regexp{},
		masks:    make(map[string]string),
	}

	// Add default patterns
	filter.addDefaultPatterns()

	return filter
}

// FilterContent removes secrets from text
func (f *DefaultSecretFilter) FilterContent(content string) string {
	filtered := content

	// Apply all patterns
	for _, pattern := range f.patterns {
		filtered = pattern.ReplaceAllStringFunc(filtered, func(match string) string {
			// Check if we have a cached mask for this secret
			if mask, ok := f.masks[match]; ok {
				return mask
			}

			// Create a new mask
			mask := f.createMask(match)
			f.masks[match] = mask
			return mask
		})
	}

	// Filter environment variable values
	filtered = f.filterEnvVarValues(filtered)

	// Filter JSON secrets
	filtered = f.filterJSONSecrets(filtered)

	return filtered
}

// FilterFile removes secrets from file content
func (f *DefaultSecretFilter) FilterFile(file *f2.File) *f2.File {
	if file == nil {
		return file
	}

	// Create a copy to avoid modifying the original
	filteredFile := *file

	// Filter content based on file type
	switch file.Type {
	case f2.FileTypeText, f2.FileTypeCode, f2.FileTypeConfig, f2.FileTypeMarkdown, f2.FileTypeData:
		// Filter text-based content
		contentStr := string(file.Content)
		filteredContent := f.FilterContent(contentStr)
		filteredFile.Content = []byte(filteredContent)

	case f2.FileTypeBinary, f2.FileTypeImage, f2.FileTypePDF, f2.FileTypeAudio, f2.FileTypeVideo:
		// Don't filter binary files
		// Just add a note in metadata
		if filteredFile.Metadata == nil {
			filteredFile.Metadata = make(map[string]interface{})
		}
		filteredFile.Metadata["secret_filtering"] = "skipped_binary"

	default:
		// For unknown types, attempt filtering if it looks like text
		detector := NewFileTypeDetector()
		if detector.IsTextFile(file.Content) {
			contentStr := string(file.Content)
			filteredContent := f.FilterContent(contentStr)
			filteredFile.Content = []byte(filteredContent)
		}
	}

	// Filter metadata
	if file.Metadata != nil {
		filteredFile.Metadata = f.filterMetadata(file.Metadata)
	}

	return &filteredFile
}

// AddPattern adds a custom secret pattern to filter
func (f *DefaultSecretFilter) AddPattern(pattern string) {
	if regex, err := regexp.Compile(pattern); err == nil {
		f.patterns = append(f.patterns, regex)
	}
}

// addDefaultPatterns adds default secret patterns
func (f *DefaultSecretFilter) addDefaultPatterns() {
	defaultPatterns := []string{
		// API Keys
		`(?i)(api[_\-\s]?key[\s]*[=:]\s*)["']?([a-zA-Z0-9\-_]{20,})["']?`,
		`(?i)(apikey[\s]*[=:]\s*)["']?([a-zA-Z0-9\-_]{20,})["']?`,
		`(?i)(api[_\-\s]?secret[\s]*[=:]\s*)["']?([a-zA-Z0-9\-_]{20,})["']?`,

		// AWS
		`AKIA[0-9A-Z]{16}`, // AWS Access Key ID
		`(?i)(aws[_\-\s]?secret[_\-\s]?access[_\-\s]?key[\s]*[=:]\s*)["']?([a-zA-Z0-9/+=]{40})["']?`,
		`(?i)(aws[_\-\s]?session[_\-\s]?token[\s]*[=:]\s*)["']?([a-zA-Z0-9/+=]{100,})["']?`,

		// JWT Tokens
		`eyJ[a-zA-Z0-9\-_]+\.eyJ[a-zA-Z0-9\-_]+\.[a-zA-Z0-9\-_]+`,

		// GitHub Tokens
		`ghp_[a-zA-Z0-9]{36}`,
		`gho_[a-zA-Z0-9]{36}`,
		`ghu_[a-zA-Z0-9]{36}`,
		`ghs_[a-zA-Z0-9]{36}`,
		`ghr_[a-zA-Z0-9]{36}`,

		// Google
		`AIza[0-9A-Za-z\-_]{35}`, // Google API Key
		`(?i)(google[_\-\s]?api[_\-\s]?key[\s]*[=:]\s*)["']?([a-zA-Z0-9\-_]{39})["']?`,

		// Slack
		`xox[baprs]-[0-9]{10,12}-[0-9]{10,12}-[a-zA-Z0-9]{24,32}`,

		// Generic Secrets
		`(?i)(password[\s]*[=:]\s*)["']?([^"'\s]{8,})["']?`,
		`(?i)(passwd[\s]*[=:]\s*)["']?([^"'\s]{8,})["']?`,
		`(?i)(secret[\s]*[=:]\s*)["']?([^"'\s]{8,})["']?`,
		`(?i)(token[\s]*[=:]\s*)["']?([a-zA-Z0-9\-_]{16,})["']?`,
		`(?i)(auth[_\-\s]?token[\s]*[=:]\s*)["']?([a-zA-Z0-9\-_]{16,})["']?`,

		// Private Keys
		`-----BEGIN (RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`,
		`-----BEGIN PGP PRIVATE KEY BLOCK-----`,

		// Database URLs
		`(?i)(postgres|postgresql|mysql|mongodb|redis|sqlite):\/\/[^:]+:[^@]+@[^\/]+\/\w+`,

		// Bearer Tokens
		`(?i)Bearer\s+[a-zA-Z0-9\-_]+\.[a-zA-Z0-9\-_]+\.[a-zA-Z0-9\-_]+`,

		// SSH Keys
		`ssh-rsa\s+[A-Za-z0-9+/]+[=]{0,3}(\s+.+)?`,
		`ssh-ed25519\s+[A-Za-z0-9+/]+[=]{0,3}(\s+.+)?`,

		// Credit Card Numbers (basic patterns)
		`\b(?:\d[ -]*?){13,16}\b`, // Very basic, should be refined

		// Social Security Numbers (US)
		`\b\d{3}-\d{2}-\d{4}\b`,

		// Email addresses (in sensitive contexts)
		`(?i)(email[\s]*[=:]\s*)["']?([a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,})["']?`,
	}

	for _, pattern := range defaultPatterns {
		if regex, err := regexp.Compile(pattern); err == nil {
			f.patterns = append(f.patterns, regex)
		}
	}
}

// createMask creates a mask for a secret value
func (f *DefaultSecretFilter) createMask(secret string) string {
	length := len(secret)

	// Determine the type of secret for better masking
	switch {
	case strings.HasPrefix(secret, "eyJ"):
		return "[FILTERED_JWT_TOKEN]"
	case strings.HasPrefix(secret, "AKIA"):
		return "[FILTERED_AWS_ACCESS_KEY]"
	case strings.HasPrefix(secret, "ghp_"):
		return "[FILTERED_GITHUB_TOKEN]"
	case strings.HasPrefix(secret, "xox"):
		return "[FILTERED_SLACK_TOKEN]"
	case strings.Contains(strings.ToLower(secret), "password"):
		return "[FILTERED_PASSWORD]"
	case strings.Contains(strings.ToLower(secret), "secret"):
		return "[FILTERED_SECRET]"
	case strings.Contains(strings.ToLower(secret), "token"):
		return "[FILTERED_TOKEN]"
	case strings.Contains(strings.ToLower(secret), "key"):
		return "[FILTERED_API_KEY]"
	case strings.Contains(secret, "-----BEGIN"):
		return "[FILTERED_PRIVATE_KEY]"
	case length == 16 && isNumeric(secret):
		return "[FILTERED_CREDIT_CARD]"
	case regexp.MustCompile(`\d{3}-\d{2}-\d{4}`).MatchString(secret):
		return "[FILTERED_SSN]"
	default:
		// Generic masking - show first and last few characters if long enough
		if length > 8 {
			return secret[:2] + strings.Repeat("*", length-4) + secret[length-2:]
		}
		return strings.Repeat("*", length)
	}
}

// filterEnvVarValues filters environment variable values
func (f *DefaultSecretFilter) filterEnvVarValues(content string) string {
	// Pattern for environment variable assignments
	envPattern := regexp.MustCompile(`(?i)((?:export\s+)?[A-Z_][A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD|PWD|AUTH|CREDENTIAL|API)[A-Z0-9_]*)\s*=\s*["']?([^"'\s]+)["']?`)

	return envPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := envPattern.FindStringSubmatch(match)
		if len(parts) >= 3 {
			varName := parts[1]
			// Keep the variable name but mask the value
			return fmt.Sprintf("%s=[FILTERED]", varName)
		}
		return match
	})
}

// filterJSONSecrets filters secrets in JSON content
func (f *DefaultSecretFilter) filterJSONSecrets(content string) string {
	// Try to detect and filter JSON content
	if json.Valid([]byte(content)) {
		var data interface{}
		if err := json.Unmarshal([]byte(content), &data); err == nil {
			filtered := f.filterJSONValue(data)
			if result, err := json.MarshalIndent(filtered, "", "  "); err == nil {
				return string(result)
			}
		}
	}

	// If not valid JSON or filtering failed, try inline JSON filtering
	jsonKeyPattern := regexp.MustCompile(`"((?i:password|secret|token|key|auth|credential|api_key|apikey)[^"]*)":\s*"([^"]+)"`)
	return jsonKeyPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := jsonKeyPattern.FindStringSubmatch(match)
		if len(parts) >= 3 {
			key := parts[1]
			return fmt.Sprintf(`"%s": "[FILTERED]"`, key)
		}
		return match
	})
}

// filterJSONValue recursively filters JSON values
func (f *DefaultSecretFilter) filterJSONValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		filtered := make(map[string]interface{})
		for key, val := range v {
			lowerKey := strings.ToLower(key)
			// Check if key indicates sensitive data
			if containsSensitiveKeyword(lowerKey) {
				if str, ok := val.(string); ok && str != "" {
					filtered[key] = "[FILTERED]"
				} else {
					filtered[key] = val
				}
			} else {
				filtered[key] = f.filterJSONValue(val)
			}
		}
		return filtered

	case []interface{}:
		filtered := make([]interface{}, len(v))
		for i, item := range v {
			filtered[i] = f.filterJSONValue(item)
		}
		return filtered

	default:
		return v
	}
}

// filterMetadata filters sensitive data from metadata
func (f *DefaultSecretFilter) filterMetadata(metadata map[string]interface{}) map[string]interface{} {
	filtered := make(map[string]interface{})

	for key, value := range metadata {
		lowerKey := strings.ToLower(key)

		// Check if key indicates sensitive data
		if containsSensitiveKeyword(lowerKey) {
			if str, ok := value.(string); ok && str != "" {
				filtered[key] = "[FILTERED]"
			} else {
				filtered[key] = value
			}
		} else {
			// Recursively filter if value is a map
			if subMap, ok := value.(map[string]interface{}); ok {
				filtered[key] = f.filterMetadata(subMap)
			} else {
				filtered[key] = value
			}
		}
	}

	return filtered
}

// Helper functions

func containsSensitiveKeyword(str string) bool {
	sensitiveKeywords := []string{
		"password", "passwd", "pwd",
		"secret",
		"token",
		"key", "apikey", "api_key",
		"auth", "authorization",
		"credential",
		"private",
		"ssn", "social_security",
		"credit_card", "creditcard",
	}

	lower := strings.ToLower(str)
	for _, keyword := range sensitiveKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}

	return false
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
