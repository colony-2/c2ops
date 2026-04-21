package main

import (
	"context"
	"fmt"

	"github.com/colony-2/c2ops/internal/extensioncmd"
	"github.com/colony-2/c2ops/pkg/llm"
)

func main() {
	extensioncmd.Main(func(ctx context.Context, input llm.LLMInferenceInput) (extensioncmd.Result[llm.LLMInferenceOutput], error) {
		output, err := llm.RunEnhancedInference(ctx, input)
		if err != nil {
			return extensioncmd.Result[llm.LLMInferenceOutput]{}, fmt.Errorf("llm inference2: %w", err)
		}
		return extensioncmd.Result[llm.LLMInferenceOutput]{Output: output}, nil
	})
}
