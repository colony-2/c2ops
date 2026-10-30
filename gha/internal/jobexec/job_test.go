package jobexec

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const dependencyWorkflow = `name: Fixture
on: workflow_dispatch
jobs:
  build:
    runs-on: local
    steps:
      - run: touch predecessor-ran
  unrelated:
    runs-on: local
    steps:
      - run: touch unrelated-ran
  combine:
    needs: build
    runs-on: local
    outputs:
      value: ${{ steps.result.outputs.value }}
    steps:
      - id: result
        env:
          FROM_BUILD: ${{ needs.build.outputs.value }}
        run: |
          test ! -e predecessor-ran
          test ! -e unrelated-ran
          printf 'value=%s\n' "$FROM_BUILD" >> "$GITHUB_OUTPUT"
`

func TestPlanPreservesIndividualDependencyEdges(t *testing.T) {
	jobs, err := Plan(dependencyWorkflow, "workflow_dispatch")
	require.NoError(t, err)
	require.Equal(t, []Job{{ID: "build"}, {ID: "combine", Needs: []string{"build"}}, {ID: "unrelated"}}, jobs)
}

func TestOnlySelectedJobRunsWithRecordedDependencyOutputs(t *testing.T) {
	workdir := t.TempDir()
	var logs bytes.Buffer
	result, err := Execute(t.Context(), Request{
		Workflow: dependencyWorkflow, JobID: "combine",
		Needs: map[string]Result{"build": {Conclusion: "success", Outputs: map[string]string{"value": "recorded"}}},
	}, Options{Workdir: workdir, StateDir: t.TempDir(), Log: &logs})
	require.NoError(t, err, logs.String())
	require.Equal(t, "success", result.Conclusion, logs.String())
	require.Equal(t, "recorded", result.Outputs["value"])
	for _, name := range []string{"predecessor-ran", "unrelated-ran"} {
		_, err := os.Stat(filepath.Join(workdir, name))
		require.True(t, os.IsNotExist(err), name)
	}
}

func TestJobFailureSkipsDependentsButAllowsReporting(t *testing.T) {
	for _, tc := range []struct{ condition, want string }{{"", "skipped"}, {"    if: always()\n", "success"}} {
		t.Run(tc.want, func(t *testing.T) {
			wf := strings.Replace(dependencyWorkflow, "  combine:\n", "  combine:\n"+tc.condition, 1)
			result, err := Execute(t.Context(), Request{Workflow: wf, JobID: "combine", Needs: map[string]Result{
				"build": {Conclusion: "failure", Outputs: map[string]string{"value": "failed"}},
			}}, Options{Workdir: t.TempDir(), StateDir: t.TempDir()})
			require.NoError(t, err)
			require.Equal(t, tc.want, result.Conclusion)
			if tc.want == "skipped" {
				require.Empty(t, result.Outputs, "skipped outputs must not contain unevaluated expressions")
			}
		})
	}
}

func TestExplicitMatrixAssignment(t *testing.T) {
	wf := `on: workflow_dispatch
jobs:
  build:
    runs-on: local
    strategy:
      matrix:
        version: [20, 24]
        include:
          - version: 24
            label: current
    outputs:
      selected: ${{ steps.build.outputs.selected }}
    steps:
      - id: build
        env:
          VERSION: ${{ matrix.version }}
          LABEL: ${{ matrix.label }}
        run: echo "selected=$VERSION-$LABEL" >> "$GITHUB_OUTPUT"
`
	for _, tc := range []struct {
		name    string
		matrix  map[string]any
		wantErr string
	}{
		{"missing", nil, "explicit matrix"},
		{"unknown", map[string]any{"version": 99}, "matrix assignment"},
		{"selected", map[string]any{"version": float64(24), "label": "current"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Execute(t.Context(), Request{Workflow: wf, JobID: "build", Matrix: tc.matrix}, Options{Workdir: t.TempDir(), StateDir: t.TempDir()})
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "success", result.Conclusion)
			require.Equal(t, "24-current", result.Outputs["selected"])
		})
	}
}

