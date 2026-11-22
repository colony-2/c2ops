package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// These paths are linked by the Nix builder; production has no environment
// override or caller-selected executable. Tests use private Go dependencies.
var gitPath, tofuPath, trustPath, toolPath string

type command struct {
	Path, Dir          string
	Args, Env          []string
	Limit              int
	CaptureDiagnostics bool
	InterruptGrace     time.Duration
}
type runResult struct {
	Code *int
	Data []byte
	Err  error
}
type runner func(context.Context, command) runResult

// One shared two-minute shutdown window covers process termination, recovery,
// and cleanup. A later action never resets it.
type shutdownKey struct{}
type shutdownBudget struct {
	sync.Mutex
	deadline time.Time
}

func (b *shutdownBudget) begin() time.Time {
	b.Lock()
	defer b.Unlock()
	if b.deadline.IsZero() {
		b.deadline = time.Now().Add(2 * time.Minute)
	}
	return b.deadline
}
func budgetFrom(ctx context.Context) *shutdownBudget {
	if b, ok := ctx.Value(shutdownKey{}).(*shutdownBudget); ok {
		return b
	}
	return &shutdownBudget{}
}
func boundedCleanup(path string, deadline time.Time) error {
	done := make(chan error, 1)
	go func() { done <- os.RemoveAll(path) }()
	timer := time.NewTimer(max(time.Until(deadline), 0))
	defer timer.Stop()
	select {
	case e := <-done:
		return e
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

type boundedBuffer struct {
	sync.Mutex
	b        []byte
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := len(p)
	left := b.limit - len(b.b)
	if n > left {
		b.overflow = true
		p = p[:left]
	}
	b.b = append(b.b, p...)
	return n, nil
}

// A cancelled stage interrupts the whole process group, then force-kills all
// descendants even when the group leader has already exited.
func runProcess(ctx context.Context, c command) runResult {
	if ctx.Err() != nil {
		return runResult{Err: ctx.Err()}
	}
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.WaitDelay = time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	n := c.Limit
	if n == 0 {
		n = 8 << 20
	}
	b := &boundedBuffer{limit: n}
	cmd.Stdout = b
	cmd.Stderr = io.Discard
	if c.CaptureDiagnostics {
		cmd.Stderr = b
	}
	// Diagnostics are drained and discarded; private stdout is bounded in memory.
	if e := cmd.Start(); e != nil {
		return runResult{Err: e}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var e error
	select {
	case e = <-done:
		// Ensure a successful tool cannot leave background processes behind.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
		deadline := budgetFrom(ctx).begin()
		// c2j escalates termination of this extension after two seconds. Leave
		// room to kill our separate tool group, encrypt available state and
		// remove private files before that enclosing deadline.
		grace := 500 * time.Millisecond
		if c.InterruptGrace > 0 {
			grace = min(grace, c.InterruptGrace)
		}
		timer := time.NewTimer(min(grace, max(time.Until(deadline), 0)))
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		timer.Stop()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		// WaitDelay bounds pipes held open by descendants.
		e = ctx.Err()
	}
	b.Lock()
	data := append([]byte(nil), b.b...)
	overflow := b.overflow
	b.Unlock()
	if overflow && c.Limit > 0 {
		return runResult{Err: errInvalid}
	}
	var code *int
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() >= 0 {
		v := cmd.ProcessState.ExitCode()
		code = &v
	}
	if _, normalExit := e.(*exec.ExitError); normalExit && code != nil {
		e = nil
	}
	return runResult{Code: code, Data: data, Err: e}
}
func privateEnv(home, data string) []string {
	return []string{"PATH=" + toolPath, "HOME=" + home, "XDG_CONFIG_HOME=" + home, "XDG_CACHE_HOME=" + home, "TMPDIR=" + home, "LANG=C", "LC_ALL=C", "SSL_CERT_FILE=" + trustPath, "TF_IN_AUTOMATION=1", "TF_INPUT=0", "TF_DATA_DIR=" + data, "CHECKPOINT_DISABLE=1", "GCE_METADATA_HOST=127.0.0.1:1"}
}
func gitEnv() []string {
	return []string{"PATH=" + toolPath, "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "LANG=C", "LC_ALL=C"}
}
func successful(r runResult) bool { return r.Err == nil && r.Code != nil && *r.Code == 0 }
func resolveSecret(ctx context.Context, helper string, env []string, name string) ([]byte, error) {
	r := runProcess(ctx, command{Path: helper, Args: []string{"secret", "get", name, "--format=raw"}, Env: env, Limit: maxInput})
	if !successful(r) || len(r.Data) == 0 {
		return nil, errInvalid
	}
	return r.Data, nil
}
func makePrivate(dir string) error { return os.MkdirAll(dir, 0700) }
