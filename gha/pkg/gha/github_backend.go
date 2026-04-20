package gha

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
)

var githubClientFactory = func(host, token string) (githubActionsClient, error) {
	return newGitHubActionsClient(host, token)
}

var githubGitOps githubGitRunner = gitCLI{}

type githubBackend struct{}

type githubRemoteRepo struct {
	PushURL string
	Host    string
	Owner   string
	Repo    string
}

type githubGitRunner interface {
	pushRef(ctx context.Context, repoPath, remoteURL, remoteRef string) error
	deleteRef(ctx context.Context, remoteURL, remoteRef string) error
}

func (b *githubBackend) Run(ctx context.Context, req backendRequest) (backendResult, error) {
	token := strings.TrimSpace(req.Input.Secrets["GITHUB_TOKEN"])
	if token == "" {
		return backendResult{}, fmt.Errorf("backend %q requires secrets.GITHUB_TOKEN", backendGitHub)
	}

	remote, err := resolveGitHubRemote(req.Input, req.GitContext)
	if err != nil {
		return backendResult{}, err
	}
	workflowRepoPath, err := workflowRepoPathForGitHub(req.Workflow, req.GitContext.WorktreePath)
	if err != nil {
		return backendResult{}, err
	}
	workflowFileName := path.Base(workflowRepoPath)

	branchName := buildGitHubBranchName(req.Input, req.GitContext, req.Workflow)
	remoteRef := "refs/heads/" + branchName

	client, err := githubClientFactory(remote.Host, token)
	if err != nil {
		return backendResult{}, err
	}
	if err := ensureGitHubWorkflowDispatchable(ctx, client, remote, workflowRepoPath); err != nil {
		return backendResult{}, err
	}
	if err := githubGitOps.pushRef(ctx, req.GitContext.WorktreePath, remote.PushURL, remoteRef); err != nil {
		return backendResult{}, err
	}

	cleanupRemoteRef := true
	defer func() {
		if cleanupRemoteRef {
			_ = githubGitOps.deleteRef(context.Background(), remote.PushURL, remoteRef)
		}
	}()

	dispatch, err := client.DispatchWorkflow(ctx, remote.Owner, remote.Repo, workflowFileName, branchName, req.Input.With)
	if err != nil {
		return backendResult{}, err
	}
	if dispatch.RunID == 0 {
		return backendResult{}, fmt.Errorf("github workflow dispatch did not return a workflow run id")
	}

	run, waitErr := waitForGitHubWorkflowRun(ctx, client, remote.Owner, remote.Repo, dispatch.RunID)
	timedOut := errors.Is(waitErr, context.DeadlineExceeded)
	cancelled := errors.Is(waitErr, context.Canceled) && !timedOut
	if waitErr != nil && !timedOut && !cancelled {
		return backendResult{}, waitErr
	}

	var jobs []githubWorkflowJob
	var artifacts []githubWorkflowArtifact
	if waitErr == nil {
		jobs, err = client.ListWorkflowJobs(ctx, remote.Owner, remote.Repo, dispatch.RunID)
		if err != nil {
			return backendResult{}, err
		}
		artifacts, err = client.ListWorkflowRunArtifacts(ctx, remote.Owner, remote.Repo, dispatch.RunID)
		if err != nil {
			return backendResult{}, err
		}
	}

	if err := githubGitOps.deleteRef(ctx, remote.PushURL, remoteRef); err != nil {
		return backendResult{}, err
	}
	cleanupRemoteRef = false

	status, exitCode := normalizeGitHubRunStatus(run, timedOut, cancelled)
	output := RunOutput{
		Status:          status,
		ExitCode:        exitCode,
		DurationSeconds: githubRunDurationSeconds(run),
		Workflow: WorkflowOutput{
			ResolvedSelector: req.Workflow.Selector,
			ResolvedCommit:   req.Workflow.ResolvedCommit,
			ContentHash:      req.Workflow.ContentHash,
		},
		Jobs: normalizeGitHubJobs(jobs),
	}
	switch {
	case timedOut:
		output.ErrorMessage = "workflow polling timed out before completion"
	case cancelled:
		output.ErrorMessage = "workflow polling was canceled before completion"
	}

	return backendResult{
		Output:       output,
		ArtifactRefs: githubArtifactRefs(run, artifacts),
	}, nil
}

