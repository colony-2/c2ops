# OpenTofu native runtime fixture

This separate test module uses the clean c2j/JobDB versions pinned in go.mod. It
keeps runtime dependencies out of the production OpenTofu module and Nix closure.
`python3 scripts/check-opentofu.py` runs the operation tests and scanner controls.
The operation's native test builds this fixture and supplies an isolated packaged
candidate, a real controlled Google boundary and disposable source/backend paths.

The fixture owns SQLite and its blob directory, submits with an injected synthetic
secret provider, and uses normal renewable leases and execution guards. Its Git
transport maps one canonical fixture repository to the test's local source; it
never fetches a live repository or changes production transport or endpoints.

Completed replay uses ReplayJobRun and a read-only recipe worker. It verifies
scope traversal, identical failed deployment data/recovery, unchanged complete
chapters, and no additional provider reads, helper calls or OpenTofu commands.

Cancellation waits for a real Google-client request during plan, captures the host
process tree, and calls backend CancelJob from an independent context. It joins the
recipe runner and extension task, verifies ErrExecutionLeaseLost, process exit,
private invocation removal, cancelled status/no final output and unchanged stage
and task-dispatch ledgers. The test wrapper deliberately ignores interrupts and
starts a stubborn descendant. This tests actual revocation, not lease expiry or a
network partition.

The scanner reads complete committed chapters, metadata, decoded byte fields,
versioned execution records and every artifact through EOF with size checks. It
follows referenced producer jobs, opens archive members and Git pack objects, and
scans ciphertext for accidental plaintext without decrypting it. Offline recovery
decryption is a separate operation test. Resource limits fail the scan. Negative
controls persist deliberately private input/artifacts and require rejection.
Physical database freed pages, WAL remnants and orphan blobs are outside this
committed-history assertion. These fixtures establish no live Google acceptance.

The nested `emulator` module pins fake-gcs-server independently of these runtime
libraries and the production operation. The qualifier builds it explicitly;
local tests reject unexpected destination hosts and independently read objects.

The test-only live mode forwards an allowlisted literal secret to the original
parent helper using the original execution environment, before creating the
local runtime's helper binding. It never places credential values in config or
recipes. The embedded runtime supplies normal workflow control for typed-object
hydration. It exports safe result JSON and only the consumer's recovery.age
ciphertext, not its database or raw diagnostics. The local packaged recovery test
exercises this same adapter using a synthetic parent credential and emulator;
that result does not establish real Google qualification.
