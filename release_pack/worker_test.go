package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The coordinator supplies a real hydrated request on the consumer worker only
// after each selected build/sign package's success and publication confirmation.
// Fixture subprocess checks run unconditionally in TestC02X06ProcessHandoff.
// Supplying a request here does not itself establish worker/retention readiness.
func TestK06HydratedFinalArtifacts(t *testing.T) {
	requestPath := os.Getenv("C2OPS_RELEASE_PACK_INTEGRATION_REQUEST")
	if requestPath == "" {
		t.Skip("K06 unverified: requires real worker hydration and separately confirmed selected go.build/apple.sign publications; see README.md")
	}
	file, e := os.Open(requestPath)
	must(t, e)
	r, e := decode(file)
	must(t, e)
	must(t, file.Close())
	exe := candidate(t)
	cwd := t.TempDir()
	must(t, os.Chmod(cwd, 0555))
	defer os.Chmod(cwd, 0755)
	expected := map[Target][]byte{}
	for _, d := range r.Files {
		b, e := os.ReadFile(filepath.Join(r.Inbox, str(d, "name")))
		must(t, e)
		expected[Target{str(d, "goos"), str(d, "goarch")}] = b
	}
	out, diag, e := invoke(exe, r, cwd, os.Environ())
	if e != nil {
		t.Fatalf("pack failed: %v %s", e, diag)
	}
	if len(diag) > 0 {
		t.Fatal(string(diag))
	}
	files := resultFiles(t, out)
	if len(files) != len(expected)+1 {
		t.Fatal("incomplete manifest")
	}
	var lines []string
	for _, d := range files {
		b, e := os.ReadFile(filepath.Join(r.Outbox, str(d, "name")))
		must(t, e)
		sum := sha256.Sum256(b)
		size, _ := number(d, "size")
		if int64(len(b)) != size || hex.EncodeToString(sum[:]) != str(d, "sha256") {
			t.Fatal("final-byte descriptor mismatch")
		}
		if str(d, "kind") != "archive" {
			continue
		}
		entries := unpack(t, b)
		binary := entries[str(r.Intent, "binary_name")]
		if binary.h.Mode != 0755 || !bytes.Equal(binary.b, expected[Target{str(d, "goos"), str(d, "goarch")}]) {
			t.Fatal("hydrated final bytes/mode mismatch")
		}
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+str(d, "name"))
	}
	checks, e := os.ReadFile(filepath.Join(r.Outbox, "checksums.txt"))
	must(t, e)
	if string(checks) != strings.Join(lines, "\n")+"\n" {
		t.Fatal("final-byte checksum content mismatch")
	}
}
