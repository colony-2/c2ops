package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func artifactTextFixture(t *testing.T, dir, name, text string) *ArtifactFileInput {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(text), 0600))
	return &ArtifactFileInput{Ref: json.RawMessage(`{"kind":"external","external":{"url":"file://` + path + `"}}`), Path: path}
}

func TestArtifactTextExactComposition(t *testing.T) {
	for _, prefix := range []string{"", " \t\r\n", "é 🐈\t\r\n${{ literal.expression }}\n"} {
		for _, withContract := range []bool{false, true} {
			t.Run(fmt.Sprintf("prefix=%q/contract=%t", prefix, withContract), func(t *testing.T) {
				dir := t.TempDir()
				instructions := "\n  Déveloper\t\"quoted\"\r\n${{ literal }}\n"
				input := ExecOpInput{
					Prompt: " \n task é \t", Instructions: " \t\n",
					InstructionsFile: artifactTextFixture(t, dir, "instructions", instructions),
					PromptPrefixFile: artifactTextFixture(t, dir, "prefix", prefix),
				}
				require.NoError(t, resolveArtifactTextInputs(&input, dir))
				require.Equal(t, instructions, input.Instructions)
				want := "task é"
				if prefix != "" {
					want = prefix + "\n\n" + want
				}
				cfg := skillExecutionConfig{}
				if withContract {
					cfg.StatusContractPath = "status.json"
				}
				got := renderSkillPrompt(composePrompt(input.promptPrefix, strings.TrimSpace(input.Prompt)), cfg)
				if withContract {
					require.True(t, strings.HasPrefix(got, "Execution contract:\n"))
					require.True(t, strings.HasSuffix(got, "\nUser request:\n"+want))
				} else {
					require.Equal(t, want, got)
				}
				assertFileContent(t, input.PromptPrefixFile.Path, prefix)
			})
		}
	}
	input := ExecOpInput{Prompt: " task ", Instructions: "inline"}
	require.NoError(t, resolveArtifactTextInputs(&input, t.TempDir()))
	require.Equal(t, "task", composePrompt(input.promptPrefix, strings.TrimSpace(input.Prompt)))
	require.Equal(t, "inline", input.Instructions)
}

func TestArtifactTextRejectsInvalidFilesBeforeSessionPreparation(t *testing.T) {
	for _, field := range []string{"instructions_file", "prompt_prefix_file"} {
		for _, problem := range []string{"invalid UTF-8", "NUL", "missing path", "missing file", "directory", "fifo", "relative", "outside", "traversal", "symlink escape", "unreadable"} {
			t.Run(field+"/"+problem, func(t *testing.T) {
				dir := t.TempDir()
				descriptor := artifactTextFixture(t, dir, "text", "private artifact body")
				switch problem {
				case "invalid UTF-8":
					require.NoError(t, os.WriteFile(descriptor.Path, []byte{0xff}, 0600))
				case "NUL":
					require.NoError(t, os.WriteFile(descriptor.Path, []byte("private artifact body\x00"), 0600))
				case "missing path":
					descriptor.Path = ""
				case "missing file":
					descriptor.Path += "-absent"
				case "directory":
					descriptor.Path = dir
				case "fifo":
					descriptor.Path = filepath.Join(dir, "pipe")
					require.NoError(t, syscall.Mkfifo(descriptor.Path, 0600))
				case "relative":
					descriptor.Path = "text"
				case "outside":
					descriptor.Path = artifactTextFixture(t, t.TempDir(), "text", "outside").Path
				case "traversal":
					descriptor.Path = dir + "/sub/../text"
				case "symlink escape":
					outside := artifactTextFixture(t, t.TempDir(), "outside", "outside")
					descriptor.Path = filepath.Join(dir, "link")
					require.NoError(t, os.Symlink(outside.Path, descriptor.Path))
				case "unreadable":
					if os.Geteuid() == 0 {
						t.Skip("root can read mode-000 files")
					}
					require.NoError(t, os.Chmod(descriptor.Path, 0))
				}
				input := ExecOpInput{Prompt: "task", WorkdirPath: dir, WorktreePath: dir, Session: &SessionInput{}}
				if field == "instructions_file" {
					input.InstructionsFile = descriptor
				} else {
					input.PromptPrefixFile = descriptor
				}
				output, err := Run(context.Background(), input)
				require.ErrorContains(t, err, field)
				require.NotContains(t, err.Error(), "private artifact body")
				require.Nil(t, output.Session)
				require.Empty(t, output.Objects)
			})
		}
	}
}