type gitCLI struct{}

func (gitCLI) pushRef(ctx context.Context, repoPath, remoteURL, remoteRef string) error {
	_, err := runGit(ctx, repoPath, "push", remoteURL, "HEAD:"+remoteRef)
	return err
}

func (gitCLI) deleteRef(ctx context.Context, remoteURL, remoteRef string) error {
	_, err := runGit(ctx, "", "push", remoteURL, ":"+remoteRef)
	return err
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func resolveGitHubRemote(input RunInput, gitCtx coreops.GitExecutionContext) (githubRemoteRepo, error) {
	pushURL := ""
	if input.Remote != nil {
		pushURL = strings.TrimSpace(input.Remote.PushTo)
	}
	if pushURL == "" {
		if strings.TrimSpace(gitCtx.BaseRepo) == "" {
			return githubRemoteRepo{}, fmt.Errorf("backend %q requires remote.push_to or git_context.base_repo", backendGitHub)
		}
		pushURL = "git@github.com:" + strings.TrimSpace(gitCtx.BaseRepo) + ".git"
	} else if isGitRemoteName(pushURL) {
		resolvedURL, err := resolveGitRemoteURL(gitCtx.WorktreePath, pushURL)
		if err != nil {
			return githubRemoteRepo{}, err
		}
		pushURL = resolvedURL
	}
	return parseGitHubRemote(pushURL)
}

func isGitRemoteName(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" &&
		!strings.Contains(trimmed, "://") &&
		!strings.Contains(trimmed, "@") &&
		!strings.Contains(trimmed, "/") &&
		!strings.Contains(trimmed, ":")
}

func resolveGitRemoteURL(worktreePath string, remoteName string) (string, error) {
	if strings.TrimSpace(worktreePath) == "" {
		return "", fmt.Errorf("remote.push_to=%q requires a worktree path", remoteName)
	}
	output, err := runGit(context.Background(), worktreePath, "remote", "get-url", "--push", strings.TrimSpace(remoteName))
	if err != nil {
		return "", fmt.Errorf("resolve git remote %q: %w", remoteName, err)
	}
	return strings.TrimSpace(output), nil
}

func parseGitHubRemote(pushURL string) (githubRemoteRepo, error) {
	trimmed := strings.TrimSpace(pushURL)
	if trimmed == "" {
		return githubRemoteRepo{}, fmt.Errorf("remote push url is required")
	}
	if strings.HasPrefix(trimmed, "git@") {
		at := strings.Index(trimmed, "@")
		colon := strings.Index(trimmed, ":")
		if at == -1 || colon == -1 || colon <= at+1 {
			return githubRemoteRepo{}, fmt.Errorf("unsupported github remote %q", trimmed)
		}
		host := trimmed[at+1 : colon]
		owner, repo, err := splitOwnerRepo(trimmed[colon+1:])
		if err != nil {
			return githubRemoteRepo{}, err
		}
		return githubRemoteRepo{PushURL: trimmed, Host: host, Owner: owner, Repo: repo}, nil
	}
	if strings.Contains(trimmed, "://") {
		withoutScheme := strings.SplitN(trimmed, "://", 2)[1]
		slash := strings.Index(withoutScheme, "/")
		if slash == -1 {
			return githubRemoteRepo{}, fmt.Errorf("unsupported github remote %q", trimmed)
		}
		hostPart := withoutScheme[:slash]
		pathPart := withoutScheme[slash+1:]
		if at := strings.LastIndex(hostPart, "@"); at != -1 {
			hostPart = hostPart[at+1:]
		}
		owner, repo, err := splitOwnerRepo(pathPart)
		if err != nil {
			return githubRemoteRepo{}, err
		}
		return githubRemoteRepo{PushURL: trimmed, Host: hostPart, Owner: owner, Repo: repo}, nil
	}
	return githubRemoteRepo{}, fmt.Errorf("unsupported github remote %q", trimmed)
}

func splitOwnerRepo(pathValue string) (string, string, error) {
	trimmed := strings.Trim(strings.TrimSpace(pathValue), "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("remote repository path %q must be owner/repo", pathValue)
	}
	owner := strings.TrimSpace(parts[len(parts)-2])
	repo := strings.TrimSpace(parts[len(parts)-1])
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("remote repository path %q must be owner/repo", pathValue)
	}
	return owner, repo, nil
}

