# Portable release archives and final-byte checksums

`release.pack` packages the final binaries you explicitly select into
`NAME_VERSION_{Linux|Darwin}_{x86_64|arm64}.tar.gz`. Each archive contains the
root executable with mode `0755`, plus optional committed documentation.
`checksums.txt` hashes the completed compressed archive bytes, using lowercase
SHA-256, two ASCII spaces, the archive basename and LF. It excludes itself.
This preserves the c2j npm installer's filename, extraction and checksum format.

The source module is `release_pack/`; the package attribute and executable are
`release-pack`. Use
`nix:github:colony-2/c2ops/<confirmed-revision>#release-pack` after publication.
The local/Git manifest runs `published#release-pack`, following the repository's
prebuilt-package convention; it does not execute your source candidate. Build
this module or a `path:/absolute/candidate#release-pack` selector for candidate
checks. The Nix package binds Git and ships a compiled executable without Go.
Package hosts are Linux amd64/arm64 and Apple Silicon macOS; host acceptance is
separate from the four output targets and from metadata evaluation.

## Inputs and artifact transport

The operation reads one JSON object on stdin and writes one
`{"output":{"intent":...,"files":[...],"provenance":...}}` envelope on stdout.
Errors use stderr and nonzero exit, with no successful result. Both request and
encoded result are limited to 128 KiB. Git diagnostics, request bodies and
environment values are not echoed. Cancellation stops Git and its child process
group and prevents successful output.

[op.yaml](op.yaml) contains the source schemas; [op.json](op.json) is the generated
package manifest. Runtime validation additionally enforces cross-field identity,
exact target sets, canonical paths, final-byte integrity and source state.

| Input | Meaning |
| --- | --- |
| `intent` | v1 intent; preserved in the result, including extension fields. |
| `worktree_path` | Absolute canonical Git source root, with HEAD at `source_commit` and no tracked/index edits. |
| `artifact_inbox_path` | Absolute canonical root containing the hydrated selected binaries. |
| `artifact_outbox_path` | Absolute canonical writable root for flat archive names and `checksums.txt`. |
| `files` | Explicit final `kind:binary` descriptors, exactly one for each required target. |
| `required_targets` | Optional nonempty unique `{goos,goarch}` set; defaults to Linux/Darwin × amd64/arm64. Extras fail too. |
| `docs` | Optional unique committed file/directory selectors, preserving repository-relative paths; default empty. |

Roots must exist, be disjoint and contain no symlink components. On macOS, use
canonical paths such as `/private/tmp/...`, rather than the `/tmp` symlink.
Descriptor names are slash-relative artifact keys under the inbox. Reject
absolute paths, traversal, empty/dot components, backslashes, controls, symlinks
and nonregular files. Unselected inbox files are ignored. Hydrated modes are
ignored; archives always restore binary `0755` without modifying the inbox.

Intent requires `schema_version:1`, nonblank `release_id`, canonical
`github.com/OWNER/REPO` without `.git`, full lowercase 40- or 64-hex
`source_commit`, valid `target_branch`, numeric three-part `version` without `v`
(optional prerelease/build suffix), `tag = vVERSION`, UTC RFC3339 `build_date`
ending in `Z`, and an ASCII single-component `binary_name`. Archive basenames
cannot contain whitespace or shell syntax. `checksums.txt` is reserved.

Every selected descriptor repeats `schema_version`, `release_id`, `repository`,
`source_commit`, `version`, `tag` exactly, and supplies `name`, positive integer
`size`, lowercase 64-hex `sha256`, `kind:binary`, `goos` and `goarch`.
Signing extensions are retained unchanged in selected-input provenance.
Declared `unsigned_sha256` values must be valid. Embedded `intent` objects and
objects declaring `release_id` or `source_commit` must match the release identity.
Signer tool metadata, including its own version/repository, remains opaque. The packer checks byte identity; the caller chooses signing
policy and supplies the final descriptor. It never scans for signed-looking
filenames or infers a missing target from available files.

