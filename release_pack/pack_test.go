package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func put(t *testing.T, p string, b []byte, mode os.FileMode) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0755))
	must(t, os.WriteFile(p, b, mode))
}
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func cloneRequest(t *testing.T, r Request) Request {
	t.Helper()
	b, e := json.Marshal(r)
	must(t, e)
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out Request
	must(t, d.Decode(&out))
	return out
}
func fixture(t *testing.T) Request {
	t.Helper()
	root := t.TempDir()
	physical, e := filepath.EvalSymlinks(root)
	must(t, e)
	root = physical
	work, in, out := filepath.Join(root, "source"), filepath.Join(root, "inbox"), filepath.Join(root, "outbox")
	for _, p := range []string{work, in, out} {
		must(t, os.Mkdir(p, 0755))
	}
	git(t, work, "init", "-q")
	put(t, filepath.Join(work, "README.md"), []byte("committed readme\n"), 0644)
	put(t, filepath.Join(work, "LICENSE"), []byte("license\n"), 0644)
	put(t, filepath.Join(work, "docs", "run"), []byte("#!/bin/sh\necho docs\n"), 0755)
	put(t, filepath.Join(work, "docs", "exported.txt"), []byte("export-ignored bytes\n"), 0644)
	put(t, filepath.Join(work, ".gitattributes"), []byte("docs/exported.txt export-ignore\n"), 0644)
	put(t, filepath.Join(work, ".gitignore"), []byte("docs/ignored.txt\n"), 0644)
	git(t, work, "add", ".")
	git(t, work, "commit", "-qm", "fixture")
	r := Request{Intent: Object{"schema_version": json.Number("1"), "release_id": "fixture-1.2.3", "repository": "github.com/example/cli", "source_commit": git(t, work, "rev-parse", "HEAD"), "target_branch": "main", "version": "1.2.3", "tag": "v1.2.3", "build_date": "2026-11-18T12:00:00Z", "binary_name": "cli"}, Worktree: work, Inbox: in, Outbox: out, Docs: []string{"README.md", "LICENSE", "docs"}}
	for idx, target := range defaultTargets {
		b := []byte(fmt.Sprintf("selected final %s/%s signed=%v\x00", target.GOOS, target.GOARCH, target.GOOS == "darwin"))
		sum := sha256.Sum256(b)
		name := target.GOOS + "/" + target.GOARCH + "/cli"
		put(t, filepath.Join(in, name), b, []os.FileMode{0600, 0644}[idx%2])
		d := descriptor(r.Intent, name, "binary", int64(len(b)), hex.EncodeToString(sum[:]), &target)
		if target.GOOS == "darwin" {
			d["unsigned_sha256"] = strings.Repeat("a", 64)
			d["signing"] = Object{"tool": "fixture", "status": "notarized"}
		}
		r.Files = append(r.Files, d)
	}
	put(t, filepath.Join(in, "signed-looking-unused"), []byte("must not select me"), 0755)
	put(t, filepath.Join(work, "docs", "ignored.txt"), []byte("ignored injection"), 0644)
	put(t, filepath.Join(work, "docs", "untracked.txt"), []byte("untracked injection"), 0644)
	return r
}
func output(t *testing.T, b []byte) Object {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v Object
	must(t, d.Decode(&v))
	return v["output"].(map[string]any)
}
func resultFiles(t *testing.T, b []byte) []Object {
	t.Helper()
	var out []Object
	for _, v := range output(t, b)["files"].([]any) {
		out = append(out, v.(map[string]any))
	}
	return out
}
func success(t *testing.T, r Request) []byte {
	t.Helper()
	b, e := pack(context.Background(), r)
	must(t, e)
	return b
}
func noOutput(t *testing.T, r Request) {
	t.Helper()
	b, e := pack(context.Background(), r)
	if e == nil || len(b) != 0 {
		t.Fatalf("wanted failure, got %d output bytes and error %v", len(b), e)
	}
	entries, err := os.ReadDir(r.Outbox)
	must(t, err)
	if len(entries) != 0 {
		t.Fatalf("failure promoted files: %v (%v)", entries, e)
	}
}

type extracted struct {
	h tar.Header
	b []byte
}

