// This executable is a test-only embedded runtime, outside the production module.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
)

type config struct {
	Recipe, Repo, Revision, Root, HelperAudit, Stages, CancelMode, Ready, PrivateMeta string
	Live                                                                              bool
	SecretNames                                                                       []string
	ResultPath, RecoveryPath                                                          string
	TotalSeconds                                                                      int
}
type provider struct {
	calls        atomic.Int32
	deny         atomic.Bool
	forward      bool
	names        []string
	parentEnv    []string
	parentHelper string
}

func (p *provider) ResolveSecrets(ctx context.Context, r jobdb.SecretResolveRequest) (jobdb.SecretResolveResult, error) {
	p.calls.Add(1)
	if p.forward {
		return p.forwardSecret(ctx, r)
	}
	if p.deny.Load() || !reflect.DeepEqual(r.Names, []string{"fixture_token"}) {
		return jobdb.SecretResolveResult{}, fmt.Errorf("unexpected secret read")
	}
	b, _ := json.Marshal(map[string]string{"access_token": "private-token-sentinel", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"fixture_token": {Value: string(b)}}}, nil
}

type observer struct {
	workflow.TaskWorker
	mu         *sync.Mutex
	dispatches *[]string
	lost       *atomic.Bool
}

func (w observer) Run(tc workflow.TaskContext, d jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, d)
}
func (w observer) RunTask(ctx context.Context, tc workflow.TaskContext, d jobdb.TaskData) (jobdb.TaskData, error) {
	w.mu.Lock()
	*w.dispatches = append(*w.dispatches, w.Name())
	w.mu.Unlock()
	var out jobdb.TaskData
	var err error
	if r, ok := w.TaskWorker.(interface {
		RunTask(context.Context, workflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)
	}); ok {
		out, err = r.RunTask(ctx, tc, d)
	} else {
		out, err = w.TaskWorker.Run(tc, d)
	}
	if errors.Is(context.Cause(ctx), jobdb.ErrExecutionLeaseLost) {
		w.lost.Store(true)
	}
	return out, err
}

