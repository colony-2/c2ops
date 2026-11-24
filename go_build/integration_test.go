package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var realLauncher string

func TestMain(m *testing.M) {
	if os.Getenv("C2OPS_TEST_GO_BUILD_REAL") == "1" {
		realLauncher = os.Getenv("C2OPS_GO_BUILD_LAUNCHER")
		if realLauncher == "" {
			dir, e := os.MkdirTemp("", "go-build-test-")
			if e != nil {
				panic(e)
			}
			realLauncher = filepath.Join(dir, "go-build")
			c := exec.Command("go", "build", "-mod=readonly", "-o", realLauncher, ".")
			c.Stdout = os.Stderr
			c.Stderr = os.Stderr
			if e = c.Run(); e != nil {
				os.RemoveAll(dir)
				os.Exit(1)
			}
			code := m.Run()
			os.RemoveAll(dir)
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}
func realOnly(t *testing.T) {
	t.Helper()
	if os.Getenv("C2OPS_TEST_GO_BUILD_REAL") != "1" {
		t.Skip("real SDK checks require C2OPS_TEST_GO_BUILD_REAL=1; absence is unverified acceptance")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Fatal("unsupported real host")
	}
}
func invoke(t *testing.T, r Request, wantSuccess bool, expectedFailure ...string) Output {
	t.Helper()
	b, _ := json.Marshal(r)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, realLauncher)
	c.Stdin = bytes.NewReader(b)
	c.Env = os.Environ()
	cwd := t.TempDir()
	_ = os.Chmod(cwd, 0555)
	defer os.Chmod(cwd, 0755)
	c.Dir = cwd
	if override := os.Getenv("C2OPS_GO_BUILD_LAUNCHER"); override != "" {
		c.Dir = filepath.Dir(filepath.Dir(override))
	}
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	e := c.Run()
	if !wantSuccess {
		if e == nil || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("invalid failure: %v stdout=%s stderr=%s", e, stdout.String(), stderr.String())
		}
		if _, err := os.Stat(filepath.Join(r.Outbox, "go-build")); !os.IsNotExist(err) {
			t.Fatal("failed op exposed final outputs")
		}
		for _, want := range expectedFailure {
			if !strings.Contains(stderr.String(), want) {
				t.Fatalf("missing expected failure stage %q: %s", want, stderr.String())
			}
		}
		return Output{}
	}
	if e != nil {
		t.Fatalf("real op failed: %v %s", e, stderr.String())
	}
	if stdout.Len() > 128*1024 {
		t.Fatal("oversized output")
	}
	var envelope struct {
		Output Output `json:"output"`
	}
	if e = json.Unmarshal(stdout.Bytes(), &envelope); e != nil {
		t.Fatal(e)
	}
	o := envelope.Output
	if o.Intent != r.Intent || o.Provenance.SDK.ResolvedVersion != o.Provenance.SDK.ActualVersion {
		t.Fatal("invalid output identity")
	}
	seen := map[string]bool{}
	for _, f := range o.Files {
		if seen[f.Name] || !relative(f.Name, false) || f.ReleaseID != r.Intent.ReleaseID || f.SourceCommit != r.Intent.SourceCommit || f.Repository != r.Intent.Repository || f.Tag != r.Intent.Tag || f.Version != r.Intent.Version || f.SchemaVersion != 1 {
			t.Fatal("invalid descriptor")
		}
		seen[f.Name] = true
		b, e := os.ReadFile(filepath.Join(r.Outbox, f.Name))
		if e != nil || int64(len(b)) != f.Size || digest(b) != f.SHA256 {
			t.Fatal("invalid descriptor bytes")
		}
	}
	t.Logf("real compiler=%s host=%s version=%s; %d verified descriptors", o.Provenance.SDK.CompilerPath, o.Provenance.SDK.Host, o.Provenance.SDK.ActualVersion, len(o.Files))
	return o
}
func nativeTarget() Target { return Target{runtime.GOOS, runtime.GOARCH} }
func binaryOutput(t *testing.T, r Request) string {
	t.Helper()
	c := exec.Command(filepath.Join(r.Outbox, "go-build/binaries", runtime.GOOS+"_"+runtime.GOARCH, r.Intent.BinaryName))
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("fixture executable: %v %s", e, b)
	}
	return string(b)
}

