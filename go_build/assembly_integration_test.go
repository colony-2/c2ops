package main

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func assemblyBody(arch, value string) string {
	if arch == "arm64" {
		return "TEXT ·Value(SB),NOSPLIT,$0-8\n MOVD $" + value + ", R0\n MOVD R0, ret+0(FP)\n RET\n"
	}
	return "TEXT ·Value(SB),NOSPLIT,$0-8\n MOVQ $" + value + ", ret+0(FP)\n RET\n"
}

func TestRealB24DownloadedModuleAssembly(t *testing.T) {
	realOnly(t)
	for _, escape := range []bool{false, true} {
		name := "contained"
		if escape {
			name = "external include"
		}
		t.Run(name, func(t *testing.T) {
			r := fixture(t, map[string]string{
				".gitignore": "ignored.inc\n",
				"main.go":    "package main\nimport(\"fmt\";\"example.com/dep\")\nfunc main(){fmt.Print(dep.Value())}\n",
				"go.mod":     "module example.com/cli\ngo 1.26.1\nrequire example.com/dep v1.0.0\n",
			})
			r.Targets = []Target{nativeTarget()}
			proxy := t.TempDir()
			module := "module example.com/dep\ngo 1.26.1\n"
			write(t, proxy, "example.com/dep/@v/v1.0.0.mod", module)
			write(t, proxy, "example.com/dep/@v/v1.0.0.info", `{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`)
			f, err := os.Create(filepath.Join(proxy, "example.com/dep/@v/v1.0.0.zip"))
			if err != nil {
				t.Fatal(err)
			}
			z := zip.NewWriter(f)
			files := map[string]string{"go.mod": module, "value.go": "package dep\nfunc Value() int64\n"}
			for _, arch := range []string{"amd64", "arm64"} {
				files["value_"+arch+".s"] = "#include \"textflag.h\"\n#include \"value_" + arch + ".inc\"\n"
				files["value_"+arch+".inc"] = assemblyBody(arch, "42")
				if escape {
					files["value_"+arch+".inc"] = "#include " + strconv.Quote(filepath.Join(r.Worktree, "ignored.inc")) + "\n"
				}
			}
			for path, body := range files {
				w, e := z.Create("example.com/dep@v1.0.0/" + path)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = w.Write([]byte(body)); e != nil {
					t.Fatal(e)
				}
			}
			if err = z.Close(); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
			t.Setenv("GOSUMDB", "off") // Disposable local proxy; checksums are committed below.
			t.Setenv("GOPRIVATE", "")
			t.Setenv("GONOPROXY", "none")
			sdk := fixtureSDK(t, "1.26.1")
			c := exec.Command(sdk+"/bin/go", "mod", "download", "example.com/dep")
			c.Dir = r.Worktree
			c.Env = envWith(cleanEnv(), map[string]string{"GOMODCACHE": t.TempDir(), "GOROOT": sdk + "/share/go", "GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off"})
			if b, e := c.CombinedOutput(); e != nil {
				t.Fatalf("fixture dependency preparation failed: %v %s", e, b)
			}
			gitTest(t, r.Worktree, "add", "go.sum")
			gitTest(t, r.Worktree, "commit", "-qm", "record dependency checksums")
			r.Intent.SourceCommit = gitTest(t, r.Worktree, "rev-parse", "HEAD")
			if !escape {
				invoke(t, r, true)
				if binaryOutput(t, r) != "42" {
					t.Fatal("contained downloaded assembly did not execute")
				}
				t.Log("fresh module cache downloaded checksummed fixture; native dependency executable=42")
				return
			}
			for _, value := range []string{"42", "99"} {
				write(t, r.Worktree, "ignored.inc", assemblyBody(runtime.GOARCH, value))
				if got := ambientControl(t, r, "1.26.1", "-asmflags=example.com/...=-D=SOURCE_CONTROL="+value); got != value {
					t.Fatalf("downloaded dependency control printed %q", got)
				}
				invoke(t, r, false, "assembly")
				t.Logf("downloaded dependency control=%s; release rejected before build", value)
			}
		})
	}
}

