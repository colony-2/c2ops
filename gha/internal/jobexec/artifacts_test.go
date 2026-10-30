package jobexec

import (
	"bytes"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactCapabilitiesAndIncompletePublication(t *testing.T) {
	b, err := startArtifacts(t.Context(), t.TempDir(), filepath.Join(t.TempDir(), "published"), nil)
	require.NoError(t, err)
	defer b.server.Close()
	other, err := startArtifacts(t.Context(), t.TempDir(), filepath.Join(t.TempDir(), "published"), nil)
	require.NoError(t, err)
	defer other.server.Close()
	require.NotEqual(t, b.token, other.token)
	rpc := func(method, body, token string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest("POST", b.env()["ACTIONS_RESULTS_URL"]+"twirp/github.actions.results.api.v1.ArtifactService/"+method, bytes.NewBufferString(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, data
	}
	code, _ := rpc("ListArtifacts", `{"workflowRunBackendId":"1","workflowJobRunBackendId":"1"}`, other.token)
	require.Equal(t, 401, code)
	code, _ = rpc("CreateArtifact", `{"name":"../escape"}`, b.token)
	require.Equal(t, 400, code)
	request := `{"name":"incomplete","version":4,"workflowRunBackendId":"1","workflowJobRunBackendId":"1"}`
	code, raw := rpc("CreateArtifact", request, b.token)
	require.Equal(t, 200, code, string(raw))
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	require.NotEmpty(t, out["signedUploadUrl"])
	code, _ = rpc("CreateArtifact", request, b.token)
	require.Equal(t, 409, code)
	code, _ = rpc("FinalizeArtifact", `{"name":"incomplete","size":"0","workflowRunBackendId":"1","workflowJobRunBackendId":"1"}`, b.token)
	require.Equal(t, 400, code)
	require.NoError(t, b.close())
	entries, err := os.ReadDir(b.output)
	require.NoError(t, err)
	require.Empty(t, entries, "unfinalized uploads cannot cross the worker boundary")
}

func TestArtifactDependencyNamesCannotOverwriteOrConflict(t *testing.T) {
	input := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(input, "result"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(input, "result", "result.zip"), []byte("first"), 0600))
	other := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(other, "result"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(other, "result", "result.zip"), []byte("different"), 0600))
	_, err := startArtifacts(t.Context(), t.TempDir(), t.TempDir(), []string{input, other})
	require.ErrorContains(t, err, "conflicting")
	b, err := startArtifacts(t.Context(), t.TempDir(), t.TempDir(), []string{input, input})
	require.NoError(t, err)
	defer b.server.Close()
	req, err := http.NewRequest("PUT", b.env()["ACTIONS_RESULTS_URL"]+"twirp/github.actions.results.api.v1.ArtifactService/UploadArtifact?artifactName=result", bytes.NewBufferString("overwrite"))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 409, resp.StatusCode)
	require.NoError(t, b.close())
	entries, err := os.ReadDir(b.output)
	require.NoError(t, err)
	require.Empty(t, entries, "inherited artifacts are referenced, not republished")
}
