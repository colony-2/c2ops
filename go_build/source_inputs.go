package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/scanner"
	"unicode"
)

const assemblyPolicy = "contained-includes-v1"

type sourcePackage struct {
	Name   string
	Dir    string
	SFiles []string
	Module *sourceModule
}

type sourceModule struct {
	Dir     string
	Replace *sourceModule
}

// go list does not compile assembly. Inspect its complete target-specific graph
// before go build can hand any of these files to the assembler. The assembler's
// preprocessor can open arbitrary paths, independently of go.mod and symlinks.
func checkPackageInputs(data []byte, source, sdk, modcache string) error {
	d := json.NewDecoder(bytes.NewReader(data))
	main := false
	for {
		var pkg sourcePackage
		if err := d.Decode(&pkg); err != nil {
			if err == io.EOF && main {
				return nil
			}
			return fmt.Errorf("entrypoint must load a committed main package")
		}
		dir, err := filepath.EvalSymlinks(pkg.Dir)
		if err != nil {
			return fmt.Errorf("package source directory is missing")
		}
		if pkg.Name == "main" && inside(source, dir) {
			main = true
		}
		if inside(sdk, dir) {
			continue // Recorded pinned SDK inputs, including its assembly, are trusted.
		}
		root := source
		if !inside(source, dir) {
			if !inside(modcache, dir) {
				return fmt.Errorf("package source escapes projection and isolated dependencies")
			}
			module := pkg.Module
			if module != nil && module.Replace != nil {
				module = module.Replace
			}
			if module == nil {
				return fmt.Errorf("dependency source is missing module identity")
			}
			root, err = filepath.EvalSymlinks(module.Dir)
			if err != nil || !inside(modcache, root) || root == modcache || !inside(root, dir) {
				return fmt.Errorf("dependency source escapes its resolved module")
			}
		}
		guard := assemblyGuard{root: root, dir: dir, sdk: sdk, seen: map[string]bool{}}
		for _, name := range pkg.SFiles {
			if filepath.Base(name) != name || name == "." || name == ".." {
				return fmt.Errorf("unsupported assembly source path")
			}
			if err := guard.file(filepath.Join(dir, name), 0); err != nil {
				return err
			}
		}
	}
}

type assemblyGuard struct {
	root, dir, sdk string
	seen           map[string]bool
}

func (g *assemblyGuard) file(path string, depth int) error {
	if depth > 100 {
		return fmt.Errorf("assembly include nesting exceeds supported limit")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !inside(g.root, real) {
		return fmt.Errorf("assembly input escapes permitted source or is missing")
	}
	if g.seen[real] {
		return nil
	}
	g.seen[real] = true
	b, err := os.ReadFile(real)
	if err != nil {
		return fmt.Errorf("cannot inspect assembly input")
	}
	return assemblyIncludes(b, func(name string) error {
		// Decode string escapes before checking paths. Absolute includes are
		// unsupported even if they happen to identify an otherwise allowed file.
		if name == "" || filepath.IsAbs(name) || strings.ContainsAny(name, "\\\x00") {
			return fmt.Errorf("absolute or unsupported assembly include is forbidden")
		}
		// cmd/asm tries the package cwd first, then the original .s directory
		// (also the package directory), the compiler workdir, and SDK pkg/include.
		// Nested includes keep this search base, not the including header's dir.
		local := filepath.Join(g.dir, name)
		if !inside(g.root, local) {
			return fmt.Errorf("assembly include escapes permitted source")
		}
		if _, err := os.Lstat(local); err == nil {
			return g.file(local, depth+1)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect assembly include")
		}
		if name == "go_asm.h" {
			return nil // Generated only by this pinned compiler from checked inputs.
		}
		switch name {
		case "textflag.h", "funcdata.h", "asm_amd64.h", "asm_ppc64x.h", "asm_riscv64.h":
			p := filepath.Join(g.sdk, "share/go/pkg/include", name)
			st, err := os.Stat(p)
			if err == nil && st.Mode().IsRegular() {
				return nil
			}
		}
		return fmt.Errorf("assembly include must be contained source or a supported SDK header")
	})
}

// Use cmd/asm's text/scanner configuration, not a line regexp: comments,
// whitespace and escaped string literals must not hide a path. Check all
// conditional branches. Reject non-directive # tokens (including in macro
// bodies/arguments), so macro expansion cannot manufacture unchecked includes.
func assemblyIncludes(b []byte, include func(string) error) error {
	var s scanner.Scanner
	s.Init(bytes.NewReader(b))
	s.Whitespace = 1<<'\t' | 1<<'\r' | 1<<' '
	s.Mode = scanner.ScanChars | scanner.ScanFloats | scanner.ScanIdents |
		scanner.ScanInts | scanner.ScanStrings | scanner.ScanComments
	s.IsIdentRune = func(ch rune, i int) bool {
		return unicode.IsLetter(ch) || ch == '_' || ch == '·' || ch == '∕' || i > 0 && unicode.IsDigit(ch)
	}
	bad := false
	s.Error = func(*scanner.Scanner, string) { bad = true }
	next := func() rune {
		for {
			tok := s.Scan()
			if tok != scanner.Comment {
				return tok
			}
		}
	}
	bol := true
	continuation := false
	for tok := next(); tok != scanner.EOF; tok = next() {
		if tok == '\n' {
			bol = !continuation
			continuation = false
			continue
		}
		if tok == '#' {
			if !bol || next() != scanner.Ident {
				return fmt.Errorf("assembly macro-generated directives are unsupported")
			}
			switch s.TokenText() {
			case "include":
				if next() != scanner.String {
					return fmt.Errorf("assembly include requires a literal quoted path")
				}
				name, err := strconv.Unquote(s.TokenText())
				if err != nil || next() != '\n' {
					return fmt.Errorf("unsupported assembly include syntax")
				}
				if err = include(name); err != nil {
					return err
				}
				bol = true
				continuation = false
				continue
			case "define", "undef", "ifdef", "ifndef", "else", "endif", "line":
			default:
				return fmt.Errorf("unsupported assembly preprocessor directive")
			}
		}
		bol = false
		continuation = tok == '\\'
	}
	if bad {
		return fmt.Errorf("unsupported assembly token syntax")
	}
	return nil
}
