package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func makeFIFO(t *testing.T, p string) { t.Helper(); must(t, syscall.Mkfifo(p, 0600)) }
func TestD01D02D04D07CommittedObjects(t *testing.T) {
	r := fixture(t)
	// A smudge filter changes only checkout bytes. Recorded blobs remain exact;
	// clean filter makes the tracked checkout compare equal to its recorded blob.
	git(t, r.Worktree, "config", "filter.fixture.smudge", "sed s/committed/checkout/g")
	git(t, r.Worktree, "config", "filter.fixture.clean", "sed s/checkout/committed/g")
	f := filepath.Join(r.Worktree, ".gitattributes")
	old, e := os.ReadFile(f)
	must(t, e)
	put(t, f, append(old, []byte("README.md filter=fixture\n")...), 0644)
	git(t, r.Worktree, "add", ".")
	git(t, r.Worktree, "commit", "-qm", "filter")
	r.Intent["source_commit"] = git(t, r.Worktree, "rev-parse", "HEAD")
	for _, d := range r.Files {
		d["source_commit"] = r.Intent["source_commit"]
	}
	put(t, filepath.Join(r.Worktree, "README.md"), []byte("checkout readme\n"), 0644)
	// Replace the original README blob with different content. --no-replace-objects
	// and direct object hashing must still retain the original bytes.
	orig := git(t, r.Worktree, "rev-parse", "HEAD:README.md")
	other := filepath.Join(t.TempDir(), "other")
	put(t, other, []byte("evil replacement\n"), 0644)
	replacement := git(t, r.Worktree, "hash-object", "-w", other)
	git(t, r.Worktree, "replace", orig, replacement)
	t.Setenv("GIT_DIR", "/nonexistent/redirect")
	t.Setenv("GIT_OBJECT_DIRECTORY", "/nonexistent/objects")
	t.Setenv("GIT_INDEX_FILE", "/nonexistent/index")
	// Existing scoped configuration is deliberately retained.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "fixture.secret")
	t.Setenv("GIT_CONFIG_VALUE_0", "sentinel-preserved")
	g := gitSource{context.Background(), r.Worktree}
	v, e := g.run("config", "--get", "fixture.secret")
	must(t, e)
	if strings.TrimSpace(string(v)) != "sentinel-preserved" {
		t.Fatal("lost scoped config")
	}
	b := success(t, r)
	data, e := os.ReadFile(filepath.Join(r.Outbox, str(resultFiles(t, b)[0], "name")))
	must(t, e)
	entries := unpack(t, data)
	if string(entries["README.md"].b) != "committed readme\n" {
		t.Fatal("ambient/filter/replacement bytes imported")
	}
	current, e := os.ReadFile(filepath.Join(r.Worktree, "README.md"))
	must(t, e)
	if !bytes.Equal(current, []byte("checkout readme\n")) {
		t.Fatal("changed checkout")
	}
}
func TestD03D05D06SourceFailures(t *testing.T) {
	for _, kind := range []string{"wrong HEAD", "staged", "unstaged", "unmerged", "missing doc", "unsafe doc", "duplicate doc", "overlap docs", "binary collision", "binary prefix", "symlink", "gitlink", "lfs"} {
		t.Run(kind, func(t *testing.T) {
			r := fixture(t)
			commit := false
			switch kind {
			case "wrong HEAD":
				r.Intent["source_commit"] = strings.Repeat("a", 40)
				for _, d := range r.Files {
					d["source_commit"] = r.Intent["source_commit"]
				}
			case "staged", "unstaged":
				put(t, filepath.Join(r.Worktree, "README.md"), []byte("edit"), 0644)
				if kind == "staged" {
					git(t, r.Worktree, "add", "README.md")
				}
			case "unmerged":
				blob := git(t, r.Worktree, "rev-parse", "HEAD:README.md")
				git(t, r.Worktree, "update-index", "--force-remove", "README.md")
				for _, stage := range []string{"1", "2", "3"} {
					line := "100644 " + blob + " " + stage + "\tREADME.md\n"
					c := gitCommand(r.Worktree, "update-index", "--index-info")
					c.Stdin = strings.NewReader(line)
					if out, e := c.CombinedOutput(); e != nil {
						t.Fatalf("index %v: %s", e, out)
					}
				}
			case "missing doc":
				r.Docs = []string{"missing"}
			case "unsafe doc":
				r.Docs = []string{"../README.md"}
			case "duplicate doc":
				r.Docs = []string{"README.md", "README.md"}
			case "overlap docs":
				r.Docs = []string{"docs", "docs/run"}
			case "binary collision":
				put(t, filepath.Join(r.Worktree, "cli"), []byte("collision"), 0644)
				r.Docs = []string{"cli"}
				commit = true
			case "binary prefix":
				put(t, filepath.Join(r.Worktree, "cli", "readme"), []byte("collision"), 0644)
				r.Docs = []string{"cli"}
				commit = true
			case "symlink":
				must(t, os.Symlink("../../outside", filepath.Join(r.Worktree, "docs", "link")))
				commit = true
			case "gitlink":
				git(t, r.Worktree, "update-index", "--add", "--cacheinfo", "160000,"+str(r.Intent, "source_commit")+",docs/submodule")
				git(t, r.Worktree, "commit", "-qm", "gitlink")
				commit = true
			case "lfs":
				put(t, filepath.Join(r.Worktree, "docs", "lfs"), []byte("version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 10\n"), 0644)
				commit = true
			}
			if commit {
				git(t, r.Worktree, "add", "-u")
				if kind != "gitlink" {
					git(t, r.Worktree, "add", ".")
					git(t, r.Worktree, "commit", "-qm", "bad docs")
				}
				r.Intent["source_commit"] = git(t, r.Worktree, "rev-parse", "HEAD")
				for _, d := range r.Files {
					d["source_commit"] = r.Intent["source_commit"]
				}
			}
			noOutput(t, r)
		})
	}
}
func TestK05LongPaths(t *testing.T) {
	r := fixture(t)
	name := "docs/" + strings.Repeat("long-name-", 15) + "/" + strings.Repeat("file-", 25) + ".txt"
	put(t, filepath.Join(r.Worktree, name), []byte("long committed path"), 0644)
	git(t, r.Worktree, "add", ".")
	git(t, r.Worktree, "commit", "-qm", "long path")
	r.Intent["source_commit"] = git(t, r.Worktree, "rev-parse", "HEAD")
	for _, d := range r.Files {
		d["source_commit"] = r.Intent["source_commit"]
	}
	b := success(t, r)
	data, e := os.ReadFile(filepath.Join(r.Outbox, str(resultFiles(t, b)[0], "name")))
	must(t, e)
	if string(unpack(t, data)[name].b) != "long committed path" {
		t.Fatal("long path lost")
	}
}

