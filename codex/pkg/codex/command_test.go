package codex

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCodexPackageMatchesManifests(t *testing.T) {
	for _, name := range []string{"../../op.yaml", "../../run_skill/op.yaml"} {
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		var manifest struct {
			Dependencies []string `yaml:"dependencies"`
		}
		require.NoError(t, yaml.Unmarshal(data, &manifest))
		require.Contains(t, manifest.Dependencies, "nix:"+codexPackage,
			"qualified CLI calls must match the declared package")
	}
	for _, name := range []string{"../../op.json", "../../run_skill/op.json"} {
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		var manifest struct {
			Dependencies []string `json:"dependencies"`
		}
		require.NoError(t, json.Unmarshal(data, &manifest))
		require.Empty(t, manifest.Dependencies, "packaged ops include the CLI in their Nix closure")
	}
}

func TestCodexCommandUsesExplicitBinaryOrDeclaredNixPackage(t *testing.T) {
	t.Setenv("C2OPS_CODEX_BIN", "")
	require.Equal(t, []string{"nix", "run", codexPackage, "--", "--version"}, codexCommand("--version"))
	t.Setenv("C2OPS_CODEX_BIN", "/some directory/bin/codex")
	require.Equal(t, []string{"/some directory/bin/codex", "--version"}, codexCommand("--version"))
}
