# Go builder tests and acceptance boundaries

Run unit and process-policy checks with:

```sh
cd go_build
go test -mod=readonly -count=1 ./...
C2OPS_TEST_GO_BUILD_REAL=1 go test -mod=readonly -count=1 -timeout=12m -v ./...
```

The real suite builds the operation executable unless
`C2OPS_GO_BUILD_LAUNCHER=/absolute/package/bin/go-build` selects an already built
candidate package. This is a test-harness setting; the production executable has
no alternate compiler or source-policy switch. With a package launcher, process
checks run from the actual read-only Nix package cwd. Without it, they use an
independent read-only directory. Fixture repositories are disposable local Git
repositories with a recorded GitHub origin; they perform no remote publication.

```sh
nix build --no-link --print-out-paths path:/absolute/c2ops#go-build
C2OPS_TEST_GO_BUILD_REAL=1 \
C2OPS_GO_BUILD_LAUNCHER=/nix/store/OUTPUT/bin/go-build \
  go test -mod=readonly -count=1 -timeout=12m -v ./...
```

The native verify hook calls `scripts/verify.py check go-build`. It runs units,
builds only this candidate op locally, checks installed/evaluated manifest and
compiled executable parity, and runs the real suite through the package. SDK
installation inside that op remains prebuilt-only. Failures in either suite are
mandatory failures in the aggregate. The hook leaves existing verification Go
declarations unchanged.

| Requirement | Implemented checks |
| --- | --- |
| B01 | Selected module go directive and default toolchain discovery. |
| B02 | Concrete go.mod/go.work toolchain precedence. |
| B03 | Explicit override; workspace and every member minimum still checked. |
| B04 | Minor 1.26 resolves to newest mapped 1.26.8; exact pinned identity reported. |
| B05 | Unsupported exact 1.26.2 rejected without substitution; no archive path exists. |
| B06 | Too-old module/workspace/member minimums, malformed and missing files/selectors fail. |
| B07 | Real mapped absolute compiler runs despite a conflicting ambient `go`. |
| B08 | Wrong observed version blocks compilation, reports requested/observed versions. |
| B09 | Poisoned GOENV/GOROOT/GOWORK/GOFLAGS/GOTOOLCHAIN/CGO settings; selected contained workspace executes. |
| B10 | Actual native executable prints exact metadata with spaces, quotes, shell metacharacters and dollar expansion syntax. |
| B11 | Test runner fails one target after a successful target; no final output directory is exposed. |
| B12 | One real op discovers 1.26.1, explicitly selects 1.26.1, and selects 1.26.8 through a minor override. |
| B13 | Both SDKs build all four targets; inspect ELF/Mach-O architectures, build-info SDK, descriptor hashes and native executable metadata. Foreign binaries are not executed. |
| B14 | Real prepared SDK reuse/offline acquisition, enforced offline argument, temporary roots. Cold retrieval from an absent SDK store remains a separate live check. |
| B15 | Cache-only CLI controls, unavailable-closure fixture failure, actual Nix `preferLocalBuild` derivation rejected before its marker-producing builder executes. This policy fixture is not a second SDK. |
| B16 | Real process-group timeout test and SIGINT to the actual operation; no successful output. |
| B17 | Scoped credential preservation unit test; opt-in fresh-cache private Git positive/denied fixture processes below. |
| B18 | Proxy settings preserved separately; opt-in authenticated/unauthorized proxy fixture processes below. |
| B19 | Source/index status unchanged on successful and failed real operations, including module files; projection/caches are private. |
| B20 | Wrong HEAD, tracked/staged changes, hidden index flags and mode changes; corrupted projection bytes/modes/path set. Real process rejects wrong HEAD/tracked edit beside healthy executable controls. |
| B21 | Live untracked .go injection changes control executable from committed to injected; release executable remains committed; ambient file survives. |
| B22 | Same executable control with ignored .go injection and clean ordinary Git status. |
| B23 | Actual go:embed control sees untracked and ignored assets; release sees only committed assets. Ambient-only required assets build in control and fail release. |
| B24 | Actual control executables consume escaping source symlink/replacement/workspace; operation rejects them. Contained committed replacement/workspace executes successfully. Absolute paths also rejected. |
| B25 | Actual embedded committed ignored/export-ignore assets survive; complete inventory matches recorded projection, unusual names and executable modes retained. |
| C01 | Required intent/root/entrypoint fields, unknown fields, duplicate JSON keys and schema versions rejected. |
| C02 | Real process JSON-only success, diagnostic stderr/nonzero failure, complete matching descriptors. |
| C03 | Packaged execution from package cwd using explicit disjoint workspace/inbox/outbox. |
| C04 | Traversal/overlapping roots, duplicate targets and escaping source paths rejected; this producer consumes no hydrated binary descriptors. |
| C05/C06 | Producer hashes/identities are exercised. Distinct-worker hydration, tamper rejection and mode restoration belong to consumer/runtime checks. |
| C07 | 128 KiB input/output bounds, including a staged oversized-descriptor fixture that cannot expose final outputs; actual four-binary plus inventory JSON stays below the limit, without binary bytes. |
| C08 | Catalog-generated Nix package contract checks installed/evaluated manifest parity. Real local package checks cover the running host; six SDK mapping definitions inspected without payload installation. |
| C09 | Raw subprocess diagnostics suppressed, no environment dumps; invalid-input/version diagnostics do not copy supplied secret strings. Live private-denial checks require fixtures. |
| C10 | No storage/retention acceptance from a local producer test; required runtime handoffs remain unverified here. |
| C11 | Manifest metadata is independent of SDK payloads; immutable SDK metadata inspection disables IFD/builders; actual preferLocalBuild fixture cannot build. Missing op-cache installation on fresh stores remains publication verification. |

