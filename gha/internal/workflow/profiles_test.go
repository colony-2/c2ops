package workflow

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRunnerBaseProfiles(t *testing.T) {
	for label, base := range map[string]string{"ubuntu-latest": "ubuntu-26.04-amd64", "ubuntu-22.04-arm": "ubuntu-26.04-arm64", "macos-latest": "macos-26-arm64"} {
		profile := Profile{Labels: []string{label}, ExecutionNeeds: map[string]any{"base": base}}
		require.NoError(t, validateBindings(Input{Profiles: []Profile{profile}}))
		got, err := matchProfile([]Profile{profile}, []string{label})
		require.NoError(t, err)
		require.Equal(t, base, got["base"])
	}
	for _, bad := range []map[string]any{{"base": ""}, {"base": 1}, {"base": "x", "platform": "linux/arm64"}, {"base": "x", "image": "x"}, {"base": "x", "unknown": true}} {
		require.Error(t, validateBindings(Input{Profiles: []Profile{{Labels: []string{"local"}, ExecutionNeeds: bad}}}))
	}
	profile := Profile{Labels: []string{"local"}, ExecutionNeeds: map[string]any{"platform": "linux/arm64", "image": "legacy"}}
	require.NoError(t, validateBindings(Input{Profiles: []Profile{profile}}))
	require.ErrorContains(t, validateBindings(Input{Profiles: []Profile{profile}, SourceCredential: "MISSING"}), "explicit secret_bindings")
	require.NoError(t, validateBindings(Input{Profiles: []Profile{profile}, SourceCredential: "source", SecretBindings: map[string]string{"SOURCE": "private_source"}}))
}
