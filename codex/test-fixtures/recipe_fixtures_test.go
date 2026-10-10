package testfixtures_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	gitexport "github.com/colony-2/c2j/pkg/git/export"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/objects"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	testfixtures "github.com/colony-2/c2j/pkg/worker/test-fixtures"
	workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var registerFixtureOpsOnce sync.Once

func TestRecipeFixtures(t *testing.T) {
	ensureFixtureOps()
	stubCodex := installStubCodex(t)

	testFiles, err := filepath.Glob(filepath.Join(fixturesRootDir(t), "recipes", "*.test.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, testFiles)

	for _, testFile := range testFiles {
		recipeName := strings.TrimSuffix(filepath.Base(testFile), ".test.yaml")
		recipePath := filepath.Join(fixturesRootDir(t), "recipes", recipeName+".yaml")

		t.Run(recipeName, func(t *testing.T) {
			activityRegistry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			deps := coreops.NewServiceDepsBuilder().Build()

			testCases := loadTestCases(t, testFile)
			recipeDef := loadRecipe(t, recipePath)

			repoPath, repoHash, cleanup := createFixtureRepo(t, recipePath, testCases.Recipes)
			t.Cleanup(cleanup)

			registry := buildRecipeRegistry(t, recipePath, recipeDef, testCases.Recipes, repoPath, repoHash)

			for _, tc := range testCases.Tests {
				tc := tc
				t.Run(tc.Name, func(t *testing.T) {
					jobCtx, gitCtx := generateTestContext(repoPath, repoHash, tc.JobContext, tc.GitContext)
					result, artifacts, jobArtifacts, err := executeRecipeWithArtifacts(
						context.Background(),
						activityRegistry,
						recipeDef,
						tc.Inputs,
						jobCtx,
						gitCtx.ParentRef,
						registry,
						deps,
						t.TempDir(),
						stubCodex,
					)

					if tc.WantErr {
						require.Error(t, err)
						if tc.WantErrContains != "" {
							require.Contains(t, err.Error(), tc.WantErrContains)
						}
						return
					}

					require.NoError(t, err)
					for key, want := range tc.Want {
						require.Equal(t, want, result[key], "unexpected output for %s", key)
					}
					assertArtifactNames(t, tc.WantArtifacts, artifacts)
					assertArtifactNames(t, tc.WantJobArtifacts, jobArtifacts)
					for name := range publicArtifactNames(result["public_artifacts"]) {
						require.False(t, objects.IsInternalArtifact(name), "checkpoint leaked into recipe artifacts")
						require.NotContains(t, name, "codex-home-state")
					}
					if value, exists := result["session"]; exists {
						ref, ok, err := objects.Parse(value)
						require.NoError(t, err)
						require.True(t, ok, "root output must forward a durable reference")
						require.Equal(t, "c2ops.codex.session/v1", ref.Type)
					}
				})
			}
		})
	}
}

func ensureFixtureOps() {
	registerFixtureOpsOnce.Do(func() {
		coreops.Register(gitexport.GetAll()...)
		coreops.Register(extops.GetExecutionOp())
		coreops.Register(commandop.GetOp())
	})
}

func installStubCodex(t *testing.T) string {
	t.Helper()

	stubDir := t.TempDir()
	stubPath := filepath.Join(stubDir, "codex")
	stub := `#!/usr/bin/env bash
set -euo pipefail
if [[ ${1:-} == --version ]]; then echo codex-cli 0.157.1; exit 0; fi
prompt="${@: -1}"
cmd="$(printf '%s\n' "$prompt" | sed -n "s/^bash -lc '\\(.*\\)'$/\\1/p" | head -n 1)"
if [[ -z "${cmd}" ]]; then
  echo "missing fixture command in prompt" >&2
  exit 1
fi
python3 - <<'PYCODE'
import os, sqlite3
from pathlib import Path
home = Path(os.environ['CODEX_HOME'])
(home / 'sessions').mkdir(exist_ok=True)
rollout = home / 'sessions/fixture-session.jsonl'
if not rollout.exists():
    rollout.write_text('original')
for name in ['state_5', 'thread_history_1', 'goals_1', 'queue_1', 'memories_1']:
    with sqlite3.connect(str(home / (name + '.sqlite'))) as db:
        db.execute('CREATE TABLE IF NOT EXISTS threads(id TEXT PRIMARY KEY, rollout_path TEXT, cwd TEXT)')
        db.execute('INSERT OR IGNORE INTO threads VALUES (?, ?, ?)', ('fixture-session', str(rollout), os.getcwd()))
PYCODE
bash -lc "$cmd"
printf '%s\n' '{"type":"session.created","session_id":"fixture-session"}'
printf '%s\n' '{"type":"item.completed","item":{"item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"fixture completed\",\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"pendingDependencies\":[],\"errorMessage\":\"\"}"}}'
`
	require.NoError(t, os.WriteFile(stubPath, []byte(stub), 0o755))

	return stubPath
}

func fixturesRootDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Dir(file)
}

func opRootDir(t *testing.T) string {
	t.Helper()
	return filepath.Clean(filepath.Join(fixturesRootDir(t), ".."))
}

func loadTestCases(t *testing.T, path string) testfixtures.TestCases {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cases testfixtures.TestCases
	require.NoError(t, yaml.Unmarshal(data, &cases))
	return cases
}

func loadRecipe(t *testing.T, path string) recipe.Recipe {
	t.Helper()
	def, err := recipe.LoadRecipeFromReader(bytes.NewReader(fixtureRecipeYAML(t, path)))
	require.NoError(t, err)
	return *def
}

func fixtureRecipeYAML(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var authored any
	require.NoError(t, yaml.Unmarshal(data, &authored))
	var override func(any)
	override = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if coordinate, ok := node["op"].(string); ok && coordinate != "command_execution" {
				require.True(t, strings.HasPrefix(coordinate, "nix:github:colony-2/c2ops/main#"), "fixture must use a public op coordinate: %s", coordinate)
				output, err := exec.Command("python3", filepath.Join(opRootDir(t), "..", "scripts", "op_test.py"),
					"reference", coordinate).CombinedOutput()
				require.NoError(t, err, string(output))
				node["op"] = strings.TrimSpace(string(output))

			}
			for _, child := range node {
				override(child)
			}
		case []any:
			for _, child := range node {
				override(child)
			}
		}
	}
	override(authored)
	data, err = yaml.Marshal(authored)
	require.NoError(t, err)
	return data
}

