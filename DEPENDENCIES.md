# Dependency review — 2026-09-27

Versions were checked against PyPI, npm, the Go module proxy, and upstream
releases. Python and CLI packages are pinned; Go modules retain their normal
`go.mod` / `go.sum` dependency graphs.

| Component | Previous | Updated / tested |
| --- | --- | --- |
| PydanticAI | 1.84.1 | 2.51.0 |
| LiteLLM | 1.83.10 | 1.102.1 |
| Aider | 0.86.2 | 0.86.2 (latest published) |
| OpenAI Go SDK | v1.12.0 | v3.66.0 |
| Anthropic Go SDK | v1.38.0 | v1.75.0 |
| Google GenAI SDK | v1.54.0 | v1.71.0 |
| go-github | v84.0.0 | v92.0.0 |
| act | v0.2.87 | v0.2.89 |
| logrus | v1.9.4 | v1.10.2 |
| jsonschema/v6 | v6.0.2 | v6.0.3 |
| testify | v1.8.4 / v1.11.1 | v1.12.1 |
| x/time | v0.15.0 | v0.16.0 |
| uv test bootstrap | 0.11.7 | 0.12.19 |
| Codex CLI | host-provided | 0.157.1 (pinned test runtime) |
| c2j fixture runtime | v0.0.15 | v0.0.53 (latest release) |
| Fixture execution runtime | swf-go May 15 pseudo-version | jobdb v0.0.19 |
| Kimi Code (new op) | — | @moonshot-ai/kimi-code 2.1.1 |
| TypeSafe SDK (new Jev op) | — | typesafe-sdk 0.7.2 |

The root module's older direct dependency versions were also refreshed, including
`shai` v0.0.13 and `validator/v10` v10.30.5. Ops are separate modules, so their own
`go.mod` files determine the versions used at runtime.

## Compatibility changes

- PydanticAI 2 moved `ToolReturn` to the public package and changed `result.usage`
  from a method to a property. OpenAI requests explicitly use `openai-chat:` to
  retain Chat Completions behavior, including compatible endpoints. Recipes can
  explicitly request `openai-responses:`. The manifest disables the new startup
  banner to keep diagnostics clean.
- OpenAI Go imports now use `/v3`; function tools use the v3 tool union constructor.
- GitHub Actions clients now import go-github `/v92`.
- The c2j fixture harness now uses jobdb's artifact/data types and workflow
  engine. It supplies an explicit worker tenant ID and wraps the engine in
  c2j's schema-registering `jobdbschema.WorkflowEngine`. The legacy swf-go
  dependency is removed.
- Codex tests use `npm exec` with CLI 0.157.1. Production still requires `codex` on
  PATH; install that version in the execution environment when upgrading it.
- Kimi uses the maintained Node CLI. The last `kimi-cli` Python release exits with
  a deprecation notice. Kimi 2 uses `--prompt`, `KIMI_CODE_HOME`, and emitted
  `session.resume_hint` records; the wrapper follows that contract.

## Validation

Run `make build` and `make test` from the repository root. Every top-level
`op.yaml` is discovered automatically, including `kimi` and `jev`; Codex's Go
suite also includes `run_skill` and recipe fixtures.

The suites exercise the real pinned Codex, Kimi, Aider, LiteLLM, PydanticAI, and
TypeSafe clients against local mock APIs. Go adapter tests cover real SDK request
serialization and tool-call response parsing for OpenAI, Anthropic, and Gemini.
GitHub client tests cover workflow dispatch, run metadata, jobs, and artifacts.
Kimi additionally executes a shell tool in a temporary worktree and resumes its
session. Tests do not call paid providers, dispatch real GitHub workflows, or
run Docker-backed `act` workflows.

The development host's default npm cache encountered `ECOMPROMISED` during CLI
installation. A fresh cache works: `npm_config_cache=/tmp/c2ops-npm-cache make test`.
No provider keys are needed. Test prerequisites are Go 1.26+, Python 3.12+, `uv`,
Node.js 24.15+, and npm; package downloads require network access on first use.

## Upstream references

- [PydanticAI releases](https://github.com/pydantic/pydantic-ai/releases)
- [LiteLLM releases](https://github.com/BerriAI/litellm/releases)
- [OpenAI Go releases](https://github.com/openai/openai-go/releases)
- [Anthropic Go releases](https://github.com/anthropics/anthropic-sdk-go/releases)
- [Google GenAI Go releases](https://github.com/googleapis/go-genai/releases)
- [go-github releases](https://github.com/google/go-github/releases)
- [Codex noninteractive execution](https://developers.openai.com/codex/noninteractive/)
- [Kimi Code](https://github.com/MoonshotAI/kimi-code)
- [TypeSafe Python SDK](https://docs.typesafe.ai/sdk/python)

## Codex object-session migration

The initial Codex migration pinned c2j
`v0.0.56-0.20260929214740-e1334817a353` (commit `e1334817a353`), which implements
immutable object checkpoints. Tagged `v0.0.55` lacks that API. Workers executing
the migrated Codex selectors need this object-capable runtime as well.

The initial migration pinned and enforced Codex CLI **0.157.1** (superseded by
the minimum-version policy below). `modernc.org/sqlite` **v1.50.1** supplies pure-Go SQLite
export/restore, including WAL-aware database snapshots and rollout-path relocation.
No external SQLite executable is needed.

See [the consumer migration guide](./codex/MIGRATION_OBJECT_SESSIONS.md). The change
is limited to `codex` and `codex/run_skill`; other op modules retain their pins.

## c2j update — 2026-10-04

The Codex module now pins **c2j v0.0.61**, the latest release returned by
`go list -m github.com/colony-2/c2j@latest` on this date. Its required JobDB version
is **v0.0.25-0.20261004045405-6d7395e73c67**. Other op module pins and Codex CLI
**0.157.1** are unchanged. Updating this module does not update deployed c2j
workers or cached extension selectors; deploy the worker and c2ops revision too.

All repository op tests pass against local mock providers with this update.
This does not exercise Docker/Shai execution. Investigation found that c2j
v0.0.61's Shai path does not forward per-invocation stdin; see
[Codex troubleshooting](./codex/TROUBLESHOOTING.md) for evidence and diagnosis.

## Extension protocol dependency boundary — 2026-10-06

Codex no longer imports c2j in production. The manifests declare the object
contract; the op consumes hydrated JSON and returns JSON drafts/markers using
local types. The incoming reference stays opaque, with reference validation
and hydration owned by the runner. c2j and JobDB remain in `codex/go.mod` for
object-store and recipe integration tests; Go keeps test dependencies in the
same module file. Neither production entrypoint links c2j or JobDB packages.

## Codex minimum version — 2026-10-09

Both Codex ops require CLI **0.148.0 or later**, with no upper bound. The default
test CLI remains pinned to **0.157.1** for repeatability. Runtime and checkpoint
producer versions are checked against the minimum; exports record the actual
CLI version. The existing checkpoint format identifier and file/database
validation remain in place. See [the compatibility report](./codex/CLI_COMPATIBILITY.md).
