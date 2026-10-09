# Codex CLI compatibility experiment — 2026-10-09

**The basic CLI/tool flow worked on all 20 tested releases. The complete current
object-session flow worked on 15, from 0.148.0 through 0.162.1.** The five earlier
releases failed during checkpoint export because our exporter requires a
database they did not create. The command-line interface was compatible.

**Policy update:** the production op now requires **0.148.0 or later**, with no
upper bound, and records the actual producer version in checkpoints. Existing
format/file validation remains. The grid below records the original experiment;
the runner now uses real version checks by default.

At the time of this experiment, the production op accepted only 0.157.1. A
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
| 0.157.1 | Works | Works | Works | Works; default test pin |
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

## Change based on the results

The exact 0.157.1 runtime restriction was replaced with a minimum of 0.148.0.
Exports record the actual producer CLI version; restore requires a producer at
or above the minimum and the supported checkpoint layout, rather than requiring
producer and consumer to match. Semantic versions are compared numerically:
0.148.0 prereleases precede the minimum; prereleases of higher versions pass the
minimum check. Build metadata does not affect comparison. This policy does not
establish compatibility with every future release's storage layout; actual
checkpoint validation remains in force.

For pre-0.148 support, first identify which databases are required for that CLI
and which are optional state parts, then test export/resume with that contract.
Do not silently create empty replacement databases or skip all state validation.
The CLI invocation itself appears basic enough across this entire sample; the
restriction comes from checkpoint handling.

The original experiment made no production changes. The subsequent policy update
changes version acceptance and producer metadata; export/restore file handling
is unchanged.

## Reproduce and inspect evidence

After the minimum-version policy change, the three test flows passed on
**0.148.0, 0.157.1, and 0.162.1 without spoofing**. Cross-version continuations
also passed for **0.148.0 → 0.162.1**, **0.157.1 → 0.162.1**, and
**0.162.1 → 0.148.0**, with assertions that each new checkpoint records the actual
producer CLI version. Boundary tests reject 0.147.x, malformed versions and
0.148.0 prereleases, and accept higher numeric versions. The full Codex suite,
build, and vet checks pass.

- [Minimum-version verification](./compatibility-results/2026-10-09/minimum-148/results.json)
- [Upgrade verification](./compatibility-results/2026-10-09/minimum-148/to-0.162.1/results.json)
- [Downgrade verification](./compatibility-results/2026-10-09/minimum-148/to-0.148.0/results.json)

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

Use `--spoof-version 0.157.1` only to reproduce the historical experiment that
bypassed version checks. Default runs exercise the real production version
policy and assert actual producer versions in both checkpoint generations.

The runner compiles the current tests once, uses isolated npm package resolution
without a global installation, and records actual versions and per-test outcomes.
It bounds package installation and each test process. Provisioning failures are
reported as errors, not claimed as CLI incompatibilities. Individual `.log`
files remain local; committed JSON includes full failed-test output.

- [20-version results](./compatibility-results/2026-10-09/results.json)
- [Upgrade to 0.162.1](./compatibility-results/2026-10-09/to-0.162.1/results.json)
- [Downgrade to 0.148.0](./compatibility-results/2026-10-09/to-0.148.0/results.json)
- [Downgrade to 0.157.1](./compatibility-results/2026-10-09/to-0.157.1/results.json)