func TestLocalCompositeSecretsAndAmbientIsolation(t *testing.T) {
	// The fixture checks its own environment; no production credential is read.
	t.Setenv("GHA_AMBIENT_FIXTURE", "must-not-inherit")
	dir := t.TempDir()
	action := filepath.Join(dir, ".github", "actions", "fixture")
	require.NoError(t, os.MkdirAll(action, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(action, "action.yml"), []byte(`name: fixture
description: local composite fixture
outputs:
  value:
    value: ${{ steps.probe.outputs.value }}
runs:
  using: composite
  steps:
    - id: probe
      shell: bash
      run: |
        test -z "$GHA_AMBIENT_FIXTURE"
        test "$NAMED_SECRET" = "synthetic-private-key"
        test "$SETTING" = "configured"
        echo "$NAMED_SECRET"
        echo "::add-mask::dynamic-private-value"
        echo dynamic-private-value
        printf 'value<<END\nfirst\nsecond\nEND\n' >> "$GITHUB_OUTPUT"
`), 0600))
	wf := `on: workflow_dispatch
jobs:
  probe:
    runs-on: local
    env:
      NAMED_SECRET: ${{ secrets.CLIENT_KEY }}
      SETTING: ${{ vars.SETTING }}
    outputs:
      value: ${{ steps.probe.outputs.value }}
      unsafe: ${{ secrets.CLIENT_KEY }}
      dynamic: dynamic-private-value
    steps:
      - id: probe
        uses: ./.github/actions/fixture
`
	var logs bytes.Buffer
	result, err := Execute(t.Context(), Request{Workflow: wf, JobID: "probe", Secrets: map[string]string{"CLIENT_KEY": "synthetic-private-key"}, Vars: map[string]string{"SETTING": "configured"}}, Options{Workdir: dir, StateDir: t.TempDir(), Log: &logs})
	require.NoError(t, err, logs.String())
	require.Equal(t, "success", result.Conclusion, logs.String())
	require.Equal(t, map[string]string{"value": "first\nsecond"}, result.Outputs)
	require.NotContains(t, logs.String(), "synthetic-private-key")
	require.NotContains(t, logs.String(), "dynamic-private-value")
}

func TestSecretNamesAreCaseInsensitiveAndRejectCollisions(t *testing.T) {
	wf := `on: workflow_dispatch
jobs:
  probe:
    runs-on: local
    steps:
      - env:
          TOKEN: ${{ secrets.CLIENT_KEY }}
        run: test "$TOKEN" = synthetic-key
`
	result, err := Execute(t.Context(), Request{Workflow: wf, JobID: "probe", Secrets: map[string]string{"client_key": "synthetic-key"}}, Options{Workdir: t.TempDir(), StateDir: t.TempDir()})
	require.NoError(t, err)
	require.Equal(t, "success", result.Conclusion)
	_, err = Execute(t.Context(), Request{Workflow: wf, JobID: "probe", Secrets: map[string]string{"client_key": "one", "CLIENT_KEY": "two"}}, Options{Workdir: t.TempDir(), StateDir: t.TempDir()})
	require.ErrorContains(t, err, "case collision")
}

func TestTransitiveResultsAndCycles(t *testing.T) {
	wf := `on: workflow_dispatch
jobs:
  first:
    runs-on: local
    steps: [{run: 'exit 1'}]
  middle:
    needs: first
    runs-on: local
    steps: [{run: 'exit 1'}]
  last:
    needs: middle
    runs-on: local
    steps: [{run: 'true'}]
`
	opts := Options{Workdir: t.TempDir(), StateDir: t.TempDir()}
	_, err := Execute(t.Context(), Request{Workflow: wf, JobID: "last", Needs: map[string]Result{"middle": {Conclusion: "success"}}}, opts)
	require.ErrorContains(t, err, "missing dependency result for \"first\"")
	result, err := Execute(t.Context(), Request{Workflow: wf, JobID: "last", Needs: map[string]Result{
		"first": {Conclusion: "success"}, "middle": {Conclusion: "success"},
	}}, opts)
	require.NoError(t, err)
	require.Equal(t, "success", result.Conclusion)
	wf = strings.Replace(wf, "  first:\n", "  first:\n    needs: last\n", 1)
	_, err = Execute(t.Context(), Request{Workflow: wf, JobID: "last"}, opts)
	require.ErrorContains(t, err, "dependency cycle")
}

