package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestB24AssemblyIncludes(t *testing.T) {
	root := physicalTempDir(t)
	sdk := physicalTempDir(t)
	write(t, sdk, "share/go/pkg/include/textflag.h", "#define NOSPLIT 4\n")
	outside := filepath.Join(physicalTempDir(t), "ambient.inc")
	write(t, filepath.Dir(outside), filepath.Base(outside), "external")
	cases := []struct {
		name, assembly string
		headers        map[string]string
		bad            bool
	}{
		{"contained headers", "#include \"nested/a.h\"\n#include \"textflag.h\"\n#include \"go_asm.h\"\n", map[string]string{"nested/a.h": "#include \"b.inc\"\n", "b.inc": "#define VALUE 42\n"}, false},
		{"absolute", "#include " + strconv.Quote(outside) + "\n", nil, true},
		{"escaped absolute", "#include \"" + strings.ReplaceAll(outside, "/", `\x2f`) + "\"\n", nil, true},
		{"parent traversal", "#include \"../ambient.inc\"\n", nil, true},
		{"nested absolute", "#include \"local.h\"\n", map[string]string{"local.h": "#include " + strconv.Quote(outside) + "\n"}, true},
		{"SDK shadow", "#include \"textflag.h\"\n", map[string]string{"textflag.h": "#include " + strconv.Quote(outside) + "\n"}, true},
		{"generated shadow", "#include \"go_asm.h\"\n", map[string]string{"go_asm.h": "#include " + strconv.Quote(outside) + "\n"}, true},
		{"unresolved", "#include \"missing.h\"\n", nil, true},
		{"nonliteral", "#include HEADER\n", nil, true},
		{"macro directive", "#define LOAD #include \"textflag.h\"\nLOAD\n", nil, true},
		{"continued macro directive", "#define LOAD \\\n#include \"textflag.h\"\nLOAD\n", nil, true},
		{"macro hash argument", "LOAD(#, include, \"textflag.h\")\n", nil, true},
		{"inactive branch", "#ifdef NEVER\n#include " + strconv.Quote(outside) + "\n#endif\n", nil, true},
		{"comments and strings", "/* #include \"/ambient\" */\nDATA symbol(SB)/8, $\"#include\"\n# /* comment */ include /* comment */ \"textflag.h\"\n#define VALUE(x) x+1\n", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(root, strings.ReplaceAll(c.name, " ", "-"))
			write(t, dir, "main.s", c.assembly)
			for name, body := range c.headers {
				write(t, dir, name, body)
			}
			g := assemblyGuard{root: dir, dir: dir, sdk: sdk, seen: map[string]bool{}}
			if err := g.file(filepath.Join(dir, "main.s"), 0); (err != nil) != c.bad {
				t.Fatalf("bad=%v, error=%v", c.bad, err)
			}
		})
	}
	t.Run("header symlink", func(t *testing.T) {
		dir := physicalTempDir(t)
		write(t, dir, "main.s", "#include \"link.h\"\n")
		if err := os.Symlink(outside, filepath.Join(dir, "link.h")); err != nil {
			t.Fatal(err)
		}
		g := assemblyGuard{root: dir, dir: dir, sdk: sdk, seen: map[string]bool{}}
		if g.file(filepath.Join(dir, "main.s"), 0) == nil {
			t.Fatal("escaping header symlink accepted")
		}
	})
}

func TestB24DependencyAssemblyGraph(t *testing.T) {
	src, sdk, cache := physicalTempDir(t), physicalTempDir(t), physicalTempDir(t)
	dep := filepath.Join(cache, "example.com/dep@v1.0.0")
	write(t, dep, "value.s", "#include \"value.inc\"\n")
	write(t, dep, "value.inc", "#define VALUE 42\n")
	packages := []sourcePackage{{Name: "dep", Dir: dep, SFiles: []string{"value.s"}, Module: &sourceModule{Dir: dep}}, {Name: "main", Dir: src}}
	stream := func() []byte {
		var result []byte
		for _, pkg := range packages {
			b, _ := json.Marshal(pkg)
			result = append(result, b...)
		}
		return result
	}
	if err := checkPackageInputs(stream(), src, sdk, cache); err != nil {
		t.Fatal(err)
	}
	write(t, dep, "value.inc", "#include \"/ambient/value.inc\"\n")
	if err := checkPackageInputs(stream(), src, sdk, cache); err == nil {
		t.Fatal("dependency assembly escape accepted")
	}
	write(t, cache, "ambient.inc", "#define VALUE 99\n")
	write(t, dep, "value.inc", "#include \"../../ambient.inc\"\n")
	if err := checkPackageInputs(stream(), src, sdk, cache); err == nil {
		t.Fatal("dependency assembly escaped its module into cache metadata")
	}
	packages[0].Dir = physicalTempDir(t)
	if err := checkPackageInputs(stream(), src, sdk, cache); err == nil {
		t.Fatal("ambient package directory accepted")
	}
}
