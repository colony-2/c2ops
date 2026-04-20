package gha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
	actcommon "github.com/nektos/act/pkg/common"
	actmodel "github.com/nektos/act/pkg/model"
	actrunner "github.com/nektos/act/pkg/runner"
	"github.com/sirupsen/logrus"
)

var actBackendFactory = func() workflowBackend {
	return &actBackend{}
}

type actBackend struct{}

func (b *actBackend) Run(ctx context.Context, req backendRequest) (backendResult, error) {
	if err := ensureDockerAvailable(); err != nil {
		return backendResult{}, fmt.Errorf("docker is required for backend %q: %w", backendLocal, err)
	}

	isolatedWorktree, err := cloneGitWorktree(ctx, req.GitContext.WorktreePath)
	if err != nil {
		return backendResult{}, err
	}
	defer os.RemoveAll(isolatedWorktree)

	workflowPath, err := localWorkflowPath(req.Workflow.Path, req.GitContext.WorktreePath, isolatedWorktree)
	if err != nil {
		return backendResult{}, err
	}

	planner, err := actmodel.NewWorkflowPlanner(workflowPath, true, false)
	if err != nil {
		return backendResult{}, err
	}

	plan, err := buildActPlan(planner)
	if err != nil {
		return backendResult{}, err
	}
	if len(plan.Stages) == 0 {
		return backendResult{}, fmt.Errorf("workflow %q has no runnable jobs for event %q", workflowPath, workflowDispatchEvent)
	}

	artifactRoot, err := os.MkdirTemp("", "c2-gha-artifacts-*")
	if err != nil {
		return backendResult{}, err
	}
	logRoot, err := os.MkdirTemp("", "c2-gha-logs-*")
	if err != nil {
		return backendResult{}, err
	}

	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	localReq := req
	localReq.GitContext.WorktreePath = isolatedWorktree
	eventPath, err := writeEventPayload(localReq)
	if err != nil {
		return backendResult{}, err
	}
	defer os.Remove(eventPath)

	artifactPort, err := reserveLocalPort()
	if err != nil {
		return backendResult{}, err
	}

	capture := newActLogCapture()
	jobLoggerFactory := &actJobLoggerFactory{
		hook: &lockedHook{hook: capture},
	}
	logger := logrus.New()
	logger.SetOutput(os.Stderr)
	logger.SetLevel(logrus.TraceLevel)

	runnerConfig := &actrunner.Config{
		Actor:              "colony2",
		Workdir:            isolatedWorktree,
		EventName:          workflowDispatchEvent,
		EventPath:          eventPath,
		DefaultBranch:      githubRefName(localReq.GitContext),
		Env:                buildActEnv(localReq, runID),
		Inputs:             stringifyInputs(req.Input.With),
		Secrets:            copyStringMap(req.Input.Secrets),
		Token:              strings.TrimSpace(req.Input.Secrets["GITHUB_TOKEN"]),
		Platforms:          defaultActPlatforms(req.Input.RunnerImage),
		LogOutput:          false,
		ArtifactServerPath: artifactRoot,
		ArtifactServerAddr: "127.0.0.1",
		ArtifactServerPort: artifactPort,
		GitHubInstance:     "github.com",
	}

	r, err := actrunner.New(runnerConfig)
	if err != nil {
		return backendResult{}, err
	}

	startedAt := time.Now()
	runCtx := actcommon.WithLogger(ctx, logger)
	runCtx = actrunner.WithJobLoggerFactory(runCtx, jobLoggerFactory)

	execErr := r.NewPlanExecutor(plan)(runCtx)
	durationSeconds := int(time.Since(startedAt).Seconds())

	jobs, combinedLogs, perJobLogs := capture.snapshot()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(execErr, context.DeadlineExceeded)
	if execErr != nil && !timedOut && len(jobs) == 0 {
		return backendResult{}, execErr
	}

	status, exitCode := deriveWorkflowStatus(jobs, timedOut)
	if execErr != nil && status == statusSuccess {
		status = statusFailure
		exitCode = 1
	}

	logRefs, err := writeLogArtifacts(logRoot, combinedLogs, perJobLogs)
	if err != nil {
		return backendResult{}, err
	}
	artifactRefs, err := collectActArtifacts(artifactRoot, runID)
	if err != nil {
		return backendResult{}, err
	}

	return backendResult{
		Output: RunOutput{
			Status:          status,
			ExitCode:        exitCode,
			DurationSeconds: durationSeconds,
			Workflow: WorkflowOutput{
				ResolvedSelector: req.Workflow.Selector,
				ResolvedCommit:   req.Workflow.ResolvedCommit,
				ContentHash:      req.Workflow.ContentHash,
			},
			Jobs: jobs,
		},
		ArtifactRefs: mergeArtifactRefs(artifactRefs, logRefs),
	}, nil
}

