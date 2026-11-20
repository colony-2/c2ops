package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestC01C04Validation(t *testing.T) {
	base := fixture(t)
	cases := map[string]func(*Request){
		"schema": func(r *Request) { r.Intent["schema_version"] = json.Number("2") }, "string schema": func(r *Request) { r.Intent["schema_version"] = "1" }, "missing identity": func(r *Request) { delete(r.Intent, "release_id") }, "wrong identity type": func(r *Request) { r.Intent["version"] = true }, "commit": func(r *Request) { r.Intent["source_commit"] = "main" }, "tag": func(r *Request) { r.Intent["tag"] = "1.2.3" }, "version prefix": func(r *Request) { r.Intent["version"] = "v1.2.3" }, "date offset": func(r *Request) { r.Intent["build_date"] = "2026-11-18T12:00:00+00:00" }, "date": func(r *Request) { r.Intent["build_date"] = "yesterday" }, "repository": func(r *Request) { r.Intent["repository"] = "https://github.com/example/cli" }, "repo git suffix": func(r *Request) { r.Intent["repository"] = "github.com/example/cli.git" }, "binary name": func(r *Request) { r.Intent["binary_name"] = "../cli" }, "branch": func(r *Request) { r.Intent["target_branch"] = "main.lock" }, "binary whitespace": func(r *Request) { r.Intent["binary_name"] = "cli name" }, "kind": func(r *Request) { r.Files[0]["kind"] = "archive" }, "sha": func(r *Request) { r.Files[0]["sha256"] = strings.Repeat("A", 64) }, "size zero": func(r *Request) { r.Files[0]["size"] = json.Number("0") }, "size string": func(r *Request) { r.Files[0]["size"] = "1" }, "size fractional": func(r *Request) { r.Files[0]["size"] = json.Number("1.5") }, "unsigned sha": func(r *Request) { r.Files[0]["unsigned_sha256"] = "bad" }, "embedded identity": func(r *Request) { r.Files[0]["signing"] = Object{"source_commit": strings.Repeat("a", 40)} }, "embedded identity wrong type": func(r *Request) { r.Files[0]["signing"] = Object{"intent": Object{"version": Object{"nested": "bad"}}} }, "duplicate name": func(r *Request) { r.Files[1]["name"] = r.Files[0]["name"] },
	}
	for _, key := range identity {
		k := key
		cases["mismatched "+k] = func(r *Request) { r.Files[0][k] = "mismatch" }
		cases["missing "+k] = func(r *Request) { delete(r.Files[0], k) }
	}
	for _, name := range []string{"/absolute", "../cli", "a/../cli", "./cli", "a//cli", "a/./cli", "a/", "a\\cli", "a\x00cli", "a\ncli", ""} {
		n := name
		cases["unsafe "+name] = func(r *Request) { r.Files[0]["name"] = n }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) { r := cloneRequest(t, base); mutate(&r); noOutput(t, r) })
	}
	// A healthy control includes preserved arbitrary signing extensions.
	success(t, base)
}
func TestC01JSONAndC07Limits(t *testing.T) {
	r := fixture(t)
	b, e := json.Marshal(r)
	must(t, e)
	_, e = decode(bytes.NewReader(b))
	must(t, e)
	for n, s := range map[string]string{"duplicate": `{"intent":{},"intent":{}}`, "nested duplicate": `{"files":[{"name":"a","name":"b"}]}`, "trailing": string(b) + "{}", "malformed": "{", "null": "null", "null targets": strings.Replace(string(b), `"files":`, `"required_targets":null,"files":`, 1), "null docs": strings.Replace(string(b), `"docs":[`, `"docs":null,"other":[`, 1), "array": "[]", "wrong docs": strings.Replace(string(b), `"docs":[`, `"docs":42,"other":[`, 1), "invalid UTF8": "{\"bad\":\"\xff\"}"} {
		t.Run(n, func(t *testing.T) {
			if _, e := decode(strings.NewReader(s)); e == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	if _, e = decode(strings.NewReader(strings.Repeat(" ", jsonLimit+1))); e == nil || !strings.Contains(e.Error(), "128 KiB") {
		t.Fatal(e)
	}
	// Large but valid metadata: serialized output exceeds the bound once both the
	// intent and selected descriptor extensions are retained. No files promoted.
	r.Intent["annotation"] = strings.Repeat("x", 70000)
	r.Files[0]["signing"] = Object{"detail": strings.Repeat("z", 60000)}
	noOutput(t, r)
}
func TestC06SnapshotFailures(t *testing.T) {
	for _, kind := range []string{"missing", "truncated", "oversized", "tampered"} {
		t.Run(kind, func(t *testing.T) {
			r := fixture(t)
			p := filepath.Join(r.Inbox, str(r.Files[0], "name"))
			b, e := os.ReadFile(p)
			must(t, e)
			switch kind {
			case "missing":
				must(t, os.Remove(p))
			case "truncated":
				put(t, p, b[:len(b)-1], 0644)
			case "oversized":
				put(t, p, append(b, 'x'), 0644)
			case "tampered":
				b[0] ^= 1
				put(t, p, b, 0644)
			}
			noOutput(t, r)
		})
	}
}
func TestC04FilesystemConfinement(t *testing.T) {
	for _, kind := range []string{"file symlink outside", "file symlink inside", "component symlink", "directory input", "fifo input", "root symlink", "root alias component", "overlap source", "overlap inbox", "relative root"} {
		t.Run(kind, func(t *testing.T) {
			r := fixture(t)
			p := filepath.Join(r.Inbox, str(r.Files[0], "name"))
			outside := filepath.Join(t.TempDir(), "outside")
			put(t, outside, []byte("secret"), 0644)
			switch kind {
			case "file symlink outside", "file symlink inside":
				must(t, os.Remove(p))
				dest := outside
				if kind == "file symlink inside" {
					dest = "../../arm64/cli"
				}
				must(t, os.Symlink(dest, p))
			case "component symlink":
				dir := filepath.Join(r.Inbox, "linux", "amd64")
				must(t, os.Rename(dir, dir+"-real"))
				must(t, os.Symlink("amd64-real", dir))
			case "directory input":
				must(t, os.Remove(p))
				must(t, os.Mkdir(p, 0755))
			case "fifo input":
				must(t, os.Remove(p))
				makeFIFO(t, p)
			case "root symlink":
				alias := r.Inbox + "-alias"
				must(t, os.Symlink(r.Inbox, alias))
				r.Inbox = alias
			case "root alias component":
				alias := filepath.Join(filepath.Dir(r.Inbox), "alias")
				must(t, os.Symlink(r.Inbox, alias))
				r.Inbox = filepath.Join(alias, "linux")
			case "overlap source":
				r.Inbox = filepath.Join(r.Worktree, "nested")
			case "overlap inbox":
				r.Inbox = r.Outbox
			case "relative root":
				r.Inbox = "."
			}
			noOutput(t, r)
		})
	}
}
func TestCanceledRequest(t *testing.T) {
	r := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if b, e := pack(ctx, r); e == nil || len(b) > 0 {
		t.Fatal("canceled success")
	}
}

func TestSigningToolMetadataIsPreserved(t *testing.T) {
	r := fixture(t)
	r.Files[0]["signing"] = Object{"tool": Object{"name": "fixture-signer", "version": "4.2", "schema_version": json.Number("7"), "repository": "github.com/example/signer"}}
	b := success(t, r)
	inputs := output(t, b)["provenance"].(map[string]any)["selected_inputs"].([]any)
	found := false
	for _, v := range inputs {
		d := v.(map[string]any)
		if str(d, "name") == str(r.Files[0], "name") {
			found = true
			if !reflect.DeepEqual(d["signing"], r.Files[0]["signing"]) {
				t.Fatal("lost signing tool metadata")
			}
		}
	}
	if !found {
		t.Fatal("lost selected input")
	}
}
