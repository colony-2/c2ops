# Go release binaries with pinned Nix SDKs

`go.build` builds a committed Go main package for Linux and Darwin, on amd64 and
arm64. One selector handles automatic and manual SDK selection:
`nix:github:colony-2/c2ops/<published-commit>#go-build`. The local/Git manifest at
`go_build/op.yaml` invokes the published `go-build` package; it does not compile
this checkout. Build and publish the package and its runtime closure before using
a new revision on execution workers.

Pass JSON on stdin. Success writes `{"output":{"intent":...,"files":...,"provenance":...}}`
on stdout; diagnostics use stderr and failure exits nonzero without a success
envelope. The complete [input schema](op.json), [input example](examples/input.json)
and [observed fixture output](examples/output.json) define the wire names. Example
hashes describe a disposable committed fixture, not downloadable product assets.
The example's absolute workspace/inbox/outbox paths are illustrative.

```yaml
- id: build
  op: nix:github:colony-2/c2ops/<published-commit>#go-build
  inputs:
    intent: ${{ inputs.intent }}
    worktree_path: ${{ context.environment.op.worktree_path }}
    artifact_inbox_path: ${{ context.environment.op.inbox }}
    artifact_outbox_path: ${{ context.environment.op.outbox }}
    module_dir: .
    entrypoint: ./cmd/cli
    linker_values:
      main.version: ${{ inputs.intent.version }}
      main.commit: ${{ inputs.intent.source_commit }}
      main.date: ${{ inputs.intent.build_date }}
```

The caller supplies a v1 intent: `schema_version`, stable `release_id`, canonical
`repository` (`github.com/OWNER/REPO`), full `source_commit`, `target_branch`,
`version` without a `v`, matching `tag` (`vVERSION`), UTC RFC3339 `build_date`, and
`binary_name`. All three roots are explicit existing absolute directories, with
no overlap. The recorded origin must match the intent's repository. HTTPS, SCP
and SSH GitHub origins are recognized; scoped URL rewrites remain available for
Git authentication without changing the recorded identity.

`module_dir` defaults to `.`. `entrypoint` is `.` or a local `./path`; each target
must load a main package. `targets` defaults to all four supported pairs, sorted
by GOOS/GOARCH. An explicit empty list, duplicates, Windows and unsupported
architectures fail. `build_tags` accepts up to 64 simple Go tag names.

## Select an SDK

Omit `go_version` to read `module_dir/go.mod`. `go_version_file` can explicitly
select that go.mod or a committed go.work relative to the repository root. A
concrete `toolchain go1.x.y` wins over `go`; `toolchain default` falls back to
`go`. With an explicit go.work, the selected module must be an active member.
Validate the workspace and every active member's Go minimum and local paths.
Without an explicit go.work, workspace mode is off, including ambient workspaces.

Set `go_version: "1.26.1"` for that exact patch, or `go_version: "1.26"` for the
newest stable patch in this package's mapping. An override wins over discovery,
while all active minimums still apply. Exact unsupported patches, prereleases,
`latest`, ranges, malformed files and versions below a minimum fail without
substitution. There is no op per compiler version and no automatic Go toolchain
download.

| Exact version | Immutable nixpkgs revision | Attribute |
| --- | --- | --- |
| 1.26.1 | `e4074f13905f5117d20ad71358596b41694863aa` | `go_1_26` |
| 1.26.8 | `e7439b6b14ad3cc35d05608ebca9bce01a25f5f8` | `go_1_26` |

The mapping in [sdk.go](sdk.go) records the exact output for each of
`x86_64-linux`, `aarch64-linux` and `aarch64-darwin`. The minor `1.26` currently
resolves to `1.26.8`. Changing this set requires a new op package revision.
The SDK paths are assembled at runtime so the operation does not retain every
compiler in its Nix runtime closure. Git and Nix are bound runtime tools.

A valid prepared output is reused offline. Otherwise Nix retrieves only the
recorded prebuilt output and its closure, keeping a temporary GC root until the
build ends. Every installation sets `max-jobs=0`, empty builders, IFD disabled,
fallback disabled, signature requirements enabled and a rejecting pre-build
hook. The executable itself rejects hook arguments before reading stdin; this
also blocks `preferLocalBuild`. No source build, archive fallback, moving-attribute
lookup, new cache service or weakened signature setting is used. A missing or
inaccessible closure fails with its exact version, revision and host.

The operation executes the absolute mapped `bin/go`, checks the actual stable
version and native OS/architecture, and rejects mismatches before package loading
or compilation. Metadata evaluation tests compare all six mapped outputs with
the immutable Nix definitions without installing those outputs.

## Build the recorded source

The release source is a private projection of Git tree/blob objects, never a
copy of the ambient checkout. HEAD must equal the full intent commit, the index
must match it, and every tracked file's bytes and executable mode must match,
even with `assume-unchanged` or `skip-worktree`. Git replacement/routing overrides
are disabled for source reads. Commit, tree and blob object hashes are verified.