// A control can run before the operation has realized its pinned SDK.
func fixtureSDK(t *testing.T, exact string) string {
	t.Helper()
	store := sdkStorePath(sdkPins[exact].paths[hostSystem()])
	args := sdkInstallArgs(store, filepath.Join(t.TempDir(), "sdk"), realLauncher, false)
	if err := exec.CommandContext(t.Context(), "nix", args...).Run(); err != nil {
		t.Fatalf("prepare pinned Go %s fixture SDK: %v", exact, err)
	}
	return store
}

func ambientControl(t *testing.T, r Request, exact string, extraFlags ...string) string {
	t.Helper()
	sdk := fixtureSDK(t, exact)
	dest := filepath.Join(t.TempDir(), "control")
	args := append([]string{"build", "-mod=readonly", "-buildvcs=false", "-o", dest}, extraFlags...)
	c := exec.Command(filepath.Join(sdk, "bin/go"), append(args, ".")...)
	c.Dir = r.Worktree
	c.Env = envWith(cleanEnv(), map[string]string{"GOROOT": sdk + "/share/go", "GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off", "CGO_ENABLED": "0", "GOMODCACHE": t.TempDir()})
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("healthy control build failed: %v %s", e, b)
	}
	b, e := exec.Command(dest).CombinedOutput()
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func TestRealB07_B09_B10_B12_B13_B19_C03(t *testing.T) {
	realOnly(t)
	for _, exact := range []string{"1.26.1", "1.26.8"} {
		t.Run(exact, func(t *testing.T) {
			r := fixture(t, nil)
			r.Targets = append([]Target{}, allTargets...)
			if exact == "1.26.8" {
				r.GoVersion = "1.26"
			}
			metadata := "release value; $(touch SHOULD_NOT_EXIST) & quote'"
			r.LinkerValues = map[string]string{"main.version": metadata}
			before := gitTest(t, r.Worktree, "status", "--porcelain=v1", "--untracked-files=all")
			conflict := t.TempDir()
			write(t, conflict, "go", "#!/bin/sh\nexit 87\n")
			_ = os.Chmod(filepath.Join(conflict, "go"), 0755)
			t.Setenv("PATH", conflict+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GOFLAGS", "-overlay=/nonexistent")
			t.Setenv("GOENV", "/nonexistent")
			t.Setenv("GOWORK", "/nonexistent")
			t.Setenv("GOROOT", "/nonexistent")
			t.Setenv("GOTOOLCHAIN", "go1.99.9")
			t.Setenv("CGO_ENABLED", "1")
			o := invoke(t, r, true)
			if exact == "1.26.1" {
				if dir := os.Getenv("C2OPS_GO_BUILD_EXAMPLE_DIR"); dir != "" {
					example := r
					example.Worktree = "/workspace/cli"
					example.Inbox = "/inbox"
					example.Outbox = "/outbox"
					input, _ := json.MarshalIndent(example, "", "  ")
					output, _ := json.MarshalIndent(map[string]any{"output": o}, "", "  ")
					write(t, dir, "input.json", string(input)+"\n")
					write(t, dir, "output.json", string(output)+"\n")
					inventory, e := os.ReadFile(filepath.Join(r.Outbox, o.Provenance.Source.InventoryName))
					if e != nil {
						t.Fatal(e)
					}
					write(t, dir, "source-tree.json", string(inventory))
				}
			}
			if o.Provenance.SDK.ActualVersion != exact || len(o.Files) != 5 {
				t.Fatal("SDK selection/target set wrong")
			}
			if got := binaryOutput(t, r); got != "committed|"+metadata+"|go"+exact {
				t.Fatalf("metadata/behavior changed: %s", got)
			}
			for _, f := range o.Files {
				if f.Kind != "binary" {
					continue
				}
				p := filepath.Join(r.Outbox, f.Name)
				info, e := buildinfo.ReadFile(p)
				if e != nil || info.GoVersion != "go"+exact {
					t.Fatalf("wrong real compiler metadata %v %v", info, e)
				}
				if f.GOOS == "linux" {
					v, e := elf.Open(p)
					if e != nil {
						t.Fatal(e)
					}
					want := elf.EM_X86_64
					if f.GOARCH == "arm64" {
						want = elf.EM_AARCH64
					}
					if v.Machine != want {
						t.Fatal("wrong ELF machine")
					}
					v.Close()
				} else {
					v, e := macho.Open(p)
					if e != nil {
						t.Fatal(e)
					}
					want := macho.CpuAmd64
					if f.GOARCH == "arm64" {
						want = macho.CpuArm64
					}
					if v.Cpu != want {
						t.Fatal("wrong Mach-O CPU")
					}
					v.Close()
				}
			}
			if got := gitTest(t, r.Worktree, "status", "--porcelain=v1", "--untracked-files=all"); got != before {
				t.Fatal("source modified")
			}
		})
	}
	// Exact manual selection uses the same executable and the same 1.26.1 mapping.
	r := fixture(t, nil)
	r.Targets = []Target{nativeTarget()}
	r.GoVersion = "1.26.1"
	o := invoke(t, r, true)
	if o.Provenance.SDK.ActualVersion != "1.26.1" || o.Provenance.SDK.Directive != nil {
		t.Fatal("manual SDK selection failed")
	}
}
func TestRealB20_B21_B22InjectionControls(t *testing.T) {
	realOnly(t)
	for _, ignored := range []bool{false, true} {
		name := "B21 untracked"
		if ignored {
			name = "B22 ignored"
		}
		t.Run(name, func(t *testing.T) {
			files := map[string]string{}
			if ignored {
				files[".gitignore"] = "injected.go\n"
			}
			r := fixture(t, files)
			r.Targets = []Target{nativeTarget()}
			if got := ambientControl(t, r, "1.26.1"); got != "committed|unset|go1.26.1" {
				t.Fatal("bad positive control")
			}
			injected := "package main\nfunc init(){state=\"injected\"}\n"
			write(t, r.Worktree, "injected.go", injected)
			if ignored && gitTest(t, r.Worktree, "status", "--porcelain=v1") != "" {
				t.Fatal("ignored control is not ordinary-status clean")
			}
			if got := ambientControl(t, r, "1.26.1"); got != "injected|unset|go1.26.1" {
				t.Fatal("injection did not affect control executable")
			}
			invoke(t, r, true)
			if got := binaryOutput(t, r); got != "committed|unset|go1.26.1" {
				t.Fatal("release consumed ambient injected source")
			}
			b, _ := os.ReadFile(filepath.Join(r.Worktree, "injected.go"))
			if string(b) != injected {
				t.Fatal("caller injection changed")
			}
		})
	}
	// Actual process rejection controls accompany unit projection corruption.
	for _, kind := range []string{"wrong HEAD", "tracked edit"} {
		t.Run("B20 "+kind, func(t *testing.T) {
			r := fixture(t, nil)
			if kind == "wrong HEAD" {
				r.Intent.SourceCommit = strings.Repeat("a", 40)
			} else {
				write(t, r.Worktree, "main.go", "bad")
			}
			invoke(t, r, false)
		})
	}
}

const embedMain = `package main
import("embed";"fmt";"sort";"strings")
//go:embed assets/*
var assets embed.FS
func main(){es,_:=assets.ReadDir("assets");names:=[]string{};for _,e:=range es{b,_:=assets.ReadFile("assets/"+e.Name());names=append(names,e.Name()+"="+string(b))};sort.Strings(names);fmt.Print(strings.Join(names,"|"))}
`

func TestRealB23_B25EmbeddedAndCompleteCommittedInputs(t *testing.T) {
	realOnly(t)
	r := fixture(t, map[string]string{"main.go": embedMain, ".gitignore": "assets/ignored.txt\n", ".gitattributes": "assets/export.txt export-ignore\n", "assets/committed.txt": "committed", "assets/ignored.txt": "force-added", "assets/export.txt": "retained", "odd \t\nname": "unusual"})
	r.Targets = []Target{nativeTarget()}
	// Preserve an executable mode as well as paths ignored by both Git mechanisms.
	_ = os.Chmod(filepath.Join(r.Worktree, "odd \t\nname"), 0755)
	gitTest(t, r.Worktree, "add", ".")
	gitTest(t, r.Worktree, "commit", "-qm", "mode")
	r.Intent.SourceCommit = gitTest(t, r.Worktree, "rev-parse", "HEAD")
	write(t, r.Worktree, "assets/untracked.txt", "ambient")
	write(t, r.Worktree, "assets/secret.ignored", "ignored")
	write(t, r.Worktree, ".git/info/exclude", "assets/secret.ignored\n")
	control := ambientControl(t, r, "1.26.1")
	if !strings.Contains(control, "secret.ignored=ignored") || !strings.Contains(control, "untracked.txt=ambient") {
		t.Fatal("embed control did not include injection")
	}
	o := invoke(t, r, true)
	if got := binaryOutput(t, r); got != "committed.txt=committed|export.txt=retained|ignored.txt=force-added" {
		t.Fatalf("wrong release assets: %s", got)
	}
	invBytes, e := os.ReadFile(filepath.Join(r.Outbox, o.Provenance.Source.InventoryName))
	if e != nil {
		t.Fatal(e)
	}
	var inv struct {
		Entries []entry `json:"entries"`
	}
	if e = json.Unmarshal(invBytes, &inv); e != nil {
		t.Fatal(e)
	}
	// Compare every recorded tree/blob entry to the private projection inventory.
	p := projectionFor(t, r)
	if !bytes.Equal(invBytes, p.inventory()) {
		t.Fatal("inventory does not describe complete committed paths/bytes/modes")
	}
	missing := fixture(t, map[string]string{"main.go": embedMain})
	missing.Targets = []Target{nativeTarget()}
	write(t, missing.Worktree, "assets/untracked.txt", "only ambient")
	if ambientControl(t, missing, "1.26.1") != "untracked.txt=only ambient" {
		t.Fatal("missing-asset control invalid")
	}
	invoke(t, missing, false)
}
func TestRealB24EscapingAndContainedModules(t *testing.T) {
	realOnly(t)
	for _, kind := range []string{"replace", "work", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			r := fixture(t, nil)
			r.Targets = []Target{nativeTarget()}
			outside := filepath.Join(filepath.Dir(r.Worktree), "outside")
			write(t, outside, "go.mod", "module example.com/dep\ngo 1.26.1\n")
			write(t, outside, "dep.go", "package dep\nconst Value=\"external\"\n")
			if kind == "symlink" {
				write(t, outside, "injected.go", "package main\nfunc init(){state=\"external\"}\n")
				if e := os.Symlink("../outside/injected.go", filepath.Join(r.Worktree, "injected.go")); e != nil {
					t.Fatal(e)
				}
				if ambientControl(t, r, "1.26.1") != "external|unset|go1.26.1" {
					t.Fatal("invalid executable symlink control")
				}

			} else {
				write(t, r.Worktree, "main.go", "package main\nimport(\"fmt\";\"example.com/dep\")\nfunc main(){fmt.Print(dep.Value)}\n")
				mod := "module example.com/cli\ngo 1.26.1\nrequire example.com/dep v0.0.0\n"
				if kind == "replace" {
					mod += "replace example.com/dep => ../outside\n"
				} else {
					write(t, r.Worktree, "go.work", "go 1.26.1\nuse (\n .\n ../outside\n)\n")
					r.VersionFile = "go.work"
				}
				write(t, r.Worktree, "go.mod", mod)
				if kind == "replace" {
					if ambientControl(t, r, "1.26.1") != "external" {
						t.Fatal("bad replacement control")
					}
				} else {
					sdk := fixtureSDK(t, "1.26.1")
					dest := filepath.Join(t.TempDir(), "control")
					c := exec.Command(sdk+"/bin/go", "build", "-mod=readonly", "-o", dest, ".")
					c.Dir = r.Worktree
					c.Env = envWith(cleanEnv(), map[string]string{"GOROOT": sdk + "/share/go", "GOENV": "off", "GOTOOLCHAIN": "local", "GOWORK": filepath.Join(r.Worktree, "go.work"), "CGO_ENABLED": "0"})
					if b, e := c.CombinedOutput(); e != nil {
						t.Fatalf("invalid workspace control: %v %s", e, b)
					}
					b, e := exec.Command(dest).CombinedOutput()
					if e != nil || string(b) != "external" {
						t.Fatal("bad workspace control behavior")
					}
				}
			}
			gitTest(t, r.Worktree, "add", ".")
			gitTest(t, r.Worktree, "commit", "-qm", "escape")
			r.Intent.SourceCommit = gitTest(t, r.Worktree, "rev-parse", "HEAD")
			invoke(t, r, false)
		})
	}
	r := fixture(t, map[string]string{"main.go": "package main\nimport(\"fmt\";\"example.com/dep\")\nfunc main(){fmt.Print(dep.Value)}\n", "go.mod": "module example.com/cli\ngo 1.26.1\nrequire example.com/dep v0.0.0\nreplace example.com/dep => ./dep\n", "dep/go.mod": "module example.com/dep\ngo 1.26.1\n", "dep/dep.go": "package dep\nconst Value=\"contained\"\n", "go.work": "go 1.26.1\nuse (\n .\n ./dep\n)\n"})
	r.Targets = []Target{nativeTarget()}
	r.VersionFile = "go.work"
	invoke(t, r, true)
	if binaryOutput(t, r) != "contained" {
		t.Fatal("contained module/workspace failed")
	}
}
func TestRealB08_B11_B14_B15_B19FailureBoundaries(t *testing.T) {
	realOnly(t)
	for _, kind := range []string{"mismatch", "missing closure", "failed target"} {
		t.Run(kind, func(t *testing.T) {
			r := fixture(t, nil)
			r.Targets = append([]Target{}, allTargets...)
			buildCalls := 0
			runFake := func(ctx context.Context, exe string, args []string, cwd string, env []string) ([]byte, error) {
				if exe == "nix" {
					if kind == "missing closure" {
						return nil, os.ErrNotExist
					}
					return nil, nil
				}
				if len(args) > 0 && args[0] == "version" {
					if kind == "mismatch" {
						return []byte("go version go1.26.8 " + runtime.GOOS + "/" + runtime.GOARCH), nil
					}
					return []byte("go version go1.26.1 " + runtime.GOOS + "/" + runtime.GOARCH), nil
				}
				if len(args) > 0 && args[0] == "list" {
					return json.Marshal(map[string]string{"Name": "main", "Dir": cwd})
				}
				buildCalls++
				if buildCalls == 2 {
					return nil, os.ErrPermission
				}
				for i, a := range args {
					if a == "-o" {
						write(t, filepath.Dir(args[i+1]), filepath.Base(args[i+1]), "fixture")
					}
				}
				return nil, nil
			}
			if _, e := buildWith(context.Background(), r, runFake); e == nil {
				t.Fatal("failed target/SDK succeeded")
			}
			if kind != "failed target" && buildCalls != 0 {
				t.Fatal("compiled before SDK verified")
			}
			if _, e := os.Stat(filepath.Join(r.Outbox, "go-build")); !os.IsNotExist(e) {
				t.Fatal("failure exposed complete artifact set")
			}
			if gitTest(t, r.Worktree, "status", "--porcelain=v1") != "" {
				t.Fatal("failure modified source")
			}
		})
	}
}

