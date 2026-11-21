# GitHub tags and verified release assets

`github.release` publishes an explicitly selected commit and a complete set of
hydrated final assets. It creates missing release tags, reuses matching tags and
resumes matching drafts. It returns public identity only after downloading and
hashing every asset, including anonymous public downloads. A failure returns no
success envelope; a tag, draft or even a published release can remain visible.

Use Nix selector `nix:github:colony-2/c2ops/REVISION#github-release` after the
coordinator confirms that the successful merged revision is published for your
host. The source selector is `./github_release`; its manifest invokes the
published package. Package definitions cover `x86_64-linux`, `aarch64-linux` and
`aarch64-darwin`. Only native Linux arm64 packaging has been exercised locally;
other hosts, cache-only installation and live publication remain unverified.

## Calling the operation

The process reads one JSON object from stdin and writes `{"output":{...}}` on
success. On failure it writes a sanitized diagnostic to stderr and exits nonzero
without stdout. Input and success envelope each have a 128 KiB limit. The
[manifest](op.yaml) contains the input/output schemas; the standalone
[output schema](output.schema.json) defines the payload within the envelope.

Pass `intent`, `files`, `provenance`, `workspace_root`, `inbox_root` and
`outbox_root`. Roots must be explicit normalized absolute existing directories,
without symlink ancestors. Outbox must be separate from inbox and workspace.
The op never reads release source from the ambient checkout or writes its cwd.

Bind **authorized** GitHub credentials as `GITHUB_TOKEN` in the recipe's process
environment. The op itself neither discovers credentials nor selects an ambient
GitHub CLI profile. It needs permission to read the public destination and create
its refs/releases/assets (for example a repository-scoped token with Contents
write permission), subject to repository rules. No production authorization is
implied by installing this package. There is no OIDC or approval service.

A recipe node uses these input bindings, after binding the exact final files in
its `artifacts` map:

```yaml
op: nix:github:colony-2/c2ops/REVISION#github-release
inputs:
  intent: ${{ inputs.intent }}
  files: ${{ sequence.pack.outputs.files }}
  provenance: ${{ sequence.pack.outputs.provenance }}
  workspace_root: ${{ context.environment.op.worktree_path }}
  inbox_root: ${{ context.environment.op.inbox }}
  outbox_root: ${{ context.environment.op.outbox }}
```

The enclosing recipe must explicitly authorize/bind `GITHUB_TOKEN` using its
existing secret mechanism. Do not put the token in intent, provenance, command
arguments or artifact contents. This example omits recipe-specific secret syntax
rather than inventing a c2j secret field.

## Wire identity and provenance

Intent has exactly these fields:

```json
{
  "schema_version": 1,
  "release_id": "cli-1.2.3",
  "repository": "github.com/example/disposable",
  "source_commit": "0123456789abcdef0123456789abcdef01234567",
  "target_branch": "main",
  "version": "1.2.3",
  "tag": "v1.2.3",
  "build_date": "2026-11-18T12:00:00Z",
  "binary_name": "cli"
}
```

Require a canonical public `github.com/OWNER/REPO`, a full lowercase 40/64 hex
commit, a SemVer-shaped version without `v`, `tag = vVERSION`, locally valid Git tag/branch syntax and
UTC RFC3339 date. Repository and version lengths are bounded. Duplicate or incorrectly cased JSON
keys, unsupported schema versions and unknown input/descriptor fields fail.

Each file repeats `schema_version`, `release_id`, `repository`, `source_commit`,
`version` and `tag`, and adds `name`, `size`, `sha256`, `kind`. `name` is a unique
slash-relative artifact key under inbox, for example
`assets/cli_1.2.3_Linux_x86_64.tar.gz`; it is never a producer-local path.
SHA-256 is lowercase hex; `size` counts actual bytes. Accepted kinds are
`archive`, `checksums` and `document`; archives additionally require `goos`
(`linux`/`darwin`) and `goarch` (`amd64`/`arm64`). All other identity fields must
match intent exactly. At least one archive and exactly one `checksums.txt` are
required, with at most 100 total files. The caller/packer defines required target
coverage; this consumer does not invent missing targets.

All file path components use ASCII letters, digits, `.`, `_` or `-`, begin with
an alphanumeric character and cannot end with `.`. Public basenames must be
unique. Reject traversal, normalization changes, symlinks, nonregular files,
missing files and inconsistent hashes/sizes. Nonregular paths are rejected before
opening; nonblocking open and inode rechecking also guard FIFO replacement races. The checksum document uses
`SHA256␠␠BASENAME\n`, includes every other selected asset exactly once and has
no extra entries. The op streams copies into private outbox scratch, hashes the
copies and retains validated files under the same artifact keys before mutation.
Uploads read only the staged copies. There are no binary bytes in result JSON.

The shared design leaves provenance nesting open. This consumer's explicit v1
projection is:

```json
{
  "schema_version": 1,
  "release_id": "cli-1.2.3",
  "repository": "github.com/example/disposable",
  "source_commit": "0123456789abcdef0123456789abcdef01234567",
  "version": "1.2.3",
  "tag": "v1.2.3",
  "source_tree": "abcdef0123456789abcdef0123456789abcdef01",
  "source_materialization": "git-tree-verified",
  "build": {"compiler": "go1.26.1"},
  "pack": {"format": "tar.gz"}
}
```

The first six fields equal intent; `source_tree` is the complete recorded Git
tree hash; `source_materialization` is exactly `git-tree-verified`. Additional
build/signing/packing provenance is retained unchanged, including exact JSON
numbers without float64 rounding. The draft marker hashes those exact values. The destination's Git
commit API must report the same commit and tree before writes. This validates
consistent producer records, not a cryptographic attestation or independent
proof of how compilation happened. A producer with another nesting must expose
this projection while preserving its detailed records; the real pack consumer
check awaits the coordinator's published-package confirmation. No upstream
package was substituted or consumed for this delivery.

