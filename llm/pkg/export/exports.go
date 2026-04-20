package export

import (
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/colony2/server/llm/pkg/codex"
	llmops "github.com/colony-2/colony2/server/llm/pkg/llm"
)

// GetAll returns all ops owned by the llm module.
func GetAll() []ops.RegisterableOp {
	return []ops.RegisterableOp{
		codex.GetOp(),
		llmops.GetOp(),
		llmops.GetEnhancedOp(),
	}
}
