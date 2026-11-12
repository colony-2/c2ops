//go:build linux || darwin

package jobexec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type completionLog struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	returned atomic.Bool
	active   atomic.Int32
	late     atomic.Int32
}

func (w *completionLog) Write(p []byte) (int, error) {
	w.active.Add(1)
	defer w.active.Add(-1)
	if w.returned.Load() {
		w.late.Add(1)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}

func (w *completionLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

// The executor itself is instrumented when this package is tested with -race.
func TestExecutorDrain(t *testing.T) {
	for _, exit := range []int{0, 7} {
		for repeat := 0; repeat < 5; repeat++ {
			t.Run(fmt.Sprintf("exit%d-%d", exit, repeat), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				var log completionLog
				wf := fmt.Sprintf(`on: workflow_dispatch
jobs:
  probe:
    runs-on: local
    outputs:
      final: ${{ steps.last.outputs.final }}
    steps:
      - id: last
        run: |
          for i in {1..100}; do echo line-$i; done
          echo '::add-mask::final-private-value'
          echo final-private-value
          echo 'final=drained' >> "$GITHUB_OUTPUT"
          echo FINAL-TRAILING-LINE
          exit %d
`, exit)
				result, err := Execute(ctx, Request{Workflow: wf, JobID: "probe"}, Options{Workdir: t.TempDir(), StateDir: t.TempDir(), Log: &log})
				log.returned.Store(true)
				require.Zero(t, log.active.Load(), "output write active at executor return")
				require.NoError(t, err, log.String())
				want := "success"
				if exit != 0 {
					want = "failure"
				}
				require.Equal(t, want, result.Conclusion, log.String())
				require.Equal(t, "drained", result.Outputs["final"], log.String())
				require.Contains(t, log.String(), "FINAL-TRAILING-LINE")
				require.NotContains(t, log.String(), "final-private-value")
				time.Sleep(10 * time.Millisecond)
				require.Zero(t, log.late.Load(), "output write started after executor return")
			})
		}
	}
}

func TestExecutorFailedStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// Pass executable lookup, then fail exec itself with a missing interpreter.
	shell := filepath.Join(t.TempDir(), "bad-shell")
	require.NoError(t, os.WriteFile(shell, []byte("#!/c2ops-missing-interpreter\n"), 0700))
	var log completionLog
	result, err := Execute(ctx, Request{Workflow: fmt.Sprintf(`on: workflow_dispatch
jobs:
  probe:
    runs-on: local
    steps:
      - shell: %s {0}
        run: echo MUST-NOT-RUN
`, shell), JobID: "probe"}, Options{Workdir: t.TempDir(), StateDir: t.TempDir(), Log: &log})
	log.returned.Store(true)
	require.Zero(t, log.active.Load())
	require.NoError(t, err, log.String())
	require.Equal(t, "failure", result.Conclusion, log.String())
	require.Contains(t, log.String(), "no such file or directory")
	time.Sleep(10 * time.Millisecond)
	require.Zero(t, log.late.Load())
}

func TestExecutorDescendants(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			state := t.TempDir()
			pidfile := filepath.Join(dir, "child.pid")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			exit := "exit 0"
			if mode == "failure" {
				exit = "exit 7"
			}
			if mode == "cancel" {
				exit = "wait"
			}
			grandfile := filepath.Join(dir, "grandchild.pid")
			command := fmt.Sprintf("bash -c 'trap \"\" HUP; sleep 60 & echo $! > %s; wait' &\necho $! > %s\nwhile test ! -s %s; do sleep 0.01; done\n%s", grandfile, pidfile, grandfile, exit)
			wf := "on: workflow_dispatch\njobs:\n  probe:\n    runs-on: local\n    steps:\n      - run: |\n          " + strings.ReplaceAll(command, "\n", "\n          ") + "\n"
			done := make(chan struct{})
			var result Result
			var err error
			// Register cleanup before readiness assertions: a timeout must still
			// retire fixture processes and join the execution goroutine.
			t.Cleanup(func() {
				cancel()
				for _, path := range []string{pidfile, grandfile} {
					if raw, err := os.ReadFile(path); err == nil {
						if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 1 {
							_ = syscall.Kill(pid, syscall.SIGKILL)
						}
					}
				}
				select {
				case <-done:
				case <-time.After(6 * time.Second):
					t.Error("executor did not stop during fixture cleanup")
				}
			})
			go func() {
				defer close(done)
				result, err = Execute(ctx, Request{Workflow: wf, JobID: "probe"}, Options{Workdir: dir, StateDir: state})
			}()
			var pid int
			require.Eventually(t, func() bool {
				raw, e := os.ReadFile(pidfile)
				if e != nil {
					return false
				}
				pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
				return pid > 0
			}, 3*time.Second, 10*time.Millisecond)
			var grand int
			require.Eventually(t, func() bool {
				raw, e := os.ReadFile(grandfile)
				if e != nil {
					return false
				}
				grand, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
				return grand > 0
			}, time.Second, 10*time.Millisecond)
			if mode == "cancel" {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("executor did not finish within fixture deadline")
			}
			if mode == "cancel" {
				require.Error(t, err)
				require.NotEqual(t, "success", result.Conclusion)
			} else {
				require.NoError(t, err)
				require.Equal(t, mode, result.Conclusion)
			}
			require.Eventually(t, func() bool { return !processAlive(pid) }, 2*time.Second, 10*time.Millisecond, "fixture descendant %d survived %s", pid, mode)
			require.Eventually(t, func() bool { return !processAlive(grand) }, time.Second, 10*time.Millisecond, "fixture grandchild %d survived %s", grand, mode)
		})
	}
}

func processAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		_, after, _ := strings.Cut(string(raw), ") ")
		if strings.HasPrefix(after, "Z ") {
			return false
		}
	}
	if runtime.GOOS == "darwin" {
		// kill(pid, 0) also succeeds for a zombie. macOS has no /proc.
		if raw, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output(); err == nil && strings.HasPrefix(strings.TrimSpace(string(raw)), "Z") {
			return false
		}
	}
	return true
}
