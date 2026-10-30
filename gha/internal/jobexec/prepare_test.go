package jobexec

import (
	"encoding/json"
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
