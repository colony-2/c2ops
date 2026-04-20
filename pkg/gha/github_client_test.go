package gha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/require"
)

func TestGoGitHubActionsClientMethods(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/actions/workflows/ci.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workflow_run_id": 123,
			"run_url":         "https://api.github.test/runs/123",
			"html_url":        "https://github.test/runs/123",
		})
	})
	mux.HandleFunc("/repos/acme/widgets/actions/runs/123", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":             123,
			"status":         "completed",
			"conclusion":     "success",
			"head_sha":       "deadbeef",
			"url":            "https://api.github.test/runs/123",
			"html_url":       "https://github.test/runs/123",
			"logs_url":       "https://api.github.test/runs/123/logs",
			"artifacts_url":  "https://api.github.test/runs/123/artifacts",
			"created_at":     "2026-03-30T20:00:00Z",
			"run_started_at": "2026-03-30T20:01:00Z",
			"updated_at":     "2026-03-30T20:02:00Z",
		})
	})
	mux.HandleFunc("/repos/acme/widgets/actions/runs/123/jobs", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": 1,
			"jobs": []map[string]any{
				{
					"id":           7,
					"name":         "test",
					"status":       "completed",
					"conclusion":   "success",
					"started_at":   "2026-03-30T20:01:00Z",
					"completed_at": "2026-03-30T20:02:00Z",
					"steps": []map[string]any{
						{
							"name":         "Echo",
							"status":       "completed",
							"conclusion":   "success",
							"started_at":   "2026-03-30T20:01:00Z",
							"completed_at": "2026-03-30T20:01:05Z",
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/repos/acme/widgets/actions/runs/123/artifacts", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": 1,
			"artifacts": []map[string]any{
				{
					"id":                   55,
					"name":                 "coverage",
					"archive_download_url": "https://api.github.test/artifacts/55/zip",
					"expired":              false,
				},
			},
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := gogithub.NewClient(server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	require.NoError(t, err)
	client.BaseURL = baseURL

	adapter := &goGitHubActionsClient{client: client}
	ctx := context.Background()

	dispatch, err := adapter.DispatchWorkflow(ctx, "acme", "widgets", "ci.yml", "main", map[string]any{"env": "test"})
	require.NoError(t, err)
	require.Equal(t, int64(123), dispatch.RunID)

	run, err := adapter.GetWorkflowRun(ctx, "acme", "widgets", 123)
	require.NoError(t, err)
	require.Equal(t, "success", run.Conclusion)
	require.Equal(t, time.Date(2026, 3, 30, 20, 1, 0, 0, time.UTC), run.StartedAt)

	jobs, err := adapter.ListWorkflowJobs(ctx, "acme", "widgets", 123)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Len(t, jobs[0].Steps, 1)
	require.Equal(t, "Echo", jobs[0].Steps[0].Name)

	artifacts, err := adapter.ListWorkflowRunArtifacts(ctx, "acme", "widgets", 123)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	require.Equal(t, "coverage", artifacts[0].Name)
}

func TestNewGitHubActionsClientBuildsEnterpriseClient(t *testing.T) {
	client, err := newGitHubActionsClient("example.com", "token")
	require.NoError(t, err)
	require.NotNil(t, client)
}
