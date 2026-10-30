package jobexec

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nektos/act/pkg/model"
	"github.com/nektos/act/pkg/runner"
)

var actionSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var remoteActionPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_./-]+)?@[0-9a-fA-F]{40}$`)

func pinnedAction(uses string) bool {
	return remoteActionPattern.MatchString(uses) && !strings.Contains(uses, "..")
}

// Use act's own cache and action loader. Pin validation also applies to remote
// actions invoked by a downloaded composite; mutable nested refs fail closed.
type pinnedActionCache struct{ runner.GoGitActionCache }

func (c pinnedActionCache) Fetch(ctx context.Context, dir, url, ref, token string) (string, error) {
	if !actionSHA.MatchString(ref) {
		return "", fmt.Errorf("remote action %s requires a full commit SHA, got %q", url, ref)
	}
	return c.GoGitActionCache.Fetch(ctx, dir, url, ref, token)
}
func (c pinnedActionCache) GetTarArchive(ctx context.Context, dir, sha, prefix string) (io.ReadCloser, error) {
	stream, err := c.GoGitActionCache.GetTarArchive(ctx, dir, sha, prefix)
	if err != nil {
		return nil, err
	}
	if filepath.Base(prefix) != "action.yml" && filepath.Base(prefix) != "action.yaml" {
		return stream, nil
	}
	defer stream.Close()
	raw, err := io.ReadAll(stream)
	if err != nil {
		return nil, err
	}
	reader := tar.NewReader(bytes.NewReader(raw))
	header, err := reader.Next()
	if err != nil {
		return nil, err
	}
	if header.Typeflag == tar.TypeSymlink {
		return nil, fmt.Errorf("symlinked action manifests are unsupported")
	}
	{
		action, err := model.ReadAction(reader)
		if err != nil {
			return nil, err
		}
		for _, step := range action.Runs.Steps {
			if step.Uses != "" && !strings.HasPrefix(step.Uses, "./") && !pinnedAction(step.Uses) {
				return nil, fmt.Errorf("nested action %q requires a full commit SHA and a JavaScript/composite action", step.Uses)
			}
		}
		if action.Runs.Using.IsDocker() {
			return nil, fmt.Errorf("Docker actions require a container execution bridge")
		}
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}
