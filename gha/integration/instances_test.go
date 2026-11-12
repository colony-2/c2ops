package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Compare public IDs to actual broker submissions, independently of executor outputs.
func assertInstances(t *testing.T, h *harness, name string, job map[string]any, conclusions ...any) []any {
	t.Helper()
	instances := job["instances"].([]any)
	require.Len(t, instances, len(conclusions))
	refs := job["diagnostics"].([]any)
	require.Len(t, refs, len(instances))
	seen := map[string]bool{}
	for i, value := range instances {
		instance := value.(map[string]any)
		id := instance["execution_job_id"].(string)
		require.False(t, seen[id])
		seen[id] = true
		found := false
		for _, child := range h.jobs() {
			if child.JobKey.JobId != id {
				continue
			}
			var meta struct {
				Recipe string `json:"recipe"`
			}
			require.NoError(t, json.Unmarshal(child.Metadata, &meta))
			require.Equal(t, fmt.Sprintf("gha-%s-%d", name, i), meta.Recipe)
			found = true
		}
		require.True(t, found, "instance ID must name the submitted execution child")
		require.Equal(t, conclusions[i], instance["conclusion"])
		require.Equal(t, refs[i], instance["diagnostics"])
		require.Contains(t, instance, "diagnostics_unavailable")
		if refs[i] != nil {
			require.Nil(t, instance["diagnostics_unavailable"])
		}
	}
	return instances
}

func jobNamed(t *testing.T, h *harness, name string) (jobdb.JobKey, bool) {
	t.Helper()
	for _, job := range h.jobs() {
		var meta struct {
			Recipe string `json:"recipe"`
		}
		require.NoError(t, json.Unmarshal(job.Metadata, &meta))
		if meta.Recipe == name {
			return job.JobKey, true
		}
	}
	return jobdb.JobKey{}, false
}

// Pause before execution so tests can choose completion order or inject faults.
func planInstances(t *testing.T, h *harness, root jobdb.JobKey, logical string, count int) []jobdb.JobKey {
	t.Helper()
	h.step(root)
	for attempt := 0; attempt < 6; attempt++ {
		if coordinator, ok := jobNamed(t, h, "gha-"+logical); ok {
			h.step(coordinator)
		}
		ids := []jobdb.JobKey{}
		for i := 0; i < count; i++ {
			if child, ok := jobNamed(t, h, fmt.Sprintf("gha-%s-%d", logical, i)); ok {
				ids = append(ids, child)
			}
		}
		if len(ids) == count {
			return ids
		}
	}
	t.Fatal("instances were not submitted")
	return nil
}

