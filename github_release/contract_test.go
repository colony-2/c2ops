package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestC01C04C06ContractFailuresBeforeEffects(t *testing.T) {
	cases := []struct {
		name   string
		change func(*fixture)
	}{
		{"schema", func(f *fixture) { f.in.Intent.SchemaVersion = 2 }},
		{"missing-id", func(f *fixture) { f.in.Intent.ReleaseID = "" }},
		{"blank-id", func(f *fixture) { f.in.Intent.ReleaseID = "   " }},
		{"repository", func(f *fixture) { f.in.Intent.Repository = "https://github.com/example/disposable" }},
		{"short-commit", func(f *fixture) { f.in.Intent.SourceCommit = "0123456" }},
		{"moving-ref", func(f *fixture) { f.in.Intent.SourceCommit = "main" }},
		{"v-version", func(f *fixture) { f.in.Intent.Version = "v1.2.3" }},
		{"tag", func(f *fixture) { f.in.Intent.Tag = "v2.0.0" }},
		{"branch", func(f *fixture) { f.in.Intent.TargetBranch = "main..other" }},
		{"date", func(f *fixture) { f.in.Intent.BuildDate = "2026-11-18T12:00:00+01:00" }},
		{"binary", func(f *fixture) { f.in.Intent.BinaryName = "../escape" }},
		{"root", func(f *fixture) { f.in.InboxRoot = "relative" }},
		{"overlap", func(f *fixture) { f.in.OutboxRoot = f.in.InboxRoot }},
		{"file-schema", func(f *fixture) { f.in.Files[0].SchemaVersion = 2 }},
		{"mixed-id", func(f *fixture) { f.in.Files[0].ReleaseID = "other" }},
		{"mixed-repository", func(f *fixture) { f.in.Files[0].Repository = "github.com/other/repo" }},
		{"mixed-commit", func(f *fixture) { f.in.Files[0].SourceCommit = tree }},
		{"mixed-version", func(f *fixture) { f.in.Files[0].Version = "3.0.0" }},
		{"mixed-tag", func(f *fixture) { f.in.Files[0].Tag = "v3.0.0" }},
		{"duplicate", func(f *fixture) { f.in.Files = append(f.in.Files, f.in.Files[0]) }},
		{"basename-collision", func(f *fixture) {
			v := f.in.Files[0]
			v.Name = "other/" + filepath.Base(v.Name)
			f.in.Files = append(f.in.Files, v)
		}},
		{"traversal", func(f *fixture) { f.in.Files[0].Name = "../escape" }},
		{"absolute", func(f *fixture) { f.in.Files[0].Name = "/escape" }},
		{"backslash", func(f *fixture) { f.in.Files[0].Name = `assets\escape` }},
		{"normalization", func(f *fixture) { f.in.Files[0].Name = "assets/a/../file" }},
		{"github-rewrite", func(f *fixture) { f.in.Files[0].Name = "assets/trailing." }},
		{"negative-size", func(f *fixture) { f.in.Files[0].Size = -1 }},
		{"uppercase-sha", func(f *fixture) { f.in.Files[0].SHA256 = strings.ToUpper(f.in.Files[0].SHA256) }},
		{"bad-sha", func(f *fixture) { f.in.Files[0].SHA256 = "missing" }},
		{"kind", func(f *fixture) { f.in.Files[0].Kind = "binary" }},
		{"target", func(f *fixture) { f.in.Files[0].GOOS = "windows" }},
		{"missing-checksums", func(f *fixture) { f.in.Files = f.in.Files[:1] }},
		{"missing-file", func(f *fixture) { _ = os.Remove(filepath.Join(f.in.InboxRoot, f.in.Files[0].Name)) }},
		{"truncated-file", func(f *fixture) { _ = os.Truncate(filepath.Join(f.in.InboxRoot, f.in.Files[0].Name), 3) }},
		{"tampered-file", func(f *fixture) {
			p := filepath.Join(f.in.InboxRoot, f.in.Files[0].Name)
			b, _ := os.ReadFile(p)
			b[0] ^= 1
			_ = os.WriteFile(p, b, 0600)
		}},
		{"file-symlink", func(f *fixture) {
			p := filepath.Join(f.in.InboxRoot, f.in.Files[0].Name)
			_ = os.Remove(p)
			_ = os.Symlink(filepath.Join(f.in.WorkspaceRoot, "escape"), p)
		}},
		{"directory-symlink", func(f *fixture) {
			p := filepath.Join(f.in.InboxRoot, "assets")
			_ = os.Rename(p, filepath.Join(f.in.WorkspaceRoot, "assets"))
			_ = os.Symlink(filepath.Join(f.in.WorkspaceRoot, "assets"), p)
		}},
		{"root-symlink", func(f *fixture) {
			p := filepath.Join(filepath.Dir(f.in.InboxRoot), "alias")
			_ = os.Symlink(f.in.InboxRoot, p)
			f.in.InboxRoot = p
		}},
		{"unverified-materialization", func(f *fixture) { f.in.Provenance["source_materialization"] = "checkout" }},
		{"missing-tree", func(f *fixture) { delete(f.in.Provenance, "source_tree") }},
		{"bad-checksum-content", func(f *fixture) {
			n := 1
			b := []byte(strings.Repeat("0", 64) + "  " + filepath.Base(f.in.Files[0].Name) + "\n")
			f.in.Files[n].Size = int64(len(b))
			f.in.Files[n].SHA256 = fmt.Sprintf("%x", sha256.Sum256(b))
			_ = os.WriteFile(filepath.Join(f.in.InboxRoot, f.in.Files[n].Name), b, 0600)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.change(f)
			out, err := f.invoke()
			if err == nil || out != "" || len(f.writes) > 0 || len(f.reads) > 0 {
				t.Fatalf("preflight failure permitted network: %v %q %v %v", err, out, f.reads, f.writes)
			}
		})
	}
	t.Run("healthy-positive-control", func(t *testing.T) {
		f := newFixture(t)
		if out, err := f.invoke(); err != nil || out == "" {
			t.Fatalf("healthy fixture fails %v", err)
		}
	})
}
func TestC01C07AmbiguousAndOversizedJSON(t *testing.T) {
	for _, input := range []string{`{}`, `{"intent":{},"intent":{}}`, `{"provenance":{"nested":{"x":1,"x":2}}}`, `{} {}`, `{"unknown":1}`, `{"files":[{"size":"not integer"}]}`, strings.Repeat(" ", jsonLimit+1), strings.Repeat("[", 66) + strings.Repeat("]", 66)} {
		var out bytes.Buffer
		if err := execute(context.Background(), strings.NewReader(input), &out, token, nil); err == nil || out.Len() != 0 {
			t.Fatal("bad JSON accepted")
		}
	}
	f := newFixture(t)
	for n := 0; n < 90; n++ {
		file := f.in.Files[0]
		file.Name = fmt.Sprintf("assets/%03d/", n) + strings.Repeat("long/", 90) + "archive.tar.gz"
		f.in.Files = append(f.in.Files, file)
	}
	out, err := f.invoke()
	if err == nil || out != "" || len(f.writes) != 0 || len(f.reads) != 0 {
		t.Fatal("oversized result did not fail before effects")
	}
}
func TestJSONKeysCannotAliasCaseInsensitively(t *testing.T) {
	for _, mode := range []string{"top", "intent", "file", "duplicate-alias"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			b, _ := json.Marshal(f.in)
			raw := string(b)
			switch mode {
			case "top":
				raw = strings.Replace(raw, `"intent":`, `"INTENT":`, 1)
			case "intent":
				raw = strings.Replace(raw, `"release_id":`, `"RELEASE_ID":`, 1)
			case "file":
				raw = strings.Replace(raw, `"size":`, `"SIZE":`, 1)
			case "duplicate-alias":
				raw = strings.Replace(raw, `"intent":`, `"Intent":{},"intent":`, 1)
			}
			var out bytes.Buffer
			if err := execute(context.Background(), strings.NewReader(raw), &out, token, f.g); err == nil || out.Len() > 0 || len(f.reads) > 0 || len(f.writes) > 0 {
				t.Fatal("aliased JSON field authorized effects")
			}
		})
	}
}