func gitCommand(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}

func TestSourceMustBeTheBoundGitWorktreeRoot(t *testing.T) {
	r := fixture(t)
	r.Worktree = filepath.Join(r.Worktree, "docs")
	noOutput(t, r)
}

func TestD08CorruptIntermediateTrees(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		for _, directory := range []string{"docs", "docs/deep"} {
			t.Run(format+"/"+directory, func(t *testing.T) {
				t.Setenv("GIT_DEFAULT_HASH", format)
				r := fixture(t)
				name := directory + "/guide.txt"
				put(t, filepath.Join(r.Worktree, name), []byte("committed guide\n"), 0644)
				git(t, r.Worktree, "add", ".")
				git(t, r.Worktree, "commit", "-qm", "nested guide")
				r.Intent["source_commit"] = git(t, r.Worktree, "rev-parse", "HEAD")
				for _, d := range r.Files {
					d["source_commit"] = r.Intent["source_commit"]
				}
				// Healthy control: committed content is actually archived.
				b := success(t, r)
				archive, e := os.ReadFile(filepath.Join(r.Outbox, str(resultFiles(t, b)[0], "name")))
				must(t, e)
				if string(unpack(t, archive)[name].b) != "committed guide\n" {
					t.Fatal("healthy document missing")
				}
				for _, d := range resultFiles(t, b) {
					must(t, os.Remove(filepath.Join(r.Outbox, str(d, "name"))))
				}
				tree := git(t, r.Worktree, "rev-parse", "HEAD:"+directory)
				g := gitSource{context.Background(), r.Worktree}
				raw, e := g.object("tree", tree)
				must(t, e)
				original, e := hex.DecodeString(git(t, r.Worktree, "rev-parse", "HEAD:"+name))
				must(t, e)
				injection := filepath.Join(t.TempDir(), "injected")
				put(t, injection, []byte("uncommitted injected documentation\n"), 0644)
				replacement, e := hex.DecodeString(git(t, r.Worktree, "hash-object", "-w", injection))
				must(t, e)
				corrupt := bytes.Replace(raw, original, replacement, 1)
				if bytes.Equal(raw, corrupt) {
					t.Fatal("corruption did not replace the tree's blob reference")
				}
				// Retain the original tree's storage key with different valid tree
				// bytes. Both leaf blobs are valid; only the intermediate hash fails.
				var compressed bytes.Buffer
				zw := zlib.NewWriter(&compressed)
				_, e = fmt.Fprintf(zw, "tree %d%c", len(corrupt), 0)
				must(t, e)
				_, e = zw.Write(corrupt)
				must(t, e)
				must(t, zw.Close())
				objectPath := filepath.Join(r.Worktree, ".git", "objects", tree[:2], tree[2:])
				must(t, os.Chmod(objectPath, 0600))
				put(t, objectPath, compressed.Bytes(), 0600)
				_, _, e = readDocuments(context.Background(), r)
				if e == nil || !strings.Contains(e.Error(), "Git object hash mismatch") {
					t.Fatalf("wanted intermediate tree hash rejection, got %v", e)
				}
				noOutput(t, r)
			})
		}
	}
}

func TestD09IndexFlagsCannotHideTrackedChanges(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		for _, change := range []string{"content", "missing", "unselected"} {
			t.Run(flag+"/"+change, func(t *testing.T) {
				r := fixture(t)
				name := "README.md"
				if change == "unselected" {
					name = ".gitignore"
				}
				git(t, r.Worktree, "update-index", flag, name)
				indexPath := filepath.Join(r.Worktree, ".git", "index")
				before, e := os.ReadFile(indexPath)
				must(t, e)
				// Flags alone are supported, including sparse-checkout settings.
				git(t, r.Worktree, "config", "core.sparseCheckout", "true")
				git(t, r.Worktree, "config", "core.splitIndex", "true")
				_, _, e = readDocuments(context.Background(), r)
				must(t, e)
				path := filepath.Join(r.Worktree, name)
				if change == "missing" {
					must(t, os.Remove(path))
				} else {
					put(t, path, []byte("hidden tracked change\n"), 0644)
				}
				noOutput(t, r)
				after, e := os.ReadFile(indexPath)
				must(t, e)
				if !bytes.Equal(before, after) {
					t.Fatal("validation modified the caller's index")
				}
				shared, e := filepath.Glob(filepath.Join(r.Worktree, ".git", "sharedindex.*"))
				must(t, e)
				if len(shared) != 0 {
					t.Fatal("validation wrote shared index files in the caller's repository")
				}
			})
		}
	}
}
