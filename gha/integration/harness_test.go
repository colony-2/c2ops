package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

const tenant = "gha-integration"

var registerOnce sync.Once

// harness is a test provisioner, not a scheduler: JobDB decides leaseability,
// c2j runs recipes and child waits, and all task results live in SQLite/blobfs.
// No background engine loop runs, so tests can stop at any durable boundary.
type harness struct {
	t           *testing.T
	ctx         context.Context
	root        string
	repo        string
	commit      string
	recipes     map[string]string
	db          *sqlite.Runtime
	runtime     *executionruntime.Runtime
	engine      jobworkflow.Engine
	workers     *jobworkflow.WorkSet
	mu          sync.Mutex
	calls       []string
	secrets     map[string]string
	secretReads int
}

func newHarness(t *testing.T, recipes map[string]string) *harness {
	t.Helper()
	registerOnce.Do(func() {
		coreops.Register(commandop.GetOp())
		coreops.Register(extops.GetExecutionOp())
		coreops.Register(recipeops.GetOps()...)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	// Use /tmp explicitly: macOS's inherited TMPDIR is too long once workers
	// append c2j-git-<random>/resolver.sock (sun_path allows only 103 bytes).
	root, err := os.MkdirTemp("/tmp", "gha-it-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	h := &harness{t: t, ctx: ctx, root: root, recipes: recipes, secrets: map[string]string{}}
	// Set once before starting workers. Concurrent invocations share this temp
	// root but c2j/act allocate unique subdirectories; no goroutine changes env.
	workerRoot := filepath.Join(h.root, "workers")
	require.NoError(t, os.Mkdir(workerRoot, 0700))
	t.Setenv("TMPDIR", workerRoot)
	bin := filepath.Join(h.root, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	dockerMarker := filepath.Join(h.root, "docker-invoked")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\ntouch \"$GHA_DOCKER_SENTINEL\"\nexit 97\n"), 0700))
	t.Setenv("GHA_DOCKER_SENTINEL", dockerMarker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(h.root, "no-docker.sock"))
	t.Cleanup(func() { require.NoFileExists(t, dockerMarker, "fast suite must never invoke Docker") })
	h.repo = filepath.Join(h.root, "source")
	require.NoError(t, os.Mkdir(h.repo, 0o700))
	for _, args := range [][]string{
		{"init", "-b", "main"}, {"config", "user.email", "fixture@example.test"},
		{"config", "user.name", "GHA fixture"},
	} {
		h.git(args...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(h.repo, "README.md"), []byte("fixture\n"), 0o600))
	for name, text := range recipes {
		require.Equal(t, filepath.Base(name), name, "fixture recipe name must be a basename")
		dir := filepath.Join(h.repo, ".c2", "recipes")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(text), 0o600))
	}
	h.git("add", ".")
	h.git("-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	h.commit = h.git("rev-parse", "HEAD")
	h.open()
	t.Cleanup(func() {
		if h.db != nil {
			require.NoError(t, h.db.Close(context.Background()))
		}
	})
	return h
}

