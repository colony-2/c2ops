package export

import (
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2ops/pkg/codex"
	"github.com/colony-2/c2ops/pkg/gha"
	llmops "github.com/colony-2/c2ops/pkg/llm"
)

// GetAll returns all ops exposed by the c2ops module.
func GetAll() []ops.RegisterableOp {
	return []ops.RegisterableOp{
		codex.GetOp(),
		gha.GetOp(),
		gha.GetRunsOp(),
		llmops.GetOp(),
		llmops.GetEnhancedOp(),
	}
}
