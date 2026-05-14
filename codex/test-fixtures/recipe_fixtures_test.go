package testfixtures_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	gitexport "github.com/colony-2/c2j/pkg/git/export"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	testfixtures "github.com/colony-2/c2j/pkg/worker/test-fixtures"
	workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	toyruntime "github.com/colony-2/swf-go/pkg/swf/runtime/toy"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var registerFixtureOpsOnce sync.Once

func TestRecipeFixtures(t *testing.T) {
	ensureFixtureOps()
	installStubCodex(t)

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

func installStubCodex(t *testing.T) {
	t.Helper()

	stubDir := t.TempDir()
	stubPath := filepath.Join(stubDir, "codex")
	stub := `#!/usr/bin/env bash
set -euo pipefail
prompt="${@: -1}"
cmd="$(printf '%s\n' "$prompt" | sed -n "s/^bash -lc '\\(.*\\)'$/\\1/p" | head -n 1)"
if [[ -z "${cmd}" ]]; then
  echo "missing fixture command in prompt" >&2
  exit 1
fi
bash -lc "$cmd"
printf '%s\n' '{"type":"session.created","session_id":"fixture-session"}'
printf '%s\n' '{"type":"item.completed","item":{"item_type":"assistant_message","text":"{\"status\":\"completed\",\"assistantSummary\":\"fixture completed\",\"incompleteReason\":\"\",\"incompleteCategory\":\"\",\"pendingDependencies\":[],\"errorMessage\":\"\"}"}}'
`
	require.NoError(t, os.WriteFile(stubPath, []byte(stub), 0o755))

	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, key := range []string{"GOMODCACHE", "GOPATH", "GOCACHE", "HOME"} {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			value = strings.TrimSpace(goEnvValue(t, key))
		}
		if value != "" {
			t.Setenv(key, value)
		}
	}
}

func goEnvValue(t *testing.T, key string) string {
	t.Helper()

	output, err := exec.Command("go", "env", key).CombinedOutput()
	require.NoError(t, err, string(output))
	return strings.TrimSpace(string(output))
}

func fixtureOpEnv(t *testing.T) map[string]string {
	t.Helper()

	env := map[string]string{
		"PATH": os.Getenv("PATH"),
	}
	for _, key := range []string{"GOMODCACHE", "GOPATH", "GOCACHE", "HOME"} {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			value = goEnvValue(t, key)
		}
		if value != "" {
			env[key] = value
		}
	}
	return env
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

	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()

	def, err := recipe.LoadRecipeFromReader(file)
	require.NoError(t, err)
	return *def
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
	require.NoError(t, copyDirTree(opRootDir(t), filepath.Join(repoDir, filepath.Base(opRootDir(t))), map[string]bool{
		".git":          true,
		"test-fixtures": true,
	}))
	require.NoError(t, injectFixtureManifestEnv(filepath.Join(repoDir, filepath.Base(opRootDir(t)), "op.yaml"), fixtureOpEnv(t)))

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

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	destPath := filepath.Join(repoDir, filepath.FromSlash(path.Join(compiler.CellRecipeDirectory, relPath)))
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0o644)
}

func copyDirTree(sourceDir string, targetDir string, skip map[string]bool) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if skip[entry.Name()] {
			continue
		}

		sourcePath := filepath.Join(sourceDir, entry.Name())
		targetPath := filepath.Join(targetDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := copyDirTree(sourcePath, targetPath, skip); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(targetPath, data, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func injectFixtureManifestEnv(manifestPath string, extraEnv map[string]string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}

	var manifest map[string]any
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return err
	}

	env, _ := manifest["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	for key, value := range extraEnv {
		env[key] = value
	}
	manifest["env"] = env

	updated, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, updated, 0o644)
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
		Actor: contextual.ActorContext{
			TicketID:   "TEST-TICKET",
			ActorName:  "test-actor",
			ActorEmail: "test-actor@colony2",
		},
		Workflow: contextual.WorkflowContext{
			CellName: "cells/test-cell",
			CellPath: "cells/test-cell",
		},
		GitBase: contextual.GitBaseContext{
			BaseRepo:         baseRepo,
			BaseRef:          baseHash,
			ResolvedBaseHash: baseHash,
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
		if jobOverride.Workflow.CellName != "" {
			jobCtx.Workflow.CellName = jobOverride.Workflow.CellName
		}
		if jobOverride.Workflow.CellPath != "" {
			jobCtx.Workflow.CellPath = jobOverride.Workflow.CellPath
		}
	}
	if gitOverride != nil {
		if gitOverride.ParentRef != "" {
			gitCtx.ParentRef = gitOverride.ParentRef
		}
	}
	return jobCtx, gitCtx
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
	mu    sync.Mutex
	names map[string]bool
}

func newArtifactCapture() *artifactCapture {
	return &artifactCapture{names: make(map[string]bool)}
}

func (c *artifactCapture) add(artifacts []swf.Artifact) {
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
	inner   swf.TaskWorker
	capture *artifactCapture
}

func (c *capturingTaskWorker) Name() string {
	return c.inner.Name()
}

func (c *capturingTaskWorker) Run(ctx swf.TaskContext, input swf.TaskData) (swf.TaskData, error) {
	output, err := c.inner.Run(ctx, input)
	if output != nil {
		if artifacts, artErr := output.GetArtifacts(); artErr == nil {
			c.capture.add(artifacts)
		}
	}
	return output, err
}

func wrapTaskWorkers(workers map[string]swf.TaskWorker, capture *artifactCapture) map[string]swf.TaskWorker {
	wrapped := make(map[string]swf.TaskWorker, len(workers))
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
) (map[string]interface{}, []string, []string, error) {
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

	capture := newArtifactCapture()
	workset.TaskWorkers = wrapTaskWorkers(workset.TaskWorkers, capture)

	taskWorkers := make([]swf.TaskWorker, 0, len(workset.TaskWorkers))
	for _, worker := range workset.TaskWorkers {
		taskWorkers = append(taskWorkers, worker)
	}
	engine, err := swf.NewEngineBuilder().
		WithRuntime(toyruntime.New()).
		PlusWorkers(workset.JobWorker, taskWorkers...).
		BuildEngine()
	if err != nil {
		return nil, nil, nil, err
	}
	go engine.Run(ctx)
	control.Engine = engine

	jobKey, err := control.StartJob(ctx, workflowctl.StartJob{
		TenantId:   "default",
		RecipeName: recipeDef.GetMetadata().ID,
		Inputs:     inputs,
		JobContext: jobCtx,
		GitRef:     gitRef,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if err := swf.WaitForJobToComplete(ctx, 2*time.Minute, jobKey, engine); err != nil {
		return nil, nil, nil, err
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
