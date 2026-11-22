package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

// Cross the same parent-helper and typed-retainer boundary used by the live
// driver, with a local credential and the emulator's persistence fault only.
func nativeLiveForwarding(t *testing.T, driver, helper, temp, selector string, request map[string]any, key *age.X25519Identity) {
	t.Helper()
	root := filepath.Join(temp, "live-forwarding")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	helpers := filepath.Join(root, "helpers")
	if err := os.Mkdir(helpers, 0700); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(root, "credential.json")
	if err := os.WriteFile(token, validSecret(time.Now()), 0600); err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(root, "parent-reads")
	script := "#!/bin/sh\nif [ \"$1 $2 $3 $4\" = 'secret get fixture_token --format=raw' ]; then\n printf read\\n >> " + shellQuote(audit) + "\n cat " + shellQuote(token) + "\nelse\n exec " + shellQuote(helper) + " \"$@\"\nfi\n"
	if err := os.WriteFile(filepath.Join(helpers, "c2j"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	recipe := map[string]any{"id": "live-forwarding", "git_profile": nil, "perms": map[string]any{"jobdb": map[string]any{"secrets": map[string]any{"resolve": []string{"fixture_token"}}}}, "sequence": []any{
		map[string]any{"id": "deploy", "op": selector + "op@HEAD", "inputs": request},
		map[string]any{"id": "retain", "op": selector + "retain@HEAD", "inputs": map[string]any{"result": "${{ sequence.deploy.outputs }}", "expected": map[string]any{"mode": request["mode"], "revision": request["revision"], "root": request["root"], "backend": request["backend"]}}},
	}, "outputs": map[string]any{"accepted": "${{ sequence.retain.outputs.accepted }}", "result": "${{ sequence.retain.outputs.result }}"}}
	rb, _ := json.Marshal(recipe)
	rp := filepath.Join(root, "recipe.json")
	if err := os.WriteFile(rp, rb, 0600); err != nil {
		t.Fatal(err)
	}
	result, recovery := filepath.Join(root, "result.json"), filepath.Join(root, "recovery.age")
	c := map[string]any{"Recipe": rp, "Repo": request["worktree_path"], "Revision": request["revision"], "Root": root, "Live": true, "SecretNames": []string{"fixture_token"}, "ResultPath": result, "RecoveryPath": recovery, "TotalSeconds": 120}
	cb, _ := json.Marshal(c)
	cmd := exec.Command(driver)
	cmd.Stdin = bytes.NewReader(cb)
	cmd.Env = append(os.Environ(), "PATH="+helpers+string(os.PathListSeparator)+os.Getenv("PATH"))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("live forwarding local fixture: %v %s", err, b)
	}
	b, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Accepted bool `json:"accepted"`
		Result   struct {
			OK      bool   `json:"ok"`
			Outcome string `json:"outcome"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &value) != nil || value.Accepted || value.Result.OK || value.Result.Outcome != "failed" {
		t.Fatalf("failed deployment data not retained: %s", b)
	}
	cipher, err := os.ReadFile(recovery)
	if err != nil {
		t.Fatal("live adapter failed to retrieve ciphertext", err)
	}
	if _, err = age.Decrypt(bytes.NewReader(cipher), key); err != nil {
		t.Fatal("live adapter ciphertext invalid", err)
	}
	reads, err := os.ReadFile(audit)
	if err != nil || strings.Count(string(reads), "read") != 1 {
		t.Fatal("parent helper not called exactly once")
	}
	if bytes.Contains(b, []byte("private-token-sentinel")) {
		t.Fatal("credential in retained result")
	}
	t.Log("live adapter: one exact parent resolution; failed result and decryptable ciphertext retained")
}
