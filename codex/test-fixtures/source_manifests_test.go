package testfixtures_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Exercise the commands in op.yaml through c2j's actual dependency preparer
// and qualified dispatcher. Execution has no Go toolchain or source checkout.
func TestGoManifestsRunPrebuiltOps(t *testing.T) {
	root := filepath.Dir(opRootDir(t))
	data, err := os.ReadFile(filepath.Join(root, "nix", "ops.json"))
	require.NoError(t, err)
	var catalog map[string]struct{ Path, Kind string }
	require.NoError(t, json.Unmarshal(data, &catalog))
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	manager, err := toolenv.Default()
	require.NoError(t, err)
	for name, op := range catalog {
		if op.Kind != "go" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, op.Path, "op.yaml"))
			require.NoError(t, err)
			var manifest struct {
				Command      []string `yaml:"command"`
				Dependencies []string `yaml:"dependencies"`
			}
			require.NoError(t, yaml.Unmarshal(data, &manifest))
			ref := "github:colony-2/c2ops/published#" + name
			require.Equal(t, []string{"nix", "run", ref, "--"}, manifest.Command)
			require.Equal(t, []string{"nix:" + ref}, manifest.Dependencies)
			// Override only in tests, so CI exercises this checkout's outputs.
			if flake := os.Getenv("C2OPS_TEST_FLAKE"); flake != "" {
				require.NotContains(t, flake, "#")
				manifest.Command[2] = flake + "#" + name
				manifest.Dependencies[0] = "nix:" + manifest.Command[2]
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			env, _, err := manager.Prepare(ctx, []toolenv.Scope{{ID: name, Packages: manifest.Dependencies}})
			require.NoError(t, err)
			require.True(t, env.Ready())
			poison := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(poison, "go"), []byte("#!/bin/sh\necho 'unexpected Go invocation' >&2\nexit 127\n"), 0755))
			command := exec.CommandContext(ctx, filepath.Join(env.Path, manifest.Command[0]), manifest.Command[1:]...)
			command.Dir = t.TempDir()
			command.Env = []string{"HOME=" + t.TempDir(), "PATH=" + env.Path + string(os.PathListSeparator) + poison,
				"C2J_OBJECT_OUTBOX=" + t.TempDir()}
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			input := "{}"
			if name == "rule_gate" {
				input = `{"rules":[{"id":"ready","type":"assert","value":true,"message":"ready"}]}`
			} else if name == "codex" || name == "skill-run" {
				// Probe the real packaged CLI, then stop before any provider call.
				request := map[string]any{"prompt": "version probe", "session": map[string]any{},
					"workdir_path": t.TempDir(), "worktree_path": t.TempDir()}
				if name == "skill-run" {
					request["skill"] = "version-probe"
				}
				encoded, err := json.Marshal(request)
				require.NoError(t, err)
				input = string(encoded)
			}
			command.Stdin = strings.NewReader(input)
			err = command.Run()
			if name == "rule_gate" {
				require.NoError(t, err, stderr.String())
				var result struct {
					Output struct {
						OK bool `json:"ok"`
					} `json:"output"`
				}
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
				require.True(t, result.Output.OK)
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, stderr.String())
				require.Equal(t, 1, exit.ExitCode(), stderr.String())
				require.NotEmpty(t, stderr.String())
				if name == "codex" || name == "skill-run" {
					require.Contains(t, stderr.String(), `"phase":"version.ready"`)
					require.Contains(t, stderr.String(), "restore session: invalid or unsupported session metadata")
				}
			}
			for _, unexpected := range []string{"unexpected Go invocation", "command not found", "No such file or directory", "not prepared"} {
				require.NotContains(t, stderr.String(), unexpected)
			}
		})
	}
}