func createFixtureRepo(t *testing.T, primaryPath string, secondaryPaths []string) (string, string, func()) {
	t.Helper()

	repoDir, err := os.MkdirTemp("", "codex-fixture-repo-*")
	require.NoError(t, err)

	cleanup := func() {
		_ = os.RemoveAll(repoDir)
	}

	require.NoError(t, initFixtureRepo(repoDir))
	require.NoError(t, copyRecipeIntoFixtureRepo(t, repoDir, primaryPath, fixtureRecipeRelPath(t, primaryPath)))
	for _, relPath := range secondaryPaths {
		sourcePath := filepath.Join(fixturesRootDir(t), "recipes", relPath)
		require.NoError(t, copyRecipeIntoFixtureRepo(t, repoDir, sourcePath, filepath.ToSlash(relPath)))
	}
	skillDir := filepath.Join(repoDir, ".agents", "skills", "checkpoint-test")
	require.NoError(t, os.MkdirAll(skillDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: checkpoint-test\ndescription: Test checkpoint continuation.\n---\nFollow the supplied command.\n"), 0644))

	require.NoError(t, runGit(repoDir, "init"))
	require.NoError(t, runGit(repoDir, "config", "user.email", "test@example.com"))
	require.NoError(t, runGit(repoDir, "config", "user.name", "Test User"))
	require.NoError(t, runGit(repoDir, "add", "."))
	require.NoError(t, runGit(repoDir, "commit", "-m", "add codex fixture repo"))

	output, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(output))

	return repoDir, strings.TrimSpace(string(output)), cleanup
}

func initFixtureRepo(dir string) error {
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("initial\n"), 0o644); err != nil {
		return err
	}
	for _, rel := range []string{"cells/test-cell", "cells/alpha", "cells/beta", "cells/cell-a"} {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(full, "README.md"), []byte(rel+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func fixtureRecipeRelPath(t *testing.T, recipePath string) string {
	t.Helper()

	relPath, err := filepath.Rel(filepath.Join(fixturesRootDir(t), "recipes"), recipePath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return filepath.Base(recipePath)
	}
	return filepath.ToSlash(relPath)
}

func copyRecipeIntoFixtureRepo(t *testing.T, repoDir string, sourcePath string, relPath string) error {
	t.Helper()

	data := fixtureRecipeYAML(t, sourcePath)
	destPath := filepath.Join(repoDir, filepath.FromSlash(path.Join(compiler.CellRecipeDirectory, relPath)))
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0o644)
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v failed: %w (%s)", args, err, out)
	}
	return nil
}

func defaultTestContext(baseRepo string, baseHash string) (contextual.JobContext, contextual.GitCommitContext) {
	job := contextual.JobContext{
		GitBase: contextual.GitBaseContext{
			BaseRepo:         baseRepo,
			BaseRef:          baseHash,
			ResolvedBaseHash: baseHash,
			GitAuthor:        "Test User <test@example.com>",
		},
	}

	gitCtx := contextual.GitCommitContext{
		ParentRef: baseHash,
	}
	return job, gitCtx
}

func generateTestContext(baseRepo string, baseHash string, jobOverride *contextual.JobContext, gitOverride *contextual.GitCommitContext) (contextual.JobContext, contextual.GitCommitContext) {
	jobCtx, gitCtx := defaultTestContext(baseRepo, baseHash)
	if jobOverride != nil {
		mergeNonZeroStructFields(&jobCtx.Workflow, jobOverride.Workflow)
		mergeNonZeroStructFields(&jobCtx.GitBase, jobOverride.GitBase)
	}
	if gitOverride != nil {
		if gitOverride.ParentRef != "" {
			gitCtx.ParentRef = gitOverride.ParentRef
		}
	}
	return jobCtx, gitCtx
}

func mergeNonZeroStructFields(dst any, src any) {
	dstValue := reflect.ValueOf(dst)
	if dstValue.Kind() != reflect.Pointer || dstValue.IsNil() {
		return
	}
	dstElem := dstValue.Elem()
	srcValue := reflect.ValueOf(src)
	if dstElem.Kind() != reflect.Struct || srcValue.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < srcValue.NumField(); i++ {
		srcField := srcValue.Field(i)
		if !srcField.CanInterface() || srcField.IsZero() {
			continue
		}
		dstField := dstElem.FieldByName(srcValue.Type().Field(i).Name)
		if dstField.IsValid() && dstField.CanSet() && srcField.Type().AssignableTo(dstField.Type()) {
			dstField.Set(srcField)
		}
	}
}

func buildFixtureRecipeSelector(repositorySource string, recipeRelPath string, ref string) (string, error) {
	repoSource, err := compiler.NormalizeGitRepositorySource(repositorySource)
	if err != nil {
		return "", err
	}
	recipePath := path.Join(compiler.CellRecipeDirectory, filepath.ToSlash(recipeRelPath))
	return fmt.Sprintf("git+%s//%s@%s", repoSource, recipePath, ref), nil
}

func buildRecipeRegistry(
	t *testing.T,
	primaryPath string,
	primary recipe.Recipe,
	secondaryPaths []string,
	repositorySource string,
	ref string,
) workflow.RecipeProjectProvider {
	t.Helper()

	recipes := map[string]*recipe.Recipe{}
	addRecipe := func(sourcePath string, relPath string, def recipe.Recipe) {
		recipes[def.GetMetadata().ID] = &def
		selector, err := buildFixtureRecipeSelector(repositorySource, relPath, ref)
		require.NoError(t, err)
		recipes[selector] = &def
	}

	addRecipe(primaryPath, fixtureRecipeRelPath(t, primaryPath), primary)
	for _, relPath := range secondaryPaths {
		sourcePath := filepath.Join(fixturesRootDir(t), "recipes", relPath)
		addRecipe(sourcePath, filepath.ToSlash(relPath), loadRecipe(t, sourcePath))
	}

	return func(_ string, recipeRef string) (*recipe.Recipe, error) {
		def, ok := recipes[recipeRef]
		if !ok {
			return nil, fmt.Errorf("unknown recipe %s", recipeRef)
		}
		return def, nil
	}
}

type artifactCapture struct {
	mu           sync.Mutex
	names        map[string]bool
	nixPackages  map[string]bool
	bindingsRoot string
	stubCodex    string
}

func newArtifactCapture(bindingsRoot, stubCodex string) *artifactCapture {
	return &artifactCapture{names: make(map[string]bool), nixPackages: make(map[string]bool), bindingsRoot: bindingsRoot, stubCodex: stubCodex}
}

func (c *artifactCapture) add(artifacts []jobdb.Artifact) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, artifact := range artifacts {
		c.names[artifact.Name()] = true
	}
}

