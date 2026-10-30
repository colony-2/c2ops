// Package workflow translates an Actions DAG into broker-submitted c2j recipes.
// The broker owns submission authority; c2j recipes own waiting and replay.
package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/colony-2/c2ops/gha/internal/jobexec"
	"github.com/nektos/act/pkg/model"
	"gopkg.in/yaml.v3"
)

const Helper = "nix:github:colony-2/c2ops/published#gha-workflow"
const Executor = "nix:github:colony-2/c2ops/published#gha-job"

type Profile struct {
	Labels         []string       `json:"labels"`
	ExecutionNeeds map[string]any `json:"execution_needs"`
}
type Input struct {
	HelperOp       string            `json:"helper_op,omitempty"`
	ExecutorOp     string            `json:"executor_op,omitempty"`
	Action         string            `json:"action,omitempty"`
	Workflow       string            `json:"workflow,omitempty"`
	WorktreePath   string            `json:"worktree_path,omitempty"`
	Repository     string            `json:"repository,omitempty"`
	Revision       string            `json:"revision,omitempty"`
	Profiles       []Profile         `json:"runner_profiles,omitempty"`
	ChangedPaths   []string          `json:"changed_paths,omitempty"`
	EventName      string            `json:"event_name,omitempty"`
	Event          map[string]any    `json:"event,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Vars           map[string]string `json:"vars,omitempty"`
	SecretBindings map[string]string `json:"secret_bindings,omitempty"`
	// Payload is opaque public metadata. Encoding prevents c2j from interpreting
	// Actions expressions in strings transported through generated recipe inputs.
	Payload      string                    `json:"payload,omitempty"`
	Needs        map[string]jobexec.Result `json:"needs,omitempty"`
	Observations []Observation             `json:"observations,omitempty"`
}
type Observation struct {
	Status  string `json:"status"`
	Outputs struct {
		Result jobexec.Result `json:"result"`
	} `json:"outputs"`
}
type Submission struct {
	JobID    string            `json:"job_id"`
	GoalID   string            `json:"goal_id"`
	TenantID string            `json:"tenant_id"`
	Status   string            `json:"status"`
	Jobs     map[string]string `json:"jobs,omitempty"`
}
type payload struct {
	Input Input  `json:"input"`
	Job   string `json:"job"`
	Hash  string `json:"hash"`
}

func Run(ctx context.Context, in Input) (any, error) {
	switch in.Action {
	case "", "submit":
		return Submit(ctx, in)
	case "expand":
		return expand(ctx, in)
	case "collect":
		return collect(in.Observations)
	default:
		return nil, fmt.Errorf("unknown workflow action %q", in.Action)
	}
}

func Submit(ctx context.Context, in Input) (Submission, error) {
	var zero Submission
	if in.HelperOp == "" {
		in.HelperOp = Helper
	}
	if in.ExecutorOp == "" {
		in.ExecutorOp = Executor
	}
	source, err := readWorkflow(in.WorktreePath, in.Workflow)
	if err != nil {
		return zero, err
	}
	if in.EventName == "" {
		in.EventName = "workflow_dispatch"
	}
	if in.Event == nil {
		in.Event = map[string]any{}
	}
	if in.Env == nil {
		in.Env = map[string]string{}
	}
	if err := validateBindings(in); err != nil {
		return zero, err
	}
	if in.Repository == "" {
		return zero, fmt.Errorf("repository is required")
	}
	// Freeze the actual worker checkout rather than resolving a moving branch on
	// each child. Require a reachable committed revision for independent workers.
	cmd := exec.CommandContext(ctx, "git", "-C", in.WorktreePath, "rev-parse", "HEAD")
	raw, err := cmd.Output()
	if err != nil {
		return zero, fmt.Errorf("resolve source: %w", err)
	}
	in.Revision = strings.TrimSpace(string(raw))
	jobs, err := jobexec.Plan(string(source), in.EventName)
	if err != nil {
		return zero, err
	}
	wf, err := model.ReadWorkflow(bytes.NewReader(source), false)
	if err != nil {
		return zero, err
	}
	if err := validateTriggers(wf, in); err != nil {
		return zero, err
	}
	matched, err := matchesTrigger(source, in)
	if err != nil {
		return zero, err
	}
	if !matched {
		jobs = nil
	}
	for _, job := range jobs {
		if err := jobexec.Validate(string(source), job.ID, in.WorktreePath); err != nil {
			return zero, err
		}
	}
	hash := sha256.Sum256(source)
	in.WorktreePath = ""
	in.Action = ""
	in.Payload = ""
	in.Needs = nil
	broker, err := currentBroker()
	if err != nil {
		return zero, err
	}
	// Construct and validate every recipe before the first submission.
	ids := map[string]string{}
	for _, job := range jobs {
		ids[job.ID] = broker.id("job:" + job.ID)
	}
	var recipes []plannedRecipe
	for _, job := range jobs {
		ancestors, err := jobexec.Dependencies(string(source), job.ID)
		if err != nil {
			return zero, err
		}
		p := payload{Input: in, Job: job.ID, Hash: hex.EncodeToString(hash[:])}
		steps, needs := waits(ancestors, ids)
		steps = append(steps, node("expand", in.HelperOp, map[string]any{"action": "expand", "payload": encode(p), "needs": needs}))
		steps = append(steps, await("result", "${{ sequence.expand.outputs.job_id }}"))
		rec := map[string]any{"id": "gha-" + job.ID, "sequence": steps, "outputs": map[string]any{"result": "${{ sequence.result.outputs.outputs.result }}"}}
		recipes = append(recipes, plannedRecipe{ID: ids[job.ID], Recipe: rec})
	}
	names := make([]string, 0, len(ids))
	for id := range ids {
		names = append(names, id)
	}
	sort.Strings(names)
	steps, results := waits(names, ids)
	// Keep diagnostics/object references in native recipe outputs. They never
	// round-trip through a subprocess just to be aggregated.
	condition := "false"
	for i := range names {
		condition += fmt.Sprintf(" || sequence.wait%d.outputs.status != 'completed' || sequence.wait%d.outputs.outputs.result.conclusion == 'failure' || sequence.wait%d.outputs.outputs.result.conclusion == 'cancelled'", i, i, i)
	}
	rec := map[string]any{"id": "gha-workflow-result", "sequence": steps, "outputs": map[string]any{
		"status": "${{ (" + condition + ") ? 'failure' : 'success' }}", "jobs": results,
		"revision": in.Revision, "workflow_sha256": hex.EncodeToString(hash[:]),
	}}
	if len(jobs) == 0 {
		rec["outputs"].(map[string]any)["status"] = "skipped"
	}
	recipes = append(recipes, plannedRecipe{ID: broker.id("result"), Recipe: rec})
	var result Submission
	for _, rec := range recipes {
		result, err = broker.submit(ctx, in, rec)
		if err != nil {
			return zero, err
		}
	}
	result.Status = "submitted"
	result.Jobs = ids
	return result, nil
}

type plannedRecipe struct {
	ID     string
	Recipe map[string]any
}

func node(id, op string, inputs map[string]any) map[string]any {
	return map[string]any{"id": id, "op": op, "inputs": inputs}
}
func await(id, target string) map[string]any {
	return node(id, "recipe.await_result_soft", map[string]any{"target": map[string]any{"job": target}, "wait": map[string]any{"on": "job"}, "return_when": "terminal"})
}
func waits(names []string, ids map[string]string) ([]any, map[string]any) {
	steps := []any{}
	results := map[string]any{}
	for i, name := range names {
		step := fmt.Sprintf("wait%d", i)
		steps = append(steps, await(step, ids[name]))
		// Infrastructure failures also become a GHA dependency failure. Keep the
		// native output references on the successful path.
		results[name] = fmt.Sprintf("${{ sequence.%s.outputs.status == 'completed' ? sequence.%s.outputs.outputs.result : {'job_id': %q, 'conclusion': sequence.%s.outputs.status == 'cancelled' ? 'cancelled' : 'failure', 'outputs': {} } }}", step, step, name, step)
	}
	return steps, results
}
func encode(v any) string { b, _ := json.Marshal(v); return base64.StdEncoding.EncodeToString(b) }
func readWorkflow(root, name string) ([]byte, error) {
	if !filepath.IsAbs(root) || name == "" || filepath.IsAbs(name) {
		return nil, fmt.Errorf("workflow requires an absolute worktree and a relative file")
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(base, name))
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, fmt.Errorf("workflow escapes worktree")
	}
	return os.ReadFile(path)
}

var bindingName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateBindings(in Input) error {
	seen := map[string]bool{}
	for name, secret := range in.SecretBindings {
		key := strings.ToUpper(name)
		if !bindingName.MatchString(name) || !bindingName.MatchString(secret) || seen[key] {
			return fmt.Errorf("invalid or colliding secret binding %q", name)
		}
		seen[key] = true
	}
	if len(in.Profiles) == 0 {
		return fmt.Errorf("runner_profiles is required; runs-on labels must have explicit execution needs")
	}
	for _, p := range in.Profiles {
		if len(p.Labels) == 0 || len(p.ExecutionNeeds) == 0 {
			return fmt.Errorf("each runner profile needs labels and execution_needs")
		}
		for key := range p.ExecutionNeeds {
			switch key {
			case "platform", "image", "packages", "resources":
			default:
				return fmt.Errorf("unsupported execution requirement %q", key)
			}
		}
	}
	return nil
}

// This is the c2j invocation-broker HTTP contract used by c2j submit. Its wire
// shape is exercised against the real pinned c2j broker in integration tests.
// No direct JobDB connection or lease credential is used by this client.
type broker struct{ endpoint, token, session, tenant, parent, invocation string }

func currentBroker() (broker, error) {
	b := broker{os.Getenv("C2J_CHILD_JOB_ENDPOINT"), os.Getenv("C2J_CHILD_JOB_TOKEN"), os.Getenv("C2J_CHILD_JOB_SESSION_ID"), os.Getenv("C2J_CURRENT_TENANT_ID"), os.Getenv("C2J_CURRENT_JOB_ID"), os.Getenv("C2J_CURRENT_INVOCATION_HASH")}
	if b.endpoint == "" || b.token == "" || b.session == "" || b.tenant == "" || b.parent == "" || b.invocation == "" {
		return b, fmt.Errorf("c2j invocation child-submission broker is required")
	}
	return b, nil
}
func (b broker) id(key string) string {
	sum := sha256.Sum256([]byte("c2ops.gha/v1\x00" + b.tenant + "\x00" + b.parent + "\x00" + b.invocation + "\x00" + key))
	return "gha-" + hex.EncodeToString(sum[:20])
}
func (b broker) submit(ctx context.Context, in Input, p plannedRecipe) (Submission, error) {
	var out Submission
	recipe, err := yaml.Marshal(p.Recipe)
	if err != nil {
		return out, err
	}
	request := map[string]any{"start": map[string]any{"tenantId": b.tenant, "job_id": p.ID, "recipe": p.Recipe["id"], "git": in.Revision, "context": map[string]any{"git": map[string]any{"repo": in.Repository, "ref": in.Revision, "resolved_hash": in.Revision}}}, "embedded_recipes": []any{map[string]any{"name": p.Recipe["id"], "yaml": recipe}}}
	raw, err := json.Marshal(request)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.endpoint, bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("X-C2J-Child-Job-Session", b.session)
	// Broker endpoints are local capabilities and must not use ambient proxies.
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return out, fmt.Errorf("submit child %s: %w", p.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, fmt.Errorf("submit child %s: %s: %s", p.ID, resp.Status, body)
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	if err == nil && out.JobID != p.ID {
		err = fmt.Errorf("broker returned an unexpected child identity")
	}
	return out, err
}
