package integration

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func installWorkflowFixture(t *testing.T, h *harness, command ...string) {
	raw, err := os.ReadFile("../workflow/op.json")
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(raw, &manifest))
	manifest["command"] = []string{workflowBinary}
	if len(command) > 0 {
		manifest["command"] = command
	}
	delete(manifest, "dependencies")
	raw, err = json.Marshal(manifest)
	require.NoError(t, err)
	h.seed(map[string]string{".c2/test-ops/gha-workflow/op.yaml": string(raw)})
}

func workflowRecipe(t *testing.T, extra string) string {
	return localRecipe(t, fmt.Sprintf(`id: full-workflow
sequence:
  - id: launch
    op: nix:github:colony-2/c2ops/published#gha-workflow
    inputs:
      workflow: .github/workflows/fixture.yml
      helper_op: nix:github:colony-2/c2ops/published#gha-workflow
      executor_op: nix:github:colony-2/c2ops/published#gha-job
      runner_profiles:
        - labels: [local]
          execution_needs: {platform: %s/%s}
%s
  - id: done
    op: recipe.await_result
    inputs:
      target: {job: '${{ sequence.launch.outputs.job_id }}'}
      wait: {on: job}
outputs:
  run: '${{ sequence.done.outputs.outputs }}'
`, runtime.GOOS, runtime.GOARCH, extra))
}
func TestSubmittedWorkflowRunsWholeDAGAndSurvivesRestart(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, fanInWorkflow)
	installWorkflowFixture(t, h)
	root := h.submit(workflowRecipe(t, ""), nil)
	require.Equal(t, workflow.JobRunSuspended, h.step(root).Status)
	require.Len(t, h.jobs(), 5, "caller, three logical jobs, result collector")
	h.reopen()
	result := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "success", result["status"])
	jobs := result["jobs"].(map[string]any)
	require.Equal(t, "alpha-beta", jobs["combine"].(map[string]any)["outputs"].(map[string]any)["combined"])
	count := len(h.jobs())
	calls := len(h.calls)
	h.reopen()
	require.Equal(t, result, h.result(root)["run"])
	require.Equal(t, workflow.JobRunCompleted, h.step(root).Status)
	require.Len(t, h.jobs(), count)
	require.Len(t, h.calls, calls)
}

func TestSubmittedWorkflowDynamicMatrixAndNamedSecret(t *testing.T) {
	h := newHarness(t, nil)
	h.secrets["build_key"] = "synthetic-workflow-key"
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  plan:
    runs-on: local
    outputs:
      matrix: ${{ steps.out.outputs.matrix }}
    steps:
      - id: out
        run: echo 'matrix={"version":[20,24]}' >> "$GITHUB_OUTPUT"
  build:
    needs: plan
    runs-on: local
    strategy:
      fail-fast: false
      matrix: ${{ fromJSON(needs.plan.outputs.matrix) }}
    steps:
      - env:
          TOKEN: ${{ secrets.CLIENT_KEY }}
          VERSION: ${{ matrix.version }}
        run: |
          test "$TOKEN" = synthetic-workflow-key
          test "$VERSION" = 20 -o "$VERSION" = 24
  combine:
    needs: build
    runs-on: local
    steps:
      - run: echo joined
`)
	installWorkflowFixture(t, h)
	root := h.submit("perms: {jobdb: {jobs: {submit: true}, secrets: {resolve: [build_key]}}}\n"+workflowRecipe(t, "      secret_bindings: {CLIENT_KEY: build_key}"), nil)
	result := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "success", result["status"])
	require.Len(t, h.jobs(), 12, "caller + 3 coordinators + root collector + 4 matrix instances + 3 per-job collectors")
	reads := h.secretReads
	require.Positive(t, reads)
	h.reopen()
	require.Equal(t, workflow.JobRunCompleted, h.step(root).Status)
	require.Equal(t, reads, h.secretReads)
}
func TestSubmittedWorkflowFailureStillRunsReporter(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  build:
    runs-on: local
    steps: [{run: 'exit 7'}]
  skipped:
    needs: build
    runs-on: local
    steps: [{run: 'exit 9'}]
  report:
    needs: build
    if: always()
    runs-on: local
    steps: [{run: 'echo reported'}]
`)
	installWorkflowFixture(t, h)
	root := h.submit(workflowRecipe(t, ""), nil)
	run := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "failure", run["status"])
	jobs := run["jobs"].(map[string]any)
	for name, want := range map[string]string{"build": "failure", "skipped": "skipped", "report": "success"} {
		require.Equal(t, want, jobs[name].(map[string]any)["conclusion"])
	}
}
func TestSubmittedWorkflowFiltersPushBranches(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, `on:
  push:
    branches: [main]
jobs:
  build:
    runs-on: local
    steps: [{run: 'exit 9'}]
`)
	installWorkflowFixture(t, h)
	root := h.submit(workflowRecipe(t, "      event_name: push\n      event: {ref: refs/heads/feature}"), nil)
	run := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "skipped", run["status"])
	require.Empty(t, run["jobs"])
	require.Len(t, h.jobs(), 2)
}

