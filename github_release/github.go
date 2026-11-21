package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type apiError struct{ Status int }

func (e *apiError) Error() string  { return fmt.Sprintf("GitHub HTTP status %d", e.Status) }
func status(err error, n int) bool { var e *apiError; return errors.As(err, &e) && e.Status == n }
func ambiguous(err error) bool {
	var e *apiError
	if errors.As(err, &e) {
		return e.Status >= 500 || e.Status == 408 || e.Status == 429
	}
	return err != nil
}

type gitObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}
type ref struct {
	Ref    string    `json:"ref"`
	Object gitObject `json:"object"`
}
type remoteRelease struct {
	ID      int64  `json:"id"`
	Tag     string `json:"tag_name"`
	Draft   bool   `json:"draft"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}
type remoteAsset struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	State      string `json:"state"`
	BrowserURL string `json:"browser_download_url"`
}
type evidence struct {
	Stage             string  `json:"stage"`
	TagCommit         string  `json:"tag_commit,omitempty"`
	ReleaseID         int64   `json:"release_id,omitempty"`
	AssetIDs          []int64 `json:"asset_ids,omitempty"`
	PublishedObserved bool    `json:"published_observed"`
}
type github struct {
	client      *http.Client
	api, upload string
	token       string
	testHTTP    bool // only constructed inside tests; no input/env endpoint overrides
	evidence    evidence
}

func newGitHub(token string) (*github, error) {
	transport, err := githubTransport(runtime.GOOS, os.Getenv("SSL_CERT_FILE"))
	if err != nil {
		return nil, err
	}
	g := &github{api: "https://api.github.com", upload: "https://uploads.github.com", token: token}
	g.client = &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("unsafe download redirect")
		}
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		return nil
	}}
	return g, nil
}
func (g *github) request(ctx context.Context, method, raw string, body io.Reader, auth bool, accept string) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !g.testHTTP) {
		return nil, errors.New("unsafe GitHub URL")
	}
	if auth && u.Host != "api.github.com" && u.Host != "uploads.github.com" && !g.testHTTP {
		return nil, errors.New("unauthorized GitHub credential destination")
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, body)
	if err != nil {
		return nil, errors.New("cannot construct GitHub request")
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "c2ops-github-release/1")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if auth {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	if body != nil {
		if f, ok := body.(*os.File); ok {
			if info, e := f.Stat(); e == nil {
				req.ContentLength = info.Size()
			}
		}
		if accept == "application/json" {
			req.Header.Set("Content-Type", "application/json")
		} else {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
	}
	// API JSON redirects could change repository identity. Only binary GETs may redirect.
	client := *g.client
	if accept != "application/octet-stream" || method != "GET" {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("GitHub transport interrupted")
	}
	return resp, nil
}
func (g *github) json(ctx context.Context, method, endpoint string, payload any, out any) error {
	var b []byte
	if payload != nil {
		b, _ = json.Marshal(payload)
	}
	attempts := 1
	if method == "GET" {
		attempts = 3
	}
	for n := 0; n < attempts; n++ {
		resp, err := g.request(ctx, method, g.api+endpoint, bytes.NewReader(b), true, "application/json")
		if err != nil {
			if n+1 < attempts {
				continue
			}
			return err
		}
		code := resp.StatusCode
		if code < 200 || code >= 300 {
			resp.Body.Close()
			if n+1 < attempts && (code >= 500 || code == 429) {
				delay := time.Duration(n+1) * 100 * time.Millisecond
				if s := resp.Header.Get("Retry-After"); s != "" {
					v, e := strconv.Atoi(s)
					if e != nil || v < 0 || v > 5 {
						return errors.New("GitHub retry delay exceeds invocation read budget")
					}
					delay = time.Duration(v) * time.Second
				}
				select {
				case <-ctx.Done():
					return errors.New("GitHub read canceled")
				case <-time.After(delay):
				}
				continue
			}
			return &apiError{code}
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024+1))
		resp.Body.Close()
		if err != nil || len(data) > 16*1024*1024 {
			return errors.New("GitHub response incomplete or oversized")
		}
		if out != nil && json.Unmarshal(data, out) != nil {
			return errors.New("invalid GitHub JSON response")
		}
		return nil
	}
	return errors.New("GitHub read attempts exhausted")
}
func repoPath(i Intent) string { return "/repos/" + strings.TrimPrefix(i.Repository, "github.com/") }
func (g *github) preflight(ctx context.Context, in Input) error {
	g.evidence.Stage = "destination validation"
	p := repoPath(in.Intent)
	var repo struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
		Archived bool   `json:"archived"`
		Disabled bool   `json:"disabled"`
	}
	if err := g.json(ctx, "GET", p, nil, &repo); err != nil {
		return err
	}
	if repo.FullName != strings.TrimPrefix(in.Intent.Repository, "github.com/") || repo.Private || repo.Archived || repo.Disabled {
		return errors.New("destination must be the canonical writable public repository")
	}
	var commit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := g.json(ctx, "GET", p+"/git/commits/"+in.Intent.SourceCommit, nil, &commit); err != nil {
		return err
	}
	if commit.SHA != in.Intent.SourceCommit || commit.Tree.SHA != in.Provenance["source_tree"] {
		return errors.New("destination commit/tree provenance mismatch")
	}
	return nil
}
func (g *github) tag(ctx context.Context, i Intent) (bool, error) {
	var r ref
	if err := g.json(ctx, "GET", repoPath(i)+"/git/ref/tags/"+url.PathEscape(i.Tag), nil, &r); err != nil {
		if status(err, 404) {
			return false, nil
		}
		return false, err
	}
	if r.Ref != "refs/tags/"+i.Tag {
		return false, errors.New("GitHub returned a different tag")
	}
	obj := r.Object
	seen := map[string]bool{}
	for n := 0; n < 16; n++ {
		if !commitRE.MatchString(obj.SHA) || seen[obj.SHA] {
			return false, errors.New("invalid or cyclic tag object")
		}
		seen[obj.SHA] = true
		if obj.Type == "commit" {
			g.evidence.TagCommit = obj.SHA
			if obj.SHA != i.SourceCommit {
				return false, errors.New("tag conflicts with source_commit")
			}
			return true, nil
		}
		if obj.Type != "tag" {
			return false, errors.New("tag does not resolve to a commit")
		}
		var t struct {
			SHA    string    `json:"sha"`
			Object gitObject `json:"object"`
		}
		if err := g.json(ctx, "GET", repoPath(i)+"/git/tags/"+obj.SHA, nil, &t); err != nil {
			return false, err
		}
		if t.SHA != obj.SHA {
			return false, errors.New("annotated tag identity mismatch")
		}
		obj = t.Object
	}
	return false, errors.New("tag peel depth exceeded")
}
func (g *github) ensureTag(ctx context.Context, i Intent) error {
	g.evidence.Stage = "tag reconciliation"
	for n := 0; n < 2; n++ {
		found, err := g.tag(ctx, i)
		if err != nil || found {
			return err
		}
		err = g.json(ctx, "POST", repoPath(i)+"/git/refs", map[string]string{"ref": "refs/tags/" + i.Tag, "sha": i.SourceCommit}, nil)
		// Reread even a successful response: observed state, not the POST body, is authoritative.
		found, readErr := g.tag(ctx, i)
		if readErr != nil {
			return readErr
		}
		if err != nil && !ambiguous(err) && !status(err, 422) && !status(err, 409) {
			return err
		}
		if found {
			return nil
		}
		if err != nil && !ambiguous(err) {
			return err
		}
	}
	return errors.New("tag creation remains unconfirmed")
}
func (g *github) findRelease(ctx context.Context, i Intent) (*remoteRelease, error) {
	var found *remoteRelease
	for page := 1; page <= 100; page++ {
		var batch []remoteRelease
		if err := g.json(ctx, "GET", fmt.Sprintf("%s/releases?per_page=100&page=%d", repoPath(i), page), nil, &batch); err != nil {
			return nil, err
		}
		for _, r := range batch {
			if r.Tag == i.Tag {
				if found != nil {
					return nil, errors.New("multiple releases for tag")
				}
				copy := r
				found = &copy
			}
		}
		if len(batch) < 100 {
			return found, nil
		}
	}
	return nil, errors.New("release pagination limit exceeded")
}
func (g *github) releaseIdentity(r *remoteRelease, in Input) error {
	if r == nil || r.ID <= 0 || r.Tag != in.Intent.Tag {
		return errors.New("release identity mismatch")
	}
	expected := "https://" + in.Intent.Repository + "/releases/tag/" + url.PathEscape(in.Intent.Tag)
	if r.HTMLURL != expected {
		return errors.New("release public URL mismatch")
	}
	if (r.Draft && r.Body != marker(in)) || (strings.Contains(r.Body, "c2ops.github.release/") && r.Body != marker(in)) {
		return errors.New("release intent marker mismatch")
	}
	g.evidence.ReleaseID = r.ID
	g.evidence.PublishedObserved = !r.Draft
	return nil
}
func (g *github) ensureRelease(ctx context.Context, in Input) (*remoteRelease, error) {
	g.evidence.Stage = "draft reconciliation"
	for n := 0; n < 2; n++ {
		r, err := g.findRelease(ctx, in.Intent)
		if err != nil {
			return nil, err
		}
		if r != nil {
			return r, g.releaseIdentity(r, in)
		}
		err = g.json(ctx, "POST", repoPath(in.Intent)+"/releases", map[string]any{"tag_name": in.Intent.Tag, "target_commitish": in.Intent.SourceCommit, "name": in.Intent.Tag, "body": marker(in), "draft": true}, nil)
		r, readErr := g.findRelease(ctx, in.Intent)
		if readErr != nil {
			return nil, readErr
		}
		if err != nil && !ambiguous(err) && !status(err, 422) {
			return nil, err
		}
		if r != nil {
			return r, g.releaseIdentity(r, in)
		}
		if err != nil && !ambiguous(err) {
			return nil, err
		}
	}
	return nil, errors.New("draft creation remains unconfirmed")
}
func (g *github) assets(ctx context.Context, in Input, r *remoteRelease) ([]remoteAsset, error) {
	var all []remoteAsset
	for page := 1; page <= 100; page++ {
		var batch []remoteAsset
		if err := g.json(ctx, "GET", fmt.Sprintf("%s/releases/%d/assets?per_page=100&page=%d", repoPath(in.Intent), r.ID, page), nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, nil
		}
	}
	return nil, errors.New("asset pagination limit exceeded")
}
func (g *github) verifyDownload(ctx context.Context, raw string, f File, auth bool) error {
	resp, err := g.request(ctx, "GET", raw, nil, auth, "application/octet-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &apiError{resp.StatusCode}
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(resp.Body, f.Size+1))
	if err != nil || n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return errors.New("downloaded asset bytes conflict with descriptor")
	}
	return nil
}
func (g *github) verifyAssets(ctx context.Context, in Input, r *remoteRelease, complete, public bool) (map[string]remoteAsset, error) {
	assets, err := g.assets(ctx, in, r)
	if err != nil {
		return nil, err
	}
	expected := map[string]File{}
	for _, f := range in.Files {
		expected[path.Base(f.Name)] = f
	}
	found := map[string]remoteAsset{}
	ids := map[int64]bool{}
	for _, a := range assets {
		f, ok := expected[a.Name]
		if !ok || found[a.Name].ID != 0 || ids[a.ID] || a.ID <= 0 || a.Size != f.Size || a.State != "uploaded" {
			return nil, errors.New("unexpected, duplicate or incomplete release asset")
		}
		ids[a.ID] = true
		found[a.Name] = a
		observed := false
		for _, id := range g.evidence.AssetIDs {
			if id == a.ID {
				observed = true
				break
			}
		}
		if !observed {
			g.evidence.AssetIDs = append(g.evidence.AssetIDs, a.ID)
		}
		// Do not trust URLs supplied by remote JSON to receive credentials.
		if err := g.verifyDownload(ctx, fmt.Sprintf("%s%s/releases/assets/%d", g.api, repoPath(in.Intent), a.ID), f, true); err != nil {
			return nil, err
		}
		expectedURL := "https://" + in.Intent.Repository + "/releases/download/" + url.PathEscape(in.Intent.Tag) + "/" + url.PathEscape(a.Name)
		if a.BrowserURL != expectedURL {
			return nil, errors.New("asset public URL mismatch")
		}
		if public {
			if err := g.verifyDownload(ctx, a.BrowserURL, f, false); err != nil {
				return nil, err
			}
		}
	}
	if complete && len(found) != len(expected) {
		return nil, errors.New("release assets incomplete")
	}
	return found, nil
}
func (g *github) uploadAsset(ctx context.Context, in Input, r *remoteRelease, f File, dir string) error {
	for n := 0; n < 2; n++ {
		// Every retry reads ownership/state and verifies the observed set before another write.
		current, readErr := g.findRelease(ctx, in.Intent)
		if readErr != nil {
			return readErr
		}
		if err := g.releaseIdentity(current, in); err != nil {
			return err
		}
		if current.ID != r.ID {
			return errors.New("release changed during upload")
		}
		if !current.Draft {
			return errors.New("release became public before uploads completed")
		}
		found, err := g.verifyAssets(ctx, in, r, false, false)
		if err != nil {
			return err
		}
		if found[path.Base(f.Name)].ID != 0 {
			return nil
		}
		src, err := os.Open(path.Join(dir, f.Name))
		if err != nil {
			return errors.New("cannot open staged upload")
		}
		raw := fmt.Sprintf("%s%s/releases/%d/assets?name=%s", g.upload, repoPath(in.Intent), r.ID, url.QueryEscape(path.Base(f.Name)))
		resp, writeErr := g.request(ctx, "POST", raw, src, true, "application/octet-stream")
		src.Close()
		if writeErr == nil {
			resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				writeErr = &apiError{resp.StatusCode}
			}
		}
		found, err = g.verifyAssets(ctx, in, r, false, false)
		if err != nil {
			return err
		}
		if writeErr != nil && !ambiguous(writeErr) && !status(writeErr, 422) {
			return writeErr
		}
		if found[path.Base(f.Name)].ID != 0 {
			return nil
		}
		if writeErr != nil && !ambiguous(writeErr) {
			return writeErr
		}
	}
	return errors.New("asset upload remains unconfirmed")
}
func (g *github) run(ctx context.Context, in Input, dir string) (Output, error) {
	var output Output
	if err := g.preflight(ctx, in); err != nil {
		return output, err
	}
	if err := g.ensureTag(ctx, in.Intent); err != nil {
		return output, err
	}
	r, err := g.ensureRelease(ctx, in)
	if err != nil {
		return output, err
	}
	g.evidence.Stage = "asset reconciliation"
	if _, err = g.verifyAssets(ctx, in, r, !r.Draft, false); err != nil {
		return output, err
	}
	if r.Draft {
		for _, f := range in.Files {
			if err = g.uploadAsset(ctx, in, r, f, dir); err != nil {
				return output, err
			}
		}
		// Reread ownership, tag and all bytes immediately before publication.
		ok, err := g.tag(ctx, in.Intent)
		if err != nil {
			return output, err
		}
		if !ok {
			return output, errors.New("tag disappeared before publication")
		}
		latest, err := g.findRelease(ctx, in.Intent)
		if err != nil {
			return output, err
		}
		if err = g.releaseIdentity(latest, in); err != nil {
			return output, err
		}
		if latest.ID != r.ID {
			return output, errors.New("release changed before publication")
		}
		r = latest
		if _, err = g.verifyAssets(ctx, in, r, true, false); err != nil {
			return output, err
		}
		if r.Draft {
			g.evidence.Stage = "publication reconciliation"
			writeErr := g.json(ctx, "PATCH", fmt.Sprintf("%s/releases/%d", repoPath(in.Intent), r.ID), map[string]bool{"draft": false}, nil)
			latest, err = g.findRelease(ctx, in.Intent)
			if err != nil {
				return output, err
			}
			if err = g.releaseIdentity(latest, in); err != nil {
				return output, err
			}
			if writeErr != nil && !ambiguous(writeErr) {
				return output, writeErr
			}
			if latest.ID != r.ID || latest.Draft {
				return output, errors.New("publication remains incomplete; retry will reread retained draft")
			}
			r = latest
		}
	}
	g.evidence.Stage = "public byte verification"
	ok, err := g.tag(ctx, in.Intent)
	if err != nil {
		return output, err
	}
	if !ok {
		return output, errors.New("release tag disappeared")
	}
	assets, err := g.verifyAssets(ctx, in, r, true, true)
	if err != nil {
		return output, err
	}
	// A concurrent change to public state must not authorize npm.
	latest, err := g.findRelease(ctx, in.Intent)
	if err != nil {
		return output, err
	}
	if err = g.releaseIdentity(latest, in); err != nil {
		return output, err
	}
	if latest.ID != r.ID || latest.Draft {
		return output, errors.New("release is no longer public")
	}
	output = Output{Intent: in.Intent, Files: in.Files, Provenance: in.Provenance, Release: PublicRelease{ID: r.ID, URL: r.HTMLURL, Tag: in.Intent.Tag, Commit: in.Intent.SourceCommit, Assets: []PublicAsset{}}}
	for _, f := range in.Files {
		a := assets[path.Base(f.Name)]
		output.Release.Assets = append(output.Release.Assets, PublicAsset{ID: a.ID, Name: a.Name, DownloadURL: a.BrowserURL, File: f})
	}
	g.evidence.Stage = "complete public release verified"
	return output, nil
}