func workflowRepoPathForGitHub(workflow resolvedWorkflow, worktreePath string) (string, error) {
	if strings.TrimSpace(workflow.RepoPath) != "" {
		return filepath.ToSlash(filepath.Clean(workflow.RepoPath)), nil
	}
	if strings.TrimSpace(workflow.Path) == "" || strings.TrimSpace(worktreePath) == "" {
		return "", fmt.Errorf("workflow path and worktree path are required")
	}
	baseAbs, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", err
	}
	workflowAbs, err := filepath.Abs(workflow.Path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseAbs, workflowAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workflow path %q is outside the worktree", workflow.Path)
	}
	repoPath := filepath.ToSlash(rel)
	if !strings.HasPrefix(repoPath, workflowDir+"/") {
		return "", fmt.Errorf("workflow path %q must be under %s", workflow.Path, workflowDir)
	}
	return repoPath, nil
}

func ensureGitHubWorkflowDispatchable(ctx context.Context, client githubActionsClient, remote githubRemoteRepo, workflowRepoPath string) error {
	defaultBranch, err := client.GetDefaultBranch(ctx, remote.Owner, remote.Repo)
	if err != nil {
		return err
	}
	defaultBranch = strings.TrimSpace(defaultBranch)
	if defaultBranch == "" {
		return fmt.Errorf("github repository %s/%s did not report a default branch", remote.Owner, remote.Repo)
	}
	exists, err := client.FileExistsAtRef(ctx, remote.Owner, remote.Repo, workflowRepoPath, defaultBranch)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("workflow %q must exist on default branch %q for backend %q", workflowRepoPath, defaultBranch, backendGitHub)
	}
	return nil
}

func buildGitHubBranchName(input RunInput, gitCtx coreops.GitExecutionContext, workflow resolvedWorkflow) string {
	prefix := "c2/gha"
	if input.Remote != nil && strings.TrimSpace(input.Remote.RefPrefix) != "" {
		prefix = strings.TrimSpace(input.Remote.RefPrefix)
	}
	prefix = strings.TrimPrefix(prefix, "refs/heads/")
	prefix = sanitizeBranchSegment(prefix)
	suffix := strings.TrimSpace(gitCtx.InvokeHash)
	if suffix == "" {
		suffix = strings.TrimPrefix(strings.TrimSpace(workflow.ContentHash), "sha256:")
	}
	if suffix == "" {
		suffix = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if len(suffix) > 16 {
		suffix = suffix[:16]
	}
	return strings.Trim(prefix+"/"+sanitizeBranchSegment(suffix), "/")
}

func sanitizeBranchSegment(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "c2-gha"
	}
	var builder strings.Builder
	builder.Grow(len(trimmed))
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '/' || r == '-' || r == '_' || r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	cleaned := strings.Trim(builder.String(), "/.-")
	if cleaned == "" {
		return "c2-gha"
	}
	for strings.Contains(cleaned, "//") {
		cleaned = strings.ReplaceAll(cleaned, "//", "/")
	}
	for strings.Contains(cleaned, "..") {
		cleaned = strings.ReplaceAll(cleaned, "..", ".")
	}
	return cleaned
}

