# Nix packages and Cachix

The flake exports all ops for `x86_64-linux`, `aarch64-linux`, and Apple Silicon
macOS (`aarch64-darwin`). Each package has an evaluation-time `c2j` manifest,
a matching `share/c2j/op.json`, and a
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
Packages with pnpm dependencies also bind GNU sed and coreutils on PATH:
pnpm's generated launchers need these before they can start Node. Having the
utilities somewhere in the Nix store does not make them available to a launcher.

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

Use the named **`published`** ref in package selectors. CI advances this branch
only after all three platforms finish testing, uploading, and verifying downloads:

```yaml
sequence:
  - id: code
    op: nix:github:colony-2/c2ops/published#codex
    inputs:
      prompt: Fix the failing test.
      worktree_path: "${{ context.environment.op.worktree_path }}"
```

Package attributes are `llm`, `llm2`, `codex`, `skill-run`, `gha`, `gha-many`,
`rule_gate`, `pydantic`, `aider`, `litellm`, `kimi`, and `jev`. `skill-run` has
manifest name `skill.run`. The selector already identifies the op package; do
not repeat it in `dependencies` or wrap it in `extension_execution`.

`published` is mutable: new resolutions may select newer verified outputs, while
c2j records the exact store path for an invocation and replays recorded results.
When a new commit is building or its publication fails, `published` stays on the
previous available release. Nix and c2j caches may continue using an older
resolved revision until refreshed; promotion does not replace running jobs.

`main` is a development reference and may select binaries before they are uploaded.
For reproducibility, use `nix:github:colony-2/c2ops/<verified-commit-sha>#codex`,
taking the SHA from a successfully promoted workflow's artifacts. Commit pins
identify an exact revision but still require its outputs to be retained.
`flake.lock` pins build dependencies for both moving and commit references.

Workers only substitute prebuilt outputs. An unpublished branch revision, a cache entry
removed by garbage collection, an untrusted signing key, or the wrong platform
fails during setup. Workers will not compile the package as a fallback. Rebuild
and republish the required output if it has been evicted. Metadata still
needs access to this definition repository and the flake's evaluation inputs.

