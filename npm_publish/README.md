# Publish the npm tarball that passed installation

`npm.publish` prepares an existing committed product wrapper, checks its real
GitHub installer and executable, and publishes the same tarball bytes. I preserve
the wrapper's name, bin mapping, platforms, engines and installer. Only the version
and explicitly requested committed documentation change, in private scratch.

The operation directory is `npm_publish`; the Nix attribute and executable are
`npm-publish`. Select `nix:github:colony-2/c2ops/<confirmed-revision>#npm-publish`.
The package binds Python, Node/npm, Git, GNU tar/gzip and CA roots. The flake
advertises `x86_64-linux`, `aarch64-linux` and `aarch64-darwin`; runtime and cache
checks on each actual host remain necessary. Source invocation is
`python3 npm_publish/main.py < request.json`, with those tools on PATH. The local
manifest declares pinned Nix runtime dependencies for c2j to prepare. The packaged
operation runs from a read-only cwd.

## Request and result

[contract.json](contract.json) is the complete versioned JSON Schema, with full
synthetic request/result examples. Its root validates requests; `#/$defs/result`
validates stdout. `op.yaml` is the local source manifest, and `op.json` is generated
with `make manifests`. Both expose the same schemas. JSON input/output is bounded
to 128 KiB; bytes travel as artifacts.

Supply explicit, existing, canonical absolute `workspace`, `inbox`, `outbox`
directories that do not overlap. Hydrate `files` under the same slash-relative
names below inbox. They must be the four final
`NAME_VERSION_{Linux|Darwin}_{x86_64|arm64}.tar.gz` archives plus `checksums.txt`.
Every descriptor binds schema version 1, release ID, repository, source commit,
version, tag, size, lowercase SHA-256 and kind; archives also bind `goos`/`goarch`.
Checksums use `HASH  NAME` lines and describe exactly those four archives.

`intent` includes `schema_version:1`, `release_id`, canonical
`github.com/OWNER/REPO`, full `source_commit`, `target_branch`, SemVer `version`
without `v` or build metadata, `tag:"vVERSION"`, UTC RFC3339 `build_date`, and
`binary_name`. The package leaf and binary name must agree.

`release` contains `id` (positive integer or nonempty string), public `url`, `tag`,
resolved `commit`, `public:true`, `draft:false`, and the five asset descriptors
with canonical GitHub `download_url`s. I independently query the public repository,
release, peeled tag and asset list, then anonymously download and hash every asset.
A public flag or `target_commitish` alone does not authorize publication. Archive
entries must be safe regular files/directories, with the executable at the root.

The npm-specific inputs look like this (the full schema example also supplies
intent, files, release and roots):

```json
{
  "wrapper_path": "npm/product",
  "package_name": "@example/product",
  "documents": [
    {"source": "README.md", "destination": "README.md"},
    {"source": "LICENSE", "destination": "LICENSE"}
  ],
  "registry": "https://registry.npmjs.org/",
  "registry_token_file": "/bound/secrets/npm-token",
  "access": "public",
  "dist_tag": "latest",
  "check": {
    "bin": "product",
    "args": ["version"],
    "stdout": "product version 1.2.3\n",
    "timeout_seconds": 60
  },
  "resume": false
}
```

`registry` is an explicit HTTPS base ending in `/`, without userinfo, query or
fragment. The token file must be private (no group/other mode bits), canonical,
outside all three bound roots, and contain one native registry token. No Actions
OIDC fallback exists. The registry must be an authorized destination; the example
does not authorize publication. `access` defaults to `public`; `dist_tag` defaults
to `latest` and must start with a lowercase letter. A version check must contain
the allocated version and match stdout exactly. Arguments are literal argv.

Success is `{"output":{...}}`, with `intent`, one `npm_tarball` descriptor in
`files`, source-tree/wrapper/Node/npm `provenance`, `package:{name,version}`, the
tested `release`, `install_check:{status:"passed",goos,goarch,check}`, and
`registry:{url,integrity,observed_integrity,outcome}`. Integrity is SHA-512 SRI;
outcome is `published` or `reconciled`. Diagnostics go to stderr and failure is
nonzero without a success envelope. Raw tool output and API bodies are suppressed
because they can contain credentials.

## Prepare now and publish the retained artifact later

The optional `phase` extends the previously approved combined operation. Omit it
to retain the original token requirement, combined publication behavior and
result keys; omitted-phase results have no `phase` field. All explicit results
include `phase`. Existing names, v1 descriptors, authorization and
`install_check.status:"passed"` are preserved.

