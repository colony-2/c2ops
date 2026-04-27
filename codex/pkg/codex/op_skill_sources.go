package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type parsedSkillRef struct {
	repoPath       string
	skillsRootPath string
	gitRef         string
}

type skillRefMaterializer func(ctx context.Context, skillRefs []string, stageRoot string, skillsRoot string) ([]string, error)

// materializeSkillRefsFn is replaceable for tests.
var materializeSkillRefsFn skillRefMaterializer = materializeSkillRefs

func prepareConfiguredSkillSources(
	ctx context.Context,
	input ExecOpInput,
	workdir string,
) ([]string, []string, func() error, error) {
	if len(input.Skills) == 0 {
		return nil, nil, nil, nil
	}

	stageRoot := filepath.Join(workdir, ".codex-skill-inputs", uuid.NewString())
	skillsRoot := filepath.Join(stageRoot, "skills")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		return nil, nil, nil, fmt.Errorf("create skill source staging dir: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(stageRoot) }

	skillsInstalled, err := materializeSkillRefsFn(ctx, input.Skills, stageRoot, skillsRoot)
	if err != nil {
		_ = cleanup()
		return nil, nil, nil, err
	}
	return []string{skillsRoot}, skillsInstalled, cleanup, nil
}

func materializeSkillRefs(ctx context.Context, skillRefs []string, stageRoot string, skillsRoot string) ([]string, error) {
	if len(skillRefs) == 0 {
		return nil, nil
	}

	reposRoot := filepath.Join(stageRoot, "repos")
	if err := os.MkdirAll(reposRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create skill repo staging dir: %w", err)
	}

	installed := make([]string, 0, len(skillRefs))
	for idx, rawRef := range skillRefs {
		parsed, err := parseSkillRef(rawRef)
		if err != nil {
			return nil, err
		}

		repoDir := filepath.Join(reposRoot, fmt.Sprintf("repo-%03d", idx+1))
		resolvedCommit, err := materializeSingleSkillRef(ctx, parsed, repoDir, skillsRoot)
		if err != nil {
			return nil, err
		}
		installed = append(installed, parsed.resolvedRef(resolvedCommit))
	}

	return installed, nil
}

func materializeSingleSkillRef(ctx context.Context, parsed parsedSkillRef, repoDir string, skillsRoot string) (string, error) {
	if err := runGitCommand(ctx, "", "init", repoDir); err != nil {
		return "", fmt.Errorf("initialize repo for skills entry %q: %w", parsed.originalRef(), err)
	}
	if err := runGitCommand(ctx, repoDir, "remote", "add", "origin", parsed.repoCloneURL()); err != nil {
		return "", fmt.Errorf("configure remote for skills entry %q: %w", parsed.originalRef(), err)
	}
	if err := runGitCommand(ctx, repoDir, "fetch", "--depth", "1", "origin", parsed.gitRef); err != nil {
		return "", fmt.Errorf("fetch skills entry %q: %w", parsed.originalRef(), err)
	}

	resolvedCommit, err := runGitCommandWithOutput(ctx, repoDir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve fetched commit for skills entry %q: %w", parsed.originalRef(), err)
	}
	resolvedCommit = strings.TrimSpace(strings.Split(resolvedCommit, "\n")[0])
	if resolvedCommit == "" {
		return "", fmt.Errorf("resolve fetched commit for skills entry %q: empty commit", parsed.originalRef())
	}
	if err := runGitCommand(ctx, repoDir, "checkout", "--detach", resolvedCommit); err != nil {
		return "", fmt.Errorf("checkout resolved commit for skills entry %q: %w", parsed.originalRef(), err)
	}

	sourceRoot := filepath.Join(repoDir, filepath.FromSlash(parsed.skillsRootPath))
	sourceInfo, err := os.Stat(sourceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("skill root path %q not found for skills entry %q", parsed.skillsRootPath, parsed.originalRef())
		}
		return "", fmt.Errorf("stat skill root path %q for skills entry %q: %w", parsed.skillsRootPath, parsed.originalRef(), err)
	}
	if !sourceInfo.IsDir() {
		return "", fmt.Errorf("skill root path %q for skills entry %q is not a directory", parsed.skillsRootPath, parsed.originalRef())
	}
	if err := copyDirContents(sourceRoot, skillsRoot); err != nil {
		return "", fmt.Errorf("copy skill root path %q for skills entry %q: %w", parsed.skillsRootPath, parsed.originalRef(), err)
	}

	return resolvedCommit, nil
}

func runGitCommand(ctx context.Context, workingDir string, args ...string) error {
	_, err := runGitCommandWithOutput(ctx, workingDir, args...)
	return err
}

func runGitCommandWithOutput(ctx context.Context, workingDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(workingDir) != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if text == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return string(output), nil
}

func parseSkillRef(rawRef string) (parsedSkillRef, error) {
	rawRef = strings.TrimSpace(rawRef)
	if rawRef == "" {
		return parsedSkillRef{}, fmt.Errorf("skills entry is empty")
	}
	at := strings.LastIndex(rawRef, "@")
	if at <= 0 || at == len(rawRef)-1 {
		return parsedSkillRef{}, fmt.Errorf("invalid skills entry %q: expected <host>/<org>/<repo>/<skills-root-path>@<git-ref>", rawRef)
	}
	refPath := rawRef[:at]
	gitRef := strings.TrimSpace(rawRef[at+1:])
	if gitRef == "" {
		return parsedSkillRef{}, fmt.Errorf("invalid skills entry %q: git ref is empty", rawRef)
	}

	parts := strings.Split(refPath, "/")
	if len(parts) < 4 {
		return parsedSkillRef{}, fmt.Errorf("invalid skills entry %q: expected <host>/<org>/<repo>/<skills-root-path>@<git-ref>", rawRef)
	}
	for _, segment := range parts {
		if strings.TrimSpace(segment) == "" {
			return parsedSkillRef{}, fmt.Errorf("invalid skills entry %q: path contains empty segment", rawRef)
		}
	}

	repoPath := strings.Join(parts[:3], "/")
	skillsRootPath, err := normalizeSkillRootPath(strings.Join(parts[3:], "/"))
	if err != nil {
		return parsedSkillRef{}, fmt.Errorf("invalid skills entry %q: %w", rawRef, err)
	}

	return parsedSkillRef{
		repoPath:       repoPath,
		skillsRootPath: skillsRootPath,
		gitRef:         gitRef,
	}, nil
}

func normalizeSkillRootPath(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("skill root path is empty")
	}
	normalized := path.Clean(rawPath)
	if strings.HasPrefix(normalized, "/") {
		return "", fmt.Errorf("skill root path must be relative")
	}
	if normalized == "." {
		return "", fmt.Errorf("skill root path must reference a directory")
	}
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("skill root path escapes repository root")
	}
	return normalized, nil
}

func (p parsedSkillRef) originalRef() string {
	return fmt.Sprintf("%s/%s@%s", p.repoPath, p.skillsRootPath, p.gitRef)
}

func (p parsedSkillRef) resolvedRef(resolvedCommit string) string {
	return fmt.Sprintf("%s/%s@%s", p.repoPath, p.skillsRootPath, strings.TrimSpace(resolvedCommit))
}

func (p parsedSkillRef) repoCloneURL() string {
	return "https://" + p.repoPath + ".git"
}
