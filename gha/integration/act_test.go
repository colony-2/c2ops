package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const publicJobOp = "nix:github:colony-2/c2ops/published#gha-job"

var helperBinary string
var workflowBinary string
var ghaBinary string

// Compile once, before the worker loop. Every invocation executes this binary;
// neither the fixture manifest nor a live job invokes the Go toolchain.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "c2ops-gha-integration-bin-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	helperBinary = filepath.Join(dir, "gha-job")
	build := exec.Command("go", "build", "-o", helperBinary, "./job")
	build.Dir = ".."
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	workflowBinary = filepath.Join(dir, "gha-workflow")
	build = exec.Command("go", "build", "-o", workflowBinary, "./workflow")
	build.Dir = ".."
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ghaBinary = filepath.Join(dir, "gha")
	build = exec.Command("go", "build", "-o", ghaBinary, ".")
	build.Dir = ".."
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// Only op selectors are overridden. Workflow bytes and expressions are never
// templated through c2j. Keep public coordinates in authored fixture recipes.
func localRecipe(t *testing.T, recipe string) string {
	t.Helper()
	var root yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(recipe), &root))
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				if (node.Content[i].Value == "op" || node.Content[i].Value == "executor_op") && node.Content[i+1].Value == publicJobOp {
					node.Content[i+1].Value = "./.c2/test-ops/gha-job"
				}
				if node.Content[i].Value == "op" && node.Content[i+1].Value == "nix:github:colony-2/c2ops/published#gha" {
					node.Content[i+1].Value = "./.c2/test-ops/gha"
				}
				if (node.Content[i].Value == "op" || node.Content[i].Value == "helper_op") && node.Content[i+1].Value == "nix:github:colony-2/c2ops/published#gha-workflow" {
					node.Content[i+1].Value = "./.c2/test-ops/gha-workflow"
				}
			}
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	walk(&root)
	result, err := yaml.Marshal(&root)
	require.NoError(t, err)
	return string(result)
}

func newActHarness(t *testing.T, ghaWorkflow string) *harness {
	t.Helper()
	child := localRecipe(t, `id: act-job
input_schema:
  job_id: {type: string, required: true}
  needs_json: {type: string, default_value: "{}"}
inputs:
  job_id: '${{ inputs.job_id }}'
  needs_json: '${{ inputs.needs_json }}'
sequence:
  - id: execute
    op: nix:github:colony-2/c2ops/published#gha-job
    inputs:
      workflow: .github/workflows/fixture.yml
      job_id: '${{ inputs.job_id }}'
      needs: '${{ json_parse(inputs.needs_json) }}'
outputs:
  result: '${{ sequence.execute.outputs }}'
`)
	h := newHarness(t, map[string]string{"act-job": child})
	installJobFixture(t, h, ghaWorkflow)
	return h
}

func installJobFixture(t *testing.T, h *harness, ghaWorkflow string) {
	t.Helper()
	raw, err := os.ReadFile("../job/op.json")
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(raw, &manifest))
	manifest["command"] = []string{helperBinary}
	delete(manifest, "dependencies")
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	h.seed(map[string]string{
		".c2/test-ops/gha-job/op.yaml":  string(encoded),
		".github/workflows/fixture.yml": ghaWorkflow,
	})
}

const fanInWorkflow = `name: Fan in
on: workflow_dispatch
jobs:
  a:
    runs-on: local
    outputs:
      value: ${{ steps.build.outputs.value }}
      workdir: ${{ steps.build.outputs.workdir }}
      home: ${{ steps.build.outputs.home }}
    steps:
      - id: build
        run: |
          test ! -e b-ran
          touch a-ran
          echo value=alpha >> "$GITHUB_OUTPUT"
          echo "workdir=$PWD" >> "$GITHUB_OUTPUT"
          echo "home=$HOME" >> "$GITHUB_OUTPUT"
  b:
    runs-on: local
    outputs:
      value: ${{ steps.build.outputs.value }}
      workdir: ${{ steps.build.outputs.workdir }}
      home: ${{ steps.build.outputs.home }}
    steps:
      - id: build
        run: |
          test ! -e a-ran
          touch b-ran
          echo value=beta >> "$GITHUB_OUTPUT"
          echo "workdir=$PWD" >> "$GITHUB_OUTPUT"
          echo "home=$HOME" >> "$GITHUB_OUTPUT"
  combine:
    needs: [a, b]
    runs-on: local
    outputs:
      combined: ${{ steps.combine.outputs.combined }}
    steps:
      - id: combine
        env:
          A: ${{ needs.a.outputs.value }}
          B: ${{ needs.b.outputs.value }}
        run: |
          test ! -e a-ran
          test ! -e b-ran
          echo "combined=$A-$B" >> "$GITHUB_OUTPUT"
`

