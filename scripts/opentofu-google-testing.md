# OpenTofu integration qualification

The managed build hook keeps local command qualification (`opentofu`) separate
from fresh basic Google credential qualification (`opentofu_credentials`). Local
fixtures cannot mark the latter passed. No production API, backend, dependency
pin or endpoint override is added.

The local qualifier builds the pinned fake-gcs-server executable from its nested
module, routes only allowed Google hostnames to a local transport, then runs the
actual packaged command, provider and native c2j consumers. It independently checks
marker bytes, generations and persisted resource state. State-write rejection is
a local transport fault after the marker effect; emergency state is retrieved and
decrypted with a disposable test identity. No Google account is used.

## Operator setup

An operator must authorize a new disposable project, billing/parent/location,
applicable deployed JobDB administration and public age recipients. Keep the
private decryption identity with the operator. This script does not register
secrets, install tools, enable development administration or run live verification.
The only supported registration instructions are for an already authorized
`development_admin_provider` route whose installed `jobdb secrets` CLI supports
`--tenant`, `--url` and `--value-file`. External secret providers require their
owner's separate registration route; do not enable a development store instead.

Example (replace each uppercase value, including the full candidate SHA):

```sh
umask 077
python3 scripts/setup-opentofu-google-test.py setup \
  --project NEW_DISPOSABLE_PROJECT \
  --private-dir /private/operator/opentofu-test \
  --folder AUTHORIZED_FOLDER --billing-account AUTHORIZED_BILLING \
  --location US \
  --tenant JOBDB_TENANT --admin-url AUTHORIZED_ADMIN_URL \
  --admin-provider development_admin_provider --confirm-development-admin \
  --hook-jobdb PUBLIC_JOBDB_URI --hook-cell github.com/colony-2/c2ops \
  --hook-candidate FULL_CANDIDATE_SHA \
  --token-secret EXACT_TOKEN_NAME --key-secret EXACT_KEY_NAME \
  --age-recipient OPERATOR_PUBLIC_AGE_RECIPIENT --confirm-denial-policy
```

Use `--organization` instead of `--folder` when applicable; omit both for an
unparented authorized project. The private directory's parent must exist. The
directory must be empty or contain this script's ownership journal and files,
outside the repository and inbox/outbox, with mode 0700 and no symlink ancestors.
Credential JSON and the journal use atomic mode-0600 writes. Do not copy private
files into job history or review material. Administrative subprocess output is
captured and discarded on failure; errors expose fixed categories only.

Setup refuses an existing unowned project, journals each confirmed cloud effect,
creates positive/negative buckets with uniform access and public access prevention,
and limits source/target accounts to four positive-bucket object permissions and
source-to-target token minting. It creates an operator-confirmed existing negative
sentinel and rejects unresolved inherited, public, group or conditional policy.
This conservative policy check may refuse projects that need separate operator
inspection. Key issuance refusal stops setup and leaves cleanup records; it does
not fall back to token-only acceptance. Token expiry comes from Google's response
and must cover the configured invocation timeout plus seven minutes.

Run the printed, shell-quoted registration commands privately. Then apply the
printed leaf permission patch with the exact literal names and authorize them at
every enclosing callsite and `perms.limit` ceiling. The script also prints `c2j
hooks perms add` for the verify hook, scoped to the supplied cell and full candidate
commit, and its removal command. That administrative source trust is required by
`cell://` hooks; repository declarations cannot grant it. Inspect existing policy
before applying additive grants. A changed candidate needs renewed explicit trust.
The hook JobDB URI is public routing data without embedded credentials; provide
operator administration through the normal authorized environment.

Supply only `profile.json`'s public contents at
`settings.variables.opentofu_google`. The profile contains identifiers, literal
secret names, disjoint owned prefixes, timeout and public recipients. Unknown
fields, missing recipients and overlapping prefixes fail before resolution. The
hook defaults to only the existing `gitvend_base_token` grant for private repository
preparation and an unconfigured profile. Retain that grant in the leaf, enclosing
ceilings and existing administrative hook policy when adding Google names. Cleanup
removes only Google grants. Pending performs no Google credential lookup or traffic;
the worker still needs its Git credential before the command can start.
The setup script rejects either Google secret name if it equals `gitvend_base_token`.
A configured missing/denied credential or
invalid result fails verification instead of skipping.

Refresh immediately before a fresh verification using the same arguments and
`token-refresh` instead of `setup`, then run its printed token registration command.
Refresh changes no cloud resource or key. The live driver resolves the selected
credential once for its inspector, and each fresh production operation resolves
its exact name once through the parent helper. The inspector uses that credential,
never operator/ADC fallback. It requires authoritative token identity and HTTP 403
for the known ungranted object; 404, timeout or authentication failure cannot pass.
For each credential path it runs apply and repeat in distinct native runtimes,
checks independent marker/state reads and unchanged marker generation, and deletes
only objects in a namespace it first proved empty. Existing objects stop the case.

The production package identity, candidate/base, canonical public profile digest,
three case results, resolution counts and public log digest bind successful
receipts. Live subprocess output is never copied into shared logs. A failed native
result retains ciphertext before the caller rejects the deployment. Only safe
result JSON and encrypted recovery leave the temporary runtime; no raw history or
private diagnostic dump is exported. The hook bounds the whole driver at 900
seconds; individual invocation budgets come from the public profile. Completed
replay cannot establish fresh live qualification.

## Cleanup and release

Run the same operator command with `cleanup`. It attempts every recorded owned
resource in reverse order, reports incomplete actions and keeps remaining journal
entries for retry. Confirmed missing resources count as already removed. Review
and run printed secret-removal and hook-grant-removal commands. Deleting keys does
not revoke already minted bearer tokens; the recorded authoritative expiry remains
relevant. Add `--remove-private-files` only when cleanup succeeds and local key/token
files may be removed. Cloud administration and cleanup have not run in this job.

Publication follows the managed merge and human confirmation, using existing
three-platform Actions/cache/retention/promotion evidence and actual full-commit
selector invocation described in [NIX_PACKAGES.md](../NIX_PACKAGES.md). Neither a
local store path, pending live check nor source merge establishes publication.