`TestRealB20_B24AssemblySourceBoundary` additionally closes the reviewed assembler
escape: committed `.s` files can use `#include` to read an ignored absolute path
even from an intact projection. Direct, nested, escaped-path, macro-generated
and local-dependency variants execute real Go 1.26.1 control binaries returning
42 then 99 with identical commit/inventory hashes and clean ordinary Git status.
The actual release process must reject both values without descriptors or final
outputs. A healthy committed-header/SDK-header fixture builds all four targets
and executes natively. `TestRealB24DownloadedModuleAssembly` uses a disposable
file-backed module proxy with committed go.sum: fresh-cache contained dependency
assembly executes, while its external include controls execute and release fails.
This is module/projection evidence, not private Git/proxy service acceptance.
Unit checks cover dependency graph containment, nested headers, SDK/generated
header shadowing, conditional branches, string escapes, macro directives and
header symlinks. The verify hook retains verbose executable-control results and
explicit prerequisite skips in its exported log.

Private fixtures are JSON operation inputs pointing to already committed,
authorized test repositories with go.mod/go.sum, a buildable main package and a
freshly fetchable private dependency. The harness replaces inbox/outbox with
fresh directories and uses a native target. Configure all paths explicitly:

- `C2OPS_GO_BUILD_PRIVATE_INPUT`: authorized private Git dependency fixture.
- `C2OPS_GO_BUILD_DENIED_INPUT`: dependency fixture the supplied profile cannot read.
- `C2OPS_GO_BUILD_PROXY_INPUT`: fixture using independently supplied HTTP proxy authorization.
- `C2OPS_GO_BUILD_UNAUTHORIZED_PROXY`: the same proxy endpoint without authorization.

Run with the real suite flag and existing scoped RO Git/helper configuration.
Use only disposable read-only fixtures authorized for these tests. Private tests
skip with an explicit unverified reason when inputs are absent; a fake runner
never counts as private-service acceptance. Their source must include dependency
checksums; readonly mode does not create a missing go.sum for a fixture.

The supported package systems are x86_64-linux, aarch64-linux and aarch64-darwin.
A cross-compiled Mach-O is evidence of output format, not a macOS host run.
Cold cached-closure retrieval, missing-cache behavior on each advertised host,
foreign native binary execution, private Git/proxy access and actual worker
storage/retention all need their real infrastructure. Cache availability or a
local package build does not establish publication. No signing, deployment,
production publication or downstream consumer acceptance is performed here.
