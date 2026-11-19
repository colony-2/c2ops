package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

type version [3]int

var goVersionRE = regexp.MustCompile(`^1\.(0|[1-9][0-9]*)(?:\.(0|[1-9][0-9]*))?$`)

func parseVersion(s string) (version, error) {
	var v version
	s = strings.TrimPrefix(s, "go")
	if !goVersionRE.MatchString(s) {
		return v, fmt.Errorf("invalid stable Go version selector")
	}
	parts := strings.Split(s, ".")
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, fmt.Errorf("invalid Go version")
		}
		v[i] = n
	}
	return v, nil
}
func less(a, b version) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

type selection struct {
	requested string
	resolved  string
	file      string
	directive *string
	workspace *string
}

func sourcePath(root, rel string) (string, error) {
	if !relative(rel, true) {
		return "", fmt.Errorf("source path is not relative")
	}
	p := filepath.Join(root, rel)
	real, err := filepath.EvalSymlinks(p)
	if err != nil || !inside(root, real) {
		return "", fmt.Errorf("missing or escaping committed source path")
	}
	return real, nil
}
func selectVersion(root string, r Request) (selection, error) {
	var s selection
	module := filepath.Join(r.ModuleDir, "go.mod")
	selected := r.VersionFile
	if selected == "" {
		selected = filepath.ToSlash(module)
	}
	s.file = selected
	if filepath.Base(selected) == "go.mod" && selected != filepath.ToSlash(module) {
		return s, fmt.Errorf("selected go.mod must match module_dir")
	}
	minima := []string{}
	readMod := func(rel string) (*modfile.File, error) {
		p, err := sourcePath(root, rel)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("cannot read committed go.mod")
		}
		f, err := modfile.Parse(rel, b, nil)
		if err != nil || f.Go == nil {
			return nil, fmt.Errorf("malformed go.mod or missing go directive")
		}
		minima = append(minima, f.Go.Version)
		for _, rep := range f.Replace {
			if rep.New.Version == "" {
				if err := localPath(root, filepath.Dir(p), rep.New.Path); err != nil {
					return nil, err
				}
			}
		}
		return f, nil
	}
	m, err := readMod(filepath.ToSlash(module))
	if err != nil {
		return s, err
	}
	goDirective := m.Go.Version
	toolchain := ""
	if m.Toolchain != nil {
		toolchain = m.Toolchain.Name
	}
	if filepath.Base(selected) == "go.work" {
		p, err := sourcePath(root, selected)
		if err != nil {
			return s, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return s, fmt.Errorf("cannot read committed go.work")
		}
		w, err := modfile.ParseWork(selected, b, nil)
		if err != nil || w.Go == nil || len(w.Use) == 0 {
			return s, fmt.Errorf("malformed go.work or missing go/use directive")
		}
		minima = append(minima, w.Go.Version)
		goDirective = w.Go.Version
		toolchain = ""
		if w.Toolchain != nil {
			toolchain = w.Toolchain.Name
		}
		member := false
		modulePath, _ := sourcePath(root, r.ModuleDir)
		for _, use := range w.Use {
			if err := localPath(root, filepath.Dir(p), use.Path); err != nil {
				return s, err
			}
			memberPath, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(p), use.Path))
			if err != nil {
				return s, fmt.Errorf("missing workspace member")
			}
			if memberPath == modulePath {
				member = true
			}
			rel, _ := filepath.Rel(root, filepath.Join(memberPath, "go.mod"))
			if _, err = readMod(filepath.ToSlash(rel)); err != nil {
				return s, err
			}
		}
		if !member {
			return s, fmt.Errorf("module_dir is not an active workspace member")
		}
		for _, rep := range w.Replace {
			if rep.New.Version == "" {
				if err := localPath(root, filepath.Dir(p), rep.New.Path); err != nil {
					return s, err
				}
			}
		}
		s.workspace = &s.file
	}
	if r.GoVersion != "" {
		s.requested = strings.TrimPrefix(r.GoVersion, "go")
	} else {
		d := "go"
		s.requested = goDirective
		if toolchain != "" && toolchain != "default" {
			s.requested = strings.TrimPrefix(toolchain, "go")
			d = "toolchain"
		}
		s.directive = &d
	}
	want, err := parseVersion(s.requested)
	if err != nil {
		return s, err
	}
	// A minor chooses the largest stable patch from this immutable mapping only.
	candidates := []string{}
	for exact := range sdkPins {
		v, _ := parseVersion(exact)
		if (strings.Count(s.requested, ".") == 1 && v[0] == want[0] && v[1] == want[1]) || exact == s.requested {
			candidates = append(candidates, exact)
		}
	}
	if len(candidates) == 0 {
		return s, fmt.Errorf("Go selector %s is unsupported by the pinned SDK mapping", s.requested)
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, _ := parseVersion(candidates[i])
		b, _ := parseVersion(candidates[j])
		return less(a, b)
	})
	s.resolved = candidates[len(candidates)-1]
	chosen, _ := parseVersion(s.resolved)
	for _, minimum := range minima {
		v, err := parseVersion(minimum)
		if err != nil {
			return s, fmt.Errorf("invalid module/workspace minimum")
		}
		if less(chosen, v) {
			return s, fmt.Errorf("selected Go %s is below module/workspace minimum %s", s.resolved, minimum)
		}
	}
	return s, nil
}
func localPath(root, base, local string) error {
	// Absolute paths are ambient inputs even when they happen to point inside the
	// caller's checkout. Relative paths must resolve entirely inside projection.
	if filepath.IsAbs(local) {
		return fmt.Errorf("absolute workspace/local replacement is forbidden")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(base, local))
	if err != nil || !inside(root, resolved) {
		return fmt.Errorf("workspace/local replacement escapes projection or is missing")
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("workspace/local replacement must be a committed directory")
	}
	return nil
}
