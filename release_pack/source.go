package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type document struct {
	Name string
	Mode int64
	Blob string
	Data []byte
}
type gitSource struct {
	ctx context.Context
	dir string
}

func (g gitSource) run(args ...string) ([]byte, error) {
	return g.runIndex("", args...)
}

func (g gitSource) runIndex(index string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(g.ctx, "git", append([]string{"--no-replace-objects", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "core.ignoreStat=false", "-c", "core.splitIndex=false", "-c", "core.sparseCheckout=false", "-c", "index.sparse=false", "-c", "diff.external=", "-C", g.dir}, args...)...)
	// Put Git and credential/filter children in one cancellable process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	// Preserve Git configuration and credential helper/socket inputs, while removing
	// alternate repository/index/object selection. Never retain Git/helper stderr.
	blocked := map[string]bool{}
	for _, s := range strings.Fields("GIT_DIR GIT_COMMON_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE GIT_SHALLOW_FILE GIT_REPLACE_REF_BASE GIT_GRAFT_FILE GIT_CEILING_DIRECTORIES GIT_DISCOVERY_ACROSS_FILESYSTEM") {
		blocked[s] = true
	}
	for _, s := range os.Environ() {
		k, _, _ := strings.Cut(s, "=")
		if !blocked[k] {
			cmd.Env = append(cmd.Env, s)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	b, e := cmd.Output()
	if e != nil {
		return nil, errors.New("local Git source verification failed")
	}
	return b, nil
}
func objectHash(kind string, b []byte, length int) string {
	header := []byte(fmt.Sprintf("%s %d\x00", kind, len(b)))
	if length == 64 {
		h := sha256.New()
		h.Write(header)
		h.Write(b)
		return hex.EncodeToString(h.Sum(nil))
	}
	h := sha1.New()
	h.Write(header)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func (g gitSource) object(kind, oid string) ([]byte, error) {
	b, e := g.run("cat-file", kind, oid)
	if e != nil {
		return nil, e
	}
	if objectHash(kind, b, len(oid)) != oid {
		return nil, errors.New("Git object hash mismatch")
	}
	return b, nil
}

type treeFile struct{ Mode, Name, OID string }

// Follow links parsed from authenticated bytes, rather than asking recursive
// ls-tree to resolve unchecked child objects. Authenticate every intermediate
// tree, including trees outside selected docs used by the private index below.
func (g gitSource) treeFiles(oid, prefix string) ([]treeFile, error) {
	raw, e := g.object("tree", oid)
	if e != nil {
		return nil, e
	}
	var files []treeFile
	seen := map[string]bool{}
	for len(raw) > 0 {
		space := bytes.IndexByte(raw, ' ')
		if space < 1 {
			return nil, errors.New("invalid Git tree entry")
		}
		mode := string(raw[:space])
		raw = raw[space+1:]
		nul := bytes.IndexByte(raw, 0)
		width := len(oid) / 2
		if nul < 1 || len(raw) < nul+1+width {
			return nil, errors.New("invalid Git tree entry")
		}
		name := string(raw[:nul])
		child := hex.EncodeToString(raw[nul+1 : nul+1+width])
		raw = raw[nul+1+width:]
		if name == "." || name == ".." || strings.Contains(name, "/") || seen[name] {
			return nil, errors.New("invalid or duplicate Git tree name")
		}
		seen[name] = true
		name = prefix + name
		switch mode {
		case "40000":
			children, e := g.treeFiles(child, name+"/")
			if e != nil {
				return nil, e
			}
			files = append(files, children...)
		case "100644", "100755", "120000", "160000":
			files = append(files, treeFile{mode, name, child})
		default:
			return nil, errors.New("unsupported Git tree mode")
		}
	}
	return files, nil
}

func (g gitSource) checkWorktree(commit string) error {
	// A fresh index has neither caller flags nor cached stat information. Read
	// the recorded tree into it without checkout, then compare all tracked paths.
	// GIT_OPTIONAL_LOCKS=0 and disabled split/sparse indexes leave the caller's
	// index and shared-index files untouched, on both success and failure.
	dir, e := os.MkdirTemp("", "release-pack-index-")
	if e != nil {
		return errors.New("cannot create private source index")
	}
	defer os.RemoveAll(dir)
	index := filepath.Join(dir, "index")
	if _, e = g.runIndex(index, "read-tree", commit); e != nil {
		return e
	}
	if _, e = g.runIndex(index, "diff", "--quiet", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", "--"); e != nil {
		return errors.New("tracked source changes block packaging")
	}
	return nil
}

func readDocuments(ctx context.Context, r Request) ([]document, string, error) {
	g := gitSource{ctx, r.Worktree}
	commit := str(r.Intent, "source_commit")
	top, e := g.run("rev-parse", "--show-toplevel")
	if e != nil {
		return nil, "", e
	}
	if strings.TrimSuffix(string(top), "\n") != r.Worktree {
		return nil, "", errors.New("worktree_path must be the Git worktree root")
	}
	head, e := g.run("rev-parse", "--verify", "HEAD")
	if e != nil {
		return nil, "", e
	}
	if strings.TrimSpace(string(head)) != commit {
		return nil, "", errors.New("HEAD does not match source_commit")
	}
	if _, e = g.run("diff", "--cached", "--quiet", "--no-ext-diff", "--no-textconv", "HEAD", "--"); e != nil {
		return nil, "", errors.New("index changes block packaging")
	}
	cb, e := g.object("commit", commit)
	if e != nil {
		return nil, "", e
	}
	first, _, _ := strings.Cut(string(cb), "\n")
	tree := strings.TrimPrefix(first, "tree ")
	if !commitPattern.MatchString(tree) || len(tree) != len(commit) {
		return nil, "", errors.New("invalid commit tree")
	}
	listing, e := g.treeFiles(tree, "")
	if e != nil {
		return nil, "", e
	}
	if e = g.checkWorktree(commit); e != nil {
		return nil, "", e
	}
	found := map[string]bool{}
	var docs []document
	for _, entry := range listing {
		name := entry.Name
		selected := false
		for _, s := range r.Docs {
			if name == s || strings.HasPrefix(name, s+"/") {
				found[s] = true
				selected = true
			}
		}
		if !selected {
			continue
		}
		if !safeName(name) || (entry.Mode != "100644" && entry.Mode != "100755") {
			return nil, "", errors.New("selected document must be a recorded regular blob")
		}
		bin := str(r.Intent, "binary_name")
		if name == bin || strings.HasPrefix(name, bin+"/") {
			return nil, "", errors.New("document collides with root executable")
		}
		b, e := g.object("blob", entry.OID)
		if e != nil {
			return nil, "", e
		}
		if bytes.HasPrefix(b, []byte("version https://git-lfs.github.com/spec/v1\n")) {
			return nil, "", errors.New("LFS document content is unsupported")
		}
		mode, _ := strconv.ParseInt(entry.Mode[3:], 8, 64)
		docs = append(docs, document{name, mode, entry.OID, b})
	}
	for _, s := range r.Docs {
		if !found[s] {
			return nil, "", errors.New("selected document not found in source commit")
		}
	}
	sort.Slice(docs, func(a, b int) bool { return docs[a].Name < docs[b].Name })
	return docs, tree, nil
}
