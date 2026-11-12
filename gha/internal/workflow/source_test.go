package workflow

import (
	"crypto/rand"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func sourceGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}
func sourceCommit(t *testing.T, root string) string {
	sourceGit(t, root, "add", ".")
	sourceGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-m", "source")
	return sourceGit(t, root, "rev-parse", "HEAD")
}
func TestSourceSnapshotBoundAndDrift(t *testing.T) {
	root := t.TempDir()
	sourceGit(t, root, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(root, "base"), []byte("base"), 0600))
	base := sourceCommit(t, root)
	// Incompressible published history must never enter the bounded delta.
	f, err := os.Create(filepath.Join(root, "published-history"))
	require.NoError(t, err)
	_, err = io.CopyN(f, rand.Reader, 65<<20)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	published := sourceCommit(t, root)
	pack, err := sourceBundle(t.Context(), root, base, published)
	require.ErrorContains(t, err, "64 MiB")
	require.Nil(t, pack)
	pack, err = sourceBundle(t.Context(), root, published, published)
	require.NoError(t, err)
	require.Empty(t, pack)
	require.NoError(t, os.WriteFile(filepath.Join(root, "candidate"), []byte("unpublished"), 0600))
	candidate := sourceCommit(t, root)
	pack, err = sourceBundle(t.Context(), root, published, candidate)
	require.NoError(t, err)
	require.Less(t, len(pack), 4096)
	_, err = sourceBundle(t.Context(), root, published, base)
	require.ErrorContains(t, err, "revision changed")
	require.NoError(t, os.WriteFile(filepath.Join(root, "dirty"), []byte("dirty"), 0600))
	_, err = sourceBundle(t.Context(), root, published, candidate)
	require.ErrorContains(t, err, "committed")
}
