package main

import (
	"context"
	"fmt"

	"github.com/colony-2/c2ops/codex/internal/extensioncmd"
	"github.com/colony-2/c2ops/codex/pkg/codex"
)

func main() {
	extensioncmd.Main(func(ctx context.Context, input codex.ExecOpInput) (extensioncmd.Result[codex.ExecOpOutput], error) {
		output, err := codex.Run(ctx, input)
		if err != nil {
			return extensioncmd.Result[codex.ExecOpOutput]{}, fmt.Errorf("codex.exec: %w", err)
		}
		return extensioncmd.Result[codex.ExecOpOutput]{Output: output}, nil
	})
}
