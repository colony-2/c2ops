package gha

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestGitHubBackendRunLive(t *testing.T) {
	if os.Getenv("GHA_LIVE_TEST") == "" {
		t.Skip("set GHA_LIVE_TEST=1 to enable live GitHub backend coverage")
	}

	token := strings.TrimSpace(os.Getenv("GHA_TEST_GITHUB_TOKEN"))
	if token == "" {
		t.Skip("set GHA_TEST_GITHUB_TOKEN to run the live GitHub backend test")
	}
	remoteURL := strings.TrimSpace(os.Getenv("GHA_TEST_GITHUB_REMOTE"))
	if remoteURL == "" {
		t.Skip("set GHA_TEST_GITHUB_REMOTE to run the live GitHub backend test")
	}

	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "repo")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	_, err := runGit(ctx, "", "clone", remoteURL, repoDir)
	require.NoError(t, err)

	head, err := runGit(ctx, repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	head = strings.TrimSpace(head)

	workflowPath := filepath.Join(repoDir, ".github", "workflows", "c2-gha-live.yml")
	require.FileExists(t, workflowPath)

	result, err := (&githubBackend{}).Run(ctx, backendRequest{
		Input: RunInput{
			Workflow: "c2-gha-live.yml",
			Backend:  backendGitHub,
			With: map[string]any{
				"message": "hello-live",
			},
			Secrets: map[string]string{
				"GITHUB_TOKEN": token,
			},
			Remote: &RemoteInput{
				PushTo:    remoteURL,
				RefPrefix: "c2/gha/live",
			},
		},
		Workflow: resolvedWorkflow{
			Selector:       "c2-gha-live.yml",
			Path:           workflowPath,
			RepoPath:       ".github/workflows/c2-gha-live.yml",
			ContentHash:    contentHash([]byte("live")),
			ResolvedCommit: head,
		},
		GitContext: coreops.GitExecutionContext{
			BaseRepo:         "col2test/ghatest",
			BaseRef:          "main",
			ResolvedBaseHash: head,
			InvokeHash:       "live-backend-test",
			WorktreePath:     repoDir,
		},
	})
	require.NoError(t, err)
	require.Equal(t, statusSuccess, result.Output.Status)
	require.Contains(t, result.Output.Jobs, "smoke")
	require.Equal(t, statusSuccess, result.Output.Jobs["smoke"].Status)
	require.Contains(t, result.ArtifactRefs, "test-results")
	require.Contains(t, result.ArtifactRefs, "gha-logs")
	require.NotEmpty(t, result.ArtifactRefs["test-results"].URL)
	require.NotEmpty(t, result.ArtifactRefs["gha-logs"].URL)
}
