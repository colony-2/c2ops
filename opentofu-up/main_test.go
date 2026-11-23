package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

const fixtureLock = `provider "registry.opentofu.org/hashicorp/google" {
 version = "7.10.0"
 constraints = "7.10.0"
 hashes = ["zh:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "zh:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "zh:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"]
}
`

func bindGit(t *testing.T) {
	t.Helper()
	var e error
	gitPath, e = exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	toolPath = filepath.Dir(gitPath)
	t.Setenv("TF_DATA_DIR", "")
	t.Setenv("TF_WORKSPACE", "")
}
func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// Fixture commits must not spawn detached maintenance racing TempDir cleanup.
	args = append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)
	r := git(context.Background(), dir, args...)
	if !successful(r) {
		t.Fatalf("git fixture failed: %v", args)
	}
	return strings.TrimSpace(string(r.Data))
}
func fixture(t *testing.T) (Input, *age.X25519Identity) {
	t.Helper()
	bindGit(t)
	dir := t.TempDir()
	gitTest(t, dir, "init", "--quiet")
	gitTest(t, dir, "config", "user.email", "fixture@example.invalid")
	gitTest(t, dir, "config", "user.name", "Fixture")
	write(t, filepath.Join(dir, "main.tf"), "terraform {\n backend \"gcs\" {}\n required_providers {\n google = { source = \"hashicorp/google\", version = \"7.10.0\" }\n }\n}\n")
	write(t, filepath.Join(dir, ".terraform.lock.hcl"), fixtureLock)
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-qm", "fixture")
	key, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]any{"worktree_path": dir, "revision": gitTest(t, dir, "rev-parse", "HEAD"), "root": ".", "mode": "up", "backend": Backend{"fixture-bucket", "fixture/state"}, "google_auth": Auth{Mode: "oauth_access_token", SecretName: "fixture_token"}, "recovery": Recovery{Recipients: []string{key.Recipient().String()}}})
	i, e := parseInput(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	return i, key
}
func write(t *testing.T, p, s string) {
	t.Helper()
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func validSecret(now time.Time) []byte {
	b, _ := json.Marshal(map[string]string{"access_token": "private-token-sentinel", "expires_at": now.Add(time.Hour).Format(time.RFC3339)})
	return b
}
func fakeRunner(t *testing.T, calls *[]command, planCode int, failed string) runner {
	t.Helper()
	return func(ctx context.Context, c command) runResult {
		*calls = append(*calls, c)
		stage := c.Args[0]
		if stage == "version" {
			return runResult{Code: ptr(0), Data: []byte(`{"terraform_version":"1.13.1"}`)}
		}
		if stage == "plan" {
			for _, a := range c.Args {
				if strings.HasPrefix(a, "-out=") {
					write(t, strings.TrimPrefix(a, "-out="), "private-plan-sentinel")
				}
			}
		}
		if stage == failed {
			if stage == "apply" {
				write(t, filepath.Join(c.Dir, "errored.tfstate"), `{"private":"private-state-sentinel"}`)
			}
			return runResult{Code: ptr(1), Err: errors.New("private-diagnostic-sentinel")}
		}
		if stage == "plan" {
			return runResult{Code: ptr(planCode)}
		}
		return runResult{Code: ptr(0)}
	}
}
func deps(t *testing.T, r runner, now time.Time, reads *int) dependencies {
	return dependencies{run: r, now: func() time.Time { return now }, outbox: t.TempDir(), secret: func(ctx context.Context, name string) ([]byte, error) {
		*reads++
		if name != "fixture_token" {
			t.Fatal("wrong secret")
		}
		return validSecret(now), nil
	}}
}
func TestLifecycle(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		code    int
		outcome string
		stages  []string
	}{{"up", 2, "applied", []string{"version", "init", "validate", "plan", "apply"}}, {"plan", 2, "planned", []string{"version", "init", "validate", "plan"}}, {"up", 0, "no_changes", []string{"version", "init", "validate", "plan"}}, {"plan", 0, "no_changes", []string{"version", "init", "validate", "plan"}}} {
		t.Run(tc.mode+tc.outcome, func(t *testing.T) {
			i, _ := fixture(t)
			i.Mode = tc.mode
			calls := []command{}
			reads := 0
			env, e := execute(context.Background(), i, deps(t, fakeRunner(t, &calls, tc.code, ""), time.Now(), &reads))
			if e != nil || !env.Output.OK || env.Output.Outcome != tc.outcome || reads != 1 {
				t.Fatalf("%+v %v reads=%d", env.Output, e, reads)
			}
			stages := []string{}
			for _, c := range calls {
				stages = append(stages, c.Args[0])
				if _, e := os.Stat(c.Dir); !os.IsNotExist(e) {
					t.Fatal("private copy retained")
				}
				if c.Dir == i.Worktree {
					t.Fatal("ran in checkout")
				}
			}
			if !reflect.DeepEqual(stages, tc.stages) {
				t.Fatal(stages)
			}
			if tc.code == 2 && tc.mode == "up" {
				planArgs := calls[3].Args
				applyArgs := calls[4].Args
				var plan string
				for _, a := range planArgs {
					if strings.HasPrefix(a, "-out=") {
						plan = strings.TrimPrefix(a, "-out=")
					}
				}
				if applyArgs[len(applyArgs)-1] != plan {
					t.Fatal("wrong saved plan")
				}
			}
			b, _ := json.Marshal(env)
			for _, private := range []string{"private-token", "private-plan", "private-state", "private-diagnostic"} {
				if bytes.Contains(b, []byte(private)) {
					t.Fatal("private output")
				}
			}
		})
	}
}
func TestStageFailuresAndRecovery(t *testing.T) {
	for _, stage := range []string{"init", "validate", "plan", "apply"} {
		t.Run(stage, func(t *testing.T) {
			i, key := fixture(t)
			calls := []command{}
			reads := 0
			env, e := execute(context.Background(), i, deps(t, fakeRunner(t, &calls, 2, stage), time.Now(), &reads))
			if e != nil || env.Output.OK || env.Output.Stage != stage || env.Output.Category == nil || *env.Output.Category != "command" {
				t.Fatalf("%+v %v", env.Output, e)
			}
			if calls[len(calls)-1].Args[0] != stage {
				t.Fatal("continued after failure")
			}
			if stage != "apply" {
				if len(env.Objects) != 0 {
					t.Fatal("unexpected recovery")
				}
				return
			}
			draft := env.Objects["recovery"]
			p := draft.Files["ciphertext"]
			cipher, e := os.ReadFile(p)
			if e != nil {
				t.Fatal(e)
			}
			if bytes.Contains(cipher, []byte("private-state-sentinel")) {
				t.Fatal("plaintext ciphertext")
			}
			r, e := age.Decrypt(bytes.NewReader(cipher), key)
			if e != nil {
				t.Fatal(e)
			}
			tr := tar.NewReader(r)
			found := false
			for {
				h, e := tr.Next()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
				b, _ := io.ReadAll(tr)
				if h.Name == "state/errored.tfstate" {
					found = bytes.Contains(b, []byte("private-state-sentinel"))
				}
				if bytes.Contains(b, []byte("private-token")) || bytes.Contains(b, []byte("private-plan")) {
					t.Fatal("unwanted recovery material")
				}
			}
			if !found {
				t.Fatal("state not recoverable")
			}
			b, _ := json.Marshal(env)
			if bytes.Contains(b, []byte("private-state-sentinel")) {
				t.Fatal("state leaked")
			}
		})
	}
}
func TestSourceRefusalsBeforeSecrets(t *testing.T) {
	cases := map[string]func(*testing.T, *Input){
		"revision": func(t *testing.T, i *Input) { i.Revision = strings.Repeat("a", 40) },
		"dirty":    func(t *testing.T, i *Input) { write(t, filepath.Join(i.Worktree, "main.tf"), "bad") },
		"assume unchanged dirty": func(t *testing.T, i *Input) {
			gitTest(t, i.Worktree, "update-index", "--assume-unchanged", "main.tf")
			write(t, filepath.Join(i.Worktree, "main.tf"), "bad")
		},
		"untracked config": func(t *testing.T, i *Input) { write(t, filepath.Join(i.Worktree, "extra.tf"), "bad") },
		"ignored state":    func(t *testing.T, i *Input) { write(t, filepath.Join(i.Worktree, "errored.tfstate"), "private-state") },
		"data directory": func(t *testing.T, i *Input) {
			if e := os.Mkdir(filepath.Join(i.Worktree, ".terraform"), 0700); e != nil {
				t.Fatal(e)
			}
		},
		"workspace":     func(t *testing.T, i *Input) { t.Setenv("TF_WORKSPACE", "hostile") },
		"ambient data":  func(t *testing.T, i *Input) { t.Setenv("TF_DATA_DIR", "hostile") },
		"escaping root": func(t *testing.T, i *Input) { i.Root = "../outside" },
		"backend": func(t *testing.T, i *Input) {
			write(t, filepath.Join(i.Worktree, "main.tf"), "terraform { backend \"local\" {} }")
			gitTest(t, i.Worktree, "add", ".")
			gitTest(t, i.Worktree, "commit", "-qm", "bad backend")
			i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
		},
		"missing lock": func(t *testing.T, i *Input) {
			gitTest(t, i.Worktree, "rm", ".terraform.lock.hcl")
			gitTest(t, i.Worktree, "commit", "-qm", "no lock")
			i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
		},
		"symlink": func(t *testing.T, i *Input) {
			if e := os.Symlink("/tmp", filepath.Join(i.Worktree, "outside")); e != nil {
				t.Fatal(e)
			}
			gitTest(t, i.Worktree, "add", ".")
			gitTest(t, i.Worktree, "commit", "-qm", "escape")
			i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
		},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			i, _ := fixture(t)
			f(t, &i)
			calls := []command{}
			reads := 0
			_, e := execute(context.Background(), i, deps(t, fakeRunner(t, &calls, 2, ""), time.Now(), &reads))
			if e == nil || reads != 0 || len(calls) != 0 {
				t.Fatalf("accepted poison %v reads=%d calls=%d", e, reads, len(calls))
			}
		})
	}
}
func TestExpiryAndInvalidCredentials(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		c := credentials{expiry: now.Add(37*time.Minute + offset)}
		if c.validUntil(now, 30*time.Minute) != (offset >= 0) {
			t.Fatal(offset)
		}
	}
	for _, s := range []string{`{}`, `{"access_token":"x","expires_at":"unknown"}`, `{"access_token":"x","expires_at":"2027-01-01T00:00:00+01:00"}`, `{"access_token":"x","access_token":"y","expires_at":"2027-01-01T00:00:00Z"}`, `{"access_token":"x","expires_at":"2027-01-01T00:00:00Z","extra":true}`} {
		if _, e := parseCredentials([]byte(s), Auth{Mode: "oauth_access_token"}, t.TempDir()); e == nil {
			t.Fatal("accepted malformed credential")
		}
	}
	for _, kind := range []string{"denied", "empty", "malformed", "expiry"} {
		t.Run(kind, func(t *testing.T) {
			i, _ := fixture(t)
			calls := []command{}
			reads := 0
			d := deps(t, fakeRunner(t, &calls, 2, ""), now, &reads)
			d.secret = func(context.Context, string) ([]byte, error) {
				reads++
				switch kind {
				case "denied":
					return nil, errInvalid
				case "empty":
					return nil, nil
				case "malformed":
					return []byte(`{}`), nil
				default:
					return []byte(`{"access_token":"x","expires_at":"2026-11-17T00:36:59Z"}`), nil
				}
			}
			env, e := execute(context.Background(), i, d)
			if e != nil || env.Output.OK || env.Output.Stage != "credentials" || reads != 1 || len(calls) != 0 {
				t.Fatalf("%+v %v", env.Output, e)
			}
		})
	}
}
func TestExpiryBeforeApply(t *testing.T) {
	i, _ := fixture(t)
	now := time.Now()
	calls := []command{}
	reads := 0
	d := deps(t, fakeRunner(t, &calls, 2, ""), now, &reads)
	clocks := 0
	d.now = func() time.Time {
		clocks++
		if clocks > 1 {
			return now.Add(time.Hour)
		}
		return now
	}
	env, e := execute(context.Background(), i, d)
	if e != nil || env.Output.OK || env.Output.Stage != "credentials" || calls[len(calls)-1].Args[0] != "plan" {
		t.Fatalf("%+v %v", env.Output, e)
	}
}
func TestPreflightPreventsApply(t *testing.T) {
	i, _ := fixture(t)
	calls := []command{}
	reads := 0
	d := deps(t, fakeRunner(t, &calls, 2, ""), time.Now(), &reads)
	d.outbox = filepath.Join(t.TempDir(), "missing")
	env, e := execute(context.Background(), i, d)
	if e != nil || env.Output.OK || env.Output.Stage != "preflight" || len(calls) != 0 {
		t.Fatalf("%+v %v", env.Output, e)
	}
}
func TestHostileEnvironment(t *testing.T) {
	i, _ := fixture(t)
	for _, s := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_OAUTH_ACCESS_TOKEN", "TF_CLI_ARGS", "TF_LOG", "C2J_SECRET_CHANNEL", "HTTP_PROXY", "STORAGE_EMULATOR_HOST"} {
		t.Setenv(s, "ambient-private-sentinel")
	}
	calls := []command{}
	reads := 0
	env, e := execute(context.Background(), i, deps(t, fakeRunner(t, &calls, 2, ""), time.Now(), &reads))
	if e != nil || !env.Output.OK {
		t.Fatal(e, env.Output)
	}
	for _, c := range calls {
		for _, v := range c.Env {
			if strings.Contains(v, "ambient-private-sentinel") || strings.HasPrefix(v, "C2J_") {
				t.Fatal("ambient authority forwarded")
			}
		}
	}
}
func TestLockMutationStops(t *testing.T) {
	i, _ := fixture(t)
	calls := []command{}
	reads := 0
	base := fakeRunner(t, &calls, 2, "")
	r := func(ctx context.Context, c command) runResult {
		rr := base(ctx, c)
		if c.Args[0] == "init" {
			write(t, filepath.Join(c.Dir, ".terraform.lock.hcl"), "changed")
		}
		return rr
	}
	env, e := execute(context.Background(), i, deps(t, r, time.Now(), &reads))
	if e != nil || env.Output.OK || env.Output.Stage != "init" || len(calls) != 2 {
		t.Fatal(e, env.Output)
	}
}
func TestCancellationStopsLaterStages(t *testing.T) {
	i, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := []command{}
	reads := 0
	base := fakeRunner(t, &calls, 2, "")
	r := func(ctx context.Context, c command) runResult {
		rr := base(ctx, c)
		if c.Args[0] == "plan" {
			cancel()
		}
		return rr
	}
	env, e := execute(ctx, i, deps(t, r, time.Now(), &reads))
	if e != nil || env.Output.OK || env.Output.Outcome != "cancelled" || calls[len(calls)-1].Args[0] != "plan" {
		t.Fatal(e, env.Output)
	}
}
func TestStrictInput(t *testing.T) {
	i, _ := fixture(t)
	b, _ := json.Marshal(i)
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	for name, f := range map[string]func(map[string]any){"extra": func(m map[string]any) { m["command"] = "bad" }, "null": func(m map[string]any) { m["variables"] = nil }, "invalid name": func(m map[string]any) {
		m["google_auth"] = map[string]any{"mode": "oauth_access_token", "secret_name": "../other"}
	}, "token impersonation": func(m map[string]any) {
		m["google_auth"] = map[string]any{"mode": "oauth_access_token", "secret_name": "x", "impersonate_service_account": ""}
	}, "timeout": func(m map[string]any) { m["timeout"] = "61m" }, "lock": func(m map[string]any) { m["lock_timeout"] = "31m" }, "root": func(m map[string]any) { m["root"] = "a/../b" }} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			f(m)
			raw, _ := json.Marshal(m)
			if _, e := parseInput(bytes.NewReader(raw)); e == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	for _, raw := range [][]byte{append(append([]byte{}, b...), []byte(`{}`)...), []byte(`{"root":".","root":"other"}`), bytes.Repeat([]byte(" "), maxInput+1)} {
		if _, e := parseInput(bytes.NewReader(raw)); e == nil {
			t.Fatal("accepted invalid protocol")
		}
	}
}
func TestProcessCancellationAndBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	r := runProcess(ctx, command{Path: "/bin/sh", Args: []string{"-c", "sleep 30 & wait"}, Env: []string{"PATH=/usr/bin:/bin"}})
	if !errors.Is(r.Err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation failed %v", r.Err)
	}
	r = runProcess(context.Background(), command{Path: "/bin/sh", Args: []string{"-c", "printf 123456789"}, Limit: 4})
	if r.Err == nil || len(r.Data) != 0 {
		t.Fatal("oversized helper accepted")
	}
}