func unpack(t *testing.T, b []byte) map[string]extracted {
	t.Helper()
	gz, e := gzip.NewReader(bytes.NewReader(b))
	must(t, e)
	if !gz.ModTime.IsZero() || gz.Name != "" || gz.Comment != "" || len(gz.Extra) != 0 || gz.OS != 255 {
		t.Fatalf("unnormalized gzip header: %+v", gz.Header)
	}
	tr := tar.NewReader(gz)
	out := map[string]extracted{}
	dest := t.TempDir()
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		must(t, e)
		data, e := io.ReadAll(tr)
		must(t, e)
		if !safeName(strings.TrimSuffix(h.Name, "/")) {
			t.Fatalf("unsafe archive path %s", h.Name)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || h.ModTime.Unix() != 0 || !h.AccessTime.IsZero() || !h.ChangeTime.IsZero() {
			t.Fatalf("unnormalized tar: %+v", h)
		}
		if _, ok := out[h.Name]; ok {
			t.Fatal("duplicate tar member")
		}
		out[h.Name] = extracted{*h, data}
		p := filepath.Join(dest, h.Name)
		if h.Typeflag == tar.TypeDir {
			must(t, os.MkdirAll(p, os.FileMode(h.Mode)))
		} else {
			if h.Typeflag != tar.TypeReg {
				t.Fatal("unexpected archive type")
			}
			put(t, p, data, os.FileMode(h.Mode))
			actual, e := os.ReadFile(p)
			must(t, e)
			if !bytes.Equal(actual, data) {
				t.Fatal("extracted bytes differ")
			}
			info, e := os.Stat(p)
			must(t, e)
			if info.Mode().Perm() != os.FileMode(h.Mode) {
				t.Fatal("extracted mode differs")
			}
		}
	}
	must(t, gz.Close())
	return out
}
func TestK01K02K04K05Archives(t *testing.T) {
	r := fixture(t)
	b := success(t, r)
	files := resultFiles(t, b)
	if len(files) != 5 {
		t.Fatal(files)
	}
	names := []string{}
	var lines []string
	for _, d := range files {
		data, e := os.ReadFile(filepath.Join(r.Outbox, str(d, "name")))
		must(t, e)
		sum := sha256.Sum256(data)
		size, _ := number(d, "size")
		if int64(len(data)) != size || hex.EncodeToString(sum[:]) != str(d, "sha256") {
			t.Fatal("final-byte descriptor mismatch")
		}
		if str(d, "kind") == "checksums" {
			continue
		}
		names = append(names, str(d, "name"))
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+str(d, "name"))
		target := Target{str(d, "goos"), str(d, "goarch")}
		if str(d, "name") != archiveName(r.Intent, target) {
			t.Fatal("archive name")
		}
		entries := unpack(t, data)
		want, e := os.ReadFile(filepath.Join(r.Inbox, target.GOOS, target.GOARCH, "cli"))
		must(t, e)
		if !bytes.Equal(entries["cli"].b, want) || entries["cli"].h.Mode != 0755 {
			t.Fatal("selected binary bytes/mode")
		}
		expected := []string{"cli", "README.md", "LICENSE", "docs/", "docs/run", "docs/exported.txt"}
		if len(entries) != len(expected) {
			t.Fatal(entries)
		}
		for _, n := range expected {
			if _, ok := entries[n]; !ok {
				t.Fatal("missing", n)
			}
		}
		if entries["docs/run"].h.Mode != 0755 || string(entries["docs/exported.txt"].b) != "export-ignored bytes\n" {
			t.Fatal("committed docs bytes/mode")
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Fatal(names)
	}
	if !reflect.DeepEqual(names, []string{"cli_1.2.3_Darwin_arm64.tar.gz", "cli_1.2.3_Darwin_x86_64.tar.gz", "cli_1.2.3_Linux_arm64.tar.gz", "cli_1.2.3_Linux_x86_64.tar.gz"}) {
		t.Fatal(names)
	}
	checks, e := os.ReadFile(filepath.Join(r.Outbox, "checksums.txt"))
	must(t, e)
	if string(checks) != strings.Join(lines, "\n")+"\n" {
		t.Fatal("installer checksum syntax")
	}
	prov := output(t, b)["provenance"].(map[string]any)
	selected := prov["selected_inputs"].([]any)
	if selected[0].(map[string]any)["unsigned_sha256"] != strings.Repeat("a", 64) {
		t.Fatal("lost signing provenance")
	}
	// Reversed descriptors/selectors, changed input mtimes, different output root.
	old := r.Outbox
	r.Outbox = filepath.Join(filepath.Dir(old), "other-out")
	must(t, os.Mkdir(r.Outbox, 0755))
	for a, z := 0, len(r.Files)-1; a < z; a, z = a+1, z-1 {
		r.Files[a], r.Files[z] = r.Files[z], r.Files[a]
	}
	r.Docs = []string{"docs", "LICENSE", "README.md"}
	for _, d := range r.Files {
		must(t, os.Chtimes(filepath.Join(r.Inbox, str(d, "name")), time.Now(), time.Now()))
	}
	again := success(t, r)
	if !bytes.Equal(b, again) {
		t.Fatal("result ordering changed")
	}
	for _, d := range files {
		a, e := os.ReadFile(filepath.Join(old, str(d, "name")))
		must(t, e)
		b, e := os.ReadFile(filepath.Join(r.Outbox, str(d, "name")))
		must(t, e)
		if !bytes.Equal(a, b) {
			t.Fatal("archive changed")
		}
	}
}
func TestK01SubsetsAndNoDocs(t *testing.T) {
	r := fixture(t)
	r.Files = r.Files[:1]
	r.Targets = &[]Target{defaultTargets[0]}
	r.Docs = nil
	b := success(t, r)
	data, e := os.ReadFile(filepath.Join(r.Outbox, str(resultFiles(t, b)[0], "name")))
	must(t, e)
	entries := unpack(t, data)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
}
func TestK03TargetFailures(t *testing.T) {
	cases := map[string]func(*Request){"missing": func(r *Request) { r.Files = r.Files[:3] }, "duplicate": func(r *Request) { r.Files = append(r.Files, r.Files[0]) }, "duplicate target": func(r *Request) { r.Files[1]["goos"] = r.Files[0]["goos"]; r.Files[1]["goarch"] = r.Files[0]["goarch"] }, "unsupported": func(r *Request) { r.Files[0]["goos"] = "windows" }, "empty": func(r *Request) { r.Targets = &[]Target{} }, "duplicate required": func(r *Request) { r.Targets = &[]Target{defaultTargets[0], defaultTargets[0]} }, "unsupported required": func(r *Request) { r.Targets = &[]Target{{"windows", "amd64"}} }, "extra": func(r *Request) { r.Targets = &[]Target{defaultTargets[0]} }}
	base := fixture(t)
	for n, mutate := range cases {
		t.Run(n, func(t *testing.T) { r := cloneRequest(t, base); mutate(&r); noOutput(t, r) })
	}
}
func TestX03X04ReconcileAndNoClobber(t *testing.T) {
	r := fixture(t)
	b := success(t, r)
	if !bytes.Equal(b, success(t, r)) {
		t.Fatal("retry changed")
	}
	files := resultFiles(t, b)
	must(t, os.Remove(filepath.Join(r.Outbox, str(files[0], "name"))))
	if !bytes.Equal(b, success(t, r)) {
		t.Fatal("partial retry changed")
	}
	name := filepath.Join(r.Outbox, str(files[0], "name"))
	must(t, os.WriteFile(name, []byte("conflict"), 0644))
	if _, e := pack(context.Background(), r); e == nil {
		t.Fatal("overwrote conflict")
	}
	data, e := os.ReadFile(name)
	must(t, e)
	if string(data) != "conflict" {
		t.Fatal("clobbered")
	}
	must(t, os.Remove(name))
	must(t, os.Symlink(filepath.Join(r.Inbox, str(r.Files[0], "name")), name))
	if _, e := pack(context.Background(), r); e == nil {
		t.Fatal("accepted output symlink")
	}
	must(t, os.Remove(name))
	must(t, os.Mkdir(name, 0755))
	if _, e := pack(context.Background(), r); e == nil {
		t.Fatal("accepted output directory")
	}
}
func TestX01SnapshotAndX02WriteFailure(t *testing.T) {
	r := fixture(t)
	root, e := openRoot(r.Inbox)
	must(t, e)
	defer root.Close()
	snapshotDir := t.TempDir()
	snapshotDir, e = filepath.EvalSymlinks(snapshotDir)
	must(t, e)
	snapshotPath := filepath.Join(snapshotDir, "snapshot")
	stage, e := openRoot(snapshotDir)
	must(t, e)
	defer stage.Close()
	d := r.Files[0]
	must(t, snapshot(context.Background(), root, d, stage, "snapshot"))
	expected, e := os.ReadFile(snapshotPath)
	must(t, e)
	put(t, filepath.Join(r.Inbox, str(d, "name")), []byte("replaced"), 0644)
	var buf bytes.Buffer
	must(t, writeArchive(context.Background(), &buf, "cli", stage, "snapshot", nil))
	if !bytes.Equal(unpack(t, buf.Bytes())["cli"].b, expected) {
		t.Fatal("snapshot changed")
	}
	if e = writeArchive(context.Background(), failingWriter{}, "cli", stage, "snapshot", nil); e == nil {
		t.Fatal("ignored compression/write failure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if b, e := pack(ctx, r); e == nil || len(b) != 0 {
		t.Fatal("canceled success")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