The projection includes all committed paths, directories, modes and bytes,
including force-added ignored files and files marked `export-ignore`. Checkout
filters and Git archive rules do not apply. Untracked/ignored source and embedded
assets are excluded. Required generated files must be committed. Escaping,
absolute or unresolved symlinks, escaping workspace members/local replacements,
submodules and LFS pointers fail explicitly. Contained committed symlinks,
replacements and workspaces are supported. The caller's files are left untouched.

Source directories/files are protected from writes during compilation and the
complete projection is reverified afterward. Source output and private module,
build and temporary caches live elsewhere. `TMPDIR` must allow a private directory
outside all three bound roots. Go package loading, discovery and go:embed all
use this projection. Module dependencies remain allowed through committed module
metadata and Go's existing checksum verification.

Before compiling each target, `go list -deps -json` identifies the complete
selected package graph. Non-SDK assembly and recursively included headers must
stay within the verified projection or their own checksummed module in the
isolated module cache. The check decodes include string escapes, follows nested
headers using Go assembler search semantics, checks every conditional branch,
and rejects absolute/escaping/missing includes. Local headers that shadow SDK
or generated header names are checked too. Only the standard SDK headers
`textflag.h`, `funcdata.h`, `asm_amd64.h`, `asm_ppc64x.h`, `asm_riscv64.h` and the
compiler-generated `go_asm.h` may resolve outside those source roots. Pinned SDK
assembly remains trusted SDK input. Macro-generated preprocessor directives and
nonliteral include paths are explicitly unsupported. Ordinary macros without
directive-generating `#` tokens remain supported. These checks close assembler
reads that projection hash verification alone cannot detect.

Builds use `CGO_ENABLED=0`, `GOENV=off`, `GOTOOLCHAIN=local`, the mapped `GOROOT`,
controlled `GOWORK`, `-mod=readonly`, `-trimpath`, and `-buildvcs=false`. Ambient Go
flags, overlays, architecture tuning, compiler launchers and workspace settings
are removed. Scoped Git configuration, HOME, helper sockets and Git credentials
survive; proxy settings/authorization remain separate. Arbitrary build flags and
generated-source import hooks are not supported.

`linker_values` maps Go string symbols to values. It uses literal argv and the
Go linker argument parser, with no shell interpolation. Spaces, dollar signs,
semicolons and either quote style work. A single value containing both quote
styles is rejected because cmd/go's linker argument quoting has no escape syntax.
NUL/newlines are rejected. Provenance records sorted symbols and the SHA-256 of
the UTF-8 compact sorted JSON map, with literal Unicode and no HTML escaping;
it does not repeat values. Basic stripping flags are recorded separately.

Signals cancel SDK/build process groups and their children. Any target failure,
source change or cancellation fails the invocation. Raw tool stderr is suppressed
to keep credential-bearing Git/proxy/helper diagnostics out of results and retained
artifacts; errors identify the failing stage and target. Diagnose an authorized
fixture directly when its compiler needs more detailed source diagnostics.

## Consume artifacts and provenance

All targets and the source inventory are staged and published by one directory
rename only after success. Use a fresh outbox: an existing `go-build` path is
rejected, including a symlink. Binary artifact names are
`go-build/binaries/GOOS_GOARCH/NAME`; inventory is
`go-build/provenance/source-tree.json`. Descriptors repeat the v1 release/source
identity and contain `name`, byte `size`, lowercase `sha256`, `kind` and target
fields for binaries. Names are artifact keys under outbox, not producer paths.

Bind actual artifact references from `sequence.build.artifacts[descriptor.name]`
into the consumer node's `artifacts` map using the same names. Export them in
recipe `outputs.artifact_refs` across job boundaries. JSON output alone does not
hydrate bytes. Consumers must validate identities, names, size/hash, symlinks and
expected targets and restore executable mode after transport. This operation
produces binaries and a document; it does not sign, archive, tag or publish them.

`provenance.schema_version` is 1. Its `source` has `commit`, `tree`,
`object_format`, `policy=git-tree-blobs-v1`, `path_count`, `inventory_name` and
`inventory_sha256`. The [example inventory](examples/source-tree.json) records every
tree/blob path as RFC4648 `path_base64`, Git `mode`, `object_id` and blob `size`
(null for directories), sorted by raw path bytes. This preserves unusual filenames.

`sdk` records `requested`, nullable `selection_file`/`directive`, `resolved_version`,
`actual_version`, `nixpkgs_revision`, `attribute`, `store_path`, `compiler_path`
and `host_system`. Resolved and actual versions must match. An explicit override
uses `directive: null`. `build` records module/entrypoint, nullable workspace,
ordered targets, linker symbols/digest, effective Go controls, allowed flags and
`assembly_policy: "contained-includes-v1"`. This added build provenance field is
required by the current output schema; intent/file descriptor fields are unchanged.
An active `GOWORK` is the absolute temporary projection path; `workspace_file` is
the portable repository-relative selection. No inherited environment dump is
recorded. Result JSON is capped at 128 KiB; inventory and binaries use artifacts.

[Tests and acceptance boundaries](TESTING.md) describe the exercised requirements,
commands, opt-in private fixtures and remaining host/cache/storage checks.
