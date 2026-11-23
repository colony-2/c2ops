# Deploy an exact configuration to GCS

`opentofu-up` plans or applies a committed Google configuration against an existing
GCS bucket/prefix. It runs OpenTofu 1.13.1 and qualifies Google 7.10.0. The operation
owns no infrastructure, IAM, credential issuance, enrollment or deployment hook.
The manifests are [op.yaml](op.yaml) and [op.json](op.json); both include the full
input and result schemas. Publication follows [the repository lifecycle](../NIX_PACKAGES.md).
An immutable selector is available only after all three native platforms complete
signed empty-store substitution, retention and promotion. A local store path is
not a release. Do not activate this operation without separate live Google acceptance.

## Initial consumer: OAuth access token

Delegate the literal name at the consuming node, within every enclosing ceiling:

```yaml
perms:
  jobdb:
    secrets:
      resolve: [google_deploy_token]
```

`google_auth.secret_name` is ordinary public data. It is deliberately not an
`x-secret` value or an expression that resolves a credential into recipe history.
The operation calls exactly `c2j secret get google_deploy_token --format=raw` once.
An input name grants no authority. Missing, denied, empty and malformed credentials
fail before init; there is no ambient authentication fallback.

Store this JSON as the delegated secret, outside configuration and job inputs:

```json
{"access_token":"<freshly issued token>","expires_at":"2026-12-01T12:00:00Z"}
```

The issuer must supply a truthful UTC RFC 3339 expiry. Before init, validity must
cover the configured total timeout plus seven minutes. Equality passes. The same
resolved token is checked before apply against the remaining budget plus seven
minutes. The operation does not refresh static tokens or impersonate from them.
Unknown expiry, extra fields and token-mode identity/impersonation fields fail.

Example public request (replace the revision, source path, backend and recipient):

```json
{
  "worktree_path":"/managed/configuration",
  "revision":"0123456789abcdef0123456789abcdef01234567",
  "root":"deploy",
  "mode":"up",
  "backend":{"bucket":"existing-state-bucket","prefix":"deploy/example"},
  "google_auth":{"mode":"oauth_access_token","secret_name":"google_deploy_token"},
  "variables":{"project_id":"example-project","region":"us-central1"},
  "timeout":"30m",
  "lock_timeout":"2m",
  "recovery":{"age_recipients":["<operator-owned-public-age-recipient>"]}
}
```

Select `nix:github:colony-2/c2ops/<verified-commit>#opentofu-up` after publication;
the placeholder is not a usable selector. `worktree_path` defaults through the
manifest to the managed worktree. `mode` is explicitly `plan` or `up`. Public
variables are private-file JSON, never shell arguments, and must contain no secrets.
`up` requires native age X25519 public recipients. There is no uploaded-plan input.

## Refreshable service-account key

Both credential modes remain supported. Store a normal Google `service_account`
JSON key as another named secret. Supply:

```json
{
  "mode":"service_account_json",
  "secret_name":"google_deploy_key",
  "expected_service_account":"source@example-project.iam.gserviceaccount.com",
  "impersonate_service_account":"target@example-project.iam.gserviceaccount.com"
}
```

Omit `impersonate_service_account` to use the source directly. The parsed source
email must equal `expected_service_account`; the operation validates the RSA key
and Google's token endpoint. It rejects user refresh, executable and external
account credentials. One 0600 ADC file and the same optional target serve both
clients. Private HOME/data/tmp, bound Git/OpenTofu/trust paths and a deliberate
environment remove inherited Google tokens, ADC discovery, proxies, CLI arguments,
logging, endpoint overrides, shared plugin caches and c2j authority. Trusted modules
and providers remain trusted code; these controls do not establish live IAM grants.

## Source, backend and execution

The full lowercase 40- or 64-digit Git revision must match clean HEAD and the
repository object format. The normalized relative root and all tracked symlinks
must stay in the repository. Submodules and special files are refused. Execution
uses immutable Git blobs in a fresh 0700 directory outside the checkout and outbox,
preserving relative local modules. Checkout contents are compared with the blobs,
including files marked assume-unchanged. Untracked/ignored configuration, variables,
state, data/workspaces, CLI configuration and override files in selected module
roots are refused. Unrelated configurations may have their own backends.

