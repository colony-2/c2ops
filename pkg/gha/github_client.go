package gha

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v84/github"
)

type githubDispatchResult struct {
	RunID   int64
	RunURL  string
	HTMLURL string
}

type githubWorkflowRun struct {
	ID           int64
	Status       string
	Conclusion   string
	HeadSHA      string
	RunURL       string
	HTMLURL      string
	LogsURL      string
	ArtifactsURL string
	CreatedAt    time.Time
	StartedAt    time.Time
	UpdatedAt    time.Time
}

type githubWorkflowJob struct {
	ID          int64
	Name        string
	Status      string
	Conclusion  string
	StartedAt   time.Time
	CompletedAt time.Time
	Steps       []githubWorkflowStep
}

type githubWorkflowStep struct {
	Name        string
	Status      string
	Conclusion  string
	StartedAt   time.Time
	CompletedAt time.Time
}

type githubWorkflowArtifact struct {
	ID                 int64
	Name               string
	ArchiveDownloadURL string
	Expired            bool
}

type githubActionsClient interface {
	DispatchWorkflow(ctx context.Context, owner, repo, workflowFileName, ref string, inputs map[string]any) (githubDispatchResult, error)
	GetWorkflowRun(ctx context.Context, owner, repo string, runID int64) (githubWorkflowRun, error)
	ListWorkflowJobs(ctx context.Context, owner, repo string, runID int64) ([]githubWorkflowJob, error)
	ListWorkflowRunArtifacts(ctx context.Context, owner, repo string, runID int64) ([]githubWorkflowArtifact, error)
	GetDefaultBranch(ctx context.Context, owner, repo string) (string, error)
	FileExistsAtRef(ctx context.Context, owner, repo, path, ref string) (bool, error)
}

func newGitHubActionsClient(host, token string) (githubActionsClient, error) {
	httpClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	client := gogithub.NewClient(httpClient)
	if strings.TrimSpace(token) != "" {
		client = client.WithAuthToken(strings.TrimSpace(token))
	}
	if normalized := strings.TrimSpace(host); normalized != "" && !strings.EqualFold(normalized, "github.com") {
		enterpriseClient, err := client.WithEnterpriseURLs("https://"+normalized, "https://"+normalized)
		if err != nil {
			return nil, err
		}
		client = enterpriseClient
	}
	return &goGitHubActionsClient{client: client}, nil
}

type goGitHubActionsClient struct {
	client *gogithub.Client
}

func (c *goGitHubActionsClient) DispatchWorkflow(ctx context.Context, owner, repo, workflowFileName, ref string, inputs map[string]any) (githubDispatchResult, error) {
	includeRunDetails := true
	details, _, err := c.client.Actions.CreateWorkflowDispatchEventByFileName(ctx, owner, repo, workflowFileName, gogithub.CreateWorkflowDispatchEventRequest{
		Ref:              ref,
		Inputs:           inputs,
		ReturnRunDetails: &includeRunDetails,
	})
	if err != nil {
		return githubDispatchResult{}, err
	}
	if details == nil {
		return githubDispatchResult{}, nil
	}
	return githubDispatchResult{
		RunID:   details.GetWorkflowRunID(),
		RunURL:  details.GetRunURL(),
		HTMLURL: details.GetHTMLURL(),
	}, nil
}

func (c *goGitHubActionsClient) GetWorkflowRun(ctx context.Context, owner, repo string, runID int64) (githubWorkflowRun, error) {
	run, _, err := c.client.Actions.GetWorkflowRunByID(ctx, owner, repo, runID)
	if err != nil {
		return githubWorkflowRun{}, err
	}
	return githubWorkflowRun{
		ID:           run.GetID(),
		Status:       strings.TrimSpace(run.GetStatus()),
		Conclusion:   strings.TrimSpace(run.GetConclusion()),
		HeadSHA:      strings.TrimSpace(run.GetHeadSHA()),
		RunURL:       strings.TrimSpace(run.GetURL()),
		HTMLURL:      strings.TrimSpace(run.GetHTMLURL()),
		LogsURL:      strings.TrimSpace(run.GetLogsURL()),
		ArtifactsURL: strings.TrimSpace(run.GetArtifactsURL()),
		CreatedAt:    timestampPtrTime(run.CreatedAt),
		StartedAt:    timestampPtrTime(run.RunStartedAt),
		UpdatedAt:    timestampPtrTime(run.UpdatedAt),
	}, nil
}

func (c *goGitHubActionsClient) ListWorkflowJobs(ctx context.Context, owner, repo string, runID int64) ([]githubWorkflowJob, error) {
	opts := &gogithub.ListWorkflowJobsOptions{
		Filter: "latest",
		ListOptions: gogithub.ListOptions{
			PerPage: 100,
		},
	}
	out := make([]githubWorkflowJob, 0)
	for {
		jobs, resp, err := c.client.Actions.ListWorkflowJobs(ctx, owner, repo, runID, opts)
		if err != nil {
			return nil, err
		}
		for _, job := range jobs.Jobs {
			if job == nil {
				continue
			}
			mapped := githubWorkflowJob{
				ID:          job.GetID(),
				Name:        strings.TrimSpace(job.GetName()),
				Status:      strings.TrimSpace(job.GetStatus()),
				Conclusion:  strings.TrimSpace(job.GetConclusion()),
				StartedAt:   timestampPtrTime(job.StartedAt),
				CompletedAt: timestampPtrTime(job.CompletedAt),
			}
			for _, step := range job.Steps {
				if step == nil {
					continue
				}
				mapped.Steps = append(mapped.Steps, githubWorkflowStep{
					Name:        strings.TrimSpace(step.GetName()),
					Status:      strings.TrimSpace(step.GetStatus()),
					Conclusion:  strings.TrimSpace(step.GetConclusion()),
					StartedAt:   timestampPtrTime(step.StartedAt),
					CompletedAt: timestampPtrTime(step.CompletedAt),
				})
			}
			out = append(out, mapped)
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func (c *goGitHubActionsClient) ListWorkflowRunArtifacts(ctx context.Context, owner, repo string, runID int64) ([]githubWorkflowArtifact, error) {
	opts := &gogithub.ListOptions{PerPage: 100}
	out := make([]githubWorkflowArtifact, 0)
	for {
		artifacts, resp, err := c.client.Actions.ListWorkflowRunArtifacts(ctx, owner, repo, runID, opts)
		if err != nil {
			return nil, err
		}
		for _, artifact := range artifacts.Artifacts {
			if artifact == nil {
				continue
			}
			out = append(out, githubWorkflowArtifact{
				ID:                 artifact.GetID(),
				Name:               strings.TrimSpace(artifact.GetName()),
				ArchiveDownloadURL: strings.TrimSpace(artifact.GetArchiveDownloadURL()),
				Expired:            artifact.GetExpired(),
			})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func (c *goGitHubActionsClient) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	repository, _, err := c.client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(repository.GetDefaultBranch()), nil
}

func (c *goGitHubActionsClient) FileExistsAtRef(ctx context.Context, owner, repo, path, ref string) (bool, error) {
	content, _, _, err := c.client.Repositories.GetContents(ctx, owner, repo, path, &gogithub.RepositoryContentGetOptions{
		Ref: strings.TrimSpace(ref),
	})
	if err == nil {
		return content != nil, nil
	}

	var responseErr *gogithub.ErrorResponse
	if errors.As(err, &responseErr) && responseErr.Response != nil && responseErr.Response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, err
}

func timestampPtrTime(ts *gogithub.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.Time
}