func TestRealB16OpCancellation(t *testing.T) {
	realOnly(t)
	r := fixture(t, nil)
	r.Targets = []Target{nativeTarget()}
	b, _ := json.Marshal(r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := exec.CommandContext(ctx, realLauncher)
	c.Stdin = bytes.NewReader(b)
	c.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if e := c.Start(); e != nil {
		t.Fatal(e)
	}
	// Wait until the operation is inside its SDK/build subprocess stage. Its
	// source preparation is much shorter than a fresh native build/cache.
	time.Sleep(150 * time.Millisecond)
	_ = c.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case e := <-done:
		if e == nil || stdout.Len() != 0 {
			t.Fatalf("cancelled operation reported success: %v %s", e, stdout.String())
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("operation failed to cancel")
	}
	if _, e := os.Stat(filepath.Join(r.Outbox, "go-build")); !os.IsNotExist(e) {
		t.Fatal("cancelled operation exposed completed outputs")
	}
}
func TestCanonicalLinkerDigest(t *testing.T) {
	values := map[string]string{"main.z": "<>&雪\u2028", "main.a": "literal\\u2028"}
	want := "{\"main.a\":\"literal\\\\u2028\",\"main.z\":\"<>&雪\u2028\"}"
	if string(canonicalJSON(values)) != want {
		t.Fatalf("canonical UTF-8 changed: %s", canonicalJSON(values))
	}
}

// These inputs describe authorized, already committed read-only fixtures. Each
// invocation uses fresh private module/build caches; no publication is involved.
func liveRequest(t *testing.T, key string) Request {
	t.Helper()
	realOnly(t)
	file := os.Getenv(key)
	if file == "" {
		t.Skip(key + " not supplied; private-service acceptance remains unverified")
	}
	b, e := os.ReadFile(file)
	if e != nil {
		t.Fatal("cannot read authorized fixture input")
	}
	var r Request
	if e = strictJSON(b, &r); e != nil {
		t.Fatal("invalid authorized fixture JSON")
	}
	r.Inbox = t.TempDir()
	r.Outbox = t.TempDir()
	r.Targets = []Target{nativeTarget()}
	if e = r.validate(); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestRealB17FreshPrivateGit(t *testing.T) {
	r := liveRequest(t, "C2OPS_GO_BUILD_PRIVATE_INPUT")
	invoke(t, r, true)
	denied := liveRequest(t, "C2OPS_GO_BUILD_DENIED_INPUT")
	invoke(t, denied, false)
}
func TestRealB18SeparateProxyAuthorization(t *testing.T) {
	r := liveRequest(t, "C2OPS_GO_BUILD_PROXY_INPUT")
	// Positive input has independently supplied proxy authorization. The negative
	// URL must address the same fixture proxy without that authorization.
	unauth := os.Getenv("C2OPS_GO_BUILD_UNAUTHORIZED_PROXY")
	if unauth == "" {
		t.Skip("unauthorized proxy endpoint not supplied; B18 remains unverified")
	}
	invoke(t, r, true)
	r.Outbox = t.TempDir()
	t.Setenv("GOPROXY", unauth)
	t.Setenv("GOAUTH", "off")
	invoke(t, r, false)
}
func TestRealC08_C11SDKMappingMetadata(t *testing.T) {
	realOnly(t)
	for exact, pin := range sdkPins {
		for host, id := range pin.paths {
			_, beforeError := os.Stat(sdkStorePath(id))
			beforeInstalled := beforeError == nil
			selector := "github:NixOS/nixpkgs/" + pin.revision + "#legacyPackages." + host + "." + pin.attribute
			b, e := command(context.Background(), "nix", []string{"--extra-experimental-features", "nix-command flakes", "eval", "--json", "--option", "allow-import-from-derivation", "false", "--option", "max-jobs", "0", "--option", "builders", "", "--apply", "p: { version=p.version; path=p.outPath; }", selector}, "", cleanEnv())
			if e != nil {
				t.Fatalf("metadata lookup failed for %s %s", exact, host)
			}
			var result struct {
				Version string `json:"version"`
				Path    string `json:"path"`
			}
			if e = json.Unmarshal(b, &result); e != nil || result.Version != exact || result.Path != sdkStorePath(id) {
				t.Fatalf("pinned SDK metadata mismatch for %s %s", exact, host)
			}
			_, afterError := os.Stat(sdkStorePath(id))
			if (afterError == nil) != beforeInstalled {
				t.Fatalf("metadata installed SDK payload for %s %s", exact, host)
			}
		}
	}
}
func TestRealB15PreferLocalBuildIsRejected(t *testing.T) {
	realOnly(t)
	marker := filepath.Join(t.TempDir(), "BUILDER_RAN")
	expr := `let d = derivation { name = "go-build-forbidden-sdk-policy-fixture"; system = builtins.currentSystem; builder = "/bin/sh"; args = [ "-c" "touch ` + marker + `; mkdir $out" ]; preferLocalBuild = true; allowSubstitutes = false; }; in d.outPath`
	b, e := command(context.Background(), "nix", []string{"--extra-experimental-features", "nix-command", "eval", "--raw", "--impure", "--expr", expr}, "", cleanEnv())
	if e != nil {
		t.Fatal("cannot evaluate local SDK policy fixture")
	}
	store := string(b)
	args := sdkInstallArgs(store, filepath.Join(t.TempDir(), "root"), realLauncher, false)
	if _, e = command(context.Background(), "nix", args, "", cleanEnv()); e == nil {
		t.Fatal("forbidden local SDK fixture built")
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("Nix ran a local builder despite cache-only policy")
	}
}

func TestRealRequiresMainPackage(t *testing.T) {
	realOnly(t)
	r := fixture(t, map[string]string{"main.go": "package library\nconst Value=1\n"})
	r.Targets = []Target{nativeTarget()}
	invoke(t, r, false)
}

func TestRealC07OversizedResultIsNotPublished(t *testing.T) {
	realOnly(t)
	r := fixture(t, nil)
	r.Targets = append([]Target{}, allTargets...)
	// Exercise result growth across repeated descriptor identities, independently
	// of binary size. This fixture uses fake compiler output, not SDK acceptance.
	r.Intent.Repository = "github.com/" + strings.Repeat("x", 22000) + "/cli"
	gitTest(t, r.Worktree, "remote", "set-url", "origin", "https://"+r.Intent.Repository+".git")
	if e := r.validate(); e != nil {
		t.Fatal(e)
	}
	runFake := func(ctx context.Context, exe string, args []string, cwd string, env []string) ([]byte, error) {
		if exe == "nix" {
			return nil, nil
		}
		if args[0] == "version" {
			return []byte("go version go1.26.1 " + runtime.GOOS + "/" + runtime.GOARCH), nil
		}
		if args[0] == "list" {
			return json.Marshal(map[string]string{"Name": "main", "Dir": cwd})
		}
		for i, a := range args {
			if a == "-o" {
				write(t, filepath.Dir(args[i+1]), filepath.Base(args[i+1]), "fixture")
			}
		}
		return nil, nil
	}
	_, e := buildWith(context.Background(), r, runFake)
	if e == nil || !strings.Contains(e.Error(), "128 KiB") {
		t.Fatalf("oversized result was not rejected at output boundary: %v", e)
	}
	if _, e = os.Stat(filepath.Join(r.Outbox, "go-build")); !os.IsNotExist(e) {
		t.Fatal("oversized result exposed final outputs")
	}
}
