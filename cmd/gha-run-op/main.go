package main

import (
	"context"
	"fmt"

	"github.com/colony-2/c2ops/internal/extensioncmd"
	"github.com/colony-2/c2ops/pkg/gha"
)

func main() {
	extensioncmd.Main(func(ctx context.Context, input gha.RunInput) (extensioncmd.Result[gha.RunOutput], error) {
		result, err := gha.Run(ctx, input)
		if err != nil {
			return extensioncmd.Result[gha.RunOutput]{
				Output:       result.Output,
				ArtifactRefs: convertArtifactRefs(result.ArtifactRefs),
			}, fmt.Errorf("gha.run: %w", err)
		}
		return extensioncmd.Result[gha.RunOutput]{
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
