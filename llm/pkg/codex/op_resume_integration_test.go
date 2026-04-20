package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
	"github.com/stretchr/testify/require"
)

type resumeSequenceOutput struct {
	Op2Summary            string `json:"op2_summary"`
	Op2Status             string `json:"op2_status"`
	Op2IncompleteReason   string `json:"op2_incomplete_reason"`
	Op2IncompleteCategory string `json:"op2_incomplete_category"`
	Op1SessionID          string `json:"op1_session_id"`
}

func TestCodexOpResumeSequence(t *testing.T) {
	ensureCodexRequired(t)
	t.Setenv("VIBETHIS_CODEX_USE_DIRECT", "1")
	// Codex may emit rollout-state warnings to stderr depending on local config/state.
	// This integration test asserts stderr is empty, so suppress that module's logs.
	t.Setenv("RUST_LOG", "codex_core::rollout::list=off")

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
id: codex-op-resume-sequence
sequence:
  - id: op1
    op: codex.exec
    inputs:
      prompt: |
        Respond ONLY with JSON matching the schema: status 'completed', assistantSummary 'stored', incompleteReason '', incompleteCategory '', errorMessage '', pendingDependencies [].
        Do not include any other text.
      cell_relative_path: %q
  - id: op2
    op: codex.exec
    inputs:
      sessionId: "{{ sequence.op1.outputs.sessionId }}"
      prompt: |
        Resume the previous session.
        Respond ONLY with JSON matching the schema: status 'completed', assistantSummary 'resumed', incompleteReason '', incompleteCategory '', errorMessage '', pendingDependencies [].
        Do not include any other text.
      cell_relative_path: %q
outputs:
  op2_summary: "{{ sequence.op2.outputs.assistantSummary }}"
  op2_status: "{{ sequence.op2.outputs.status }}"
  op2_incomplete_reason: "{{ sequence.op2.outputs.incompleteReason }}"
  op2_incomplete_category: "{{ sequence.op2.outputs.incompleteCategory }}"
  op1_session_id: "{{ sequence.op1.outputs.sessionId }}"
`, cellRel, cellRel)

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
	require.NoError(t, swf.WaitForJobToComplete(context.Background(), 2*time.Minute, key, engine))

	res, err := swfutil.JobResult(context.Background(), engine, key)
	require.NoError(t, err)

	raw, err := res.GetData()
	require.NoError(t, err)

	var output resumeSequenceOutput
	require.NoError(t, json.Unmarshal(raw, &output))
	if output.Op2Status != string(StatusCompleted) {
		t.Fatalf("op2 failed status=%s incomplete_reason=%q incomplete_category=%q summary=%q",
			output.Op2Status,
			output.Op2IncompleteReason,
			output.Op2IncompleteCategory,
			output.Op2Summary,
		)
	}
	require.NotEmpty(t, output.Op1SessionID)
	require.Equal(t, "resumed", output.Op2Summary)

	_ = res
}

func ensureCodexRequired(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Fatalf("codex CLI required for integration test but -short is set")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatalf("codex CLI required but unavailable: %v", err)
	}
}
