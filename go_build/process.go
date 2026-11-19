package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type runner func(context.Context, string, []string, string, []string) ([]byte, error)

// Every external process gets a separate group. Context cancellation (including
// SIGTERM to this op) kills the whole group, including credential helpers.
func command(ctx context.Context, exe string, args []string, cwd string, env []string) ([]byte, error) {
	c := exec.CommandContext(ctx, exe, args...)
	c.Dir = cwd
	c.Env = env
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	c.WaitDelay = 3 * time.Second
	var out bytes.Buffer
	c.Stdout = &out
	// Never persist raw subprocess diagnostics. They can include URLs containing
	// credentials, Git helper command text or source with embedded secrets.
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("operation cancelled")
		}
		return nil, fmt.Errorf("subprocess failed")
	}
	return out.Bytes(), nil
}
func cleanEnv() []string {
	out := []string{}
	keepGo := map[string]bool{"GOPRIVATE": true, "GONOPROXY": true, "GONOSUMDB": true, "GOPROXY": true, "GOSUMDB": true, "GOAUTH": true}
	routing := map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_COMMON_DIR": true, "GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true, "GIT_REPLACE_REF_BASE": true, "GIT_SHALLOW_FILE": true, "GIT_CONFIG": true, "GIT_CEILING_DIRECTORIES": true, "GIT_DISCOVERY_ACROSS_FILESYSTEM": true}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if routing[k] || strings.HasPrefix(k, "GO") && !keepGo[k] || k == "CGO_ENABLED" {
			continue
		}
		out = append(out, kv)
	}
	// Scoped GIT_CONFIG_COUNT/KEY_n/VALUE_n, GLOBAL, credential sockets, HOME and
	// Git authorization are retained. They are deliberately never serialized.
	return envWith(out, map[string]string{"GIT_NO_REPLACE_OBJECTS": "1", "GIT_TERMINAL_PROMPT": "0"})
}
func envWith(env []string, values map[string]string) []string {
	out := []string{}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := values[k]; !ok {
			out = append(out, kv)
		}
	}
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	return out
}
