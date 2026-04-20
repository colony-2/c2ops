package export

import (
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/colony2/server/gha/pkg/gha"
)

// GetAll returns all GitHub Actions ops exposed by this module.
func GetAll() []ops.RegisterableOp {
	return []ops.RegisterableOp{
		gha.GetOp(),
		gha.GetRunsOp(),
	}
}
