# Nix packages and Cachix

The flake exports all 12 ops for `x86_64-linux` and `aarch64-linux`. Each package
has an evaluation-time `c2j` manifest, a matching `share/c2j/op.json`, and a
JSON-speaking executable under `bin/`. `flake.lock` pins all Nix evaluation and
build inputs. There is no default op: select the attribute explicitly.

The test commands in the upstream extension and execution-tools guides refer
to a c2j checkout. This repository's checks are described below.

## Worker setup

Use a c2j build implementing [EXTENSION_OPS.md](./EXTENSION_OPS.md) and
[EXECUTION_TOOLS.md](./EXECUTION_TOOLS.md). Codex's integration harness uses
c2j `v0.0.64-0.20261010012403-3849f3a48140` and enables `tool_setup_version: 1`
to exercise the Nix setup lifecycle.

Enable `nix-command` and `flakes` in Nix, then configure the public cache:

```sh
cachix use colony2
```

This installs the cache's real public signing key and substituter configuration;
do not invent a key or disable signature verification. Apply the configuration
to the Nix daemon/user that runs c2j, including inside worker containers.
Retain `cache.nixos.org` as a substituter for upstream runtime dependencies.
The worker base still needs Nix, git, uv with Python, and pnpm with Node as
described in the execution-tools guide. Kimi needs Node.js 24.15 or later.

pnpm 12 requires explicit decisions for dependency install scripts. Set this
in the worker environment **before** c2j tool setup (not in op `inputs.env`):

```sh
export PNPM_CONFIG_ALLOW_BUILDS='{"@moonshot-ai/kimi-code@2.1.1":false,"node-pty@1.1.0":false}'
```

