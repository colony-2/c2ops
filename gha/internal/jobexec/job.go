// Package jobexec adapts act's public planner and executor to one externally
// scheduled job. It deliberately has no c2j/JobDB dependency or scheduling loop.
package jobexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nektos/act/pkg/common"
	"github.com/nektos/act/pkg/model"
	"github.com/nektos/act/pkg/runner"
	"github.com/sirupsen/logrus"
)

type Job struct {
	ID    string   `json:"id"`
	Needs []string `json:"needs,omitempty"`
}

type Result struct {
	JobID       string            `json:"job_id"`
	Conclusion  string            `json:"conclusion"`
	Outputs     map[string]string `json:"outputs"`
	Diagnostics any               `json:"diagnostics,omitempty"`
}

type Request struct {
	Workflow  string                 `json:"workflow"`
	JobID     string                 `json:"job_id"`
	Needs     map[string]Result      `json:"needs,omitempty"`
	Matrix    map[string]interface{} `json:"matrix,omitempty"`
	EventName string                 `json:"event_name,omitempty"`
	Event     json.RawMessage        `json:"event,omitempty"`
	Env       map[string]string      `json:"env,omitempty"`
	Vars      map[string]string      `json:"vars,omitempty"`
	Secrets   map[string]string      `json:"secrets,omitempty"`
}

type Options struct {
	Workdir  string
	StateDir string
	Log      io.Writer
}