func (h *harness) seed(files map[string]string) {
	h.t.Helper()
	for name, content := range files {
		clean := filepath.Clean(name)
		require.False(h.t, filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../"))
		path := filepath.Join(h.repo, clean)
		require.NoError(h.t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(h.t, os.WriteFile(path, []byte(content), 0o600))
	}
	h.git("add", ".")
	h.git("-c", "commit.gpgsign=false", "commit", "-m", "seed workflow fixtures")
	h.commit = h.git("rev-parse", "HEAD")
}

func (h *harness) git(args ...string) string {
	h.t.Helper()
	cmd := exec.CommandContext(h.ctx, "git", append([]string{"-C", h.repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	require.NoError(h.t, err, "git: %s", out)
	return strings.TrimSpace(string(out))
}

func (h *harness) open() {
	h.t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlite.NewFromConfig(h.ctx, sqlite.Config{DBPath: filepath.Join(h.root, "jobs.db"), Logger: logger, SecretProvider: h})
	require.NoError(h.t, err)
	h.db = db
	platform := runtime.GOOS + "/" + runtime.GOARCH
	h.runtime = executionruntime.New(db, execution.Allocation{SchemaVersion: 1, Platform: &platform})
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(h.runtime).WithWorkerTenantId(tenant).BuildEngine()
	require.NoError(h.t, err)
	h.engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: db}
	provider := func(_ string, name string) (*recipe.Recipe, error) {
		text, ok := h.recipes[name]
		if !ok {
			return nil, fmt.Errorf("unknown fixture recipe %q", name)
		}
		return recipe.LoadRecipeFromString([]byte(text))
	}
	control := &workerworkflow.SWFWorkflowControl{Engine: h.engine, Registry: provider}
	registry, err := workerops.NewActivityRegistry()
	require.NoError(h.t, err)
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(control).Build()
	h.workers, err = compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{
		Logger: logger, Allocation: h.runtime.Allocation, StageExecution: h.runtime.Stage,
		RootSourceResolver: compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{
			RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(provider),
		}),
		WrapTaskWorker: func(worker jobworkflow.TaskWorker) jobworkflow.TaskWorker {
			return h.runtime.WrapTaskWorker(recordTask{inner: worker, harness: h})
		},
	})
	require.NoError(h.t, err)
}

func (h *harness) reopen() {
	h.t.Helper()
	require.NoError(h.t, h.db.Close(h.ctx))
	h.db = nil
	h.open()
}

func (h *harness) submit(text string, inputs map[string]any) jobdb.JobKey {
	h.t.Helper()
	def, err := recipe.LoadRecipeFromString([]byte(text))
	require.NoError(h.t, err)
	ref, err := starter.StartRecipeJob(h.ctx, workflowctl.StartJob{
		TenantId: tenant, RecipeName: def.GetMetadata().ID, Inputs: inputs, GitRef: h.commit,
		JobContext: contextual.JobContext{
			Workflow: contextual.WorkflowContext{CellName: "fixture"},
			GitBase:  contextual.GitBaseContext{BaseRepo: h.repo, BaseRef: h.commit, ResolvedBaseHash: h.commit, GitAuthor: "GHA fixture <fixture@example.test>"},
		},
	}, h.engine, *def)
	require.NoError(h.t, err)
	return ref.Key()
}

func (h *harness) step(key jobdb.JobKey) jobworkflow.JobRunOutcome {
	h.t.Helper()
	var tasks []jobworkflow.TaskWorker
	for _, worker := range h.workers.TaskWorkers {
		tasks = append(tasks, worker)
	}
	runnable, err := jobworkflow.GetJobForRun(h.ctx, h.runtime, jobworkflow.GetJobForRunRequest{
		JobKey: key, JobWorker: h.workers.JobWorker, TaskWorkers: tasks,
		ConsumerVersions: compiler.ExecutionConsumerVersions(nil),
		WorkerID:         "fixture", LeaseDuration: time.Minute, AwaitThreshold: time.Nanosecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(h.t, err)
	out, err := runnable.Run(nil)
	require.NoError(h.t, err)
	if out.JobError != nil {
		h.t.Logf("job %s: %v", key.JobId, out.JobError)
	}
	return out
}

func (h *harness) jobs() []jobdb.JobSummary {
	h.t.Helper()
	var jobs []jobdb.JobSummary
	page := ""
	for {
		result, err := h.db.ListJobs(h.ctx, jobdb.ListJobsRequest{TenantIds: []string{tenant}, PageSize: 200, PageToken: page})
		require.NoError(h.t, err)
		jobs = append(jobs, result.Jobs...)
		page = result.NextPageToken
		if page == "" {
			break
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].JobKey.JobId < jobs[j].JobKey.JobId })
	return jobs
}

func (h *harness) drain(root jobdb.JobKey) map[string]any {
	h.t.Helper()
	for iteration := 0; iteration < 100; iteration++ {
		progress := false
		for _, job := range h.jobs() {
			out := h.step(job.JobKey)
			if job.JobKey == root {
				if out.Status == jobworkflow.JobRunFailed {
					h.t.Fatalf("root failed: %v", out.JobError)
				}
				if out.Status == jobworkflow.JobRunCompleted {
					return h.result(root)
				}
			}
			progress = progress || out.LeaseAcquired
		}
		if !progress {
			h.t.Fatalf("no leaseable work; jobs: %+v; live tasks: %v", h.jobs(), h.calls)
		}
	}
	h.t.Fatalf("harness iteration limit reached; jobs: %+v", h.jobs())
	return nil
}

func (h *harness) result(key jobdb.JobKey) map[string]any {
	h.t.Helper()
	info, err := h.db.GetJob(h.ctx, key)
	require.NoError(h.t, err)
	require.NotNil(h.t, info.Data)
	raw, err := info.Data.GetData()
	require.NoError(h.t, err)
	var out map[string]any
	require.NoError(h.t, json.Unmarshal(raw, &out))
	return out
}

type recordTask struct {
	inner   jobworkflow.TaskWorker
	harness *harness
}

func (w recordTask) Name() string { return w.inner.Name() }
func (w recordTask) Run(ctx jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(ctx.Context(), ctx, in)
}

// Preserve the scoped context installed by executionruntime (permissions,
// secret redaction and Git credentials); calling inner.Run would discard it.
func (w recordTask) RunTask(local context.Context, ctx jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	w.harness.mu.Lock()
	w.harness.calls = append(w.harness.calls, ctx.JobKey.JobId+":"+w.Name())
	w.harness.mu.Unlock()
	if worker, ok := w.inner.(interface {
		RunTask(context.Context, jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)
	}); ok {
		return worker.RunTask(local, ctx, in)
	}
	return w.inner.Run(ctx, in)
}

// Synthetic credentials remain outside SQLite, just like a production secret
// provider. Counting resolutions lets replay tests check the live boundary.
func (h *harness) ResolveSecrets(ctx context.Context, req jobdb.SecretResolveRequest) (jobdb.SecretResolveResult, error) {
	if err := ctx.Err(); err != nil {
		return jobdb.SecretResolveResult{}, err
	}
	if req.TenantID != tenant {
		return jobdb.SecretResolveResult{}, jobdb.ErrSecretResolutionFailed
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.secretReads++
	out := jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{}}
	for _, name := range req.Names {
		if value, ok := h.secrets[name]; ok {
			out.Secrets[name] = jobdb.SecretValue{Value: value}
		}
	}
	return out, nil
}
