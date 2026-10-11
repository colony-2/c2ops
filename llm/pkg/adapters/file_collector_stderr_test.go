package llmadapters

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestGitCommandOutputPreservesFailureDiagnostics(t *testing.T) {
	for _, status := range []string{"0", "7"} {
		output, err := gitCommandOutput(exec.Command("sh", "-c", "printf 'file.txt\\n'; printf 'git diagnostic\\n' >&2; exit "+status))
		if string(output) != "file.txt\n" {
			t.Fatalf("stderr contaminated the filename protocol: %q", output)
		}
		if status == "0" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || !strings.Contains(err.Error(), "git diagnostic") {
			t.Fatalf("lost child diagnostic or exit status: %v", err)
		}
	}
}
