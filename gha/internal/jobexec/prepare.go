package jobexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
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
		if _, err := checkoutHistory(step); err != nil {
			return err
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
	in.Env = repositoryEnvironment(ctx, workdir, in.Env)
	newContext := func(matrix map[string]any) (*runner.RunContext, error) {
		return evaluationContext(ctx, in, workdir, matrix)
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
	// act's CartesianProduct ranges over a Go map, so multi-axis matrices can
	// arrive in a different order on each retry. Shard IDs use this index;
	// canonical JSON ordering keeps the index bound to the same assignment.
	type canonicalMatrix struct {
		value map[string]any
		key   string
	}
	canonical := make([]canonicalMatrix, len(matrices))
	for i, matrix := range matrices {
		key, err := json.Marshal(matrix)
		if err != nil {
			return nil, fmt.Errorf("encode matrix assignment: %w", err)
		}
		canonical[i] = canonicalMatrix{matrix, string(key)}
	}
	sort.SliceStable(canonical, func(i, j int) bool { return canonical[i].key < canonical[j].key })
	for i, matrix := range canonical {
		matrices[i] = matrix.value
	}
	out := make([]Assignment, 0, len(matrices))
	for _, matrix := range matrices {
		rc, err := newContext(matrix)
		if err != nil {
			return nil, err
		}
		if err := evaluateRunsOn(ctx, rc); err != nil {
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

// Prepared contains only frozen public evaluation data; no runtime state exists yet.
type Prepared struct {
	Request Request
	Enabled bool
	Labels  []string
}

func Preflight(ctx context.Context, in Request, workdir string) (Prepared, error) {
	ctx = common.WithLogger(ctx, logFactory{writer: io.Discard}.WithJobLogger())
	in, _, _, err := validateRequest(in, workdir)
	if err != nil {
		return Prepared{}, err
	}
	out := Prepared{Request: in}
	rc, err := evaluationContext(ctx, in, workdir, nil)
	if err != nil {
		return out, err
	}
	enabled, err := runner.EvalBool(ctx, rc.ExprEval, rc.Run.Job().If.Value, exprparser.DefaultStatusCheckSuccess)
	if err != nil {
		return out, err
	}
	if !enabled {
		return out, nil
	}
	rc, err = evaluationContext(ctx, in, workdir, in.Matrix)
	if err != nil {
		return out, err
	}
	if err := selectMatrix(ctx, rc, in.Matrix); err != nil {
		return out, err
	}
	if err := evaluateRunsOn(ctx, rc); err != nil {
		return out, err
	}
	labels := rc.Run.Job().RunsOn()
	if len(labels) == 0 {
		return out, fmt.Errorf("job requires runs-on")
	}
	out.Enabled = true
	out.Labels = labels
	return out, nil
}

func evaluateRunsOn(ctx context.Context, rc *runner.RunContext) error {
	node := &rc.Run.Job().RawRunsOn
	if err := rc.ExprEval.EvaluateYamlNode(ctx, node); err != nil {
		return err
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content) != 2 || node.Content[0].Value != "labels" {
			return fmt.Errorf("runs-on groups and additional constraints are unsupported")
		}
		*node = *node.Content[1]
	}
	if node.Kind != yaml.ScalarNode && node.Kind != yaml.SequenceNode {
		return fmt.Errorf("runs-on must be a label or label array")
	}
	if node.Kind == yaml.SequenceNode {
		for _, n := range node.Content {
			if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
				return fmt.Errorf("runs-on labels must be strings")
			}
		}
	} else if node.Tag != "!!str" {
		return fmt.Errorf("runs-on label must be a string")
	}
	return nil
}

// evaluationContext is shared by expansion and single-assignment preflight.
// It intentionally supplies no secrets or host allocation to expressions.
func evaluationContext(ctx context.Context, in Request, workdir string, matrix map[string]any) (*runner.RunContext, error) {
	if in.EventName == "" {
		in.EventName = "workflow_dispatch"
	}
	if len(in.Event) == 0 {
		in.Event = json.RawMessage(`{}`)
	}
	vars, err := normalizeNames(in.Vars)
	if err != nil {
		return nil, err
	}
	in.Vars = vars
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

// Freeze placement before act receives execution-only secrets/environment.
// act may evaluate runs-on again while constructing the host executor.
func freezeRunsOn(job *model.Job, labels []string) {
	node := yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, label := range labels {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: label})
	}
	job.RawRunsOn = node
}
