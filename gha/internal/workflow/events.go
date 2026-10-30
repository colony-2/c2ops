package workflow

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/nektos/act/pkg/model"
	"github.com/nektos/act/pkg/workflowpattern"
	"gopkg.in/yaml.v3"
)

func validateTriggers(_ *model.Workflow, in Input) error {
	switch in.EventName {
	case "workflow_dispatch":
	case "push":
		if ref, _ := in.Event["ref"].(string); !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
			return fmt.Errorf("push event.ref must be refs/heads/... or refs/tags/...")
		}
	case "pull_request":
		pr, _ := in.Event["pull_request"].(map[string]any)
		base, _ := pr["base"].(map[string]any)
		head, _ := pr["head"].(map[string]any)
		baseRef, _ := base["ref"].(string)
		headRef, _ := head["ref"].(string)
		if in.Event["number"] == nil || baseRef == "" || headRef == "" {
			return fmt.Errorf("pull_request metadata requires number, pull_request.base.ref and pull_request.head.ref")
		}
	default:
		return fmt.Errorf("unsupported event %q; use workflow_dispatch, push, or pull_request", in.EventName)
	}
	return nil
}
func matchesTrigger(source []byte, in Input) (bool, error) {
	wf, err := model.ReadWorkflow(bytes.NewReader(source), false)
	if err != nil {
		return false, err
	}
	found := false
	for _, name := range wf.On() {
		found = found || name == in.EventName
	}
	if !found {
		return false, nil
	}
	var document struct {
		On yaml.Node `yaml:"on"`
	}
	if err = yaml.Unmarshal(source, &document); err != nil {
		return false, err
	}
	if document.On.Kind != yaml.MappingNode {
		if in.EventName == "pull_request" {
			action, _ := in.Event["action"].(string)
			return action == "opened" || action == "synchronize" || action == "reopened", nil
		}
		return true, nil
	}
	var filters map[string]yaml.Node
	for i := 0; i < len(document.On.Content); i += 2 {
		if document.On.Content[i].Value == in.EventName {
			if err = document.On.Content[i+1].Decode(&filters); err != nil {
				return false, err
			}
		}
	}
	ref, _ := in.Event["ref"].(string)
	kind := "branches"
	ref = strings.TrimPrefix(ref, "refs/heads/")
	if strings.HasPrefix(ref, "refs/tags/") {
		kind = "tags"
		ref = strings.TrimPrefix(ref, "refs/tags/")
	}
	if in.EventName == "pull_request" {
		pr, _ := in.Event["pull_request"].(map[string]any)
		base, _ := pr["base"].(map[string]any)
		ref, _ = base["ref"].(string)
	}
	if in.EventName == "push" {
		other := "tags"
		if kind == "tags" {
			other = "branches"
		}
		_, a := filters[other]
		_, b := filters[other+"-ignore"]
		_, c := filters[kind]
		_, d := filters[kind+"-ignore"]
		if (a || b) && !c && !d {
			return false, nil
		}
	}
	for _, key := range []string{"branches", "tags", "paths"} {
		if _, a := filters[key]; a {
			if _, b := filters[key+"-ignore"]; b {
				return false, fmt.Errorf("cannot combine %s and %s-ignore", key, key)
			}
		}
	}
	for key, node := range filters {
		if key == "inputs" {
			continue
		}
		if key == "types" {
			var types []string
			if err = node.Decode(&types); err != nil {
				return false, err
			}
			action, _ := in.Event["action"].(string)
			match := false
			for _, t := range types {
				match = match || t == action
			}
			if !match {
				return false, nil
			}
			continue
		}
		if key == "branches" || key == "branches-ignore" || key == "tags" || key == "tags-ignore" {
			if !strings.HasPrefix(key, kind) {
				continue
			}
		} else if key != "paths" && key != "paths-ignore" {
			return false, fmt.Errorf("unsupported event filter %q", key)
		}
		values := []string{ref}
		if strings.HasPrefix(key, "paths") {
			if kind == "tags" {
				continue
			}
			if in.ChangedPaths == nil {
				return false, fmt.Errorf("changed_paths is required for workflow path filters")
			}
			values = in.ChangedPaths
		}
		var patterns []string
		if err = node.Decode(&patterns); err != nil {
			return false, err
		}
		compiled, err := workflowpattern.CompilePatterns(patterns...)
		if err != nil {
			return false, err
		}
		skip := workflowpattern.Skip(compiled, values, quietTrace{})
		if strings.HasSuffix(key, "-ignore") {
			skip = workflowpattern.Filter(compiled, values, quietTrace{})
		}
		if skip {
			return false, nil
		}
	}
	if in.EventName == "pull_request" {
		if _, ok := filters["types"]; !ok {
			action, _ := in.Event["action"].(string)
			if action != "opened" && action != "synchronize" && action != "reopened" {
				return false, nil
			}
		}
	}
	return true, nil
}

type quietTrace struct{}

func (quietTrace) Info(string, ...interface{}) {}
