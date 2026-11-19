package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid"}, args...)
	c := exec.Command("git", argv...)
	c.Env = cleanEnv()
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func write(t *testing.T, root, name, value string) {
	t.Helper()
	p := filepath.Join(root, name)
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(value), 0644); e != nil {
		t.Fatal(e)
	}
}

const fixtureMain = `package main
import("fmt";"runtime")
var state="committed"
var version="unset"
func main(){fmt.Print(state+"|"+version+"|"+runtime.Version())}
`

func fixture(t *testing.T, files map[string]string) Request {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	in := filepath.Join(base, "in")
	out := filepath.Join(base, "out")
	for _, p := range []string{root, in, out} {
		if e := os.Mkdir(p, 0755); e != nil {
			t.Fatal(e)
		}
	}
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "remote", "add", "origin", "https://github.com/example/cli.git")
	write(t, root, "go.mod", "module example.com/cli\n\ngo 1.26.1\n")
	write(t, root, "main.go", fixtureMain)
	for p, b := range files {
		write(t, root, p, b)
	}
	gitTest(t, root, "add", "-f", ".")
	gitTest(t, root, "commit", "-qm", "fixture")
	r := Request{Intent: Intent{1, "fixture-1", "github.com/example/cli", gitTest(t, root, "rev-parse", "HEAD"), "main", "1.2.3", "v1.2.3", "2026-11-18T12:00:00Z", "cli"}, Worktree: root, Inbox: in, Outbox: out, Entrypoint: ".", Targets: []Target{{"linux", "arm64"}}}
	if e := r.validate(); e != nil {
		t.Fatal(e)
	}
	return r
}
func projectionFor(t *testing.T, r Request) *projection {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projection")
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	p, e := prepareSource(context.Background(), r, root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { removeScratch(root) })
	return p
}
func selectionFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, v := range files {
		write(t, root, p, v)
	}
	return root
}
func TestB01_B06Selection(t *testing.T) {
	cases := []struct {
		name, mod, work, override, want string
		bad                             bool
	}{
		{"B01 go", "go 1.26\n", "", "", "1.26.8", false},
		{"B01 default", "go 1.26.1\ntoolchain default\n", "", "", "1.26.1", false},
		{"B02 toolchain", "go 1.26\ntoolchain go1.26.1\n", "", "", "1.26.1", false},
		{"B02 work", "go 1.26\n", "go 1.26\ntoolchain go1.26.1\nuse .\n", "", "1.26.1", false},
		{"B03 override", "go 1.26\ntoolchain go1.26.8\n", "", "1.26.1", "1.26.1", false},
		{"B04 minor", "go 1.26\n", "", "1.26", "1.26.8", false},
		{"B05 exact", "go 1.26\n", "", "1.26.2", "", true},
		{"B06 module minimum", "go 1.26.8\n", "", "1.26.1", "", true},
		{"B06 work minimum", "go 1.26.1\n", "go 1.26.8\nuse .\n", "1.26.1", "", true},
		{"B06 malformed", "go bananas\n", "", "1.26.1", "", true},
		{"B06 missing minimum", "", "", "1.26.1", "", true},
		{"B06 malformed override", "go 1.26\n", "", "latest", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"go.mod": "module example.com/test\n" + c.mod}
			r := Request{ModuleDir: ".", GoVersion: c.override}
			if c.work != "" {
				files["go.work"] = c.work
				r.VersionFile = "go.work"
			}
			s, e := selectVersion(selectionFiles(t, files), r)
			if c.bad {
				if e == nil {
					t.Fatal("accepted invalid selection")
				}
				return
			}
			if e != nil || s.resolved != c.want {
				t.Fatalf("got %+v %v", s, e)
			}
			if sdkPins[s.resolved].revision == "" {
				t.Fatal("missing pin")
			}
		})
	}
	t.Run("B06 missing selection file", func(t *testing.T) {
		_, e := selectVersion(t.TempDir(), Request{ModuleDir: "."})
		if e == nil {
			t.Fatal("accepted missing file")
		}
	})
	t.Run("B03 other workspace member minimum", func(t *testing.T) {
		root := selectionFiles(t, map[string]string{"go.mod": "module example.com/root\ngo 1.26\n", "other/go.mod": "module example.com/other\ngo 1.26.8\n", "go.work": "go 1.26\nuse (\n .\n ./other\n)\n"})
		_, e := selectVersion(root, Request{ModuleDir: ".", VersionFile: "go.work", GoVersion: "1.26.1"})
		if e == nil {
			t.Fatal("ignored active member minimum")
		}
	})
}
func TestB20_B25Projection(t *testing.T) {
	r := fixture(t, map[string]string{".gitignore": "hidden.txt\n", ".gitattributes": "data.txt export-ignore\n", "hidden.txt": "force-added", "data.txt": "export ignored", "odd \t\nname": "weird"})
	write(t, r.Worktree, "untracked.go", "poison")
	p := projectionFor(t, r)
	if _, e := os.Stat(filepath.Join(p.root, "untracked.go")); !os.IsNotExist(e) {
		t.Fatal("untracked file projected")
	}
	for _, name := range []string{"hidden.txt", "data.txt", "odd \t\nname"} {
		if _, e := os.Stat(filepath.Join(p.root, name)); e != nil {
			t.Fatal(e)
		}
	}
	for _, mutation := range []string{"bytes", "mode", "readmode", "extra", "missing"} {
		t.Run("B20 "+mutation, func(t *testing.T) {
			p := projectionFor(t, r)
			file := filepath.Join(p.root, "main.go")
			switch mutation {
			case "bytes":
				_ = os.Chmod(file, 0644)
				_ = os.WriteFile(file, []byte("changed"), 0444)
			case "mode":
				_ = os.Chmod(file, 0555)
			case "readmode":
				_ = os.Chmod(file, 0400)
			case "extra":
				_ = os.Chmod(p.root, 0755)
				write(t, p.root, "extra", "extra")
			case "missing":
				_ = os.Chmod(p.root, 0755)
				_ = os.Remove(file)
			}
			if p.verify() == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
	for _, mutation := range []string{"head", "tracked", "staged", "assume-unchanged", "skip-worktree", "mode"} {
		t.Run("B20 "+mutation, func(t *testing.T) {
			r := fixture(t, nil)
			switch mutation {
			case "head":
				r.Intent.SourceCommit = strings.Repeat("a", 40)
			case "staged":
				write(t, r.Worktree, "main.go", fixtureMain+"\n")
				gitTest(t, r.Worktree, "add", "main.go")
			case "mode":
				_ = os.Chmod(filepath.Join(r.Worktree, "main.go"), 0755)
			default:
				if mutation != "tracked" {
					gitTest(t, r.Worktree, "update-index", "--"+mutation, "main.go")
				}
				write(t, r.Worktree, "main.go", "bad")
			}
			root := t.TempDir()
			if _, e := prepareSource(context.Background(), r, root); e == nil {
				t.Fatal("accepted changed source")
			}
		})
	}
}
func TestB24EscapingAndContainedPaths(t *testing.T) {
	for _, kind := range []string{"replace", "work", "symlink", "absolute"} {
		t.Run(kind, func(t *testing.T) {
			r := fixture(t, nil)
			switch kind {
			case "symlink":
				if e := os.Symlink("../../outside", filepath.Join(r.Worktree, "escape")); e != nil {
					t.Fatal(e)
				}
			case "work":
				write(t, r.Worktree, "go.work", "go 1.26\nuse (\n .\n ../outside\n)\n")
				r.VersionFile = "go.work"
			case "replace":
				write(t, r.Worktree, "go.mod", "module example.com/cli\ngo 1.26\nreplace example.com/other => ../outside\n")
			case "absolute":
				write(t, r.Worktree, "go.mod", "module example.com/cli\ngo 1.26\nreplace example.com/other => /tmp\n")
			}
			gitTest(t, r.Worktree, "add", ".")
			gitTest(t, r.Worktree, "commit", "-qm", "unsafe")
			r.Intent.SourceCommit = gitTest(t, r.Worktree, "rev-parse", "HEAD")
			root := t.TempDir()
			p, e := prepareSource(context.Background(), r, root)
			defer removeScratch(root)
			if e == nil {
				_, e = selectVersion(p.root, r)
			}
			if e == nil {
				t.Fatal("accepted escaping source")
			}
		})
	}
	root := selectionFiles(t, map[string]string{"go.mod": "module example.com/root\ngo 1.26\nreplace example.com/local => ./local\n", "local/go.mod": "module example.com/local\ngo 1.26\n", "go.work": "go 1.26\nuse (\n .\n ./local\n)\n"})
	if _, e := selectVersion(root, Request{ModuleDir: ".", VersionFile: "go.work"}); e != nil {
		t.Fatal(e)
	}
}
func TestC01_C02_C04_C07_C09Validation(t *testing.T) {
	r := fixture(t, nil)
	mutations := []func(*Request){func(r *Request) { r.Intent.SchemaVersion = 2 }, func(r *Request) { r.Intent.ReleaseID = "" }, func(r *Request) { r.Intent.Repository = "https://github.com/example/cli" }, func(r *Request) { r.Intent.Repository = "github.com/../cli" }, func(r *Request) { r.Intent.SourceCommit = "main" }, func(r *Request) { r.Intent.TargetBranch = "" }, func(r *Request) { r.Intent.Version = "v1.2.3" }, func(r *Request) { r.Intent.Tag = "mismatch" }, func(r *Request) { r.Intent.BuildDate = "bad" }, func(r *Request) { r.Intent.BinaryName = "../escape" }, func(r *Request) { r.Inbox = "relative" }, func(r *Request) { r.Outbox = "" }, func(r *Request) { r.Worktree = "" }, func(r *Request) { r.Entrypoint = "-toolexec=evil" }, func(r *Request) { r.ModuleDir = "../escape" }, func(r *Request) { r.VersionFile = "../go.mod" }, func(r *Request) { r.Targets = []Target{} }, func(r *Request) { r.Targets = []Target{{"linux", "arm64"}, {"linux", "arm64"}} }}
	for i, m := range mutations {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			copy := r
			m(&copy)
			b, _ := json.Marshal(copy)
			var out bytes.Buffer
			if e := run(context.Background(), bytes.NewReader(b), &out); e == nil || out.Len() != 0 {
				t.Fatalf("invalid request produced success: %s", out.String())
			}
		})
	}
	for _, data := range []string{`{"intent":{"schema_version":1,"schema_version":2}}`, `{} {}`, `{"unknown":"secret-token"}`, strings.Repeat(" ", 128*1024+1)} {
		var out bytes.Buffer
		e := run(context.Background(), strings.NewReader(data), &out)
		if e == nil || out.Len() != 0 || strings.Contains(e.Error(), "secret-token") {
			t.Fatal("unsafe invalid input handling")
		}
	}
	r.Outbox = r.Worktree
	if _, e := roots(r); e == nil {
		t.Fatal("overlapping output accepted")
	}
}
func TestB14_B15NixPolicy(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		a := sdkInstallArgs("/nix/store/recorded", "/tmp/root", "/reject", prepared)
		joined := strings.Join(a, "|")
		for _, want := range []string{"--max-jobs|0", "--builders||", "allow-import-from-derivation|false", "fallback|false", "require-sigs|true", "pre-build-hook|/reject"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("missing %s: %v", want, a)
			}
		}
		if strings.Contains(joined, "--offline") != prepared {
			t.Fatal("prepared SDK must be offline")
		}
	}
}
func TestB08VersionDiagnostic(t *testing.T) {
	if safeObserved([]byte("go version go1.26.8 linux/arm64")) != "1.26.8" {
		t.Fatal("missing observed version")
	}
	if strings.Contains(safeObserved([]byte("secret-token")), "secret") {
		t.Fatal("leaks raw output")
	}
}
func TestB09CredentialAndGoEnvironment(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "scoped-secret-helper")
	t.Setenv("GOTOOLCHAIN", "auto")
	t.Setenv("GIT_DIR", "/wrong")
	t.Setenv("GOFLAGS", "-overlay=/wrong")
	t.Setenv("GOPROXY", "https://private.invalid")
	env := strings.Join(cleanEnv(), "\n")
	for _, want := range []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_VALUE_0=scoped-secret-helper", "GOPROXY=https://private.invalid"} {
		if !strings.Contains(env, want) {
			t.Fatal("lost credential configuration")
		}
	}
	for _, bad := range []string{"GOTOOLCHAIN=auto", "GIT_DIR=", "GOFLAGS="} {
		if strings.Contains(env, bad) {
			t.Fatal("ambient configuration retained")
		}
	}
}
func TestB16Cancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, e := command(ctx, "sh", []string{"-c", "sleep 60 & wait"}, "", cleanEnv()); e == nil {
		t.Fatal("cancelled process succeeded")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("process group was not stopped")
	}
}

func TestB25SHA256ObjectProjection(t *testing.T) {
	t.Setenv("GIT_DEFAULT_HASH", "sha256")
	r := fixture(t, nil)
	if len(r.Intent.SourceCommit) != 64 {
		t.Fatal("fixture is not a SHA-256 repository")
	}
	p := projectionFor(t, r)
	if p.format != "sha256" {
		t.Fatal("wrong object format")
	}
}
