package gha

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

func fileURL(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: abs}).String(), nil
}

func sanitizeArtifactName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	return strings.TrimPrefix(trimmed, "./")
}

func mergeArtifactRefs(maps ...map[string]externalFileRef) map[string]externalFileRef {
	total := 0
	for _, refs := range maps {
		total += len(refs)
	}
	if total == 0 {
		return nil
	}
	out := make(map[string]externalFileRef, total)
	for _, refs := range maps {
		for name, ref := range refs {
			out[name] = ref
		}
	}
	return out
}

func externalArtifactURL(ref externalFileRef) (string, error) {
	if raw := strings.TrimSpace(ref.URL); raw != "" {
		return raw, nil
	}
	return fileURL(ref.Path)
}

func buildArtifactRefs(refs map[string]externalFileRef) (map[string]ArtifactRef, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make(map[string]ArtifactRef, len(refs))
	for name, ref := range refs {
		fileRefURL, err := externalArtifactURL(ref)
		if err != nil {
			return nil, fmt.Errorf("build file url for %q: %w", name, err)
		}
		out[name] = ArtifactRef{
			Kind: "external",
			Name: name,
			External: &ExternalArtifact{
				URL:    fileRefURL,
				Expand: ref.Expand,
			},
		}
	}
	return out, nil
}
