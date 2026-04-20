package gha

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

type fakeGitHubClient struct {
	dispatch      githubDispatchResult
	run           githubWorkflowRun
	jobs          []githubWorkflowJob
	artifacts     []githubWorkflowArtifact
	defaultBranch string
	filesAtRef    map[string]map[string]bool
}

func (f *fakeGitHubClient) DispatchWorkflow(context.Context, string, string, string, string, map[string]any) (githubDispatchResult, error) {
	return f.dispatch, nil
}

func (f *fakeGitHubClient) GetWorkflowRun(context.Context, string, string, int64) (githubWorkflowRun, error) {
	return f.run, nil
}

func (f *fakeGitHubClient) ListWorkflowJobs(context.Context, string, string, int64) ([]githubWorkflowJob, error) {
	return f.jobs, nil
}

func (f *fakeGitHubClient) ListWorkflowRunArtifacts(context.Context, string, string, int64) ([]githubWorkflowArtifact, error) {
	return f.artifacts, nil
}

func (f *fakeGitHubClient) GetDefaultBranch(context.Context, string, string) (string, error) {
	return f.defaultBranch, nil
}

func (f *fakeGitHubClient) FileExistsAtRef(_ context.Context, _, _, path, ref string) (bool, error) {
	if f.filesAtRef == nil {
		return false, nil
	}
	return f.filesAtRef[ref][path], nil
}

type fakeGitHubGitRunner struct {
	pushedRemote string
	deletedRef   string
}

func (f *fakeGitHubGitRunner) pushRef(_ context.Context, _ string, remoteURL, remoteRef string) error {
	f.pushedRemote = remoteURL + " " + remoteRef
	return nil
}

func (f *fakeGitHubGitRunner) deleteRef(_ context.Context, _ string, remoteRef string) error {
	f.deletedRef = remoteRef
	return nil
}

func TestParseGitHubRemote(t *testing.T) {
	remote, err := parseGitHubRemote("git@github.com:col2test/ghatest.git")
	require.NoError(t, err)
	require.Equal(t, "github.com", remote.Host)
	require.Equal(t, "col2test", remote.Owner)
	require.Equal(t, "ghatest", remote.Repo)

	remote, err = parseGitHubRemote("https://github.com/col2test/ghatest.git")
	require.NoError(t, err)
	require.Equal(t, "github.com", remote.Host)
	require.Equal(t, "col2test", remote.Owner)
	require.Equal(t, "ghatest", remote.Repo)
}

func TestResolveGitHubRemoteFromNamedRemote(t *testing.T) {
	repo := t.TempDir()
	runGitCommand(t, repo, "init")
	runGitCommand(t, repo, "remote", "add", "origin", "git@github.com:col2test/ghatest.git")

	remote, err := resolveGitHubRemote(RunInput{
		Remote: &RemoteInput{PushTo: "origin"},
	}, coreops.GitExecutionContext{
		WorktreePath: repo,
	})
	require.NoError(t, err)
	require.Equal(t, "git@github.com:col2test/ghatest.git", remote.PushURL)
	require.Equal(t, "col2test", remote.Owner)
	require.Equal(t, "ghatest", remote.Repo)
}

func TestGitHubBackendRunRegistersArtifactURLs(t *testing.T) {
	worktree := t.TempDir()
	workflowPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("name: ci\n"), 0o644))

	fakeClient := &fakeGitHubClient{
		defaultBranch: "main",
		filesAtRef: map[string]map[string]bool{
			"main": {
				".github/workflows/ci.yml": true,
			},
		},
		dispatch: githubDispatchResult{RunID: 99},
		run: githubWorkflowRun{
			ID:         99,
			Status:     "completed",
			Conclusion: "success",
			LogsURL:    "https://api.github.com/repos/col2test/ghatest/actions/runs/99/logs",
			StartedAt:  time.Unix(10, 0),
			UpdatedAt:  time.Unix(20, 0),
		},
		jobs: []githubWorkflowJob{
			{
				ID:          1,
				Name:        "smoke",
				Status:      "completed",
				Conclusion:  "success",
				StartedAt:   time.Unix(10, 0),
				CompletedAt: time.Unix(20, 0),
				Steps: []githubWorkflowStep{
					{Name: "Echo", Status: "completed", Conclusion: "success", StartedAt: time.Unix(10, 0), CompletedAt: time.Unix(12, 0)},
				},
			},
		},
		artifacts: []githubWorkflowArtifact{
			{Name: "test-results", ArchiveDownloadURL: "https://api.github.com/repos/col2test/ghatest/actions/artifacts/123/zip"},
		},
	}
	fakeGit := &fakeGitHubGitRunner{}

	origClientFactory := githubClientFactory
	origGitOps := githubGitOps
	githubClientFactory = func(host, token string) (githubActionsClient, error) {
		require.Equal(t, "github.com", host)
		require.Equal(t, "token", token)
		return fakeClient, nil
	}
	githubGitOps = fakeGit
	t.Cleanup(func() {
		githubClientFactory = origClientFactory
		githubGitOps = origGitOps
	})

	result, err := (&githubBackend{}).Run(context.Background(), backendRequest{
		Input: RunInput{
			Workflow: "ci.yml",
			Backend:  backendGitHub,
			Secrets:  map[string]string{"GITHUB_TOKEN": "token"},
			Remote:   &RemoteInput{PushTo: "git@github.com:col2test/ghatest.git"},
		},
		Workflow: resolvedWorkflow{
			Selector:       "ci.yml",
			Path:           workflowPath,
			RepoPath:       ".github/workflows/ci.yml",
			ContentHash:    "sha256:abc123",
			ResolvedCommit: "deadbeef",
		},
		GitContext: coreops.GitExecutionContext{
			BaseRepo:         "col2test/ghatest",
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			InvokeHash:       "invoke1234567890",
			WorktreePath:     worktree,
		},
	})
	require.NoError(t, err)
	require.Equal(t, statusSuccess, result.Output.Status)
	require.Equal(t, 10, result.Output.DurationSeconds)
	require.Contains(t, result.Output.Jobs, "smoke")
	require.Equal(t, "https://api.github.com/repos/col2test/ghatest/actions/runs/99/logs", result.ArtifactRefs["gha-logs"].URL)
	require.True(t, result.ArtifactRefs["gha-logs"].Expand)
	require.Equal(t, "https://api.github.com/repos/col2test/ghatest/actions/artifacts/123/zip", result.ArtifactRefs["test-results"].URL)
	require.Contains(t, fakeGit.pushedRemote, "refs/heads/c2/gha/invoke1234567890")
	require.Equal(t, "refs/heads/c2/gha/invoke1234567890", fakeGit.deletedRef)
}