// Plan reuses act's static plan but preserves the original dependency edges.
// Like act.PlanEvent, it selects event names only. The orchestration layer must
// additionally evaluate trigger filters before scheduling any job.
func Plan(workflow, event string) ([]Job, error) {
	planner, err := model.NewSingleWorkflowPlanner("workflow.yml", strings.NewReader(workflow))
	if err != nil {
		return nil, err
	}
	plan, err := planner.PlanEvent(event)
	if err != nil {
		return nil, err
	}
	var jobs []Job
	for _, stage := range plan.Stages {
		for _, run := range stage.Runs {
			jobs = append(jobs, Job{ID: run.JobID, Needs: run.Job().Needs()})
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	return jobs, nil
}

// Execute runs exactly one assigned job in act's host mode. Workdir must already
// contain an isolated source snapshot. This is not a sandbox: the provisioner
// supplies the process/OS isolation boundary. Remote actions, reusable workflows,
// and Docker features are rejected until their bridges are implemented.
func Execute(ctx context.Context, in Request, opts Options) (Result, error) {
	if !filepath.IsAbs(opts.Workdir) || !filepath.IsAbs(opts.StateDir) {
		return Result{}, fmt.Errorf("absolute workdir and state directory are required")
	}
	if err := validateFeatures(in.Workflow, in.JobID); err != nil {
		return Result{}, err
	}
	wf, err := model.ReadWorkflow(strings.NewReader(in.Workflow), false)
	if err != nil {
		return Result{}, err
	}
	wf.File = "workflow.yml"
	job := wf.GetJob(in.JobID)
	if job == nil {
		return Result{}, fmt.Errorf("unknown job %q", in.JobID)
	}
	in.Secrets, err = normalizeNames(in.Secrets)
	if err != nil {
		return Result{}, fmt.Errorf("secrets: %w", err)
	}
	in.Vars, err = normalizeNames(in.Vars)
	if err != nil {
		return Result{}, fmt.Errorf("vars: %w", err)
	}
	if job.Uses != "" {
		return Result{}, fmt.Errorf("reusable workflows require orchestration support")
	}
	if job.RawContainer.Kind != 0 || len(job.Services) > 0 {
		return Result{}, fmt.Errorf("job containers and service containers are unsupported in host mode")
	}
	if len(job.Steps) == 0 {
		return Result{}, fmt.Errorf("job requires at least one step")
	}
	for _, step := range job.Steps {
		if step == nil {
			return Result{}, fmt.Errorf("empty step")
		}
		if err := validateAction(opts.Workdir, step.Uses, map[string]bool{}); err != nil {
			return Result{}, err
		}
	}
	ancestors, err := dependencyIDs(wf, in.JobID)
	if err != nil {
		return Result{}, err
	}
	allowed := map[string]bool{}
	for _, id := range ancestors {
		allowed[id] = true
	}
	for id, result := range in.Needs {
		dependency := wf.GetJob(id)
		if dependency == nil || !allowed[id] || (result.JobID != "" && result.JobID != id) {
			return Result{}, fmt.Errorf("invalid dependency job %q", id)
		}
		switch result.Conclusion {
		case "success", "failure", "cancelled", "skipped":
		default:
			return Result{}, fmt.Errorf("invalid dependency conclusion for %q", id)
		}
		dependency.Outputs = copyMap(result.Outputs)
		dependency.Result = result.Conclusion
	}
	for _, id := range ancestors {
		if _, ok := in.Needs[id]; !ok {
			return Result{}, fmt.Errorf("missing dependency result for %q", id)
		}
	}
	if in.EventName == "" {
		in.EventName = "workflow_dispatch"
	}
	if len(in.Event) == 0 {
		in.Event = json.RawMessage(`{}`)
	}
	var event map[string]any
	if err := json.Unmarshal(in.Event, &event); err != nil || event == nil {
		return Result{}, fmt.Errorf("event must be a JSON object")
	}
	state, err := os.MkdirTemp(opts.StateDir, "act-job-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(state)
	for _, dir := range []string{"home", "tmp", "cache"} {
		if err := os.Mkdir(filepath.Join(state, dir), 0o700); err != nil {
			return Result{}, err
		}
	}
	log := opts.Log
	if log == nil {
		log = io.Discard
	}
	factory := logFactory{writer: log, secrets: secretValues(in.Secrets)}
	ctx = common.WithLogger(ctx, factory.WithJobLogger())
	ctx = runner.WithJobLoggerFactory(ctx, factory)
	config := &runner.Config{
		Workdir: opts.Workdir, ActionCacheDir: filepath.Join(state, "cache"),
		EventName: in.EventName, Env: copyMap(in.Env), Secrets: copyMap(in.Secrets), Vars: copyMap(in.Vars),
		Token: in.Secrets["GITHUB_TOKEN"], Actor: "c2j", GitHubInstance: "github.com",
		Platforms: map[string]string{}, AutoRemove: true, LogOutput: true, ActionOfflineMode: true,
	}
	// act host mode fills absent keys from os.Environ. Explicitly blank ambient
	// values, then apply workflow/job/caller settings and isolated runtime paths.
	env := map[string]string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		env[key] = ""
	}
	env["PATH"] = os.Getenv("PATH")
	for _, values := range []map[string]string{wf.Env, job.Environment(), in.Env} {
		for k, v := range values {
			env[k] = v
		}
	}
	env["HOME"] = filepath.Join(state, "home")
	env["TMPDIR"] = filepath.Join(state, "tmp")
	env["XDG_CACHE_HOME"] = filepath.Join(state, "cache")
	rc := &runner.RunContext{
		Config: config, Run: &model.Run{Workflow: wf, JobID: in.JobID},
		EventJSON: string(in.Event), Matrix: in.Matrix, Env: env,
		StepResults: map[string]*model.StepResult{}, Name: in.JobID, JobName: in.JobID,
	}
	rc.ExprEval = rc.NewExpressionEvaluator(ctx)
	if err := selectMatrix(ctx, rc, in.Matrix); err != nil {
		return Result{}, err
	}
	if err := rc.ExprEval.EvaluateYamlNode(ctx, &job.RawRunsOn); err != nil {
		return Result{}, fmt.Errorf("runs-on: %w", err)
	}
	if len(job.RunsOn()) == 0 {
		return Result{}, fmt.Errorf("job requires runs-on")
	}
	for _, label := range job.RunsOn() {
		config.Platforms[strings.ToLower(label)] = "-self-hosted"
	}
	ctx = runner.WithJobLogger(ctx, in.JobID, in.JobID, config, &rc.Masks, in.Matrix)
	ctx = common.WithJobErrorContainer(ctx)
	// act's host executor starts with an empty workspace. Its built-in local
	// checkout shortcut copies Config.Workdir without fetching an action or Git
	// revision. Prepend that preparation step; authored remote actions still fail
	// preflight. Assign IDs first so adding a step does not shift implicit IDs.
	for i, step := range job.Steps {
		if step.ID == "" {
			step.ID = fmt.Sprint(i)
		}
	}
	job.Steps = append([]*model.Step{{Name: "Prepare c2j source snapshot", Uses: "actions/checkout@v4", ID: sourceStepID(job)}}, job.Steps...)
	execute, err := rc.Executor()
	if err != nil {
		return Result{}, err
	}
	execErr := execute(ctx)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if job.Result == "" {
		return Result{}, fmt.Errorf("act returned no conclusion for %q: %v", in.JobID, execErr)
	}
	if execErr != nil && job.Result == "success" {
		return Result{}, fmt.Errorf("act execution failed: %w", execErr)
	}
	outputs := copyMap(job.Outputs)
	if job.Result == "skipped" {
		outputs = map[string]string{}
	}
	for name, value := range outputs {
		for _, mask := range maskValues(append(secretValues(in.Secrets), rc.Masks...)) {
			if mask != "" && strings.Contains(value, mask) {
				delete(outputs, name)
				common.Logger(ctx).Warnf("Suppressing secret-bearing job output %q", name)
				break
			}
		}
	}
	return Result{JobID: in.JobID, Conclusion: job.Result, Outputs: outputs}, nil
}

func copyMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

func secretValues(values map[string]string) []string {
	var out []string
	for _, v := range values {
		out = append(out, v)
	}
	return out
}

type logFactory struct {
	writer  io.Writer
	secrets []string
}

func (f logFactory) WithJobLogger() *logrus.Logger {
	log := logrus.New()
	log.SetOutput(f.writer)
	log.SetLevel(logrus.InfoLevel)
	log.AddHook(maskHook{secrets: f.secrets})
	log.SetFormatter(messageFormatter{})
	return log
}

// act masks entry.Message, but its structured fields include the raw add-mask
// command and set-output arguments. Do not serialize those fields into logs.
// Apply masks here too for messages before WithJobLogger is installed.
type messageFormatter struct{}

func (f messageFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	return []byte(fmt.Sprintf("%s: %s\n", entry.Level, entry.Message)), nil
}

type maskHook struct{ secrets []string }

func (maskHook) Levels() []logrus.Level { return logrus.AllLevels }

func (f maskHook) Fire(entry *logrus.Entry) error {
	masks := append([]string(nil), f.secrets...)
	if entry.Context != nil {
		masks = append(masks, (*runner.Masks(entry.Context))...)
	}
	// Run before act's formatter so overlapping secrets are replaced longest
	// first, without a shorter replacement exposing the longer value's suffix.
	for _, value := range maskValues(masks) {
		entry.Message = strings.ReplaceAll(entry.Message, value, "***")
	}
	return nil
}

func maskValues(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" {
			continue
		}
		seen[value] = true
		// Output is forwarded line by line, including multiline credentials.
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if line != "" {
				seen[line] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func validateAction(workdir, uses string, visited map[string]bool) error {
	if uses == "" {
		return nil
	}
	if strings.HasPrefix(uses, "docker://") {
		return fmt.Errorf("container actions are unsupported in host mode")
	}
	if !strings.HasPrefix(uses, "./") {
		return fmt.Errorf("remote action %q requires a pinned action resolver", uses)
	}
	root, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, uses))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("local action escapes worktree")
	}
	if visited[path] {
		return fmt.Errorf("local action cycle at %q", uses)
	}
	visited[path] = true
	defer delete(visited, path)
	file, err := os.Open(filepath.Join(path, "action.yml"))
	if os.IsNotExist(err) {
		file, err = os.Open(filepath.Join(path, "action.yaml"))
	}
	if err != nil {
		return err
	}
	defer file.Close()
	action, err := model.ReadAction(file)
	if err != nil {
		return err
	}
	if action.Runs.Using.IsDocker() {
		return fmt.Errorf("container action %q is unsupported in host mode", uses)
	}
	for _, step := range action.Runs.Steps {
		if err := validateAction(root, step.Uses, visited); err != nil {
			return err
		}
	}
	return nil
}
