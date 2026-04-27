package gha

import (
	"context"
)

const (
	backendLocal  = "local"
	backendGitHub = "github"

	statusSuccess   = "success"
	statusFailure   = "failure"
	statusCancelled = "cancelled"
	statusTimedOut  = "timed_out"
)

type RunInput struct {
	Workflow              string            `json:"workflow" validate:"required"`
	With                  map[string]any    `json:"with,omitempty"`
	Env                   map[string]string `json:"env,omitempty"`
	Secrets               map[string]string `json:"secrets,omitempty"`
	Backend               string            `json:"backend,omitempty"`
	RunnerImage           string            `json:"runner_image,omitempty"`
	ContainerArchitecture string            `json:"container_architecture,omitempty"`
	Timeout               string            `json:"timeout,omitempty"`
	ContinueOnError       bool              `json:"continue_on_error,omitempty"`
	Remote                *RemoteInput      `json:"remote,omitempty"`
	GitContext            GitContext        `json:"git_context,omitempty"`
}

type RunsInput struct {
	Workflows       []RunsWorkflowInput `json:"workflows" validate:"required"`
	Timeout         string              `json:"timeout,omitempty"`
	ContinueOnError bool                `json:"continue_on_error,omitempty"`
	GitContext      GitContext          `json:"git_context,omitempty"`
}

type RunsWorkflowInput struct {
	ID       string `json:"id,omitempty"`
	RunInput `json:",inline"`
}

type RunsOutput struct {
	Status       string               `json:"status"`
	AllPassed    bool                 `json:"all_passed"`
	ErrorMessage string               `json:"error_message,omitempty"`
	Results      map[string]RunOutput `json:"results,omitempty"`
}

type RemoteInput struct {
	PushTo    string `json:"push_to,omitempty"`
	RefPrefix string `json:"ref_prefix,omitempty"`
}

type RunOutput struct {
	Status          string                       `json:"status"`
	ExitCode        int                          `json:"exit_code"`
	DurationSeconds int                          `json:"duration_seconds"`
	ErrorMessage    string                       `json:"error_message,omitempty"`
	Workflow        WorkflowOutput               `json:"workflow"`
	Jobs            map[string]WorkflowJobOutput `json:"jobs,omitempty"`
}

type WorkflowOutput struct {
	ResolvedSelector string `json:"resolved_selector"`
	ResolvedCommit   string `json:"resolved_commit,omitempty"`
	ContentHash      string `json:"content_hash,omitempty"`
}

type WorkflowJobOutput struct {
	Name            string                 `json:"name,omitempty"`
	Status          string                 `json:"status"`
	Conclusion      string                 `json:"conclusion,omitempty"`
	DurationSeconds int                    `json:"duration_seconds"`
	Matrix          map[string]interface{} `json:"matrix,omitempty"`
	Steps           []WorkflowStepOutput   `json:"steps,omitempty"`
}

type WorkflowStepOutput struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	Conclusion      string `json:"conclusion,omitempty"`
	DurationSeconds int    `json:"duration_seconds"`
}

type GitContext struct {
	BaseRepo         string `json:"base_repo,omitempty"`
	BaseRef          string `json:"base_ref,omitempty"`
	ResolvedBaseHash string `json:"resolved_base_hash,omitempty"`
	PersistHash      string `json:"persist_hash,omitempty"`
	ParentHash       string `json:"parent_hash,omitempty"`
	InvokeHash       string `json:"invoke_hash,omitempty"`
	WorktreePath     string `json:"worktree_path,omitempty"`
}

type resolvedWorkflow struct {
	Selector       string
	Path           string
	RepoPath       string
	ContentHash    string
	ResolvedCommit string
}

type externalFileRef struct {
	Path   string
	URL    string
	Expand bool
}

type ExternalArtifact struct {
	URL    string `json:"url"`
	Expand bool   `json:"expand,omitempty"`
}

type ArtifactRef struct {
	Kind     string            `json:"kind"`
	Name     string            `json:"name,omitempty"`
	External *ExternalArtifact `json:"external,omitempty"`
}

type backendRequest struct {
	Input      RunInput
	Workflow   resolvedWorkflow
	GitContext GitContext
}

type backendResult struct {
	Output       RunOutput
	ArtifactRefs map[string]externalFileRef
}

type RunResult struct {
	Output       RunOutput
	ArtifactRefs map[string]ArtifactRef
}

type RunsResult struct {
	Output       RunsOutput
	ArtifactRefs map[string]ArtifactRef
}

type workflowBackend interface {
	Run(context.Context, backendRequest) (backendResult, error)
}
