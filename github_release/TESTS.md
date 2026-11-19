# GitHub release acceptance mapping

`go test -race -count=1 -cover ./...` runs byte-backed fixtures and fake HTTP
services without external credentials. These fixtures exercise the real client,
JSON process implementation and state reconciliation, with explicit positive
controls. They do not establish live GitHub permissions or publication.

| Requirement | Executed coverage |
| --- | --- |
| G01 | `TestG01DestinationPreflight`: nonexistent commit, wrong canonical repository, commit tree and provenance identity; all fail before mutations. |
| G02 | `TestG02MissingTagAndCompletePublicResult`: create explicit commit ref, complete draft/upload/public verification; exact retained file hashes. |
| G03 | `TestG03MatchingTagsReused`: matching lightweight and annotated refs remain unchanged. |
| G04 | `TestG04ConflictingAndProtectedTags`: different commit, protected denial, noncommit and cyclic tag; no release publication, force or deletion. |
| G05 | `TestG05TagRaceAndAmbiguousWrites`: concurrent 422 and disconnected response after each write; reread/reconcile without duplicate effects. |
| G06 | `TestG06PartialMatchingDraft`: one matching uploaded archive resumes with only the missing checksum upload. |
| G07 | `TestG07MatchingPublishedReconciles`: legacy unmarked public release, paginated release enumeration, matching downloads, zero writes. |
| G08 | `TestG08SameSizeDifferentPublishedBytes`: flip a byte without changing asset name/size; reject without edits. |
| G09 | `TestG09G10IncompleteUploadAndEvidence`: denied/missing/truncated uploads prevent publication; inaccessible public bytes prevent output after publish. |
| G10 | Same test retains observed tag/release/evidence and never calls DELETE. |
| G11 | `TestG11LiveDisposablePublication` is executable but **live acceptance unverified**. Requires authorized disposable GitHub repository/token and human-confirmed `release.pack` publication; independently verifies public URLs/hashes and retained bytes. |
| G12 | `TestG12FixtureRetryAfterInterruptedPublication` covers retained draft/assets and retry locally. `TestG12LiveInterruptedPublicationRetry` implements real retained-draft retry, unchanged IDs, read-only public reconciliation and conflicting hash rejection. **Live acceptance unverified** without disposable authorization. |
| G13 | `TestG13CredentialsAndPermissionFailures`: missing token makes no requests; denied draft/upload/publication returns no successful output. |

`TestDraftConflictsAndIncompletePublished` additionally rejects unmarked or
conflicting drafts, duplicate/unexpected names, different draft bytes, incomplete
public sets and paginated unexpected assets. `TestRedirectCredentialScope`
checks credential stripping on actual redirects and rejects insecure URLs.
`TestReadRetriesAreBoundedAndAPIRedirectsRejected` tests transient read limits,
denied reads and JSON redirect rejection. `TestStagingResistsLaterInboxMutation`
proves that uploads use the validated private copies.

| Shared requirement | Executed coverage and limits |
| --- | --- |
| C01 | `TestC01C04C06ContractFailuresBeforeEffects` and `TestC01C07AmbiguousAndOversizedJSON`: independent malformed identity/type/schema cases and healthy positive control; case aliases and missing/null sizes are explicitly rejected. |
| C02 | `TestC02C03SeparateProcessExplicitRootsReadOnlyCWD`: success JSON-only stdout, diagnostic stderr/nonzero failure and no successful failure envelope. Installed binary rejects empty input without network/tool lookup. |
| C03 | `TestC03InstalledPackageFromImmutableCWD` executes the actual aarch64-linux Nix wrapper from immutable package cwd with empty PATH and controlled TLS/CONNECT fixtures. Complete success and invalid-input failure pass. Other hosts remain unverified. |
| C04 | Duplicate/basename collision, mixed identities, traversal, normalization, escaping file/directory/root symlinks fail before any requests. |
| C05 | Outbox retention and staging are tested; **distinct-worker runtime hydration/mode transport unverified**. This consumer uploads archives, so it does not execute or chmod binaries. |
| C06 | Missing, truncated, tampered and checksum-mismatched hydrated fixture files fail before requests. **Actual cross-worker runtime path unverified**. |
| C07 | Realistic descriptor-only output remains bounded; oversized request/result and deep/ambiguous JSON fail before effects. Result bound includes duplicate asset descriptors and maximum ID/URL sizes. |
| C08 | Native aarch64-linux package build, evaluated/installed manifest parity and compiled-payload check pass locally. **Other hosts and cache-only install unverified**. |
| C09 | Secret-bearing API errors never enter stdout/stderr or retained diagnostics; redirects and anonymous downloads receive no credential. |
| C10 | **Unverified.** Requires configured artifact storage/workers and actual retention interval. |
| C11 | Metadata evaluates with import-from-derivation disabled. **Cold cache-only installation behavior unverified**; no source fallback is added to invocation. |

`scripts/test_verify.py::test_github_release_dispatch_and_hook_evidence` checks
that the hook dispatches this race suite, exports its log/status, and preserves a
suite failure. `test_installed_github_success_check_failure_propagates` checks
that the mandatory installed test cannot fail silently. The existing verification regressions check aggregate failure,
missing/malformed evidence, cancellation and source preservation.

Nix `checks.SYSTEM.github-release-source` runs the local source suite with CGO
disabled. `checks.SYSTEM.github-release` checks the installed manifest, compiled
payload, complete TLS fixture success from immutable package cwd with empty PATH,
and nonzero missing-input behavior. The managed `nix-process` task runs the same
installed test and preserves failures. The op's own package
has no Go module dependencies or runtime compiler; the wrapper supplies HTTPS
trust roots. Tests do not publish the package or assert cache availability.

Review regressions additionally cover exact nested JSON provenance numbers above
2^53, a huge exponent and a long decimal in both successful output and draft
identity (`TestC01ExactProvenanceNumbers`); invalid Git tag syntax before any
requests with a valid prerelease/build-tag control (`TestC01InvalidTagBeforeRequests`);
and FIFO rejection in a subprocess with a five-second deadline
(`TestC04FIFOAssetDoesNotBlock`).

[Live test instructions](LIVE_TESTS.md) document the executable authorization
gates. `TestLiveAuthorizationGate` proves ambient credentials, vague opt-in,
missing settings and missing upstream confirmation cannot enable live checks.
Default runs explicitly skip G11/live G12; those skips are not acceptance passes.
