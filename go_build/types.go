package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

type Intent struct {
	SchemaVersion int    `json:"schema_version"`
	ReleaseID     string `json:"release_id"`
	Repository    string `json:"repository"`
	SourceCommit  string `json:"source_commit"`
	TargetBranch  string `json:"target_branch"`
	Version       string `json:"version"`
	Tag           string `json:"tag"`
	BuildDate     string `json:"build_date"`
	BinaryName    string `json:"binary_name"`
}
type Target struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}
type Request struct {
	Intent       Intent            `json:"intent"`
	Worktree     string            `json:"worktree_path"`
	Inbox        string            `json:"artifact_inbox_path"`
	Outbox       string            `json:"artifact_outbox_path"`
	ModuleDir    string            `json:"module_dir,omitempty"`
	Entrypoint   string            `json:"entrypoint"`
	GoVersion    string            `json:"go_version,omitempty"`
	VersionFile  string            `json:"go_version_file,omitempty"`
	Targets      []Target          `json:"targets"`
	LinkerValues map[string]string `json:"linker_values,omitempty"`
	Tags         []string          `json:"build_tags,omitempty"`
}
type File struct {
	SchemaVersion int    `json:"schema_version"`
	ReleaseID     string `json:"release_id"`
	Repository    string `json:"repository"`
	SourceCommit  string `json:"source_commit"`
	Version       string `json:"version"`
	Tag           string `json:"tag"`
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	Kind          string `json:"kind"`
	GOOS          string `json:"goos,omitempty"`
	GOARCH        string `json:"goarch,omitempty"`
}
type Output struct {
	Intent     Intent     `json:"intent"`
	Files      []File     `json:"files"`
	Provenance Provenance `json:"provenance"`
}
type Provenance struct {
	SchemaVersion int        `json:"schema_version"`
	Source        SourceInfo `json:"source"`
	SDK           SDKInfo    `json:"sdk"`
	Build         BuildInfo  `json:"build"`
}
type SourceInfo struct {
	Commit          string `json:"commit"`
	Tree            string `json:"tree"`
	ObjectFormat    string `json:"object_format"`
	Policy          string `json:"policy"`
	PathCount       int    `json:"path_count"`
	InventoryName   string `json:"inventory_name"`
	InventorySHA256 string `json:"inventory_sha256"`
}
type SDKInfo struct {
	Requested       string  `json:"requested"`
	SelectionFile   *string `json:"selection_file"`
	Directive       *string `json:"directive"`
	ResolvedVersion string  `json:"resolved_version"`
	ActualVersion   string  `json:"actual_version"`
	Revision        string  `json:"nixpkgs_revision"`
	Attribute       string  `json:"attribute"`
	StorePath       string  `json:"store_path"`
	CompilerPath    string  `json:"compiler_path"`
	Host            string  `json:"host_system"`
}
type BuildInfo struct {
	ModuleDir          string            `json:"module_dir"`
	Entrypoint         string            `json:"entrypoint"`
	WorkspaceFile      *string           `json:"workspace_file"`
	Targets            []Target          `json:"targets"`
	LinkerSymbols      []string          `json:"linker_symbols"`
	LinkerValuesSHA256 string            `json:"linker_values_sha256"`
	Environment        map[string]string `json:"environment"`
	Flags              []string          `json:"flags"`
	AssemblyPolicy     string            `json:"assembly_policy"`
}

