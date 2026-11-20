package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func candidate(t *testing.T) string {
	t.Helper()
	if exe := os.Getenv("C2OPS_RELEASE_PACK_TEST_BIN"); exe != "" {
		return exe
	}
	exe := filepath.Join(t.TempDir(), "release-pack")
	c := exec.Command("go", "build", "-mod=readonly", "-o", exe, ".")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("candidate build: %v %s", e, b)
	}
	return exe
}
func invoke(exe string, r Request, cwd string, env []string) ([]byte, []byte, error) {
	b, _ := json.Marshal(r)
	c := exec.Command(exe)
	c.Dir = cwd
	c.Env = env
	c.Stdin = bytes.NewReader(b)
	var out, diag bytes.Buffer
	c.Stdout = &out
	c.Stderr = &diag
	e := c.Run()
	return out.Bytes(), diag.Bytes(), e
}
func TestC02X06ProcessHandoff(t *testing.T) {
	exe := candidate(t)
	r := fixture(t)
	producer := t.TempDir()
	consumer := t.TempDir()
	producerDir, e := filepath.EvalSymlinks(producer)
	must(t, e)
	consumer, e = filepath.EvalSymlinks(consumer)
	must(t, e)
	// Simulate artifact retention/hydration using bytes only. No producer executable
	// bit or directory survives, and no upstream operation/package is invoked.
	for _, d := range r.Files {
		b, e := os.ReadFile(filepath.Join(r.Inbox, str(d, "name")))
		must(t, e)
		put(t, filepath.Join(producerDir, str(d, "name")), b, 0755)
		put(t, filepath.Join(consumer, str(d, "name")), b, 0600)
	}
	must(t, os.RemoveAll(producerDir))
	must(t, os.RemoveAll(r.Inbox))
	r.Inbox = consumer
	cwd := t.TempDir()
	must(t, os.Chmod(cwd, 0555))
	defer os.Chmod(cwd, 0755)
	out, diag, e := invoke(exe, r, cwd, os.Environ())
	must(t, e)
	if len(diag) > 0 {
		t.Fatal(string(diag))
	}
	files := resultFiles(t, out)
	if len(files) != 5 {
		t.Fatal("incomplete output")
	}
	for _, d := range files {
		if str(d, "kind") != "archive" {
			continue
		}
		b, e := os.ReadFile(filepath.Join(r.Outbox, str(d, "name")))
		must(t, e)
		entry := unpack(t, b)["cli"]
		want, e := os.ReadFile(filepath.Join(consumer, str(d, "goos"), str(d, "goarch"), "cli"))
		must(t, e)
		if !bytes.Equal(entry.b, want) || entry.h.Mode != 0755 {
			t.Fatal("handoff bytes/modes")
		}
	}
	r.Files = r.Files[:1]
	out, diag, e = invoke(exe, r, cwd, os.Environ())
	if e == nil || len(out) > 0 || len(diag) == 0 {
		t.Fatalf("failure boundary: %s %s %v", out, diag, e)
	}
}
func TestC09GitFailureAndX05Cancellation(t *testing.T) {
	exe := candidate(t)
	r := fixture(t)
	tools := t.TempDir()
	sentinel := "SECRET_SENTINEL_NEVER_RETAIN"
	helper := filepath.Join(tools, "git")
	put(t, helper, []byte("#!/bin/sh\nprintf '%s' \"$FIXTURE_SECRET\" >&2\nexit 9\n"), 0755)
	env := append(os.Environ(), "PATH="+tools, "FIXTURE_SECRET="+sentinel)
	out, diag, e := invoke(exe, r, t.TempDir(), env)
	if e == nil || len(out) > 0 || bytes.Contains(diag, []byte(sentinel)) || !bytes.Contains(diag, []byte("Git")) {
		t.Fatalf("leaky failure: %s %s %v", out, diag, e)
	}
	// Git's child gets cancelled with the packaging process. No descriptors on stdout.
	ready := filepath.Join(tools, "ready")
	env = append(env, "FIXTURE_READY="+ready)
	put(t, helper, []byte("#!/bin/sh\necho $$ > \"$FIXTURE_READY\"\nwhile :; do :; done\n"), 0755)
	b, _ := json.Marshal(r)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, exe)
	c.Env = env
	c.Stdin = bytes.NewReader(b)
	var stdout bytes.Buffer
	c.Stdout = &stdout
	mustStart := c.Start()
	must(t, mustStart)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			c.Process.Kill()
			c.Wait()
			t.Fatal("Git helper never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	must(t, c.Process.Signal(os.Interrupt))
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case e := <-done:
		if e == nil || stdout.Len() != 0 {
			t.Fatal("cancelled success")
		}
	case <-time.After(3 * time.Second):
		c.Process.Kill()
		t.Fatal("cancel did not stop packaging")
	}
	pidBytes, err := os.ReadFile(ready)
	must(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	must(t, err)
	if err = syscall.Kill(pid, 0); err != syscall.ESRCH {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("Git helper survived cancellation")
	}

	for _, s := range []string{"{}", strings.Repeat(" ", jsonLimit+1)} {
		cmd := exec.Command(exe)
		cmd.Stdin = strings.NewReader(s)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if e := cmd.Run(); e == nil || stdout.Len() != 0 || stderr.Len() == 0 || bytes.Contains(stderr.Bytes(), []byte(sentinel)) {
			t.Fatal("invalid request process boundary")
		}
	}
}
