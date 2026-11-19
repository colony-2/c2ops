package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestC01ExactProvenanceNumbers(t *testing.T) {
	f := newFixture(t)
	f.in.Provenance["large"] = json.Number("9007199254740993")
	f.in.Provenance["nested"] = []any{json.Number("1e400"), json.Number("0.12345678901234567890123456789")}
	wire, _ := json.Marshal(f.in)
	decoded, err := decodeInput(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	other := decoded
	other.Provenance = make(map[string]any)
	for k, v := range decoded.Provenance {
		other.Provenance[k] = v
	}
	other.Provenance["large"] = json.Number("9007199254740992")
	if marker(decoded) == marker(other) {
		t.Fatal("distinct exact provenance shares draft identity")
	}
	out, err := f.invoke()
	if err != nil {
		t.Fatal(err)
	}
	for _, exact := range []string{"9007199254740993", "1e400", "0.12345678901234567890123456789"} {
		if !strings.Contains(out, exact) {
			t.Fatalf("provenance number lost: %s", exact)
		}
	}
	if f.release.Body != marker(decoded) {
		t.Fatal("draft identity changed while decoding")
	}
}

func TestC01InvalidTagBeforeRequests(t *testing.T) {
	for _, version := range []string{"1.2.3-rc.", "1.2.3-rc..1", "1.2.3+meta.lock"} {
		t.Run(version, func(t *testing.T) {
			f := newFixture(t)
			id := f.in.Intent.Identity
			id.Version, id.Tag = version, "v"+version
			f.in.Intent.Identity = id
			for n := range f.in.Files {
				f.in.Files[n].Identity = id
			}
			f.in.Provenance["version"], f.in.Provenance["tag"] = id.Version, id.Tag
			if out, err := f.invoke(); err == nil || out != "" || len(f.reads)+len(f.writes) != 0 {
				t.Fatal("invalid tag reached network")
			}
		})
	}
	f := newFixture(t)
	f.in.Intent.Version, f.in.Intent.Tag = "1.2.3-rc.1+build.7", "v1.2.3-rc.1+build.7"
	for n := range f.in.Files {
		f.in.Files[n].Identity = f.in.Intent.Identity
	}
	f.in.Provenance["version"], f.in.Provenance["tag"] = f.in.Intent.Version, f.in.Intent.Tag
	if err := validate(f.in); err != nil {
		t.Fatal("valid prerelease/build ref rejected", err)
	}
}

// A subprocess deadline bounds the regression even if a future open blocks.
func TestC04FIFOAssetHelper(t *testing.T) {
	if os.Getenv("C2OPS_TEST_FIFO") == "" {
		return
	}
	f := newFixture(t)
	p := filepath.Join(f.in.InboxRoot, f.in.Files[0].Name)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := f.invoke(); err == nil || out != "" || len(f.reads)+len(f.writes) != 0 {
		t.Fatal("FIFO accepted")
	}
}
func TestC04FIFOAssetDoesNotBlock(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestC04FIFOAssetHelper$")
	cmd.Env = append(os.Environ(), "C2OPS_TEST_FIFO=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO rejection blocked or failed: %v %s", err, out)
	}
}