The supported configuration files are `.tf` and `.tf.json`. The selected root and
every loaded module refuse `.tofu`, `.tofu.json`, `.tofuvars` and `.tofuvars.json`
files, including ignored/untracked files and overrides. This prevents OpenTofu's
filename precedence from replacing an inspected configuration before credentials
are resolved.

HCL and JSON parsing require one partial GCS backend in the selected root, containing
only absent or literal matching bucket/prefix. No backend fallback, migration or
credential/endpoint override is accepted, including block types generated by
`dynamic` labels with empty or unknown expansions. Other dynamic block types remain
supported; ordinary HCL resource/output labels are not generated block types.
Local modules must stay in the copy;
remote modules must use a registry address (`namespace/name/provider` or
`registry.host/namespace/name/provider`) with an exact `X.Y.Z` version, or explicit
`git::https://host/repository.git[//subdirectory]?ref=<full-commit>` with no other
query options. HTTP archives, source discovery, shorthand Git addresses, mutable
refs and embedded URL credentials are refused. A hash-shaped archive query does
not establish immutability. Downloaded modules are inspected before validation.
Commit compatible provider locks in advance. Init uses `-lockfile=readonly`; lock
bytes are compared after every stage, including failure. No upgrade or checksum
rewrite occurs. [The fixture lock](testdata/.terraform.lock.hcl) records the qualified
OpenTofu Google release's ZIP and unpacked hashes for all three targets. HashiCorp
registry locks can require migration by OpenTofu and are rejected when incompatible.

The only lifecycle is version check, init, validate, plan, and (changed `up` only)
apply of this invocation's private saved plan. Preview discards its plan; unchanged
preview/up returns `no_changes`. A stale plan, contention or failure stops without
retry, replan, unlock, rollback, destroy or state push. Plans and private files are
removed on every handled exit.

Total timeout defaults to 30 minutes, at most 60; lock wait defaults to two minutes
and cannot exceed total time. Positive integer seconds/minutes are accepted. Init,
validate and plan ceilings are 5/2/10 minutes, capped by remaining total time; apply
uses the remainder. Process-group interrupt, force termination and cleanup share
one additional window of at most two minutes. An enclosing runtime deadline wins.
After interrupting a tool group, the operation waits at most 500 ms before force
termination. This leaves cleanup time within c2j's two-second extension termination
grace. A cancelled invocation can still lose recovery before result acceptance.
Tool stdout is bounded to 8 MiB in private memory and excess is drained; stderr is
drained without publication. Input/secret reads are limited to 1 MiB, tracked source
to 256 MiB and emergency candidates to 128 MiB. These limits fail safely.

## Results and a caller that retains failure

Process exit zero is transport success. Deployment success requires literal
`ok:true`, a mode-appropriate outcome and matching revision/root/backend. Success
outcomes are `planned` (changed preview), `applied` or `no_changes`. Failed/cancelled
results have `ok:false`, a fixed stage/category, nullable exit/change status and
elapsed milliseconds. No provider text, outputs, resource addresses, plans, state,
credential, account or worker path enters the operation-owned result.

Malformed requests and source/backend/lock refusals exit nonzero with a fixed safe
diagnostic. Once source identity is verified, handled credential, command, deadline,
preflight, recovery and cleanup failures can return process zero with `ok:false`.
This permits c2j to seal an optional `c2ops.opentofu.recovery/v1` object while keeping
deployment failure visible. The output schema deliberately permits boolean false.

The [minimal caller](examples/retain_result.py) is exercised through native c2j:
first retain the safe result and **complete unchanged** reference, retrieve ciphertext
through typed-object hydration, then assert success in a separate single-attempt
step. It validates mode/outcome and requested source/backend. Native test recipes
also retain those artifacts with scope observations before their final assertion
fails. This illustrates the contract; infra owns its actual hook and writer ordering.
A reference forwarded elsewhere does not create a new retention copy. Keep producer
history for as long as recovery may be needed. Completed task replay is not a fresh
deployment or reconciliation; start a fresh invocation for new resolution/planning.

## Private recovery