func ensureDockerAvailable() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return err
	}
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker daemon is not reachable: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	if strings.TrimSpace(string(output)) == "" {
		return fmt.Errorf("docker daemon did not report a server version")
	}
	return nil
}

const workflowDispatchEvent = "workflow_dispatch"

type actJobLoggerFactory struct {
	hook logrus.Hook
}

func (f *actJobLoggerFactory) WithJobLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	logger.SetLevel(logrus.TraceLevel)
	logger.SetFormatter(&logrus.JSONFormatter{})
	if f.hook != nil {
		logger.AddHook(f.hook)
	}
	return logger
}

type lockedHook struct {
	hook logrus.Hook
	mu   sync.Mutex
}

func (h *lockedHook) Levels() []logrus.Level {
	return h.hook.Levels()
}

func (h *lockedHook) Fire(entry *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hook.Fire(entry)
}

func buildActPlan(planner actmodel.WorkflowPlanner) (*actmodel.Plan, error) {
	return planner.PlanEvent(workflowDispatchEvent)
}

func stringifyInputs(with map[string]any) map[string]string {
	if len(with) == 0 {
		return nil
	}
	out := make(map[string]string, len(with))
	for key, value := range with {
		out[key] = fmt.Sprint(value)
	}
	return out
}

func copyStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func defaultActPlatforms(override string) map[string]string {
	image := strings.TrimSpace(override)
	if image == "" {
		return map[string]string{
			"ubuntu-latest": "node:16-buster-slim",
			"ubuntu-22.04":  "node:16-bullseye-slim",
			"ubuntu-20.04":  "node:16-buster-slim",
			"ubuntu-18.04":  "node:16-buster-slim",
		}
	}
	return map[string]string{
		"ubuntu-latest": image,
		"ubuntu-22.04":  image,
		"ubuntu-20.04":  image,
		"ubuntu-18.04":  image,
	}
}

func buildActEnv(req backendRequest, runID string) map[string]string {
	env := copyStringMap(req.Input.Env)
	if env == nil {
		env = make(map[string]string)
	}
	sha := resolvedCommit(req.GitContext)
	env["GITHUB_SERVER_URL"] = "https://github.com"
	env["GITHUB_API_URL"] = "https://api.github.com"
	env["GITHUB_GRAPHQL_URL"] = "https://api.github.com/graphql"
	env["GITHUB_REPOSITORY"] = req.GitContext.BaseRepo
	env["GITHUB_REF"] = githubRef(req.GitContext)
	env["GITHUB_REF_NAME"] = githubRefName(req.GitContext)
	env["GITHUB_REF_TYPE"] = githubRefType(req.GitContext)
	env["GITHUB_SHA"] = sha
	env["SHA_REF"] = sha
	env["GITHUB_WORKSPACE"] = req.GitContext.WorktreePath
	env["GITHUB_REPOSITORY_OWNER"] = repositoryOwner(req.GitContext.BaseRepo)
	env["GITHUB_RUN_ID"] = runID
	env["GITHUB_RUN_NUMBER"] = runID
	env["GITHUB_RUN_ATTEMPT"] = "1"
	env["GITHUB_RETENTION_DAYS"] = "0"
	return env
}

