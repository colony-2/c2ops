# Nix packages and Cachix

The flake exports all 12 ops for `x86_64-linux` and `aarch64-linux`. Each package
has an evaluation-time `c2j` manifest, a matching `share/c2j/op.json`, and a
JSON-speaking executable under `bin/`. `flake.lock` pins all Nix evaluation and
build inputs. There is no default op: select the attribute explicitly.

The test commands in the upstream extension and execution-tools guides refer
to a c2j checkout. This repository's checks are described below.

## Worker setup

Use a c2j build implementing [EXTENSION_OPS.md](./EXTENSION_OPS.md) and
[EXECUTION_TOOLS.md](./EXECUTION_TOOLS.md). The guide was checked against c2j
commit `3849f3a48140a8b1513ece9911a9a2ce7acd3e05`; the older c2j version in
Codex's integration-test module does not exercise the Nix setup lifecycle.

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

Pin a package selector to a commit whose **test workflow completed successfully**,
including the Cachix action's upload step:

```yaml
sequence:
  - id: code
    op: nix:github:colony-2/c2ops/<commit>#codex
    inputs:
      prompt: Fix the failing test.
      worktree_path: "${{ context.environment.op.worktree_path }}"
```

Package attributes are `llm`, `llm2`, `codex`, `skill-run`, `gha`, `gha-many`,
`rule_gate`, `pydantic`, `aider`, `litellm`, `kimi`, and `jev`. `skill-run` has
manifest name `skill.run`. The selector already identifies the op package; do
not repeat it in `dependencies` or wrap it in `extension_execution`.

Workers only substitute prebuilt outputs. An unpublished commit, a cache entry
removed by garbage collection, an untrusted signing key, or the wrong platform
fails during setup. Workers will not compile the package as a fallback. Rebuild
and republish the same revision if its outputs have been evicted. Metadata still
needs access to this definition repository and the flake's evaluation inputs.

## What is packaged

| Ops | Included in the Nix runtime closure | Additional c2j setup |
| --- | --- | --- |
| `llm`, `llm2` | Compiled Go op and Git | None |
| `codex`, `skill-run` | Compiled Go op, SQLite implementation, Git | `pnpm:@openai/codex@0.157.1` |
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

[The existing test workflow](./.github/workflows/test.yml) runs the source tests,
then uses native GitHub Actions runners for both Linux architectures. It uses
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
Use the workflow's `nix-packages-<system>` artifacts to find the exact published
revision and store paths. An artifact from a failed workflow is not publication
confirmation; wait for the entire job, including the post-job upload, to succeed.

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
directory. They also exercise a successful `rule_gate` invocation. Source tests
use real pinned clients against mock providers; the package checks need no
provider credentials. CI disables import-from-derivation during evaluation.

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
