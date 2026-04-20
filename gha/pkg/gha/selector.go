package gha

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	coreops "github.com/colony-2/c2j/pkg/ops"
	yamlv3 "gopkg.in/yaml.v3"
)

const workflowDir = ".github/workflows"

func resolveWorkflowSelector(selector string, gitCtx coreops.GitExecutionContext) (resolvedWorkflow, error) {
	raw := strings.TrimSpace(selector)
	if raw == "" {
		return resolvedWorkflow{}, fmt.Errorf("workflow is required")
	}
	if strings.TrimSpace(gitCtx.WorktreePath) == "" {
		return resolvedWorkflow{}, fmt.Errorf("worktree path is required")
	}
	if err := validateWorkflowFileName(raw); err != nil {
		return resolvedWorkflow{}, err
	}

	repoPath := filepath.ToSlash(filepath.Join(workflowDir, raw))
	targetPath := filepath.Join(gitCtx.WorktreePath, filepath.FromSlash(repoPath))

	info, err := os.Stat(targetPath)
	if err != nil {
		return resolvedWorkflow{}, err
	}
	if info.IsDir() {
		return resolvedWorkflow{}, fmt.Errorf("workflow path %q is a directory", targetPath)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		return resolvedWorkflow{}, err
	}
	if err := requireWorkflowDispatch(data); err != nil {
		return resolvedWorkflow{}, fmt.Errorf("workflow %q %w", repoPath, err)
	}

	return resolvedWorkflow{
		Selector:       raw,
		Path:           targetPath,
		RepoPath:       repoPath,
		ContentHash:    contentHash(data),
		ResolvedCommit: "",
	}, nil
}

func validateWorkflowFileName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("workflow is required")
	}
	if strings.Contains(trimmed, "://") || strings.HasPrefix(trimmed, "git+") {
		return fmt.Errorf("workflow %q must be a workflow file name under %s", trimmed, workflowDir)
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return fmt.Errorf("workflow %q must be a file name under %s; subdirectories are not supported", trimmed, workflowDir)
	}
	ext := strings.ToLower(filepath.Ext(trimmed))
	switch ext {
	case ".yml", ".yaml":
		return nil
	default:
		return fmt.Errorf("workflow %q must end in .yml or .yaml", trimmed)
	}
}

func requireWorkflowDispatch(data []byte) error {
	var decoded map[string]any
	if err := yamlv3.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("must be valid YAML: %w", err)
	}

	onValue, ok := decoded["on"]
	if !ok {
		return fmt.Errorf("must declare on.workflow_dispatch")
	}
	if workflowDispatchEnabled(onValue) {
		return nil
	}
	return fmt.Errorf("must declare on.workflow_dispatch")
}

func workflowDispatchEnabled(v any) bool {
	switch typed := v.(type) {
	case string:
		return strings.TrimSpace(typed) == workflowDispatchEvent
	case []any:
		for _, item := range typed {
			if workflowDispatchEnabled(item) {
				return true
			}
		}
		return false
	case map[string]any:
		_, ok := typed[workflowDispatchEvent]
		return ok
	case map[any]any:
		for key := range typed {
			if keyString, ok := key.(string); ok && strings.TrimSpace(keyString) == workflowDispatchEvent {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