func TestLocalJavaScriptAction(t *testing.T) {
	dir := t.TempDir()
	action := filepath.Join(dir, "action")
	require.NoError(t, os.Mkdir(action, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(action, "action.yml"), []byte("name: local\ndescription: fixture\nruns:\n  using: node20\n  main: index.js\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(action, "index.js"), []byte(`require('fs').appendFileSync(process.env.GITHUB_OUTPUT, 'result=javascript\n');`), 0600))
	var logs bytes.Buffer
	result, err := Execute(t.Context(), Request{Workflow: `on: workflow_dispatch
jobs:
  test:
    runs-on: local
    outputs:
      value: ${{ steps.js.outputs.result }}
    steps:
      - id: js
        uses: ./action
`, JobID: "test"}, Options{Workdir: dir, StateDir: t.TempDir(), Log: &logs})
	require.NoError(t, err, logs.String())
	require.Equal(t, "success", result.Conclusion, logs.String())
	require.Equal(t, "javascript", result.Outputs["value"])
}

func TestExplicitEventContextSelectsPRJob(t *testing.T) {
	wf := `on: [push, pull_request]
jobs:
  pr:
    if: github.event_name == 'pull_request' && github.base_ref == 'main'
    runs-on: local
    outputs:
      identity: ${{ steps.identity.outputs.value }}
    steps:
      - id: identity
        run: |
          test "$GITHUB_EVENT_NAME" = pull_request
          test "$GITHUB_BASE_REF" = main
          test -f "$GITHUB_EVENT_PATH"
          node -e 'const e = JSON.parse(require("fs").readFileSync(process.env.GITHUB_EVENT_PATH)); if (e.pull_request.base.ref !== "main") process.exit(1)'
          echo "value=$GITHUB_REF" >> "$GITHUB_OUTPUT"
`
	for _, event := range []string{"pull_request", "push"} {
		t.Run(event, func(t *testing.T) {
			result, err := Execute(t.Context(), Request{Workflow: wf, JobID: "pr", EventName: event, Event: json.RawMessage(`{"ref":"refs/heads/feature","number":42,"pull_request":{"base":{"ref":"main"},"head":{"ref":"feature"}}}`)}, Options{Workdir: t.TempDir(), StateDir: t.TempDir()})
			require.NoError(t, err)
			if event == "push" {
				require.Equal(t, "skipped", result.Conclusion)
				return
			}
			require.Equal(t, "success", result.Conclusion)
			require.Equal(t, "refs/pull/42/merge", result.Outputs["identity"])
		})
	}
}

func TestLogRedactionMasksMultilineAndOverlappingValues(t *testing.T) {
	var logs bytes.Buffer
	logger := (logFactory{writer: &logs, secrets: []string{"prefix", "prefix-with-a-private-suffix", "line-one\nline-two"}}).WithJobLogger()
	logger.WithField("raw", "field-that-must-not-be-serialized").Info("prefix-with-a-private-suffix line-one line-two")
	require.Equal(t, "info: *** *** ***\n", logs.String())
}

func TestFailingShellIsRecordedAsFailure(t *testing.T) {
	result, err := Execute(t.Context(), Request{Workflow: "on: workflow_dispatch\njobs:\n  bad:\n    runs-on: local\n    steps:\n      - run: exit 7\n", JobID: "bad"}, Options{Workdir: t.TempDir(), StateDir: t.TempDir()})
	require.NoError(t, err, "a job failure must be collectable by the parent")
	require.Equal(t, "failure", result.Conclusion)
}

func TestMissingDependencyDoesNotRun(t *testing.T) {
	_, err := Execute(t.Context(), Request{Workflow: dependencyWorkflow, JobID: "combine"}, Options{Workdir: t.TempDir(), StateDir: t.TempDir()})
	require.ErrorContains(t, err, "missing dependency result")
}

func TestHostModeRejectsContainerFeaturesBeforeRunning(t *testing.T) {
	for _, fragment := range []string{"    container: alpine\n", "    services:\n      db:\n        image: postgres\n", "    steps:\n      - uses: docker://alpine\n"} {
		wf := "on: workflow_dispatch\njobs:\n  test:\n    runs-on: local\n" + fragment
		if !strings.Contains(fragment, "steps:") {
			wf += "    steps:\n      - run: touch should-not-run\n"
		}
		_, err := Execute(context.Background(), Request{Workflow: wf, JobID: "test"}, Options{Workdir: t.TempDir(), StateDir: t.TempDir()})
		require.ErrorContains(t, err, "container")
	}
}

func TestRejectsUnsupportedOrchestrationFeatures(t *testing.T) {
	for _, fragment := range []string{"permissions: {id-token: write}", "environment: production", "concurrency: release", "continue-on-error: true", "timeout-minutes: 1"} {
		wf := "on: workflow_dispatch\njobs:\n  test:\n    runs-on: local\n    " + fragment + "\n    steps: [{run: 'exit 99'}]\n"
		_, err := Execute(t.Context(), Request{Workflow: wf, JobID: "test"}, Options{Workdir: t.TempDir(), StateDir: t.TempDir()})
		require.ErrorContains(t, err, "requires orchestration")
	}
}
