package runners

import (
	"encoding/json"
	"github.com/colony-2/c2ops/gha/internal/execution"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCatalogSizes(t *testing.T) {
	for _, tc := range []struct{ label, base, cpu, memory string }{
		{"ubuntu-24.04", LinuxAMD, "4", "16Gi"}, {"ubuntu-26.04-arm", LinuxARM, "4", "16Gi"},
		{"ubuntu-24.04-16core", LinuxAMD, "16", "64G"}, {"ubuntu-26.04-arm-64core", LinuxARM, "64", "208G"},
		{"ubuntu-latest-64core", LinuxAMD, "64", "256G"}, {"ubuntu-26.04-96core", LinuxAMD, "96", "384G"},
		{"macos-15-xlarge", MacARM, "5", "14G"}, {"macos-latest-xlarge", MacARM, "5", "14G"},
	} {
		r, err := Resolve([]string{tc.label}, nil)
		require.NoError(t, err)
		require.Equal(t, tc.base, *r.Required.Base)
		require.Equal(t, tc.cpu, *r.Required.Resources.CPU)
		require.Equal(t, tc.memory, *r.Required.Resources.Memory)
	}
	// Exercise every emitted alias for validation and wire round-trip.
	for label := range catalog {
		r, err := Resolve([]string{label}, nil)
		require.NoError(t, err)
		raw, err := json.Marshal(r)
		require.NoError(t, err)
		var decoded Resolution
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.Equal(t, r, decoded)
	}
	for _, label := range []string{"windows-2025", "macos-15-large", "macos-26", "macos-latest", "macos-15-intel", "ubuntu-22.04", "ubuntu-26.04-arm-96core", "ubuntu-26.04-128core", "ubuntu-slim", "custom"} {
		_, err := Resolve([]string{label}, nil)
		require.Error(t, err, label)
	}
}
func TestProfilePrecedenceAndConstraints(t *testing.T) {
	p := Profile{Labels: []string{"Ubuntu-24.04-16core"}, Type: &Type{Image: "ubuntu-26.04", Architecture: "amd64", Size: "32core"}}
	r, err := Resolve([]string{"ubuntu-24.04-16core"}, []Profile{p})
	require.NoError(t, err)
	require.Equal(t, "32", *r.Required.Resources.CPU)
	_, err = Resolve([]string{"ubuntu-24.04-16core"}, []Profile{p, p})
	require.ErrorContains(t, err, "ambiguous")
	p.Type.Architecture = "arm64"
	_, err = Resolve([]string{"ubuntu-24.04-16core"}, []Profile{p})
	require.ErrorContains(t, err, "conflicts")
	p.Labels = []string{"custom"}
	r, err = Resolve([]string{"CUSTOM"}, []Profile{p})
	require.NoError(t, err)
	require.Equal(t, LinuxARM, *r.Required.Base)
	p.Type.Size = "96core"
	require.Error(t, ValidateProfiles([]Profile{p}))
	for _, label := range []string{"macos-15-large", "macos-13", "windows-latest"} {
		p = Profile{Labels: []string{label}, Required: &execution.Requirements{Base: execution.String(MacARM)}}
		require.Error(t, ValidateProfiles([]Profile{p}))
	}
	_, err = Resolve([]string{"ubuntu-24.04", "gpu"}, nil)
	require.Error(t, err)
}