func TestInstancesActualMixedConclusionsAndCompletionOrder(t *testing.T) {
	for _, order := range [][]int{{2, 1, 0}, {0, 1, 2}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			h := newHarness(t, nil)
			installJobFixture(t, h, `on: workflow_dispatch
jobs:
  build:
    if: vars.RUN_INSTANCE != 'b'
    runs-on: local
    strategy:
      fail-fast: false
      matrix: {os: [authored-os], shard: [a, b, c]}
    steps:
      - run: echo 'EXECUTED-${{ matrix.shard }}'
`)
			// The job condition is valid Actions syntax. Supply a per-assignment
			// fixture variable before invoking the REAL executor; no result is
			// fabricated. Production planning and collection remain untouched.
			wrapper := filepath.Join(h.root, "instance-vars")
			script := `#!/usr/bin/env node
const fs=require('fs'),cp=require('child_process');
const input=JSON.parse(fs.readFileSync(0,'utf8'));
const request=JSON.parse(Buffer.from(input.payload,'base64'));
request.vars={...request.vars,RUN_INSTANCE:request.matrix.shard};
input.payload=Buffer.from(JSON.stringify(request)).toString('base64');
const result=cp.spawnSync(` + fmt.Sprintf("%q", helperBinary) + `,[],{input:JSON.stringify(input),stdio:['pipe','inherit','inherit']});
process.exit(result.status ?? 1);
`
			require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
			setJobFixtureCommand(t, h, []string{wrapper}, false)
			installWorkflowFixture(t, h)
			root := h.submit(workflowRecipe(t, ""), nil)
			ids := planInstances(t, h, root, "build", 3)
			for _, index := range order {
				require.Equal(t, workflow.JobRunCompleted, h.step(ids[index]).Status)
			}
			run := h.drain(root)["run"].(map[string]any)
			job := run["jobs"].(map[string]any)["build"].(map[string]any)
			require.Equal(t, "success", run["status"])
			require.Equal(t, "success", job["conclusion"])
			instances := assertInstances(t, h, "build", job, "success", "skipped", "success")
			for i, value := range instances {
				instance := value.(map[string]any)
				shard := string(rune('a' + i))
				require.Equal(t, map[string]any{"os": "authored-os", "shard": shard}, instance["matrix"])
				require.Equal(t, []any{"local"}, instance["labels"])
				require.Equal(t, map[string]any{"platform": runtime.GOOS + "/" + runtime.GOARCH}, instance["execution_needs"])
				require.Equal(t, ids[i].JobId, instance["execution_job_id"])
				require.Equal(t, "completed", instance["status"])
				log := string(readDiagnostic(t, h, instance["diagnostics"]))
				if i == 1 {
					require.NotContains(t, log, "EXECUTED-b")
				} else {
					require.Contains(t, log, "EXECUTED-"+shard)
				}
			}
			count, calls := len(h.jobs()), len(h.calls)
			h.reopen()
			require.Equal(t, workflow.JobRunCompleted, h.step(root).Status)
			require.Equal(t, run, h.result(root)["run"])
			require.Len(t, h.jobs(), count)
			require.Len(t, h.calls, calls)
			saveEvidence(t, fmt.Sprintf("mixed-%d-result.json", order[0]), map[string]any{"result": run, "completion_order": order, "execution_job_ids": ids, "fixture": "actual gha-job executor; wrapper sets RUN_INSTANCE from matrix.shard before execution, triggering the authored vars condition; no output injection"})
		})
	}
}

