# Authorized disposable GitHub checks

These executable Go tests are **unverified live acceptance** until the coordinator
provides an authorized disposable public repository, a scoped credential and the
required upstream publication confirmation. They are skipped by default even
when `GITHUB_TOKEN` exists. Production destinations/artifacts are not authorized.
The managed verify runner removes the live gate from its test environment.

Prepare a fresh v1 input JSON using the README contract, explicit existing
workspace/inbox/outbox directories, a real source commit/tree in the disposable
repository, and all final hydrated assets/checksums. Each test requires a new,
absent tag/release; use a distinct intent for G11 and G12. Versions must begin
`0.0.0-c2ops-disposable.` (for example `0.0.0-c2ops-disposable.20261118.1`), with the
corresponding `v` tag. Every descriptor and provenance identity must match.
Do not manufacture pack output as evidence of upstream consumer acceptance.

Bind a repository-scoped authorized `GITHUB_TOKEN` through the existing secret
mechanism. Do not put it in the input file, command arguments, logs or artifacts.
After authorization, set these nonsecret test settings in the process environment:

| Setting | Required value |
| --- | --- |
| `C2OPS_GITHUB_LIVE` | Exactly `authorized-disposable-publication`; explicitly enables external effects. |
| `C2OPS_GITHUB_LIVE_INPUT` | Absolute path of the prepared input JSON. |
| `C2OPS_GITHUB_LIVE_REPOSITORY` | Exact authorized canonical repository, equal to intent. |
| `C2OPS_GITHUB_LIVE_TAG` | Exact authorized disposable tag, equal to intent. |
| `C2OPS_GITHUB_LIVE_PACK_CONFIRMATION` | For G11 only: coordinator record of successful merged pack owner job, exact revision/selector/hosts, and human publication confirmation. |

The last setting records the invocation prerequisite; it does not verify the
human statement or create a runtime approval service. A local package build or
cache availability cannot provide that confirmation. The coordinator must supply
it before G11 consumes actual `release.pack` output. G12 can use local fixtures
with its own separately authorized destination/tag; it needs no upstream package.

From `github_release/`, run one test at a time with its distinct prepared intent:

```sh
go test -count=1 -v -timeout=12m -run '^TestG11LiveDisposablePublication$' .
go test -count=1 -v -timeout=12m -run '^TestG12LiveInterruptedPublicationRetry$' .
```

G11 invokes the real release implementation, verifies retained bytes, checks the
source tag and independently downloads every returned anonymous public URL to
verify the descriptor hash, including checksum content. It uses the confirmed
pack output supplied in the inbox; it does not install that package implicitly.

G12 blocks the publication PATCH in a test-only HTTP transport after real draft
uploads, verifies that complete draft and downloaded assets remain, then removes
the interruption and retries. It asserts the same release/asset IDs survive, only
the publication PATCH occurs on retry, and another published retry has no writes.
A same-size conflicting digest probe exercises real published-byte verification
without replacing assets; all original public downloads are verified again.
Fake-server tests separately cover denied writes and ambiguous post-write
responses, with no credential requirements.

Each invocation keeps its own fresh local outbox/evidence. The harness does not
print credential-bearing transport errors, alter tag targets, delete releases,
or perform automatic remote cleanup. Tags, drafts and public disposable assets
remain for inspection, including on later failure. Cleanup requires separate
owner action. A failed/unrun test remains failed/unverified; skipped tests do not
satisfy G11 or live G12. npm pack-consumer/publication checks remain downstream.
