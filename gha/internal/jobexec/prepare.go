package jobexec

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/nektos/act/pkg/common"
	"github.com/nektos/act/pkg/exprparser"
	"github.com/nektos/act/pkg/model"
	"github.com/nektos/act/pkg/runner"
	"gopkg.in/yaml.v3"
)

type Assignment struct {
	Matrix map[string]any `json:"matrix"`
	Labels []string       `json:"labels"`
}

func Dependencies(source, id string) ([]string, error) {
	wf, err := model.ReadWorkflow(strings.NewReader(source), false)
	if err != nil {
		return nil, err
	}
	return dependencyIDs(wf, id)
}
func Validate(source, id, workdir string) error {
	if err := validateFeatures(source, id); err != nil {
		return err
	}
	wf, err := model.ReadWorkflow(strings.NewReader(source), false)
	if err != nil {
		return err
	}
	job := wf.GetJob(id)
	if job == nil {
		return fmt.Errorf("unknown job %q", id)
	}
	if job.Uses != "" || job.RawContainer.Kind != 0 || len(job.Services) > 0 {
		return fmt.Errorf("job %s: reusable workflows and job/service containers are unsupported", id)
	}
	if job.Strategy != nil {
		if job.Strategy.FailFastString != "false" {
			return fmt.Errorf("job %s: distributed matrix currently requires strategy.fail-fast: false", id)
		}
		if job.Strategy.MaxParallelString != "" {
			return fmt.Errorf("job %s: strategy.max-parallel is not supported", id)
		}
	}
	for _, step := range job.Steps {
		if step == nil {
			return fmt.Errorf("empty step")
		}
		if strings.HasPrefix(step.Uses, "actions/checkout@") && len(step.With) > 0 {
			return fmt.Errorf("checkout options are unsupported with a frozen source snapshot")
		}
		if err := validateAction(workdir, step.Uses, map[string]bool{}); err != nil {
			return err
		}
	}
	_, err = dependencyIDs(wf, id)
	return err
}

// Prepare uses act's evaluator after dependencies have completed. No secret
// value is present in this context; secrets cannot select runners or matrices.
func Prepare(ctx context.Context, in Request, workdir string) ([]Assignment, error) {
	ctx = common.WithLogger(ctx, logFactory{writer: io.Discard}.WithJobLogger())
	newContext := func(matrix map[string]any) (*runner.RunContext, error) {
		wf, err := model.ReadWorkflow(strings.NewReader(in.Workflow), false)
		if err != nil {
			return nil, err
		}
		wf.File = "workflow.yml"
		for id, result := range in.Needs {
			if j := wf.GetJob(id); j != nil {
				j.Result = result.Conclusion
				j.Outputs = copyMap(result.Outputs)
			}
		}
		rc := &runner.RunContext{Config: &runner.Config{Workdir: workdir, EventName: in.EventName, Env: copyMap(in.Env), Vars: in.Vars, GitHubInstance: "github.com", Actor: "c2j"}, Run: &model.Run{Workflow: wf, JobID: in.JobID}, EventJSON: string(in.Event), Matrix: matrix, StepResults: map[string]*model.StepResult{}}
		rc.ExprEval = rc.NewExpressionEvaluator(ctx)
		return rc, nil
	}
	rc, err := newContext(nil)
	if err != nil {
		return nil, err
	}
	job := rc.Run.Job()
	if job == nil {
		return nil, fmt.Errorf("unknown job %q", in.JobID)
	}
	enabled, err := runner.EvalBool(ctx, rc.ExprEval, job.If.Value, exprparser.DefaultStatusCheckSuccess)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	matrices := []map[string]any{nil}
	if job.Strategy != nil {
		if err := rc.ExprEval.EvaluateYamlNode(ctx, &job.Strategy.RawMatrix); err != nil {
			return nil, err
		}
		if err := validateMatrixShape(job.Strategy.RawMatrix); err != nil {
			return nil, err
		}
		matrices, err = job.GetMatrixes()
		if err != nil {
			return nil, err
		}
	}
	if len(matrices) > 256 {
		return nil, fmt.Errorf("matrix exceeds 256 combinations")
	}
	out := make([]Assignment, 0, len(matrices))
	for _, matrix := range matrices {
		rc, err := newContext(matrix)
		if err != nil {
			return nil, err
		}
		if err := rc.ExprEval.EvaluateYamlNode(ctx, &rc.Run.Job().RawRunsOn); err != nil {
			return nil, err
		}
		labels := rc.Run.Job().RunsOn()
		if len(labels) == 0 {
			return nil, fmt.Errorf("job requires runs-on")
		}
		out = append(out, Assignment{matrix, labels})
	}
	return out, nil
}
func validateMatrixShape(node yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("matrix must be an object")
	}
	for i := 0; i < len(node.Content); i += 2 {
		name, values := node.Content[i].Value, node.Content[i+1]
		if values.Kind != yaml.SequenceNode {
			return fmt.Errorf("matrix %q must be an array", name)
		}
		if name == "include" || name == "exclude" {
			for _, value := range values.Content {
				if value.Kind != yaml.MappingNode {
					return fmt.Errorf("matrix %q entries must be objects", name)
				}
			}
		}
	}
	return nil
}
