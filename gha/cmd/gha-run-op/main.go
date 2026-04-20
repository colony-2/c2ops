package main

import (
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/colony2/server/gha/pkg/gha"
)

func main() {
	ops.CommandMain(gha.GetOp())
}
