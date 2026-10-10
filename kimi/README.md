# Kimi Code op

Runs the maintained [Kimi Code CLI](https://github.com/MoonshotAI/kimi-code), pinned
at `@moonshot-ai/kimi-code@2.1.1`. The manifest explicitly declares
`pnpm:@moonshot-ai/kimi-code@2.1.1`; c2j prepares the CLI before execution.
The Nix package includes Python and Git. The worker base supplies pnpm and
Node.js 24.15+. Configure the [`colony2` cache](../NIX_PACKAGES.md) for the `main` coordinates below.

```yaml
sequence:
  - id: code_task
    op: nix:github:colony-2/c2ops/main#kimi
    inputs:
      prompt: Fix the failing tests in this repository.
      worktree_path: "{{ context.environment.op.worktree_path }}"
      workdir_path: "{{ context.environment.op.workdir }}"
      artifact_outbox_path: "{{ context.environment.op.outbox }}"
      env:
        KIMI_MODEL_NAME: kimi-for-coding
        KIMI_MODEL_API_KEY: "${{ secrets.kimi_api_key }}"
        KIMI_MODEL_BASE_URL: https://api.kimi.com/coding/v1
```

The op reads one JSON object and emits `{"output": {...}}`. `prompt` and
`worktree_path` are required. `model` selects a configured model alias;
`skills_dirs` supplies directories using Kimi's `--skills-dir` option.
Noninteractive mode automatically approves commands and edits. Use the recipe
engine's sandbox when isolation is required.

Kimi state defaults to `<workdir_path>/.kimi-op` (the worktree is the fallback
workdir). Return `sessionId` to the next invocation, using the same worktree and
workdir, to resume. The session ID comes from the CLI, rather than being invented
by the wrapper. To use an existing Kimi login or `config.toml`, explicitly set
`env.KIMI_CODE_HOME` to its directory. Provider keys such as `KIMI_API_KEY` are
not automatically read by this version of Kimi; use `KIMI_MODEL_*` as above or
configure `api_key_env` in Kimi's config. See the upstream
[environment reference](https://moonshotai.github.io/kimi-code/en/configuration/env-vars).

Outputs include `status`, `sessionId`, `assistantSummary`, `exitCode`, and error
information. The outbox contains raw `stdout.jsonl` and `stderr.txt`; it defaults
to `<workdir_path>/outbox`. Invalid or empty successful CLI output is an error.
`timeout_seconds` defaults to 1740 seconds, below the manifest's 30-minute limit.
The op exits nonzero on CLI, timeout, or output-parsing failure.

```sh
make install-test-deps
make test
```

Tests run the actual pinned CLI against a local mock HTTP API, including session
resumption, and check failure handling and artifact preservation. No provider
credentials are required. npm needs network access on first use to fetch the CLI.

CLI execution uses `pnpm --package=@moonshot-ai/kimi-code@2.1.1 dlx kimi <args>`,
matching the manifest dependency. c2j dispatches this qualified form to the
prepared installation; an unrelated `kimi` on PATH cannot select another version.
