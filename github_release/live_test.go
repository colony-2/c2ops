package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This is a test invocation gate, not an operation input or runtime service.
// It is deliberately independent of ambient GITHUB_TOKEN availability.
func liveGate(get func(string) string, pack bool) (bool, error) {
	if get("C2OPS_GITHUB_LIVE") == "" {
		return false, nil
	}
	if get("C2OPS_GITHUB_LIVE") != "authorized-disposable-publication" {
		return false, errors.New("explicit disposable authorization is required")
	}
	for _, key := range []string{"C2OPS_GITHUB_LIVE_INPUT", "C2OPS_GITHUB_LIVE_REPOSITORY", "C2OPS_GITHUB_LIVE_TAG", "GITHUB_TOKEN"} {
		if strings.TrimSpace(get(key)) == "" {
			return false, fmt.Errorf("required live test setting missing: %s", key)
		}
	}
	if pack && strings.TrimSpace(get("C2OPS_GITHUB_LIVE_PACK_CONFIRMATION")) == "" {
		return false, errors.New("G11 requires coordinator-recorded human confirmation of successful release.pack publication")
	}
	return true, nil
}
func TestLiveAuthorizationGate(t *testing.T) {
	values := map[string]string{"GITHUB_TOKEN": "ambient token"}
	get := func(k string) string { return values[k] }
	if enabled, err := liveGate(get, false); enabled || err != nil {
		t.Fatal("ambient credential authorized publication")
	}
	values["C2OPS_GITHUB_LIVE"] = "yes"
	if enabled, err := liveGate(get, false); enabled || err == nil {
		t.Fatal("vague authorization accepted")
	}
	values["C2OPS_GITHUB_LIVE"] = "authorized-disposable-publication"
	for _, key := range []string{"C2OPS_GITHUB_LIVE_INPUT", "C2OPS_GITHUB_LIVE_REPOSITORY", "C2OPS_GITHUB_LIVE_TAG"} {
		values[key] = "explicit fixture"
	}
	for _, key := range []string{"C2OPS_GITHUB_LIVE_INPUT", "C2OPS_GITHUB_LIVE_REPOSITORY", "C2OPS_GITHUB_LIVE_TAG", "GITHUB_TOKEN"} {
		saved := values[key]
		delete(values, key)
		if enabled, err := liveGate(get, false); enabled || err == nil {
			t.Fatalf("missing %s accepted", key)
		}
		values[key] = saved
	}
	if enabled, err := liveGate(get, true); enabled || err == nil {
		t.Fatal("G11 bypassed upstream confirmation")
	}
	values["C2OPS_GITHUB_LIVE_PACK_CONFIRMATION"] = "owner job/revision/selector/hosts and human confirmation"
	if enabled, err := liveGate(get, true); !enabled || err != nil {
		t.Fatal("fully explicit gate rejected", err)
	}
}

