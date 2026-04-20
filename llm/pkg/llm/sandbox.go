package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SandboxConfig configures the security sandbox
type SandboxConfig struct {
	AllowedPaths    []string `json:"allowed_paths"`
	RestrictedPaths []string `json:"restricted_paths"`
}

// SecuritySandbox provides path validation and security checks
type SecuritySandbox struct {
	allowedPaths    []string
	restrictedPaths []string
}

// NewSecuritySandbox creates a new security sandbox
func NewSecuritySandbox(config SandboxConfig) *SecuritySandbox {
	// Normalize paths
	allowed := make([]string, len(config.AllowedPaths))
	for i, path := range config.AllowedPaths {
		allowed[i] = filepath.Clean(path)
	}
	
	restricted := make([]string, len(config.RestrictedPaths))
	for i, path := range config.RestrictedPaths {
		restricted[i] = filepath.Clean(path)
	}
	
	// Add default restricted paths if not already present
	defaultRestricted := []string{
		"/etc",
		"/usr",
		"/bin",
		"/sbin",
		"/proc",
		"/sys",
		"/dev",
		"~/.ssh",
		"~/.aws",
		"~/.kube",
		"~/.config",
	}
	
	for _, path := range defaultRestricted {
		found := false
		expandedPath := expandPath(path)
		for _, r := range restricted {
			if r == expandedPath {
				found = true
				break
			}
		}
		if !found {
			restricted = append(restricted, expandedPath)
		}
	}
	
	return &SecuritySandbox{
		allowedPaths:    allowed,
		restrictedPaths: restricted,
	}
}

// ValidatePath validates that a path is safe to access
func (s *SecuritySandbox) ValidatePath(path string) error {
	// Clean and resolve the path
	cleanPath := filepath.Clean(path)
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}
	
	// Check for dangerous patterns
	if err := s.checkDangerousPatterns(cleanPath); err != nil {
		return err
	}
	
	// Check against restricted paths
	for _, restricted := range s.restrictedPaths {
		restrictedAbs, _ := filepath.Abs(restricted)
		if strings.HasPrefix(absPath, restrictedAbs) {
			return fmt.Errorf("path '%s' is in restricted area '%s'", path, restricted)
		}
	}
	
	// If allowed paths are specified, path must be within one of them
	if len(s.allowedPaths) > 0 {
		allowed := false
		for _, allowedPath := range s.allowedPaths {
			allowedAbs, _ := filepath.Abs(allowedPath)
			if strings.HasPrefix(absPath, allowedAbs) {
				allowed = true
				break
			}
		}
		
		if !allowed {
			return fmt.Errorf("path '%s' is not in allowed paths", path)
		}
	}
	
	return nil
}

// checkDangerousPatterns checks for dangerous path patterns
func (s *SecuritySandbox) checkDangerousPatterns(path string) error {
	// Check for parent directory traversal
	if strings.Contains(path, "..") {
		return fmt.Errorf("path contains parent directory traversal: %s", path)
	}
	
	// Check for absolute paths to sensitive areas (if not already in restricted)
	sensitivePrefixes := []string{
		"/etc/passwd",
		"/etc/shadow",
		"/etc/sudoers",
		"/root",
		"/var/log/secure",
	}
	
	for _, prefix := range sensitivePrefixes {
		if strings.HasPrefix(path, prefix) {
			return fmt.Errorf("path points to sensitive location: %s", path)
		}
	}
	
	// Check for hidden sensitive files
	sensitiveFiles := []string{
		".ssh/id_rsa",
		".ssh/id_dsa",
		".ssh/id_ecdsa",
		".ssh/id_ed25519",
		".aws/credentials",
		".kube/config",
		".docker/config.json",
		".npmrc",
		".pypirc",
		".gitconfig",
		".git-credentials",
	}
	
	for _, file := range sensitiveFiles {
		if strings.Contains(path, file) {
			return fmt.Errorf("path contains sensitive file pattern: %s", file)
		}
	}
	
	return nil
}

// IsPathAllowed checks if a path is explicitly allowed
func (s *SecuritySandbox) IsPathAllowed(path string) bool {
	if len(s.allowedPaths) == 0 {
		// If no allowed paths specified, check only restrictions
		return s.ValidatePath(path) == nil
	}
	
	cleanPath := filepath.Clean(path)
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return false
	}
	
	for _, allowed := range s.allowedPaths {
		allowedAbs, _ := filepath.Abs(allowed)
		if strings.HasPrefix(absPath, allowedAbs) {
			return true
		}
	}
	
	return false
}

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home := os.Getenv("HOME")
		if home == "" {
			home = os.Getenv("USERPROFILE") // Windows
		}
		if home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}