| Request | Token field | Retained input | Registry result |
| --- | --- | --- | --- |
| Phase omitted | Required | Existing resume rules | Verified published/reconciled, original shape |
| `phase:"prepare"` | Forbidden, even null/empty | None for fresh; original descriptor/bytes for resume | `url`, computed SHA-512 `integrity`, `outcome:"not_attempted"`; no `observed_integrity` |
| `phase:"publish"` | Required | Explicit `resume:true` and original `retained_tarball` | Verified matching `observed_integrity`, published/reconciled |

For fresh prepare, remove `registry_token_file` from the complete request above
and add `"phase":"prepare"`. It validates public GitHub assets, packs once,
retains the tarball and performs the real isolated offline installation and
version check. It never reads a registry token, observes a registry version or
publishes. Prepare still needs access to anonymous public GitHub assets.

Capture the successful prepare result and its original artifact. For resumed
prepare, add `"resume":true` and copy `output.files[0]` unchanged into
`retained_tarball`. For publish, use those same settings, set
`"phase":"publish"`, and provide the existing private native token file.
Hydrate all **six** original files into the new inbox: the five entries in
`files` and the separate `retained_tarball`. Every invocation validates
identity, name, path, size and SHA-256, checksums, committed wrapper/documents,
public assets and installation. Resumed calls calculate expected content in
memory; they never pack, rewrite package metadata or regenerate missing bytes.
Malformed phase combinations fail before any subprocess or service effect.

Run every publication task with caller-supplied `const:true`. Const prevents
task Git commits while allowing outbox artifacts, inbox reads and temporary
writes. Source remains in the checkout; projection, staging, packing, installation,
home/cache/authentication and other working files stay in temporary space outside
all bound roots. Keep tarballs in outbox for capture and hydrate them through
inbox on later tasks. A file written to outbox alone does not establish durable
capture; the coordinator must bind/export artifact references. Never store
pipeline outputs, caches or installation state in tracked, ignored or untracked
checkout paths.

Publication uses the existing exact-byte state machine: authoritative absence
before mutation, observation after ambiguous acceptance, conflict rejection and
at most two attempts with a fresh absence check before the sole retry.
Reconciliation verifies version bytes. It neither certifies nor repairs dist-tag
ownership, even when another version owns the requested tag.

## Committed source and installation

HEAD must match intent and tracked files must be clean. I project the entire
commit using Git tree/blob objects, verify its path set, object hashes and modes,
and include committed export-ignored content. Ignored/untracked input is excluded
and the caller's checkout is unchanged. Git commands preserve credential settings
and helper sockets while ignoring source-selection overrides and replacement refs.
Submodules, unresolved LFS and escaping symlinks fail explicitly.

The supported wrapper follows the existing c2j product convention: bin
`NAME -> bin/cli.js`, `postinstall: node scripts/postinstall.js`, Linux/Darwin,
x64/arm64 and a declared Node engine. `repository` must match intent. Its committed
version must equal intent or `0.0.0-development`. Package dependencies, workspaces,
publication overrides, symlinks and bundled installation state are rejected.
Supporting another wrapper convention requires an explicit compatibility change.
Documentation copies must come from that projection and cannot overwrite the
shim, installer or metadata.

For fresh calls I run `npm pack --ignore-scripts` exactly once, validate the packed contents
against prepared committed bytes and retain `npm/NAME-VERSION.tgz` before install.
The install uses an isolated npm directory/cache, runs postinstall, receives no
registry/Git credentials, and uses the unchanged product GitHub URL path. I compare
the installed `vendor/NAME` executable with the verified host archive, verify the
bin shim, and run your declared command. Other target binaries are checked for
asset completeness/hash but are not executed on this host. npm pack/publish
lifecycle scripts are disabled. Ambient npm config and `NODE_OPTIONS` are removed.

## Retries and hydration

Before mutation I read the exact registry package/version. Matching SHA-512 SRI
reconciles after a successful install check; conflicting bytes fail with “new
version required.” Missing strong integrity triggers an HTTPS tarball download
and byte comparison. Authentication/network errors are never version absence.
HTTP 200 responses must contain valid package identity and distribution metadata;
JSON null and malformed metadata stop both initial publication and timeout retries.

Only an authoritative 404 permits publication. I pass the retained candidate
filename to `npm publish`, never a directory, and rehash around installation and
publication. npm's automatic fetch retries are disabled. After success, conflict,
timeout or lost response I query again before deciding. A failed publish followed
by confirmed absence permits one further attempt of the same bytes, preceded by a
fresh read. Unknown state stops. Cancellation kills the active process group and
never retries. Network requests, subprocesses and the whole invocation are bounded
(30 seconds per request, up to 300 per install, 1200 total).

