package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func strptr(s string) *string { return &s }
func nativeBase() string {
	if runtime.GOOS == "darwin" {
		return "macos-sequoia-15.7.7-arm64"
	}
	return "ubuntu-26.04-" + runtime.GOARCH
}
func setAllocation(h *harness, cpu, memory string) {
	h.allocation = &execution.Allocation{SchemaVersion: 2, Base: nativeBase(), Resources: execution.Resources{CPU: strptr(cpu), Memory: strptr(memory)}}
	h.reopen()
}

func TestGHAJobNegotiatesThenResumesSameTask(t *testing.T) {
	h := newHarness(t, nil)
	setAllocation(h, "2", "8G")
	marker := filepath.Join(h.root, "executions")
	installJobFixture(t, h, fmt.Sprintf(`on: workflow_dispatch
jobs:
  test:
    runs-on: local
    steps:
      - run: echo executed >> %q
`, marker))
	root := h.submit(localRecipe(t, fmt.Sprintf(`id: negotiate
sequence:
  - id: before
    op: command_execution
    inputs: {run: 'echo before >> %s'}
  - id: execute
    op: nix:github:colony-2/c2ops/published#gha-job
    retry: {maximum_attempts: 1}
    catch:
      - when: true
        continue: {outputs: {caught: true}}
    inputs:
      workflow: .github/workflows/fixture.yml
      job_id: test
      execution_required:
        base: %s
        resources: {cpu: '16', memory: '64G'}
outputs:
  result: '${{ sequence.execute.outputs }}'
`, marker, nativeBase())), nil)
	out := h.step(root)
	handoff, err := compiler.ExecutionHandoffFromOutcome(out)
	require.NoError(t, err)
	require.NotNil(t, handoff)
	require.Equal(t, nativeBase(), *handoff.Demand.Effective.Base)
	require.Equal(t, "64G", *handoff.Demand.Effective.Resources.Memory)
	content, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "before\n", string(content))
	jobs := len(h.jobs())
	// A still-insufficient resumed worker cannot execute the job.
	setAllocation(h, "8", "32G")
	out = h.step(root)
	handoff, err = compiler.ExecutionHandoffFromOutcome(out)
	require.NoError(t, err)
	require.NotNil(t, handoff)
	content, err = os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "before\n", string(content))
	// Delete all process-local directories before another DB reopen.
	workers := filepath.Join(h.root, "workers")
	require.NoError(t, os.RemoveAll(workers))
	require.NoError(t, os.Mkdir(workers, 0700))
	setAllocation(h, "16", "64G")
	result := h.drain(root)["result"].(map[string]any)
	require.Equal(t, "success", result["conclusion"])
	require.NotNil(t, result["diagnostics"])
	content, err = os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "before\nexecuted\n", string(content))
	require.Len(t, h.jobs(), jobs)
	calls := len(h.calls)
	h.reopen()
	require.Equal(t, workflow.JobRunCompleted, h.step(root).Status)
	require.Len(t, h.calls, calls)
}

func TestNegotiatedWorkflowCatalogAndCustomMatrix(t *testing.T) {
	h := newHarness(t, nil)
	setAllocation(h, "96", "384G")
	label := "ubuntu-24.04-16core"
	if runtime.GOARCH == "arm64" {
		label = "ubuntu-24.04-arm-16core"
	}
	if runtime.GOOS == "darwin" {
		label = "macos-15-xlarge"
	}
	installJobFixture(t, h, fmt.Sprintf(`on: workflow_dispatch
jobs:
  test:
    runs-on: {labels: '${{ matrix.runner }}'}
    strategy:
      fail-fast: false
      matrix:
        runner: [%s, custom]
    steps:
      - run: echo hello
`, label))
	installWorkflowFixture(t, h)
	recipe := localRecipe(t, fmt.Sprintf(`id: negotiated-workflow
sequence:
  - id: launch
    op: nix:github:colony-2/c2ops/published#gha-workflow
    inputs:
      workflow: .github/workflows/fixture.yml
      helper_op: nix:github:colony-2/c2ops/published#gha-workflow
      executor_op: nix:github:colony-2/c2ops/published#gha-job
      backend: c2j
      execution_mode: negotiate
      runner_profiles:
        - labels: [custom]
          execution_required: {base: %s, resources: {cpu: '8', memory: '32G'}}
  - id: done
    op: recipe.await_result
    inputs:
      target: {job: '${{ sequence.launch.outputs.job_id }}'}
      wait: {on: job}
outputs:
  run: '${{ sequence.done.outputs.outputs }}'
`, nativeBase()))
	// Test public gha entrypoint propagation as well as its helper.
	installGHAFixture(t, h)
	recipe = strings.Replace(recipe, "op: ./.c2/test-ops/gha-workflow", "op: ./.c2/test-ops/gha", 1)
	root := h.submit(recipe, nil)
	result := h.drain(root)["run"].(map[string]any)
	require.Equal(t, "success", result["status"])
	job := result["jobs"].(map[string]any)["test"].(map[string]any)
	instances := assertInstances(t, h, "test", job, "success", "success")
	for _, value := range instances {
		instance := value.(map[string]any)
		requirements := instance["execution_needs"].(map[string]any)
		require.Equal(t, nativeBase(), requirements["base"])
		resources := requirements["resources"].(map[string]any)
		if instance["matrix"].(map[string]any)["runner"] == "custom" {
			require.Equal(t, "8", resources["cpu"])
			require.Equal(t, "32G", resources["memory"])
		} else if runtime.GOOS == "darwin" {
			require.Equal(t, "5", resources["cpu"])
			require.Equal(t, "14G", resources["memory"])
		} else {
			require.Equal(t, "16", resources["cpu"])
			require.Equal(t, "64G", resources["memory"])
		}
	}
}
