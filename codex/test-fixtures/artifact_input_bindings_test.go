package testfixtures_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	coreops "github.com/colony-2/c2j/pkg/ops"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const artifactInstructions = "\nDéveloper\t\"quoted\"\r\n${{ literal.instructions }}\n"
const artifactPrefix = "Shared context 🐈\t\r\n${{ literal.prefix }}\n"
const artifactCurrentPrefix = "Current shared context\n"

type capturedCodexArgs struct {
	Argv  []string `json:"argv"`
	Label string   `json:"label"`
}

func TestArtifactInputRecipeReferences(t *testing.T) {
	ensureFixtureOps()
	t.Setenv("C2OPS_CODEX_BIN", installStubCodex(t))
	for _, selector := range []string{"nix", "source"} {
		t.Run(selector, func(t *testing.T) {
			if selector == "source" {
				// Keep the authored source manifest route, selecting this candidate's
				// prebuilt executable rather than the default published package.
				data, err := os.ReadFile(filepath.Join(opRootDir(t), "op.yaml"))
				require.NoError(t, err)
				var manifest map[string]any
				require.NoError(t, yaml.Unmarshal(data, &manifest))
				flake := os.Getenv("C2OPS_TEST_FLAKE")
				if flake == "" {
					flake = "path:" + filepath.Dir(opRootDir(t))
				}
				manifest["command"] = []string{"nix", "run", flake + "#codex", "--"}
				manifest["dependencies"] = []string{"nix:" + flake + "#codex"}
				data, err = yaml.Marshal(manifest)
				require.NoError(t, err)
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "op.yaml"), data, 0600))
				t.Setenv("C2OPS_TEST_CODEX_SOURCE_MANIFEST", dir)
			}
			for _, testCase := range []string{"both stored", "instructions only", "prefix only", "neither", "file", "http", "https", "invalid UTF-8", "NUL", "conflict", "blank task", "raw path rejected", "directory", "descriptor rejected", "null rejected", "malformed reference", "missing stored artifact", "missing resume artifact"} {
				t.Run(testCase, func(t *testing.T) {
					captureDir := t.TempDir()
					t.Setenv("C2OPS_CODEX_CAPTURE_DIR", captureDir)
					data, err := os.ReadFile(filepath.Join(fixturesRootDir(t), "recipes", "codex-artifact-inputs.yaml"))
					require.NoError(t, err)
					var document map[string]any
					require.NoError(t, yaml.Unmarshal(data, &document))
					nodes := document["sequence"].([]any)
					initial := nodes[3].(map[string]any)
					resumed := nodes[4].(map[string]any)
					inputs := initial["inputs"].(map[string]any)
					require.NotContains(t, initial, "artifacts", "direct reference inputs must need no explicit bindings")
					require.NotContains(t, resumed, "artifacts")
					withInstructions := testCase != "prefix only" && testCase != "neither"
					withPrefix := testCase != "instructions only" && testCase != "neither"
					for _, node := range []map[string]any{initial, resumed} {
						if !withInstructions {
							delete(node["inputs"].(map[string]any), "instructions_file")
						}
						if !withPrefix {
							delete(node["inputs"].(map[string]any), "prompt_prefix_file")
						}
					}
					externalRef := func(source string) any {
						return map[string]any{"kind": "external", "name": "shared.txt", "external": map[string]any{"url": source}}
					}
					wantErr, wantCalls := "", 2
					switch testCase {
					case "file", "invalid UTF-8", "NUL", "directory":
						text := artifactInstructions
						if testCase == "invalid UTF-8" {
							text = "\xff"
							wantErr = "instructions_file"
						}
						if testCase == "NUL" {
							text += "\x00"
							wantErr = "instructions_file"
						}
						path := filepath.Join(t.TempDir(), "external.txt")
						if testCase == "directory" {
							require.NoError(t, os.Mkdir(path, 0700))
							wantErr = "instructions_file"
						} else {
							require.NoError(t, os.WriteFile(path, []byte(text), 0600))
						}
						inputs["instructions_file"] = externalRef((&url.URL{Scheme: "file", Path: path}).String())
						if testCase == "file" {
							prefixPath := filepath.Join(t.TempDir(), "prefix.txt")
							require.NoError(t, os.WriteFile(prefixPath, []byte(artifactPrefix), 0600))
							inputs["prompt_prefix_file"] = externalRef((&url.URL{Scheme: "file", Path: prefixPath}).String())
						}
					case "http", "https":
						handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							text := artifactInstructions
							if r.URL.Path == "/prefix" {
								text = artifactPrefix
							}
							_, _ = w.Write([]byte(text))
						})
						var server *httptest.Server
						if testCase == "https" {
							server = httptest.NewTLSServer(handler)
							previous := http.DefaultTransport
							http.DefaultTransport = server.Client().Transport // trust only the local test CA
							defer func() { http.DefaultTransport = previous }()
						} else {
							server = httptest.NewServer(handler)
						}
						defer server.Close()
						inputs["instructions_file"] = externalRef(server.URL + "/instructions")
						inputs["prompt_prefix_file"] = externalRef(server.URL + "/prefix")
					case "conflict":
						inputs["instructions"] = "inline"
						wantErr = "instructions_file"
					case "blank task":
						inputs["prompt"] = " \t\n"
						wantErr = "prompt is required"
					case "raw path rejected":
						inputs["instructions_file"] = "{{ context.environment.op.inbox }}/absent"
						wantErr = "instructions_file"
					case "descriptor rejected":
						inputs["instructions_file"] = map[string]any{"ref": map[string]any{"kind": "stored"}, "path": "/tmp/forged"}
						wantErr = "instructions_file"
					case "null rejected":
						inputs["instructions_file"] = nil
						wantErr = "instructions_file"
					case "malformed reference":
						inputs["instructions_file"] = map[string]any{"kind": "stored", "stored": map[string]any{}}
						wantErr = "instructions_file"
					case "missing stored artifact":
						inputs["instructions_file"] = map[string]any{"kind": "stored", "stored": map[string]any{"key": map[string]any{"jobId": "absent", "taskOrdinal": 1, "name": "absent.txt", "sizeBytes": 1}}}
						wantErr = "artifact"
					case "missing resume artifact":
						resumed["inputs"].(map[string]any)["instructions_file"] = externalRef("file:///missing-codex-artifact-instructions")
						wantErr = "instructions_file"
						wantCalls = 1 // initial session succeeds; unavailable resume input never reaches Codex
					}
					if wantErr != "" && testCase != "missing resume artifact" {
						wantCalls = 0
					}
					data, err = yaml.Marshal(document)
					require.NoError(t, err)
					path := filepath.Join(t.TempDir(), "artifact-case.yaml")
					require.NoError(t, os.WriteFile(path, data, 0600))
					definition := loadRecipe(t, path)
					repo, hash, cleanup := createFixtureRepo(t, path, nil)
					defer cleanup()
					registry, err := workerops.NewActivityRegistry()
					require.NoError(t, err)
					job, git := defaultTestContext(repo, hash)
					var observed *artifactCapture
					result, _, _, runErr := executeRecipeWithArtifacts(t, context.Background(), registry, definition, nil, job, git.ParentRef,
						buildRecipeRegistry(t, path, definition, nil, repo, hash), coreops.NewServiceDepsBuilder().Build(),
						func(capture *artifactCapture) { observed = capture })
					if wantErr == "" {
						require.NoError(t, runErr)
						require.Equal(t, "completed", result["status"])
					} else {
						require.ErrorContains(t, runErr, wantErr)
					}
					files, err := filepath.Glob(filepath.Join(captureDir, "*.json"))
					require.NoError(t, err)
					require.Len(t, files, wantCalls, "rejected text must not reach Codex")
					for _, path := range files {
						data, err := os.ReadFile(path)
						require.NoError(t, err)
						var captured capturedCodexArgs
						require.NoError(t, json.Unmarshal(data, &captured))
						prefix := ""
						if withPrefix {
							prefix = artifactPrefix
							if captured.Label == "resumed" {
								prefix = artifactCurrentPrefix
							}
						}
						wantPrompt := "bash -lc 'true'"
						if prefix != "" {
							wantPrompt = prefix + "\n\n" + wantPrompt
						}
						require.Equal(t, wantPrompt, captured.Argv[len(captured.Argv)-1])
						var developer []string
						for _, arg := range captured.Argv {
							if strings.HasPrefix(arg, "developer_instructions=") {
								developer = append(developer, arg)
							}
						}
						if withInstructions && captured.Label == "initial" {
							encoded, err := json.Marshal(artifactInstructions)
							require.NoError(t, err)
							require.Equal(t, []string{"developer_instructions=" + string(encoded)}, developer)
						} else {
							require.Empty(t, developer)
						}
					}
					if wantErr != "" {
						return
					}
					require.NotNil(t, observed)
					observed.mu.Lock()
					defer observed.mu.Unlock()
					if selector == "nix" {
						require.NotEmpty(t, observed.nixPackages)
					} else {
						require.True(t, observed.sourcePrepared)
					}
					require.Len(t, observed.invocations, 2)
					for _, invocation := range observed.invocations {
						data, err := json.Marshal(invocation)
						require.NoError(t, err)
						require.Empty(t, invocation.Artifacts, "file inputs must not require explicit artifact bindings")
						require.NotContains(t, string(data), "artifact-files-", "staged paths must not enter recorded inputs")
						for _, field := range []string{"instructions_file", "prompt_prefix_file"} {
							if value, exists := invocation.Input["inputs"].(map[string]any)[field]; exists {
								_, err := artifacts.ParseFileRef(value)
								require.NoError(t, err, "recorded file inputs must remain references")
							}
						}
						for _, text := range []string{artifactInstructions, artifactPrefix, artifactCurrentPrefix} {
							encoded, _ := json.Marshal(text)
							require.NotContains(t, string(data), string(encoded[1:len(encoded)-1]), "recorded extension inputs must not embed artifact text")
						}
					}
					if testCase == "both stored" {
						refs := observed.invocations[0].Input["inputs"].(map[string]any)
						aRef, err := artifacts.ParseFileRef(refs["instructions_file"])
						require.NoError(t, err)
						bRef, err := artifacts.ParseFileRef(refs["prompt_prefix_file"])
						require.NoError(t, err)
						a, b := aRef.Stored, bRef.Stored
						require.NotNil(t, a)
						require.NotNil(t, b)
						require.Equal(t, "shared.txt", a.Key.Name)
						require.Equal(t, a.Key.Name, b.Key.Name)
						require.NotEqual(t, a.Key.TaskOrdinal, b.Key.TaskOrdinal)
					}
				})
			}
		})
	}
}
