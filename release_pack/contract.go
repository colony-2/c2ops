package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const jsonLimit = 128 * 1024

type Object = map[string]any
type Target struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}
type Request struct {
	Intent   Object    `json:"intent"`
	Worktree string    `json:"worktree_path"`
	Inbox    string    `json:"artifact_inbox_path"`
	Outbox   string    `json:"artifact_outbox_path"`
	Files    []Object  `json:"files"`
	Targets  *[]Target `json:"required_targets,omitempty"`
	Docs     []string  `json:"docs,omitempty"`
}

var identity = []string{"schema_version", "release_id", "repository", "source_commit", "version", "tag"}
var defaultTargets = []Target{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}}
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
var repoPattern = regexp.MustCompile(`^github\.com/[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func str(m Object, key string) string { s, _ := m[key].(string); return s }
func safeName(s string) bool {
	if s == "" || !utf8.ValidString(s) || strings.Contains(s, `\`) || path.IsAbs(s) || path.Clean(s) != s {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	for _, p := range strings.Split(s, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return true
}

// Walk the token stream first: encoding/json otherwise silently accepts duplicate keys.
func uniqueJSON(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[s] = true
			if e = uniqueJSON(d); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e := uniqueJSON(d); e != nil {
				return e
			}
		}
	default:
		return errors.New("invalid JSON structure")
	}
	_, e = d.Token()
	return e
}
func decode(r io.Reader) (Request, error) {
	var req Request
	b, e := io.ReadAll(io.LimitReader(r, jsonLimit+1))
	if e != nil {
		return req, errors.New("cannot read request")
	}
	if len(b) > jsonLimit {
		return req, errors.New("request exceeds 128 KiB")
	}
	if !utf8.Valid(b) {
		return req, errors.New("request must be valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e = uniqueJSON(d); e != nil {
		return req, errors.New("invalid JSON or duplicate keys")
	}
	if _, e = d.Token(); e != io.EOF {
		return req, errors.New("trailing JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return req, errors.New("request must be an object")
	}
	for _, key := range []string{"required_targets", "docs"} {
		if raw, ok := fields[key]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return req, errors.New("optional arrays must not be null")
		}
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e = d.Decode(&req); e != nil {
		return req, errors.New("invalid request types")
	}
	if e = req.validate(); e != nil {
		return req, e
	}
	return req, nil
}
func (r Request) required() []Target {
	if r.Targets == nil {
		return defaultTargets
	}
	return *r.Targets
}
func supported(t Target) bool {
	return (t.GOOS == "linux" || t.GOOS == "darwin") && (t.GOARCH == "amd64" || t.GOARCH == "arm64")
}
func archiveName(i Object, t Target) string {
	osName := map[string]string{"linux": "Linux", "darwin": "Darwin"}[t.GOOS]
	arch := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[t.GOARCH]
	return str(i, "binary_name") + "_" + str(i, "version") + "_" + osName + "_" + arch + ".tar.gz"
}
func number(m Object, k string) (int64, bool) {
	n, ok := m[k].(json.Number)
	if !ok {
		return 0, false
	}
	v, e := n.Int64()
	return v, e == nil
}
func (r Request) validate() error {
	i := r.Intent
	if n, ok := number(i, "schema_version"); !ok || n != 1 {
		return errors.New("intent schema_version must be 1")
	}
	for _, k := range []string{"release_id", "repository", "source_commit", "target_branch", "version", "tag", "build_date", "binary_name"} {
		s := str(i, k)
		if strings.TrimSpace(s) == "" || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return fmt.Errorf("invalid intent %s", k)
		}
	}
	if !repoPattern.MatchString(str(i, "repository")) || strings.HasSuffix(str(i, "repository"), ".git") {
		return errors.New("repository must be canonical github.com/OWNER/REPO")
	}
	if !commitPattern.MatchString(str(i, "source_commit")) {
		return errors.New("source_commit must be a full immutable hash")
	}
	if !versionPattern.MatchString(str(i, "version")) || str(i, "tag") != "v"+str(i, "version") {
		return errors.New("invalid version/tag")
	}
	if !namePattern.MatchString(str(i, "binary_name")) || str(i, "binary_name") == "checksums.txt" {
		return errors.New("invalid binary_name")
	}
	date, e := time.Parse(time.RFC3339, str(i, "build_date"))
	if e != nil || !strings.HasSuffix(str(i, "build_date"), "Z") || date.Location() != time.UTC {
		return errors.New("build_date must be UTC RFC3339")
	}
	b := str(i, "target_branch")
	if !safeName(b) || strings.HasPrefix(b, "-") || strings.HasSuffix(b, ".") || strings.Contains(b, "..") || strings.ContainsAny(b, " ~^:?*[") || strings.Contains(b, "@{") || b == "@" {
		return errors.New("invalid target_branch")
	}
	for _, c := range strings.Split(b, "/") {
		if strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".lock") {
			return errors.New("invalid target_branch")
		}
	}
	if len(r.required()) == 0 {
		return errors.New("required_targets must not be empty")
	}
	targets := map[Target]bool{}
	for _, t := range r.required() {
		if !supported(t) || targets[t] {
			return errors.New("unsupported or duplicate required target")
		}
		targets[t] = true
	}
	names := map[string]bool{}
	got := map[Target]bool{}
	for _, f := range r.Files {
		for _, k := range identity {
			if !reflect.DeepEqual(f[k], i[k]) {
				return fmt.Errorf("descriptor identity mismatch: %s", k)
			}
		}
		if !safeName(str(f, "name")) || names[str(f, "name")] {
			return errors.New("unsafe or duplicate artifact name")
		}
		names[str(f, "name")] = true
		if str(f, "kind") != "binary" {
			return errors.New("selected files must be binary descriptors")
		}
		if n, ok := number(f, "size"); !ok || n <= 0 || n == 1<<63-1 {
			return errors.New("binary size must be a positive integer")
		}
		if !digestPattern.MatchString(str(f, "sha256")) {
			return errors.New("invalid binary sha256")
		}
		t := Target{str(f, "goos"), str(f, "goarch")}
		if !targets[t] || got[t] {
			return errors.New("extra, unsupported or duplicate binary target")
		}
		got[t] = true
		if e := extensions(f, i); e != nil {
			return e
		}
	}
	if len(got) != len(targets) {
		return errors.New("incomplete required target set")
	}
	seen := map[string]bool{}
	for _, s := range r.Docs {
		if !safeName(s) || seen[s] {
			return errors.New("unsafe or duplicate document selector")
		}
		for prev := range seen {
			if strings.HasPrefix(s, prev+"/") || strings.HasPrefix(prev, s+"/") {
				return errors.New("overlapping document selectors")
			}
		}
		seen[s] = true
	}
	return nil
}

// Tool/version metadata is opaque. An embedded intent, or an object declaring
// release_id/source_commit, identifies itself as release provenance and must
// agree with the selected release. Do not confuse a signer's version with it.
func extensions(v any, i Object) error { return checkExtensions(v, i, false) }
func checkExtensions(v any, i Object, releaseObject bool) error {
	switch m := v.(type) {
	case map[string]any:
		if _, ok := m["release_id"]; ok {
			releaseObject = true
		}
		if _, ok := m["source_commit"]; ok {
			releaseObject = true
		}
		for k, v := range m {
			if k == "unsigned_sha256" {
				s, ok := v.(string)
				if !ok || !digestPattern.MatchString(s) {
					return errors.New("invalid unsigned_sha256")
				}
			}
			if releaseObject {
				for _, key := range append(identity, "binary_name", "target_branch", "build_date") {
					if k == key && !reflect.DeepEqual(v, i[key]) {
						return errors.New("conflicting embedded release identity")
					}
				}
			}
			if e := checkExtensions(v, i, k == "intent"); e != nil {
				return e
			}
		}
	case []any:
		for _, x := range m {
			if e := checkExtensions(x, i, releaseObject); e != nil {
				return e
			}
		}
	}
	return nil
}
