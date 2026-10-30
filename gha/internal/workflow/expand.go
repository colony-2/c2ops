package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/colony-2/c2ops/gha/internal/jobexec"
)

func expand(ctx context.Context, in Input) (Submission, error) {
	var zero Submission
	var p payload
	raw, err := base64.StdEncoding.DecodeString(in.Payload)
	if err != nil {
		return zero, err
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return zero, err
	}
	source, err := readWorkflow(in.WorktreePath, p.Input.Workflow)
	if err != nil {
		return zero, err
	}
	hash := sha256.Sum256(source)
	if hex.EncodeToString(hash[:]) != p.Hash {
		return zero, fmt.Errorf("workflow changed from the planned snapshot")
	}
	if p.Input.Event == nil {
		p.Input.Event = map[string]any{}
	}
	event, _ := json.Marshal(p.Input.Event)
	request := jobexec.Request{Workflow: string(source), JobID: p.Job, Needs: in.Needs, EventName: p.Input.EventName, Event: event, Env: p.Input.Env, Vars: p.Input.Vars}
	assignments, err := jobexec.Prepare(ctx, request, in.WorktreePath)
	if err != nil {
		return zero, err
	}
	broker, err := currentBroker()
	if err != nil {
		return zero, err
	}
	var recipes []plannedRecipe
	var ids []string
	for i, assignment := range assignments {
		requirements, err := matchProfile(p.Input.Profiles, assignment.Labels)
		if err != nil {
			return zero, err
		}
		request.Workflow = p.Input.Workflow
		request.Matrix = assignment.Matrix
		secrets := map[string]any{}
		names := []string{}
		for key, name := range p.Input.SecretBindings {
			secrets[key] = "${{ context.secrets." + name + " }}"
			names = append(names, name)
		}
		// Keep GHA/public strings opaque across the recipe template evaluator.
		inputs := map[string]any{"payload": encode(request), "workflow": p.Input.Workflow, "job_id": p.Job, "workflow_sha256": p.Hash, "secrets": secrets}
		step := node("execute", p.Input.ExecutorOp, inputs)
		step["execution_needs"] = requirements
		rec := map[string]any{"id": fmt.Sprintf("gha-%s-%d", p.Job, i), "sequence": []any{step}, "outputs": map[string]any{"result": "${{ sequence.execute.outputs }}"}}
		if len(names) > 0 {
			rec["perms"] = map[string]any{"jobdb": map[string]any{"secrets": map[string]any{"resolve": names}}}
		}
		id := broker.id(fmt.Sprintf("shard:%d", i))
		ids = append(ids, id)
		recipes = append(recipes, plannedRecipe{id, rec})
	}
	steps := []any{}
	observations := []any{}
	diagnostics := []any{}
	for i, id := range ids {
		step := fmt.Sprintf("wait%d", i)
		steps = append(steps, await(step, id))
		observations = append(observations, map[string]any{"status": "${{ sequence." + step + ".outputs.status }}", "outputs": map[string]any{"result": fmt.Sprintf("${{ sequence.%s.outputs.status == 'completed' ? {'job_id': sequence.%s.outputs.outputs.result.job_id, 'conclusion': sequence.%s.outputs.outputs.result.conclusion, 'outputs': sequence.%s.outputs.outputs.result.outputs} : {'conclusion': 'failure', 'outputs': {} } }}", step, step, step, step)}})
		diagnostics = append(diagnostics, fmt.Sprintf("${{ sequence.%s.outputs.status == 'completed' ? sequence.%s.outputs.outputs.result.diagnostics : null }}", step, step))
	}
	steps = append(steps, node("collect", p.Input.HelperOp, map[string]any{"action": "collect", "observations": observations}))
	result := map[string]any{"job_id": p.Job, "conclusion": "${{ sequence.collect.outputs.conclusion }}", "outputs": "${{ sequence.collect.outputs.outputs }}", "diagnostics": diagnostics}
	rec := map[string]any{"id": "gha-" + p.Job + "-result", "sequence": steps, "outputs": map[string]any{"result": result}}
	recipes = append(recipes, plannedRecipe{broker.id("result"), rec})
	var out Submission
	for _, rec := range recipes {
		out, err = broker.submit(ctx, p.Input, rec)
		if err != nil {
			return zero, err
		}
	}
	return out, nil
}
func matchProfile(profiles []Profile, labels []string) (map[string]any, error) {
	var found map[string]any
	for _, profile := range profiles {
		available := map[string]bool{}
		for _, label := range profile.Labels {
			available[strings.ToLower(label)] = true
		}
		matches := true
		for _, label := range labels {
			matches = matches && available[strings.ToLower(label)]
		}
		if matches {
			if found != nil {
				return nil, fmt.Errorf("ambiguous runner profile for %v", labels)
			}
			found = profile.ExecutionNeeds
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no runner profile matches %v", labels)
	}
	return found, nil
}
func collect(observations []Observation) (jobexec.Result, error) {
	out := jobexec.Result{Conclusion: "success", Outputs: map[string]string{}}
	if len(observations) == 0 {
		out.Conclusion = "skipped"
		return out, nil
	}
	allSkipped := true
	for _, observation := range observations {
		r := observation.Outputs.Result
		if observation.Status == "cancelled" || r.Conclusion == "cancelled" {
			if out.Conclusion != "failure" {
				out.Conclusion = "cancelled"
			}
		}
		if observation.Status != "completed" && observation.Status != "cancelled" || r.Conclusion == "failure" {
			out.Conclusion = "failure"
		}
		allSkipped = allSkipped && r.Conclusion == "skipped"
		for key, value := range r.Outputs {
			if old, ok := out.Outputs[key]; ok && old != value {
				return out, fmt.Errorf("matrix outputs collide for %q; use distinct output names", key)
			}
			out.Outputs[key] = value
		}
	}
	if allSkipped {
		out.Conclusion = "skipped"
	}
	return out, nil
}