Before apply, the operation encrypts a disposable probe and writes/removes ciphertext
under the actual object outbox. After failed apply or cooperative cancellation it
opens only known regular state candidates without following links: `errored.tfstate`,
`terraform.tfstate` and `terraform.tfstate.backup` in the private root. Available files
become one age-encrypted TAR with `index.json` and `state/<original-name>` entries.
Only ciphertext is exported. Public metadata records `age-tar/v1`, revision/root,
backend and originating stage; it includes no state hashes, lineage or serial.

Use normal typed-object hydration as the example does. For manual recovery, retain
the entire reference, locate its exact producer task with `c2j job log JOB --json`,
then list/download that task's referenced internal artifact. Keep these steps out of
shared job logs; use private operator storage:

```sh
umask 077
c2j job artifacts JOB --task 'EXACT-TASK-OCCURRENCE-ID' --json
c2j job artifact JOB --task 'EXACT-TASK-OCCURRENCE-ID' --name '__c2j_objects__/DIGEST.tar' --output sealed-object.tar
# Confirm type, size and SHA-256 against the COMPLETE reference before use.
tar -xOf sealed-object.tar files/ciphertext > recovery.age
age --decrypt --identity /private/operator.agekey --output recovery.tar recovery.age
mkdir recovered
tar -xf recovery.tar -C recovered
```

The native test verifies retrieval, offline decryption and the real emergency state
from an isolated GCS fault. Inspect lineage/serial privately, compare remote state
and actual resources, and decide a checked repair before another plan. Never automate
state push or unlock from the archive. No emergency file does not establish successful
remote persistence. SIGKILL, disk/worker loss, failed emergency writes, encryption,
sealing failure or an enclosing cancellation can prevent recovery. This is not durable
escrow, health/enrollment acceptance or a promise to undo resource effects.

## Qualification

`make test` runs unit/control-flow tests. `python3 scripts/check-opentofu.py` from the
repository root runs real OpenTofu/Google fixtures and native c2j recovery, requiring
Nix and c2j `b399a9afb40d`. CI builds that pinned test helper separately; it is never
bundled in the production package. The private test proxy/mirror is bound only to
an isolated candidate's fixed OpenTofu dependency. Production has no fixture input,
endpoint flag or environment bypass. The native test uses a case-local synthetic
secret and creates no real cloud resources. After an installed failed apply writes
ciphertext, test-only faults prove native c2j rejects an extra result field, an
inconsistent success status, and a ciphertext path replaced with a symlink. These
checks require the real local effect and encryption to complete, then verify no
later consumer, deployment retry or private producer directory survives. The path
refusal demonstrates c2j sealing rejection, not every object-storage failure.
CI runs the qualification on Linux
AMD64/ARM64 and Apple Silicon macOS before the existing publication stages.
The released macOS Google provider uses Keychain trust. On the disposable runner,
CI sets `C2OPS_TEST_MACOS_FIXTURE_TRUST=1` and checks certificate setup and
cleanup before the Nix build. During qualification, each proxy used by the real
tools temporarily trusts its own TLS certificate for the fixture hostnames using
passwordless sudo, then removes its trust setting and Keychain entry.

Qualification has two independent components. The local command component uses
pinned `fsouza/fake-gcs-server` in a separate test module, actual OpenTofu and the
Google provider. It checks preview/apply/unchanged repeat against marker and state
objects, conditional locks, and locally injected persistence rejection after the
marker effect. Native recovery, replay, cancellation and privacy checks remain.

The separate `opentofu_credentials` hook component runs basic real Google checks
only when an operator supplies a public test profile and delegates exact named
secrets. It checks token, direct key and key-source impersonation, authoritative
effective identity, marker/state operations, unchanged repeat and HTTP 403 for an
operator-confirmed existing ungranted sentinel. An unconfigured component is
explicitly pending and resolves no credentials or Google requests. Configured
invalid/denied inputs fail; fixtures never establish live success. Broader live
contention, outage, IAM and rotation matrices are outside this qualification.
See [operator setup and cleanup](../scripts/opentofu-google-testing.md).

The qualifier runs `TestNativePackagedRecovery` and its complement in separate
processes, each retaining the eight-minute Go test limit. The isolated native
fixture measured 370 seconds; combining it with authentication renewal and lock
checks exceeded the shared alarm. The complement includes future tests, and
partition/failure regressions require every current test exactly once and failure
from either process (or the scanner suite) to fail qualification.