func TestGitHubBackendLeavesLocalWorktreeUnchanged(t *testing.T) {
	worktree := t.TempDir()
	workflowPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	targetFile := filepath.Join(worktree, "result.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("name: ci\n"), 0o644))
	require.NoError(t, os.WriteFile(targetFile, []byte("before"), 0o644))

	fakeClient := &fakeGitHubClient{
		defaultBranch: "main",
		filesAtRef: map[string]map[string]bool{
			"main": {
				".github/workflows/ci.yml": true,
			},
		},
		dispatch: githubDispatchResult{RunID: 100},
		run: githubWorkflowRun{
			ID:         100,
			Status:     "completed",
			Conclusion: "success",
		},
	}
	fakeGit := &fakeGitHubGitRunner{}

	origClientFactory := githubClientFactory
	origGitOps := githubGitOps
	githubClientFactory = func(string, string) (githubActionsClient, error) {
		return fakeClient, nil
	}
	githubGitOps = fakeGit
	t.Cleanup(func() {
		githubClientFactory = origClientFactory
		githubGitOps = origGitOps
	})

	_, err := (&githubBackend{}).Run(context.Background(), backendRequest{
		Input: RunInput{
			Workflow: "ci.yml",
			Backend:  backendGitHub,
			Secrets:  map[string]string{"GITHUB_TOKEN": "token"},
			Remote:   &RemoteInput{PushTo: "git@github.com:col2test/ghatest.git"},
		},
		Workflow: resolvedWorkflow{
			Selector:       "ci.yml",
			Path:           workflowPath,
			RepoPath:       ".github/workflows/ci.yml",
			ContentHash:    "sha256:abc123",
			ResolvedCommit: "deadbeef",
		},
		GitContext: coreops.GitExecutionContext{
			InvokeHash:   "invoke123",
			WorktreePath: worktree,
		},
	})
	require.NoError(t, err)
	data, err := os.ReadFile(targetFile)
	require.NoError(t, err)
	require.Equal(t, "before", string(data))
}

func TestGitHubBackendRequiresWorkflowOnDefaultBranch(t *testing.T) {
	fakeClient := &fakeGitHubClient{
		defaultBranch: "main",
		filesAtRef: map[string]map[string]bool{
			"main": {},
		},
	}
	fakeGit := &fakeGitHubGitRunner{}

	origClientFactory := githubClientFactory
	origGitOps := githubGitOps
	githubClientFactory = func(string, string) (githubActionsClient, error) {
		return fakeClient, nil
	}
	githubGitOps = fakeGit
	t.Cleanup(func() {
		githubClientFactory = origClientFactory
		githubGitOps = origGitOps
	})

	_, err := (&githubBackend{}).Run(context.Background(), backendRequest{
		Input: RunInput{
			Workflow: "ci.yml",
			Backend:  backendGitHub,
			Secrets:  map[string]string{"GITHUB_TOKEN": "token"},
			Remote:   &RemoteInput{PushTo: "git@github.com:col2test/ghatest.git"},
		},
		Workflow: resolvedWorkflow{
			Selector: "ci.yml",
			RepoPath: ".github/workflows/ci.yml",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must exist on default branch")
}

func TestNormalizeGitHubExecutionState(t *testing.T) {
	require.Equal(t, statusSuccess, normalizeGitHubExecutionState("completed", "success"))
	require.Equal(t, statusSuccess, normalizeGitHubExecutionState("completed", "skipped"))
	require.Equal(t, statusCancelled, normalizeGitHubExecutionState("completed", "cancelled"))
	require.Equal(t, statusTimedOut, normalizeGitHubExecutionState("completed", "timed_out"))
	require.Equal(t, statusFailure, normalizeGitHubExecutionState("in_progress", ""))
}

func runGitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(output))
}
