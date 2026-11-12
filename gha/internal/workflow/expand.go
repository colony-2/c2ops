package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
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
	request := jobexec.Request{Repository: p.Input.Repository, Revision: p.Input.Revision, SourceCredential: p.Input.SourceCredential, Workflow: string(source), JobID: p.Job, Needs: in.Needs, EventName: p.Input.EventName, Event: event, Env: p.Input.Env, Vars: p.Input.Vars}
	assignments, err := jobexec.Prepare(ctx, request, in.WorktreePath)
	if err != nil {
		return zero, err
	}
	broker, err := currentBroker()
	if err != nil {
		return zero, err
	}
	broker.sourcePack, err = sourceBundle(ctx, in.WorktreePath, p.Input.BaseRevision, p.Input.Revision)
	if err != nil {
		return zero, err
	}
	var recipes []plannedRecipe
	var ids []string
	identities := []instanceIdentity{}
	for i, assignment := range assignments {
		requirements, err := matchProfile(p.Input.Profiles, assignment.Labels)
		if err != nil {
			return zero, err
		}
		request.Workflow = p.Input.Workflow
		request.Matrix = assignment.Matrix
		artifactInputs := []any{}
		ancestorIDs := make([]string, 0, len(in.Needs))
		for id := range in.Needs {
			ancestorIDs = append(ancestorIDs, id)
		}
		sort.Strings(ancestorIDs)
		for _, id := range ancestorIDs {
			need := in.Needs[id]
			for _, ref := range need.Artifacts {
				if ref == nil {
					continue
				}
				if snapshot, ok := ref.(map[string]any); ok {
					if original, ok := snapshot["ref"]; ok {
						ref = original
					}
				}
				artifactInputs = append(artifactInputs, ref)
			}
		}
		secrets := map[string]any{}
		names := []string{}
		for key, name := range p.Input.SecretBindings {
			secrets[key] = "${{ context.secrets." + name + " }}"
			names = append(names, name)
		}
		// Keep GHA/public strings opaque across the recipe template evaluator.
		sort.Strings(names)
		inputs := map[string]any{"artifact_inputs": artifactInputs, "payload": encode(request), "workflow": p.Input.Workflow, "job_id": p.Job, "workflow_sha256": p.Hash, "secrets": secrets}
		step := node("execute", p.Input.ExecutorOp, inputs)
		step["execution_needs"] = opaqueRequirements(requirements)
		rec := map[string]any{"id": fmt.Sprintf("gha-%s-%d", p.Job, i), "sequence": []any{step}, "outputs": map[string]any{"result": "${{ sequence.execute.outputs }}"}}
		if len(names) > 0 {
			rec["perms"] = map[string]any{"jobdb": map[string]any{"secrets": map[string]any{"resolve": names}}}
		}
		id := broker.id(fmt.Sprintf("shard:%d", i))
		ids = append(ids, id)
		matrix := assignment.Matrix
		if matrix == nil {
			matrix = map[string]any{}
		}
		identities = append(identities, instanceIdentity{matrix, assignment.Labels, requirements, id})
		recipes = append(recipes, plannedRecipe{id, rec})
	}
	steps := []any{}
	observations := []any{}
	diagnostics := []any{}
	uploaded := []any{}
	instances := []any{}
	for i, id := range ids {
		step := fmt.Sprintf("wait%d", i)
		steps = append(steps, await(step, id))
		observations = append(observations, map[string]any{"status": "${{ sequence." + step + ".outputs.status }}", "outputs": map[string]any{"result": fmt.Sprintf("${{ sequence.%s.outputs.status == 'completed' ? {'job_id': sequence.%s.outputs.outputs.result.job_id, 'conclusion': sequence.%s.outputs.outputs.result.conclusion, 'outputs': sequence.%s.outputs.outputs.result.outputs} : {'conclusion': 'failure', 'outputs': {} } }}", step, step, step, step)}})
		uploaded = append(uploaded, fmt.Sprintf("${{ sequence.%s.outputs.status == 'completed' && has(sequence.%s.outputs.outputs.result.artifacts) ? sequence.%s.outputs.outputs.result.artifacts[0] : null }}", step, step, step))
		diagnostic := fmt.Sprintf("${{ sequence.%s.outputs.status == 'completed' && has(sequence.%s.outputs.outputs.result.diagnostics) ? sequence.%s.outputs.outputs.result.diagnostics : null }}", step, step, step)
		diagnostics = append(diagnostics, diagnostic)
		instances = append(instances, map[string]any{
			"matrix":                  fmt.Sprintf("${{ sequence.collect.outputs.assignments[%d].matrix }}", i),
			"labels":                  fmt.Sprintf("${{ sequence.collect.outputs.assignments[%d].labels }}", i),
			"execution_needs":         fmt.Sprintf("${{ sequence.collect.outputs.assignments[%d].execution_needs }}", i),
			"execution_job_id":        id,
			"status":                  "${{ sequence." + step + ".outputs.status }}",
			"conclusion":              fmt.Sprintf("${{ sequence.%s.outputs.status == 'completed' && has(sequence.%s.outputs.outputs.result.conclusion) ? sequence.%s.outputs.outputs.result.conclusion : null }}", step, step, step),
			"diagnostics":             diagnostic,
			"diagnostics_unavailable": fmt.Sprintf("${{ sequence.%s.outputs.status != 'completed' ? 'executor_not_completed' : (has(sequence.%s.outputs.outputs.result.diagnostics) && sequence.%s.outputs.outputs.result.diagnostics != null ? dyn(null) : 'not_returned') }}", step, step, step),
		})
	}
	// Decode public metadata in the helper, then read its native output. Raw
	// matrix/label/requirement strings must not become recipe expressions; object
	// references stay in native outputs and never traverse this subprocess.
	steps = append(steps, node("collect", p.Input.HelperOp, map[string]any{"action": "collect", "observations": observations, "payload": encode(identities)}))
	result := map[string]any{"job_id": p.Job, "conclusion": "${{ sequence.collect.outputs.conclusion }}", "outputs": "${{ sequence.collect.outputs.outputs }}", "diagnostics": diagnostics, "artifacts": uploaded, "instances": instances}
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