const fanInRecipe = `id: fan-in
sequence:
  - id: builds
    child_group:
      children:
        - key: a
          recipe: act-job
          inputs: {job_id: a}
        - key: b
          recipe: act-job
          inputs: {job_id: b}
  - id: combine
    op: recipe.run_and_get_result
    inputs:
      name: act-job
      inputs:
        job_id: combine
        needs_json: '${{ json_stringify({"a": {"conclusion": sequence.builds.outputs.children[0].outputs.result.conclusion, "outputs": sequence.builds.outputs.children[0].outputs.result.outputs}, "b": {"conclusion": sequence.builds.outputs.children[1].outputs.result.conclusion, "outputs": sequence.builds.outputs.children[1].outputs.result.outputs} }) }}'
outputs:
  builds: '${{ sequence.builds.outputs.children }}'
  result: '${{ sequence.combine.outputs.outputs.result }}'
`

func TestActFanOutFanInAcrossC2JChildrenAndRestart(t *testing.T) {
	h := newActHarness(t, fanInWorkflow)
	root := h.submit(fanInRecipe, nil)
	require.Equal(t, workflow.JobRunSuspended, h.step(root).Status)
	for _, job := range h.jobs() {
		if job.JobKey != root {
			out := h.step(job.JobKey)
			require.Equal(t, workflow.JobRunCompleted, out.Status, "%v", out.JobError)
			break
		}
	}
	h.reopen()
	result := h.drain(root)
	run := result["result"].(map[string]any)
	require.Equal(t, "success", run["conclusion"])
	require.Equal(t, "alpha-beta", run["outputs"].(map[string]any)["combined"])
	require.Len(t, h.jobs(), 4, "one parent and three independently scheduled jobs")
	builds := result["builds"].([]any)
	outputs := func(i int) map[string]any {
		return builds[i].(map[string]any)["outputs"].(map[string]any)["result"].(map[string]any)["outputs"].(map[string]any)
	}
	require.NotEqual(t, outputs(0)["workdir"], outputs(1)["workdir"])
	require.NotEqual(t, outputs(0)["home"], outputs(1)["home"])
	for _, item := range builds {
		child := item.(map[string]any)
		liveExecutions := 0
		for _, call := range h.calls {
			if strings.HasPrefix(call, child["job_id"].(string)+":extension_execution:") {
				liveExecutions++
			}
		}
		require.Equal(t, 1, liveExecutions, "replay must not execute a completed extension again")
	}
}

