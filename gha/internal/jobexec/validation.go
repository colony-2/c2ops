package jobexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/nektos/act/pkg/model"
	"github.com/nektos/act/pkg/runner"
	"gopkg.in/yaml.v3"
)

var contextName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// act's model omits several orchestration-level keys. Reject them explicitly
// rather than silently running a workflow that expects those guarantees.
func validateFeatures(workflow, jobID string) error {
	var raw struct {
		Fields map[string]yaml.Node            `yaml:",inline"`
		Jobs   map[string]map[string]yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &raw); err != nil {
		return err
	}
	for _, key := range []string{"concurrency", "permissions"} {
		if _, ok := raw.Fields[key]; ok {
			return fmt.Errorf("workflow %s requires an orchestration/identity bridge", key)
		}
	}
	for _, key := range []string{"environment", "concurrency", "permissions", "continue-on-error", "timeout-minutes"} {
		if _, ok := raw.Jobs[jobID][key]; ok {
			return fmt.Errorf("job %s requires orchestration support", key)
		}
	}
	return nil
}

func normalizeNames(in map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(in))
	for name, value := range in {
		if !contextName.MatchString(name) {
			return nil, fmt.Errorf("invalid context name %q", name)
		}
		key := strings.ToUpper(name)
		if _, ok := out[key]; ok {
			return nil, fmt.Errorf("case collision for %q", key)
		}
		out[key] = value
	}
	return out, nil
}

// act's success()/failure() inspect transitive dependencies, although the
// needs expression context exposes only immediate dependencies.
func dependencyIDs(wf *model.Workflow, id string) ([]string, error) {
	seen := map[string]bool{}
	visiting := map[string]bool{}
	var ids []string
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return fmt.Errorf("dependency cycle at %q", key)
		}
		if seen[key] {
			return nil
		}
		job := wf.GetJob(key)
		if job == nil {
			return fmt.Errorf("unknown dependency job %q", key)
		}
		visiting[key] = true
		for _, parent := range job.Needs() {
			if err := visit(parent); err != nil {
				return err
			}
		}
		delete(visiting, key)
		seen[key] = true
		if key != id {
			ids = append(ids, key)
		}
		return nil
	}
	err := visit(id)
	return ids, err
}

func selectMatrix(ctx context.Context, rc *runner.RunContext, selected map[string]any) error {
	job := rc.Run.Job()
	if job.Strategy == nil || job.Strategy.RawMatrix.Kind == 0 {
		if len(selected) > 0 {
			return fmt.Errorf("matrix assignment supplied for a non-matrix job")
		}
		return nil
	}
	if selected == nil {
		return fmt.Errorf("matrix job requires an explicit matrix assignment")
	}
	if err := rc.ExprEval.EvaluateYamlNode(ctx, &job.Strategy.RawMatrix); err != nil {
		return fmt.Errorf("matrix: %w", err)
	}
	node := job.Strategy.RawMatrix
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("matrix must be an object")
	}
	// Validate the shapes before act's GetMatrixes, which type-asserts include
	// and exclude entries rather than returning errors for malformed entries.
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
	assignments, err := job.GetMatrixes()
	if err != nil {
		return fmt.Errorf("matrix: %w", err)
	}
	want, err := json.Marshal(selected)
	if err != nil {
		return fmt.Errorf("matrix assignment: %w", err)
	}
	for _, assignment := range assignments {
		encoded, err := json.Marshal(assignment)
		if err != nil {
			return fmt.Errorf("matrix: %w", err)
		}
		if bytes.Equal(encoded, want) {
			return nil
		}
	}
	return fmt.Errorf("matrix assignment is not an expanded workflow combination")
}

func sourceStepID(job *model.Job) string {
	id := "__c2j_source"
	for {
		found := false
		for _, step := range job.Steps {
			if step.ID == id {
				found = true
				break
			}
		}
		if !found {
			return id
		}
		id += "_"
	}
}