func TestSubmittedWorkflowTransfersV4ArtifactsAfterWorkerDeletion(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.CreateHeader(&zip.FileHeader{Name: "result.txt", Method: zip.Store})
	require.NoError(t, err)
	_, err = file.Write([]byte("durable artifact bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	h := newHarness(t, nil)
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  upload:
    runs-on: local
    steps:
      - run: node upload.js
  download:
    needs: upload
    runs-on: local
    outputs:
      received: ${{ steps.download.outputs.received }}
    steps:
      - id: download
        run: node download.js
`)
	shared := `const base=process.env.ACTIONS_RESULTS_URL+'twirp/github.actions.results.api.v1.ArtifactService/';
async function rpc(method,extra){const response=await fetch(base+method,{method:'POST',headers:{Authorization:'Bearer '+process.env.ACTIONS_RUNTIME_TOKEN,'Content-Type':'application/json'},body:JSON.stringify({workflowRunBackendId:'1',workflowJobRunBackendId:'1',...extra})});if(!response.ok)throw new Error(await response.text());return response.json()}
`
	h.seed(map[string]string{
		"upload.js":   shared + fmt.Sprintf(`(async()=>{const data=Buffer.from(%q,'base64');const created=await rpc('CreateArtifact',{name:'build',version:4});const put=await fetch(created.signedUploadUrl+'&comp=block',{method:'PUT',body:data});if(!put.ok)throw new Error(await put.text());await rpc('FinalizeArtifact',{name:'build',size:String(data.length)});})().catch(e=>{console.error(e);process.exit(1)});`, base64.StdEncoding.EncodeToString(archive.Bytes())),
		"download.js": shared + `(async()=>{const listed=await rpc('ListArtifacts',{});if(listed.artifacts.length!==1)throw new Error('missing artifact');const url=await rpc('GetSignedArtifactURL',{name:'build'});const r=await fetch(url.signedUrl);const data=Buffer.from(await r.arrayBuffer());if(!data.includes(Buffer.from('durable artifact bytes')))throw new Error('wrong artifact bytes');require('fs').appendFileSync(process.env.GITHUB_OUTPUT,'received=yes\n');})().catch(e=>{console.error(e);process.exit(1)});`,
	})
	installWorkflowFixture(t, h)
	root := h.submit(workflowRecipe(t, ""), nil)
	require.Equal(t, workflow.JobRunSuspended, h.step(root).Status)
	// Advance only upload's coordinator and its execution/result children.
	for iteration := 0; iteration < 10; iteration++ {
		for _, job := range h.jobs() {
			var meta struct {
				Recipe string `json:"recipe"`
			}
			require.NoError(t, json.Unmarshal(job.Metadata, &meta))
			if strings.HasPrefix(meta.Recipe, "gha-upload") {
				h.step(job.JobKey)
			}
		}
	}
	require.NoError(t, os.RemoveAll(filepath.Join(h.root, "workers")))
	require.NoError(t, os.Mkdir(filepath.Join(h.root, "workers"), 0700))
	h.reopen()
	run := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "success", run["status"])
	jobs := run["jobs"].(map[string]any)
	require.Equal(t, "yes", jobs["download"].(map[string]any)["outputs"].(map[string]any)["received"])
}

func installGHAFixture(t *testing.T, h *harness) {
	raw, err := os.ReadFile("../op.json")
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(raw, &manifest))
	manifest["command"] = []string{ghaBinary}
	delete(manifest, "dependencies")
	raw, err = json.Marshal(manifest)
	require.NoError(t, err)
	h.seed(map[string]string{".c2/test-ops/gha/op.yaml": string(raw)})
}

func TestGHABackendC2J(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, fanInWorkflow)
	installWorkflowFixture(t, h)
	installGHAFixture(t, h)
	recipe := strings.Replace(workflowRecipe(t, "      backend: c2j"), "op: ./.c2/test-ops/gha-workflow", "op: ./.c2/test-ops/gha", 1)
	root := h.submit(recipe, nil)
	result := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "success", result["status"])
}

// CI enables this on both native architectures. Ordinary test-fast runs retain
// their offline, container-free contract.
func TestSubmittedWorkflowStandardArtifactActions(t *testing.T) {
	if os.Getenv("C2OPS_TEST_GHA_ACTIONS") != "1" {
		t.Skip("set C2OPS_TEST_GHA_ACTIONS=1 to fetch pinned public actions")
	}
	h := newHarness(t, nil)
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  upload:
    runs-on: local
    steps:
      - run: echo durable-standard-action > result.txt
      - uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02
        with:
          name: result
          path: result.txt
  download:
    needs: upload
    runs-on: local
    outputs:
      received: ${{ steps.check.outputs.received }}
    steps:
      - uses: actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093
        with:
          name: result
          path: incoming
      - id: check
        run: |
          test "$(cat incoming/result.txt)" = durable-standard-action
          echo received=yes >> "$GITHUB_OUTPUT"
`)
	installWorkflowFixture(t, h)
	root := h.submit(workflowRecipe(t, "      event: {repository: {full_name: fixture/test, name: test, owner: {login: fixture}}}"), nil)
	result := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "success", result["status"])
	jobs := result["jobs"].(map[string]any)
	require.Equal(t, "yes", jobs["download"].(map[string]any)["outputs"].(map[string]any)["received"])
}

