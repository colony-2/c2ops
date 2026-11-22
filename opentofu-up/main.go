package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type Output struct {
	OK       bool    `json:"ok"`
	Outcome  string  `json:"outcome"`
	Stage    string  `json:"stage"`
	Category *string `json:"error_category"`
	ExitCode *int    `json:"exit_code"`
	Duration int64   `json:"duration_ms"`
	Changes  *bool   `json:"changes_present"`
	Revision string  `json:"revision"`
	Root     string  `json:"root"`
	Backend  Backend `json:"backend"`
	Recovery any     `json:"recovery,omitempty"`
}
type Envelope struct {
	Output  Output                 `json:"output"`
	Objects map[string]objectDraft `json:"objects,omitempty"`
}
type dependencies struct {
	run    runner
	secret func(context.Context, string) ([]byte, error)
	now    func() time.Time
	outbox string
}

func ptr[T any](v T) *T { return &v }
func failure(o *Output, stage, category string, r runResult) {
	o.OK = false
	o.Outcome = "failed"
	o.Stage = stage
	o.Category = &category
	o.ExitCode = r.Code
	if category == "cancelled" {
		o.Outcome = "cancelled"
	}
}
func execute(parent context.Context, i Input, d dependencies) (env Envelope, err error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, i.total)
	defer cancel()
	shutdown := &shutdownBudget{}
	ctx = context.WithValue(ctx, shutdownKey{}, shutdown)
	dir, e := os.MkdirTemp("/tmp", "c2ops-opentofu-")
	if e != nil {
		return env, errInvalid
	}
	physical, e := filepath.EvalSymlinks(dir)
	if e != nil {
		_ = os.RemoveAll(dir)
		return env, errInvalid
	}
	dir = physical
	// Cleanup always runs before the safe envelope is returned.
	defer func() {
		if e := boundedCleanup(dir, shutdown.begin()); e != nil && err == nil {
			failure(&env.Output, "cleanup", "cleanup", runResult{})
		}
		env.Output.Duration = time.Since(start).Milliseconds()
	}()
	s, e := buildSnapshot(ctx, i, filepath.Join(dir, "source"))
	if e != nil {
		return env, errInvalid
	}
	env.Output = Output{Outcome: "failed", Stage: "credentials", Revision: i.Revision, Root: i.Root, Backend: i.Backend}
	fail := func(stage, category string, r runResult) {
		if errors.Is(ctx.Err(), context.Canceled) {
			category = "cancelled"
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(r.Err, context.DeadlineExceeded) {
			category = "deadline"
		}
		failure(&env.Output, stage, category, r)
	}
	if ctx.Err() != nil {
		fail("credentials", "deadline", runResult{})
		return env, nil
	}
	data, e := d.secret(ctx, i.Auth.SecretName)
	if e != nil {
		fail("credentials", "credentials", runResult{})
		return env, nil
	}
	creds, e := parseCredentials(data, i.Auth, dir)
	clear(data)
	if e != nil || !creds.validUntil(d.now(), i.total) {
		fail("credentials", "credentials", runResult{})
		return env, nil
	}
	home := filepath.Join(dir, "home")
	dataDir := filepath.Join(dir, "data")
	if makePrivate(home) != nil || makePrivate(dataDir) != nil {
		fail("preflight", "preflight", runResult{})
		return env, nil
	}
	vars := filepath.Join(dir, "public.tfvars.json")
	v := i.Variables
	if v == nil {
		v = map[string]any{}
	}
	vb, e := json.Marshal(v)
	if e != nil || os.WriteFile(vars, vb, 0600) != nil {
		fail("preflight", "preflight", runResult{})
		return env, nil
	}
	plan := filepath.Join(dir, "current.tfplan")
	toolEnv := append(privateEnv(home, dataDir), creds.env...)
	var out *os.File
	if i.Mode == "up" {
		out, e = preflightRecovery(d.outbox, i.recipients)
		if e != nil {
			fail("preflight", "preflight", runResult{})
			return env, nil
		}
		defer out.Close()
	}
	version := d.run(ctx, command{Path: tofuPath, Dir: s.Root, Args: []string{"version", "-json"}, Env: toolEnv})
	var ver struct {
		Version string `json:"terraform_version"`
	}
	if !successful(version) || json.Unmarshal(version.Data, &ver) != nil || ver.Version != "1.13.1" {
		fail("preflight", "preflight", version)
		return env, nil
	}
	stage := func(name string, ceiling time.Duration, args ...string) bool {
		if ctx.Err() != nil {
			fail(name, "cancelled", runResult{Err: ctx.Err()})
			return false
		}
		if !s.locksUnchanged() {
			fail(name, "preflight", runResult{})
			return false
		}
		sc, stop := context.WithTimeout(ctx, ceiling)
		r := d.run(sc, command{Path: tofuPath, Dir: s.Root, Args: args, Env: toolEnv})
		deadlineErr := sc.Err()
		stop()
		env.Output.ExitCode = r.Code
		if !s.locksUnchanged() {
			fail(name, "preflight", r)
			return false
		}
		if ctx.Err() != nil || deadlineErr != nil {
			if deadlineErr != nil {
				r.Err = deadlineErr
			}
			fail(name, "deadline", r)
			return false
		}
		if name == "plan" && r.Err == nil && r.Code != nil && (*r.Code == 0 || *r.Code == 2) {
			env.Output.Changes = ptr(*r.Code == 2)
			return true
		}
		if !successful(r) {
			fail(name, "command", r)
			return false
		}
		return true
	}
	lockArg := func(ceiling time.Duration) string {
		remaining := time.Until(start.Add(i.total))
		n := min(i.lock, ceiling, remaining)
		if n < time.Millisecond {
			n = time.Millisecond
		}
		return "-lock-timeout=" + n.String()
	}
	if !stage("init", 5*time.Minute, "init", "-input=false", "-reconfigure", "-lockfile=readonly", "-backend-config=bucket="+i.Backend.Bucket, "-backend-config=prefix="+i.Backend.Prefix) {
		return env, nil
	}
	// Downloaded modules must obey the same credential and immutable-source rules.
	modules := filepath.Join(dataDir, "modules")
	if _, e = os.Stat(modules); e == nil {
		if e = inspectTree(ctx, modules, s.Root, i.Backend, false); e != nil {
			fail("validate", "preflight", runResult{})
			return env, nil
		}
	}
	if !stage("validate", 2*time.Minute, "validate", "-no-color") {
		return env, nil
	}
	if !stage("plan", 10*time.Minute, "plan", "-input=false", "-lock=true", lockArg(10*time.Minute), "-detailed-exitcode", "-out="+plan, "-var-file="+vars, "-no-color") {
		return env, nil
	}
	if !*env.Output.Changes {
		env.Output.OK = true
		env.Output.Outcome = "no_changes"
	} else if i.Mode == "plan" {
		env.Output.OK = true
		env.Output.Outcome = "planned"
	} else {
		if !creds.validUntil(d.now(), max(time.Until(start.Add(i.total)), 0)) {
			fail("credentials", "credentials", runResult{})
			return env, nil
		}
		if stage("apply", i.total, "apply", "-input=false", "-lock=true", lockArg(i.total), "-no-color", plan) {
			env.Output.OK = true
			env.Output.Outcome = "applied"
		} else {
			// A handled failure can seal ciphertext. It never changes deployment success.
			recoveryCtx, stop := context.WithDeadline(context.Background(), shutdown.begin())
			draft, e := emergency(recoveryCtx, s, out, i.recipients, i, "apply")
			stop()
			if e != nil {
				failure(&env.Output, "recovery", "recovery", runResult{})
			} else if draft != nil {
				env.Output.Recovery = map[string]string{"$object": "recovery"}
				env.Objects = map[string]objectDraft{"recovery": *draft}
			}
			return env, nil
		}
	}
	env.Output.Stage = "complete"
	env.Output.Category = nil
	return env, nil
}
func main() {
	syscall.Umask(0077)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	i, e := parseInput(os.Stdin)
	if e != nil {
		invocationFailure()
		return
	}
	helper, e := exec.LookPath("c2j")
	if e != nil || !filepath.IsAbs(helper) || gitPath == "" || tofuPath == "" || trustPath == "" {
		invocationFailure()
		return
	}
	helperEnv := os.Environ()
	env, e := execute(ctx, i, dependencies{run: runProcess, secret: func(ctx context.Context, name string) ([]byte, error) {
		return resolveSecret(ctx, helper, helperEnv, name)
	}, now: time.Now, outbox: os.Getenv("C2J_OBJECT_OUTBOX")})
	if e != nil {
		invocationFailure()
		return
	}
	if json.NewEncoder(os.Stdout).Encode(env) != nil {
		os.Exit(1)
	}
}
func invocationFailure() {
	_, _ = io.WriteString(os.Stderr, "opentofu-up: invalid invocation or source preflight\n")
	os.Exit(1)
}
