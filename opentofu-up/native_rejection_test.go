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
)

// Faults are introduced after the real installed producer has completed a failed
// apply and written age ciphertext. This tests native c2j acceptance boundaries,
// separately from local encryption/write failures and the retain-then-fail caller.
func nativeResultRefusals(t *testing.T, helper, python, launcher string, manifest, request map[string]any, temp, effect, audit, stages, privateDirs string) {
	t.Helper()
	for _, tc := range []struct{ name, want string }{
		{"extra-field", "additional properties"},
		{"inconsistent-result", "output failed schema validation"},
		{"sealing-rejected", "object part path contains a symlink"},
	} {
		t.Run("native-result-refusal/"+tc.name, func(t *testing.T) {
			root := filepath.Join(temp, tc.name)
			if e := os.Mkdir(root, 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.Remove(effect); e != nil && !os.IsNotExist(e) {
				t.Fatal(e)
			}
			beforeAudit, e := os.ReadFile(audit)
			if e != nil {
				t.Fatal(e)
			}
			beforeStages, e := os.ReadFile(stages)
			if e != nil {
				t.Fatal(e)
			}
			producerReceipt := filepath.Join(root, "producer.json")
			later := filepath.Join(root, "later-ran")
			// Keep the package's schemas exactly. Only this fixture command adds a
			// post-execution fault; production has no output/path override switch.
			fault := filepath.Join(root, "fault.py")
			write(t, fault, `import json, os, pathlib, subprocess, sys
completed = subprocess.run([sys.argv[1]], stdin=sys.stdin.buffer, stdout=subprocess.PIPE, check=True)
envelope = json.loads(completed.stdout)
result = envelope["output"]
assert result["ok"] is False and result["outcome"] == "failed" and result["stage"] == "apply"
assert result["recovery"] == {"$object": "recovery"}
draft = envelope["objects"]["recovery"]
assert draft["type"] == "c2ops.opentofu.recovery/v1"
ciphertext = pathlib.Path(draft["files"]["ciphertext"])
assert ciphertext.read_bytes().startswith(b"age-encryption.org/v1")
pathlib.Path(sys.argv[3]).write_text(json.dumps({"ok": result["ok"], "outcome": result["outcome"], "stage": result["stage"], "ciphertext_written": True}))
if sys.argv[2] == "extra-field":
    result["unexpected_field"] = True
elif sys.argv[2] == "inconsistent-result":
    result["ok"] = True
elif sys.argv[2] == "sealing-rejected":
    payload = ciphertext.with_suffix(".payload")
    ciphertext.rename(payload)
    ciphertext.symlink_to(payload.name)
else:
    raise AssertionError("unknown fault")
print(json.dumps(envelope))
`)
			for _, name := range []string{"op", "later"} {
				if e := os.Mkdir(filepath.Join(root, name), 0700); e != nil {
					t.Fatal(e)
				}
			}
			m := map[string]any{}
			for k, v := range manifest {
				m[k] = v
			}
			m["command"] = []string{python, fault, launcher, tc.name, producerReceipt}
			mb, _ := json.Marshal(m)
			write(t, filepath.Join(root, "op/op.yaml"), string(mb))
			lm := map[string]any{"name": "later", "command": []string{python, "-c", "import pathlib,sys; pathlib.Path(sys.argv[1]).touch(); print('{\"output\":{}}')", later}, "input_schema": map[string]any{"type": "object"}, "output_schema": map[string]any{"type": "object"}}
			mb, _ = json.Marshal(lm)
			write(t, filepath.Join(root, "later/op.yaml"), string(mb))
			gitTest(t, root, "init", "--quiet")
			gitTest(t, root, "add", ".")
			gitTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "native rejection fixture")
			selector := "git+file://" + root + "//"
			recipe := map[string]any{"id": "native-rejection", "git_profile": nil, "perms": map[string]any{"jobdb": map[string]any{"secrets": map[string]any{"resolve": []string{"fixture_token"}}}}, "sequence": []any{
				map[string]any{"id": "deploy", "op": selector + "op@HEAD", "inputs": request},
				map[string]any{"id": "later", "op": selector + "later@HEAD", "inputs": map[string]any{}},
			}}
			rb, _ := json.Marshal(recipe)
			write(t, filepath.Join(root, "recipe.yaml"), string(rb))
			suite := map[string]any{"recipe": "recipe.yaml", "cases": []any{map[string]any{"id": tc.name, "type": "integration_case", "runtime": map[string]any{"expect_error": tc.want, "secrets": map[string]string{"fixture_token": string(validSecret(time.Now()))}}, "mocks": map[string]any{"ops": []any{map[string]any{"repeat": true, "match": map[string]any{"op": "extension_execution"}, "behavior": map[string]any{"mode": "passthrough"}}}}}}}
			sb, _ := json.Marshal(suite)
			write(t, filepath.Join(root, "suite.test.yaml"), string(sb))
			results := filepath.Join(root, "results")
			cmd := exec.Command(helper, "test", "run", "--file", filepath.Join(root, "suite.test.yaml"), "--format", "compact_yaml", "--out-dir", results)
			if b, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("native rejection %s: %v %s", tc.name, e, b)
			}
			b, e := os.ReadFile(producerReceipt)
			if e != nil || string(b) != `{"ok": false, "outcome": "failed", "stage": "apply", "ciphertext_written": true}` {
				t.Fatal("real failed producer did not complete ciphertext before rejection", string(b), e)
			}
			b, e = os.ReadFile(effect)
			if e != nil || string(b) != "created" || filePresent(later) {
				t.Fatal("missing actual effect or native rejection dispatched later consumer", e)
			}
			afterAudit, e := os.ReadFile(audit)
			if e != nil || string(afterAudit) != string(beforeAudit)+"secret get fixture_token --format=raw\n" {
				t.Fatal("rejection changed exact one-name resolution", e)
			}
			afterStages, e := os.ReadFile(stages)
			if e != nil || string(afterStages) != string(beforeStages)+"version\ninit\nvalidate\nplan\napply\n" {
				t.Fatal("rejection retried deployment or changed lifecycle", e)
			}
			dirs, e := os.ReadFile(privateDirs)
			if e != nil {
				t.Fatal(e)
			}
			for _, dir := range strings.Fields(string(dirs)) {
				if _, e := os.Stat(dir); !os.IsNotExist(e) {
					t.Fatal("private producer directory survived rejection", dir, e)
				}
			}
			report, e := os.ReadFile(filepath.Join(results, "summary.json"))
			if e != nil || !bytes.Contains(report, []byte(tc.want)) {
				t.Fatal("missing retained native rejection reason", e)
			}
			if tc.name == "sealing-rejected" && bytes.Contains(report, []byte("__c2j_objects__/")) {
				t.Fatal("rejected ciphertext acquired a sealed object artifact")
			}
			for _, sentinel := range []string{"private-token-sentinel", "BEGIN PRIVATE KEY", `\"name\": \"effect\"`} {
				if bytes.Contains(report, []byte(sentinel)) {
					t.Fatal("private data in rejection report")
				}
			}
			t.Logf("installed failed apply wrote ciphertext; native c2j refused %s (%s); no later consumer, retry or private producer directory", tc.name, tc.want)
		})
	}
}
