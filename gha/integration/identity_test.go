package integration

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Invoked as an actual test subprocess from inside the host executor. It uses
// real SQLite/blobfs JobDB, recipes, workers and completed replay.
func TestIdentityEmbeddedChild(t *testing.T) {
	if os.Getenv("GHA_IDENTITY_CHILD") != "1" {
		t.Skip("only invoked inside gha host execution")
	}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "C2J_CURRENT_") || strings.HasPrefix(key, "C2J_CHILD_JOB_") || strings.HasPrefix(key, "C2J_WORKSPACE_") || key == "C2J_TENANT_ID" || key == "C2J_OBJECT_OUTBOX" {
			require.Empty(t, value, key)
			if os.Getenv("GHA_IDENTITY_UNSET") == "1" {
				t.Fatalf("explicit unset retained %s", key)
			}
		}
	}
	require.Equal(t, "synthetic-private-module-token", os.Getenv("PRIVATE_MODULE_TOKEN"))
	h := newHarness(t, nil)
	one := h.submit("id: embedded-one\nsequence: [{id: run, op: command_execution, inputs: {run: 'echo fresh-one'}}]\noutputs: {text: '${{ sequence.run.outputs.stdout }}'}\n", nil)
	two := h.submit("id: embedded-two\nsequence: [{id: run, op: command_execution, inputs: {run: 'echo fresh-two'}}]\noutputs: {text: '${{ sequence.run.outputs.stdout }}'}\n", nil)
	require.NotEqual(t, one, two)
	require.Contains(t, h.drain(one)["text"], "fresh-one")
	require.Contains(t, h.drain(two)["text"], "fresh-two")
	for _, job := range h.jobs() {
		require.Empty(t, job.ParentJobID)
		require.NotEqual(t, "synthetic-inherited-parent", job.JobKey.JobId)
	}
	calls := len(h.calls)
	h.reopen()
	h.step(one)
	require.Len(t, h.calls, calls)
	fmt.Println("INDEPENDENT-EMBEDDED-JOBS", one.JobId, two.JobId)
}

func TestEmbeddedIdentityThroughHostExecutor(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	for _, explicitUnset := range []bool{false, true} {
		t.Run(fmt.Sprint(explicitUnset), func(t *testing.T) {
			for _, key := range []string{"C2J_CURRENT_JOB_ID", "C2J_CURRENT_TENANT_ID", "C2J_CURRENT_INVOCATION_HASH", "C2J_CHILD_JOB_ENDPOINT", "C2J_CHILD_JOB_TOKEN", "C2J_CHILD_JOB_SESSION_ID", "C2J_WORKSPACE_SCOPE", "C2J_WORKSPACE_ROOT", "C2J_TENANT_ID", "C2J_OBJECT_OUTBOX"} {
				t.Setenv(key, "synthetic-inherited-parent")
			}
			h := newHarness(t, nil)
			h.secrets["private_modules"] = "synthetic-private-module-token"
			unset := ""
			unsetFlag := "0"
			if explicitUnset {
				unsetFlag = "1"
				unset = `for variable in ${!C2J_CURRENT_@} ${!C2J_CHILD_JOB_@} ${!C2J_WORKSPACE_@}; do
            unset "$variable"
          done
          unset C2J_TENANT_ID C2J_OBJECT_OUTBOX
          `
			}
			installJobFixture(t, h, `on: workflow_dispatch
jobs:
  embedded:
    runs-on: local
    steps:
      - env:
          GHA_IDENTITY_CHILD: '1'
          GHA_IDENTITY_UNSET: '`+unsetFlag+`'
          PRIVATE_MODULE_TOKEN: ${{ secrets.READ_ALL_C2_REPOS }}
          EMBEDDED_TEST_BINARY: '`+binary+`'
        run: |
          `+unset+`"$EMBEDDED_TEST_BINARY" -test.v -test.run '^TestIdentityEmbeddedChild$' -test.count=1
`)
			installWorkflowFixture(t, h)
			root := h.submit("perms: {jobdb: {jobs: {submit: true}, secrets: {resolve: [private_modules]}}}\n"+workflowRecipe(t, "      secret_bindings: {READ_ALL_C2_REPOS: private_modules}"), nil)
			result := h.drain(root)["run"].(map[string]any)
			require.Equal(t, "success", result["status"])
			ref := result["jobs"].(map[string]any)["embedded"].(map[string]any)["diagnostics"].([]any)[0]
			log := readDiagnostic(t, h, ref)
			require.Contains(t, string(log), "INDEPENDENT-EMBEDDED-JOBS")
			saveEvidence(t, fmt.Sprintf("identity-%t.json", explicitUnset), map[string]any{"caller_job_id": root.JobId, "result": result, "log": string(log)})
		})
	}
}
