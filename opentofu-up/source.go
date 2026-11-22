package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

type snapshot struct {
	Dir, Root string
	Locks     map[string][]byte
}

func git(ctx context.Context, dir string, args ...string) runResult {
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.attributesFile=/dev/null", "-C", dir}
	return runProcess(ctx, command{Path: gitPath, Args: append(prefix, args...), Env: gitEnv(), Limit: 256 << 20})
}
func output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	r := git(ctx, dir, args...)
	if !successful(r) {
		return nil, errInvalid
	}
	return r.Data, nil
}
func clean(ctx context.Context, i Input) error {
	b, e := output(ctx, i.Worktree, "rev-parse", "--show-toplevel")
	if e != nil {
		return errInvalid
	}
	abs, e := filepath.EvalSymlinks(i.Worktree)
	if e != nil || strings.TrimSpace(string(b)) != abs {
		return errInvalid
	}
	b, e = output(ctx, i.Worktree, "rev-parse", "--show-object-format")
	if e != nil {
		return errInvalid
	}
	want := 40
	if strings.TrimSpace(string(b)) == "sha256" {
		want = 64
	}
	if len(i.Revision) != want {
		return errInvalid
	}
	b, e = output(ctx, i.Worktree, "rev-parse", "--verify", "HEAD")
	if e != nil || strings.TrimSpace(string(b)) != i.Revision {
		return errInvalid
	}
	b, e = output(ctx, i.Worktree, "status", "--porcelain=v1", "--untracked-files=no", "--ignore-submodules=none")
	if e != nil || len(b) != 0 {
		return errInvalid
	}
	return ctx.Err()
}
func poison(name string) bool {
	base := filepath.Base(name)
	// OpenTofu gives .tofu files precedence over .tf. Until that complete
	// configuration dialect is inspected, refuse it (including overrides) in
	// selected roots/modules, whether tracked, ignored, or untracked.
	if strings.HasSuffix(base, ".tofu") || strings.HasSuffix(base, ".tofu.json") || strings.HasSuffix(base, ".tofuvars") || strings.HasSuffix(base, ".tofuvars.json") {
		return true
	}
	return base == ".terraform" || strings.HasSuffix(base, ".tfbackend") || base == "terraform.tfstate" || base == "terraform.tfstate.backup" || base == "errored.tfstate" || strings.HasSuffix(base, ".tfstate") || strings.HasSuffix(base, ".tfstate.backup") || base == ".terraformrc" || base == "terraform.rc" || base == "tofu.rc" || base == ".tofurc" || base == "override.tf" || base == "override.tf.json" || strings.HasSuffix(base, "_override.tf") || strings.HasSuffix(base, "_override.tf.json")
}
func configFile(name string) bool {
	return strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json") || strings.HasSuffix(name, ".tfvars") || strings.HasSuffix(name, ".tfvars.json") || filepath.Base(name) == ".terraform.lock.hcl"
}
func within(root, p string) bool {
	r, e := filepath.Rel(root, p)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(os.PathSeparator))
}
func buildSnapshot(ctx context.Context, i Input, dest string) (snapshot, error) {
	s := snapshot{Dir: dest, Root: filepath.Join(dest, filepath.FromSlash(i.Root)), Locks: map[string][]byte{}}
	if e := clean(ctx, i); e != nil {
		return s, errInvalid
	}
	physical, e := filepath.EvalSymlinks(i.Worktree)
	if e != nil {
		return s, errInvalid
	}
	i.Worktree = physical
	// Ambient data/workspaces are refused before credentials or tool invocation.
	for _, k := range []string{"TF_DATA_DIR", "TF_WORKSPACE"} {
		if os.Getenv(k) != "" {
			return s, errInvalid
		}
	}
	b, e := output(ctx, i.Worktree, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if e != nil {
		return s, errInvalid
	}
	c, e := output(ctx, i.Worktree, "ls-files", "--others", "--exclude-standard", "-z")
	if e != nil {
		return s, errInvalid
	}
	untracked := strings.Split(string(append(b, c...)), "\x00")
	b, e = output(ctx, i.Worktree, "ls-tree", "-r", "-z", i.Revision)
	if e != nil {
		return s, errInvalid
	}
	totalBytes := 0
	for _, record := range bytes.Split(b, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return s, errInvalid
		}
		fields := strings.Fields(string(parts[0]))
		if len(fields) != 3 || fields[1] != "blob" {
			return s, errInvalid
		}
		name := string(parts[1])
		if !canonical(name, false) {
			return s, errInvalid
		}
		data, e := output(ctx, i.Worktree, "cat-file", "blob", fields[2])
		if e != nil {
			return s, errInvalid
		}
		totalBytes += len(data)
		if totalBytes > 256<<20 {
			return s, errInvalid
		}
		sourcePath := filepath.Join(i.Worktree, filepath.FromSlash(name))
		sourceParent, e := filepath.EvalSymlinks(filepath.Dir(sourcePath))
		if e != nil || !within(i.Worktree, sourceParent) {
			return s, errInvalid
		}
		st, e := os.Lstat(sourcePath)
		if e != nil {
			return s, errInvalid
		}
		if fields[0] == "120000" {
			link, e := os.Readlink(sourcePath)
			if e != nil || link != string(data) {
				return s, errInvalid
			}
		} else {
			if !st.Mode().IsRegular() {
				return s, errInvalid
			}
			current, e := os.ReadFile(sourcePath)
			if e != nil || !bytes.Equal(current, data) {
				return s, errInvalid
			}
		}
		p := filepath.Join(dest, filepath.FromSlash(name))
		if !within(dest, p) {
			return s, errInvalid
		}
		if e = makePrivate(filepath.Dir(p)); e != nil {
			return s, errInvalid
		}
		switch fields[0] {
		case "100644", "100755":
			mode := os.FileMode(0600)
			if fields[0] == "100755" {
				mode = 0700
			}
			if e = os.WriteFile(p, data, mode); e != nil {
				return s, errInvalid
			}
		case "120000":
			if e = os.Symlink(string(data), p); e != nil {
				return s, errInvalid
			}
		default:
			return s, errInvalid
		}
	}
	// All links are confined and resolved after the complete immutable tree copy.
	e = filepath.WalkDir(dest, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return errInvalid
		}
		if d.Type()&os.ModeSymlink != 0 {
			r, e := filepath.EvalSymlinks(p)
			if e != nil || !within(dest, r) {
				return errInvalid
			}
		}
		return ctx.Err()
	})
	if e != nil {
		return s, errInvalid
	}
	root, e := filepath.EvalSymlinks(s.Root)
	if e != nil || !within(dest, root) {
		return s, errInvalid
	}
	st, e := os.Stat(root)
	if e != nil || !st.IsDir() {
		return s, errInvalid
	}
	s.Root = root
	if e = clean(ctx, i); e != nil {
		return s, errInvalid
	}
	relevant := map[string]bool{}
	if e = inspectTree(ctx, s.Dir, s.Root, i.Backend, true, relevant); e != nil {
		return s, errInvalid
	}
	for copied := range relevant {
		rel, e := filepath.Rel(dest, copied)
		if e != nil {
			return s, errInvalid
		}
		original := filepath.Join(i.Worktree, rel)
		entries, e := os.ReadDir(original)
		if e != nil {
			return s, errInvalid
		}
		for _, entry := range entries {
			if poison(entry.Name()) {
				return s, errInvalid
			}
		}
		for _, name := range untracked {
			if filepath.Dir(filepath.Join(i.Worktree, filepath.FromSlash(name))) == original && configFile(name) {
				return s, errInvalid
			}
		}
	}
	lockPath := filepath.Join(s.Root, ".terraform.lock.hcl")
	lockBytes, e := os.ReadFile(lockPath)
	if e != nil || validateLock(lockBytes, lockPath) != nil {
		return s, errInvalid
	}
	s.Locks[lockPath] = lockBytes
	return s, nil
}
func (s snapshot) locksUnchanged() bool {
	for p, b := range s.Locks {
		c, e := os.ReadFile(p)
		if e != nil || !bytes.Equal(b, c) {
			return false
		}
	}
	return true
}

