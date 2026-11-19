package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const projectionPolicy = "git-tree-blobs-v1"

type entry struct {
	PathBase64 string `json:"path_base64"`
	Mode       string `json:"mode"`
	ObjectID   string `json:"object_id"`
	Size       *int64 `json:"size"`
	name       string
	data       []byte
}
type projection struct {
	root    string
	commit  string
	tree    string
	format  string
	entries []entry
	sealed  bool
}

func git(ctx context.Context, root string, args ...string) ([]byte, error) {
	argv := append([]string{"--no-replace-objects", "-C", root, "-c", "core.fsmonitor=false", "-c", "core.fileMode=true"}, args...)
	return command(ctx, "git", argv, "", cleanEnv())
}
func getgit(ctx context.Context, root string, args ...string) (string, error) {
	b, e := git(ctx, root, args...)
	return strings.TrimSpace(string(b)), e
}
func prepareSource(ctx context.Context, r Request, root string) (*projection, error) {
	head, err := getgit(ctx, r.Worktree, "rev-parse", "--verify", "HEAD")
	if err != nil || head != r.Intent.SourceCommit {
		return nil, fmt.Errorf("source HEAD differs from intent source_commit")
	}
	// A private object projection alone must not hide staged modifications.
	if _, err = git(ctx, r.Worktree, "diff-index", "--cached", "--quiet", "--no-ext-diff", head, "--"); err != nil {
		return nil, fmt.Errorf("tracked index changes block release")
	}
	tree, err := getgit(ctx, r.Worktree, "rev-parse", head+"^{tree}")
	if err != nil {
		return nil, fmt.Errorf("source tree lookup failed")
	}
	format, err := getgit(ctx, r.Worktree, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, fmt.Errorf("source object format lookup failed")
	}
	if format != "sha1" && format != "sha256" {
		return nil, fmt.Errorf("unsupported Git object format")
	}
	for kind, oid := range map[string]string{"commit": head, "tree": tree} {
		object, e := git(ctx, r.Worktree, "cat-file", kind, oid)
		if e != nil || gitObjectID(format, kind, object) != oid {
			return nil, fmt.Errorf("source object hash verification failed")
		}
	}
	raw, err := git(ctx, r.Worktree, "ls-tree", "-r", "-t", "-z", "--full-tree", head)
	if err != nil {
		return nil, fmt.Errorf("source tree enumeration failed")
	}
	p := &projection{root: root, commit: head, tree: tree, format: format}
	seen := map[string]bool{}
	for _, row := range bytes.Split(raw, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		fields, name, ok := bytes.Cut(row, []byte{'\t'})
		meta := strings.Fields(string(fields))
		n := string(name)
		if !ok || len(meta) != 3 || !safeGitPath(n) || seen[n] {
			return nil, fmt.Errorf("unsafe source tree path")
		}
		seen[n] = true
		mode, kind, oid := meta[0], meta[1], meta[2]
		e := entry{PathBase64: base64.StdEncoding.EncodeToString(name), Mode: mode, ObjectID: oid, name: n}
		dest := filepath.Join(root, n)
		if kind == "tree" && mode == "040000" {
			object, objErr := git(ctx, r.Worktree, "cat-file", "tree", oid)
			if objErr != nil || gitObjectID(format, "tree", object) != oid {
				return nil, fmt.Errorf("source tree hash verification failed")
			}
			if err = verifyEntry(r.Worktree, e, false); err != nil {
				return nil, fmt.Errorf("tracked source directory differs from commit")
			}
			if err = os.Mkdir(dest, 0700); err != nil {
				return nil, fmt.Errorf("source directory materialization failed")
			}
		} else if kind == "blob" && (mode == "100644" || mode == "100755" || mode == "120000") {
			e.data, err = git(ctx, r.Worktree, "cat-file", "blob", oid)
			if err != nil {
				return nil, fmt.Errorf("source blob retrieval failed")
			}
			if gitObjectID(format, "blob", e.data) != oid {
				return nil, fmt.Errorf("source blob hash verification failed")
			}
			size := int64(len(e.data))
			e.Size = &size
			if bytes.HasPrefix(e.data, []byte("version https://git-lfs.github.com/spec/v1\n")) {
				return nil, fmt.Errorf("LFS recorded pointers are unsupported")
			}
			// Compare actual tracked files directly, even assume-unchanged/skip-worktree
			// files which ordinary status and diff may omit. Ignore checkout filters.
			if err = verifyEntry(r.Worktree, e, false); err != nil {
				return nil, fmt.Errorf("tracked source bytes or modes differ from commit")
			}
			if mode == "120000" {
				target := string(e.data)
				if filepath.IsAbs(target) || !inside(root, filepath.Join(filepath.Dir(dest), target)) || strings.ContainsRune(target, 0) {
					return nil, fmt.Errorf("source symlink escapes projection")
				}
				err = os.Symlink(target, dest)
			} else {
				perm := fs.FileMode(0444)
				if mode == "100755" {
					perm = 0555
				}
				err = os.WriteFile(dest, e.data, perm)
				if err == nil {
					err = os.Chmod(dest, perm)
				}
			}
			if err != nil {
				return nil, fmt.Errorf("source file materialization failed")
			}
		} else {
			return nil, fmt.Errorf("unsupported source object (including submodules)")
		}
		p.entries = append(p.entries, e)
	}
	// The repository's recorded origin binds provenance to the intent repository.
	origin, err := getgit(ctx, r.Worktree, "config", "--get", "remote.origin.url")
	if err != nil || canonicalOrigin(origin) != r.Intent.Repository {
		return nil, fmt.Errorf("source origin differs from intent repository")
	}
	for _, e := range p.entries {
		if e.Mode == "120000" {
			resolved, err := filepath.EvalSymlinks(filepath.Join(root, e.name))
			if err != nil || !inside(root, resolved) {
				return nil, fmt.Errorf("source symlink escapes projection or is unresolved")
			}
		}
	}
	sort.Slice(p.entries, func(i, j int) bool { return p.entries[i].name < p.entries[j].name })
	if err = p.verify(); err != nil {
		return nil, err
	}
	// Remove write permissions from every source directory, retaining execute.
	for i := len(p.entries) - 1; i >= 0; i-- {
		e := p.entries[i]
		if e.Mode == "040000" {
			if err = os.Chmod(filepath.Join(root, e.name), 0555); err != nil {
				return nil, fmt.Errorf("source protection failed")
			}
		}
	}
	if err = os.Chmod(root, 0555); err != nil {
		return nil, fmt.Errorf("source protection failed")
	}
	p.sealed = true
	if err = p.verify(); err != nil {
		return nil, err
	}
	return p, nil
}
func canonicalOrigin(s string) string {
	s = strings.TrimSuffix(s, ".git")
	for _, prefix := range []string{"https://github.com/", "ssh://git@github.com/", "git@github.com:"} {
		if strings.HasPrefix(s, prefix) {
			return "github.com/" + strings.TrimPrefix(s, prefix)
		}
	}
	return s
}
func inside(root, p string) bool {
	r, e := filepath.Rel(root, p)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
func safeGitPath(s string) bool {
	if s == "" || strings.ContainsRune(s, 0) || strings.Contains(s, "\\") || filepath.IsAbs(s) || filepath.Clean(s) != s {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." || part == "." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}
func verifyEntry(root string, e entry, protected bool) error {
	p := filepath.Join(root, e.name)
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if e.Mode == "040000" {
		if !info.IsDir() || protected && info.Mode().Perm() != 0555 {
			return fmt.Errorf("wrong directory")
		}
		return nil
	}
	var actual []byte
	if e.Mode == "120000" {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("wrong symlink")
		}
		s, err := os.Readlink(p)
		if err != nil {
			return err
		}
		actual = []byte(s)
	} else {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("wrong file")
		}
		executable := info.Mode().Perm()&0111 != 0
		if executable != (e.Mode == "100755") {
			return fmt.Errorf("wrong executable mode")
		}
		expected := fs.FileMode(0444)
		if e.Mode == "100755" {
			expected = 0555
		}
		if protected && info.Mode().Perm() != expected {
			return fmt.Errorf("source permission mode changed")
		}
		actual, err = os.ReadFile(p)
		if err != nil {
			return err
		}
	}
	if !bytes.Equal(e.data, actual) {
		return fmt.Errorf("changed blob")
	}
	return nil
}
func (p *projection) verify() error {
	if p.sealed {
		info, err := os.Lstat(p.root)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0555 {
			return fmt.Errorf("source projection root mode changed")
		}
	}
	expected := map[string]entry{}
	for _, e := range p.entries {
		expected[e.name] = e
	}
	count := 0
	err := filepath.WalkDir(p.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == p.root {
			return nil
		}
		name, err := filepath.Rel(p.root, path)
		if err != nil {
			return err
		}
		e, ok := expected[name]
		if !ok {
			return fmt.Errorf("unexpected path")
		}
		count++
		return verifyEntry(p.root, e, p.sealed || e.Mode != "040000")
	})
	if err != nil || count != len(expected) {
		return fmt.Errorf("source projection verification failed")
	}
	return nil
}
func (p *projection) inventory() []byte {
	b, _ := json.MarshalIndent(struct {
		SchemaVersion int     `json:"schema_version"`
		Commit        string  `json:"source_commit"`
		Tree          string  `json:"source_tree"`
		Format        string  `json:"object_format"`
		Policy        string  `json:"policy"`
		Entries       []entry `json:"entries"`
	}{1, p.commit, p.tree, p.format, projectionPolicy, p.entries}, "", "  ")
	return append(b, '\n')
}

// chmod permits removal after source directories have been made read-only.
func removeScratch(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e == nil && d.IsDir() {
			_ = os.Chmod(p, 0700)
		}
		return nil
	})
	_ = os.RemoveAll(root)
}

func gitObjectID(format, kind string, data []byte) string {
	var h hash.Hash = sha1.New()
	if format == "sha256" {
		h = sha256.New()
	}
	fmt.Fprintf(h, "%s %d%c", kind, len(data), 0)
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