func writeEventPayload(req backendRequest) (string, error) {
	body, err := json.Marshal(buildEventPayload(req))
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "c2-gha-event-*.json")
	if err != nil {
		return "", err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return file.Name(), nil
}

func buildEventPayload(req backendRequest) map[string]interface{} {
	repoName := req.GitContext.BaseRepo
	owner := repositoryOwner(repoName)
	ref := githubRef(req.GitContext)
	sha := resolvedCommit(req.GitContext)
	event := map[string]interface{}{
		"ref":    ref,
		"after":  sha,
		"before": strings.TrimSpace(req.GitContext.ParentHash),
		"repository": map[string]interface{}{
			"full_name":      repoName,
			"name":           repositoryName(repoName),
			"default_branch": githubRefName(req.GitContext),
			"owner": map[string]interface{}{
				"login": owner,
			},
		},
		"sender": map[string]interface{}{
			"login": "colony2",
		},
	}
	if len(req.Input.With) > 0 {
		event["inputs"] = stringifyInputs(req.Input.With)
	}
	return event
}

func localWorkflowPath(workflowPath string, sourceWorktree string, isolatedWorktree string) (string, error) {
	if strings.TrimSpace(workflowPath) == "" {
		return "", fmt.Errorf("workflow path is required")
	}
	sourceAbs, err := filepath.Abs(sourceWorktree)
	if err != nil {
		return "", err
	}
	workflowAbs, err := filepath.Abs(workflowPath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(sourceAbs, workflowAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return workflowAbs, nil
	}
	return filepath.Join(isolatedWorktree, rel), nil
}

func githubRef(gitCtx coreops.GitExecutionContext) string {
	ref := strings.TrimSpace(gitCtx.BaseRef)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}

func githubRefName(gitCtx coreops.GitExecutionContext) string {
	ref := githubRef(gitCtx)
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/tags/"):
		return strings.TrimPrefix(ref, "refs/tags/")
	default:
		return strings.TrimPrefix(ref, "refs/")
	}
}

func githubRefType(gitCtx coreops.GitExecutionContext) string {
	ref := githubRef(gitCtx)
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return "branch"
	case strings.HasPrefix(ref, "refs/tags/"):
		return "tag"
	default:
		return ""
	}
}

func resolvedCommit(gitCtx coreops.GitExecutionContext) string {
	if hash := strings.TrimSpace(gitCtx.PersistHash); hash != "" {
		return hash
	}
	if hash := strings.TrimSpace(gitCtx.ResolvedBaseHash); hash != "" {
		return hash
	}
	return strings.TrimSpace(gitCtx.BaseRef)
}

func repositoryOwner(repo string) string {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

func repositoryName(repo string) string {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func writeLogArtifacts(root string, combined []string, perJob map[string][]string) (map[string]externalFileRef, error) {
	refs := make(map[string]externalFileRef, len(perJob)+1)
	combinedPath := filepath.Join(root, "combined.log")
	if err := os.WriteFile(combinedPath, []byte(strings.Join(combined, "\n")), 0o644); err != nil {
		return nil, err
	}
	refs["gha-logs"] = externalFileRef{Path: combinedPath}
	for jobKey, lines := range perJob {
		jobPath := filepath.Join(root, sanitizeArtifactName(jobKey)+".log")
		if err := os.MkdirAll(filepath.Dir(jobPath), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(jobPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return nil, err
		}
		refs[filepath.ToSlash(filepath.Join("gha-logs", jobKey))] = externalFileRef{Path: jobPath}
	}
	return refs, nil
}

func collectActArtifacts(root string, runID string) (map[string]externalFileRef, error) {
	runRoot := filepath.Join(root, runID)
	entries, err := os.ReadDir(runRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	refs := make(map[string]externalFileRef, len(entries))
	for _, entry := range entries {
		name := sanitizeArtifactName(entry.Name())
		if name == "" {
			continue
		}
		refs[name] = externalFileRef{Path: filepath.Join(runRoot, entry.Name())}
	}
	return refs, nil
}

func reserveLocalPort() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "", err
	}
	return port, nil
}
