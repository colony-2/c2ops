package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	toyruntime "github.com/colony-2/swf-go/pkg/swf/runtime/toy"
	"github.com/stretchr/testify/require"
)

type jobIDGen struct {
	count int
	max   int
}

func (g *jobIDGen) Generate(tenantID string) (swf.JobKey, error) {
	g.count++
	if g.count > g.max {
		return swf.JobKey{}, fmt.Errorf("too many jobs")
	}
	return swf.JobKey{TenantId: tenantID, JobId: fmt.Sprintf("job-%d", g.count)}, nil
}

func newToyEngine(t *testing.T, gen func(string) (swf.JobKey, error)) swf.SWFEngine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := make([]toyruntime.Option, 0, 1)
	if gen != nil {
		opts = append(opts, toyruntime.WithJobIDGenerator(gen))
	}
	engine, err := swf.NewEngineBuilder().
		WithRuntime(toyruntime.New(opts...)).
		BuildEngine()
	require.NoError(t, err)
	go engine.Run(ctx)
	return engine
}

func TestCodexOpToyEngine(t *testing.T) {
	originalOps := coreops.List()
	t.Cleanup(func() {
		coreops.Clear()
		if len(originalOps) > 0 {
			coreops.Register(originalOps...)
		}
	})
	coreops.Clear()
	coreops.Register(GetOp())

	cellRel := filepath.Join("cells", "alpha")

	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "stdout.jsonl")
	stderrPath := filepath.Join(tempDir, "stderr.txt")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("{\"hello\":\"world\"}\n"), 0o644))
	require.NoError(t, os.WriteFile(stderrPath, []byte(""), 0o644))

	executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
		return Result{
			Status:              StatusCompleted,
			SessionID:           "sess-123",
			AssistantSummary:    "all good",
			PendingDependencies: []Dependency{},
		}, stdoutPath, stderrPath, tempDir, nil
	}
	t.Cleanup(func() { executeLibrary = Execute })

	recipeYaml := fmt.Sprintf(`
---
id: codex-op-toy
op: codex.exec
inputs:
  prompt: "do something"
  cell_relative_path: %q
`, cellRel)
	testRecipe, err := recipe.LoadRecipeFromString([]byte(recipeYaml))
	require.NoError(t, err)

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	g := jobIDGen{max: 1}
	engine := newToyEngine(t, g.Generate)

	wf := workflow.SWFWorkflowControl{Engine: engine}
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(&wf).Build()
	workSet, err := compiler.NewRecipeWorker(deps, registry, nil)
	require.NoError(t, err)
	workSet.JobWorker = &artifactValidatingJobWorker{inner: workSet.JobWorker, t: t}
	require.NoError(t, engine.RegisterWorkers(workSet))

	jobCtx, gitCtx := compiler.GenerateTestContext()
	jobCtx.Workflow.CellName = cellRel
	jobCtx.Workflow.CellPath = cellRel
	start := workflowctl.StartJob{
		TenantId:   "test-tenant",
		RecipeName: testRecipe.GetMetadata().ID,
		Inputs:     map[string]interface{}{},
		JobContext: jobCtx,
		GitRef:     gitCtx.ParentRef,
	}

	key, err := starter.StartRecipeJob(context.Background(), start, engine, *testRecipe)
	require.NoError(t, err)
	require.NoError(t, swf.WaitForJobToComplete(context.Background(), 30*time.Second, key, engine))

	res, err := swfutil.JobResult(context.Background(), engine, key)
	require.NoError(t, err)

	raw, err := res.GetData()
	require.NoError(t, err)

	var output ExecOpOutput
	require.NoError(t, json.Unmarshal(raw, &output))
	require.Equal(t, string(StatusCompleted), output.Status)
	require.Equal(t, "sess-123", output.SessionID)
	require.Equal(t, "all good", output.AssistantSummary)
}

func TestCodexOpToyEngineCLIOutput(t *testing.T) {
	ensureCodexAvailable(t)
	t.Setenv("VIBETHIS_CODEX_USE_DIRECT", "1")

	originalOps := coreops.List()
	t.Cleanup(func() {
		coreops.Clear()
		if len(originalOps) > 0 {
			coreops.Register(originalOps...)
		}
	})
	coreops.Clear()
	coreops.Register(GetOp())

	cellRel := filepath.Join("cells", "alpha")

	recipeYaml := fmt.Sprintf(`
---
id: codex-op-toy-cli
op: codex.exec
inputs:
  prompt: "Respond strictly with JSON matching the schema: status 'completed', assistantSummary 'All good', incompleteReason '', incompleteCategory '', errorMessage '', pendingDependencies []."
  cell_relative_path: %q
`, cellRel)
	testRecipe, err := recipe.LoadRecipeFromString([]byte(recipeYaml))
	require.NoError(t, err)

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	g := jobIDGen{max: 1}
	engine := newToyEngine(t, g.Generate)

	wf := workflow.SWFWorkflowControl{Engine: engine}
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(&wf).Build()
	workSet, err := compiler.NewRecipeWorker(deps, registry, nil)
	require.NoError(t, err)
	require.NoError(t, engine.RegisterWorkers(workSet))

	jobCtx, gitCtx := compiler.GenerateTestContext()
	jobCtx.Workflow.CellName = cellRel
	jobCtx.Workflow.CellPath = cellRel
	start := workflowctl.StartJob{
		TenantId:   "test-tenant",
		RecipeName: testRecipe.GetMetadata().ID,
		Inputs:     map[string]interface{}{},
		JobContext: jobCtx,
		GitRef:     gitCtx.ParentRef,
	}

	key, err := starter.StartRecipeJob(context.Background(), start, engine, *testRecipe)
	require.NoError(t, err)
	require.NoError(t, swf.WaitForJobToComplete(context.Background(), 30*time.Second, key, engine))

	res, err := swfutil.JobResult(context.Background(), engine, key)
	require.NoError(t, err)

	raw, err := res.GetData()
	require.NoError(t, err)

	var output ExecOpOutput
	require.NoError(t, json.Unmarshal(raw, &output))
	if output.Status != string(StatusCompleted) {
		t.Fatalf("codex error: raw=%s", string(raw))
	}
	require.Equal(t, "All good", output.AssistantSummary)
}
