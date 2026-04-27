package main

import (
	"context"
	"fmt"

	"github.com/colony-2/c2ops/gha-many/internal/extensioncmd"
	"github.com/colony-2/c2ops/gha-many/pkg/gha"
)

func main() {
	extensioncmd.Main(func(ctx context.Context, input gha.RunsInput) (extensioncmd.Result[gha.RunsOutput], error) {
		result, err := gha.RunBatch(ctx, input)
		if err != nil {
			return extensioncmd.Result[gha.RunsOutput]{
				Output:       result.Output,
				ArtifactRefs: convertArtifactRefs(result.ArtifactRefs),
			}, fmt.Errorf("gha.runs: %w", err)
		}
		return extensioncmd.Result[gha.RunsOutput]{
			Output:       result.Output,
			ArtifactRefs: convertArtifactRefs(result.ArtifactRefs),
		}, nil
	})
}

func convertArtifactRefs(refs map[string]gha.ArtifactRef) map[string]extensioncmd.ArtifactRef {
	if len(refs) == 0 {
		return nil
	}
	out := make(map[string]extensioncmd.ArtifactRef, len(refs))
	for name, ref := range refs {
		converted := extensioncmd.ArtifactRef{
			Kind: ref.Kind,
			Name: ref.Name,
		}
		if ref.External != nil {
			converted.External = &extensioncmd.ExternalRef{
				URL:    ref.External.URL,
				Expand: ref.External.Expand,
			}
		}
		out[name] = converted
	}
	return out
}