func main() {
	if len(os.Args) > 1 {
		helper, e := exec.LookPath("c2j")
		if e != nil {
			panic(e)
		}
		if e = syscall.Exec(helper, append([]string{helper}, os.Args[1:]...), os.Environ()); e != nil {
			panic(e)
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	var c config
	if err := json.NewDecoder(os.Stdin).Decode(&c); err != nil {
		panic(err)
	}
	if err := run(c); err != nil {
		if c.Live {
			var category liveError
			if errors.As(err, &category) {
				fmt.Fprintln(os.Stderr, "native live invocation failed: "+string(category))
			} else {
				fmt.Fprintln(os.Stderr, "native live invocation failed")
			}
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
func run(c config) error {
	limit := 90 * time.Second
	if c.Live {
		if c.TotalSeconds < 60 || c.TotalSeconds > 1800 {
			return fmt.Errorf("invalid live deadline")
		}
		limit = time.Duration(c.TotalSeconds+120) * time.Second
	}
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), limit)
	defer cancel()
	p := &provider{}
	if c.Live {
		helper, e := exec.LookPath("c2j")
		if e != nil {
			return fmt.Errorf("parent helper unavailable")
		}
		p.forward = true
		p.names = append([]string(nil), c.SecretNames...)
		p.parentEnv = os.Environ()
		p.parentHelper = helper
	}
	rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(c.Root, "history.db"), BlobDir: filepath.Join(c.Root, "blobs"), SecretProvider: p})
	if err != nil {
		return err
	}
	defer rt.Close(context.Background())
	const repo = "github.com/opentofu-fixture/config"
	// Use a canonical cell identity and an explicit local Git transport. No network
	// repository or credential is needed by this disposable fixture.
	realGit, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	gitDir := filepath.Join(c.Root, "git-transport")
	if err = os.Mkdir(gitDir, 0700); err != nil {
		return err
	}
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nexec " + quote(realGit) + " -c " + quote("url.file://"+c.Repo+".insteadOf=https://"+repo+".git") + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(gitDir, "git"), []byte(script), 0700); err != nil {
		return err
	}
	os.Setenv("PATH", gitDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	os.Setenv("GIT_ALLOW_PROTOCOL", "file:https")
	_, err = rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "tofu-native", Name: "fixture", Repo: repo})
	if err != nil {
		return err
	}
	coreops.Register(extops.GetExecutionOp())
	registry, err := workerops.NewActivityRegistry()
	if err != nil {
		return err
	}
	guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
	var mu sync.Mutex
	var dispatches []string
	var lost atomic.Bool
	wrap := func(w workflow.TaskWorker) workflow.TaskWorker {
		return guard.WrapTaskWorker(observer{w, &mu, &dispatches, &lost})
	}
	ctl := &workerworkflow.SWFWorkflowControl{}
	ws, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build(), registry, compiler.RecipeJobWorkerOptions{Allocation: guard.Allocation, StageExecution: guard.Stage, WrapTaskWorker: wrap})
	if err != nil {
		return err
	}
	engine, err := workflow.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId("tofu-native").PlusWorkers(ws.JobWorker).BuildEngine()
	if err != nil {
		return err
	}
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}
	ctl.Engine = engine
	raw, err := os.ReadFile(c.Recipe)
	if err != nil {
		return err
	}
	rec, err := recipe.LoadRecipeFromString(raw)
	if err != nil {
		return err
	}
	expanded, err := compiler.ResolveInlineRecipes(ctx, *rec, compiler.InlineResolutionOptions{RootFile: c.Recipe})
	if err != nil {
		return err
	}
	*rec = expanded.Recipe
	job, git := compiler.GenerateTestContext()
	job.GitBase.BaseRepo = repo
	job.GitBase.BaseRef = c.Revision
	job.GitBase.ResolvedBaseHash = c.Revision
	git.ParentRef = c.Revision
	keyref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tofu-native", RecipeName: rec.GetMetadata().ID, JobContext: job, GitRef: git.ParentRef, Cell: "fixture", CellRepo: repo}, engine, *rec)
	if err != nil {
		return err
	}
	key := keyref.Key()
	if p.calls.Load() != 0 {
		return fmt.Errorf("submission resolved secret")
	}
	if c.CancelMode != "" {
		if err = os.WriteFile(c.CancelMode, []byte("cancel"), 0600); err != nil {
			return err
		}
	}
	var taskWorkers []workflow.TaskWorker
	for _, w := range ws.TaskWorkers {
		taskWorkers = append(taskWorkers, w)
	}
	runnable, err := workflow.GetJobForRun(ctx, guard, workflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: taskWorkers, WorkerID: "tofu-native", LeaseDuration: time.Second})
	if err != nil {
		return err
	}
	type result struct {
		out workflow.JobRunOutcome
		err error
	}
	done := make(chan result, 1)
	go func() { o, e := runnable.Run(nil); done <- result{o, e} }()
	joined := false
	defer func() {
		if !joined {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
			}
		}
	}()
	if c.CancelMode != "" {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
	waitReady:
		for {
			select {
			case got := <-done:
				joined = true
				return fmt.Errorf("stopped before real plan boundary: %+v %v", got.out, got.err)
			case <-ctx.Done():
				return fmt.Errorf("real stage entry timed out")
			case <-ticker.C:
				if _, e := os.Stat(c.Ready); e == nil {
					break waitReady
				}
			}
		}
		pids, privateDir, e := processSnapshot(c.PrivateMeta)
		if e != nil {
			return e
		}
		defer func() {
			for _, pid := range pids {
				if processAlive(pid) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
			_ = os.RemoveAll(privateDir)
		}()
		stageBefore, e := os.ReadFile(c.Stages)
		if e != nil {
			return e
		}
		mu.Lock()
		baseline := append([]string(nil), dispatches...)
		mu.Unlock()
		start := time.Now()
		if err = rt.CancelJob(celltest.Context(context.Background()), jobdb.CancelJobRequest{JobKey: key, Reason: "OpenTofu lease-loss qualification"}); err != nil {
			return err
		}
		select {
		case got := <-done:
			joined = true
			if got.err != nil && !errors.Is(got.err, jobdb.ErrExecutionLeaseLost) {
				return fmt.Errorf("unexpected cancellation: %w", got.err)
			}
			if got.out.Output != nil {
				return fmt.Errorf("cancelled job returned final output")
			}
		case <-time.After(8 * time.Second):
			return fmt.Errorf("lease-loss shutdown exceeded eight seconds")
		}
		// The recipe runner may return as soon as its lease is lost. Join the
		// actual extension task as well before asserting process/file cleanup.
		taskJoin := time.NewTicker(10 * time.Millisecond)
		for !lost.Load() && time.Since(start) < 8*time.Second {
			<-taskJoin.C
		}
		taskJoin.Stop()
		if !lost.Load() {
			return fmt.Errorf("extension task did not join after lease loss")
		}
		for _, pid := range pids {
			if processAlive(pid) {
				info, _ := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "pid=", "-o", "ppid=", "-o", "pgid=", "-o", "comm=").Output()
				return fmt.Errorf("descendant survived native cancellation after %s: %s (private %s)", time.Since(start), info, privateDir)
			}
		}
		if _, e := os.Stat(privateDir); !os.IsNotExist(e) {
			return fmt.Errorf("private invocation survived native cancellation: %s", privateDir)
		}
		stageAfter, e := os.ReadFile(c.Stages)
		if e != nil {
			return e
		}
		if !bytes.Equal(stageBefore, stageAfter) {
			return fmt.Errorf("OpenTofu stage ran after revocation")
		}
		if !lost.Load() {
			return fmt.Errorf("task did not observe actual execution lease loss")
		}
		mu.Lock()
		same := reflect.DeepEqual(baseline, dispatches)
		mu.Unlock()
		if !same {
			return fmt.Errorf("task dispatched after lease revocation")
		}
		info, e := rt.GetJob(ctx, key)
		if e != nil {
			return e
		}
		if info.Status != jobdb.JobStatusCancelled {
			return fmt.Errorf("job not cancelled: %s", info.Status)
		}
		if p.calls.Load() != 1 {
			return fmt.Errorf("cancelled invocation changed secret resolution count")
		}
		stored, e := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
		if e != nil {
			return e
		}
		for _, chapter := range stored {
			if strings.HasPrefix(chapter.TaskType, "extension_execution") {
				if _, ok := chapter.Body.(jobdb.TaskAttemptOutcomeChapter); ok {
					return fmt.Errorf("cancelled extension persisted a completed result")
				}
			}
		}
		n, a, e := scanHistory(ctx, rt, key, c.Repo)
		if e != nil {
			return e
		}
		fmt.Printf("lease revoked during real plan; task cause verified; no later dispatch; %d chapters/%d artifacts scanned; shutdown %s\n", n, a, time.Since(start).Round(time.Millisecond))
		return nil
	}
	select {
	case got := <-done:
		joined = true
		if got.err != nil {
			return got.err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	resultData, err := swfutil.JobResult(ctx, engine, key)
	if err != nil {
		return err
	}
	if c.Live {
		return liveResult(ctx, rt, key, resultData, p, c)
	}
	// Direct extension transport succeeds while failed deployment data remains false.
	b, err := json.Marshal(resultData)
	if err != nil {
		return err
	}
	if !bytes.Contains(b, []byte(`"ok":false`)) || !bytes.Contains(b, []byte("c2ops.opentofu.recovery/v1")) {
		return fmt.Errorf("missing failed deployment/recovery result: %s", b)
	}
	n, a, err := scanHistory(ctx, rt, key, c.Repo)
	if err != nil {
		return err
	}
	before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
	if err != nil {
		return err
	}
	calls := p.calls.Load()
	if calls != 1 {
		return fmt.Errorf("expected one provider read, got %d", calls)
	}
	helper, err := os.ReadFile(c.HelperAudit)
	if err != nil {
		return err
	}
	stages, err := os.ReadFile(c.Stages)
	if err != nil {
		return err
	}
	p.deny.Store(true)
	var replayScopes atomic.Int32
	replayed, replayErr := engine.ReplayJobRun(ctx, workflow.ReplayRunRequest{JobKey: key, JobWorker: compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true, BeforeScope: func(context.Context, string, *template.ResolutionContext) error { replayScopes.Add(1); return nil }})})
	err = replayErr
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	if replayScopes.Load() == 0 || replayed == nil {
		return fmt.Errorf("replay did not traverse recipe scopes")
	}
	originalData, e := resultData.GetData()
	if e != nil {
		return e
	}
	replayedData, e := replayed.GetData()
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(originalData, replayedData) {
		return fmt.Errorf("replay returned different deployment data")
	}
	after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
	if err != nil {
		return err
	}
	helperAfter, e := os.ReadFile(c.HelperAudit)
	if e != nil {
		return e
	}
	stagesAfter, e := os.ReadFile(c.Stages)
	if e != nil {
		return e
	}
	if p.calls.Load() != calls || !bytes.Equal(helper, helperAfter) || !bytes.Equal(stages, stagesAfter) || !reflect.DeepEqual(before, after) {
		return fmt.Errorf("completed replay changed history or side effects")
	}
	if _, _, err = scanHistory(ctx, rt, key, c.Repo); err != nil {
		return err
	}
	if err = rt.Close(ctx); err != nil {
		return err
	}
	reopened, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(c.Root, "history.db"), BlobDir: filepath.Join(c.Root, "blobs"), SecretProvider: p})
	if err != nil {
		return err
	}
	defer reopened.Close(context.Background())
	rn, ra, err := scanHistory(ctx, reopened, key, c.Repo)
	if err != nil {
		return err
	}
	if rn != n || ra != a {
		return fmt.Errorf("reopened history inventory differs")
	}
	fmt.Printf("completed native replay; provider/helper/command counts unchanged; %d chapters/%d artifacts scanned\n", n, a)
	return nil
}

