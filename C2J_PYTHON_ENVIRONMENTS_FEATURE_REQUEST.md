# Feature request: manifest-declared Python environments prepared with uv

Status: proposal for c2j; the example syntax below is not currently supported.

## Request

Let an extension op declare a locked Python environment in its manifest and
explicitly execute its script using that environment. c2j should prepare the
environment with uv during dependency setup, before the op's execution timeout,
and reuse uv's shared package cache across ops.

This would let us publish small Nix packages containing op code, launchers, and
dependency specifications, while keeping Python libraries out of our Cachix
cache. It should also work for local/Git extensions.

## Current contract gap

[EXECUTION_TOOLS.md](./EXECUTION_TOOLS.md) defines `uv:` dependencies as CLI
applications, not dependencies injected into a Python environment. For example,
`uv:ruff==0.11.2` prepares an executable that an op can explicitly invoke through
`uvx --from ruff==0.11.2 ruff ...`.

Our Pydantic op instead imports `pydantic_ai` from its own Python script. Adding
`uv:pydantic-ai==2.55.0` would not bind that script to an environment containing
the library. An installed CLI and an importable library environment are different
requirements, and the manifest currently expresses only the former.

The current alternatives each have drawbacks:

- Package Python and its libraries through Nix, as c2ops currently does. This
  provides a prebuilt runtime but puts Python dependency closures in our binary
  cache and requires maintaining Python packaging through Nix.
- Run `uv run --script main.py` from the op. Without environment preparation in
  c2j, this can resolve or install dependencies during timed execution, outside
  the recorded dependency setup lifecycle.
- Distribute each op as an installable Python CLI. This may fit the existing
  `uv:` contract, but requires publishing/installing the op as a Python package
  when its source is already distributed in a Nix package or Git extension.

The missing capability is a prepared library environment with an explicit
execution binding, not merely a declaration that the uv executable is available.

## Motivation and sharing expectations

Suppose two ops import the same compatible version of PydanticAI. Their library
downloads should be reusable through the worker's uv cache even when the ops
have different entrypoints or additional dependencies. Compatible complete
environments could also be reused when their identities match.

This is not a claim that Nix always duplicates dependencies: matching Nix store
paths are shared. Nor does uv guarantee one physical copy in every deployment:
separate environments can share package files through linking or copy-on-write,
but filesystem boundaries may require copying. The goal is to reuse Python
artifacts independently of each op's Nix output and avoid publishing those
artifacts to Cachix. See [uv caching](https://docs.astral.sh/uv/concepts/cache/).

## Illustrative manifest and invocation

The exact field names and invocation syntax are open for discussion. A possible
Nix `op.json` fragment is:

```json
{
  "name": "pydantic",
  "command": ["bin/pydantic"],
  "python_environments": {
    "runtime": {
      "manager": "uv",
      "project": "python",
      "lock": "python/uv.lock"
    }
  }
}
```

The package would contain `python/pyproject.toml`, `python/uv.lock`, the op script
at `libexec/main.py`, and a launcher at `bin/pydantic`. The project declares its
Python requirement and pinned library dependencies; the lock fixes the complete
resolution. A library-only project need not install the op itself as a package.

One possible launcher form is:

```sh
exec uv run --project ./python --no-sync python ./libexec/main.py "$@"
```

**Proposed c2j behavior:** recognize this declared project and bind the qualified
call to its retained interpreter, following the existing qualified `uvx`/`pnpm`
model. The explicit `python` command executes in that environment. This must not
fall through to an ordinary uv invocation that creates or modifies an environment
during execution. An explicit environment-name selector would also be acceptable;
the requirement is an unambiguous binding, not a particular CLI spelling.

This keeps the existing Nix requirement that `command[0]` be under `bin/`.
Project and script paths resolve relative to the op package, independently of a
task's worktree. c2j owns the dispatcher binding; ambient `python` or a task's
`.venv` must not determine which environment runs the script.

## Required setup and execution semantics

1. **Explicit, locked inputs.** Validate the project and lock together without
   rewriting either or silently re-resolving dependencies. Record their content
   identities. Paths must stay within the resolved extension. Missing files,
   stale locks, and unsupported declarations fail during setup.
2. **Existing lazy lifecycle.** Metadata inspection alone must not install
   Python dependencies. After an activity-result miss, realize and validate the
   exact op package, then prepare its environment before starting execution.
   Unvisited ops and cached replay must not trigger uv setup.
3. **Read-only packages.** Keep environments and caches in writable worker-owned
   directories, never inside the Nix output or extension source. Do not create a
   `.venv` beside the packaged project or modify the task's environment.
4. **Compatible reuse.** Key environments by project/lock content, selected
   extras/groups, interpreter identity, platform/ABI, and relevant installer
   settings. Share uv's artifact cache across compatible ops. Do not require the
   entire op store path to match when a library-only environment is identical.
   Environment identity must cover local dependency contents if those are allowed.
5. **Explicit execution.** Bind qualified calls to the prepared interpreter.
   Reject undeclared or mismatched environment requests through this binding.
   Running the op must not resolve, install, or update its declared dependencies.
6. **Durable setup.** Use the existing setup timeout, status records, timing,
   concurrency locking, and recovery behavior. If an environment was evicted,
   restore it during setup before resuming a live step. Publish readiness only
   after installation succeeds; never reuse a partially prepared environment.
7. **Worker persistence.** Document both the retained environment directory and
   uv artifact cache that providers must preserve. Keep incompatible runtimes
   and trust domains separate. A warm prepared environment must run without
   contacting package registries.
8. **Backward compatibility.** Preserve existing CLI `uv:` dependencies and
   Nix-packaged runtimes. Add the environment declaration consistently to local/
   Git manifests, Nix metadata, installed-manifest validation, and setup history.

## Decisions for c2j

- Start with locked `pyproject.toml` projects; decide whether locked PEP 723
  scripts should be supported initially or later.
- Define interpreter provisioning: a compatible worker interpreter, a retained
  uv-managed interpreter, or an explicit packaged interpreter. Record the actual
  identity; do not select an arbitrary `python` from PATH. Any interpreter
  download belongs in setup.
- Define extras/groups and registry configuration as setup inputs. Credentials
  must remain outside manifests and durable logs.
- Decide whether the first version requires compatible wheels or allows source
  builds. Native libraries and build tools still need an explicit contract;
  moving Python dependencies to uv does not remove those requirements.
- Consider initially excluding editable installs and local path dependencies to
  simplify environment sharing and read-only package support.

## Acceptance criteria

- A Nix-packaged fixture imports a declared Python library and implements the
  normal JSON op protocol, with that library absent from its Nix closure.
- Two ops using the same compatible locked environment can reuse preparation;
  ops with overlapping dependencies reuse cached artifacts without requiring
  identical environments. Different versions remain isolated.
- A cold run records dependency setup separately from execution. A warm run
  works with registry access disabled and performs no dependency installation.
- Stale locks, incompatible Python, failed installation, and undeclared
  environment calls fail clearly before op execution where applicable.
- Concurrent preparation, eviction recovery, read-only package paths, and a
  deliberately conflicting ambient Python installation are covered by tests.
- Cached replay and unvisited ops invoke neither uv nor package retrieval.
- Integration coverage runs on both `x86_64-linux` and `aarch64-linux` in CI.

Once this contract exists, c2ops can migrate Pydantic, LiteLLM, Jev, and Aider to
small Nix payloads plus manifest-declared uv environments. This proposal does not
change their current packaging or imply that the feature is already implemented.
