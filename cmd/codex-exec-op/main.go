package main

import (
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2ops/pkg/codex"
)

func main() {
	ops.CommandMain(codex.GetOp())
}