var allTargets = []Target{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}}
var repoRE = regexp.MustCompile(`^github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var commitRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var symbolRE = regexp.MustCompile(`^[A-Za-z0-9_./-]+\.[A-Za-z_][A-Za-z0-9_]*$`)
var productVersionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func textOK(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func relative(s string, dot bool) bool {
	return textOK(s) && !strings.Contains(s, "\\") && !path.IsAbs(s) && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../") && (dot || s != ".")
}
func (r *Request) validate() error {
	i := r.Intent
	if i.SchemaVersion != 1 || !textOK(i.ReleaseID) || len(i.ReleaseID) > 256 || !repoRE.MatchString(i.Repository) || !commitRE.MatchString(i.SourceCommit) || !textOK(i.TargetBranch) || !productVersionRE.MatchString(i.Version) || i.Tag != "v"+i.Version || !nameRE.MatchString(i.BinaryName) {
		return fmt.Errorf("invalid release intent")
	}
	for _, part := range strings.Split(i.Repository, "/")[1:] {
		if part == "." || part == ".." {
			return fmt.Errorf("invalid canonical repository")
		}
	}
	d, err := time.Parse(time.RFC3339, i.BuildDate)
	if err != nil || !strings.HasSuffix(i.BuildDate, "Z") || d.Location() != time.UTC {
		return fmt.Errorf("build_date must be UTC RFC3339")
	}
	for _, p := range []string{r.Worktree, r.Inbox, r.Outbox} {
		if !filepath.IsAbs(p) || !textOK(p) {
			return fmt.Errorf("explicit absolute workspace/inbox/outbox roots required")
		}
	}
	if r.ModuleDir == "" {
		r.ModuleDir = "."
	}
	if !relative(r.ModuleDir, true) {
		return fmt.Errorf("invalid module_dir")
	}
	if r.Entrypoint != "." && (!strings.HasPrefix(r.Entrypoint, "./") || !relative(strings.TrimPrefix(r.Entrypoint, "./"), false)) {
		return fmt.Errorf("entrypoint must be a relative local package")
	}
	if r.VersionFile != "" && (!relative(r.VersionFile, false) || (path.Base(r.VersionFile) != "go.mod" && path.Base(r.VersionFile) != "go.work")) {
		return fmt.Errorf("go_version_file must select go.mod or go.work")
	}
	if r.Targets == nil {
		r.Targets = append([]Target{}, allTargets...)
	}
	if len(r.Targets) == 0 || len(r.Targets) > 4 {
		return fmt.Errorf("one to four targets required")
	}
	seen := map[Target]bool{}
	for _, t := range r.Targets {
		valid := false
		for _, a := range allTargets {
			if t == a {
				valid = true
			}
		}
		if !valid || seen[t] {
			return fmt.Errorf("invalid or duplicate target")
		}
		seen[t] = true
	}
	sort.Slice(r.Targets, func(i, j int) bool {
		a, b := r.Targets[i], r.Targets[j]
		return a.GOOS+"/"+a.GOARCH < b.GOOS+"/"+b.GOARCH
	})
	if len(r.LinkerValues) > 64 {
		return fmt.Errorf("too many linker values")
	}
	for k, v := range r.LinkerValues {
		if !symbolRE.MatchString(k) || !utf8.ValidString(v) || strings.ContainsAny(v, "\x00\r\n") || len(v) > 4096 || (strings.Contains(v, `'`) && strings.Contains(v, `"`)) {
			return fmt.Errorf("invalid linker assignment (both quote styles are unsupported)")
		}
	}
	if len(r.Tags) > 64 {
		return fmt.Errorf("too many build tags")
	}
	for _, tag := range r.Tags {
		if len(tag) > 128 || !regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(tag) {
			return fmt.Errorf("invalid build tag")
		}
	}
	return nil
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func descriptor(i Intent, name, kind string, b []byte, t Target) File {
	return File{1, i.ReleaseID, i.Repository, i.SourceCommit, i.Version, i.Tag, name, int64(len(b)), digest(b), kind, t.GOOS, t.GOARCH}
}

// Canonical linker map: sorted keys, compact UTF-8 JSON, no HTML escaping.
func canonicalJSON(v map[string]string) []byte {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	quote := func(s string) {
		b.WriteByte('"')
		for _, r := range s {
			switch r {
			case '"', 92:
				b.WriteByte(92)
				b.WriteRune(r)
			default:
				if r < 32 {
					fmt.Fprintf(&b, "%cu%04x", 92, r)
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		quote(k)
		b.WriteByte(':')
		quote(v[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}