func TestSubmittedWorkflowTransfersUnpublishedSource(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  read:
    runs-on: local
    steps:
      - run: test "$(cat candidate.txt)" = unpublished
`)
	installWorkflowFixture(t, h)
	recipe := strings.Replace(workflowRecipe(t, ""), "sequence:\n", `sequence:
    - id: change
      op: command_execution
      inputs:
        run: echo unpublished > candidate.txt
`, 1)
	root := h.submit(recipe, nil)
	result := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "success", result["status"])
	require.NotEqual(t, h.commit, result["revision"])
	require.NoFileExists(t, filepath.Join(h.repo, "candidate.txt"))
}

func TestSubmittedWorkflowDuplicateBrokerSubmissions(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		jobs         int
	}{
		{"dependencies", fanInWorkflow, 11},
		{"matrix", `on: workflow_dispatch
jobs:
  build:
    runs-on: local
    strategy:
      fail-fast: false
      matrix:
        arch: [amd64, arm64]
        version: [20, 24]
    outputs:
      value: ${{ steps.build.outputs.value }}
    steps:
      - id: build
        env:
          ARCH: ${{ matrix.arch }}
          VERSION: ${{ matrix.version }}
        run: |
          test "$ARCH" = amd64 -o "$ARCH" = arm64
          test "$VERSION" = 20 -o "$VERSION" = 24
          echo value=done >> "$GITHUB_OUTPUT"
`, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			installJobFixture(t, h, tc.source)
			wrapper := filepath.Join(h.root, "repeat.sh")
			require.NoError(t, os.WriteFile(wrapper, []byte(`set -e
payload=$(cat)
printf '%s' "$payload" | "$1" > /dev/null
printf '%s' "$payload" | "$1"
`), 0600))
			installWorkflowFixture(t, h, "bash", wrapper, workflowBinary)
			root := h.submit(workflowRecipe(t, ""), nil)
			result := h.drain(root)["run"].(map[string]any)
			require.Equal(t, "success", result["status"])
			require.Len(t, h.jobs(), tc.jobs, "repeating each broker invocation must reuse every logical job, shard and collector")
		})
	}
}

func TestSubmittedWorkflowPRAndPushConditions(t *testing.T) {
	for _, event := range []string{"pull_request", "push"} {
		t.Run(event, func(t *testing.T) {
			h := newHarness(t, nil)
			installJobFixture(t, h, `on:
  pull_request: {branches: [main]}
  push: {branches: [main]}
jobs:
  pr:
    if: github.event_name == 'pull_request' && github.base_ref == 'main'
    runs-on: local
    steps:
      - run: |
          test "$GITHUB_EVENT_NAME" = pull_request
          test "$GITHUB_BASE_REF" = main
          test "$GITHUB_HEAD_REF" = topic
          node -e 'const e=JSON.parse(require("fs").readFileSync(process.env.GITHUB_EVENT_PATH)); if(e.number !== 42 || e.action !== "synchronize") process.exit(1)'
  push:
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    runs-on: local
    steps:
      - run: test "$GITHUB_EVENT_NAME:$GITHUB_REF" = push:refs/heads/main
`)
			installWorkflowFixture(t, h)
			metadata := "      event_name: push\n      event: {ref: refs/heads/main}"
			if event == "pull_request" {
				metadata = "      event_name: pull_request\n      event: {number: 42, pull_request: {base: {ref: main}, head: {ref: topic}}}"
			}
			root := h.submit(workflowRecipe(t, metadata), nil)
			result := h.drain(root)["run"].(map[string]any)
			require.Equal(t, "success", result["status"])
			jobs := result["jobs"].(map[string]any)
			selected, skipped := "push", "pr"
			if event == "pull_request" {
				selected, skipped = "pr", "push"
			}
			require.Equal(t, "success", jobs[selected].(map[string]any)["conclusion"])
			require.Equal(t, "skipped", jobs[skipped].(map[string]any)["conclusion"])
		})
	}
}