// c2j evaluates demand strings as templates. Quote expression-like public
// values as CEL string literals, escaping braces so its template parser cannot
// mistake data for nested template syntax. Package selectors are already opaque.
func opaqueRequirements(requirements map[string]any) map[string]any {
	out := make(map[string]any, len(requirements))
	for key, value := range requirements {
		if nested, ok := value.(map[string]any); ok && key == "resources" {
			value = opaqueRequirements(nested)
		} else if text, ok := value.(string); ok && strings.Contains(text, "{{") {
			quoted, _ := json.Marshal(text)
			literal := strings.NewReplacer("{", `\u007b`, "}", `\u007d`).Replace(string(quoted))
			value = "${{ " + literal + " }}"
		}
		out[key] = value
	}
	return out
}

type instanceIdentity struct {
	Matrix         map[string]any `json:"matrix"`
	Labels         []string       `json:"labels"`
	ExecutionNeeds map[string]any `json:"execution_needs"`
	ExecutionJobID string         `json:"execution_job_id"`
}

type collection struct {
	jobexec.Result
	Assignments []instanceIdentity `json:"assignments"`
}

func collectWithIdentity(in Input) (collection, error) {
	result, err := collect(in.Observations)
	out := collection{Result: result, Assignments: []instanceIdentity{}}
	if err != nil || in.Payload == "" {
		return out, err
	}
	raw, err := base64.StdEncoding.DecodeString(in.Payload)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out.Assignments)
	if err == nil && len(out.Assignments) != len(in.Observations) {
		err = fmt.Errorf("assignment and observation counts differ")
	}
	return out, err
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
