package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func roots(r Request) (Request, error) {
	values := []*string{&r.Worktree, &r.Inbox, &r.Outbox}
	for _, p := range values {
		real, err := filepath.EvalSymlinks(*p)
		if err != nil {
			return r, fmt.Errorf("workspace/inbox/outbox root is missing")
		}
		st, err := os.Stat(real)
		if err != nil || !st.IsDir() {
			return r, fmt.Errorf("workspace/inbox/outbox root is not a directory")
		}
		*p = real
	}
	for i, a := range values {
		for j, b := range values {
			if i != j && (inside(*a, *b) || inside(*b, *a)) {
				return r, fmt.Errorf("workspace/inbox/outbox roots must be disjoint")
			}
		}
	}
	return r, nil
}

// The system temp directory may itself be reached through a symlink on macOS.
func buildScratch(r Request) (string, error) {
	parent, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("cannot resolve temporary directory")
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return "", fmt.Errorf("cannot resolve temporary directory")
	}
	for _, root := range []string{r.Worktree, r.Inbox, r.Outbox} {
		if inside(root, parent) {
			return "", fmt.Errorf("temporary directory must be outside bound roots")
		}
	}
	scratch, err := os.MkdirTemp(parent, "c2ops-go-build-")
	if err != nil {
		return "", fmt.Errorf("cannot create private build directory")
	}
	return scratch, nil
}

func build(ctx context.Context, r Request) (Output, error) { return buildWith(ctx, r, command) }