## Reconciliation and failure behavior

The op uses only GitHub's fixed HTTPS API/upload origins. API JSON redirects
fail. Binary downloads permit at most five HTTPS redirects and strip credentials
and cookies on every redirect. Anonymous browser downloads never receive the
GitHub credential. Nix supplies trust roots, honoring an explicit
`SSL_CERT_FILE`. On macOS, a nonempty setting selects this client's entire
trust pool; keychain roots are not added to that pool. The file must be readable
and contain at least one valid PEM certificate (multiple certificates are
supported). Invalid settings fail before network access with a fixed
configuration diagnostic that omits the filename and certificate contents.
Without a setting, macOS retains native keychain trust. Linux keeps Go's native
`SSL_CERT_FILE` and `SSL_CERT_DIR` handling, including directory fallback.
Certificate chains and hostnames are always verified. HTTP requests have context cancellation and two-minute
per-request timeouts; read requests retry transient errors at most three times,
with a bounded Retry-After delay. Large/malformed JSON responses fail. Writes
are never blindly retried.

1. Validate and retain all local assets/checksums, then read the exact repository
   and commit/tree. Private, renamed, archived or disabled destinations fail.
2. Read the exact tag and peel annotated tags with cycle/depth checks. Create a
   missing lightweight ref at `source_commit`; reread even successful creates.
   Races and ambiguous writes reconcile the observed ref before another attempt.
   Conflicting, denied or protected tags fail. There is no force, update or delete.
3. Enumerate releases and assets with bounded pagination. A new release is a
   draft with explicit `target_commitish`. Its entire body is an ownership marker
   hashing canonical intent, name-sorted descriptors and provenance. Only matching
   marked drafts can resume; conflicting/unmarked drafts fail. Unmarked legacy
   public releases can reconcile through tag identity and complete downloaded
   bytes without being edited. Duplicate releases for a tag fail.
4. Verify existing asset IDs by downloading actual bytes; upload only missing
   draft assets and verify the resulting bytes. Unexpected/duplicate assets,
   incomplete uploads and differing bytes fail, including same-size conflicts.
   Draft conflicts are preserved for investigation rather than deleted/replaced.
5. Reread ownership, tag and all asset bytes before changing `draft` to false.
   Reread ambiguous publication. Recheck tag and complete API/public downloads
   before success. A still-draft, inaccessible or incomplete public release
   cannot authorize npm. A retry can reconcile an already published match.

Tag, draft/upload creation each get at most two write attempts, with intervening
reads establishing whether the effect exists. Publication gets one PATCH per
invocation; a later invocation rereads retained state. No operation deletes a
tag, release or asset. A failure after remote reads retains sanitized stage,
observed commit, release ID and asset IDs in `error/github-release.json`, where
possible. Raw bodies, transport URLs and credentials are excluded. The outbox
must be fresh for each invocation so retained artifacts cannot overwrite earlier
evidence. Preserve remote state and retry with the same intent/file bytes.

GitHub does not offer a transaction over these steps. Rechecks detect observed
concurrent changes but cannot prevent a later edit by another actor. The product
coordinator owns serialization and durable intent. A merge is never undone by
this op, and a final public verification failure can occur after publication.

## Result and artifact transport

Success preserves `intent`, `files`, `provenance` and adds `release`:

```json
{
  "id": 42,
  "url": "https://github.com/example/disposable/releases/tag/v1.2.3",
  "tag": "v1.2.3",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "draft": false,
  "assets": [{
    "id": 100,
    "name": "cli_1.2.3_Linux_x86_64.tar.gz",
    "download_url": "https://github.com/example/disposable/releases/download/v1.2.3/cli_1.2.3_Linux_x86_64.tar.gz",
    "file": {"...": "the original complete file descriptor"}
  }]
}
```

This abbreviated release example is explanatory; successful results include all
assets and complete descriptors, as pinned by the schema and byte-backed tests.
`release.assets[].file.name` remains the artifact key; the asset `name` is the
public basename. npm may proceed only on successful complete output. Hydrate
retained files through node `artifacts` bindings and export their stored refs
through `outputs.artifact_refs` for another job. JSON references alone do not
transfer bytes; outbox files are the actual transport inputs.

## Validation

Run `cd github_release && go test -race -count=1 -cover ./...` and
`python3 -m unittest -v scripts/test_verify.py`. [Acceptance mapping](TESTS.md)
distinguishes executed fixtures/local packaging from gated live checks.
The verify hook runs this race-tested suite as its own task and includes its
status/log hashes in aggregation. Nix checks exercise the source suite and the
installed binary/manifest boundary, including a complete local TLS release flow
from immutable package cwd with empty PATH; metadata stays evaluation-only.
Run that installed success check with `C2OPS_GITHUB_INSTALLED_BINARY` bound to
the actual `/nix/store/.../bin/github-release` wrapper and
`go test -count=1 -v -run '^TestC03InstalledPackageFromImmutableCWD$' .` from
`github_release/`. No production endpoint overrides are added.

No live disposable GitHub release, production release, npm publication or
upstream pack-package consumer check ran. G11 and live G12 require an authorized
disposable destination/credential; G11 also requires confirmed `release.pack`.
The [executable live harness instructions](LIVE_TESTS.md) describe explicit
authorization and upstream confirmation gates. Cache-only installation and all
advertised package hosts remain required
acceptance checks. A locally built package is not publication confirmation.
