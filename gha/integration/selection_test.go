package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const matchingRevision = "0123456789abcdef0123456789abcdef01234567"
const matchingPrefix = "nix:github:colony-2/c2ops/" + matchingRevision
const otherHelper = "nix:github:colony-2/c2ops/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa#gha-workflow"

// A transport fixture, deliberately outside production. Capture each original
// recipe before translating only op nodes to installed local fixtures. Opaque
// payloads retain their original immutable/explicit selectors throughout c2j.
func selectionProxy() int {
	target, logPath := os.Args[2], os.Args[3]
	var aliases map[string]string
	aliasData, err := os.ReadFile(os.Args[4])
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(aliasData, &aliases); err != nil {
		panic(err)
	}
	endpoint := os.Getenv("C2J_CHILD_JOB_ENDPOINT")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			panic(err)
		}
		var request map[string]any
		if err = json.Unmarshal(raw, &request); err != nil {
			panic(err)
		}
		for _, entry := range request["embedded_recipes"].([]any) {
			item := entry.(map[string]any)
			original, err := base64.StdEncoding.DecodeString(item["yaml"].(string))
			if err != nil {
				panic(err)
			}
			var recipe map[string]any
			if err = yaml.Unmarshal(original, &recipe); err != nil {
				panic(err)
			}
			file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				panic(err)
			}
			if err = json.NewEncoder(file).Encode(recipe); err != nil {
				panic(err)
			}
			file.Close()
			var walk func(any)
			walk = func(value any) {
				switch value := value.(type) {
				case map[string]any:
					if op, ok := value["op"].(string); ok {
						if alias, ok := aliases[op]; ok {
							value["op"] = alias
						}
					}
					for _, child := range value {
						walk(child)
					}
				case []any:
					for _, child := range value {
						walk(child)
					}
				}
			}
			walk(recipe)
			encoded, err := yaml.Marshal(recipe)
			if err != nil {
				panic(err)
			}
			item["yaml"] = base64.StdEncoding.EncodeToString(encoded)
		}
		raw, err = json.Marshal(request)
		if err != nil {
			panic(err)
		}
		forward, err := http.NewRequest("POST", endpoint, bytes.NewReader(raw))
		if err != nil {
			panic(err)
		}
		forward.Header = r.Header.Clone()
		response, err := http.DefaultClient.Do(forward)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer response.Body.Close()
		w.WriteHeader(response.StatusCode)
		io.Copy(w, response.Body)
	}))
	defer proxy.Close()
	cmd := exec.Command(target)
	cmd.Env = append(os.Environ(), "C2J_CHILD_JOB_ENDPOINT="+proxy.URL, "GORACE=atexit_sleep_ms=0")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if len(os.Args) > 5 && os.Args[5] == "repeat" {
		first := exec.Command(target)
		first.Env, first.Stdin, first.Stdout, first.Stderr = cmd.Env, bytes.NewReader(nil), io.Discard, os.Stderr
		payload, err := io.ReadAll(os.Stdin)
		if err != nil {
			panic(err)
		}
		first.Stdin = bytes.NewReader(payload)
		cmd.Stdin = bytes.NewReader(payload)
		if err := first.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func selectionRecords(t *testing.T, path string) []map[string]any {
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var records []map[string]any
	for decoder.More() {
		var record map[string]any
		require.NoError(t, decoder.Decode(&record))
		records = append(records, record)
	}
	return records
}

func TestMatchingWorkersPersistAcrossNativeRuntimeRestart(t *testing.T) {
	for _, facade := range []bool{false, true} {
		for _, overrides := range []struct{ name, helper, executor string }{{"automatic", "", ""}, {"helper", otherHelper, ""}, {"executor", "", publicJobOp}, {"both", otherHelper, publicJobOp}, {"repeat", "", ""}} {
			t.Run(fmt.Sprintf("facade=%t/%s", facade, overrides.name), func(t *testing.T) {
				h := newHarness(t, nil)
				h.secrets["build_key"] = "synthetic-value"
				installJobFixture(t, h, `on: workflow_dispatch
jobs:
  plan:
    runs-on: local
    outputs:
      matrix: ${{ steps.out.outputs.matrix }}
    steps:
      - id: out
        run: echo 'matrix={"shard":["one","two"]}' >> "$GITHUB_OUTPUT"
  build:
    needs: plan
    runs-on: local
    strategy:
      fail-fast: false
      matrix: ${{ fromJSON(needs.plan.outputs.matrix) }}
    steps:
      - env:
          TOKEN: ${{ secrets.KEY }}
        run: test "$TOKEN" = synthetic-value
`)
				logPath := filepath.Join(h.root, "selectors.jsonl")
				aliases := map[string]string{matchingPrefix + "#gha-workflow": "./.c2/test-ops/gha-workflow", matchingPrefix + "#gha-job": "./.c2/test-ops/gha-job", otherHelper: "./.c2/test-ops/gha-workflow", publicJobOp: "./.c2/test-ops/gha-job", "nix:github:colony-2/c2ops/published#gha-workflow": "./.c2/test-ops/gha-workflow"}
				raw, err := json.Marshal(aliases)
				require.NoError(t, err)
				aliasPath := filepath.Join(h.root, "fixture-package-aliases.json")
				require.NoError(t, os.WriteFile(aliasPath, raw, 0600))
				proxyCommand := func(binary string) []string {
					return []string{os.Args[0], "gha-selection-proxy", binary, logPath, aliasPath}
				}
				// Expansion deliberately uses an unversioned helper, proving it consumes
				// the submitting binary's stored selectors rather than recomputing them.
				raw, err = os.ReadFile("../workflow/op.json")
				require.NoError(t, err)
				var helperManifest map[string]any
				require.NoError(t, json.Unmarshal(raw, &helperManifest))
				helperManifest["command"] = proxyCommand(workflowBinary)
				// Keep race detection while avoiding its per-process exit delay
				// in the recording transport and product executables.
				helperManifest["env"] = map[string]string{"GORACE": "atexit_sleep_ms=0"}
				delete(helperManifest, "dependencies")
				raw, err = json.Marshal(helperManifest)
				require.NoError(t, err)
				h.seed(map[string]string{".c2/test-ops/gha-workflow/op.yaml": string(raw)})
				var manifest map[string]any
				path := "../workflow/op.json"
				binary := matchingWorkflowBinary
				local := "gha-workflow-launch"
				if facade {
					path = "../op.json"
					binary = matchingGHABinary
					local = "gha-launch"
				}
				sourceManifest := overrides.name == "automatic"
				if sourceManifest {
					path = strings.TrimSuffix(path, ".json") + ".yaml"
				}
				raw, err = os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, yaml.Unmarshal(raw, &manifest))
				if sourceManifest {
					name := "gha-workflow"
					if facade {
						name = "gha"
					}
					// Model the existing qualified-command package binding;
					// use the actual versioned executable, never source compilation at invocation.
					require.Equal(t, []any{"nix", "run", "github:colony-2/c2ops/published#" + name, "--"}, manifest["command"])
				}
				manifest["command"] = proxyCommand(binary)
				manifest["env"] = map[string]string{"GORACE": "atexit_sleep_ms=0"}
				if overrides.name == "repeat" {
					manifest["command"] = append(proxyCommand(binary), "repeat")
				}
				delete(manifest, "dependencies")
				raw, err = json.Marshal(manifest)
				require.NoError(t, err)
				h.seed(map[string]string{".c2/test-ops/" + local + "/op.yaml": string(raw)})
				extra := "      secret_bindings: {KEY: build_key}"
				if facade {
					extra += "\n      backend: c2j"
				}
				var recipeData map[string]any
				require.NoError(t, yaml.Unmarshal([]byte(workflowRecipe(t, extra)), &recipeData))
				sequence := recipeData["sequence"].([]any)
				launch := sequence[0].(map[string]any)
				launch["op"] = "./.c2/test-ops/" + local
				inputs := launch["inputs"].(map[string]any)
				delete(inputs, "helper_op")
				delete(inputs, "executor_op")
				if overrides.helper != "" {
					inputs["helper_op"] = overrides.helper
				}
				if overrides.executor != "" {
					inputs["executor_op"] = overrides.executor
				}
				// Force a real unpublished source bootstrap, independently of release identity.
				recipeData["sequence"] = append([]any{map[string]any{"id": "candidate", "op": "command_execution", "inputs": map[string]any{"run": "echo candidate > candidate.txt"}}}, sequence...)
				recipeBytes, err := yaml.Marshal(recipeData)
				require.NoError(t, err)
				recipe := string(recipeBytes)
				root := h.submit(recipe, nil)
				require.Equal(t, workflow.JobRunSuspended, h.step(root).Status)
				helper := overrides.helper
				if helper == "" {
					helper = matchingPrefix + "#gha-workflow"
				}
				executor := overrides.executor
				if executor == "" {
					executor = matchingPrefix + "#gha-job"
				}
				initial := selectionRecords(t, logPath)
				initialCount := 3
				if overrides.name == "repeat" {
					initialCount = 6
				}
				require.Len(t, initial, initialCount)
				for _, record := range initial {
					seq := record["sequence"].([]any)
					require.Equal(t, helper, seq[0].(map[string]any)["op"])
					for _, entry := range seq {
						node := entry.(map[string]any)
						if node["id"] == "expand" {
							payloadRaw, err := base64.StdEncoding.DecodeString(node["inputs"].(map[string]any)["payload"].(string))
							require.NoError(t, err)
							var p struct {
								Input struct {
									Helper   string `json:"helper_op"`
									Executor string `json:"executor_op"`
									Revision string `json:"revision"`
								}
							}
							require.NoError(t, json.Unmarshal(payloadRaw, &p))
							require.Equal(t, helper, p.Input.Helper)
							require.Equal(t, executor, p.Input.Executor)
							require.NotEqual(t, matchingRevision, p.Input.Revision)
						}
					}
				}
				// Move mutable package aliases to unavailable fixture releases. Pending
				// automatic work must continue through the recorded immutable pair.
				// Explicit mutable overrides keep their ordinary resolution semantics.
				if overrides.helper == "" && overrides.executor == "" {
					aliases["nix:github:colony-2/c2ops/published#gha-workflow"] = "./.c2/test-ops/unavailable-new-helper"
					aliases[publicJobOp] = "./.c2/test-ops/unavailable-new-executor"
					moved, err := json.Marshal(aliases)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(aliasPath, moved, 0600))
				}
				require.NoError(t, os.RemoveAll(filepath.Join(h.root, "workers")))
				require.NoError(t, os.Mkdir(filepath.Join(h.root, "workers"), 0700))
				h.reopen()
				result := h.drain(root)["run"].(map[string]any)
				require.Equal(t, "success", result["status"])
				records := selectionRecords(t, logPath)
				executions, collectors := 0, 0
				for _, record := range records {
					for _, entry := range record["sequence"].([]any) {
						node := entry.(map[string]any)
						switch node["id"] {
						case "source", "expand":
							require.Equal(t, helper, node["op"])
						case "execute":
							executions++
							require.Equal(t, executor, node["op"])
						case "collect":
							collectors++
							require.Equal(t, helper, node["op"])
						}
					}
				}
				require.Len(t, h.jobs(), 9, "caller, logical jobs, workflow collector, three shards and two collectors; repeated immutable submission reuses these jobs")
				require.Equal(t, 3, executions)
				require.Equal(t, 2, collectors)
				calls, reads, jobs := len(h.calls), h.secretReads, len(h.jobs())
				require.Positive(t, reads)
				h.reopen()
				require.Equal(t, workflow.JobRunCompleted, h.step(root).Status)
				require.Equal(t, result, h.result(root)["run"])
				require.Len(t, h.calls, calls)
				require.Equal(t, reads, h.secretReads)
				require.Len(t, h.jobs(), jobs)
				require.Equal(t, records, selectionRecords(t, logPath))
				saveEvidence(t, "matching-workers.json", map[string]any{"result": result, "recipes": records, "fixture": "original recipes recorded before op-only local transport; no package availability claim"})
			})
		}
	}
}
