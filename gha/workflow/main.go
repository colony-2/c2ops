package main

import (
	"context"
	"github.com/colony-2/c2ops/gha/internal/extensioncmd"
	"github.com/colony-2/c2ops/gha/internal/workflow"
)

func main() {
	extensioncmd.Main(func(ctx context.Context, in workflow.Input) (extensioncmd.Result[any], error) {
		out, err := workflow.Run(ctx, in)
		return extensioncmd.Result[any]{Output: out}, err
	})
}