This one-target example uses the five bytes `hello`. A real caller supplies
actual final binary descriptors and a real immutable source commit:

```json
{
  "intent": {
    "schema_version": 1,
    "release_id": "example-1.2.3",
    "repository": "github.com/example/cli",
    "source_commit": "1111111111111111111111111111111111111111",
    "target_branch": "main",
    "version": "1.2.3",
    "tag": "v1.2.3",
    "build_date": "2026-11-18T12:00:00Z",
    "binary_name": "cli"
  },
  "worktree_path": "/job/source",
  "artifact_inbox_path": "/job/inbox",
  "artifact_outbox_path": "/job/outbox",
  "files": [{
    "schema_version": 1,
    "release_id": "example-1.2.3",
    "repository": "github.com/example/cli",
    "source_commit": "1111111111111111111111111111111111111111",
    "version": "1.2.3",
    "tag": "v1.2.3",
    "name": "final/linux-amd64/cli",
    "size": 5,
    "sha256": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
    "kind": "binary",
    "goos": "linux",
    "goarch": "amd64"
  }],
  "required_targets": [{"goos": "linux", "goarch": "amd64"}],
  "docs": ["README.md", "LICENSE", "docs"]
}
```

Files in arbitrary result JSON do not hydrate bytes. Bind actual node artifact
references to the consumer's `artifacts` under the same relative keys used in
`files[].name`; export selected `outputs.artifact_refs` across recipe/job
boundaries. For example, binding a producer's stored artifact at
`final/linux-amd64/cli` hydrates that path under the consumer's explicit inbox.
The consumer root may differ completely, and the producer workspace may be gone.
Keep binary bytes and detailed logs in artifact storage, outside result JSON.

## Source, archive and retry policy

Documentation comes directly from verified Git tree/blob objects at
`source_commit`, never from the checkout or `git archive`. Recorded object hashes
are recomputed for the commit, every intermediate tree and each selected blob;
tree entries are parsed from those authenticated bytes. Tracked/index changes
and mismatched HEAD fail. Worktree comparison uses a fresh private index loaded
from the recorded commit, so `assume-unchanged`, `skip-worktree`, sparse settings
and cached stat data cannot hide tracked edits. The caller's index is never
refreshed or rewritten; split-index output is disabled during validation.
Selected symlinks,
gitlinks, LFS pointers, missing paths, overlapping selectors and executable/path
collisions fail. Committed export-ignored and ignored paths remain included;
untracked/ignored additions remain excluded. Docs retain `0644` or `0755` from
the recorded mode. The operation does not fetch or change source files. It
removes ambient Git repository/index/object overrides and disables replacement
objects while preserving scoped Git configuration and credential access.

Binaries are copied and verified into private staging accessed through an
already-open outbox root; archives read those snapshots. Packaging validates the
whole descriptor/target set before opening binary payloads. Staging contains no
producer absolute paths. File reads are confined through opened roots, reject
symlink components, and use non-following opens for regular payloads.

The binary comes first in each tar, followed by sorted docs/directories. Targets
and checksum lines are sorted by archive basename. Policy `release-pack-v1`
uses tar PAX for long paths, Unix epoch mtime, no access/change times, zero
UID/GID, empty owner names, directory mode `0755`, gzip level 9, empty optional
gzip fields, zero gzip timestamp and OS byte 255. Repeated local inputs produce
identical bytes. Cross-toolchain/host compressed-byte reproducibility requires
measurement; normalized headers alone do not establish it.

Archive writers and files close before the final file is reread for size and
SHA-256. All files and the bounded result are prepared before promotion. New
outputs use exclusive creation; existing regular files reconcile only if their
size/hash match. Conflicting bytes, symlinks and nonregular destinations fail
without clobbering. Promotion is not transactional: interruption can leave a
partial batch, which must be revalidated on retry. No incomplete batch returns
a successful manifest.

