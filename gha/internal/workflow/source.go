package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Use the same Git bundle and artifact name as c2j's workspace persistence.
// This carries locally created commits without publishing them to GitHub.
func sourceBundle(ctx context.Context, root, base, revision string) ([]byte, error) {
	head, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != revision {
		return nil, fmt.Errorf("source revision changed from the planned snapshot")
	}
	dirty, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return nil, err
	}
	if len(dirty) != 0 {
		return nil, fmt.Errorf("workflow source must be committed before submission")
	}
	if base == revision {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "gha-source-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "snapshot.pack")
	cmd := exec.CommandContext(ctx, "git", "-C", root, "bundle", "create", path, "HEAD", "^"+base)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("snapshot source: %w: %s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	// The invocation broker accepts at most 128 MiB including JSON/base64 and
	// recipe metadata. Fail locally before submitting a partially sized request.
	if info.Size() > 64<<20 {
		return nil, fmt.Errorf("source snapshot exceeds 64 MiB; publish the revision before running the workflow")
	}
	return os.ReadFile(path)
}
