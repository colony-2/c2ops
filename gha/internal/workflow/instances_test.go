package workflow

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2ops/gha/internal/jobexec"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func observation(status, conclusion string, outputs map[string]string) Observation {
	var o Observation
	o.Status = status
	o.Outputs.Result = jobexec.Result{Conclusion: conclusion, Outputs: outputs}
	return o
}

func TestCollectionKeepsAggregationAndOpaqueIdentity(t *testing.T) {
	identities := []instanceIdentity{
		{map[string]any{"os": "${{ context.secret }}", "n": float64(24), "flag": true, "obj": map[string]any{"v": nil}}, []string{"${{ literal }}"}, map[string]any{"base": "${{ literal }}"}, "child-0"},
		{map[string]any{}, []string{"local"}, map[string]any{"platform": "linux/arm64"}, "child-1"},
		{map[string]any{}, []string{"local"}, map[string]any{"image": "legacy"}, "child-2"},
	}
	in := Input{Payload: encode(identities), Observations: []Observation{observation("completed", "success", map[string]string{"a": "1"}), observation("completed", "skipped", nil), observation("completed", "success", map[string]string{"b": "2"})}}
	got, err := collectWithIdentity(in)
	require.NoError(t, err)
	require.Equal(t, identities, got.Assignments)
	require.Equal(t, "success", got.Conclusion)
	require.Equal(t, map[string]string{"a": "1", "b": "2"}, got.Outputs)
	in.Observations[2].Outputs.Result.Outputs["a"] = "conflict"
	_, err = collectWithIdentity(in)
	require.ErrorContains(t, err, "matrix outputs collide")
	for _, observations := range [][]Observation{nil, {observation("completed", "skipped", nil)}, {observation("cancelled", "failure", nil)}} {
		want, err := collect(observations)
		require.NoError(t, err)
		got, err := collectWithIdentity(Input{Observations: observations})
		require.NoError(t, err)
		require.Equal(t, want, got.Result)
	}
	empty, err := collectWithIdentity(Input{Payload: encode([]instanceIdentity{})})
	require.NoError(t, err)
	require.NotNil(t, empty.Assignments)
	require.Empty(t, empty.Assignments)
	require.Equal(t, "skipped", empty.Conclusion)
	_, err = collectWithIdentity(Input{Payload: encode(identities)})
	require.ErrorContains(t, err, "counts differ")
	_, err = collectWithIdentity(Input{Payload: "not base64"})
	require.Error(t, err)
}

func TestGeneratedInstancesKeepNativeReferencesAndPlanOrder(t *testing.T) {
	root := t.TempDir()
	sourceGit(t, root, "init", "-b", "main")
	source := `on: workflow_dispatch
jobs:
  build:
    runs-on: local
    strategy:
      fail-fast: false
      matrix: {n: [1, 1, 2]}
    steps: [{run: 'true'}]
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "workflow.yml"), []byte(source), 0600))
	revision := sourceCommit(t, root)
	type submitted struct {
		Start struct {
			JobID string `json:"job_id"`
		} `json:"start"`
		Embedded []struct {
			YAML []byte `json:"yaml"`
		} `json:"embedded_recipes"`
	}
	recipes := []map[string]any{}
	ids := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request submitted
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		var rec map[string]any
		if err := yaml.Unmarshal(request.Embedded[0].YAML, &rec); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		recipes = append(recipes, rec)
		ids = append(ids, request.Start.JobID)
		json.NewEncoder(w).Encode(Submission{JobID: request.Start.JobID})
	}))
	defer server.Close()
	for key, value := range map[string]string{"C2J_CHILD_JOB_ENDPOINT": server.URL, "C2J_CHILD_JOB_TOKEN": "fixture", "C2J_CHILD_JOB_SESSION_ID": "fixture", "C2J_CURRENT_TENANT_ID": "fixture", "C2J_CURRENT_JOB_ID": "parent", "C2J_CURRENT_INVOCATION_HASH": "invocation"} {
		t.Setenv(key, value)
	}
	hash := sha256.Sum256([]byte(source))
	_, err := expand(t.Context(), Input{WorktreePath: root, Payload: encode(payload{Input: Input{Repository: "example.test/fixture", Revision: revision, BaseRevision: revision, Workflow: "workflow.yml", EventName: "workflow_dispatch", HelperOp: "helper", ExecutorOp: "executor", Profiles: []Profile{{Labels: []string{"local"}, ExecutionNeeds: map[string]any{"platform": "linux/arm64"}}}}, Job: "build", Hash: hex.EncodeToString(hash[:])})})
	require.NoError(t, err)
	require.Len(t, recipes, 4)
	collector := recipes[3]
	steps := collector["sequence"].([]any)
	require.Len(t, steps, 4)
	collectInputs := steps[3].(map[string]any)["inputs"].(map[string]any)
	raw, err := base64.StdEncoding.DecodeString(collectInputs["payload"].(string))
	require.NoError(t, err)
	var identities []instanceIdentity
	require.NoError(t, json.Unmarshal(raw, &identities))
	require.Len(t, identities, 3)
	result := collector["outputs"].(map[string]any)["result"].(map[string]any)
	instances := result["instances"].([]any)
	refs := result["diagnostics"].([]any)
	for i, value := range instances {
		instance := value.(map[string]any)
		require.Equal(t, ids[i], instance["execution_job_id"])
		require.Equal(t, ids[i], identities[i].ExecutionJobID)
		require.Equal(t, refs[i], instance["diagnostics"])
		require.Contains(t, instance["matrix"], "sequence.collect.outputs.assignments")
		require.NotContains(t, string(raw), "diagnostics")
	}
	require.Equal(t, identities[0].Matrix, identities[1].Matrix)
	require.NotEqual(t, identities[0].ExecutionJobID, identities[1].ExecutionJobID)
}
