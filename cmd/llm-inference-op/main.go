package main

import (
	"context"
	"fmt"

	"github.com/colony-2/c2ops/internal/extensioncmd"
	"github.com/colony-2/c2ops/pkg/llm"
)

func main() {
	extensioncmd.Main(func(ctx context.Context, input llm.LLMInput) (extensioncmd.Result[llm.LLMOutput], error) {
		output, err := llm.RunInference(ctx, input)
		if err != nil {
			return extensioncmd.Result[llm.LLMOutput]{}, fmt.Errorf("llm inference: %w", err)
		}
		return extensioncmd.Result[llm.LLMOutput]{Output: output}, nil
	})
}
