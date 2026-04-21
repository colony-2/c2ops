package gha

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeBackend struct {
	result  backendResult
	err     error
	request backendRequest
}

func (f *fakeBackend) Run(_ context.Context, req backendRequest) (backendResult, error) {
	f.request = req
	return f.result, f.err
}

type workflowBackendFunc func(context.Context, backendRequest) (backendResult, error)

func (f workflowBackendFunc) Run(ctx context.Context, req backendRequest) (backendResult, error) {
	return f(ctx, req)
}

func TestResolveWorkflowSelectorFileNameOnly(t *testing.T) {
	worktree := t.TempDir()
	repoWorkflow := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoWorkflow), 0o755))
	require.NoError(t, os.WriteFile(repoWorkflow, []byte("name: repo\non:\n  workflow_dispatch:\n"), 0o644))

	resolvedRepo, err := resolveWorkflowSelector("ci.yml", GitContext{
		WorktreePath:     worktree,
		ResolvedBaseHash: "deadbeef",
	})
	require.NoError(t, err)
	require.Equal(t, repoWorkflow, resolvedRepo.Path)
	require.Equal(t, ".github/workflows/ci.yml", resolvedRepo.RepoPath)
	require.Equal(t, "ci.yml", resolvedRepo.Selector)
	require.NotEmpty(t, resolvedRepo.ContentHash)
}

func TestResolveWorkflowSelectorRejectsUnsupportedNames(t *testing.T) {
	worktree := t.TempDir()
	workflowPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("name: repo\non:\n  workflow_dispatch:\n"), 0o644))

	gitCtx := GitContext{WorktreePath: worktree}

	for _, input := range []string{
		"repo://.github/workflows/ci.yml",
		"cell://workflows/ci.yml",
		"git+https://github.com/acme/repo.git//.github/workflows/ci.yml@main",
		"nested/ci.yml",
		`nested\ci.yml`,
		"ci.txt",
	} {
		_, err := resolveWorkflowSelector(input, gitCtx)
		require.Error(t, err, input)
	}
}

func TestResolveWorkflowSelectorRequiresWorkflowDispatch(t *testing.T) {
	worktree := t.TempDir()
	workflowPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("name: repo\non:\n  push:\n"), 0o644))

	_, err := resolveWorkflowSelector("ci.yml", GitContext{WorktreePath: worktree})
	require.Error(t, err)
	require.Contains(t, err.Error(), "workflow_dispatch")
}

