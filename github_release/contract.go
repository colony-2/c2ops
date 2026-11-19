package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const jsonLimit = 128 * 1024

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
var repositoryRE = regexp.MustCompile(`^github\.com/[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
var commitRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

type Identity struct {
	SchemaVersion int    `json:"schema_version"`
	ReleaseID     string `json:"release_id"`
	Repository    string `json:"repository"`
	SourceCommit  string `json:"source_commit"`
	Version       string `json:"version"`
	Tag           string `json:"tag"`
}
type Intent struct {
	Identity
	TargetBranch string `json:"target_branch"`
	BuildDate    string `json:"build_date"`
	BinaryName   string `json:"binary_name"`
}
type File struct {
	Identity
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Kind   string `json:"kind"`
	GOOS   string `json:"goos,omitempty"`
	GOARCH string `json:"goarch,omitempty"`
}

func exactKeys(fields map[string]json.RawMessage, allowed string) error {
	names := map[string]bool{}
	for _, name := range strings.Fields(allowed) {
		names[name] = true
	}
	for name := range fields {
		if !names[name] {
			return errors.New("unknown or incorrectly cased JSON field")
		}
	}
	return nil
}

// A missing/null size must not silently become zero through Go's decoder.
func (f *File) UnmarshalJSON(b []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	if err := exactKeys(fields, "schema_version release_id repository source_commit version tag name size sha256 kind goos goarch"); err != nil {
		return err
	}
	if _, ok := fields["size"]; !ok {
		return errors.New("file size is required")
	}
	for _, v := range fields {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return errors.New("null descriptor fields are forbidden")
		}
	}
	type wire File
	var decoded wire
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&decoded); err != nil {
		return err
	}
	*f = File(decoded)
	return nil
}

type Input struct {
	Intent        Intent         `json:"intent"`
	Files         []File         `json:"files"`
	Provenance    map[string]any `json:"provenance"`
	WorkspaceRoot string         `json:"workspace_root"`
	InboxRoot     string         `json:"inbox_root"`
	OutboxRoot    string         `json:"outbox_root"`
}
type PublicAsset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DownloadURL string `json:"download_url"`
	File        File   `json:"file"`
}
type PublicRelease struct {
	ID     int64         `json:"id"`
	URL    string        `json:"url"`
	Tag    string        `json:"tag"`
	Commit string        `json:"commit"`
	Draft  bool          `json:"draft"`
	Assets []PublicAsset `json:"assets"`
}
type Output struct {
	Intent     Intent         `json:"intent"`
	Files      []File         `json:"files"`
	Provenance map[string]any `json:"provenance"`
	Release    PublicRelease  `json:"release"`
}

// Reject duplicate keys, including in nested provenance, rather than accepting
// different interpretations of one release identity.
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds limit")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return errors.New("duplicate JSON key")
			}
			seen[s] = true
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
		_, err = d.Token()
	case json.Delim('['):
		for d.More() {
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
		_, err = d.Token()
	}
	return err
}
func decodeInput(r io.Reader) (Input, error) {
	var in Input
	b, err := io.ReadAll(io.LimitReader(r, jsonLimit+1))
	if err != nil || len(b) > jsonLimit || !utf8.Valid(b) {
		return in, errors.New("input exceeds 128 KiB or cannot be read")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := uniqueJSON(d, 0); err != nil {
		return in, errors.New("malformed or ambiguous JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return in, errors.New("trailing JSON")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return in, errors.New("input must be an object")
	}
	if err := exactKeys(top, "intent files provenance workspace_root inbox_root outbox_root"); err != nil {
		return in, err
	}
	var intent map[string]json.RawMessage
	if err := json.Unmarshal(top["intent"], &intent); err != nil {
		return in, errors.New("intent must be an object")
	}
	if err := exactKeys(intent, "schema_version release_id repository source_commit version tag target_branch build_date binary_name"); err != nil {
		return in, err
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return in, errors.New("invalid input fields or types")
	}
	return in, nil
}
func normalizedName(s string) bool {
	if s == "" || strings.Contains(s, "\\") || path.Clean(s) != s || path.IsAbs(s) {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if !safeName.MatchString(part) || part == ".." || part == "." || strings.HasSuffix(part, ".") {
			return false
		}
	}
	return true
}
func validBranch(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") || s == "@" {
		return false
	}
	for _, r := range s {
		if r <= 32 || strings.ContainsRune(`~^:?*[\`, r) || r == 127 {
			return false
		}
	}
	for _, p := range strings.Split(s, "/") {
		if p == "" || strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".lock") {
			return false
		}
	}
	return true
}
func validate(in Input) error {
	i := in.Intent
	if len(i.Repository) > 160 || len(i.Version) > 128 || i.SchemaVersion != 1 || strings.TrimSpace(i.ReleaseID) == "" || len(i.ReleaseID) > 200 || !repositoryRE.MatchString(i.Repository) || strings.HasSuffix(i.Repository, ".git") || !commitRE.MatchString(i.SourceCommit) || !versionRE.MatchString(i.Version) || i.Tag != "v"+i.Version || !validBranch(i.Tag) || !validBranch(i.TargetBranch) || !safeName.MatchString(i.BinaryName) {
		return errors.New("invalid v1 release intent")
	}
	for _, r := range i.ReleaseID {
		if r < 32 || r == 127 {
			return errors.New("invalid release ID")
		}
	}
	date, err := time.Parse(time.RFC3339, i.BuildDate)
	if err != nil || !strings.HasSuffix(i.BuildDate, "Z") || date.Location() != time.UTC {
		return errors.New("build_date must be UTC RFC3339")
	}
	for _, root := range []string{in.WorkspaceRoot, in.InboxRoot, in.OutboxRoot} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return errors.New("explicit absolute normalized roots required")
		}
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("roots must be existing directories")
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil || resolved != root {
			return errors.New("symlink roots are forbidden")
		}
	}
	// Writable outputs must not overlap hydrated inputs or the recorded workspace.
	for _, r := range []string{in.InboxRoot, in.WorkspaceRoot} {
		if within(r, in.OutboxRoot) || within(in.OutboxRoot, r) {
			return errors.New("outbox overlaps inputs/workspace")
		}
	}
	if len(in.Files) < 2 || len(in.Files) > 100 {
		return errors.New("require archives and one checksums file; at most 100 assets")
	}
	names := map[string]bool{}
	bases := map[string]bool{}
	checksums, archives := 0, 0
	for _, f := range in.Files {
		base := path.Base(f.Name)
		if len(f.Name) > 1000 || f.Identity != i.Identity || !normalizedName(f.Name) || names[f.Name] || bases[base] || f.Size < 0 || f.Size > 1<<40 || !hashRE.MatchString(f.SHA256) {
			return errors.New("invalid, duplicate or inconsistent file descriptor")
		}
		names[f.Name] = true
		bases[base] = true
		switch f.Kind {
		case "archive":
			archives++
			if (f.GOOS != "linux" && f.GOOS != "darwin") || (f.GOARCH != "amd64" && f.GOARCH != "arm64") {
				return errors.New("archive target required")
			}
		case "checksums":
			checksums++
			if base != "checksums.txt" || f.GOOS != "" || f.GOARCH != "" || f.Size > jsonLimit {
				return errors.New("invalid checksum document")
			}
		case "document":
			if f.GOOS != "" || f.GOARCH != "" {
				return errors.New("document target forbidden")
			}
		default:
			return errors.New("only final archives, documents and checksums are accepted")
		}
	}
	if checksums != 1 || archives == 0 {
		return errors.New("require archives and exactly one checksums.txt")
	}
	p := in.Provenance
	// Compare identity as typed JSON, without converting unrelated provenance
	// numbers to float64 (which loses integers above 2^53).
	identity, _ := json.Marshal(i.Identity)
	var want map[string]json.RawMessage
	_ = json.Unmarshal(identity, &want)
	for k, v := range want {
		got, err := json.Marshal(p[k])
		if err != nil || !bytes.Equal(got, v) {
			return errors.New("provenance identity mismatch")
		}
	}
	tree, ok := p["source_tree"].(string)
	if !ok || !commitRE.MatchString(tree) || p["source_materialization"] != "git-tree-verified" {
		return errors.New("verified Git tree provenance required")
	}
	// All accepted remote URLs must equal these canonical URLs. Maximum signed
	// 64-bit IDs give a precise upper bound before any mutation, including the
	// descriptor copies in release.assets and the process envelope.
	preview := Output{Intent: i, Files: in.Files, Provenance: in.Provenance, Release: PublicRelease{ID: 9223372036854775807, URL: "https://" + i.Repository + "/releases/tag/" + url.PathEscape(i.Tag), Tag: i.Tag, Commit: i.SourceCommit}}
	for _, f := range in.Files {
		preview.Release.Assets = append(preview.Release.Assets, PublicAsset{ID: 9223372036854775807, Name: path.Base(f.Name), DownloadURL: "https://" + i.Repository + "/releases/download/" + url.PathEscape(i.Tag) + "/" + url.PathEscape(path.Base(f.Name)), File: f})
	}
	b, _ := json.Marshal(struct {
		Output Output `json:"output"`
	}{preview})
	if len(b)+1 > jsonLimit {
		return errors.New("result would exceed 128 KiB")
	}

	return nil
}
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))))
}

// Root confines path resolution even if an inbox ancestor is replaced during
// staging. Reject symlinks explicitly as well; committed descriptors name bytes.
func stage(in Input) (string, error) {
	tmp, err := os.MkdirTemp(in.OutboxRoot, ".github-release-")
	if err != nil {
		return "", errors.New("cannot create private asset staging")
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(tmp)
		}
	}()
	root, err := os.OpenRoot(in.InboxRoot)
	if err != nil {
		return "", errors.New("cannot open inbox")
	}
	defer root.Close()
	for _, f := range in.Files {
		parts := strings.Split(f.Name, "/")
		var before os.FileInfo
		for n := range parts {
			info, e := root.Lstat(strings.Join(parts[:n+1], "/"))
			if e != nil || info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("missing or symlink asset path")
			}
			if n < len(parts)-1 && !info.IsDir() || n == len(parts)-1 && !info.Mode().IsRegular() {
				return "", errors.New("asset path must contain directories and a regular file")
			}
			before = info
		}
		// Nonblocking open also prevents a FIFO replacement between Lstat and
		// open from hanging. The opened inode must still match the checked file.
		src, e := root.OpenFile(f.Name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if e != nil {
			return "", errors.New("cannot read asset")
		}
		info, e := src.Stat()
		if e != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() != f.Size {
			src.Close()
			return "", errors.New("asset size/type mismatch")
		}
		dstPath := filepath.Join(tmp, filepath.FromSlash(f.Name))
		if e = os.MkdirAll(filepath.Dir(dstPath), 0700); e != nil {
			src.Close()
			return "", errors.New("cannot stage asset")
		}
		dst, e := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			src.Close()
			return "", errors.New("cannot stage asset")
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, f.Size+1))
		ce := dst.Close()
		src.Close()
		if e != nil || ce != nil || n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
			return "", errors.New("asset hash/size mismatch")
		}
	}
	if err := verifyChecksums(in, tmp); err != nil {
		return "", err
	}
	success = true
	return tmp, nil
}
func verifyChecksums(in Input, dir string) error {
	expected := map[string]string{}
	var check File
	for _, f := range in.Files {
		if f.Kind == "checksums" {
			check = f
		} else {
			expected[path.Base(f.Name)] = f.SHA256
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, check.Name))
	if err != nil {
		return errors.New("cannot read checksums")
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if len(line) < 67 || line[64:66] != "  " {
			return errors.New("invalid checksum syntax")
		}
		name := line[66:]
		if seen[name] || expected[name] != line[:64] {
			return errors.New("checksum set/hash mismatch")
		}
		seen[name] = true
	}
	if len(seen) != len(expected) {
		return errors.New("incomplete checksums")
	}
	return nil
}
func marker(in Input) string {
	files := append([]File(nil), in.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	b, _ := json.Marshal(struct {
		Intent     Intent
		Files      []File
		Provenance map[string]any
	}{in.Intent, files, in.Provenance})
	return fmt.Sprintf("<!-- c2ops.github.release/v1 %x -->", sha256.Sum256(b))
}
func retain(in Input, tmp string) error {
	root, err := os.OpenRoot(in.OutboxRoot)
	if err != nil {
		return errors.New("cannot open outbox")
	}
	defer root.Close()
	for _, f := range in.Files {
		if err := root.MkdirAll(filepath.Dir(f.Name), 0700); err != nil {
			return errors.New("cannot retain assets")
		}
		src, err := os.Open(filepath.Join(tmp, f.Name))
		if err != nil {
			return errors.New("cannot read staged asset")
		}
		// O_EXCL prevents overwriting caller evidence and refuses existing symlinks.
		dst, err := root.OpenFile(f.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			src.Close()
			return errors.New("outbox artifact already exists or cannot be created")
		}
		_, err = io.Copy(dst, src)
		ce := dst.Close()
		src.Close()
		if err != nil || ce != nil {
			return errors.New("cannot retain assets")
		}
	}
	return nil
}
