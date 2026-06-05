package main

import (
	"context"
	"fmt"

	"github.com/colony-2/c2ops/rule_gate/internal/extensioncmd"
	"github.com/colony-2/c2ops/rule_gate/pkg/rulegate"
)

func main() {
	extensioncmd.Main(func(ctx context.Context, input rulegate.Input) (extensioncmd.Result[rulegate.Output], error) {
		output, err := rulegate.Evaluate(ctx, input)
		if err != nil {
			return extensioncmd.Result[rulegate.Output]{}, fmt.Errorf("rule_gate: %w", err)
		}
		return extensioncmd.Result[rulegate.Output]{Output: output}, nil
	})
}
