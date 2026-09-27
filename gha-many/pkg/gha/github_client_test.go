package gha

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	gogithub "github.com/google/go-github/v92/github"
)

func TestGitHubActionsSDKCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/org/repo/actions/workflows/ci.yml/dispatches":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if r.Method != "POST" || request["ref"] != "main" || request["return_run_details"] != true {
				t.Errorf("unexpected dispatch: %s %v", r.Method, request)
			}
			fmt.Fprint(w, `{"workflow_run_id":42,"run_url":"https://api.github.com/runs/42","html_url":"https://github.com/runs/42"}`)
		case "/repos/org/repo/actions/runs/42":
			fmt.Fprint(w, `{"id":42,"status":"completed","conclusion":"success","head_sha":"abc","created_at":"2026-09-01T00:00:00Z"}`)
		case "/repos/org/repo/actions/runs/42/jobs":
			fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":7,"name":"test","status":"completed","conclusion":"success","steps":[{"name":"go test","status":"completed","conclusion":"success"}]}]}`)
		case "/repos/org/repo/actions/runs/42/artifacts":
			fmt.Fprint(w, `{"total_count":1,"artifacts":[{"id":8,"name":"report","expired":false,"archive_download_url":"https://example.test/report.zip"}]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := gogithub.NewClient(gogithub.WithHTTPClient(server.Client()), gogithub.WithURLs(gogithub.Ptr(server.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &goGitHubActionsClient{client: client}
	ctx := context.Background()
	dispatch, err := adapter.DispatchWorkflow(ctx, "org", "repo", "ci.yml", "main", map[string]any{"name": "test"})
	if err != nil || dispatch.RunID != 42 {
		t.Fatalf("dispatch: %+v %v", dispatch, err)
	}
	run, err := adapter.GetWorkflowRun(ctx, "org", "repo", dispatch.RunID)
	if err != nil || run.Conclusion != "success" || run.HeadSHA != "abc" || run.CreatedAt.IsZero() {
		t.Fatalf("run: %+v %v", run, err)
	}
	jobs, err := adapter.ListWorkflowJobs(ctx, "org", "repo", dispatch.RunID)
	if err != nil || len(jobs) != 1 || len(jobs[0].Steps) != 1 || jobs[0].Steps[0].Conclusion != "success" {
		t.Fatalf("jobs: %+v %v", jobs, err)
	}
	artifacts, err := adapter.ListWorkflowRunArtifacts(ctx, "org", "repo", dispatch.RunID)
	if err != nil || len(artifacts) != 1 || artifacts[0].Name != "report" {
		t.Fatalf("artifacts: %+v %v", artifacts, err)
	}
}
