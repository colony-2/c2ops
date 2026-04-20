package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSkillRefValid(t *testing.T) {
	parsed, err := parseSkillRef("github.com/acme/codex-platform-skills/.agents/skills@platform-v12")
	require.NoError(t, err)
	require.Equal(t, "github.com/acme/codex-platform-skills", parsed.repoPath)
	require.Equal(t, ".agents/skills", parsed.skillsRootPath)
	require.Equal(t, "platform-v12", parsed.gitRef)
	require.Equal(t, "github.com/acme/codex-platform-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4", parsed.resolvedRef("9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4"))
}

func TestParseSkillRefRejectsInvalidFormat(t *testing.T) {
	_, err := parseSkillRef("github.com/acme/codex-platform-skills@main")
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected <host>/<org>/<repo>/<skills-root-path>@<git-ref>")
}

func TestPrepareConfiguredSkillSourcesMaterializesSkillRefs(t *testing.T) {
	workdir := t.TempDir()
	originalMaterializer := materializeSkillRefsFn
	materializeSkillRefsFn = func(ctx context.Context, skillRefs []string, stageRoot string, skillsRoot string) ([]string, error) {
		require.Equal(t, []string{"github.com/acme/codex-platform-skills/.agents/skills@platform-v12"}, skillRefs)
		require.NoError(t, os.MkdirAll(filepath.Join(skillsRoot, "platform-skill"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillsRoot, "platform-skill", "SKILL.md"), []byte("test"), 0o644))
		return []string{"github.com/acme/codex-platform-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4"}, nil
	}
	defer func() { materializeSkillRefsFn = originalMaterializer }()

	dirs, installed, cleanup, err := prepareConfiguredSkillSources(context.Background(), ExecOpInput{
		Skills: []string{"github.com/acme/codex-platform-skills/.agents/skills@platform-v12"},
	}, workdir)
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.Len(t, dirs, 1)
	require.Equal(t, []string{"github.com/acme/codex-platform-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4"}, installed)

	skillPath := filepath.Join(dirs[0], "platform-skill", "SKILL.md")
	require.FileExists(t, skillPath)

	require.NoError(t, cleanup())
	require.NoDirExists(t, filepath.Dir(dirs[0]))
}
