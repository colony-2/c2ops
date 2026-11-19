package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const commit = "0123456789abcdef0123456789abcdef01234567"
const tree = "abcdef0123456789abcdef0123456789abcdef01"
const annotated = "aaaaaa0123456789abcdef0123456789abcdef01"
const token = "fixture-secret-never-retain"

type routedTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt routedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	u := *req.URL
	r.Host = u.Host
	u.Scheme = rt.target.Scheme
	u.Host = rt.target.Host
	r.URL = &u
	return rt.base.RoundTrip(r)
}

type fixture struct {
	mu                                                                         sync.Mutex
	t                                                                          *testing.T
	in                                                                         Input
	g                                                                          *github
	server                                                                     *httptest.Server
	tag                                                                        *ref
	release                                                                    *remoteRelease
	assets                                                                     []remoteAsset
	data                                                                       map[int64][]byte
	writes                                                                     []string
	reads                                                                      []string
	denyTag, denyRelease, denyUpload, denyPublish                              bool
	missingCommit, badRepo, badTree                                            bool
	tagAmbiguous, tagRace, releaseAmbiguous, uploadAmbiguous, publishAmbiguous bool
	tagMissingResponse, uploadDrop, publicFail, redirect, assetTruncate        bool
	tagReads                                                                   int
	uploadCount                                                                int
	releaseCount                                                               int
	publishCount                                                               int
	paginateAssets, paginateReleases                                           bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	w := filepath.Join(base, "workspace")
	i := filepath.Join(base, "inbox")
	o := filepath.Join(base, "outbox")
	for _, p := range []string{w, i, o} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	id := Identity{SchemaVersion: 1, ReleaseID: "cli-1.2.3", Repository: "github.com/example/disposable", SourceCommit: commit, Version: "1.2.3", Tag: "v1.2.3"}
	in := Input{Intent: Intent{Identity: id, TargetBranch: "main", BuildDate: "2026-11-18T12:00:00Z", BinaryName: "cli"}, WorkspaceRoot: w, InboxRoot: i, OutboxRoot: o}
	archive := []byte("a realistic archive fixture\x00\xff")
	digest := fmt.Sprintf("%x", sha256.Sum256(archive))
	name := "assets/cli_1.2.3_Linux_x86_64.tar.gz"
	checks := []byte(digest + "  " + path.Base(name) + "\n")
	in.Files = []File{{Identity: id, Name: name, Size: int64(len(archive)), SHA256: digest, Kind: "archive", GOOS: "linux", GOARCH: "amd64"}, {Identity: id, Name: "assets/checksums.txt", Size: int64(len(checks)), SHA256: fmt.Sprintf("%x", sha256.Sum256(checks)), Kind: "checksums"}}
	in.Provenance = map[string]any{"schema_version": float64(1), "release_id": id.ReleaseID, "repository": id.Repository, "source_commit": commit, "version": id.Version, "tag": id.Tag, "source_tree": tree, "source_materialization": "git-tree-verified", "pack": map[string]any{"format": "tar.gz"}}
	for n, b := range [][]byte{archive, checks} {
		p := filepath.Join(i, in.Files[n].Name)
		_ = os.MkdirAll(filepath.Dir(p), 0700)
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{t: t, in: in, data: map[int64][]byte{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	u, _ := url.Parse(f.server.URL)
	f.g = newGitHub(token)
	f.g.client.Transport = routedTransport{u, http.DefaultTransport}
	return f
}
func (f *fixture) publicURL(name string) string {
	return "https://" + f.in.Intent.Repository + "/releases/download/" + f.in.Intent.Tag + "/" + url.PathEscape(name)
}
func (f *fixture) setTag(kind, sha string) {
	f.tag = &ref{Ref: "refs/tags/" + f.in.Intent.Tag, Object: gitObject{Type: kind, SHA: sha}}
}
func (f *fixture) setRelease(draft bool) {
	f.release = &remoteRelease{ID: 42, Tag: f.in.Intent.Tag, Draft: draft, Body: marker(f.in), HTMLURL: "https://" + f.in.Intent.Repository + "/releases/tag/" + f.in.Intent.Tag}
}
func (f *fixture) addAsset(n int) {
	d := f.in.Files[n]
	b, err := os.ReadFile(filepath.Join(f.in.InboxRoot, d.Name))
	if err != nil {
		f.t.Fatal(err)
	}
	f.addBytes(path.Base(d.Name), b)
}
func (f *fixture) addBytes(name string, b []byte) {
	id := int64(100 + len(f.assets))
	f.assets = append(f.assets, remoteAsset{ID: id, Name: name, Size: int64(len(b)), State: "uploaded", BrowserURL: f.publicURL(name)})
	f.data[id] = append([]byte(nil), b...)
}
func (f *fixture) run() (Output, error) {
	tmp, err := stage(f.in)
	if err != nil {
		return Output{}, err
	}
	defer os.RemoveAll(tmp)
	return f.g.run(context.Background(), f.in, tmp)
}
func (f *fixture) invoke() (string, error) {
	b, _ := json.Marshal(f.in)
	var out bytes.Buffer
	err := execute(context.Background(), bytes.NewReader(b), &out, token, f.g)
	return out.String(), err
}
func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != "GET" {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
	} else {
		f.reads = append(f.reads, r.URL.Path)
	}
	if r.Host == "github.com" || r.Host == "objects.githubusercontent.com" {
		if r.Header.Get("Authorization") != "" {
			f.t.Error("credential leaked to public download")
		}
		if f.publicFail {
			w.WriteHeader(404)
			return
		}
		if f.redirect && r.Host == "github.com" {
			w.Header().Set("Location", "https://objects.githubusercontent.com/"+path.Base(r.URL.Path))
			w.WriteHeader(302)
			return
		}
		name, _ := url.PathUnescape(path.Base(r.URL.Path))
		for _, a := range f.assets {
			if a.Name == name {
				_, _ = w.Write(f.data[a.ID])
				return
			}
		}
		w.WriteHeader(404)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		f.t.Error("API credential missing")
	}
	p := r.URL.Path
	rp := "/repos/example/disposable"
	respond := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
	deny := func() { w.WriteHeader(403); fmt.Fprint(w, token+" secret-bearing denied body") }
	switch {
	case p == rp && r.Method == "GET":
		name := "example/disposable"
		if f.badRepo {
			name = "other/repository"
		}
		respond(map[string]any{"full_name": name, "private": false})
	case p == rp+"/git/commits/"+commit:
		if f.missingCommit {
			w.WriteHeader(404)
			return
		}
		tr := tree
		if f.badTree {
			tr = commit
		}
		respond(map[string]any{"sha": commit, "tree": map[string]string{"sha": tr}})
	case p == rp+"/git/ref/tags/v1.2.3":
		f.tagReads++
		if f.tag == nil {
			w.WriteHeader(404)
			return
		}
		respond(f.tag)
	case p == rp+"/git/tags/"+commit:
		respond(map[string]any{"sha": commit, "object": gitObject{Type: "tag", SHA: commit}})
	case p == rp+"/git/tags/"+annotated:
		respond(map[string]any{"sha": annotated, "object": gitObject{Type: "commit", SHA: commit}})
	case p == rp+"/git/refs" && r.Method == "POST":
		if f.denyTag {
			deny()
			return
		}
		var v map[string]string
		_ = json.NewDecoder(r.Body).Decode(&v)
		if v["sha"] != commit || v["ref"] != "refs/tags/v1.2.3" {
			f.t.Error("tag creation at wrong source")
		}
		if !f.tagMissingResponse {
			f.setTag("commit", commit)
		}
		if f.tagRace {
			w.WriteHeader(422)
			return
		}
		if f.tagAmbiguous {
			disconnect(w)
			return
		}
		respond(f.tag)
	case p == rp+"/releases" && r.Method == "GET":
		page := r.URL.Query().Get("page")
		if f.paginateReleases && page == "1" {
			v := make([]remoteRelease, 100)
			for n := range v {
				v[n] = remoteRelease{ID: int64(n + 200), Tag: fmt.Sprintf("other-%d", n)}
			}
			respond(v)
			return
		}
		if f.release == nil {
			respond([]remoteRelease{})
			return
		}
		respond([]remoteRelease{*f.release})
	case p == rp+"/releases" && r.Method == "POST":
		f.releaseCount++
		if f.denyRelease {
			deny()
			return
		}
		var v map[string]any
		_ = json.NewDecoder(r.Body).Decode(&v)
		if v["draft"] != true || v["target_commitish"] != commit || v["body"] != marker(f.in) {
			f.t.Error("unsafe draft creation")
		}
		f.setRelease(true)
		if f.releaseAmbiguous {
			disconnect(w)
			return
		}
		respond(f.release)
	case p == rp+"/releases/42/assets" && r.Method == "GET":
		if f.paginateAssets && r.URL.Query().Get("page") == "1" {
			v := make([]remoteAsset, 100)
			for n := range v {
				v[n] = remoteAsset{ID: int64(500 + n), Name: fmt.Sprintf("unexpected-%d", n)}
			}
			respond(v)
			return
		}
		respond(f.assets)
	case p == rp+"/releases/42/assets" && r.Method == "POST":
		f.uploadCount++
		if f.denyUpload {
			deny()
			return
		}
		if f.release == nil || !f.release.Draft {
			f.t.Error("upload attempted without draft")
		}
		b, _ := io.ReadAll(r.Body)
		if r.ContentLength != int64(len(b)) {
			f.t.Error("upload lacks explicit byte length")
		}
		if !f.uploadDrop {
			f.addBytes(r.URL.Query().Get("name"), b)
		}
		if f.uploadAmbiguous {
			disconnect(w)
			return
		}
		respond(map[string]bool{"ok": true})
	case strings.HasPrefix(p, rp+"/releases/assets/"):
		var id int64
		_, _ = fmt.Sscanf(strings.TrimPrefix(p, rp+"/releases/assets/"), "%d", &id)
		b, ok := f.data[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if f.assetTruncate {
			b = b[:len(b)/2]
		}
		_, _ = w.Write(b)
	case p == rp+"/releases/42" && r.Method == "PATCH":
		f.publishCount++
		if f.denyPublish {
			deny()
			return
		}
		if len(f.assets) != len(f.in.Files) {
			f.t.Error("published incomplete assets")
		}
		f.release.Draft = false
		if f.publishAmbiguous {
			disconnect(w)
			return
		}
		respond(f.release)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, p)
		w.WriteHeader(404)
	}
}
func disconnect(w http.ResponseWriter) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = c.Close()
	}
}