func (c *artifactCapture) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.names))
	for name := range c.names {
		out = append(out, name)
	}
	return out
}

type capturingTaskWorker struct {
	inner   jobworkflow.TaskWorker
	capture *artifactCapture
}

func (c *capturingTaskWorker) Name() string {
	return c.inner.Name()
}

func (c *capturingTaskWorker) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	output, err := c.inner.Run(ctx, input)
	if err == nil && output != nil && c.inner.Name() == workerops.ToolSetupTaskType {
		data, readErr := output.GetData()
		var setup workerops.ToolSetupResult
		if readErr == nil && json.Unmarshal(data, &setup) == nil && setup.Extension != nil &&
			setup.Extension.Nix != nil && setup.Ready() {
			// Keep real dependency preparation, but replace the downstream model
			// client in a private binding directory. Never modify c2j's shared cache.
			if setup.Environment == nil {
				return nil, fmt.Errorf("Codex fixture is missing its prepared tool environment")
			}
			bindings, bindErr := c.fixtureBindings(setup.Environment)
			if bindErr != nil {
				return nil, bindErr
			}
			setup.Environment.Path = bindings
			if !setup.Ready() {
				return nil, fmt.Errorf("fixture tool environment is not ready")
			}
			output, err = jobdb.NewTaskData(setup)
			if err != nil {
				return nil, err
			}
			c.capture.mu.Lock()
			c.capture.nixPackages[setup.Extension.Nix.StorePath] = true
			c.capture.mu.Unlock()
		}
	}
	if output != nil {
		if artifacts, artErr := output.GetArtifacts(); artErr == nil {
			c.capture.add(artifacts)
		}
	}
	return output, err
}