func TestActNamedSecretsConfigAndDurableLogs(t *testing.T) {
	h := newHarness(t, nil)
	const credential = "gha-synthetic-private-credential"
	h.secrets["client_key"] = credential
	_, err := h.db.SetTenantConfigurationValue(h.ctx, jobdb.SetTenantConfigurationValueRequest{
		TenantID: tenant, Name: "build_setting", Value: json.RawMessage(`"configured"`),
	})
	require.NoError(t, err)
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  probe:
    runs-on: local
    outputs:
      ok: ${{ steps.probe.outputs.ok }}
      unsafe: ${{ secrets.CLIENT_KEY }}
    steps:
      - id: probe
        env:
          TOKEN: ${{ secrets.CLIENT_KEY }}
          SETTING: ${{ vars.SETTING }}
        run: |
          test -n "$TOKEN"
          test "$SETTING" = configured
          echo "$TOKEN"
          echo 'recorded fixture log'
          echo 'ok=yes' >> "$GITHUB_OUTPUT"
`)
	root := h.submit(localRecipe(t, `id: secrets
perms: {jobdb: {secrets: {resolve: [client_key]}}}
op: nix:github:colony-2/c2ops/published#gha-job
inputs:
  workflow: .github/workflows/fixture.yml
  job_id: probe
  secrets:
    CLIENT_KEY: '${{ context.secrets.client_key }}'
  vars:
    SETTING: '${{ context.config.build_setting }}'
`), nil)
	result := h.drain(root)
	require.Equal(t, "success", result["conclusion"])
	require.Equal(t, map[string]any{"ok": "yes"}, result["outputs"])
	require.Positive(t, h.secretReads)
	reads := h.secretReads
	// The worker's logs and worktrees disappear before the runtime is recreated.
	require.NoError(t, os.RemoveAll(filepath.Join(h.root, "workers")))
	require.NoError(t, os.Mkdir(filepath.Join(h.root, "workers"), 0700))
	h.reopen()
	require.Equal(t, workflow.JobRunCompleted, h.step(root).Status)
	require.Equal(t, reads, h.secretReads, "completed replay must not resolve secrets again")
	chapters, err := h.db.ListChapters(h.ctx, jobdb.ListChaptersRequest{JobKey: root})
	require.NoError(t, err)
	for _, chapter := range chapters {
		// Inspect actual payload bytes (JSON marshaling a []byte base64-encodes it).
		switch body := chapter.Body.(type) {
		case jobdb.JobStartChapter:
			require.NotContains(t, string(body.Input.Data), credential)
		case jobdb.TaskAttemptOutcomeChapter:
			if outcome, ok := body.Outcome.(jobdb.ApplicationOutputOutcome); ok {
				require.NotContains(t, string(outcome.Output.Data), credential)
			}
		case jobdb.ExecutionChapter:
			require.NotContains(t, string(body.Record), credential)
		}
	}
	raw, err := json.Marshal(result["diagnostics"])
	require.NoError(t, err)
	var ref objects.Ref
	require.NoError(t, json.Unmarshal(raw, &ref))
	store := objects.NewStore(objects.Config{JobKey: root, Workdir: t.TempDir(), GetArtifact: func(key jobdb.ArtifactKey) (jobdb.Artifact, error) {
		stored, err := h.db.OpenArtifact(h.ctx, jobdb.ArtifactRef{JobKey: root, Ordinal: int64(key.TaskOrdinal), Name: key.Name})
		if err != nil {
			return nil, err
		}
		reader, err := stored.Open()
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		return jobdb.NewArtifactFromBytes(key.Name, data), nil
	}})
	snapshot, err := store.Open(h.ctx, ref, "c2ops.gha.diagnostics/v1")
	require.NoError(t, err, "diagnostics must hydrate after the worker directory is deleted")
	defer snapshot.Close()
	data, err := os.ReadFile(snapshot.Files["log"])
	require.NoError(t, err)
	require.NotContains(t, string(data), credential)
	require.Contains(t, string(data), "recorded fixture log")
}

func TestActIndependentChildrenCanOverlap(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	var arrivals atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if arrivals.Add(1) == 2 {
			releaseOnce.Do(func() { close(release) })
		}
		select {
		case <-release:
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })
	barrier := "          node -e 'fetch(process.env.BARRIER).then(r => { if (!r.ok) process.exit(1) }).catch(() => process.exit(1))'\n"
	wf := strings.Replace(fanInWorkflow, "jobs:\n", "env:\n  BARRIER: "+server.URL+"\njobs:\n", 1)
	wf = strings.ReplaceAll(wf, "          touch ", barrier+"          touch ")
	h := newActHarness(t, wf)
	root := h.submit(fanInRecipe, nil)
	require.Equal(t, workflow.JobRunSuspended, h.step(root).Status)
	var wg sync.WaitGroup
	// The parent has durably submitted both children. Run only these two leases
	// concurrently; neither fixture step can finish before the other arrives.
	for _, job := range h.jobs() {
		if job.JobKey == root {
			continue
		}
		wg.Add(1)
		go func(key jobdb.JobKey) { defer wg.Done(); h.step(key) }(job.JobKey)
	}
	wg.Wait()
	require.EqualValues(t, 2, arrivals.Load())
	result := h.drain(root)["result"].(map[string]any)
	require.Equal(t, "success", result["conclusion"])
}

func TestActFailedChildrenRemainCollectableForReporting(t *testing.T) {
	for _, tc := range []struct{ condition, want string }{{"", "skipped"}, {"    if: always()\n", "success"}} {
		t.Run(tc.want, func(t *testing.T) {
			wf := strings.Replace(fanInWorkflow, "          touch a-ran", "          exit 7", 1)
			wf = strings.Replace(wf, "  combine:\n", "  combine:\n"+tc.condition, 1)
			h := newActHarness(t, wf)
			root := h.submit(fanInRecipe, nil)
			out := h.drain(root)
			builds := out["builds"].([]any)
			first := builds[0].(map[string]any)["outputs"].(map[string]any)["result"].(map[string]any)
			require.Equal(t, "failure", first["conclusion"])
			result := out["result"].(map[string]any)
			require.Equal(t, tc.want, result["conclusion"])
			if tc.want == "skipped" {
				require.Empty(t, result["outputs"])
			}
		})
	}
}