func TestArtifactTextContainedSymlink(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		dir := t.TempDir()
		descriptor := artifactTextFixture(t, dir, "text", "exact\r\n")
		target := "text"
		if absolute {
			target = descriptor.Path
		}
		descriptor.Path = filepath.Join(dir, "link")
		require.NoError(t, os.Symlink(target, descriptor.Path))
		text, err := readArtifactText("instructions_file", descriptor.Path, dir)
		require.NoError(t, err)
		require.Equal(t, "exact\r\n", text)
	}
}

func TestArtifactTextRejectsConflictsAndBlankTask(t *testing.T) {
	dir := t.TempDir()
	descriptor := artifactTextFixture(t, dir, "text", "nonempty")
	for _, resumed := range []bool{false, true} {
		for _, prompt := range []string{"", " \t\r\n", "task"} {
			input := ExecOpInput{Prompt: prompt, Instructions: "inline", InstructionsFile: descriptor, PromptPrefixFile: descriptor, WorkdirPath: dir, WorktreePath: dir}
			if resumed {
				input.Session = &SessionInput{}
			}
			_, err := Run(context.Background(), input)
			if prompt == "task" {
				require.ErrorContains(t, err, "instructions_file cannot be combined")
			} else {
				require.ErrorContains(t, err, "prompt is required")
			}
		}
	}
}

func TestArtifactTextRunInitialAndResumedCLI(t *testing.T) {
	for _, instructions := range []string{"\nDéveloper\t\"quoted\"\r\n${{ literal }}\n", " \r\n\t"} {
		t.Run(fmt.Sprintf("instructions=%q", instructions), func(t *testing.T) {
			dir := installNativeSequence(t, []string{nativeTurn("first"), nativeTurn("second")}, 0)
			t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
			original := executeLibrary
			t.Cleanup(func() { executeLibrary = original })
			executeLibrary = func(ctx context.Context, opts Options) (Result, string, string, string, error) {
				seedTestSession(t, opts.CodexHome, "test-session")
				return Execute(ctx, opts)
			}
			workdir := t.TempDir()
			input := ExecOpInput{Prompt: " task ", WorkdirPath: workdir, WorktreePath: workdir,
				InstructionsFile: artifactTextFixture(t, workdir, "instructions", instructions),
				PromptPrefixFile: artifactTextFixture(t, workdir, "prefix", "prefix\t\r\n"),
			}
			first, err := Run(context.Background(), input)
			require.NoError(t, err)
			args, err := os.ReadFile(filepath.Join(dir, "args-1"))
			require.NoError(t, err)
			require.True(t, strings.HasSuffix(string(args), "prefix\t\r\n\n\ntask\n"))
			if strings.TrimSpace(instructions) == "" {
				require.NotContains(t, string(args), "developer_instructions=")
			} else {
				encoded, err := json.Marshal(instructions)
				require.NoError(t, err)
				require.Contains(t, string(args), "developer_instructions="+string(encoded)+"\n")
			}
			store := checkpointStore(t)
			draft := first.Objects[first.Session.Name]
			ref, err := store.Publish(context.Background(), draft.Type, draft.Metadata, draft.Files)
			require.NoError(t, err)
			metadata, err := json.Marshal(draft.Metadata)
			require.NoError(t, err)
			require.NotContains(t, string(metadata), "prefix")
			require.NotContains(t, string(metadata), workdir)
			input.Session = hydrateSession(t, store, ref)
			input.InstructionsFile = artifactTextFixture(t, workdir, "instructions", "ignored on resume")
			input.PromptPrefixFile = artifactTextFixture(t, workdir, "prefix", "current prefix")
			_, err = Run(context.Background(), input)
			require.NoError(t, err)
			args, err = os.ReadFile(filepath.Join(dir, "args-2"))
			require.NoError(t, err)
			require.Contains(t, string(args), "resume\ntest-session\n")
			require.NotContains(t, string(args), "developer_instructions=")
			require.NotContains(t, string(args), "ignored on resume")
			require.True(t, strings.HasSuffix(string(args), "current prefix\n\ntask\n"))
			require.NoError(t, os.Remove(input.InstructionsFile.Path))
			_, err = Run(context.Background(), input)
			require.ErrorContains(t, err, "instructions_file")
			assertFileContent(t, filepath.Join(dir, "count"), "2\n")
		})
	}
}