Scratch paths are canonicalized, so a symlinked `TMPDIR` is supported when its
resolved directory lies outside workspace, inbox and outbox. A temporary directory
inside any bound root is rejected before consuming artifacts or contacting services.

On retry set `resume:true` and supply `retained_tarball` as the original v1
descriptor, with those exact bytes hydrated below inbox. I validate its identity,
size/hash, safe tar entries and content against the same committed preparation,
rerun installation and publish without repacking. Missing bytes fail. A first run
cannot detect an earlier invocation by itself: you must durably preserve intent
and the retained artifact, including artifacts from failed tasks. The stable
`npm/NAME-VERSION.tgz` path is available even when installation/publication fails.

Bind node `artifacts` to producer artifact references and export
`outputs.artifact_refs` across recipes/jobs. A descriptor name is an artifact key;
result JSON and producer-local paths do not hydrate files. Never bind a token
into outbox or retained artifact collections.

## Verification and remaining acceptance

```sh
uv run --with jsonschema==4.26.0 python -m unittest discover -s scripts -p 'test_npm_publish*.py' -v
nix build --no-link --no-write-lock-file .#npm-publish .#checks.aarch64-linux.npm-publish .#checks.aarch64-linux.npm-fixtures
```

Schema tests require the declared `jsonschema==4.26.0` test dependency; the
production operation uses only the standard library. The native Nix fixture
environment includes jsonschema. Phase tests additionally transfer six saved
artifacts into a fresh workspace/process, forbid packing/staging on resume,
and verify unchanged HEAD, index, complete checkout contents/modes/symlinks
(including ignored/untracked sentinels), command working directories and
home/cache/tmp/install paths across success, failure, resume and reconciliation.
Fixture transfer proves local operation behavior; actual worker artifact capture
and required-host package publication remain separate checks.

The fixture uses the unchanged c2j wrapper at
`c412b478897ee0637f879b3c7b13b1afbdf092a6`, a test-only Node transport shim and
separate loopback GitHub/registry servers. It checks download paths, checksums,
executable behavior, exact published attachment bytes, conflicting registries,
lost responses and resumes. The shipped op exposes no fixture endpoint switch.
The verification hook prepares tar/gzip and runs a dedicated `npm_publish` task
(`verify.py check npm.publish`) against this candidate's operation and local
fixtures. It retains `npm.publish.log` and `npm.publish.json`, exports both as
hook artifacts, and includes task failure or missing/tampered evidence in the
aggregate. The broader `test-tools` suite also covers these tests in its declared schema-test environment. The runner
removes `C2OPS_NPM_LIVE_REQUEST` so normal verification cannot publish externally;
live N08 remains skipped until its separate authorization/publication gates.

| Requirements | Checks |
| --- | --- |
| N01 | Scratch preparation, unchanged caller bytes, preserved metadata/shim/installer, committed documentation |
| N02 | Version/repository/release/commit/privacy/completeness/hash negatives, healthy publication control |
| N03 | Failed install, changed checksum download, wrong version output; retained bytes and no publication |
| N04 | One pack, exact registry attachment, rehydrated resume without pack, tampered resume rejection |
| N05 | Explicit private native token; Actions variables do not authorize npm |
| N06 | Matching/conflicting SRI, tarball fallback, malformed metadata, unavailable registry |
| N07 | Lost response, timeout, authoritative rereads, null-response rejection, bounded retry, cancellation/unknown state |
| N08 | Separate live test, skipped unless an explicitly authorized request is supplied |
| C01/C02/C04/C06/C07/C09 | Closed input contract, process diagnostics, malformed descriptors/path/symlink/hash/size negatives, JSON bounds, secret isolation |
| C03/C08 | Native Nix package/manifest check and fixtures loaded from installed package, explicit roots/read-only cwd |
| C05/C10/C11 | Actual worker hydration/retention and cache-only installation require deployment checks; fixture/local builds do not certify them |

Live N08 needs the coordinator's confirmation of the successful merged GitHub-op
package revision/selector/hosts, authorized disposable public assets and a
disposable registry/package token. Only then provide a request file through
`C2OPS_NPM_LIVE_REQUEST` for the separate live test. Producer checks do not wait for
downstream adoption. Consumers of this new Nix package must wait for this job's
successful merge, then human confirmation of its exact revision/selector/hosts.
No production release, merge, push or cutover is performed by this implementation.
