# Codex CLI compatibility experiment — 2026-10-09

**The basic CLI/tool flow worked on all 20 tested releases. The complete current
object-session flow worked on 15, from 0.148.0 through 0.162.1.** The five earlier
releases failed during checkpoint export because our exporter requires a
database they did not create. The command-line interface was compatible.

**The production op still accepts only 0.157.1.** For this experiment only, a
launcher returned `codex-cli 0.157.1` for `--version` and delegated every other
invocation to the actual verified CLI version. No production commands, parsing,
export/restore logic, or metadata checks were changed. Thus “works” below means
the tested workflow works after bypassing the version guard, not that the
unmodified op currently accepts that release.

## Version grid

Selected the latest stable patch in each of the 20 minor series 0.143–0.162.
The npm `latest` tag was **0.162.1** when queried; prereleases were excluded.
[Official package version history](https://www.npmjs.com/package/@openai/codex?activeTab=versions).

| CLI version | Basic execution + tool + skill | Checkpoint export / relocated resume | `codex` → `run_skill` + successor checkpoint | Overall / incompatibility |
| --- | --- | --- | --- | --- |
| 0.143.0 | Works | Incompat | Incompat | A: missing required DB |
| 0.144.6 | Works | Incompat | Incompat | A: missing required DB |
| 0.145.0 | Works | Incompat | Incompat | A: missing required DB |
| 0.146.1 | Works | Incompat | Incompat | A: missing required DB |
| 0.147.0 | Works | Incompat | Incompat | A: missing required DB |
| 0.148.0 | Works | Works | Works | Works |
| 0.149.1 | Works | Works | Works | Works |
| 0.150.1 | Works | Works | Works | Works |
| 0.151.0 | Works | Works | Works | Works |
| 0.152.1 | Works | Works | Works | Works |
| 0.153.4 | Works | Works | Works | Works |
| 0.154.0 | Works | Works | Works | Works |
| 0.155.1 | Works | Works | Works | Works |
| 0.156.1 | Works | Works | Works | Works |
| 0.157.1 | Works | Works | Works | Works; current production pin |
| 0.158.0 | Works | Works | Works | Works |
| 0.159.3 | Works | Works | Works | Works |
| 0.160.1 | Works | Works | Works | Works |
| 0.161.0 | Works | Works | Works | Works |
| 0.162.1 | Works | Works | Works | Works; latest stable tested |

**A — checkpoint export fails after successful CLI execution:**

```text
export session: lstat .../thread_history_1.sqlite: no such file or directory
```

`session.go` requires all five entries in `sessionDatabases` to exist and be
regular files. The first missing file was `thread_history_1.sqlite` on each
affected version. This stops even a fresh `Run`, because the op always publishes
a checkpoint before returning success. Resume and `run_skill` were consequently
not reached in those full-op tests. This is a mismatch between our exporter and
the CLI's state layout, not a rejected CLI flag or broken tool execution. These
tests do not establish that making this one file optional is sufficient: later
checks could reveal other differences.

## Cross-version checkpoint probes

Used the actual object store to freeze/hydrate a checkpoint from `Run`, deleted
the originating invocation directory, then resumed with another CLI through
`RunSkill`. All four probes passed, including conversation history, tool output,
relocated working directory, output-schema validation, and successor export:

| Producer CLI | Consumer CLI | Result |
| --- | --- | --- |
| 0.148.0 | 0.162.1 | Works |
| 0.157.1 | 0.162.1 | Works |
| 0.162.1 | 0.148.0 | Works |
| 0.162.1 | 0.157.1 | Works |

The experiment retains the op's hardcoded checkpoint metadata label
`runtime_version: 0.157.1` and `state_format: codex-0.157.1/v1`. These probes
exercise real file/database compatibility underneath that policy; they do not
test a redesigned metadata compatibility policy or every version pair. The
downgrade results apply to these small sessions, not all state a newer CLI could
produce.

## What was actually exercised

- Linux x86_64; verified each downloaded executable's real `--version` before
  introducing the version-response shim. Production source baseline:
  `36a5e2b`.
- Real npm CLI packages, subprocesses, JSONL parsing, structured final output,
  an `exec_command` shell call, and configured skill-file availability.
- Same-version checkpoint export including SQLite, removal of the old home,
  relocation into a new worktree, and resume with the same session ID.
- Full `Run` publication, c2j object-store freeze/hydration, then `RunSkill`
  with a local skill, generated contract, JSON artifact/schema validation, and
  successor publication. The entire originating invocation is removed first.
- Verified that the resumed provider request includes the original conversation;
  the mock response alone is not treated as proof that memory survived.

The provider is a deterministic local Responses/SSE mock, with dummy credentials
and the same mock model configuration for all releases. This exercises the CLI
and op integration without paid API calls. It does not measure real-model skill
selection, hosted authentication, provider/model compatibility, network retries,
Shai execution, large/long-lived sessions, or every optional Codex feature.
Ordinary op tests and recipe fixtures also pass at the production pin.

## Suggested change based on the results

The exact 0.157.1 runtime restriction is narrower than these results justify.
A follow-up can accept the tested compatible releases, record the actual producer
CLI version separately, and base restore checks on the supported checkpoint
format/capabilities rather than requiring the producer version to equal one
constant. Keep this matrix as a release check before broadening support; this
sample does not prove compatibility with every intervening patch or future
release.

For pre-0.148 support, first identify which databases are required for that CLI
and which are optional state parts, then test export/resume with that contract.
Do not silently create empty replacement databases or skip all state validation.
The CLI invocation itself appears basic enough across this entire sample; the
restriction comes from checkpoint handling.

No production version policy or exporter behavior was changed by this experiment.

## Reproduce and inspect evidence

From `codex/` (Go, Node/npm, Python 3 and package-download access required):

```sh
python3 scripts/compatibility_matrix.py --output /tmp/codex-matrix

# Explicit sample; default selection follows the registry's newest stable minors.
python3 scripts/compatibility_matrix.py \
  --versions 0.143.0 0.147.0 0.148.0 0.157.1 0.162.1 \
  --output /tmp/codex-sample

python3 scripts/compatibility_matrix.py \
  --versions 0.148.0 0.157.1 --resume-version 0.162.1 \
  --output /tmp/codex-upgrade
```

The runner compiles the current tests once, uses isolated npm package resolution
without a global installation, and records actual versions and per-test outcomes.
It bounds package installation and each test process. Provisioning failures are
reported as errors, not claimed as CLI incompatibilities. Individual `.log`
files remain local; committed JSON includes full failed-test output.

- [20-version results](./compatibility-results/2026-10-09/results.json)
- [Upgrade to 0.162.1](./compatibility-results/2026-10-09/to-0.162.1/results.json)
- [Downgrade to 0.148.0](./compatibility-results/2026-10-09/to-0.148.0/results.json)
- [Downgrade to 0.157.1](./compatibility-results/2026-10-09/to-0.157.1/results.json)
