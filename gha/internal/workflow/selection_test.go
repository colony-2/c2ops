package workflow

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func releaseFixture(t *testing.T) (Input, *[]map[string]any) {
	t.Helper()
	root := t.TempDir()
	sourceGit(t, root, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(root, "workflow.yml"), []byte("on: workflow_dispatch\njobs:\n  build:\n    runs-on: local\n    strategy:\n      fail-fast: false\n      matrix: {shard: [one, two]}\n    steps: [{run: 'echo hello'}]\n"), 0600))
	base := sourceCommit(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, "candidate"), []byte("candidate"), 0600))
	sourceCommit(t, root)
	records := &[]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Start struct {
				JobID string `json:"job_id"`
			}
			Recipes []struct {
				YAML []byte `json:"yaml"`
			} `json:"embedded_recipes"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		var recipe map[string]any
		require.NoError(t, yaml.Unmarshal(request.Recipes[0].YAML, &recipe))
		*records = append(*records, recipe)
		require.NoError(t, json.NewEncoder(w).Encode(Submission{JobID: request.Start.JobID}))
	}))
	t.Cleanup(server.Close)
	for key, value := range map[string]string{"C2J_CHILD_JOB_ENDPOINT": server.URL, "C2J_CHILD_JOB_TOKEN": "fixture", "C2J_CHILD_JOB_SESSION_ID": "session", "C2J_CURRENT_TENANT_ID": "tenant", "C2J_CURRENT_JOB_ID": "parent", "C2J_CURRENT_INVOCATION_HASH": "invoke"} {
		t.Setenv(key, value)
	}
	return Input{Workflow: "workflow.yml", WorktreePath: root, Repository: "example.test/fixture", BaseRevision: base, Profiles: []Profile{{Labels: []string{"local"}, ExecutionNeeds: map[string]any{"platform": "linux/arm64"}}}}, records
}

func setRelease(t *testing.T, repo, rev string) {
	t.Helper()
	oldRepo, oldRev := releaseRepository, releaseRevision
	releaseRepository, releaseRevision = repo, rev
	t.Cleanup(func() { releaseRepository, releaseRevision = oldRepo, oldRev })
}

func TestSelectionFailsBeforeSubmission(t *testing.T) {
	in, records := releaseFixture(t)
	setRelease(t, "", "")
	for _, partial := range []Input{in, func() Input { copy := in; copy.HelperOp = "explicit"; return copy }(), func() Input { copy := in; copy.ExecutorOp = "explicit"; return copy }()} {
		_, err := Submit(t.Context(), partial)
		require.ErrorContains(t, err, "metadata unavailable")
		require.Empty(t, *records)
	}
}

func TestSelectionCarriesWorkersThroughExpansion(t *testing.T) {
	for _, override := range []string{"", "nix:github:colony-2/c2ops/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa#gha-workflow"} {
		t.Run(override, func(t *testing.T) {
			in, records := releaseFixture(t)
			in.HelperOp = override
			setRelease(t, testRepository, testRevision)
			_, err := Submit(t.Context(), in)
			require.NoError(t, err)
			helper := override
			if helper == "" {
				helper = testPrefix + "#gha-workflow"
			}
			require.Len(t, *records, 2)
			var p payload
			steps := (*records)[0]["sequence"].([]any)
			require.Equal(t, helper, steps[0].(map[string]any)["op"])
			expandNode := steps[1].(map[string]any)
			require.Equal(t, helper, expandNode["op"])
			raw, err := base64.StdEncoding.DecodeString(expandNode["inputs"].(map[string]any)["payload"].(string))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &p))
			require.Equal(t, helper, p.Input.HelperOp)
			require.Equal(t, testPrefix+"#gha-job", p.Input.ExecutorOp)
			require.Equal(t, helper, (*records)[1]["sequence"].([]any)[0].(map[string]any)["op"])
			*records = nil
			// A compatible helper from another release, or an unversioned helper, must
			// consume the recorded pair without consulting its own build metadata.
			releaseRepository, releaseRevision = "", ""
			_, err = Run(t.Context(), Input{Action: "expand", WorktreePath: in.WorktreePath, Payload: encode(p)})
			require.NoError(t, err)
			require.Len(t, *records, 3)
			for _, recipe := range *records {
				seq := recipe["sequence"].([]any)
				require.Equal(t, helper, seq[0].(map[string]any)["op"])
				for _, step := range seq[1:] {
					node := step.(map[string]any)
					switch node["id"] {
					case "execute":
						require.Equal(t, p.Input.ExecutorOp, node["op"])
					case "collect":
						require.Equal(t, helper, node["op"])
					}
				}
			}
			_, err = Run(t.Context(), Input{Action: "source"})
			require.NoError(t, err)
			_, err = Run(t.Context(), Input{Action: "collect", Payload: encode([]instanceIdentity{})})
			require.NoError(t, err)
		})
	}
}
