package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

func TestForwardSecretUsesOnlyExactParentBinding(t *testing.T) {
	root := t.TempDir()
	audit := filepath.Join(root, "audit")
	helper := filepath.Join(root, "c2j")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$AUDIT\"\nprintf '%s' 'private-forwarding-value'\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := &provider{forward: true, names: []string{"literal_key"}, parentHelper: helper, parentEnv: []string{"AUDIT=" + audit}}
	for _, names := range [][]string{{"other"}, {"literal_key", "other"}, {"$(false)"}, {}} {
		if _, err := p.ResolveSecrets(context.Background(), jobdb.SecretResolveRequest{Names: names}); err == nil {
			t.Fatal("ungranted parent resolution")
		}
	}
	if _, err := os.Stat(audit); !os.IsNotExist(err) {
		t.Fatal("helper invoked before allowlist check")
	}
	r, err := p.ResolveSecrets(context.Background(), jobdb.SecretResolveRequest{Names: []string{"literal_key"}})
	if err != nil || r.Secrets["literal_key"].Value != "private-forwarding-value" {
		t.Fatal("parent forwarding failed")
	}
	b, err := os.ReadFile(audit)
	if err != nil || string(b) != "secret get literal_key --format=raw\n" {
		t.Fatal("incorrect helper command")
	}
	p.deny.Store(true)
	if _, err = p.ResolveSecrets(context.Background(), jobdb.SecretResolveRequest{Names: []string{"literal_key"}}); err == nil {
		t.Fatal("deny ignored")
	}
}

func TestForwardSecretFailureDoesNotExposeOutput(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "c2j")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf private-forwarding-value >&2\nexit 9\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p := &provider{forward: true, names: []string{"literal_key"}, parentHelper: helper}
	_, err := p.ResolveSecrets(context.Background(), jobdb.SecretResolveRequest{Names: []string{"literal_key"}})
	if err == nil || strings.Contains(err.Error(), "private-forwarding-value") {
		t.Fatal("private helper failure exposed")
	}
}
