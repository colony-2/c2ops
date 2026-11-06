package integration

import (
	"encoding/json"
	"testing"

	core "github.com/colony-2/c2j/pkg/execution"
	local "github.com/colony-2/c2ops/gha/internal/execution"
	"github.com/stretchr/testify/require"
)

// Keep the production matcher free of private runtime dependencies while
// checking actual decisions against the runtime version used by the harness.
func TestMatcherConformance(t *testing.T) {
	allocations := []string{
		`{"schema_version":2}`,
		`{"schema_version":2,"base":"ubuntu-26.04-arm64","resources":{"cpu":"4","memory":"16Gi","ephemeral-storage":"20G"},"capabilities":["network"]}`,
		`{"schema_version":2,"base":"macos-sequoia-15.7.7-arm64","resources":{"cpu":"8000m","memory":"32G"}}`,
	}
	requirements := []string{`{"base":"*"}`, `{"base":"ubuntu*"}`, `{"base":"*arm64"}`, `{"base":"Ubuntu*"}`, `{"base":"ubuntu?"}`, `{"base":"[um]*"}`, `{"capabilities":["network","docker","network"]}`}
	for _, field := range []string{"cpu", "memory", "ephemeral-storage"} {
		for _, value := range []string{"0", "-1", "1", "1m", "0.0001", "4000m", "4.001", "16Gi", "16G", "0.001k", "1e3", "9223372036854775808Gi", "0.0001k"} {
			requirements = append(requirements, `{"resources":{"`+field+`":"`+value+`"}}`)
		}
	}
	for _, raw := range requirements {
		for _, actual := range allocations {
			var cr core.Requirements
			require.NoError(t, json.Unmarshal([]byte(raw), &cr))
			var ca core.Environment
			require.NoError(t, json.Unmarshal([]byte(actual), &ca))
			cm, ce := core.Compare(cr, ca.Allocation)
			var lr local.Requirements
			le := json.Unmarshal([]byte(raw), &lr)
			var la local.Environment
			require.NoError(t, json.Unmarshal([]byte(actual), &la))
			var lm []local.Mismatch
			if le == nil {
				lm, le = local.Compare(lr, la)
			}
			require.Equal(t, ce == nil, le == nil, "request=%s allocation=%s core=%v local=%v", raw, actual, ce, le)
			if ce == nil {
				expected := []any{}
				got := []any{}
				for _, m := range cm {
					expected = append(expected, map[string]string{"field": m.Field, "reason": m.Reason})
				}
				for _, m := range lm {
					got = append(got, map[string]string{"field": m.Field, "reason": m.Reason})
				}
				require.ElementsMatch(t, expected, got, "request=%s allocation=%s", raw, actual)
			}
		}
	}
}
