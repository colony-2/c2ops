package testfixtures_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestArtifactFileInputManifests(t *testing.T) {
	for _, directory := range []string{opRootDir(t), filepath.Join(opRootDir(t), "run_skill")} {
		yamlBytes, err := os.ReadFile(filepath.Join(directory, "op.yaml"))
		require.NoError(t, err)
		jsonBytes, err := os.ReadFile(filepath.Join(directory, "op.json"))
		require.NoError(t, err)
		var source, generated map[string]any
		require.NoError(t, yaml.Unmarshal(yamlBytes, &source))
		require.NoError(t, json.Unmarshal(jsonBytes, &generated))
		sourceSchema, err := json.Marshal(source["input_schema"])
		require.NoError(t, err)
		generatedSchema, err := json.Marshal(generated["input_schema"])
		require.NoError(t, err)
		require.JSONEq(t, string(sourceSchema), string(generatedSchema))
		props := generated["input_schema"].(map[string]any)["properties"].(map[string]any)
		for _, field := range []string{"instructions_file", "prompt_prefix_file"} {
			if directory != opRootDir(t) {
				require.NotContains(t, props, field, "skill.run's contract must remain unchanged")
				continue
			}
			property := props[field].(map[string]any)
			require.Equal(t, "object", property["type"])
			require.Equal(t, true, property["x-c2j-artifact-file"])
			for _, legacy := range []string{"systeminstructionsfile", "promptprefixfile", "prompt_prefix"} {
				require.NotContains(t, props, legacy)
			}
		}
	}
}