func TestRunBuildsArtifactRefsAndPassesResolvedWorkflowToBackend(t *testing.T) {
	worktree := t.TempDir()
	workflowPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	logPath := filepath.Join(worktree, "gha.log")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("name: ci\non:\n  workflow_dispatch:\n"), 0o644))
	require.NoError(t, os.WriteFile(logPath, []byte("hello"), 0o644))

	fake := &fakeBackend{
		result: backendResult{
			Output: RunOutput{
				Status: statusSuccess,
				Jobs: map[string]WorkflowJobOutput{
					"test": {Status: statusSuccess},
				},
			},
			ArtifactRefs: map[string]externalFileRef{
				"gha-logs": {Path: logPath},
			},
		},
	}
	origFactory := backendFactory
	backendFactory = func(name string) (workflowBackend, error) {
		require.Equal(t, backendLocal, name)
		return fake, nil
	}
	t.Cleanup(func() {
		backendFactory = origFactory
	})

	result, err := Run(context.Background(), RunInput{
		Workflow: "ci.yml",
		Backend:  backendLocal,
		GitContext: GitContext{
			BaseRepo:         "acme/widgets",
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.NoError(t, err)
	require.Equal(t, statusSuccess, result.Output.Status)
	require.Equal(t, "ci.yml", result.Output.Workflow.ResolvedSelector)
	require.Equal(t, "deadbeef", result.Output.Workflow.ResolvedCommit)
	require.NotEmpty(t, result.Output.Workflow.ContentHash)
	require.Equal(t, workflowPath, fake.request.Workflow.Path)
	require.Equal(t, ".github/workflows/ci.yml", fake.request.Workflow.RepoPath)
	require.Equal(t, "acme/widgets", fake.request.GitContext.BaseRepo)

	refs := result.ArtifactRefs
	require.Contains(t, refs, "gha-logs")
	expectedURL, err := fileURL(logPath)
	require.NoError(t, err)
	require.Equal(t, expectedURL, refs["gha-logs"].External.URL)
}

func TestRunFailureAnnotatesOutputWhenContinueOnErrorIsFalse(t *testing.T) {
	worktree := t.TempDir()
	workflowPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("name: ci\non:\n  workflow_dispatch:\n"), 0o644))

	fake := &fakeBackend{
		result: backendResult{
			Output: RunOutput{
				Status: statusFailure,
				Jobs: map[string]WorkflowJobOutput{
					"test": {Status: statusFailure},
				},
			},
		},
	}
	origFactory := backendFactory
	backendFactory = func(string) (workflowBackend, error) {
		return fake, nil
	}
	t.Cleanup(func() {
		backendFactory = origFactory
	})

	result, err := Run(context.Background(), RunInput{
		Workflow: "ci.yml",
		Backend:  backendLocal,
		GitContext: GitContext{
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.Error(t, err)
	require.Equal(t, statusFailure, result.Output.Status)
	require.Contains(t, result.Output.ErrorMessage, "workflow concluded with status")

	result, err = Run(context.Background(), RunInput{
		Workflow:        "ci.yml",
		Backend:         backendLocal,
		ContinueOnError: true,
		GitContext: GitContext{
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.NoError(t, err)
	require.Equal(t, statusFailure, result.Output.Status)
	require.Empty(t, result.Output.ErrorMessage)
}

func TestRunBatchFailureReturnsStructuredOutputWhenContinueOnErrorIsFalse(t *testing.T) {
	worktree := initGitRepoWithFiles(t, map[string]string{
		".github/workflows/ci.yml": "name: ci\non:\n  workflow_dispatch:\n",
	})

	origFactory := backendFactory
	backendFactory = func(string) (workflowBackend, error) {
		return &fakeBackend{
			result: backendResult{
				Output: RunOutput{
					Status:       statusFailure,
					ExitCode:     1,
					ErrorMessage: "lint failed",
				},
			},
		}, nil
	}
	t.Cleanup(func() {
		backendFactory = origFactory
	})

	result, err := RunBatch(context.Background(), RunsInput{
		Workflows: []RunsWorkflowInput{
			{ID: "ci", RunInput: RunInput{Workflow: "ci.yml", Backend: backendLocal}},
		},
		GitContext: GitContext{
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.Error(t, err)
	require.Equal(t, statusFailure, result.Output.Status)
	require.False(t, result.Output.AllPassed)
	require.Equal(t, statusFailure, result.Output.Results["ci"].Status)
	require.Equal(t, "lint failed", result.Output.Results["ci"].ErrorMessage)
}

func TestRunSelectsGitHubBackend(t *testing.T) {
	worktree := t.TempDir()
	workflowPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("name: ci\non:\n  workflow_dispatch:\n"), 0o644))

	fake := &fakeBackend{
		result: backendResult{
			Output: RunOutput{
				Status: statusSuccess,
			},
		},
	}
	origFactory := backendFactory
	backendFactory = func(name string) (workflowBackend, error) {
		require.Equal(t, backendGitHub, name)
		return fake, nil
	}
	t.Cleanup(func() {
		backendFactory = origFactory
	})

	result, err := Run(context.Background(), RunInput{
		Workflow: "ci.yml",
		Backend:  backendGitHub,
		GitContext: GitContext{
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.NoError(t, err)
	require.Equal(t, statusSuccess, result.Output.Status)
}

func TestRunRejectsLegacyActBackendName(t *testing.T) {
	worktree := t.TempDir()
	workflowPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("name: ci\non:\n  workflow_dispatch:\n"), 0o644))

	result, err := Run(context.Background(), RunInput{
		Workflow: "ci.yml",
		Backend:  "act",
		GitContext: GitContext{
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.Error(t, err)
	require.Empty(t, result.Output.Status)
	require.Contains(t, err.Error(), `unsupported backend "act"`)
}

func TestRunBatchAggregatesResultsAndPrefixesArtifacts(t *testing.T) {
	worktree := initGitRepoWithFiles(t, map[string]string{
		".github/workflows/ci.yml": "name: ci\non:\n  workflow_dispatch:\n",
	})
	logPath := filepath.Join(t.TempDir(), "combined.log")
	require.NoError(t, os.WriteFile(logPath, []byte("hello"), 0o644))

	origFactory := backendFactory
	backendFactory = func(string) (workflowBackend, error) {
		return &fakeBackend{
			result: backendResult{
				Output: RunOutput{
					Status: statusSuccess,
					Jobs: map[string]WorkflowJobOutput{
						"test": {Status: statusSuccess},
					},
				},
				ArtifactRefs: map[string]externalFileRef{
					"gha-logs": {Path: logPath},
				},
			},
		}, nil
	}
	t.Cleanup(func() {
		backendFactory = origFactory
	})

	result, err := RunBatch(context.Background(), RunsInput{
		Workflows: []RunsWorkflowInput{
			{ID: "ci", RunInput: RunInput{Workflow: "ci.yml", Backend: backendLocal}},
			{ID: "lint", RunInput: RunInput{Workflow: "ci.yml", Backend: backendLocal}},
		},
		ContinueOnError: true,
		GitContext: GitContext{
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.NoError(t, err)
	require.Equal(t, statusSuccess, result.Output.Status)
	require.True(t, result.Output.AllPassed)
	require.Contains(t, result.Output.Results, "ci")
	require.Contains(t, result.Output.Results, "lint")
	require.Contains(t, result.ArtifactRefs, "ci/gha-logs")
	require.Contains(t, result.ArtifactRefs, "lint/gha-logs")
}

func TestRunBatchContinueOnErrorCapturesPerWorkflowErrors(t *testing.T) {
	worktree := initGitRepoWithFiles(t, map[string]string{
		".github/workflows/ci.yml": "name: ci\non:\n  workflow_dispatch:\n",
	})

	origFactory := backendFactory
	backendFactory = func(string) (workflowBackend, error) {
		return &fakeBackend{
			result: backendResult{
				Output: RunOutput{
					Status: statusSuccess,
				},
			},
		}, nil
	}
	t.Cleanup(func() {
		backendFactory = origFactory
	})

	result, err := RunBatch(context.Background(), RunsInput{
		Workflows: []RunsWorkflowInput{
			{ID: "good", RunInput: RunInput{Workflow: "ci.yml", Backend: backendLocal}},
			{ID: "bad", RunInput: RunInput{Workflow: "missing.yml", Backend: backendLocal}},
		},
		ContinueOnError: true,
		GitContext: GitContext{
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.NoError(t, err)
	require.Equal(t, statusFailure, result.Output.Status)
	require.False(t, result.Output.AllPassed)
	require.Equal(t, statusSuccess, result.Output.Results["good"].Status)
	require.Equal(t, statusFailure, result.Output.Results["bad"].Status)
	require.NotEmpty(t, result.Output.Results["bad"].ErrorMessage)
}

func TestRunBatchDiscardsWorkflowMutations(t *testing.T) {
	worktree := initGitRepoWithFiles(t, map[string]string{
		".github/workflows/ci.yml": "name: ci\non:\n  workflow_dispatch:\n",
	})
	mutatedPath := filepath.Join(worktree, "mutated.txt")

	origFactory := backendFactory
	backendFactory = func(string) (workflowBackend, error) {
		return workflowBackendFunc(func(_ context.Context, req backendRequest) (backendResult, error) {
			target := filepath.Join(req.GitContext.WorktreePath, "mutated.txt")
			require.NoError(t, os.WriteFile(target, []byte("changed"), 0o644))
			return backendResult{Output: RunOutput{Status: statusSuccess}}, nil
		}), nil
	}
	t.Cleanup(func() {
		backendFactory = origFactory
	})

	result, err := RunBatch(context.Background(), RunsInput{
		Workflows: []RunsWorkflowInput{
			{ID: "mutating", RunInput: RunInput{Workflow: "ci.yml", Backend: backendLocal}},
		},
		ContinueOnError: true,
		GitContext: GitContext{
			BaseRef:          "main",
			ResolvedBaseHash: "deadbeef",
			WorktreePath:     worktree,
		},
	})
	require.NoError(t, err)
	require.Equal(t, statusSuccess, result.Output.Status)
	require.Equal(t, statusSuccess, result.Output.Results["mutating"].Status)
	_, statErr := os.Stat(mutatedPath)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestBatchWorkflowKeyFallsBackWhenIDMissing(t *testing.T) {
	key := batchWorkflowKey(RunsWorkflowInput{
		RunInput: RunInput{Workflow: "ci.yml"},
	}, 0)
	require.Equal(t, "ci", key)
}
