package llmadapters

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	f2 "github.com/colony-2/c2j/pkg/file"
)

// FileCollector provides file collection capabilities for LLM context
type FileCollector interface {
	// CollectFiles gathers files based on patterns
	CollectFiles(patterns []string, opts CollectionOptions) ([]f2.File, error)

	// CollectGitFiles gathers git-tracked files
	CollectGitFiles(repoPath string, opts GitCollectionOptions) ([]f2.File, error)
}

// CollectionOptions configures file collection
type CollectionOptions struct {
	MaxFileSize     int64    `json:"max_file_size"`
	ExcludePatterns []string `json:"exclude_patterns"`
	IncludeHidden   bool     `json:"include_hidden"`
	FollowSymlinks  bool     `json:"follow_symlinks"`
	AutoDetectType  bool     `json:"auto_detect_type"`
}

// GitCollectionOptions extends CollectionOptions for git
type GitCollectionOptions struct {
	CollectionOptions
	IncludeStaged    bool   `json:"include_staged"`
	IncludeUntracked bool   `json:"include_untracked"`
	Branch           string `json:"branch,omitempty"`
}

// DefaultFileCollector implements FileCollector
type DefaultFileCollector struct {
	typeDetector FileTypeDetector
}

// NewFileCollector creates a new file collector
func NewFileCollector() FileCollector {
	return &DefaultFileCollector{
		typeDetector: NewFileTypeDetector(),
	}
}

// CollectFiles gathers files based on patterns
func (c *DefaultFileCollector) CollectFiles(patterns []string, opts CollectionOptions) ([]f2.File, error) {
	files := []f2.File{}
	seen := make(map[string]bool)

	// Set defaults
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = 1024 * 1024 // 1MB default
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %s: %w", pattern, err)
		}

		for _, match := range matches {
			// Skip if already processed
			if seen[match] {
				continue
			}
			seen[match] = true

			// Check if file should be excluded
			if c.shouldExclude(match, opts) {
				continue
			}

			// Process the file
			file, err := c.processFile(match, opts)
			if err != nil {
				// Skip files that can't be processed
				continue
			}

			files = append(files, file)
		}
	}

	return files, nil
}

// CollectGitFiles gathers git-tracked files
func (c *DefaultFileCollector) CollectGitFiles(repoPath string, opts GitCollectionOptions) ([]f2.File, error) {
	files := []f2.File{}

	// Ensure we're in a git repository
	if !c.isGitRepo(repoPath) {
		return nil, fmt.Errorf("not a git repository: %s", repoPath)
	}

	// Get list of files from git
	gitFiles, err := c.getGitFiles(repoPath, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to get git files: %w", err)
	}

	for _, gitFile := range gitFiles {
		// Skip if file should be excluded
		if c.shouldExclude(gitFile, opts.CollectionOptions) {
			continue
		}

		// Process the file
		fullPath := filepath.Join(repoPath, gitFile)
		file, err := c.processFile(fullPath, opts.CollectionOptions)
		if err != nil {
			// Skip files that can't be processed
			continue
		}

		// Add git metadata
		if file.Metadata == nil {
			file.Metadata = make(map[string]interface{})
		}
		file.Metadata["git_tracked"] = true

		files = append(files, file)
	}

	return files, nil
}

// processFile processes a single file
func (c *DefaultFileCollector) processFile(path string, opts CollectionOptions) (f2.File, error) {
	file := f2.File{
		Path:     path,
		Name:     filepath.Base(path),
		Metadata: make(map[string]interface{}),
	}

	// Get file info
	info, err := os.Stat(path)
	if err != nil {
		return file, err
	}

	// Skip directories
	if info.IsDir() {
		return file, fmt.Errorf("path is a directory")
	}

	// Check file size
	if info.Size() > opts.MaxFileSize {
		return file, fmt.Errorf("file exceeds maximum size")
	}

	// Read file content
	content, err := ioutil.ReadFile(path)
	if err != nil {
		return file, err
	}

	file.Content = content
	file.Metadata["size"] = info.Size()
	file.Metadata["modified"] = info.ModTime().Unix()

	// Detect file type if requested
	if opts.AutoDetectType {
		file.MimeType = c.typeDetector.DetectMimeType(content, path)
		file.Type = c.typeDetector.DetectType(content, path)

		// Add language for code files
		if file.Type == f2.FileTypeCode {
			if lang := c.typeDetector.GetLanguage(content, path); lang != "" {
				file.Metadata["language"] = lang
			}
		}
	} else {
		// Basic type detection from extension
		file.MimeType = getMimeTypeFromPath(path)
		file.Type = GetFileType(file.MimeType)
	}

	return file, nil
}

// shouldExclude checks if a file should be excluded
func (c *DefaultFileCollector) shouldExclude(path string, opts CollectionOptions) bool {
	// Check hidden files
	if !opts.IncludeHidden && strings.HasPrefix(filepath.Base(path), ".") {
		return true
	}

	// Check exclude patterns
	for _, pattern := range opts.ExcludePatterns {
		matched, err := filepath.Match(pattern, filepath.Base(path))
		if err == nil && matched {
			return true
		}

		// Also check against full path
		matched, err = filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}

	// Check if it's a symlink and we're not following them
	if !opts.FollowSymlinks {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}

	return false
}

// isGitRepo checks if a path is a git repository
func (c *DefaultFileCollector) isGitRepo(path string) bool {
	gitDir := filepath.Join(path, ".git")
	info, err := os.Stat(gitDir)
	return err == nil && info.IsDir()
}

// getGitFiles gets list of files from git
func (c *DefaultFileCollector) getGitFiles(repoPath string, opts GitCollectionOptions) ([]string, error) {
	files := []string{}

	// Base command to list tracked files
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = repoPath

	// Add branch if specified
	if opts.Branch != "" {
		// Switch to specified branch temporarily
		cmd = exec.Command("git", "ls-tree", "-r", "--name-only", opts.Branch)
		cmd.Dir = repoPath
	}

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	// Parse output
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}

	// Add staged files if requested
	if opts.IncludeStaged {
		cmd = exec.Command("git", "diff", "--cached", "--name-only")
		cmd.Dir = repoPath
		output, err = cmd.Output()
		if err == nil {
			lines = strings.Split(string(output), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line != "" && !contains(files, line) {
					files = append(files, line)
				}
			}
		}
	}

	// Add untracked files if requested
	if opts.IncludeUntracked {
		cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard")
		cmd.Dir = repoPath
		output, err = cmd.Output()
		if err == nil {
			lines = strings.Split(string(output), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line != "" && !contains(files, line) {
					files = append(files, line)
				}
			}
		}
	}

	return files, nil
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
