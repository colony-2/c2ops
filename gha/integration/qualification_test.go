package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Admission of the unchanged workflow establishes planning compatibility, not
// native qualification: this embedded harness does not provide native workers.
func TestSuppliedWorkflowPlansAllNativeProfiles(t *testing.T) {
	raw, err := os.ReadFile("testdata/c2j-test.yaml")
	require.NoError(t, err)
	digest := sha256.Sum256(raw)
	require.Equal(t, "7ea7255c348875228f908ea554402d829d369da57852db32348d0a472b42bbb0", hex.EncodeToString(digest[:]))
	h := newHarness(t, nil)
	installJobFixture(t, h, string(raw))
	installWorkflowFixture(t, h)
	recipe := workflowRecipe(t, "")
	start := strings.Index(recipe, "runner_profiles:")
	end := strings.Index(recipe[start:], "- id: done") + start
	require.Greater(t, end, start)
	// Use parsed YAML to avoid changing the supplied workflow bytes.
	var tree map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(recipe), &tree))
	launch := tree["sequence"].([]any)[0].(map[string]any)
	inputs := launch["inputs"].(map[string]any)
	inputs["runner_profiles"] = []any{map[string]any{"labels": []string{"ubuntu-latest"}, "execution_needs": map[string]any{"base": "ubuntu-26.04-amd64"}}, map[string]any{"labels": []string{"ubuntu-22.04-arm"}, "execution_needs": map[string]any{"base": "ubuntu-26.04-arm64"}}, map[string]any{"labels": []string{"macos-latest"}, "execution_needs": map[string]any{"base": "macos-26-arm64"}}}
	encoded, err := yaml.Marshal(tree)
	require.NoError(t, err)
	root := h.submit(string(encoded), nil)
	h.step(root)
	seen := map[string]bool{}
	demands := map[string]any{}
	for iteration := 0; iteration < 4 && len(seen) < 3; iteration++ {
		for _, job := range h.jobs() {
			var meta struct {
				Recipe string `json:"recipe"`
			}
			require.NoError(t, json.Unmarshal(job.Metadata, &meta))
			if meta.Recipe == "gha-test" {
				h.step(job.JobKey)
			}
			if strings.HasPrefix(meta.Recipe, "gha-test-") && meta.Recipe != "gha-test-result" && meta.Recipe != "gha-test-summary" && meta.Recipe != "gha-test-summary-result" {
				out := h.step(job.JobKey)
				handoff, err := compiler.ExecutionHandoffFromOutcome(out)
				require.NoError(t, err)
				require.NotNil(t, handoff)
				require.NotNil(t, handoff.Demand.Effective.Base)
				seen[*handoff.Demand.Effective.Base] = true
				demands[job.JobKey.JobId] = handoff.Demand
			}
		}
	}
	require.Equal(t, map[string]bool{"ubuntu-26.04-amd64": true, "ubuntu-26.04-arm64": true, "macos-26-arm64": true}, seen)
	saveEvidence(t, "supplied-workflow-admission.json", map[string]any{"caller_job_id": root.JobId, "workflow_sha256": hex.EncodeToString(digest[:]), "candidate_head": h.commit, "candidate_tree": h.git("rev-parse", "HEAD^{tree}"), "demands": demands, "qualification": "planning only; native worker leases/actions/commands did not run"})
}