func assemblyFixture(t *testing.T, dependency bool) (Request, string) {
	t.Helper()
	files := map[string]string{
		".gitignore": "ignored.inc\n",
		"main.go":    "package main\nimport \"fmt\"\nfunc Value() int64\nfunc main(){fmt.Print(Value())}\n",
	}
	prefix := ""
	if dependency {
		prefix = "dep/"
		files["main.go"] = "package main\nimport(\"fmt\";\"example.com/dep\")\nfunc main(){fmt.Print(dep.Value())}\n"
		files["go.mod"] = "module example.com/cli\ngo 1.26.1\nrequire example.com/dep v0.0.0\nreplace example.com/dep => ./dep\n"
		files["dep/go.mod"] = "module example.com/dep\ngo 1.26.1\n"
		files["dep/value.go"] = "package dep\nfunc Value() int64\n"
	}
	for _, arch := range []string{"amd64", "arm64"} {
		files[prefix+"value_"+arch+".s"] = "#include \"textflag.h\"\n#include \"go_asm.h\"\n#include \"value_" + arch + ".inc\"\n"
		files[prefix+"value_"+arch+".inc"] = assemblyBody(arch, "42")
	}
	return fixture(t, files), prefix
}

func TestRealB20_B24AssemblySourceBoundary(t *testing.T) {
	realOnly(t)
	t.Run("contained committed includes and SDK headers", func(t *testing.T) {
		r, _ := assemblyFixture(t, false)
		r.Targets = append([]Target{}, allTargets...)
		o := invoke(t, r, true)
		if got := binaryOutput(t, r); got != "42" {
			t.Fatalf("contained assembly executable printed %q", got)
		}
		if o.Provenance.Build.AssemblyPolicy != assemblyPolicy {
			t.Fatal("missing assembly input policy provenance")
		}
		t.Log("four target builds accepted contained committed assembly and SDK/generated headers; native executable=42")
	})
	for _, kind := range []string{"direct", "nested", "escaped path", "macro", "dependency"} {
		t.Run(kind, func(t *testing.T) {
			r, prefix := assemblyFixture(t, kind == "dependency")
			r.Targets = []Target{nativeTarget()}
			external := filepath.Join(r.Worktree, "ignored.inc")
			quoted := strconv.Quote(external)
			if kind == "escaped path" {
				quoted = `"` + strings.ReplaceAll(external, "/", `\x2f`) + `"`
			}
			assembly := "#include \"textflag.h\"\n#include " + quoted + "\n"
			if kind == "nested" {
				assembly = "#include \"textflag.h\"\n#include \"value_" + runtime.GOARCH + ".inc\"\n"
				write(t, r.Worktree, prefix+"value_"+runtime.GOARCH+".inc", "#include "+quoted+"\n")
			}
			if kind == "macro" {
				assembly = "#include \"textflag.h\"\n#define IMPORT #include " + quoted + "\nIMPORT\n"
			}
			write(t, r.Worktree, prefix+"value_"+runtime.GOARCH+".s", assembly)
			gitTest(t, r.Worktree, "add", ".")
			gitTest(t, r.Worktree, "commit", "-qm", "assembly escape")
			r.Intent.SourceCommit = gitTest(t, r.Worktree, "rev-parse", "HEAD")
			inventoryHash := digest(projectionFor(t, r).inventory())
			for _, value := range []string{"42", "99"} {
				write(t, r.Worktree, "ignored.inc", assemblyBody(runtime.GOARCH, value))
				if gitTest(t, r.Worktree, "status", "--porcelain=v1") != "" {
					t.Fatal("ignored assembly control must have clean ordinary Git status")
				}
				// Force only fixture assembly actions to rerun: Go's cache does not
				// necessarily know about includes outside its package file lists.
				flag := "-asmflags=example.com/...=-D=SOURCE_CONTROL=" + value
				if got := ambientControl(t, r, "1.26.1", flag); got != value {
					t.Fatalf("escape did not affect actual control executable: got %q want %q", got, value)
				}
				if digest(projectionFor(t, r).inventory()) != inventoryHash {
					t.Fatal("ignored include altered recorded inventory")
				}
				invoke(t, r, false, "assembly")
				t.Logf("control executable=%s; release rejected without outputs; commit=%s inventory_sha256=%s", value, r.Intent.SourceCommit, inventoryHash)
			}
			if got := gitTest(t, r.Worktree, "rev-parse", "HEAD"); got != r.Intent.SourceCommit {
				t.Fatalf("caller commit changed: %s", got)
			}
		})
	}
}
