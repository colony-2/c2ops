package test_fixtures_test

import (
	"os/exec"
	"sync"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	testfixtures "github.com/colony-2/c2j/pkg/worker/test-fixtures"
	"github.com/colony-2/colony2/server/llm/pkg/codex"
)

var registerOpsOnce sync.Once

func ensureCodexAvailable(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping codex fixture tests in short mode")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not available in PATH")
	}
}

func registerFixtureOps() {
	registerOpsOnce.Do(func() {
		coreops.Register(extensions.GetExecutionOp())
		coreops.Register(commandop.GetOp())
		coreops.Register(codex.GetOp())
	})
}

func TestCodexRecipeFixtures(t *testing.T) {
	ensureCodexAvailable(t)
	t.Setenv("VIBETHIS_CODEX_USE_DIRECT", "1")
	registerFixtureOps()
	testfixtures.RunTestOnAllRecipes("recipes/*.test.yaml", t)
}
