package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"filippo.io/age"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func TestNativePackagedRecovery(t *testing.T) {
	if os.Getenv("C2OPS_TEST_NATIVE") != "1" {
		t.Skip("native qualification requires C2OPS_TEST_NATIVE=1 and Nix/c2j")
	}
	i, tofu, cli := emulatorFixture(t)
	key, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	i.Recovery = &Recovery{Recipients: []string{key.Recipient().String()}}
	i.recipients = []age.Recipient{key.Recipient()}
	helper, e := exec.LookPath("c2j")
	if e != nil {
		t.Fatal(e)
	}
	gcs := &gcsFixture{objects: map[string][]byte{}, auth: "private-token-sentinel", t: t}
	emulator := startEmulator(t, gcs)
	gcs.storage = emulator.handler
	proxy, ca := googleProxy(t, localGoogleHandler(gcs), true)
	temp := t.TempDir()
	effect := filepath.Join(temp, "resource-effect")
	failFlag := filepath.Join(temp, "fail-state-write")
	failureMode := filepath.Join(temp, "enable-state-failure")
	stages := filepath.Join(temp, "tofu-stages")
	privateDirs := filepath.Join(temp, "private-directories")
	cancelMode := filepath.Join(temp, "cancel-mode")
	blockFlag := filepath.Join(temp, "block-plan")
	ready := filepath.Join(temp, "plan-ready")
	privateMeta := filepath.Join(temp, "private-processes")
	sleepPath, e := exec.LookPath("sleep")
	if e != nil {
		t.Fatal(e)
	}
	// A real terraform_data provisioner creates an external disposable file. The
	// GCS fixture rejects persistence only after the plan and resource effect.
	p := filepath.Join(i.Worktree, "main.tf")
	cfg, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	cfg = append(cfg, []byte(fmt.Sprintf("\nresource \"terraform_data\" \"effect\" {\n provisioner \"local-exec\" { command = %q }\n}\n", "printf created > "+shellQuote(effect)))...)
	write(t, p, string(cfg))
	gitTest(t, i.Worktree, "add", ".")
	gitTest(t, i.Worktree, "commit", "-qm", "native resource effect")
	i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
	gcs.failFlag = failFlag
	gcs.blockFlag, gcs.blockReady = blockFlag, ready
	// This is an explicitly isolated candidate package: only its fixed OpenTofu
	// dependency differs. Its wrapper binds the private test transport/mirror.
	// Production's package accepts no proxy, endpoint, or tool override inputs.
	root, e := filepath.Abs("..")
	if e != nil {
		t.Fatal(e)
	}
	wrapper := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + shellQuote(stages) + "\nprintf '%s\\n' \"$PWD\" >> " + shellQuote(privateDirs) + "\nif [ \"$1\" = apply ] && [ -f " + shellQuote(failureMode) + " ];then : > " + shellQuote(failFlag) + ";fi\n" +
		"if [ \"$1\" = plan ] && [ -f " + shellQuote(cancelMode) + " ];then\n" +
		" (trap '' INT TERM; exec " + shellQuote(sleepPath) + " 300) &\n" +
		" printf '%s\\n%s\\n%s\\n' \"$$\" \"$!\" \"$PWD\" > " + shellQuote(privateMeta) + "\n" +
		" : > " + shellQuote(blockFlag) + "\nfi\n" +
		"export HTTPS_PROXY=" + shellQuote(proxy) + "\nexport SSL_CERT_FILE=" + shellQuote(ca) + "\nexport TF_CLI_CONFIG_FILE=" + shellQuote(cli) + "\n" +
		"if [ \"$1\" = plan ] && [ -f " + shellQuote(cancelMode) + " ];then\n" +
		" trap '' INT TERM\n " + shellQuote(tofu) + " \"$@\" &\n wait\nelse\n exec " + shellQuote(tofu) + " \"$@\"\nfi\n"
	expr := fmt.Sprintf(`let
 root = builtins.toPath %q;
 base = (builtins.getFlake (toString root)).inputs.nixpkgs.legacyPackages.${builtins.currentSystem};
 tool = (base.writeShellScriptBin "tofu" %q) // { version = "1.13.1"; };
 in (import (root + "/nix/packages.nix") { pkgs = base // { opentofu = tool; }; }).opentofu-up`, root, wrapper)
	cmd := exec.Command("nix", "build", "--max-jobs", "2", "--no-link", "--print-out-paths", "--impure", "--expr", expr)
	b, e := cmd.Output()
	if e != nil {
		if x, ok := e.(*exec.ExitError); ok {
			t.Log(string(x.Stderr))
		}
		t.Fatal(e)
	}
	pkg := strings.TrimSpace(string(b))
	if !strings.HasPrefix(pkg, "/nix/store/") {
		t.Fatal("missing package output")
	}
	manifestBytes, e := os.ReadFile(filepath.Join(pkg, "share/c2j/op.json"))
	if e != nil {
		t.Fatal(e)
	}
	var manifest map[string]any
	if e = json.Unmarshal(manifestBytes, &manifest); e != nil {
		t.Fatal(e)
	}
	audit := filepath.Join(temp, "secret-audit")
	bindingDir := filepath.Join(temp, "helpers")
	if e = makePrivate(bindingDir); e != nil {
		t.Fatal(e)
	}
	helperScript := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(audit) + "\nexec " + shellQuote(helper) + " \"$@\"\n"
	helperWrapper := filepath.Join(bindingDir, "c2j")
	write(t, helperWrapper, helperScript)
	_ = os.Chmod(helperWrapper, 0700)
	// Exercise the installed candidate's refusal boundary, not a command stub:
	// Unchecked dialects, mutable archives and generated authentication blocks
	// must reach neither helper nor tofu.
	for _, name := range []string{"main.tofu", "main.tofu.json", "override.tofu", "mutable-archive", "dynamic-auth", "dynamic-auth-json"} {
		t.Run("packaged-refusal/"+name, func(t *testing.T) {
			unsafe, _ := fixture(t)
			switch name {
			case "dynamic-auth":
				unsafe.Auth = Auth{Mode: "service_account_json", SecretName: "fixture_token", Expected: "fixture@fixture.iam.gserviceaccount.com"}
				write(t, filepath.Join(unsafe.Worktree, "auth.tf"), dynamicExternalCredentials)
			case "dynamic-auth-json":
				write(t, filepath.Join(unsafe.Worktree, "auth.tf.json"), dynamicExternalCredentialsJSON)
			case "mutable-archive":
				b, e := os.ReadFile(filepath.Join(unsafe.Worktree, "main.tf"))
				if e != nil {
					t.Fatal(e)
				}
				write(t, filepath.Join(unsafe.Worktree, "main.tf"), string(b)+fmt.Sprintf("\nmodule \"mutable\" { source = %q }\n", "https://example.invalid/module.zip?ref="+strings.Repeat("a", 40)))
			default:
				write(t, filepath.Join(unsafe.Worktree, name), `terraform { backend "local" {} }`)
			}
			gitTest(t, unsafe.Worktree, "add", ".")
			gitTest(t, unsafe.Worktree, "commit", "-qm", "packaged refusal")
			unsafe.Revision = gitTest(t, unsafe.Worktree, "rev-parse", "HEAD")
			input, _ := json.Marshal(unsafe)
			cmd := exec.Command(filepath.Join(pkg, "bin/opentofu-up"))
			cmd.Env = append(os.Environ(), "PATH="+bindingDir+":"+os.Getenv("PATH"))
			cmd.Stdin = bytes.NewReader(input)
			if _, e := cmd.CombinedOutput(); e == nil || filePresent(audit) || filePresent(stages) {
				t.Fatal("packaged source refusal crossed credential/tool boundary")
			}
		})
	}
	if t.Failed() {
		t.FailNow()
	}
	launcher := filepath.Join(temp, "launch")
	write(t, launcher, "#!/bin/sh\nexport PATH="+shellQuote(bindingDir)+":\"$PATH\"\nexec "+shellQuote(filepath.Join(pkg, "bin/opentofu-up"))+"\n")
	_ = os.Chmod(launcher, 0700)
	manifest["command"] = []string{launcher}
	manifestDir := filepath.Join(temp, "manifest")
	if e = makePrivate(filepath.Join(manifestDir, "op")); e != nil {
		t.Fatal(e)
	}
	mb, _ := json.Marshal(manifest)
	write(t, filepath.Join(manifestDir, "op/op.yaml"), string(mb))
	gitTest(t, manifestDir, "init", "--quiet")
	gitTest(t, manifestDir, "add", ".")
	gitTest(t, manifestDir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "packaged manifest")
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Fatal(e)
	}
	consumerSource, e := filepath.Abs("examples/retain_result.py")
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"retain", "assert"} {
		if e = makePrivate(filepath.Join(manifestDir, name)); e != nil {
			t.Fatal(e)
		}
	}
	objectSchema := map[string]any{"type": "object", "x-c2j-object-type": "c2ops.opentofu.recovery/v1"}
	retainManifest := map[string]any{"name": "retain", "command": []string{python, consumerSource}, "input_schema": map[string]any{"type": "object", "properties": map[string]any{"result": map[string]any{"type": "object", "properties": map[string]any{"recovery": objectSchema}}, "expected": map[string]any{"type": "object"}, "recovery": objectSchema, "artifact_outbox_path": map[string]any{"type": "string", "default": "{{ context.environment.op.outbox }}"}}}, "output_schema": map[string]any{"type": "object", "properties": map[string]any{"recovery": objectSchema, "accepted": map[string]any{"type": "boolean"}, "result": map[string]any{"type": "object"}}}}
	assertManifest := map[string]any{"name": "assert", "command": []string{python, consumerSource, "assert"}, "input_schema": map[string]any{"type": "object"}, "output_schema": map[string]any{"type": "object", "required": []string{"ok"}, "properties": map[string]any{"ok": map[string]any{"const": true}}}}
	for name, m := range map[string]any{"retain": retainManifest, "assert": assertManifest} {
		b, _ := json.Marshal(m)
		write(t, filepath.Join(manifestDir, name, "op.yaml"), string(b))
	}
	gitTest(t, manifestDir, "add", ".")
	gitTest(t, manifestDir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "caller fixture")
	requestBytes, _ := json.Marshal(i)
	var request map[string]any
	_ = json.Unmarshal(requestBytes, &request)
	selector := "git+file://" + manifestDir + "//"
	successResults := []string{}
	var persisted []byte
	var markerGeneration string
	for number, tc := range []struct {
		name, mode, outcome string
		changed             bool
		commands            string
	}{
		{"changed-preview", "plan", "planned", true, "version\ninit\nvalidate\nplan\n"},
		{"changed-apply", "up", "applied", true, "version\ninit\nvalidate\nplan\napply\n"},
		{"unchanged-up", "up", "no_changes", false, "version\ninit\nvalidate\nplan\n"},
		{"unchanged-preview", "plan", "no_changes", false, "version\ninit\nvalidate\nplan\n"},
	} {
		t.Run("native-success/"+tc.name, func(t *testing.T) {
			public := map[string]any{}
			for k, v := range request {
				public[k] = v
			}
			public["mode"] = tc.mode
			direct := map[string]any{"id": "native-success", "git_profile": nil, "perms": map[string]any{"jobdb": map[string]any{"secrets": map[string]any{"resolve": []string{"fixture_token"}}}}, "op": selector + "op@HEAD", "inputs": public}
			rb, _ := json.Marshal(direct)
			write(t, filepath.Join(temp, "success-recipe.yaml"), string(rb))
			assertions := []any{}
			for _, expectation := range []struct {
				path  string
				value any
			}{
				{"ok", true}, {"outcome", tc.outcome}, {"stage", "complete"}, {"changes_present", tc.changed}, {"revision", i.Revision}, {"root", i.Root}, {"backend", i.Backend},
			} {
				assertions = append(assertions, map[string]any{"type": "output_equals", "path": expectation.path, "value": expectation.value})
			}
			assertions = append(assertions, map[string]any{"type": "cel_true", "expr": "!has(outputs.recovery)"})
			success := map[string]any{"recipe": "success-recipe.yaml", "cases": []any{map[string]any{"id": tc.name, "type": "integration_case", "runtime": map[string]any{"secrets": map[string]string{"fixture_token": string(validSecret(time.Now()))}}, "mocks": map[string]any{"ops": []any{map[string]any{"repeat": true, "match": map[string]any{"op": "extension_execution"}, "behavior": map[string]any{"mode": "passthrough"}}}}, "assertions": assertions}}}
			sb, _ := json.Marshal(success)
			write(t, filepath.Join(temp, "success.test.yaml"), string(sb))
			result := filepath.Join(temp, tc.name)
			cmd := exec.Command(helper, "test", "run", "--file", filepath.Join(temp, "success.test.yaml"), "--format", "compact_yaml", "--out-dir", result)
			if b, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("native success %s: %v %s", tc.name, e, b)
			}
			successResults = append(successResults, result)
			ab, e := os.ReadFile(audit)
			if e != nil || string(ab) != strings.Repeat("secret get fixture_token --format=raw\n", number+1) {
				t.Fatal("success invocation must resolve exactly once", string(ab), e)
			}
			stageBytes, e := os.ReadFile(stages)
			if e != nil || string(stageBytes) != tc.commands {
				t.Fatal("unexpected successful lifecycle", string(stageBytes), e)
			}
			dirs, e := os.ReadFile(privateDirs)
			if e != nil {
				t.Fatal(e)
			}
			for _, dir := range strings.Fields(string(dirs)) {
				if _, e := os.Stat(dir); !os.IsNotExist(e) {
					t.Fatal("private successful invocation survived", dir, e)
				}
			}
			if e := os.Remove(stages); e != nil {
				t.Fatal(e)
			}
			if e := os.Remove(privateDirs); e != nil {
				t.Fatal(e)
			}
			if number == 0 {
				if filePresent(effect) {
					t.Fatal("preview created a resource effect")
				}
			} else {
				b, e := os.ReadFile(effect)
				if e != nil || string(b) != "created" {
					t.Fatal("successful resource effect missing", e)
				}
			}
			state, _, stateStatus := emulator.object("fixture/state/default.tfstate")
			marker, generation, markerStatus := emulator.object("fixture/marker")
			if number == 0 {
				if markerStatus != 404 || stateStatus == 200 && bytes.Contains(state, []byte(`"type": "google_storage_bucket_object"`)) {
					t.Fatal("preview created resource/state")
				}
			} else {
				if markerStatus != 200 || string(marker) != "opentofu integration marker" || generation == "" {
					t.Fatal("emulator marker missing")
				}
				if number == 1 {
					markerGeneration = generation
				} else if generation != markerGeneration {
					t.Fatal("unchanged invocation created marker generation")
				}
			}
			if number == 1 {
				var saved struct{ Resources []struct{ Name, Type string } }
				if e := json.Unmarshal(state, &saved); e != nil {
					t.Fatal("invalid persisted state", e)
				}
				found := false
				for _, resource := range saved.Resources {
					if resource.Name == "effect" && resource.Type == "terraform_data" {
						found = true
					}
				}
				if !found {
					t.Fatal("successful state was not persisted to GCS fixture")
				}
				persisted = state
			} else if number > 1 && !bytes.Equal(state, persisted) {
				t.Fatal("no-change invocation modified persisted state")
			}
			t.Logf("installed fixture package %s: ok=true outcome=%s; exact lifecycle; one helper read; private cleanup", pkg, tc.outcome)
		})
	}
	if t.Failed() {
		t.FailNow()
	}
	// Start independent persistence-fault scenarios from a fresh controlled
	// namespace. No production resource/backend repair is performed by the op.
	if e := os.Remove(effect); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(audit); e != nil {
		t.Fatal(e)
	}
	emulator.reset(t)
	write(t, failureMode, "enable test-only persistence fault")
	recipe := map[string]any{"id": "native-tofu", "git_profile": nil, "perms": map[string]any{"jobdb": map[string]any{"secrets": map[string]any{"resolve": []string{"fixture_token"}}}}, "sequence": []any{
		map[string]any{"id": "deploy", "op": selector + "op@HEAD", "inputs": request},
		map[string]any{"id": "retain", "op": selector + "retain@HEAD", "inputs": map[string]any{"result": "${{ sequence.deploy.outputs }}", "recovery": "${{ sequence.deploy.outputs.recovery }}", "expected": map[string]any{"mode": i.Mode, "revision": i.Revision, "root": i.Root, "backend": i.Backend}}},
		map[string]any{"id": "require-success", "op": selector + "assert@HEAD", "inputs": map[string]any{"accepted": "${{ sequence.retain.outputs.accepted }}"}},
	}}
	rb, _ := json.Marshal(recipe)
	write(t, filepath.Join(temp, "recipe.yaml"), string(rb))
	suite := map[string]any{"recipe": "recipe.yaml", "cases": []any{map[string]any{"id": "failed-apply-seals-ciphertext", "type": "integration_case", "runtime": map[string]any{"expect_error": "jsonschema validation failed", "observe": map[string]any{"scopes": []any{map[string]any{"node_path": "native-tofu/deploy", "outputs": []string{"ok", "stage", "outcome"}}, map[string]any{"node_path": "native-tofu/retain", "outputs": []string{"accepted", "result", "recovery"}, "retain_artifacts": []string{"deployment.json", "recovery.age"}}}}, "secrets": map[string]string{"fixture_token": string(validSecret(time.Now()))}}, "mocks": map[string]any{"ops": []any{map[string]any{"repeat": true, "match": map[string]any{"op": "extension_execution"}, "behavior": map[string]any{"mode": "passthrough"}}}}, "assertions": []any{map[string]any{"type": "cel_true", "expr": "observations.size() == 2 && observations[1].outputs.accepted == false && observations[1].outputs.recovery.type == 'c2ops.opentofu.recovery/v1'"}}}}}

	sb, _ := json.Marshal(suite)
	write(t, filepath.Join(temp, "suite.test.yaml"), string(sb))
	results := filepath.Join(temp, "results")
	cmd = exec.CommandContext(context.Background(), helper, "test", "run", "--file", filepath.Join(temp, "suite.test.yaml"), "--format", "compact_yaml", "--out-dir", results, "--artifact-mode", "inline", "--artifact-max-bytes", "1048576")
	b, e = cmd.CombinedOutput()
	if e != nil {
		report, _ := os.ReadFile(filepath.Join(results, "summary.json"))
		t.Logf("native diagnostics: %s", report)
		t.Fatalf("native package failed: %v %s", e, b)
	}
	if marker, _, status := emulator.object("fixture/marker"); status != 200 || string(marker) != "opentofu integration marker" {
		t.Fatal("post-effect failure missing marker")
	}
	if effectBytes, e := os.ReadFile(effect); e != nil || string(effectBytes) != "created" {
		t.Fatal("actual resource effect missing")
	}
	ab, e := os.ReadFile(audit)
	if e != nil || string(ab) != "secret get fixture_token --format=raw\n" {
		t.Fatal("native helper did not read exactly once", string(ab), e)
	}
	// A separate native invocation must resolve again and create a new plan.
	// This deliberately uses a new runtime; it is not completed-task replay.
	if e = os.Remove(effect); e != nil {
		t.Fatal(e)
	}
	freshResults := filepath.Join(temp, "fresh-results")
	cmd = exec.Command(helper, "test", "run", "--file", filepath.Join(temp, "suite.test.yaml"), "--format", "compact_yaml", "--out-dir", freshResults)
	if b, e = cmd.CombinedOutput(); e != nil {
		t.Fatalf("fresh native invocation failed: %v %s", e, b)
	}
	if marker, _, status := emulator.object("fixture/marker"); status != 200 || string(marker) != "opentofu integration marker" {
		t.Fatal("post-effect failure missing marker")
	}
	if effectBytes, e := os.ReadFile(effect); e != nil || string(effectBytes) != "created" {
		t.Fatal("fresh invocation did not produce its resource effect")
	}
	ab, e = os.ReadFile(audit)
	if e != nil || string(ab) != strings.Repeat("secret get fixture_token --format=raw\n", 2) {
		t.Fatal("fresh invocation did not resolve exactly once", string(ab), e)
	}
	stageBytes, e := os.ReadFile(stages)
	if e != nil || string(stageBytes) != strings.Repeat("version\ninit\nvalidate\nplan\napply\n", 2) {
		t.Fatal("fresh invocation did not create and apply its own plan", string(stageBytes), e)
	}
	// Scan persisted local harness reports, not only stdout.
	for _, directory := range append(successResults, results, freshResults) {
		e = filepath.WalkDir(directory, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			for _, private := range []string{"private-token-sentinel", "private_key", "BEGIN PRIVATE KEY"} {
				if bytes.Contains(b, []byte(private)) {
					return fmt.Errorf("private fixture data in history")
				}
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	gcs.Lock()
	locks := gcs.locks
	gcs.Unlock()
	if locks == 0 {
		t.Fatal("no conditional backend locks observed")
	}
	// Retrieval happens through a separate native consumer after producer cleanup.
	reportBytes, e := os.ReadFile(filepath.Join(results, "summary.json"))
	if e != nil {
		t.Fatal(e)
	}
	var report struct {
		Results []struct {
			Run struct {
				Artifacts map[string]struct {
					Content string `json:"content_base64"`
				} `json:"artifacts"`
			} `json:"run"`
		} `json:"results"`
	}
	if e = json.Unmarshal(reportBytes, &report); e != nil {
		t.Fatal(e)
	}
	if len(report.Results) != 1 {
		t.Fatal("missing native result")
	}
	var cipher []byte
	for _, a := range report.Results[0].Run.Artifacts {
		b, e := base64.StdEncoding.DecodeString(a.Content)
		if e == nil && bytes.HasPrefix(b, []byte("age-encryption.org/v1")) {
			cipher = b
		}
	}
	if len(cipher) == 0 {
		t.Fatal("ciphertext retrieval missing")
	}

	plain, e := age.Decrypt(bytes.NewReader(cipher), key)
	if e != nil {
		t.Fatal(e)
	}
	tr := tar.NewReader(plain)
	found := false
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(tr)
		if strings.HasSuffix(h.Name, "errored.tfstate") && bytes.Contains(b, []byte(`"name": "effect"`)) {
			found = true
		}
	}
	if !found {
		t.Fatal("real emergency state not recovered")
	}
	gcs.Lock()
	before := gcs.requests
	gcs.Unlock()
	for _, kind := range []string{"denied", "wrong-scope", "foreign-ceiling"} {
		grants := []string{}
		if kind == "wrong-scope" {
			grants = []string{"other_token"}
		}
		direct := map[string]any{"id": "native-secret", "git_profile": nil, "perms": map[string]any{"jobdb": map[string]any{"secrets": map[string]any{"resolve": grants}}}, "op": selector + "op@HEAD", "inputs": request}
		runtime := map[string]any{"secrets": map[string]string{"fixture_token": string(validSecret(time.Now()))}}
		mocks := map[string]any{"ops": []any{map[string]any{"repeat": true, "match": map[string]any{"op": "extension_execution"}, "behavior": map[string]any{"mode": "passthrough"}}}}
		assertions := []any{map[string]any{"type": "output_equals", "path": "ok", "value": false}, map[string]any{"type": "output_equals", "path": "stage", "value": "credentials"}}
		if kind == "foreign-ceiling" {
			direct = map[string]any{"id": "native-secret", "git_profile": nil, "perms": map[string]any{"limit": map[string]any{"jobdb": map[string]any{}}}, "sequence": []any{map[string]any{"id": "foreign", "perms": map[string]any{"jobdb": map[string]any{"secrets": map[string]any{"resolve": []string{"fixture_token"}}}}, "op": selector + "op@HEAD", "inputs": request}}}
			runtime["expect_error"] = "exceed"
			mocks = map[string]any{}
			assertions = nil
		}
		rb, _ := json.Marshal(direct)
		write(t, filepath.Join(temp, "recipe.yaml"), string(rb))
		check := map[string]any{"recipe": "recipe.yaml", "cases": []any{map[string]any{"id": kind, "type": "integration_case", "runtime": runtime, "mocks": mocks, "assertions": assertions}}}
		sb, _ := json.Marshal(check)
		write(t, filepath.Join(temp, "suite.test.yaml"), string(sb))
		cmd := exec.Command(helper, "test", "run", "--file", filepath.Join(temp, "suite.test.yaml"), "--format", "compact_yaml", "--out-dir", filepath.Join(temp, "secret-"+kind))
		b, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("native %s: %v %s", kind, e, b)
		}
	}
	gcs.Lock()
	after := gcs.requests
	gcs.Unlock()
	if after != before {
		t.Fatal("cloud client ran after denied authority")
	}
	ab, e = os.ReadFile(audit)
	if e != nil || strings.Count(string(ab), "secret get fixture_token --format=raw\n") != 4 {
		t.Fatal("unexpected helper reads at scope boundaries", string(ab), e)
	}
	nativeResultRefusals(t, helper, python, launcher, manifest, request, temp, effect, audit, stages, privateDirs)
	if t.Failed() {
		t.FailNow()
	}
	// Retain SQLite and own the worker for checks unavailable through CLI reports.
	driver := filepath.Join(temp, "runtime-fixture")
	cmd = exec.Command("go", "build", "-o", driver, ".")
	cmd.Dir = filepath.Join(root, "opentofu-runtime-fixture")
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("embedded runtime build: %v %s", e, b)
	}
	nativeLiveForwarding(t, driver, helper, temp, selector, request, key)
	for _, mode := range []string{"replay", "cancel"} {
		caseRoot := filepath.Join(temp, "embedded-"+mode)
		if e := os.Mkdir(caseRoot, 0700); e != nil {
			t.Fatal(e)
		}
		direct := map[string]any{"id": "native-embedded", "git_profile": nil, "perms": map[string]any{"jobdb": map[string]any{"secrets": map[string]any{"resolve": []string{"fixture_token"}}}}, "op": selector + "op@HEAD", "inputs": request}
		if mode == "cancel" {
			delete(direct, "op")
			delete(direct, "inputs")
			direct["sequence"] = []any{
				map[string]any{"id": "deploy", "op": selector + "op@HEAD", "inputs": request},
				map[string]any{"id": "later", "op": selector + "op@HEAD", "inputs": request},
			}
		}
		rb, _ := json.Marshal(direct)
		recipePath := filepath.Join(caseRoot, "recipe.yaml")
		write(t, recipePath, string(rb))
		config := map[string]string{"Recipe": recipePath, "Repo": i.Worktree, "Revision": i.Revision, "Root": caseRoot, "HelperAudit": audit, "Stages": stages, "PrivateMeta": privateMeta}
		if mode == "cancel" {
			config["CancelMode"], config["Ready"] = cancelMode, ready
		}
		cb, _ := json.Marshal(config)
		cmd = exec.Command(driver)
		cmd.Stdin = bytes.NewReader(cb)
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("embedded %s: %v %s", mode, e, b)
		} else {
			t.Logf("embedded %s: %s", mode, b)
		}
	}
	t.Log("real resource effect; failed apply; native typed ciphertext sealing; one scoped helper read per invocation; fresh invocation replans; safe persisted reports", pkg)
}