Nix caches missing-output lookups. If retrying an output that has since been
uploaded, `--option narinfo-cache-negative-ttl 0` forces a fresh cache lookup.
See [Nix's negative lookup cache setting](https://nix.dev/manual/nix/stable/command-ref/conf-file.html#conf-narinfo-cache-negative-ttl).

## What is packaged

All Go ops are compiled by `buildGoModule` during the CI Nix build. The resulting
runtime closure contains a native ELF executable on Linux or Mach-O executable
on macOS for the selected architecture; it contains neither the op's Go source
nor the Go compiler. `bin/<op>` is a small
shell launcher that sets runtime paths and executes that compiled binary from
the Nix store. Seeing a shell launcher does not mean the op compiles on startup.
The package build rejects a runtime dependency on Go, and package checks verify
the compiled payload and its architecture for every Go op.

GHA source builds and its integration harness require Go 1.26.10 or newer.
`nix/gha-go.nix` pins Go 1.26.10's source archive and checksum for `gha`,
`gha-job` and `gha-workflow`; other operations retain the compiler from the
unchanged nixpkgs pin. Both compilers are excluded from GHA runtime closures.
The Go version selected by a workflow's setup action remains independent of
the compiler used to build these helpers.

Source builders can prepare that exact compiler with
`nix build .#gha.buildCompiler`. The native verification hook selects this
compiler for `gha` and `gha-runtime`, including their compiled integration
helpers, while other tasks retain the worker's general Go. Verification workers
realize the prepared compiler with builds disabled and fail actionably if it is
unavailable; they never fall back to an older compiler. This evaluation-only
package attribute does not add the compiler to any operation's runtime closure.

### Private GHA build input

GHA imports remain under `github.com/nektos/act`; `gha/go.mod` replaces that module
with the immutable `github.com/colony-2/act` version recorded in
`nix/act-source.json`. There is no retained act runtime source in this checkout.
Ordinary Go builders need Go 1.26.10 or newer, private-module policy covering
`github.com/colony-2/act` (`GOPRIVATE`, and any overridden `GONOPROXY` /
`GONOSUMDB`), and an authorized Git route. Policy alone supplies no credentials.
The fork requires necessary transitive module upgrades; use the committed sums.

Before any sandboxed GHA dependency/package build, run:

```sh
python3 scripts/prefetch-act.py
nix build .#gha .#gha-job .#gha-workflow
```

Run prefetch in the source builder's client environment with its authorized Git
credential helper. It fetches the pinned commit outside the sandbox, verifies the
NAR hash and imports it under the exact fixed-output name `act-source`. An already
acquired tree can be supplied with `--source PATH`; it receives the same hash
verification. A mismatch or unavailable route/revision fails without a retained
source fallback. On failure, check private Git authorization and store access;
the tool suppresses fetcher diagnostics because ambient helpers can print secrets.
Never put a token in a URL, Nix configuration, derivation or source pin. CI performs
this prefetch before `nix flake check`, passing its existing authorized secret only
in that step's environment.

Only the checked vendor derivation uses a temporary relative replacement pointing
to the verified source. It restores the immutable remote identity in vendor
metadata before hashing. Binary builds use the remote Go metadata and that vendor
output. Embedded JavaScript/schema JSON are checked, and act/Docker licenses are
installed with each compiled helper. Neither act source nor either Go compiler is
allowed in the GHA runtime closure.

Download-only consumers evaluate package definitions and substitute the final
closure without private fork access or running this prefetch. An absent closure
with builds disabled fails instead of silently building. Cache publication is a
separate CI responsibility; prefetch is required only for source builders. Prior
published closures remain usable. The Go minimum applies to helper compilation,
not the Go version requested by authored workflow actions.

Host command completion cleans up descendants remaining in its command group;
callers cannot rely on such background processes surviving a completed command.
Writers must return from writes. Escaped process groups and worker destruction
remain the provisioner's responsibility.

Nix still fetches the flake repository and its inputs to evaluate package
definitions. This metadata/source download is separate from the executable
output downloaded from Cachix. Go ops' `op.yaml` manifests declare the corresponding
published op package as their sole dependency and invoke it using
`nix run github:colony-2/c2ops/published#<op> --`. c2j binds this qualified command
to the prepared executable; no compilation happens during invocation. Nix selectors
use `op.json` with `command: ["bin/<op>"]` and no self-dependency. Use
`nix:github:colony-2/c2ops/published#codex` to select the packaged Codex op.

| Ops | Included in the Nix runtime closure | Additional c2j setup |
| --- | --- | --- |
| `llm`, `llm2` | Compiled Go op and Git | None |
| `codex`, `skill-run` | Compiled Go op, SQLite implementation, Git, native Codex 0.161.0 | None |
| `gha`, `gha-many` | Compiled Go op, Git, Docker client | Reachable Docker daemon for the local backend; credentials for the GitHub backend |
| `rule_gate` | Compiled Go op | None |
| `pydantic` | Python 3.14, PydanticAI Slim 2.31.0 and OpenAI/Anthropic/Google provider libraries | Provider credentials |
| `litellm` | Python 3.14 and nixpkgs LiteLLM 1.102.1 with a shutdown fix | Provider credentials |
| `jev` | Python 3.14, TypeSafe SDK 0.7.4 and nixpkgs library dependencies | Provider credentials |
| `aider` | Python 3.14, nixpkgs Aider 0.86.1 using the same patched LiteLLM, Git | Provider credentials |
| `kimi` | Python 3.14 and Git | `pnpm:@moonshot-ai/kimi-code@2.1.1` |

Python environments use nixpkgs' `python3.withPackages`: each environment links
to separately packaged libraries in `/nix/store`. Ops with matching dependencies
share those store paths. The package contains the op script and a wrapper that
binds its interpreter; Aider's wrapper also binds the upstream CLI's absolute
path. We accept the versions in the pinned nixpkgs package set, even when PyPI
has newer releases, to preserve upstream package and binary-cache reuse.

There are three compatibility exceptions; their other dependencies still come
from the shared nixpkgs package set:

- TypeSafe SDK is not in this nixpkgs revision. Its wheel is packaged in
  [`nix/typesafe-sdk.nix`](./nix/typesafe-sdk.nix) with `buildPythonPackage`.
- Nixpkgs' PydanticAI 2.52.0 expects newer SDKs than nixpkgs supplies. We package
  PydanticAI/Graph 2.31.0 and Anthropic 0.108.0 as small wheels, reusing nixpkgs'
  OpenAI 2.53.0 and Google GenAI 2.16.0. Anthropic 1.6.0 uses incompatible httpx2
  clients. These versions are also pinned for uv source execution.
- LiteLLM's HTTP-client destructor can deadlock during Python shutdown. A small
  [patch](./nix/patches/litellm-finalization.patch) skips cleanup during interpreter
  finalization. Aider and the LiteLLM op share that patched library. Aider is
  rebuilt against it with nixpkgs' existing packaging and tests.

We do not maintain separate uv2nix environments or transitive Python lockfiles.

CI still pushes each op's runtime closure through the existing Cachix action.
Cachix treats paths already in `cache.nixos.org` as upstream and does not store
them in our cache. Our wrappers, composed environments, compatibility packages,
and any dependencies unavailable upstream still need publishing. Workers must retain
both substituters. See [Cachix's upstream-cache behavior](https://docs.cachix.org/faq).

No packaged op calls `go run`, `uv run`, or `npm exec`. Codex and skill-run
share nixpkgs' native Codex package through an explicit absolute executable
path. Their installed manifests need no external tool dependencies. The
`C2OPS_CODEX_BIN` environment override is available for fixtures and CLI
compatibility experiments; normally the wrapper supplies its Nix store path.

The flake also exports `codex-cli` and `aider-cli`, normal CLI packages for
direct tool use (and Aider's Python source manifest). They are not JSON-speaking
ops. Their respective packaged ops include the same CLI derivations, so
publishing the op closures also covers these helpers. For standalone Codex:

```sh
nix run github:colony-2/c2ops/published#codex-cli -- --version
```

Kimi's declared npm CLI is prepared by c2j before execution and is not stored in
Cachix. It uses a qualified call:

```sh
pnpm --package=@moonshot-ai/kimi-code@2.1.1 dlx kimi --version
```

c2j dispatches these qualified calls to retained binaries without installation
or bare-name lookup during execution. Kimi's top-level version is pinned; c2j
does not provide a transitive pnpm lock service. Retain its tool cache for reuse.

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
runs integration and unit tests on native GitHub Actions runners for Linux AMD64,
Linux ARM64, and Apple Silicon macOS (`macos-15`). Integration tests override the
public coordinates with the current checkout; they cannot accidentally test the
published branch instead. It uses
`cachix/install-nix-action` to install Nix, `cachix/cachix-action` to publish, and
`actions/upload-artifact` / `actions/download-artifact` to transfer exact selectors
and output paths. Everything runs in this single workflow:

1. `test`: build and test on all three platforms, then upload through the Cachix
   action's post-job phase. Record every op output plus `codex-cli` and `aider-cli`.
   The Docker-based minimal Linux worker check runs only on Linux; native package
   contract and CLI checks also run on macOS.
2. `verify`: wait for **all three complete test jobs**, then use a separate native
   matrix to download every recorded runtime closure into an empty Nix store.
   Only `cache.nixos.org` and the configured Cachix cache are used. Signature
   verification stays enabled; local and remote builds are disabled, including
   a rejecting build hook for derivations with `preferLocalBuild`. Retain verified
   op closures using per-revision Cachix pins before allowing promotion.
3. `promote`: wait for all three verification jobs, then fast-forward `published` to
   the exact tested SHA. It creates the branch on the first successful publication.
   Older late-finishing runs and reruns leave a newer `published` untouched.
   The branch update uses a normal, non-forced Git push, which also guards races.

Only the promotion job receives `contents: write`. It is serialized, and the
workflow's push trigger accepts only `main`, so promotion does not start another
publication cycle. Pull requests and dispatches on other branches cannot publish
or promote. A failed build, upload, download, signature check, or pin leaves
`published` unchanged. A verification job that succeeds while its sibling fails
may leave pins for that unpromoted candidate; those can be removed later.

macOS packages are built and tested on macOS. A Linux coordinator can dispatch
Nix builds to a [remote macOS builder](https://nix.dev/manual/nix/stable/advanced-topics/distributed-builds.html),
but a Linux runner alone cannot execute the Darwin binaries and package checks.
This workflow uses a native macOS runner for both building and cache verification.

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
5. Allow the workflow token to create and fast-forward `published`. Repository
   branch rules must permit this CI update; protect the branch against manual
   force pushes and deletion. Bootstrap finishes only when the promotion job
   succeeds, so wait for that first run before using `published` in workers.

The Cachix action pushes the 12 op outputs, the two CLI outputs, and their runtime closures,
including outputs reused from a previous build. It does not upload every build
input or compiler. Upstream NixOS cache entries are deduplicated by Cachix.
Use the workflow's `nix-packages-<system>` artifacts to find public coordinates,
the tested source revision, and exact store paths. An artifact from a failed
workflow is not publication confirmation; wait for `promote` to succeed. The
upload action is registered only after all tests pass.

### Retention

CI creates [Cachix pins](https://docs.cachix.org/pins) named
`c2ops-<revision>-<system>-<op>` for each verified op closure. The native CLI
dependencies are retained through their owning ops. These pins keep earlier
releases available and are not automatically pruned by this workflow. Pinning
does not duplicate shared store paths or increase the cache's storage quota.

Manage the retention horizon in the Cachix dashboard: keep the current
`published` revision, any commit-pinned recipes still in use, and previous
revisions needed by queued or already-resolved jobs. Removing their pins makes
those outputs eligible for cache garbage collection and may break later installs.
Monitor cache capacity and retire unused release pins explicitly.

### Manual publication

For a manual publication on a native builder:

```sh
nix flake check -L
nix build .#rule_gate
cachix push colony2 ./result
```

This uploads an individual output; dispatch the workflow on `main` to verify and
advance `published`. Cachix credentials come from the environment or CLI configuration.

## Development and updates

```sh
make manifests                 # regenerate op.json after editing op.yaml
make check-manifests            # schemas and commands must stay in sync
nix flake check --no-build --all-systems
nix flake check -L              # builds every op and runs package contract checks
nix eval --json .#packages.aarch64-linux.codex.c2j
```

### Build settings for development versus workers

`max-jobs = 0` disables **Nix builds**, not c2j job execution or Go tests. It is
appropriate for execution workers and the publication download check, where a
missing cached package should fail instead of compiling on the worker.
Development and CI need builds enabled to create changed packages and run their
Nix contract checks. The test launcher still requires those packages to have
been built first; its download-only setting is intentional.

Some base images also install a `pre-build-hook` that rejects all builds. On a
development instance of that image, enable builds for the check command only:

```sh
nix flake check -L --max-jobs 2 --cores 2 --option pre-build-hook ""
```

This requires a writable Nix store and the working native Nix installation.
It leaves the image's default worker policy unchanged. CI's build job uses a
build-enabled Nix installation; the separate publication verification explicitly
disables builds to prove that complete packages can be downloaded from caches.

The package checks compare installed and evaluation-time manifests and invoke
every op with an empty PATH and invalid input from its read-only package
directory. They also exercise a successful `rule_gate` invocation, check Python
source pins against installed distributions, construct all three PydanticAI
providers, and verify that the Python library ops import the same nixpkgs
Pydantic store path. Integration tests
use the packaged processes and real pinned clients against mock providers; the
package checks need no provider credentials. CI disables import-from-derivation during evaluation.

`python3 scripts/check-codex-worker.py` additionally runs the built Codex and
skill-run packages in the minimal worker container, using the native CLI in their closures
with first and repeated invocations and compiler and package-manager commands disabled. It checks `version.ready`, then
deliberately rejects an invalid checkpoint before any provider request. CI runs
this regression on both architectures. It requires Docker, Nix, and prebuilt
packages; set `C2OPS_TEST_FLAKE=path:$PWD` to test a local checkout.

`make test-tools` includes promotion tests against disposable Git repositories.
CI also sets `C2OPS_TEST_NIX_PUBLICATION=1` to exercise real signed cache imports,
missing dependencies, signature rejection, and Cachix pinning against a local
test server. To run those checks locally, install Nix and Cachix, then run
`C2OPS_TEST_NIX_PUBLICATION=1 make test-tools`. No remote cache writes are made
by these tests.

Add new ops to `nix/ops.json`. Edit schemas in `op.yaml`, then regenerate `op.json`.
The generator fails if a discovered manifest is absent from the catalog.

For Python dependency updates, prefer updating the pinned nixpkgs package set.
Keep the PEP 723 pins in Pydantic, LiteLLM, and Jev's `main.py` aligned with the
installed distributions; the Nix checks enforce agreement. Provider libraries
are selected explicitly in `nix/packages.nix`. Update the version and wheel hash
in the corresponding `nix/*.nix` expression for a locally packaged library.
Recheck whether newer nixpkgs packages eliminate each compatibility exception.
Accept compatible
nixpkgs versions rather than overriding packages solely to match the latest PyPI
release, since an override can prevent reuse of upstream substitutes.

For Go module changes, update the matching
entry in `nix/go-vendor-hashes.json`: temporarily set it to
`sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=`, build that package, replace
it with the actual hash reported by Nix, and build again. Hashes cover production
source dependencies, excluding test-only private c2j/JobDB imports.

Update Nix inputs with `nix flake update` and commit `flake.lock`. Also update the
pinned nixpkgs revision in local/Git `op.yaml` dependency references, then
regenerate the manifests. Check that
the source manifests' Python attributes still match `pkgs.python3`. Local/Git
Go manifests run published prebuilt packages, not the checked-out Go files. To
test local Go edits, build the local flake and select
`nix:path:/absolute/path/to/c2ops#<op>`. Python source manifests can still perform
uv script library setup at invocation time. Aider's source wrapper uses its declared Nix CLI through
`nix run <reference> -- ...`; packaged execution binds that CLI directly.

## Integration tests and local overrides

All op process integration tests and recipe fixtures name public coordinates
such as `nix:github:colony-2/c2ops/published#codex`. `scripts/op_test.py` shares their
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

With the override unset, `make test` resolves public `published` packages and requires
the configured Cachix cache. There is no fallback to `go run`, source manifests,
or `uv run --script` for op invocations. The runner evaluates package metadata,
realizes that exact output with builds disabled, checks the installed manifest,
and runs its packaged command. Unit tests still import the implementations.

Codex recipes run through the real c2j Nix resolution/setup lifecycle. Their
harness overrides the flake reference and replaces the model client in a private
tool binding directory after real dependency setup; the op executable, objects,
and artifacts remain real. Successful fixtures assert that c2j prepared a Nix package.

The fixture suite also reads every Go `op.yaml`, prepares its declared package
through c2j, and executes its qualified command from an empty directory with Go
blocked. These tests check all seven Go ops, including a successful policy check
and real native Codex version probes. The same test flake override applies to
both each manifest's dependency and command. All three CI platforms run this suite.

The test harness itself needs Go, Python, uv, Node, pnpm, and Git; `nix develop`
provides them. Python unit-test libraries may be downloaded by uv. `scripts/with-tools.go`
uses c2j to prepare the CLI dependencies declared in the installed manifest,
for packaged process tests and the Codex test launcher. These harness dependencies
are distinct from packaged op libraries, which are already in the Nix outputs.