// Record the whole host process tree while the real Google request is pending.
func processSnapshot(meta string) ([]int, string, error) {
	b, e := os.ReadFile(meta)
	if e != nil {
		return nil, "", e
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		return nil, "", fmt.Errorf("invalid private process ledger")
	}
	selected := map[int]bool{}
	for _, line := range lines[:2] {
		pid, e := strconv.Atoi(line)
		if e != nil || pid <= 1 {
			return nil, "", fmt.Errorf("invalid process id")
		}
		selected[pid] = true
	}
	out, e := exec.Command("ps", "-A", "-o", "pid=", "-o", "ppid=").Output()
	if e != nil {
		return nil, "", e
	}
	changed := true
	for changed {
		changed = false
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			pid, e := strconv.Atoi(f[0])
			if e != nil {
				return nil, "", e
			}
			ppid, e := strconv.Atoi(f[1])
			if e != nil {
				return nil, "", e
			}
			if selected[ppid] && !selected[pid] {
				selected[pid] = true
				changed = true
			}
		}
	}
	var pids []int
	for pid := range selected {
		if !processAlive(pid) {
			return nil, "", fmt.Errorf("descendant exited before cancellation")
		}
		pids = append(pids, pid)
	}
	return pids, filepath.Dir(lines[2]), nil
}
func processAlive(pid int) bool {
	out, e := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	return e == nil && strings.TrimSpace(string(out)) != "" && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