`output.files` contains archive descriptors in basename order, then the checksum
descriptor. Each repeats the common identity; archive descriptors add their
target, while `checksums` has no target. Provenance contains `schema_version:1`,
`operation:release.pack`, `archive_policy:release-pack-v1`, verified
`source_commit`/`source_tree`, complete ordered `selected_inputs`, and sorted
`documents` entries with `name`, `git_blob`, `git_mode`, `size` and `sha256`.
Docs are inside the archives, rather than separately downloadable descriptors.

## Checks and acceptance boundaries

Run the candidate's unit, Git and process checks:

```sh
go test -mod=readonly -race -count=1 ./...
```

From the repository root, run the independent subprocess checker against a
candidate executable, or the installed wrapper with its own Git dependency:

```sh
python3 scripts/check-release-pack.py /absolute/candidate/release-pack
python3 scripts/check-release-pack.py /nix/store/OUTPUT/bin/release-pack --empty-path
make check-manifests
make test-tools
```

The Nix contract check runs the latter with an empty ambient `PATH` and a
read-only cwd, checks the installed/evaluated manifest and compiled architecture,
and extracts actual bytes/modes. `scripts/verify.py check release-pack` and the
build verify hook run the Go suite; task failures enter the existing aggregation.
`python3 scripts/verify_hook_harness.py --case release-pack --out /tmp/hook-check`
runs that task and the aggregator through the local c2j runtime, with other
operation tasks explicitly mocked.

After building this component, optionally exercise a disposable local file cache
and empty stores with:

```sh
C2OPS_RELEASE_PACK_PACKAGE=/nix/store/OUTPUT python3 scripts/test_release_pack_cache.py -v
```

That fixture imports the complete closure with builds disabled and rejects a
missing cache output despite the warm builder store. It disables signature
requirements only for its local fixture cache; it neither publishes packages nor
certifies a production cache's trust configuration.

| Requirements | Tests |
| --- | --- |
| K01–K05, X01–X04 | `pack_test.go`: all archive names, extracted final bytes/modes, final-file hashes, normalized metadata/order, snapshots, failure and retry controls. |
| C01/C04/C06/C07 | `contract_test.go`: malformed/duplicate JSON, identity/target/path/type mismatches, symlinks, nonregular inputs, corrupted/missing snapshots and JSON bounds. |
| D01–D09 | `source_test.go`: actual Git objects, committed docs/modes, injection/filters/replacements, source changes, corrupted intermediate trees (SHA-1/SHA-256) and index-flag bypass rejection without index mutation. |
| C02/C09/X05/X06 | `process_test.go`: executable subprocess, removed producer root, copied nonexecutable inputs, private diagnostics and cancellation. |
| C03, local C06 controls | `scripts/check-release-pack.py` through `nix/checks.nix`: installed execution and representative copied-byte hydration. |
| C08/C11, X07 | Package architecture/manifest checks, `test_release_pack_cache.py`, `test_release_packaging.py`, hook aggregation/failure controls. |
| K06 | `worker_test.go`: opt-in consumption and inspection of real hydrated final descriptors. Skipped without configured real inputs. |

Full K06 needs actual artifact storage on a distinct consumer worker, successful
merged owner deliveries and separate human confirmations for each selected
`go.build`/`apple.sign` revision, selector and hosts. The coordinator manages
those gates. After they are satisfied, supply that worker's real request JSON
through `C2OPS_RELEASE_PACK_INTEGRATION_REQUEST` and the candidate executable
through `C2OPS_RELEASE_PACK_TEST_BIN`, then run
`go test -run '^TestK06HydratedFinalArtifacts$' -count=1 -v` on the consumer.
Record actual worker identities, selected upstream packages and artifact refs
with the run. The input flag is a test input, not a publication approval service.
Copied fixtures and a supplied request alone cannot prove distinct workers or
retention; C05/C06/C10 require real storage and its configured retention interval.
These live checks and unavailable real hosts remain unverified until run.