This acknowledges and skips Kimi's global-install migration hook and the
optional node-pty build scripts. The noninteractive op uses the published CLI
assets; its real shell execution, session resume, and artifact tests pass with
these scripts disabled. Other packages keep pnpm's default policy. If the worker
already has an `allowBuilds` policy, merge these entries into it. CI and the dev
shell read the same [configuration](./nix/pnpm-build-policy.json). See
[pnpm's build settings](https://pnpm.io/settings#allowbuilds).

Use the named `main` ref in package selectors. Wait for the **test workflow to
complete successfully**, including Cachix upload, after `main` advances:

```yaml
sequence:
  - id: code
    op: nix:github:colony-2/c2ops/main#codex
    inputs:
      prompt: Fix the failing test.
      worktree_path: "${{ context.environment.op.worktree_path }}"
```

Package attributes are `llm`, `llm2`, `codex`, `skill-run`, `gha`, `gha-many`,
`rule_gate`, `pydantic`, `aider`, `litellm`, `kimi`, and `jev`. `skill-run` has
manifest name `skill.run`. The selector already identifies the op package; do
not repeat it in `dependencies` or wrap it in `extension_execution`.

`main` is mutable: new live resolutions may select newer outputs, while c2j
records the exact store path for an invocation and replays recorded results.
There can be a setup failure between a push to `main` and its completed upload.
`flake.lock` still pins package build dependencies; recipe authors need no commit
IDs. CI records the source revision separately for diagnostics.

Workers only substitute prebuilt outputs. An unpublished branch revision, a cache entry
removed by garbage collection, an untrusted signing key, or the wrong platform
fails during setup. Workers will not compile the package as a fallback. Rebuild
and republish the required output if it has been evicted. Metadata still
needs access to this definition repository and the flake's evaluation inputs.

## What is packaged

| Ops | Included in the Nix runtime closure | Additional c2j setup |
| --- | --- | --- |
| `llm`, `llm2` | Compiled Go op and Git | None |
| `codex`, `skill-run` | Compiled Go op, SQLite implementation, Git | `pnpm:@openai/codex@0.162.1` |
| `gha`, `gha-many` | Compiled Go op, Git, Docker client | Reachable Docker daemon for the local backend; credentials for the GitHub backend |
| `rule_gate` | Compiled Go op | None |
| `pydantic`, `litellm`, `jev` | Python 3.13 and locked Python libraries | Provider credentials |
| `aider` | Python 3.12, locked Aider environment, Git | Provider credentials |
| `kimi` | Python 3.13 and Git | `pnpm:@moonshot-ai/kimi-code@2.1.1` |

Python environments are built using uv2nix from per-op `uv.lock` files. Aider
uses Python 3.12 for its pinned NumPy dependency. No packaged op calls `go run`,
`uv run`, or `npm exec`. Codex and Kimi's declared CLIs are prepared by c2j before
the op's execution budget; those npm packages are **not** stored in Cachix.
Their declared top-level versions are pinned; c2j does not provide a transitive
pnpm lock service. Keep the worker tool cache persistent for reuse.

Both clients use qualified calls that name the package and executable:

```sh
pnpm --package=@openai/codex@0.162.1 dlx codex --version
pnpm --package=@moonshot-ai/kimi-code@2.1.1 dlx kimi --version
```

c2j dispatches these to retained binaries without installation or bare-name
lookup during execution. The package version must match the manifest declaration.

Nix runtime tools are bound in package wrappers. Packaged manifests must not
contain `nix:` dependencies. `uv:` and `pnpm:` references are for CLI applications,
not a way to install libraries into an op's Python environment. Recipe-specific
tools should be declared in `execution.packages` or `execution_needs.packages`:

```yaml
execution:
  packages:
    - uv:ruff==0.11.2
sequence:
  - id: lint
    op: command_execution
    inputs:
      run: uvx --from ruff==0.11.2 ruff check .
```

## CI and publishing

[The existing test workflow](./.github/workflows/test.yml) builds packages and
runs integration and unit tests on native GitHub Actions runners for both Linux
architectures. Integration tests override the public coordinates with the current
checkout; they cannot accidentally test the published branch instead. It uses
`cachix/install-nix-action` to install Nix, `cachix/cachix-action` to publish, and
`actions/upload-artifact` to record exact selectors and output paths.

One-time repository setup:

1. Create the **public `colony2` cache** in your Cachix account, if it does not
   already exist. Cachix's [free OSS plan](https://www.cachix.org/pricing) currently
   includes 5 GB; this workflow does not create a cache or subscribe to a plan.
2. Store a cache-scoped write token as the GitHub Actions secret
   **`CACHIX_COLONY2_AUTHKEY`**. Do not commit it.
3. The cache name defaults to `colony2`. Set the optional repository variable
   `CACHIX_CACHE` only to override that name.
4. Push the packaging revision to `main`, or dispatch the workflow on `main`.
   Main builds fail explicitly if the write token is missing. Pull requests
   build and validate without receiving the write token or publishing outputs.

The Cachix action pushes the 12 final package paths and their runtime closures,
including outputs reused from a previous build. It does not upload every build
input or compiler. Upstream NixOS cache entries are deduplicated by Cachix.
Use the workflow's `nix-packages-<system>` artifacts to find public coordinates,
the tested source revision, and exact store paths. An artifact from a failed
workflow is not publication confirmation; wait for the entire job, including
the post-job upload, to succeed. The upload action is registered only after all
tests pass.

For a manual publication on a native builder:

```sh
nix flake check -L
nix build .#rule_gate
cachix push colony2 ./result
```

Publish every selected package on each supported architecture before deploying
its reference. Cachix credentials come from the environment or CLI configuration.

## Development and updates

```sh
make manifests                 # regenerate op.json after editing op.yaml
make check-manifests            # schemas and commands must stay in sync
nix flake check --no-build --all-systems
nix flake check -L              # builds every op and runs package contract checks
nix eval --json .#packages.aarch64-linux.codex.c2j
```

The package checks compare installed and evaluation-time manifests and invoke
every op with an empty PATH and invalid input from its read-only package
directory. They also exercise a successful `rule_gate` invocation. Integration tests
use the packaged processes and real pinned clients against mock providers; the
package checks need no provider credentials. CI disables import-from-derivation during evaluation.

Add new ops to `nix/ops.json`. Edit schemas in `op.yaml`, then regenerate `op.json`.
The generator fails if a discovered manifest is absent from the catalog.

For Python dependency updates, change the direct pins in `main.py` and the
corresponding `nix/python/<op>/pyproject.toml`, then run:

```sh
uv lock --project nix/python/jev
```

Commit the lock and run the source and Nix checks. The generator verifies that
direct dependency declarations agree. For Go module changes, update the matching
entry in `nix/go-vendor-hashes.json`: temporarily set it to
`sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=`, build that package, replace
it with the actual hash reported by Nix, and build again. Hashes cover production
source dependencies, excluding test-only private c2j/JobDB imports.

Update Nix inputs with `nix flake update` and commit `flake.lock`. Also update the
pinned nixpkgs revision in local/Git `op.yaml` dependency references and regenerate
the manifests. Local/Git source execution remains available, with explicit tool
declarations, but still performs Go compilation or uv script library setup at
invocation time. Packaged execution avoids those steps.

## Integration tests and local overrides

All op process integration tests and recipe fixtures name public coordinates
such as `nix:github:colony-2/c2ops/main#codex`. `scripts/op_test.py` shares their
resolution rule. Set **`C2OPS_TEST_FLAKE`** to a flake reference without an
attribute to replace only the flake part; the named op is preserved and unknown
coordinates fail. The override is test-only and is not read by production ops.

To build and test the checkout:

```sh
nix develop -c make test-local
```

This runs `nix flake check`, then sets `C2OPS_TEST_FLAKE=path:<checkout>` for
`make test`. To select an already built checkout explicitly:

```sh
C2OPS_TEST_FLAKE=path:/absolute/path/to/c2ops make test
```

With the override unset, `make test` resolves public `main` packages and requires
the configured Cachix cache. There is no fallback to `go run`, source manifests,
or `uv run --script` for op invocations. The runner evaluates package metadata,
realizes that exact output with builds disabled, checks the installed manifest,
and runs its packaged command. Unit tests still import the implementations.

Codex recipes run through the real c2j Nix resolution/setup lifecycle. Their
harness overrides the flake reference and replaces the model client in a private
tool binding directory after real dependency setup; the op executable, objects,
and artifacts remain real. Successful fixtures assert that c2j prepared a Nix package.

The test harness itself needs Go, Python, uv, Node, pnpm, and Git; `nix develop`
provides them. Python unit-test libraries may be downloaded by uv. `scripts/with-tools.go`
uses c2j to prepare the CLI dependencies declared in the installed manifest,
for packaged process tests and the Codex test launcher. These harness dependencies
are distinct from packaged op libraries, which are already in the Nix outputs.