func waitForGitHubWorkflowRun(ctx context.Context, client githubActionsClient, owner, repo string, runID int64) (githubWorkflowRun, error) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var last githubWorkflowRun
	for {
		run, err := client.GetWorkflowRun(ctx, owner, repo, runID)
		if err != nil {
			return last, err
		}
		last = run
		if strings.EqualFold(strings.TrimSpace(run.Status), "completed") {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-ticker.C:
		}
	}
}

func normalizeGitHubRunStatus(run githubWorkflowRun, timedOut bool, cancelled bool) (string, int) {
	if timedOut {
		return statusTimedOut, 1
	}
	if cancelled {
		return statusCancelled, 1
	}
	status := normalizeGitHubExecutionState(run.Status, run.Conclusion)
	switch status {
	case statusSuccess:
		return statusSuccess, 0
	case statusCancelled:
		return statusCancelled, 1
	case statusTimedOut:
		return statusTimedOut, 1
	default:
		return statusFailure, 1
	}
}

func normalizeGitHubJobs(jobs []githubWorkflowJob) map[string]WorkflowJobOutput {
	if len(jobs) == 0 {
		return nil
	}
	out := make(map[string]WorkflowJobOutput, len(jobs))
	for _, job := range jobs {
		key := sanitizeArtifactName(job.Name)
		if key == "" {
			key = fmt.Sprintf("job-%d", job.ID)
		}
		steps := make([]WorkflowStepOutput, 0, len(job.Steps))
		for _, step := range job.Steps {
			steps = append(steps, WorkflowStepOutput{
				Name:            step.Name,
				Status:          normalizeGitHubExecutionState(step.Status, step.Conclusion),
				Conclusion:      strings.TrimSpace(step.Conclusion),
				DurationSeconds: durationSeconds(step.StartedAt, step.CompletedAt),
			})
		}
		out[key] = WorkflowJobOutput{
			Name:            job.Name,
			Status:          normalizeGitHubExecutionState(job.Status, job.Conclusion),
			Conclusion:      strings.TrimSpace(job.Conclusion),
			DurationSeconds: durationSeconds(job.StartedAt, job.CompletedAt),
			Steps:           steps,
		}
	}
	return out
}

func normalizeGitHubExecutionState(status string, conclusion string) string {
	conclusion = strings.TrimSpace(strings.ToLower(conclusion))
	switch conclusion {
	case "", "null":
	case "success", "neutral", "skipped":
		return statusSuccess
	case "cancelled":
		return statusCancelled
	case "timed_out":
		return statusTimedOut
	default:
		return statusFailure
	}

	status = strings.TrimSpace(strings.ToLower(status))
	switch status {
	case "", "completed":
		return statusSuccess
	case "cancelled":
		return statusCancelled
	default:
		return statusFailure
	}
}

func githubRunDurationSeconds(run githubWorkflowRun) int {
	return durationSeconds(run.StartedAt, run.UpdatedAt)
}

func githubArtifactRefs(run githubWorkflowRun, artifacts []githubWorkflowArtifact) map[string]externalFileRef {
	total := len(artifacts)
	if strings.TrimSpace(run.LogsURL) != "" {
		total++
	}
	if total == 0 {
		return nil
	}
	refs := make(map[string]externalFileRef, total)
	if logsURL := strings.TrimSpace(run.LogsURL); logsURL != "" {
		refs["gha-logs"] = externalFileRef{URL: logsURL, Expand: true}
	}
	for _, artifact := range artifacts {
		if artifact.Expired {
			continue
		}
		name := sanitizeArtifactName(artifact.Name)
		if name == "" {
			continue
		}
		refs[name] = externalFileRef{URL: artifact.ArchiveDownloadURL, Expand: true}
	}
	return refs
}