func liveInput(t *testing.T, pack bool) (context.Context, Input, *github) {
	t.Helper()
	enabled, err := liveGate(os.Getenv, pack)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("live GitHub acceptance UNVERIFIED: authorized disposable destination/credentials not configured")
	}
	f, err := os.Open(os.Getenv("C2OPS_GITHUB_LIVE_INPUT"))
	if err != nil {
		t.Fatal("cannot read authorized live input")
	}
	defer f.Close()
	in, err := decodeInput(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := validate(in); err != nil {
		t.Fatal(err)
	}
	if in.Intent.Repository != os.Getenv("C2OPS_GITHUB_LIVE_REPOSITORY") || in.Intent.Tag != os.Getenv("C2OPS_GITHUB_LIVE_TAG") {
		t.Fatal("input does not match authorized repository/tag")
	}
	// Reserve conspicuous disposable versions. Production release intents cannot
	// accidentally be run by this harness, even with a broad credential.
	if !strings.HasPrefix(in.Intent.Version, "0.0.0-c2ops-disposable.") {
		t.Fatal("live test requires a disposable version: 0.0.0-c2ops-disposable.UNIQUE")
	}
	staged, err := stage(in)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(staged)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	g, err := newGitHub(os.Getenv("GITHUB_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.preflight(ctx, in); err != nil {
		t.Fatal(err)
	}
	exists, err := g.tag(ctx, in.Intent)
	if err != nil || exists {
		t.Fatal("live test requires a fresh absent tag; existing remote state is preserved")
	}
	release, err := g.findRelease(ctx, in.Intent)
	if err != nil || release != nil {
		t.Fatal("live test requires a fresh absent release")
	}
	t.Log("Authorized disposable tag/releases/assets will be retained; no cleanup deletion is performed.")
	return ctx, in, g
}
func liveInvoke(t *testing.T, ctx context.Context, in Input, g *github) (Output, error) {
	t.Helper()
	in.OutboxRoot = t.TempDir() // Fresh evidence for every invocation.
	wire, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = execute(ctx, bytes.NewReader(wire), &out, os.Getenv("GITHUB_TOKEN"), g)
	if err != nil {
		if out.Len() != 0 {
			t.Fatal("failed live invocation returned success bytes")
		}
		return Output{}, err
	}
	var result struct {
		Output Output `json:"output"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Output.Release.Draft || result.Output.Release.ID <= 0 || result.Output.Release.Commit != in.Intent.SourceCommit || len(result.Output.Release.Assets) != len(in.Files) {
		t.Fatal("incomplete public live result")
	}
	for _, file := range in.Files {
		original, e := os.ReadFile(filepath.Join(in.InboxRoot, file.Name))
		if e != nil {
			t.Fatal(e)
		}
		retained, e := os.ReadFile(filepath.Join(in.OutboxRoot, file.Name))
		if e != nil || !bytes.Equal(original, retained) {
			t.Fatal("live retained bytes differ")
		}
	}
	return result.Output, nil
}
func TestG11LiveDisposablePublication(t *testing.T) {
	ctx, in, g := liveInput(t, true)
	result, err := liveInvoke(t, ctx, in, g)
	if err != nil {
		t.Fatal(err)
	}
	// Independent anonymous requests verify returned public URLs and every hash,
	// including checksums.txt, against the supplied confirmed pack artifacts.
	anonymous, err := newGitHub("")
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range result.Release.Assets {
		if err := anonymous.verifyDownload(ctx, asset.DownloadURL, asset.File, false); err != nil {
			t.Fatal(err)
		}
	}
	ok, err := g.tag(ctx, in.Intent)
	if err != nil || !ok {
		t.Fatal("live tag changed")
	}
}

type liveTransport struct {
	base             http.RoundTripper
	blockPublication bool
	writes           []string
	blocked          int
}

func (r *liveTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != "GET" {
		r.writes = append(r.writes, req.Method+" "+req.URL.Path)
	}
	if r.blockPublication && req.Method == "PATCH" && strings.Contains(req.URL.Path, "/releases/") {
		r.blocked++
		return nil, errors.New("controlled test interruption before publication")
	}
	return r.base.RoundTrip(req)
}
func TestG12LiveInterruptedPublicationRetry(t *testing.T) {
	ctx, in, g := liveInput(t, false)
	transport := &liveTransport{base: http.DefaultTransport, blockPublication: true}
	g.client.Transport = transport
	if _, err := liveInvoke(t, ctx, in, g); err == nil || transport.blocked != 1 {
		t.Fatal("publication interruption not exercised")
	}
	draft, err := g.findRelease(ctx, in.Intent)
	if err != nil || draft == nil || !draft.Draft {
		t.Fatal("interrupted draft not retained")
	}
	before, err := g.verifyAssets(ctx, in, draft, true, false)
	if err != nil {
		t.Fatal(err)
	}
	transport.blockPublication = false
	transport.writes = nil
	result, err := liveInvoke(t, ctx, in, g)
	if err != nil {
		t.Fatal(err)
	}
	if result.Release.ID != draft.ID {
		t.Fatal("retry replaced release")
	}
	after, err := g.verifyAssets(ctx, in, draft, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("retry replaced uploaded assets")
	}
	for _, write := range transport.writes {
		if !strings.HasPrefix(write, "PATCH ") {
			t.Fatal("retry unexpectedly mutated tag/assets")
		}
	}
	transport.writes = nil
	if _, err := liveInvoke(t, ctx, in, g); err != nil || len(transport.writes) != 0 {
		t.Fatal("published retry was not read-only", err)
	}
	// Exercise the byte verifier against a same-size conflicting expected digest
	// on the actual published release, without mutating remote state.
	conflict := in
	conflict.Files = append([]File(nil), in.Files...)
	for n := range conflict.Files {
		if conflict.Files[n].Kind == "archive" {
			file := &conflict.Files[n]
			file.SHA256 = strings.Repeat("0", 64)
			if file.SHA256 == in.Files[n].SHA256 {
				file.SHA256 = strings.Repeat("1", 64)
			}
			break
		}
	}
	if _, err := g.verifyAssets(ctx, conflict, draft, true, true); err == nil || len(transport.writes) != 0 {
		t.Fatal("conflicting published hash accepted or mutated")
	}
	// Verify every unchanged public URL again after the conflict probe.
	for _, asset := range result.Release.Assets {
		if path.Base(asset.File.Name) != asset.Name {
			t.Fatal("public artifact identity lost")
		}
		if err := g.verifyDownload(ctx, asset.DownloadURL, asset.File, false); err != nil {
			t.Fatal(err)
		}
	}
}
