package gha

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var backendFactory = func(name string) (workflowBackend, error) {
	switch strings.TrimSpace(name) {
	case "", backendLocal:
		return actBackendFactory(), nil
	case backendGitHub:
		return &githubBackend{}, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q", name)
	}
}

func Run(ctx context.Context, input RunInput) (RunResult, error) {
	result, err := executeRun(ctx, input, input.GitContext)
	artifactRefs, artifactErr := buildArtifactRefs(result.ArtifactRefs)
	if artifactErr != nil {
		return RunResult{}, artifactErr
	}
	if err != nil {
		return RunResult{Output: result.Output, ArtifactRefs: artifactRefs}, err
	}
	return RunResult{Output: result.Output, ArtifactRefs: artifactRefs}, nil
}

func RunBatch(ctx context.Context, input RunsInput) (RunsResult, error) {
	if len(input.Workflows) == 0 {
		return RunsResult{}, fmt.Errorf("workflows is required")
	}

	gitCtx := input.GitContext
	if strings.TrimSpace(gitCtx.WorktreePath) == "" {
		return RunsResult{}, fmt.Errorf("git_context.worktree_path is required")
	}

	results := make(map[string]RunOutput, len(input.Workflows))
	artifactRefs := make(map[string]externalFileRef)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	var firstErrMu sync.Mutex

	for i, workflowInput := range input.Workflows {
		workflowInput := workflowInput
		index := i
		wg.Add(1)
		go func() {
			defer wg.Done()

			key := batchWorkflowKey(workflowInput, index)
			clonedWorktree, err := cloneGitWorktree(ctx, gitCtx.WorktreePath)
			if err != nil {
				mu.Lock()
				results[key] = failureOutputForError(RunOutput{}, err)
				mu.Unlock()
				recordBatchRunError(&firstErrMu, &firstErr, input.ContinueOnError, err)
				return
			}
			defer os.RemoveAll(clonedWorktree)

			itemInput := workflowInput.RunInput
			if strings.TrimSpace(itemInput.Timeout) == "" {
				itemInput.Timeout = input.Timeout
			}

			itemGitCtx := gitCtx
			itemGitCtx.WorktreePath = clonedWorktree

			result, err := executeRun(ctx, itemInput, itemGitCtx)
			if err != nil {
				failureResult := failureOutputForError(result.Output, err)
				prefixedArtifacts := prefixArtifactRefs(key, result.ArtifactRefs)
				mu.Lock()
				results[key] = failureResult
				for name, ref := range prefixedArtifacts {
					artifactRefs[name] = ref
				}
				mu.Unlock()
				recordBatchRunError(&firstErrMu, &firstErr, input.ContinueOnError, err)
				return
			}

			prefixedArtifacts := prefixArtifactRefs(key, result.ArtifactRefs)

			mu.Lock()
			results[key] = result.Output
			for name, ref := range prefixedArtifacts {
				artifactRefs[name] = ref
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	output := buildRunsOutput(results, input.ContinueOnError)
	builtRefs, err := buildArtifactRefs(artifactRefs)
	if err != nil {
		return RunsResult{}, err
	}
	if firstErr != nil && !input.ContinueOnError {
		return RunsResult{Output: output, ArtifactRefs: builtRefs}, firstErr
	}
	return RunsResult{Output: output, ArtifactRefs: builtRefs}, nil
}

func executeRun(ctx context.Context, input RunInput, gitCtx GitContext) (backendResult, error) {
	workflowSelector := strings.TrimSpace(input.Workflow)
	if workflowSelector == "" {
		return backendResult{}, fmt.Errorf("workflow is required")
	}

	if strings.TrimSpace(gitCtx.WorktreePath) == "" {
		return backendResult{}, fmt.Errorf("git_context.worktree_path is required")
	}

	resolved, err := resolveWorkflowSelector(workflowSelector, gitCtx)
	if err != nil {
		return backendResult{}, err
	}
	if resolved.ResolvedCommit == "" {
		resolved.ResolvedCommit = resolvedCommit(gitCtx)
	}

	timeout, err := parseTimeout(input.Timeout)
	if err != nil {
		return backendResult{}, fmt.Errorf("invalid timeout: %s", err.Error())
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	backend, err := backendFactory(strings.TrimSpace(input.Backend))
	if err != nil {
		return backendResult{}, err
	}

	result, err := backend.Run(ctx, backendRequest{
		Input:      input,
		Workflow:   resolved,
		GitContext: gitCtx,
	})
	if err != nil {
		return backendResult{}, err
	}

	if result.Output.Workflow.ResolvedSelector == "" {
		result.Output.Workflow = WorkflowOutput{
			ResolvedSelector: resolved.Selector,
			ResolvedCommit:   resolved.ResolvedCommit,
			ContentHash:      resolved.ContentHash,
		}
	}

	if result.Output.Status != statusSuccess && !input.ContinueOnError && strings.TrimSpace(result.Output.ErrorMessage) == "" {
		result.Output.ErrorMessage = fmt.Sprintf("workflow concluded with status %s", result.Output.Status)
	}
	if result.Output.Status != statusSuccess && !input.ContinueOnError {
		return result, fmt.Errorf("%s", result.Output.ErrorMessage)
	}

	return result, nil
}

func recordBatchRunError(mu *sync.Mutex, target *error, continueOnError bool, err error) {
	if continueOnError || err == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if *target == nil {
		*target = err
	}
}

func batchWorkflowKey(input RunsWorkflowInput, index int) string {
	key := sanitizeArtifactName(strings.TrimSpace(input.ID))
	if key != "" {
		return key
	}
	workflowPath := strings.TrimSpace(input.Workflow)
	if workflowPath != "" {
		base := filepath.Base(workflowPath)
		key = sanitizeArtifactName(strings.TrimSuffix(base, filepath.Ext(base)))
	}
	if key == "" {
		key = fmt.Sprintf("workflow-%d", index+1)
	}
	return key
}

func prefixArtifactRefs(prefix string, refs map[string]externalFileRef) map[string]externalFileRef {
	if len(refs) == 0 {
		return nil
	}
	out := make(map[string]externalFileRef, len(refs))
	for name, ref := range refs {
		out[filepath.ToSlash(filepath.Join(prefix, name))] = ref
	}
	return out
}

func buildRunsOutput(results map[string]RunOutput, continueOnError bool) RunsOutput {
	allPassed := true
	for _, result := range results {
		if result.Status != statusSuccess {
			allPassed = false
			break
		}
	}

	output := RunsOutput{
		Status:    statusSuccess,
		AllPassed: allPassed,
		Results:   results,
	}
	if !allPassed {
		output.Status = statusFailure
		if !continueOnError {
			output.ErrorMessage = "one or more workflows failed"
		}
	}
	return output
}

func failureOutputForError(output RunOutput, err error) RunOutput {
	if output.Status == "" {
		output.Status = statusFailure
	}
	if output.ExitCode == 0 && output.Status != statusSuccess {
		output.ExitCode = 1
	}
	if strings.TrimSpace(output.ErrorMessage) == "" && err != nil {
		output.ErrorMessage = err.Error()
	}
	return output
}

func cloneGitWorktree(ctx context.Context, src string) (string, error) {
	dst, err := os.MkdirTemp("", "c2-gha-run-*")
	if err != nil {
		return "", err
	}
	if _, err := runGit(ctx, "", "clone", "--quiet", "--no-hardlinks", src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return "", err
	}
	return dst, nil
}

func parseTimeout(raw string) (time.Duration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 30 * time.Minute, nil
	}
	return time.ParseDuration(trimmed)
}
