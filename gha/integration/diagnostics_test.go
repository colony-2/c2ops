package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func readDiagnostic(t *testing.T, h *harness, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var ref objects.Ref
	require.NoError(t, json.Unmarshal(raw, &ref))
	require.NoError(t, ref.Validate())
	store := objects.NewStore(objects.Config{JobKey: jobdb.JobKey{TenantId: ref.TenantID, JobId: ref.Artifact.JobId}, Workdir: t.TempDir(), GetArtifact: func(key jobdb.ArtifactKey) (jobdb.Artifact, error) {
		stored, err := h.db.OpenArtifact(h.ctx, jobdb.ArtifactRef{JobKey: jobdb.JobKey{TenantId: ref.TenantID, JobId: key.JobId}, Ordinal: int64(key.TaskOrdinal), Name: key.Name})
		if err != nil {
			return nil, err
		}
		r, err := stored.Open()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		return jobdb.NewArtifactFromBytes(key.Name, data), err
	}})
	snapshot, err := store.Open(h.ctx, ref, "c2ops.gha.diagnostics/v1")
	require.NoError(t, err)
	defer snapshot.Close()
	data, err := os.ReadFile(snapshot.Files["log"])
	require.NoError(t, err)
	return data
}
func saveEvidence(t *testing.T, name string, value any) {
	t.Helper()
	dir := os.Getenv("C2OPS_GHA_EVIDENCE_DIR")
	if dir == "" {
		return
	}
	require.True(t, filepath.IsAbs(dir))
	require.NoError(t, os.MkdirAll(dir, 0700))
	raw, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), append(raw, '\n'), 0600))
}
func TestMatrixFailureDurableMaskedDiagnostics(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			h := newHarness(t, nil)
			h.secrets["overlap"] = "prefix-private-suffix"
			h.secrets["prefix"] = "prefix"
			h.secrets["multiline"] = "private-line-one\nprivate-line-two"
			exit := "true"
			if fail {
				exit = `test "$SHARD" != bad`
			}
			installJobFixture(t, h, `on: workflow_dispatch
jobs:
  build:
    runs-on: local
    strategy:
      fail-fast: false
      matrix: {shard: [bad, good]}
    steps:
      - env:
          SHARD: ${{ matrix.shard }}
          OVERLAP: ${{ secrets.LONG }}
          MULTILINE: ${{ secrets.MULTILINE }}
        run: |
          printf '%s\n' "$OVERLAP" "$MULTILINE"
          echo "SHARD-FINISHED-$SHARD"
          `+exit+`
  summary:
    needs: build
    if: always()
    runs-on: local
    steps:
      - run: echo "SUMMARY-RAN-${{ needs.build.result }}"
`)
			installWorkflowFixture(t, h)
			root := h.submit("perms: {jobdb: {jobs: {submit: true}, secrets: {resolve: [overlap, prefix, multiline]}}}\n"+workflowRecipe(t, "      secret_bindings: {LONG: overlap, SHORT: prefix, MULTILINE: multiline}"), nil)
			completed := h.drain(root)
			run := completed["run"].(map[string]any)
			want := "success"
			if fail {
				want = "failure"
			}
			require.Equal(t, want, run["status"])
			require.Equal(t, h.commit, run["revision"])
			require.Len(t, run["workflow_sha256"], 64)
			jobs := run["jobs"].(map[string]any)
			require.Equal(t, want, jobs["build"].(map[string]any)["conclusion"])
			require.Equal(t, "success", jobs["summary"].(map[string]any)["conclusion"])
			require.NoError(t, os.RemoveAll(filepath.Join(h.root, "workers")))
			require.NoError(t, os.Mkdir(filepath.Join(h.root, "workers"), 0700))
			h.reopen()
			calls, reads, count := len(h.calls), h.secretReads, len(h.jobs())
			require.Equal(t, workflow.JobRunCompleted, h.step(root).Status)
			require.Len(t, h.calls, calls)
			require.Equal(t, reads, h.secretReads)
			require.Len(t, h.jobs(), count)
			for name, job := range jobs {
				refs := job.(map[string]any)["diagnostics"].([]any)
				if name == "build" {
					require.Len(t, refs, 2)
				}
				for i, ref := range refs {
					data := readDiagnostic(t, h, ref)
					for _, value := range []string{"prefix-private-suffix", "private-line-one", "private-line-two"} {
						require.NotContains(t, string(data), value)
					}
					if name == "build" {
						shard := "bad"
						if i == 1 {
							shard = "good"
						}
						require.Contains(t, string(data), "SHARD-FINISHED-"+shard)
					} else {
						require.Contains(t, string(data), "SUMMARY-RAN-"+want)
					}
					saveEvidence(t, fmt.Sprintf("matrix-%s-%s-%d.json", want, name, i), map[string]any{"ref": ref, "log": string(data)})
				}
			}
			ids := []string{}
			for _, job := range h.jobs() {
				ids = append(ids, job.JobKey.JobId)
			}
			saveEvidence(t, "matrix-"+want+"-result.json", map[string]any{"caller_job_id": root.JobId, "submission": completed["submission"], "result": run, "job_ids": ids, "candidate_tree": h.git("rev-parse", "HEAD^{tree}")})
		})
	}
}

