package execution

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRequirementsStrict(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"packages":[]}`, `{"platform":"linux/amd64"}`, `{"base":null}`, `{"base":""}`, `{"base":"a","base":"b"}`, `{"resources":{"cpu":"-1"}}`, `{"resources":{"ram":"1G"}}`, `{"base":"a"} {}`, `{"capabilities":["Docker"]}`} {
		var r Requirements
		require.Error(t, json.Unmarshal([]byte(raw), &r), raw)
	}
	var r Requirements
	require.NoError(t, json.Unmarshal([]byte(`{"base":"ubuntu*","resources":{"cpu":"4000m","memory":"16Gi"},"capabilities":["network"]}`), &r))
	require.NoError(t, r.Validate())
}
func TestComparisonAndInformationalPackages(t *testing.T) {
	t.Setenv("C2J_EXECUTION_ALLOCATION", `{"schema_version":2,"base":"ubuntu-26.04-arm64","resources":{"cpu":"4","memory":"16Gi"},"capabilities":["network"],"packages":["uv:ruff==0.12.0","uv:ruff==0.13.0"]}`)
	env, err := ReadEnvironment()
	require.NoError(t, err)
	require.Len(t, env.Packages, 2)
	for _, tc := range []struct {
		raw        string
		mismatches int
	}{
		{`{"base":"ubuntu*","resources":{"cpu":"4000m","memory":"16Gi"},"capabilities":["network"]}`, 0},
		{`{"base":"Ubuntu*"}`, 1}, {`{"base":"ubuntu?"}`, 1},
		{`{"resources":{"memory":"17Gi","ephemeral-storage":"1G"},"capabilities":["docker"]}`, 3},
	} {
		var r Requirements
		require.NoError(t, json.Unmarshal([]byte(tc.raw), &r))
		m, err := Compare(r, env)
		require.NoError(t, err)
		require.Len(t, m, tc.mismatches)
	}
	env = Environment{SchemaVersion: 2}
	m, err := Compare(Requirements{Base: String("*")}, env)
	require.NoError(t, err)
	require.Empty(t, m)
	m, err = Compare(Requirements{Base: String("ubuntu*")}, env)
	require.NoError(t, err)
	require.Len(t, m, 1)
	for _, raw := range []string{`{}`, `null`, `{"schema_version":1}`, `{"schema_version":2,"resources":{"cpu":"invalid"}}`, `{"schema_version":2,"packages":"git"}`} {
		t.Setenv("C2J_EXECUTION_ALLOCATION", raw)
		_, err := ReadEnvironment()
		require.Error(t, err, raw)
	}
}