// run is injectable only within Go tests; the executable has no compiler/path or
// source policy bypass. Actual builds always use command.
func buildWith(ctx context.Context, r Request, run runner) (result Output, err error) {
	r, err = roots(r)
	if err != nil {
		return result, err
	}
	final := filepath.Join(r.Outbox, "go-build")
	if _, e := os.Lstat(final); !os.IsNotExist(e) {
		return result, fmt.Errorf("go-build output already exists; use a fresh bound outbox")
	}
	scratch, err := buildScratch(r)
	if err != nil {
		return result, err
	}
	defer removeScratch(scratch)
	src := filepath.Join(scratch, "source")
	if err = os.Mkdir(src, 0700); err != nil {
		return result, fmt.Errorf("cannot create source projection")
	}
	p, err := prepareSource(ctx, r, src)
	if err != nil {
		return result, err
	}
	s, err := selectVersion(src, r)
	if err != nil {
		return result, err
	}
	cwd, err := sourcePath(src, r.ModuleDir)
	if err != nil {
		return result, err
	}
	ep := filepath.Join(cwd, r.Entrypoint)
	resolved, err := filepath.EvalSymlinks(ep)
	if err != nil || !inside(src, resolved) {
		return result, fmt.Errorf("entrypoint is missing or escapes projection")
	}
	sdk, err := acquireSDK(ctx, s, scratch, run)
	if err != nil {
		return result, err
	}
	// Staging is in outbox for a single same-filesystem atomic directory rename.
	// No descriptors or final files are visible until every target has succeeded.
	stage, err := os.MkdirTemp(r.Outbox, ".go-build-")
	if err != nil {
		return result, fmt.Errorf("cannot stage release outputs")
	}
	defer os.RemoveAll(stage)
	controls := map[string]string{"CGO_ENABLED": "0", "GOENV": "off", "GOTOOLCHAIN": "local", "GOROOT": sdk.StorePath + "/share/go", "GOWORK": "off"}
	if s.workspace != nil {
		controls["GOWORK"] = filepath.Join(src, *s.workspace)
	}
	envControls := map[string]string{}
	for k, v := range controls {
		envControls[k] = v
	}
	for _, k := range []string{"GOPATH", "GOCACHE", "GOMODCACHE", "GOTMPDIR"} {
		dir := filepath.Join(scratch, k)
		if err = os.Mkdir(dir, 0700); err != nil {
			return result, fmt.Errorf("cannot create isolated Go cache")
		}
		envControls[k] = dir
	}
	envControls["GOTELEMETRY"] = "off"
	env := envWith(cleanEnv(), envControls)
	flags := []string{"-mod=readonly", "-trimpath", "-buildvcs=false"}
	if len(r.Tags) > 0 {
		flags = append(flags, "-tags="+strings.Join(r.Tags, ","))
	}
	symbols := []string{}
	for k := range r.LinkerValues {
		symbols = append(symbols, k)
	}
	sort.Strings(symbols)
	linker := []string{"-s", "-w"}
	for _, k := range symbols {
		linker = append(linker, "-X", linkerQuote(k+"="+r.LinkerValues[k]))
	}
	effective := append(append([]string{}, flags...), "-ldflags="+strings.Join(linker, " "))
	result = Output{Intent: r.Intent, Files: []File{}, Provenance: Provenance{SchemaVersion: 1}}
	for _, t := range r.Targets {
		name := "go-build/binaries/" + t.GOOS + "_" + t.GOARCH + "/" + r.Intent.BinaryName
		dest := filepath.Join(stage, strings.TrimPrefix(name, "go-build/"))
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return Output{}, fmt.Errorf("cannot stage binary")
		}
		targetEnv := envWith(env, map[string]string{"GOOS": t.GOOS, "GOARCH": t.GOARCH})
		listArgs := append([]string{"list", "-deps", "-json"}, flags...)
		listArgs = append(listArgs, r.Entrypoint)
		packageJSON, listErr := run(ctx, sdk.CompilerPath, listArgs, cwd, targetEnv)
		if listErr != nil {
			return Output{}, fmt.Errorf("entrypoint must load a committed main package for %s/%s", t.GOOS, t.GOARCH)
		}
		if err = checkPackageInputs(packageJSON, src, sdk.StorePath, envControls["GOMODCACHE"]); err != nil {
			return Output{}, fmt.Errorf("source input validation for %s/%s: %w", t.GOOS, t.GOARCH, err)
		}
		args := append([]string{"build"}, effective...)
		args = append(args, "-o", dest, r.Entrypoint)
		if _, err = run(ctx, sdk.CompilerPath, args, cwd, targetEnv); err != nil {
			return Output{}, fmt.Errorf("Go %s build failed for %s/%s (subprocess diagnostics suppressed to protect credentials)", sdk.ResolvedVersion, t.GOOS, t.GOARCH)
		}
		info, e := os.Lstat(dest)
		if e != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return Output{}, fmt.Errorf("compiler did not produce a regular binary")
		}
		if err = os.Chmod(dest, 0755); err != nil {
			return Output{}, fmt.Errorf("cannot set binary executable mode")
		}
		b, e := os.ReadFile(dest)
		if e != nil {
			return Output{}, fmt.Errorf("cannot hash binary")
		}
		result.Files = append(result.Files, descriptor(r.Intent, name, "binary", b, t))
	}
	if err = p.verify(); err != nil {
		return Output{}, err
	}
	inv := p.inventory()
	if err = os.Mkdir(filepath.Join(stage, "provenance"), 0700); err != nil {
		return Output{}, fmt.Errorf("cannot stage provenance")
	}
	invName := "go-build/provenance/source-tree.json"
	if err = os.WriteFile(filepath.Join(stage, "provenance/source-tree.json"), inv, 0644); err != nil {
		return Output{}, fmt.Errorf("cannot write source provenance")
	}
	result.Files = append(result.Files, descriptor(r.Intent, invName, "document", inv, Target{}))
	result.Provenance.Source = SourceInfo{p.commit, p.tree, p.format, projectionPolicy, len(p.entries), invName, digest(inv)}
	result.Provenance.SDK = sdk
	recordedEnv := map[string]string{}
	for k, v := range controls {
		recordedEnv[k] = v
	}
	values := r.LinkerValues
	if values == nil {
		values = map[string]string{}
	}
	result.Provenance.Build = BuildInfo{r.ModuleDir, r.Entrypoint, s.workspace, r.Targets, symbols, digest(canonicalJSON(values)), recordedEnv, append(append([]string{}, flags...), "-ldflags=-s -w"), assemblyPolicy}
	encoded, err := json.Marshal(map[string]any{"output": result})
	if err != nil || len(encoded)+1 > 128*1024 {
		return Output{}, fmt.Errorf("result exceeds 128 KiB")
	}
	if ctx.Err() != nil {
		return Output{}, fmt.Errorf("operation cancelled")
	}
	if err = os.Rename(stage, final); err != nil {
		return Output{}, fmt.Errorf("cannot commit complete output directory")
	}
	return result, nil
}

// cmd/go's -ldflags parser accepts one enclosing quote per argument; it does
// not apply shell expansion or backslash escapes. Choose an available quote.
func linkerQuote(s string) string {
	if !strings.Contains(s, `'`) {
		return `'` + s + `'`
	}
	return `"` + s + `"`
}
