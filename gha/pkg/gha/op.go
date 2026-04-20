package gha

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflow"
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

func GetOp() coreops.RegisterableOp {
	return coreops.NewActivityMappedOpV2[RunInput, RunOutput](
		coreops.OpMetadata{
			Type:           "gha.run",
			Description:    "Executes a GitHub Actions workflow against an isolated snapshot of the current worktree",
			Version:        "1.0.0",
			DefaultTimeout: 30 * time.Minute,
		},
		run,
	)
}

func GetRunsOp() coreops.RegisterableOp {
	return coreops.NewActivityMappedOpV2[RunsInput, RunsOutput](
		coreops.OpMetadata{
			Type:           "gha.runs",
			Description:    "Executes multiple GitHub Actions workflows against isolated copies of the current worktree",
			Version:        "1.0.0",
			DefaultTimeout: 45 * time.Minute,
		},
		runs,
	)
}

func run(inv coreops.OpDependencies, ctx context.Context, input RunInput) (RunOutput, error) {
	result, err := executeRun(ctx, input, currentGitContext(inv))
	if addErr := addExternalArtifactRefs(inv, result.ArtifactRefs); addErr != nil {
		return RunOutput{}, addErr
	}
	if err != nil {
		return result.Output, err
	}
	return result.Output, nil
}

func runs(inv coreops.OpDependencies, ctx context.Context, input RunsInput) (RunsOutput, error) {
	if len(input.Workflows) == 0 {
		return RunsOutput{}, workflow.NewNonRetryableApplicationError("workflows is required")
	}

	gitCtx := currentGitContext(inv)
	if strings.TrimSpace(gitCtx.WorktreePath) == "" {
		return RunsOutput{}, workflow.NewNonRetryableApplicationError("worktree path is required")
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
	if err := addExternalArtifactRefs(inv, artifactRefs); err != nil {
		return RunsOutput{}, err
	}
	if firstErr != nil && !input.ContinueOnError {
		return output, firstErr
	}
	return output, nil
}

func executeRun(ctx context.Context, input RunInput, gitCtx coreops.GitExecutionContext) (backendResult, error) {
	workflowSelector := strings.TrimSpace(input.Workflow)
	if workflowSelector == "" {
		return backendResult{}, workflow.NewNonRetryableApplicationError("workflow is required")
	}

	if strings.TrimSpace(gitCtx.WorktreePath) == "" {
		return backendResult{}, workflow.NewNonRetryableApplicationError("worktree path is required")
	}

	resolved, err := resolveWorkflowSelector(workflowSelector, gitCtx)
	if err != nil {
		return backendResult{}, workflow.NewNonRetryableApplicationError("%s", err.Error())
	}
	if resolved.ResolvedCommit == "" {
		resolved.ResolvedCommit = resolvedCommit(gitCtx)
	}

	timeout, err := parseTimeout(input.Timeout)
	if err != nil {
		return backendResult{}, workflow.NewNonRetryableApplicationError("invalid timeout: %s", err.Error())
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	backend, err := backendFactory(strings.TrimSpace(input.Backend))
	if err != nil {
		return backendResult{}, workflow.NewNonRetryableApplicationError("%s", err.Error())
	}

	result, err := backend.Run(ctx, backendRequest{
		Input:      input,
		Workflow:   resolved,
		GitContext: gitCtx,
	})
	if err != nil {
		return backendResult{}, workflow.NewNonRetryableApplicationError("%s", err.Error())
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
		return result, workflow.NewNonRetryableApplicationError("%s", result.Output.ErrorMessage)
	}

	return result, nil
}

func currentGitContext(inv coreops.OpDependencies) coreops.GitExecutionContext {
	gitCtx := inv.GitContext()
	if gitCtx.WorktreePath == "" {
		gitCtx.WorktreePath = inv.WorktreePath()
	}
	return gitCtx
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