func (c *capturingTaskWorker) fixtureBindings(prepared *toolenv.Environment) (string, error) {
	entries, err := os.ReadDir(prepared.Path)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(c.capture.bindingsRoot, "tools-")
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() == "codex" || entry.Name() == "pnpm" {
			continue
		}
		if err := os.Symlink(filepath.Join(prepared.Path, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			return "", err
		}
	}
	// Retain c2j's real qualified dispatcher, replacing only the prepared
	// client's absolute target. Poison the bare name to catch PATH regressions.
	client := ""
	for _, tool := range prepared.Tools {
		if strings.HasPrefix(tool.Reference, "pnpm:@openai/codex@") {
			client = filepath.Join(tool.Bin, "codex")
		}
	}
	if client == "" {
		return "", fmt.Errorf("missing prepared Codex package")
	}
	runner, err := os.ReadFile(filepath.Join(prepared.Path, "pnpm"))
	if err != nil {
		return "", err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	patched := strings.ReplaceAll(string(runner), quote(client), quote(c.capture.stubCodex))
	if patched == string(runner) {
		return "", fmt.Errorf("prepared dispatcher did not bind the declared Codex client")
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm"), []byte(patched), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\necho 'unexpected bare codex invocation' >&2\nexit 127\n"), 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func wrapTaskWorkers(workers map[string]jobworkflow.TaskWorker, capture *artifactCapture) map[string]jobworkflow.TaskWorker {
	wrapped := make(map[string]jobworkflow.TaskWorker, len(workers))
	for name, worker := range workers {
		wrapped[name] = &capturingTaskWorker{inner: worker, capture: capture}
	}
	return wrapped
}

func executeRecipeWithArtifacts(
	ctx context.Context,
	registry *workerops.ActivityRegistry,
	recipeDef recipe.Recipe,
	inputs map[string]interface{},
	jobCtx contextual.JobContext,
	gitRef string,
	recipeRegistry workflow.RecipeProjectProvider,
	deps coreops.ServiceDependencies2,
	bindingsRoot, stubCodex string,
) (map[string]interface{}, []string, []string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	rootResolver := compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{
		RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(func(projectID string, recipeRef string) (*recipe.Recipe, error) {
			return recipeRegistry(projectID, recipeRef)
		}),
	})

	control := &workflow.SWFWorkflowControl{
		Registry:                      recipeRegistry,
		PreferRuntimeRecipeResolution: true,
	}

	deps = coreops.NewServiceDepsBuilder().WithWorkflowControl(control).WithDatabase(deps.Database()).WithSSEManager(deps.SSEManager()).Build()
	workset, err := compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{
		RootSourceResolver: rootResolver,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	capture := newArtifactCapture(bindingsRoot, stubCodex)
	workset.TaskWorkers = wrapTaskWorkers(workset.TaskWorkers, capture)

	taskWorkers := make([]jobworkflow.TaskWorker, 0, len(workset.TaskWorkers))
	for _, worker := range workset.TaskWorkers {
		taskWorkers = append(taskWorkers, worker)
	}
	fixtureRuntime := toyruntime.New()
	engine, err := jobworkflow.NewEngineBuilder().
		WithRuntime(fixtureRuntime).
		WithWorkerTenantId("default").
		PlusWorkers(workset.JobWorker, taskWorkers...).
		BuildEngine()
	if err != nil {
		return nil, nil, nil, err
	}
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: fixtureRuntime}
	go engine.Run(ctx)
	control.Engine = engine

	jobKey, err := control.StartJob(ctx, workflowctl.StartJob{
		ToolSetupVersion: 1,
		TenantId:         "default",
		RecipeName:       recipeDef.GetMetadata().ID,
		Inputs:           inputs,
		JobContext:       jobCtx,
		GitRef:           gitRef,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if err := jobworkflow.WaitForJobToComplete(ctx, 5*time.Minute, jobKey, engine); err != nil {
		return nil, nil, nil, err
	}

	capture.mu.Lock()
	prepared := len(capture.nixPackages)
	capture.mu.Unlock()
	if prepared == 0 {
		return nil, nil, nil, fmt.Errorf("fixture did not prepare a Nix op")
	}

	out, err := swfutil.JobResult(ctx, engine, jobKey)
	if err != nil {
		return nil, nil, nil, err
	}
	jobArtifacts, err := out.GetArtifacts()
	if err != nil {
		return nil, nil, nil, err
	}
	jobArtifactNames := make([]string, 0, len(jobArtifacts))
	for _, artifact := range jobArtifacts {
		jobArtifactNames = append(jobArtifactNames, artifact.Name())
	}

	data, err := out.GetData()
	if err != nil {
		return nil, nil, nil, err
	}
	outMap := make(map[string]interface{})
	if err := yaml.Unmarshal(data, &outMap); err != nil {
		return nil, nil, nil, err
	}
	return outMap, capture.list(), jobArtifactNames, nil
}

func assertArtifactNames(t *testing.T, expected []string, actual []string) {
	t.Helper()

	seen := make(map[string]bool, len(actual))
	for _, name := range actual {
		seen[name] = true
	}
	for _, name := range expected {
		require.True(t, seen[name], "missing expected artifact: %s", name)
	}
}

func publicArtifactNames(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}
