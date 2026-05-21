package main

import (
	"context"
	"fmt"

	"github.com/colony-2/c2ops/codex/internal/extensioncmd"
	"github.com/colony-2/c2ops/codex/pkg/codex"
)

func main() {
	extensioncmd.Main(func(ctx context.Context, input codex.SkillRunInput) (extensioncmd.Result[codex.SkillRunOutput], error) {
		output, err := codex.RunSkill(ctx, input)
		if err != nil {
			return extensioncmd.Result[codex.SkillRunOutput]{Output: output}, fmt.Errorf("codex.run_skill: %w", err)
		}
		return extensioncmd.Result[codex.SkillRunOutput]{Output: output}, nil
	})
}
