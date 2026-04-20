package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	errEmptyPrompt     = errors.New("codex: prompt is required")
	errMissingWorktree = errors.New("codex: worktree root is required")
)

const (
	codexHomeDirName              = ".codex"
	codexHomeStateArtifactDirName = "codex-home-state"
	codexSessionsArtifactDirName  = "codex-sessions"
)

func (o *Options) validate() error {
	if strings.TrimSpace(o.Prompt) == "" {
		return errEmptyPrompt
	}
	o.WorktreeRoot = strings.TrimSpace(o.WorktreeRoot)
	if o.WorktreeRoot == "" {
		return errMissingWorktree
	}
	o.WorktreeRoot = filepath.Clean(o.WorktreeRoot)
	o.WorkDirRoot = strings.TrimSpace(o.WorkDirRoot)
	if o.WorkDirRoot == "" {
		// Backward-compatible fallback for older callers.
		o.WorkDirRoot = o.WorktreeRoot
	}
	o.WorkDirRoot = filepath.Clean(o.WorkDirRoot)
	o.ArtifactInbox = strings.TrimSpace(o.ArtifactInbox)
	if o.ArtifactInbox == "" {
		o.ArtifactInbox = filepath.Join(o.WorkDirRoot, "inbox")
	}
	o.ArtifactInbox = filepath.Clean(o.ArtifactInbox)
	o.ArtifactOutbox = strings.TrimSpace(o.ArtifactOutbox)
	if o.ArtifactOutbox == "" {
		o.ArtifactOutbox = filepath.Join(o.WorkDirRoot, "outbox")
	}
	o.ArtifactOutbox = filepath.Clean(o.ArtifactOutbox)
	o.CodexHome = strings.TrimSpace(o.CodexHome)
	if o.CodexHome == "" {
		o.CodexHome = filepath.Join(o.WorkDirRoot, codexHomeDirName)
	}
	o.CodexHome = filepath.Clean(o.CodexHome)
	o.HostCodexHome = resolveHostCodexHomePath(o.HostCodexHome)
	if o.ExtraEnv == nil {
		o.ExtraEnv = map[string]string{}
	}
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.RunnerFactory == nil {
		o.RunnerFactory = defaultRunnerFactory
	}
	if len(o.StructuredSchema) == 0 {
		o.StructuredSchema = structuredOutputSchema
	}
	o.CellRelativePath = strings.TrimSpace(o.CellRelativePath)
	if o.CellRelativePath == "" {
		o.CellRelativePath = "."
	}
	o.CellRelativePath = filepath.Clean(o.CellRelativePath)
	if o.CellRelativePath == "." {
		o.CellRelativePath = "."
	} else if filepath.IsAbs(o.CellRelativePath) || o.CellRelativePath == ".." || strings.HasPrefix(o.CellRelativePath, ".."+string(filepath.Separator)) {
		return fmt.Errorf("codex: cell relative path %q escapes worktree", o.CellRelativePath)
	}
	if err := ensureDescendantPath(o.WorkDirRoot, o.WorktreeRoot, "worktree root"); err != nil {
		return err
	}
	if err := ensureDescendantPath(o.WorkDirRoot, o.ArtifactInbox, "artifact inbox"); err != nil {
		return err
	}
	if err := ensureDescendantPath(o.WorkDirRoot, o.ArtifactOutbox, "artifact outbox"); err != nil {
		return err
	}
	if err := ensureDescendantPath(o.WorkDirRoot, o.CodexHome, "codex home"); err != nil {
		return err
	}
	if len(o.ConfiguredSkillDirs) > 0 {
		normalized := make([]string, 0, len(o.ConfiguredSkillDirs))
		for _, configuredDir := range o.ConfiguredSkillDirs {
			trimmed := strings.TrimSpace(configuredDir)
			if trimmed == "" {
				continue
			}
			cleanDir := filepath.Clean(trimmed)
			if err := ensureDescendantPath(o.WorkDirRoot, cleanDir, "configured skill dir"); err != nil {
				return err
			}
			normalized = append(normalized, cleanDir)
		}
		o.ConfiguredSkillDirs = normalized
	}
	return nil
}

func (o Options) codexSessionsInboxPath() string {
	return filepath.Join(o.ArtifactInbox, codexSessionsArtifactDirName)
}

func (o Options) codexSessionsOutboxPath() string {
	return filepath.Join(o.ArtifactOutbox, codexSessionsArtifactDirName)
}

func (o Options) codexHomeStateInboxPath() string {
	return filepath.Join(o.ArtifactInbox, codexHomeStateArtifactDirName)
}

func (o Options) codexHomeStateOutboxPath() string {
	return filepath.Join(o.ArtifactOutbox, codexHomeStateArtifactDirName)
}

func (o Options) codexAgentsSkillsPath() string {
	return filepath.Join(o.CodexHome, ".agents", "skills")
}

func (o Options) worktreeAgentsSkillsPath() string {
	return filepath.Join(o.WorktreeRoot, ".agents", "skills")
}

func (o Options) skillSourceDirs() []string {
	seen := make(map[string]struct{}, 1+len(o.ConfiguredSkillDirs))
	dirs := make([]string, 0, 1+len(o.ConfiguredSkillDirs))
	appendDir := func(dir string) {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "" {
			return
		}
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}

	appendDir(o.worktreeAgentsSkillsPath())
	for _, dir := range o.ConfiguredSkillDirs {
		appendDir(dir)
	}

	return dirs
}

func (o Options) stdoutPath(dir string) string {
	return filepath.Join(dir, "stdout.jsonl")
}

func (o Options) stderrPath(dir string) string {
	return filepath.Join(dir, "stderr.txt")
}

func (o Options) containerPath(hostPath string) (string, error) {
	rel, err := filepath.Rel(o.WorkDirRoot, hostPath)
	if err != nil {
		return "", fmt.Errorf("compute container path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s escapes workdir", hostPath)
	}
	return filepath.ToSlash(filepath.Join("/src", rel)), nil
}

func (o Options) relativeToWorkdir(hostPath string) (string, error) {
	rel, err := filepath.Rel(o.WorkDirRoot, hostPath)
	if err != nil {
		return "", fmt.Errorf("compute workdir relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s escapes workdir", hostPath)
	}
	return rel, nil
}

func (o Options) stdoutRelativePath(ts string, id string) string {
	return filepath.ToSlash(filepath.Join("codex", ts, id+".jsonl"))
}

func (o Options) copyEnv() map[string]string {
	if len(o.ExtraEnv) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(o.ExtraEnv))
	for k, v := range o.ExtraEnv {
		cloned[k] = v
	}
	return cloned
}

func ensureDescendantPath(basePath string, targetPath string, label string) error {
	rel, err := filepath.Rel(basePath, targetPath)
	if err != nil {
		return fmt.Errorf("codex: resolve %s path: %w", label, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("codex: %s %q escapes workdir %q", label, targetPath, basePath)
	}
	return nil
}

func resolveHostCodexHomePath(override string) string {
	resolved := strings.TrimSpace(override)
	if resolved == "" {
		envHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if envHome != "" {
			resolved = envHome
		} else if userHome, err := os.UserHomeDir(); err == nil && strings.TrimSpace(userHome) != "" {
			resolved = filepath.Join(userHome, ".codex")
		}
	}
	if resolved == "" {
		return ""
	}
	if !filepath.IsAbs(resolved) {
		if absPath, err := filepath.Abs(resolved); err == nil {
			resolved = absPath
		}
	}
	return filepath.Clean(resolved)
}
