package main

import (
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2ops/pkg/llm"
)

func main() {
	ops.CommandMain(llm.GetEnhancedOp())
}