func TestG01DestinationPreflight(t *testing.T) {
	for _, name := range []string{"missing-commit", "repository", "tree", "provenance"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			switch name {
			case "missing-commit":
				f.missingCommit = true
			case "repository":
				f.badRepo = true
			case "tree":
				f.badTree = true
			case "provenance":
				f.in.Provenance["source_commit"] = tree
			}
			out, err := f.invoke()
			if err == nil || out != "" || len(f.writes) != 0 {
				t.Fatalf("invalid source authorized effects: %v %q %v", err, out, f.writes)
			}
		})
	}
}
func TestG02MissingTagAndCompletePublicResult(t *testing.T) {
	f := newFixture(t)
	f.redirect = true
	out, err := f.invoke()
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Output Output `json:"output"`
	}
	if err = json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatal(err)
	}
	r := envelope.Output.Release
	if r.Draft || r.ID != 42 || r.Commit != commit || len(r.Assets) != 2 {
		t.Fatalf("incomplete identity: %+v", r)
	}
	if f.tag.Object.SHA != commit || f.uploadCount != 2 || f.publishCount != 1 {
		t.Fatalf("incorrect effects: %v", f.writes)
	}
	for _, file := range f.in.Files {
		b, _ := os.ReadFile(filepath.Join(f.in.OutboxRoot, file.Name))
		if fmt.Sprintf("%x", sha256.Sum256(b)) != file.SHA256 {
			t.Fatal("retained bytes mismatch")
		}
	}
}
func TestG03MatchingTagsReused(t *testing.T) {
	for _, kind := range []string{"commit", "tag"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			sha := commit
			if kind == "tag" {
				sha = annotated
			}
			f.setTag(kind, sha)
			if _, err := f.run(); err != nil {
				t.Fatal(err)
			}
			for _, w := range f.writes {
				if strings.Contains(w, "/git/") {
					t.Fatal("matching tag mutated")
				}
			}
		})
	}
}
func TestG04ConflictingAndProtectedTags(t *testing.T) {
	for _, kind := range []string{"conflict", "protected", "blob", "cycle"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "conflict":
				f.setTag("commit", tree)
			case "protected":
				f.denyTag = true
			case "blob":
				f.setTag("blob", commit)
			case "cycle":
				f.setTag("tag", commit)
			}
			out, err := f.invoke()
			if err == nil || out != "" || f.release != nil {
				t.Fatalf("unsafe conflict result: %v %s", err, out)
			}
			for _, w := range f.writes {
				if strings.HasPrefix(w, "DELETE") || strings.HasPrefix(w, "PATCH") || strings.Contains(w, "/releases") {
					t.Fatal(w)
				}
			}
		})
	}
}
func TestG05TagRaceAndAmbiguousWrites(t *testing.T) {
	for _, mode := range []string{"race", "tag-lost-response", "draft-lost-response", "upload-lost-response", "publish-lost-response"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			switch mode {
			case "race":
				f.tagRace = true
			case "tag-lost-response":
				f.tagAmbiguous = true
			case "draft-lost-response":
				f.releaseAmbiguous = true
			case "upload-lost-response":
				f.uploadAmbiguous = true
			case "publish-lost-response":
				f.publishAmbiguous = true
			}
			if _, err := f.run(); err != nil {
				t.Fatal(err)
			}
			if f.tagReads < 2 || f.releaseCount != 1 || f.uploadCount != 2 || f.publishCount != 1 {
				t.Fatalf("writes duplicated: %v", f.writes)
			}
		})
	}
}
func TestG06PartialMatchingDraft(t *testing.T) {
	f := newFixture(t)
	f.setTag("commit", commit)
	f.setRelease(true)
	f.addAsset(0)
	if _, err := f.run(); err != nil {
		t.Fatal(err)
	}
	if f.releaseCount != 0 || f.uploadCount != 1 || f.publishCount != 1 {
		t.Fatal(f.writes)
	}
}
func TestG07MatchingPublishedReconciles(t *testing.T) {
	f := newFixture(t)
	f.setTag("commit", commit)
	f.setRelease(false)
	f.release.Body = "legacy public release"
	for n := range f.in.Files {
		f.addAsset(n)
	}
	f.paginateReleases = true
	if _, err := f.run(); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 {
		t.Fatal(f.writes)
	}
}
func TestG08SameSizeDifferentPublishedBytes(t *testing.T) {
	f := newFixture(t)
	f.setTag("commit", commit)
	f.setRelease(false)
	for n := range f.in.Files {
		f.addAsset(n)
	}
	f.data[f.assets[0].ID][0] ^= 1
	out, err := f.invoke()
	if err == nil || out != "" || len(f.writes) != 0 {
		t.Fatalf("conflicting bytes accepted %v %s", err, out)
	}
}
func TestG09G10IncompleteUploadAndEvidence(t *testing.T) {
	for _, mode := range []string{"denied", "not-created", "truncated", "public-inaccessible"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			switch mode {
			case "denied":
				f.denyUpload = true
			case "not-created":
				f.uploadDrop = true
			case "truncated":
				f.assetTruncate = true
			case "public-inaccessible":
				f.publicFail = true
			}
			out, err := f.invoke()
			if err == nil || out != "" || f.tag == nil || f.release == nil {
				t.Fatalf("bad failure evidence: %v %s", err, out)
			}
			if mode != "public-inaccessible" && (!f.release.Draft || f.publishCount != 0) {
				t.Fatal("published before verification")
			}
			b, e := os.ReadFile(filepath.Join(f.in.OutboxRoot, "error/github-release.json"))
			if e != nil || !bytes.Contains(b, []byte(`"release_id":42`)) {
				t.Fatalf("no evidence: %s %v", b, e)
			}
			if strings.Contains(err.Error(), token) || bytes.Contains(b, []byte(token)) {
				t.Fatal("secret leaked")
			}
			for _, w := range f.writes {
				if strings.HasPrefix(w, "DELETE") {
					t.Fatal("destructive rollback")
				}
			}
		})
	}
}
func TestG12FixtureRetryAfterInterruptedPublication(t *testing.T) {
	f := newFixture(t)
	f.denyPublish = true
	if _, err := f.run(); err == nil {
		t.Fatal("expected denied publish")
	}
	if f.release == nil || !f.release.Draft || len(f.assets) != 2 {
		t.Fatal("partial state lost")
	}
	uploads := f.uploadCount
	f.denyPublish = false
	if _, err := f.run(); err != nil {
		t.Fatal(err)
	}
	if f.uploadCount != uploads || f.releaseCount != 1 {
		t.Fatal("retry duplicated effects")
	}
	// This is the fake-server analogue; live G12 remains explicitly unverified.
}
func TestG13CredentialsAndPermissionFailures(t *testing.T) {
	f := newFixture(t)
	b, _ := json.Marshal(f.in)
	var out bytes.Buffer
	if err := execute(context.Background(), bytes.NewReader(b), &out, "", f.g); err == nil || out.Len() != 0 || len(f.reads) > 0 || len(f.writes) > 0 {
		t.Fatal("missing credential authorized network")
	}
	for _, where := range []string{"release", "upload", "publication"} {
		t.Run(where, func(t *testing.T) {
			f := newFixture(t)
			switch where {
			case "release":
				f.denyRelease = true
			case "upload":
				f.denyUpload = true
			case "publication":
				f.denyPublish = true
			}
			out, err := f.invoke()
			if err == nil || out != "" {
				t.Fatal("denial authorized npm")
			}
			if strings.Contains(err.Error(), token) {
				t.Fatal("API body leaked")
			}
		})
	}
}
func TestDraftConflictsAndIncompletePublished(t *testing.T) {
	for _, kind := range []string{"unmarked", "wrong-marker", "unexpected", "duplicate", "conflict", "missing-public", "pagination-extra"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.setTag("commit", commit)
			f.setRelease(true)
			switch kind {
			case "unmarked":
				f.release.Body = "other owner"
			case "wrong-marker":
				f.release.Body = "<!-- c2ops.github.release/v1 wrong -->"
			case "unexpected":
				f.addBytes("alien.txt", []byte("other"))
			case "duplicate":
				f.addAsset(0)
				f.addAsset(0)
			case "conflict":
				f.addAsset(0)
				f.data[f.assets[0].ID][0] ^= 1
			case "missing-public":
				f.release.Draft = false
			case "pagination-extra":
				f.paginateAssets = true
			}
			if out, err := f.invoke(); err == nil || out != "" {
				t.Fatal("conflicting state authorized success")
			}
			if len(f.writes) != 0 {
				t.Fatalf("conflicting state mutated %v", f.writes)
			}
		})
	}
}