func TestHCLBackendAndCredentialRefusals(t *testing.T) {
	for name, config := range map[string]string{
		"bucket mismatch": `terraform { backend "gcs" { bucket = "other-bucket" } }`,
		"expression":      `terraform { backend "gcs" { bucket = var.bucket } }`,
		"backend auth":    `terraform { backend "gcs" { access_token = "private" } }`,
		"duplicate":       "terraform {\n backend \"gcs\" {}\n backend \"gcs\" {}\n}\n",
		"alias auth":      "terraform {\n backend \"gcs\" {}\n}\nprovider \"google\" {\n alias=\"other\"\n credentials=\"private\"\n}\n",
		"custom endpoint": "terraform {\n backend \"gcs\" {}\n}\nprovider \"google\" { storage_custom_endpoint=\"https://invalid\" }\n",
		"json backend":    `{"terraform":{"backend":{"gcs":{"prefix":"wrong"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			i, _ := fixture(t)
			p := filepath.Join(i.Worktree, "main.tf")
			if strings.HasPrefix(config, "{") {
				_ = os.Remove(p)
				p = filepath.Join(i.Worktree, "main.tf.json")
			}
			write(t, p, config)
			gitTest(t, i.Worktree, "add", ".")
			gitTest(t, i.Worktree, "commit", "-qm", "refusal")
			i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
			reads := 0
			calls := []command{}
			_, e := execute(context.Background(), i, deps(t, fakeRunner(t, &calls, 2, ""), time.Now(), &reads))
			if e == nil || reads != 0 || len(calls) != 0 {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

const dynamicExternalCredentials = `provider "google" {
 dynamic "external_credentials" {
  for_each = [1]
  content {
   audience = "fixture"
   identity_token = "fixture-synthetic-token"
   service_account_email = "other@fixture.iam.gserviceaccount.com"
  }
 }
}
`

const dynamicExternalCredentialsJSON = `{"provider":{"google":{"dynamic":{"external_credentials":{"for_each":[1],"content":{"audience":"fixture","identity_token":"fixture-synthetic-token","service_account_email":"other@fixture.iam.gserviceaccount.com"}}}}}}`

func TestDynamicAuthenticationBeforeSecrets(t *testing.T) {
	configs := map[string]string{
		"external credentials": dynamicExternalCredentials,
		"empty expansion":      strings.Replace(dynamicExternalCredentials, "[1]", "[]", 1),
		"unknown expansion":    strings.Replace(dynamicExternalCredentials, "[1]", "var.credentials", 1),
		"provider alias":       strings.Replace(dynamicExternalCredentials, "dynamic", "alias = \"other\"\n dynamic", 1),
		"nested expansion":     "resource \"fixture\" \"nested\" {\n dynamic \"settings\" {\n for_each = [1]\n content {\n" + dynamicExternalCredentials + "\n }\n }\n}\n",
		"json":                 dynamicExternalCredentialsJSON,
	}
	// Every forbidden block name, including endpoint suffixes, must have the
	// same refusal boundary whether written literally or generated by dynamic.
	for _, name := range []string{
		"credentials", "access_token", "oauth_access_token", "external_credentials",
		"impersonate_service_account", "impersonate_service_account_delegates",
		"impersonate_service_account_delegation_chain", "endpoints", "endpoint", "universe_domain",
	} {
		configs["target/"+name] = fmt.Sprintf("provider \"google\" {\n dynamic %q {\n for_each = []\n content {}\n }\n}\n", name)
	}
	for _, name := range []string{"storage_custom_endpoint", "custom_endpoint"} {
		configs["target/"+name] = fmt.Sprintf("provider \"google\" {\n dynamic %q {\n for_each = []\n content {}\n }\n}\n", name)
	}
	configs["missing target"] = "provider \"google\" {\n dynamic {\n for_each = []\n content {}\n }\n}\n"
	configs["multiple targets"] = "provider \"google\" {\n dynamic \"safe\" \"external_credentials\" {\n for_each = []\n content {}\n }\n}\n"
	for name, config := range configs {
		t.Run(name, func(t *testing.T) {
			i, _ := fixture(t)
			file := "auth.tf"
			if name == "json" {
				file += ".json"
			}
			write(t, filepath.Join(i.Worktree, file), config)
			gitTest(t, i.Worktree, "add", ".")
			gitTest(t, i.Worktree, "commit", "-qm", "dynamic auth refusal")
			i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
			reads := 0
			calls := []command{}
			_, e := execute(context.Background(), i, deps(t, fakeRunner(t, &calls, 2, ""), time.Now(), &reads))
			if e == nil || reads != 0 || len(calls) != 0 {
				t.Fatalf("dynamic authentication reached secret/tool boundary: error=%v reads=%d calls=%d", e, reads, len(calls))
			}
		})
	}
	t.Run("local module", func(t *testing.T) {
		i, _ := fixture(t)
		if e := os.Mkdir(filepath.Join(i.Worktree, "child"), 0700); e != nil {
			t.Fatal(e)
		}
		write(t, filepath.Join(i.Worktree, "module.tf"), "module \"child\" { source = \"./child\" }\n")
		write(t, filepath.Join(i.Worktree, "child/main.tf"), dynamicExternalCredentials)
		gitTest(t, i.Worktree, "add", ".")
		gitTest(t, i.Worktree, "commit", "-qm", "dynamic auth in local module")
		i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
		reads := 0
		calls := []command{}
		_, e := execute(context.Background(), i, deps(t, fakeRunner(t, &calls, 2, ""), time.Now(), &reads))
		if e == nil || reads != 0 || len(calls) != 0 {
			t.Fatalf("local module auth reached secret/tool boundary: error=%v reads=%d calls=%d", e, reads, len(calls))
		}
	})
}

// execute creates its snapshot under a physical scratch path. Match that
// boundary when calling buildSnapshot directly, including with an aliased TMPDIR.
func snapshotTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNonAuthDynamicAndOrdinaryLabels(t *testing.T) {
	for name, config := range map[string]string{
		"dynamic": "resource \"fixture\" \"safe\" {\n dynamic \"settings\" {\n for_each = [1]\n content { size = 1 }\n }\n}\n",
		"labels":  "resource \"fixture\" \"external_credentials\" {}\noutput \"access_token\" { value = \"public\" }\n",
	} {
		t.Run(name, func(t *testing.T) {
			i, _ := fixture(t)
			write(t, filepath.Join(i.Worktree, "safe.tf"), config)
			gitTest(t, i.Worktree, "add", ".")
			gitTest(t, i.Worktree, "commit", "-qm", "permitted syntax")
			i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
			if _, e := buildSnapshot(context.Background(), i, filepath.Join(snapshotTempDir(t), "snapshot")); e != nil {
				t.Fatal("permitted source syntax refused", e)
			}
		})
	}
}

func TestUnsupportedConfigurationBeforeSecrets(t *testing.T) {
	for _, name := range []string{"main.tofu", "main.tofu.json", "override.tofu", "auth_override.tofu.json", "terraform.tofuvars", "values.auto.tofuvars.json"} {
		for _, tracked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tracked=%t", name, tracked), func(t *testing.T) {
				i, _ := fixture(t)
				write(t, filepath.Join(i.Worktree, name), `terraform { backend "local" {} }`)
				if tracked {
					gitTest(t, i.Worktree, "add", ".")
					gitTest(t, i.Worktree, "commit", "-qm", "unchecked dialect")
					i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
				}
				reads := 0
				calls := []command{}
				_, e := execute(context.Background(), i, deps(t, fakeRunner(t, &calls, 2, ""), time.Now(), &reads))
				if e == nil || reads != 0 || len(calls) != 0 {
					t.Fatalf("unsupported configuration reached credentials/tool: %v %d %d", e, reads, len(calls))
				}
			})
		}
	}
}

func TestRemoteModuleSources(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct {
		source, version string
		accept          bool
	}{
		{"git::https://example.com/team/module.git?ref=" + commit, "", true},
		{"git::https://example.com/team/module.git//modules/vpc?ref=" + commit, "", true},
		{"hashicorp/consul/google", "1.2.3", true},
		{"registry.example.com/team/vpc/google", "1.2.3", true},
		{"http://example.com/module.zip?ref=" + commit, "", false},
		{"https://example.com/module.zip?ref=" + commit, "", false},
		{"https://example.com/module.zip", "1.2.3", false},
		{"s3::https://example.com/module.zip?ref=" + commit, "", false},
		{"github.com/team/module?ref=" + commit, "", false},
		{"git::https://example.com/team/module.git?ref=main", "", false},
		{"git::https://example.com/team/module.git?ref=" + commit + "&depth=1", "", false},
		{"git::https://example.com/team/module.git?ref=" + commit + "&ref=main", "", false},
		{"git::https://example.com/team/module.git//../escape?ref=" + commit, "", false},
		{"git::https://user@example.com/team/module.git?ref=" + commit, "", false},
		{"git::https://example.com/team/module.git?ref=" + commit, "1.2.3", false},
		{"hashicorp/consul/google", ">= 1.2.3", false},
		{"hashicorp/consul/google", "", false},
	} {
		t.Run(tc.source+tc.version, func(t *testing.T) {
			i, _ := fixture(t)
			p := filepath.Join(i.Worktree, "main.tf")
			b, e := os.ReadFile(p)
			if e != nil {
				t.Fatal(e)
			}
			cfg := fmt.Sprintf("\nmodule \"remote\" {\n source = %q\n", tc.source)
			if tc.version != "" {
				cfg += fmt.Sprintf(" version = %q\n", tc.version)
			}
			write(t, p, string(b)+cfg+"}\n")
			gitTest(t, i.Worktree, "add", ".")
			gitTest(t, i.Worktree, "commit", "-qm", "remote source")
			i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
			if tc.accept {
				_, e = buildSnapshot(context.Background(), i, snapshotTempDir(t))
				if e != nil {
					t.Fatal("supported immutable source refused", e)
				}
			} else {
				reads := 0
				calls := []command{}
				_, e = execute(context.Background(), i, deps(t, fakeRunner(t, &calls, 2, ""), time.Now(), &reads))
				if e == nil || reads != 0 || len(calls) != 0 {
					t.Fatal("mutable module reached credentials/tool")
				}
			}
		})
	}
}
func TestTotalDeadlineAndStageCeilings(t *testing.T) {
	i, _ := fixture(t)
	i.total = 50 * time.Millisecond
	calls := []command{}
	reads := 0
	d := deps(t, fakeRunner(t, &calls, 2, ""), time.Now(), &reads)
	d.secret = func(ctx context.Context, _ string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	env, e := execute(context.Background(), i, d)
	if e == nil && (env.Output.Category == nil || *env.Output.Category != "deadline") {
		t.Fatal(env.Output)
	}
	if len(calls) != 0 {
		t.Fatal("commands after deadline")
	}
	i, _ = fixture(t)
	i.total = time.Hour
	calls = nil
	reads = 0
	base := fakeRunner(t, &calls, 0, "")
	d = deps(t, func(ctx context.Context, c command) runResult {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("missing deadline")
		}
		ceiling := time.Hour
		switch c.Args[0] {
		case "init":
			ceiling = 5 * time.Minute
		case "validate":
			ceiling = 2 * time.Minute
		case "plan":
			ceiling = 10 * time.Minute
		}
		if time.Until(deadline) > ceiling {
			t.Fatal("stage deadline too long")
		}
		return base(ctx, c)
	}, time.Now(), &reads)
	d.secret = func(context.Context, string) ([]byte, error) { return validSecret(time.Now().Add(time.Hour)), nil }
	env, e = execute(context.Background(), i, d)
	if e != nil || !env.Output.OK {
		t.Fatal(e, env.Output)
	}
}
func TestForcedGroupTermination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	r := runProcess(ctx, command{Path: "/bin/sh", Args: []string{"-c", "trap '' INT; sleep 30 & wait"}, Env: []string{"PATH=/usr/bin:/bin"}, InterruptGrace: 100 * time.Millisecond})
	if !errors.Is(r.Err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatal("force termination failed", r.Err)
	}
}
func TestMissingEmergencyStateAndOutboxSymlink(t *testing.T) {
	i, _ := fixture(t)
	calls := []command{}
	reads := 0
	base := fakeRunner(t, &calls, 2, "")
	r := func(ctx context.Context, c command) runResult {
		rr := base(ctx, c)
		if c.Args[0] == "apply" {
			return runResult{Code: ptr(1)}
		}
		return rr
	}
	env, e := execute(context.Background(), i, deps(t, r, time.Now(), &reads))
	if e != nil || env.Output.OK || len(env.Objects) != 0 || env.Output.Stage != "apply" {
		t.Fatal(e, env.Output)
	}
	link := filepath.Join(t.TempDir(), "out")
	if e = os.Symlink(t.TempDir(), link); e != nil {
		t.Fatal(e)
	}
	d := deps(t, r, time.Now(), &reads)
	d.outbox = link
	env, e = execute(context.Background(), i, d)
	if e != nil || env.Output.Stage != "preflight" || env.Output.OK {
		t.Fatal(e, env.Output)
	}
}

func TestSelectedRootAndRelativeModule(t *testing.T) {
	i, _ := fixture(t)
	if e := os.Mkdir(filepath.Join(i.Worktree, "module"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(i.Worktree, "unrelated"), 0700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(i.Worktree, "module/main.tf"), `variable "public" { type=string }`)
	main, _ := os.ReadFile(filepath.Join(i.Worktree, "main.tf"))
	write(t, filepath.Join(i.Worktree, "main.tf"), string(main)+"\nmodule \"local\" {\n source=\"./module\"\n public=\"value\"\n}\n")
	write(t, filepath.Join(i.Worktree, "unrelated/main.tf"), "terraform {\n backend \"local\" {}\n}\n")
	gitTest(t, i.Worktree, "add", ".")
	gitTest(t, i.Worktree, "commit", "-qm", "relative module and unrelated backend")
	i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
	calls := []command{}
	reads := 0
	env, e := execute(context.Background(), i, deps(t, fakeRunner(t, &calls, 2, ""), time.Now(), &reads))
	if e != nil || !env.Output.OK {
		t.Fatal("selected root rejected", e, env.Output)
	}
}

func TestExactCaseAndNullFields(t *testing.T) {
	i, _ := fixture(t)
	b, _ := json.Marshal(i)
	for _, raw := range []string{strings.Replace(string(b), `"worktree_path"`, `"Worktree_path"`, 1), strings.Replace(string(b), `"google_auth"`, `"GOOGLE_AUTH"`, 1), strings.Replace(string(b), `"prefix"`, `"Prefix"`, 1), strings.Replace(string(b), `"age_recipients":[`, `"age_recipients":[null,`, 1)} {
		if _, e := parseInput(strings.NewReader(raw)); e == nil {
			t.Fatal("accepted non-schema field")
		}
	}
	if _, e := parseCredentials([]byte(`{"Access_token":"x","expires_at":"2099-01-01T00:00:00Z"}`), Auth{Mode: "oauth_access_token"}, t.TempDir()); e == nil {
		t.Fatal("accepted non-schema secret")
	}
}

func TestJSONConfiguration(t *testing.T) {
	i, _ := fixture(t)
	if e := os.Remove(filepath.Join(i.Worktree, "main.tf")); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(i.Worktree, "main.tf.json"), `{"terraform":{"backend":{"gcs":{}},"required_providers":{"google":{"source":"hashicorp/google","version":"7.10.0"}}}}`)
	gitTest(t, i.Worktree, "add", ".")
	gitTest(t, i.Worktree, "commit", "-qm", "JSON config")
	i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
	calls := []command{}
	reads := 0
	env, e := execute(context.Background(), i, deps(t, fakeRunner(t, &calls, 0, ""), time.Now(), &reads))
	if e != nil || !env.Output.OK {
		t.Fatal(e, env.Output)
	}
}
func TestServiceAccountRestrictions(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	der, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	auth := Auth{Mode: "service_account_json", Expected: "fixture@fixture.iam.gserviceaccount.com"}
	base := map[string]string{"type": "service_account", "project_id": "fixture", "private_key_id": "id", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "client_email": auth.Expected, "client_id": "123", "token_uri": "https://oauth2.googleapis.com/token"}
	for field, value := range map[string]string{"type": "authorized_user", "client_email": "other@fixture.iam.gserviceaccount.com", "token_uri": "https://fixture.invalid/token", "private_key": "invalid", "universe_domain": "fixture.invalid", "executable": "command"} {
		t.Run(field, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			m[field] = value
			b, _ := json.Marshal(m)
			if _, e := parseCredentials(b, auth, t.TempDir()); e == nil {
				t.Fatal("unsupported credential accepted")
			}
		})
	}
}

func TestLockHashSyntax(t *testing.T) {
	if validateLock([]byte(strings.Replace(fixtureLock, strings.Repeat("a", 64), strings.Repeat("z", 64), 1)), "lock.hcl") == nil {
		t.Fatal("malformed provider hash accepted")
	}
	b, e := os.ReadFile("testdata/.terraform.lock.hcl")
	if e != nil || validateLock(b, "lock.hcl") != nil {
		t.Fatal("qualified lock refused", e)
	}
}