func TestInstancesDuplicateTypedMatrices(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  build:
    runs-on: local
    strategy:
      fail-fast: false
      matrix:
        include:
          - {os: custom-os, n: 24, flag: true, nested: {items: [a, 2, false]}, nothing: null}
          - {os: custom-os, n: 24, flag: true, nested: {items: [a, 2, false]}, nothing: null}
    steps: [{run: 'echo typed-and-duplicate'}]
`)
	installWorkflowFixture(t, h)
	root := h.submit(workflowRecipe(t, ""), nil)
	run := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "success", run["status"])
	job := run["jobs"].(map[string]any)["build"].(map[string]any)
	instances := assertInstances(t, h, "build", job, "success", "success")
	want := map[string]any{"os": "custom-os", "n": float64(24), "flag": true, "nested": map[string]any{"items": []any{"a", float64(2), false}}, "nothing": nil}
	for _, value := range instances {
		require.Equal(t, want, value.(map[string]any)["matrix"])
	}
	saveEvidence(t, "duplicate-typed-result.json", map[string]any{"result": run, "fixture": "actual equal include assignments; multiplicity is intentional"})
}

func TestInstancesCompletedEmptyAndUnavailable(t *testing.T) {
	for _, matrix := range []bool{false, true} {
		t.Run(fmt.Sprint(matrix), func(t *testing.T) {
			h := newHarness(t, nil)
			config := "    if: false\n"
			if matrix {
				config = "    if: false\n    strategy:\n      fail-fast: false\n      matrix: {shard: [excluded], exclude: [{shard: excluded}]}\n"
			}
			installJobFixture(t, h, "on: workflow_dispatch\njobs:\n  empty:\n"+config+"    runs-on: local\n    steps: [{run: 'exit 99'}]\n")
			installWorkflowFixture(t, h)
			root := h.submit(workflowRecipe(t, ""), nil)
			run := h.drain(root)["run"].(map[string]any)
			job := run["jobs"].(map[string]any)["empty"].(map[string]any)
			require.Equal(t, "skipped", job["conclusion"])
			require.Equal(t, []any{}, job["instances"])
			saveEvidence(t, fmt.Sprintf("empty-%t-result.json", matrix), map[string]any{"result": run})
		})
	}
	for _, action := range []string{"expand", "collect"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t, nil)
			installJobFixture(t, h, "on: workflow_dispatch\njobs:\n  broken:\n    runs-on: local\n    steps: [{run: 'true'}]\n")
			// Controlled helper fault; executor is still the actual changed binary.
			script := "#!/usr/bin/env node\nconst fs=require('fs'), cp=require('child_process');const raw=fs.readFileSync(0,'utf8');if(JSON.parse(raw).action===" + fmt.Sprintf("%q", action) + "){process.exit(42)}const p=cp.spawnSync(" + fmt.Sprintf("%q", workflowBinary) + ",[],{input:raw,stdio:['pipe','inherit','inherit']});process.exit(p.status ?? 1)\n"
			path := filepath.Join(h.root, "fault-helper")
			require.NoError(t, os.WriteFile(path, []byte(script), 0700))
			installWorkflowFixture(t, h, path)
			root := h.submit(workflowRecipe(t, ""), nil)
			run := h.drain(root)["run"].(map[string]any)
			require.Equal(t, "failure", run["status"])
			job := run["jobs"].(map[string]any)["broken"].(map[string]any)
			require.NotContains(t, job, "instances")
			saveEvidence(t, "fault-"+action+"-result.json", map[string]any{"result": run, "fault_injection": "workflow helper exits 42 on " + action})
		})
	}
}

func TestInstancesMissingAndInvalidExecutorEvidence(t *testing.T) {
	for _, conclusion := range []string{"missing", "unknown", "malformed", "cancelled", "skipped", "success"} {
		t.Run(conclusion, func(t *testing.T) {
			h := newHarness(t, nil)
			installJobFixture(t, h, "on: workflow_dispatch\njobs:\n  broken:\n    runs-on: local\n    steps: [{run: 'exit 99'}]\n")
			result := map[string]any{"job_id": "broken", "outputs": map[string]any{}}
			switch conclusion {
			case "missing":
			case "unknown":
				result["conclusion"] = "unrecognized"
			case "malformed":
				result["conclusion"] = map[string]any{"bad": true}
			default:
				result["conclusion"] = conclusion
			}
			raw, err := json.Marshal(map[string]any{"output": result})
			require.NoError(t, err)
			// Controlled extension output injection; not positive execution evidence.
			manifestRaw, err := os.ReadFile(filepath.Join(h.repo, ".c2/test-ops/gha-job/op.yaml"))
			require.NoError(t, err)
			var manifest map[string]any
			require.NoError(t, json.Unmarshal(manifestRaw, &manifest))
			manifest["command"] = []string{"printf", "%s", string(raw)}
			manifest["output_schema"] = map[string]any{"type": "object"} // Allow deliberately invalid evidence to reach collection.
			encoded, err := json.Marshal(manifest)
			require.NoError(t, err)
			h.seed(map[string]string{".c2/test-ops/gha-job/op.yaml": string(encoded)})
			installWorkflowFixture(t, h)
			root := h.submit(workflowRecipe(t, ""), nil)
			run := h.drain(root)["run"].(map[string]any)
			job := run["jobs"].(map[string]any)["broken"].(map[string]any)
			if values, ok := job["instances"]; ok {
				instance := values.([]any)[0].(map[string]any)
				if conclusion != "success" {
					require.NotEqual(t, "success", instance["conclusion"])
				}
				require.Equal(t, "completed", instance["status"])
				require.Nil(t, instance["diagnostics"])
				require.Equal(t, "not_returned", instance["diagnostics_unavailable"])
				require.Equal(t, result["conclusion"], instance["conclusion"])
			} else {
				require.Contains(t, []string{"missing", "malformed"}, conclusion)
				require.Equal(t, "failure", run["status"])
			}
			saveEvidence(t, "fault-conclusion-"+conclusion+"-result.json", map[string]any{"result": run, "fault_injection": "synthetic executor output without diagnostics", "injected_output": result})
		})
	}
}

func setJobFixtureCommand(t *testing.T, h *harness, command []string, permissive bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.repo, ".c2/test-ops/gha-job/op.yaml"))
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(raw, &manifest))
	manifest["command"] = command
	if permissive {
		manifest["output_schema"] = map[string]any{"type": "object"}
	}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	h.seed(map[string]string{".c2/test-ops/gha-job/op.yaml": string(encoded)})
}

func TestInstancesOpaquePublicStringsReachAdmissionAndCollection(t *testing.T) {
	h := newHarness(t, nil)
	installJobFixture(t, h, `on: workflow_dispatch
jobs:
  literal:
    runs-on: ${{ vars.LABEL }}
    strategy:
      fail-fast: false
      matrix: ${{ fromJSON(vars.MATRIX) }}
    steps: [{run: 'exit 99'}]
`)
	installWorkflowFixture(t, h)
	literal := "${{literal}}"
	matrix := map[string]any{"os": "${{ context.git.repo }}", "nested": map[string]any{"string": "{{inputs.literal}}", "items": []any{true, float64(3), nil}}}
	matrixRaw, err := json.Marshal(map[string]any{"include": []any{matrix}})
	require.NoError(t, err)
	var recipe map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(workflowRecipe(t, "")), &recipe))
	recipe["input_schema"] = map[string]any{"vars_json": map[string]any{"type": "string"}, "profiles_json": map[string]any{"type": "string"}}
	recipe["inputs"] = map[string]any{"vars_json": "${{ inputs.vars_json }}", "profiles_json": "${{ inputs.profiles_json }}"}
	launch := recipe["sequence"].([]any)[0].(map[string]any)["inputs"].(map[string]any)
	launch["vars"] = "${{ json_parse(inputs.vars_json) }}"
	launch["runner_profiles"] = "${{ json_parse(inputs.profiles_json) }}"
	raw, err := yaml.Marshal(recipe)
	require.NoError(t, err)
	varsRaw, err := json.Marshal(map[string]any{"MATRIX": string(matrixRaw), "LABEL": literal})
	require.NoError(t, err)
	profilesRaw, err := json.Marshal([]any{map[string]any{"labels": []any{literal}, "execution_needs": map[string]any{"base": literal}}})
	require.NoError(t, err)
	root := h.submit(string(raw), map[string]any{"vars_json": string(varsRaw), "profiles_json": string(profilesRaw)})
	ids := planInstances(t, h, root, "literal", 1)
	outcome := h.step(ids[0])
	handoff, err := compiler.ExecutionHandoffFromOutcome(outcome)
	require.NoError(t, err)
	require.NotNil(t, handoff)
	require.Equal(t, literal, *handoff.Demand.Effective.Base)
	require.NoError(t, h.db.CancelJob(h.ctx, jobdb.CancelJobRequest{JobKey: ids[0], Reason: "opaque demand admission fixture; no matching base provisioned"}))
	run := h.drain(root)["run"].(map[string]any)
	job := run["jobs"].(map[string]any)["literal"].(map[string]any)
	instances := assertInstances(t, h, "literal", job, nil)
	instance := instances[0].(map[string]any)
	require.Equal(t, matrix, instance["matrix"])
	require.Equal(t, []any{literal}, instance["labels"])
	require.Equal(t, map[string]any{"base": literal}, instance["execution_needs"])
	require.Equal(t, "cancelled", instance["status"])
	saveEvidence(t, "opaque-result.json", map[string]any{"result": run, "admitted_base": *handoff.Demand.Effective.Base, "fixture": "actual generated metadata and execution demand admission; cancelled before execution because no literal base exists"})
}
