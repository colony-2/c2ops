package rulegate

import (
	"fmt"
	"path/filepath"
	"strings"
)

func resolveWorktreePath(worktreePath string, path string) (string, error) {
	return resolveUnderRoot(worktreePath, path, "path")
}

func resolveInboxPath(artifactInboxPath string, inboxPath string) (string, error) {
	return resolveUnderRoot(artifactInboxPath, inboxPath, "inbox_path")
}

func resolveUnderRoot(root string, relPath string, fieldName string) (string, error) {
	root = strings.TrimSpace(root)
	relPath = strings.TrimSpace(relPath)
	if root == "" {
		return "", fmt.Errorf("root path is required for %s", fieldName)
	}
	if relPath == "" {
		return "", fmt.Errorf("%s is required", fieldName)
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("%s must be relative", fieldName)
	}
	for _, segment := range strings.Split(filepath.ToSlash(relPath), "/") {
		if segment == ".." {
			return "", fmt.Errorf("%s must not contain ..", fieldName)
		}
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root path: %w", err)
	}
	targetAbs, err := filepath.Abs(filepath.Join(rootAbs, filepath.Clean(relPath)))
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", fieldName, err)
	}
	relToRoot, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return "", fmt.Errorf("check %s ancestry: %w", fieldName, err)
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s escapes root", fieldName)
	}
	return targetAbs, nil
}

func slashPath(path string) string {
	return filepath.ToSlash(strings.TrimSpace(path))
}