func TestDescriptorSizeMustBeExplicitEvenForEmptyBytes(t *testing.T) {
	f := newFixture(t)
	for _, raw := range []string{`{"kind":"archive"}`, `{"size":null}`, `{"size":0,"unknown":true}`, `{"size":0,"goos":null}`, `{"SIZE":0}`} {
		var file File
		if err := json.Unmarshal([]byte(raw), &file); err == nil {
			t.Fatal("missing, null or unknown descriptor fields accepted")
		}
	}
	var file File
	if err := json.Unmarshal([]byte(`{"size":0}`), &file); err != nil {
		t.Fatal("explicit zero should decode; semantic identity validation follows")
	}
	if out, err := f.invoke(); err != nil || out == "" {
		t.Fatal("healthy positive control failed", err)
	}
}

func TestC07RealisticOutputBound(t *testing.T) {
	f := newFixture(t)
	f.in.Provenance["build"] = map[string]any{"compiler": "go1.26.1", "targets": []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"}}
	if out, err := f.invoke(); err != nil || len(out) > jsonLimit || strings.Contains(out, "realistic archive fixture") {
		t.Fatalf("invalid descriptor output len=%d err=%v", len(out), err)
	}
}
func TestC09FailureDiagnosticsNeverContainRemoteSecrets(t *testing.T) {
	f := newFixture(t)
	f.denyTag = true
	out, err := f.invoke()
	if err == nil || strings.Contains(err.Error(), token) || out != "" {
		t.Fatal("secret/output violation")
	}
	_ = filepath.WalkDir(f.in.OutboxRoot, func(p string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, []byte(token)) {
				t.Error("secret in retained artifact")
			}
		}
		return nil
	})
}
func TestStagingResistsLaterInboxMutation(t *testing.T) {
	f := newFixture(t)
	tmp, err := stage(f.in)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	p := filepath.Join(f.in.InboxRoot, f.in.Files[0].Name)
	_ = os.WriteFile(p, []byte("later changed bytes"), 0600)
	if _, err = f.g.run(context.Background(), f.in, tmp); err != nil {
		t.Fatal("upload used mutable inbox", err)
	}
}