func TestWorkflowInfrastructureFailureHasNullDiagnostics(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, "on: workflow_dispatch\njobs:\n  broken:\n    runs-on: local\n    steps: [{run: 'true'}]\n")
	installWorkflowFixture(t, h)
	// Real extension failure before Execute: no object outbox can be published.
	raw, err := os.ReadFile(filepath.Join(h.repo, ".c2/test-ops/gha-job/op.yaml"))
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(raw, &manifest))
	manifest["command"] = []string{"/bin/false"}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	h.seed(map[string]string{".c2/test-ops/gha-job/op.yaml": string(encoded)})
	root := h.submit(workflowRecipe(t, ""), nil)
	completed := h.drain(root)
	run := completed["run"].(map[string]any)
	require.Equal(t, "failure", run["status"])
	job := run["jobs"].(map[string]any)["broken"].(map[string]any)
	require.Equal(t, []any{nil}, job["diagnostics"])
	ids := []string{}
	for _, job := range h.jobs() {
		ids = append(ids, job.JobKey.JobId)
	}
	saveEvidence(t, "infrastructure-result.json", map[string]any{"caller_job_id": root.JobId, "submission": completed["submission"], "result": run, "job_ids": ids, "unavailable_diagnostic": "executor process exited 1 before emitting extension result/object drafts"})
}

func TestCancelledShardHasNullDiagnostics(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, "on: workflow_dispatch\njobs:\n  pending:\n    runs-on: local\n    steps: [{run: 'exit 99'}]\n")
	installWorkflowFixture(t, h)
	root := h.submit(workflowRecipe(t, ""), nil)
	h.step(root)
	var shard jobdb.JobKey
	for iteration := 0; iteration < 5 && shard.JobId == ""; iteration++ {
		for _, job := range h.jobs() {
			var meta struct {
				Recipe string `json:"recipe"`
			}
			require.NoError(t, json.Unmarshal(job.Metadata, &meta))
			if meta.Recipe == "gha-pending-0" {
				shard = job.JobKey
				break
			}
			if meta.Recipe == "gha-pending" {
				h.step(job.JobKey)
			}
		}
	}
	require.NotEmpty(t, shard.JobId)
	require.NoError(t, h.db.CancelJob(h.ctx, jobdb.CancelJobRequest{JobKey: shard, Reason: "bounded qualification fixture cancellation"}))
	completed := h.drain(root)
	run := completed["run"].(map[string]any)
	require.Equal(t, "failure", run["status"])
	job := run["jobs"].(map[string]any)["pending"].(map[string]any)
	require.Equal(t, "failure", job["conclusion"])
	require.Equal(t, []any{nil}, job["diagnostics"])
	saveEvidence(t, "cancellation-result.json", map[string]any{"caller_job_id": root.JobId, "submission": completed["submission"], "cancelled_shard_job_id": shard.JobId, "result": run, "unavailable_diagnostic": "shard cancelled in JobDB before executor invocation; no log object was created"})
}

func TestSecretBindingsMissingAndUnauthorized(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, nil)
			h.secrets["ungranted"] = "never-public-private-value"
			installJobFixture(t, h, "on: workflow_dispatch\njobs:\n  private:\n    runs-on: local\n    steps: [{run: 'exit 99'}]\n")
			installWorkflowFixture(t, h)
			binding := "missing"
			perms := "perms: {jobdb: {jobs: {submit: true}, secrets: {resolve: [missing]}}}\n"
			if mode == "unauthorized" {
				binding = "ungranted"
				perms = "perms: {jobdb: {jobs: {submit: true}, secrets: {resolve: []}}}\n"
			}
			root := h.submit(perms+workflowRecipe(t, "      secret_bindings: {CLIENT_KEY: "+binding+"}\n      source_credential: CLIENT_KEY"), nil)
			if mode == "unauthorized" {
				h.deniedSecrets = map[string]bool{"ungranted": true}
			}
			result := h.drain(root)["run"].(map[string]any)
			require.Equal(t, "failure", result["status"])

			failed := false
			for _, job := range h.jobs() {
				if strings.HasPrefix(job.CompletionStatus, "failed") {
					failed = true
				}
			}
			require.True(t, failed, "missing/unauthorized binding must fail a real job")
		})
	}
}
