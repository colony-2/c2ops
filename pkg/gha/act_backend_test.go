package gha

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestActBackendRunIntegration(t *testing.T) {
	require.NoError(t, ensureDockerAvailable())

	worktree := initGitRepoWithFiles(t, map[string]string{
		".github/workflows/ci.yml": `
name: ci
on:
  workflow_dispatch:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: Write file
        run: |
          echo hello > output.txt
      - name: Read file
        run: |
          cat output.txt
`,
	})
	workflowPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	outputPath := filepath.Join(worktree, "output.txt")

	result, err := (&actBackend{}).Run(context.Background(), backendRequest{
		Input: RunInput{
			Workflow: "ci.yml",
		},
		Workflow: resolvedWorkflow{
			Selector:       "ci.yml",
			Path:           workflowPath,
			RepoPath:       ".github/workflows/ci.yml",
			ContentHash:    contentHash([]byte("name: ci")),
			ResolvedCommit: "deadbeef",
		},
		GitContext: coreops.GitExecutionContext{
			BaseRepo:         "acme/widgets",
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.NoError(t, err)
	require.Equal(t, statusSuccess, result.Output.Status)
	require.Contains(t, result.Output.Jobs, "test")
	require.Contains(t, result.ArtifactRefs, "gha-logs")
	require.FileExists(t, result.ArtifactRefs["gha-logs"].Path)
	_, err = os.Stat(outputPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestActBackendRunIntegrationWithCheckout(t *testing.T) {
	require.NoError(t, ensureDockerAvailable())

	worktree := initGitRepoWithFiles(t, map[string]string{
		".github/workflows/ci.yml": `
name: ci
on:
  workflow_dispatch:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Read tracked file
        run: cat README.md
`,
		"README.md": "hello from checkout\n",
	})
	workflowPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")

	result, err := (&actBackend{}).Run(context.Background(), backendRequest{
		Input: RunInput{
			Workflow: "ci.yml",
		},
		Workflow: resolvedWorkflow{
			Selector:       "ci.yml",
			Path:           workflowPath,
			RepoPath:       ".github/workflows/ci.yml",
			ContentHash:    contentHash([]byte("name: ci")),
			ResolvedCommit: "deadbeef",
		},
		GitContext: coreops.GitExecutionContext{
			BaseRepo:         "acme/widgets",
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.NoError(t, err)
	require.Equal(t, statusSuccess, result.Output.Status)
}