// Subprocess routing is confined to this test executable. The shipped program
// has no endpoint override. It exercises the real JSON/stdout/stderr contract.
func TestProcessHelper(t *testing.T) {
	raw := os.Getenv("C2OPS_TEST_PROCESS_SERVER")
	if raw == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil {
		os.Exit(98)
	}
	g := newGitHub(token)
	g.client.Transport = routedTransport{u, http.DefaultTransport}
	if err = execute(context.Background(), os.Stdin, os.Stdout, token, g); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
func TestC02C03SeparateProcessExplicitRootsReadOnlyCWD(t *testing.T) {
	f := newFixture(t)
	cwd := t.TempDir()
	if err := os.Chmod(cwd, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cwd, 0700) })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			cmd := exec.Command(exe, "-test.run=^TestProcessHelper$")
			cmd.Dir = cwd
			cmd.Env = append(os.Environ(), "C2OPS_TEST_PROCESS_SERVER="+f.server.URL)
			b, _ := json.Marshal(f.in)
			if bad {
				b = []byte(`{"intent":{}}`)
			}
			cmd.Stdin = bytes.NewReader(b)
			var out, diagnostic bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &diagnostic
			err = cmd.Run()
			if bad {
				if err == nil || out.Len() != 0 || diagnostic.Len() == 0 {
					t.Fatalf("bad failure contract: %v %q %q", err, out.String(), diagnostic.String())
				}
			} else {
				var v map[string]any
				if err != nil || diagnostic.Len() != 0 || json.Unmarshal(out.Bytes(), &v) != nil || v["output"] == nil {
					t.Fatalf("bad success contract: %v %q %q", err, out.String(), diagnostic.String())
				}
			}
		})
	}
}
func TestRedirectCredentialScope(t *testing.T) {
	f := newFixture(t)
	f.redirect = true
	if _, err := f.run(); err != nil {
		t.Fatal(err)
	}
	g := newGitHub(token)
	for _, raw := range []string{"http://api.github.com/x", "https://evil.example/x", "https://user:password@api.github.com/x"} {
		if _, err := g.request(context.Background(), "GET", raw, nil, true, "application/json"); err == nil {
			t.Fatal("unsafe credential URL accepted")
		}
	}
	req, _ := http.NewRequest("GET", "http://evil.example/x", nil)
	if g.client.CheckRedirect(req, []*http.Request{{}}) == nil {
		t.Fatal("insecure redirect accepted")
	}
}
func TestReadRetriesAreBoundedAndAPIRedirectsRejected(t *testing.T) {
	for _, code := range []int{500, 429, 403, 302} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "https://evil.example/")
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(code)
				fmt.Fprint(w, token)
			}))
			defer s.Close()
			u, _ := url.Parse(s.URL)
			g := newGitHub(token)
			g.client.Transport = routedTransport{u, http.DefaultTransport}
			err := g.json(context.Background(), "GET", "/test", nil, nil)
			expected := 1
			if code == 500 || code == 429 {
				expected = 3
			}
			if err == nil || calls != expected || strings.Contains(err.Error(), token) {
				t.Fatalf("retry/secret mismatch: calls=%d err=%v", calls, err)
			}
		})
	}
}
