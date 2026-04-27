package main

import (
	"context"
	"fmt"

	"github.com/colony-2/c2ops/llm2/internal/extensioncmd"
	"github.com/colony-2/c2ops/llm2/pkg/llm"
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
