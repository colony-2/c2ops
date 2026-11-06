package jobexec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareKeepsMatrixShardIdentityAcrossRetries(t *testing.T) {
	request := Request{JobID: "build", EventName: "workflow_dispatch", Event: json.RawMessage(`{}`), Workflow: `on: workflow_dispatch
jobs:
  build:
    runs-on: [local, '${{ matrix.arch }}']
    strategy:
      fail-fast: false
      matrix:
        arch: [amd64, arm64]
        version: [20, 24]
        include:
          - arch: arm64
            variant: native
        exclude:
          - arch: amd64
            version: 20
    steps: [{run: 'echo done'}]
`}
	root := t.TempDir()
	initial, err := Prepare(t.Context(), request, root)
	require.NoError(t, err)
	require.Len(t, initial, 3)
	for retry := 0; retry < 64; retry++ {
		repeated, err := Prepare(t.Context(), request, root)
		require.NoError(t, err)
		require.Equal(t, initial, repeated, "the same shard index must refer to the same matrix assignment on every retry")
	}
}

func TestPreflightPublicPlacementAndLabelsObjects(t *testing.T) {
	req := Request{JobID: "build", Vars: map[string]string{"os": "ubuntu-24.04"}, Secrets: map[string]string{"OS": "windows-latest"}, Workflow: `on: workflow_dispatch
jobs:
  build:
    runs-on: {labels: '${{ vars.OS }}'}
    steps: [{run: 'echo done'}]
`}
	p, err := Preflight(t.Context(), req, t.TempDir())
	require.NoError(t, err)
	require.True(t, p.Enabled)
	require.Equal(t, []string{"ubuntu-24.04"}, p.Labels)
	req.Workflow = strings.Replace(req.Workflow, "vars.OS", "secrets.OS || vars.OS", 1)
	_, err = Preflight(t.Context(), req, t.TempDir())
	require.ErrorContains(t, err, "secrets", "secret-dependent placement must be rejected before negotiation")
	req.Workflow = `on: workflow_dispatch
jobs:
  build:
    runs-on: {group: privileged, labels: ubuntu-24.04}
    steps: [{run: 'echo done'}]
`
	_, err = Preflight(t.Context(), req, t.TempDir())
	require.ErrorContains(t, err, "groups")
}
