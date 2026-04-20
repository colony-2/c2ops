package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

type multiSkillLoopOutput struct {
	RunCount             int    `json:"run_count"`
	Run1Status           string `json:"run1_status"`
	Run2Status           string `json:"run2_status"`
	Run3Status           string `json:"run3_status"`
	Run1Summary          string `json:"run1_summary"`
	Run2Summary          string `json:"run2_summary"`
	Run3Summary          string `json:"run3_summary"`
	Run1IncompleteReason string `json:"run1_incomplete_reason"`
	Run2IncompleteReason string `json:"run2_incomplete_reason"`
	Run3IncompleteReason string `json:"run3_incomplete_reason"`
}

func TestCodexOpMultiSkillStateMachineLoop(t *testing.T) {
	ensureCodexRequired(t)
	t.Setenv("VIBETHIS_CODEX_USE_DIRECT", "1")
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
	token := "amber-lion-42"
	repoPath, repoHash := createMultiSkillDelegationRepo(t, cellRel, token)

	recipeYaml := fmt.Sprintf(`
---
id: codex-op-multi-skill-loop
state:
  initial: codex_loop
  states:
    codex_loop:
      op: codex.exec
      inputs:
        sessionId: '${{ "codex_loop" in states ? states["codex_loop"].outputs.sessionId : "" }}'
        skill: "software-dev-orchestrator"
        skill_mode: "enforce"
        skill_selection_mode: "ordered"
        cell_relative_path: %q
        prompt: '${{ !("codex_loop" in states) ? "Start the software-development flow. previous_incomplete_reason=none. previous_summary=none. Execute exactly one delegated step, then return. Remember this token for follow-up turns: %s. Return only schema JSON." : "continue. previous_incomplete_reason=" + states["codex_loop"].outputs.incompleteReason + ". previous_summary=" + states["codex_loop"].outputs.assistantSummary + ". Execute exactly one delegated step, then return. Return only schema JSON." }}'
      transitions:
        - to: codex_loop
          when: 'outputs.status == "incomplete"'

outputs:
  run_count: '${{ size(states["codex_loop"].runs) + 1 }}'
  run1_status: '${{ states["codex_loop"].runs[0].outputs.status }}'
  run2_status: '${{ states["codex_loop"].runs[1].outputs.status }}'
  run3_status: '${{ states["codex_loop"].outputs.status }}'
  run1_summary: '${{ states["codex_loop"].runs[0].outputs.assistantSummary }}'
  run2_summary: '${{ states["codex_loop"].runs[1].outputs.assistantSummary }}'
  run3_summary: '${{ states["codex_loop"].outputs.assistantSummary }}'
  run1_incomplete_reason: '${{ states["codex_loop"].runs[0].outputs.incompleteReason }}'
  run2_incomplete_reason: '${{ states["codex_loop"].runs[1].outputs.incompleteReason }}'
  run3_incomplete_reason: '${{ states["codex_loop"].outputs.incompleteReason }}'
`, cellRel, token)

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
	jobCtx.GitBase.BaseRepo = repoPath
	jobCtx.GitBase.BaseRef = repoHash
	jobCtx.GitBase.ResolvedBaseHash = repoHash
	gitCtx.ParentRef = repoHash
	start := workflowctl.StartJob{
		TenantId:   "test-tenant",
		RecipeName: testRecipe.GetMetadata().ID,
		Inputs:     map[string]interface{}{},
		JobContext: jobCtx,
		GitRef:     repoHash,
	}

	key, err := starter.StartRecipeJob(context.Background(), start, engine, *testRecipe)
	require.NoError(t, err)
	require.NoError(t, swf.WaitForJobToComplete(context.Background(), 4*time.Minute, key, engine))

	res, err := swfutil.JobResult(context.Background(), engine, key)
	require.NoError(t, err)

	raw, err := res.GetData()
	require.NoError(t, err)

	var output multiSkillLoopOutput
	require.NoError(t, json.Unmarshal(raw, &output), "raw=%s", string(raw))

	require.Equal(t, 3, output.RunCount)

	require.Equal(t, string(StatusIncomplete), output.Run1Status)
	require.Equal(t, string(StatusIncomplete), output.Run2Status)
	require.Equal(t, string(StatusCompleted), output.Run3Status)

	require.Equal(t, "planning complete", output.Run1Summary)
	require.Equal(t, fmt.Sprintf("execution complete token:%s", token), output.Run2Summary)
	require.Equal(t, "validation complete", output.Run3Summary)

	require.Equal(t, "next:execute", output.Run1IncompleteReason)
	require.Equal(t, "next:validate", output.Run2IncompleteReason)
	require.Equal(t, "", output.Run3IncompleteReason)
}

func createMultiSkillDelegationRepo(t *testing.T, cellRel string, token string) (string, string) {
	t.Helper()

	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Test User")

	writeRepoFile(t, filepath.Join(repoDir, "README.md"), "multi-skill integration fixture\n")
	writeRepoFile(t, filepath.Join(repoDir, cellRel, "README.md"), "fixture cell\n")

	skillsDir := filepath.Join(repoDir, ".agents", "skills")
	writeRepoFile(t, filepath.Join(skillsDir, "software-dev-orchestrator", "SKILL.md"), fmt.Sprintf(`---
name: software-dev-orchestrator
description: Delegates software development phases to planning, execution, and validation skills.
---
# Software Development Orchestrator

Execute exactly one phase per invocation.

1. Determine phase from current user request fields:
   - if previous_incomplete_reason is none, phase is plan
   - if previous_incomplete_reason is next:execute, phase is execute
   - if previous_incomplete_reason is next:validate, phase is validate
   - if previous_summary is validation complete, return completed JSON with assistantSummary "already complete", empty incompleteReason, empty incompleteCategory
2. Delegate by invoking exactly one skill by name:
   - software-dev-plan
   - software-dev-execute
   - software-dev-validate
3. Do not run shell commands in this skill.
4. Return only schema JSON.
`))

	writeRepoFile(t, filepath.Join(skillsDir, "software-dev-plan", "SKILL.md"), fmt.Sprintf(`---
name: software-dev-plan
description: Planning phase for the software development workflow.
---
# Planning Skill

Return only schema JSON exactly:
{"status":"incomplete","assistantSummary":"planning complete","incompleteReason":"next:execute","incompleteCategory":"","errorMessage":"","pendingDependencies":[]}
`))

	writeRepoFile(t, filepath.Join(skillsDir, "software-dev-execute", "SKILL.md"), fmt.Sprintf(`---
name: software-dev-execute
description: Execution phase for the software development workflow.
---
# Execution Skill

Return only schema JSON exactly:
{"status":"incomplete","assistantSummary":"execution complete token:%s","incompleteReason":"next:validate","incompleteCategory":"","errorMessage":"","pendingDependencies":[]}
`, token))

	writeRepoFile(t, filepath.Join(skillsDir, "software-dev-validate", "SKILL.md"), fmt.Sprintf(`---
name: software-dev-validate
description: Validation phase for the software development workflow.
---
# Validation Skill

Return only schema JSON exactly:
{"status":"completed","assistantSummary":"validation complete","incompleteReason":"","incompleteCategory":"","errorMessage":"","pendingDependencies":[]}
`))

	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "seed multi-skill fixtures")
	hash := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "HEAD"))
	return repoDir, hash
}

func writeRepoFile(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, dir, args...)
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(output))
	return string(output)
}