var forbidden = map[string]bool{
	"credentials": true, "access_token": true, "oauth_access_token": true, "impersonate_service_account": true, "impersonate_service_account_delegates": true, "impersonate_service_account_delegation_chain": true, "endpoints": true, "endpoint": true, "external_credentials": true, "universe_domain": true,
}

func forbiddenName(k string) bool {
	return forbidden[k] || strings.HasSuffix(k, "_custom_endpoint") || strings.HasSuffix(k, "_endpoint")
}
func inspectSyntax(body *hclsyntax.Body) error {
	for k := range body.Attributes {
		if forbiddenName(k) {
			return errInvalid
		}
	}
	for _, b := range body.Blocks {
		if forbiddenName(b.Type) {
			return errInvalid
		}
		// OpenTofu expands dynamic's label into a block type. Inspect that
		// target even when for_each is empty or unknown; expansion must not
		// introduce an alternative identity or endpoint after preflight.
		if b.Type == "dynamic" && (len(b.Labels) != 1 || forbiddenName(b.Labels[0])) {
			return errInvalid
		}
		if e := inspectSyntax(b.Body); e != nil {
			return e
		}
	}
	return nil
}
func inspectJSON(v any) error {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if forbiddenName(k) {
				return errInvalid
			}
			if e := inspectJSON(v); e != nil {
				return e
			}
		}
	case []any:
		for _, v := range x {
			if e := inspectJSON(v); e != nil {
				return e
			}
		}
	}
	return nil
}
func literal(e hcl.Expression) (string, error) {
	v, d := e.Value(nil)
	if d.HasErrors() || !v.IsKnown() || v.IsNull() || v.Type() != cty.String {
		return "", errInvalid
	}
	return v.AsString(), nil
}
func blocks(body hcl.Body, defs ...hcl.BlockHeaderSchema) ([]*hcl.Block, error) {
	c, _, d := body.PartialContent(&hcl.BodySchema{Blocks: defs})
	if d.HasErrors() {
		return nil, errInvalid
	}
	return c.Blocks, nil
}
func attrs(body hcl.Body) (hcl.Attributes, error) {
	a, d := body.JustAttributes()
	if d.HasErrors() {
		return nil, errInvalid
	}
	return a, nil
}
func inspectTree(ctx context.Context, tree, root string, backend Backend, initial bool, visited ...map[string]bool) error {
	count := 0
	queue := []string{root}
	allowed := tree
	if !initial {
		allowed = filepath.Dir(filepath.Dir(tree))
		data, e := os.ReadFile(filepath.Join(tree, "modules.json"))
		if e != nil || len(data) > maxInput {
			return errInvalid
		}
		var manifest struct{ Modules []struct{ Key, Dir string } }
		if json.Unmarshal(data, &manifest) != nil {
			return errInvalid
		}
		queue = nil
		for _, m := range manifest.Modules {
			if m.Key == "" {
				continue
			}
			dir := m.Dir
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(root, dir)
			}
			queue = append(queue, dir)
		}
	}
	seen := map[string]bool{}
	inspectFile := func(p string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resolved, e := filepath.EvalSymlinks(p)
		if e != nil || !within(allowed, resolved) {
			return errInvalid
		}
		st, e := os.Stat(resolved)
		if e != nil || !st.Mode().IsRegular() {
			return errInvalid
		}
		data, e := os.ReadFile(p)
		if e != nil || len(data) > maxInput*8 {
			return errInvalid
		}
		parser := hclparse.NewParser()
		var f *hcl.File
		var diag hcl.Diagnostics
		if strings.HasSuffix(p, ".json") {
			f, diag = parser.ParseJSON(data, p)
			var v any
			if strict(data, &v) != nil || inspectJSON(v) != nil {
				return errInvalid
			}
		} else {
			f, diag = parser.ParseHCL(data, p)
			if !diag.HasErrors() {
				if e = inspectSyntax(f.Body.(*hclsyntax.Body)); e != nil {
					return e
				}
			}
		}
		if diag.HasErrors() {
			return errInvalid
		}
		bs, e := blocks(f.Body, hcl.BlockHeaderSchema{Type: "terraform"}, hcl.BlockHeaderSchema{Type: "module", LabelNames: []string{"name"}})
		if e != nil {
			return e
		}
		for _, b := range bs {
			if b.Type == "terraform" {
				backends, e := blocks(b.Body, hcl.BlockHeaderSchema{Type: "backend", LabelNames: []string{"type"}}, hcl.BlockHeaderSchema{Type: "cloud"})
				if e != nil {
					return e
				}
				for _, be := range backends {
					if !initial || be.Type == "cloud" || filepath.Dir(p) != root || be.Labels[0] != "gcs" {
						return errInvalid
					}
					count++
					a, e := attrs(be.Body)
					if e != nil {
						return e
					}
					for k, v := range a {
						if k != "bucket" && k != "prefix" {
							return errInvalid
						}
						s, e := literal(v.Expr)
						if e != nil || (k == "bucket" && s != backend.Bucket) || (k == "prefix" && s != backend.Prefix) {
							return errInvalid
						}
					}
				}
			} else {
				// Module sources must be immutable and local modules must stay in the copy.
				c, _, di := b.Body.PartialContent(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "source", Required: true}, {Name: "version"}}})
				if di.HasErrors() {
					return errInvalid
				}
				src, e := literal(c.Attributes["source"].Expr)
				if e != nil {
					return e
				}
				if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
					target := filepath.Join(filepath.Dir(p), src)
					r, e := filepath.EvalSymlinks(target)
					if e != nil || !within(allowed, r) {
						return errInvalid
					}
					queue = append(queue, r)
				} else if v, ok := c.Attributes["version"]; ok {
					version, e := literal(v.Expr)
					if e != nil || !regexpVersion(version) || !registryModule(src) {
						return errInvalid
					}
				} else {
					if !pinnedGitModule(src) {
						return errInvalid
					}
				}
			}
		}
		return nil
	}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		resolved, e := filepath.EvalSymlinks(dir)
		if e != nil || !within(allowed, resolved) {
			return errInvalid
		}
		dir = resolved
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if len(seen) > 10000 {
			return errInvalid
		}
		if len(visited) > 0 {
			visited[0][dir] = true
		}
		entries, e := os.ReadDir(dir)
		if e != nil {
			return errInvalid
		}
		for _, entry := range entries {
			if poison(entry.Name()) {
				return errInvalid
			}
			p := filepath.Join(dir, entry.Name())
			if strings.HasSuffix(p, ".tf") || strings.HasSuffix(p, ".tf.json") {
				if e = inspectFile(p); e != nil {
					return e
				}
			}
		}
	}
	if initial && count != 1 {
		return errInvalid
	}
	if !initial && count != 0 {
		return errInvalid
	}
	return nil
}

var modulePartRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
var registryHostRE = regexp.MustCompile(`^[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*\.[A-Za-z0-9]+$`)

func registryModule(src string) bool {
	parts := strings.Split(src, "/")
	if len(parts) == 4 {
		if !registryHostRE.MatchString(parts[0]) {
			return false
		}
		parts = parts[1:]
	}
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if !modulePartRE.MatchString(p) {
			return false
		}
	}
	return true
}

func pinnedGitModule(src string) bool {
	// An archive server can ignore a ref query. Only the explicit Git getter
	// enforces checkout of a full commit. No getter auto-detection, discovery,
	// credentials, depth, fragments, or additional query options are accepted.
	if !strings.HasPrefix(src, "git::https://") {
		return false
	}
	u, e := url.Parse(strings.TrimPrefix(src, "git::"))
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.Path == "" {
		return false
	}
	if !strings.HasPrefix(u.RawQuery, "ref=") || !revisionRE.MatchString(strings.TrimPrefix(u.RawQuery, "ref=")) {
		return false
	}
	// Validate repository and optional module subdirectory without interpreting
	// escapes or allowing traversal in either component.
	for _, p := range strings.Split(strings.TrimPrefix(u.Path, "/"), "//") {
		if !canonical(p, false) {
			return false
		}
	}
	return true
}
func regexpVersion(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}
func validateLock(data []byte, name string) error {
	f, d := hclsyntax.ParseConfig(data, name, hcl.Pos{Line: 1, Column: 1})
	if d.HasErrors() {
		return errInvalid
	}
	bs, e := blocks(f.Body, hcl.BlockHeaderSchema{Type: "provider", LabelNames: []string{"name"}})
	if e != nil {
		return e
	}
	google := false
	seen := map[string]bool{}
	for _, b := range bs {
		if seen[b.Labels[0]] {
			return errInvalid
		}
		seen[b.Labels[0]] = true
		a, e := attrs(b.Body)
		if e != nil {
			return e
		}
		v, ok := a["version"]
		if !ok {
			return errInvalid
		}
		version, e := literal(v.Expr)
		if e != nil || !regexpVersion(version) {
			return errInvalid
		}
		if b.Labels[0] == "registry.opentofu.org/hashicorp/google" || b.Labels[0] == "registry.terraform.io/hashicorp/google" {
			google = true
			if version != "7.10.0" {
				return errInvalid
			}
		}
		hashes, ok := a["hashes"]
		if !ok {
			return errInvalid
		}
		hv, di := hashes.Expr.Value(nil)
		if di.HasErrors() || hv.IsNull() || !hv.IsKnown() || !hv.CanIterateElements() {
			return errInvalid
		}
		n := 0
		it := hv.ElementIterator()
		for it.Next() {
			_, x := it.Element()
			if x.Type() != cty.String || x.IsNull() {
				return errInvalid
			}
			s := x.AsString()
			switch {
			case strings.HasPrefix(s, "zh:"):
				digest, e := hex.DecodeString(strings.TrimPrefix(s, "zh:"))
				if e != nil || len(digest) != 32 {
					return errInvalid
				}
				n++
			case strings.HasPrefix(s, "h1:"):
				digest, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "h1:"))
				if e != nil || len(digest) != 32 {
					return errInvalid
				}
			default:
				return errInvalid
			}
		}
		if n < 3 {
			return errInvalid
		}
	}
	if !google {
		return errInvalid
	}
	return nil
}
