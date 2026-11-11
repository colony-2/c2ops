# Directly bound structured inference

`pydantic` sends a prompt to the host-selected OpenAI-compatible HTTPS root and
returns a JSON Schema-validated structured answer. Every call requires `api` with
`base_url` and secret string `api_key`, `default_provider: openai`, an unqualified
`default_model`, object `response_schema`, nonempty `prompt` and finite positive
`invocation_timeout` in seconds. Optional settings are `system_prompt`,
`temperature`, positive integer `max_tokens`, `top_p`, and `stop_sequences`.
`metadata` must be an object; its entries are discarded. No metadata is sent to
the provider or copied to results.

This is a breaking replacement. Ambient credentials, provider aliases, `api_keys`,
file context, sandboxing and external tools have been removed. Unknown fields
(including legacy fields with false/empty values) fail validation before requests.
There is no legacy mode or binding switch. The StructuredDict final-result tool
is the only tool sent to the model.

The URL is the exact API root: `https://host.example/custom/api` becomes
`https://host.example/custom/api/chat/completions`. Only trailing slashes are
normalized; no `/v1` is inferred. Roots require HTTPS, host and valid port.
Userinfo, query/fragment delimiters, backslashes, percent encoding, repeated path
separators and dot segments are rejected. Credentials and binding strings cannot
contain whitespace or controls. Content, metadata and provider output cannot
change bindings. URL syntax checks do not authorize a destination: cortex-assistant
must enforce its trusted endpoint/model policy.

Clients are request-scoped, with TLS verification, no environment proxies or
credential/project fallback, and no redirects (including same-origin redirects).
Every attempt checks the complete destination before attaching the credential.
Provider retries are limited to one; structured repair is limited to one. Local
Draft 2020-12 validation enforces nested constraints because pinned StructuredDict
alone advertises the schema without validating its nested values. Recursive or
unresolvable references, remote references, `$id`, `$dynamicRef` and `$recursiveRef`
are unsupported and rejected before clients are created. The local validator's
registry cannot retrieve remote schemas. Unsupported normalization fails safely.

A monotonic deadline covers validation and inference, transport backoff and
structured repair. Expiry cancels the request and closes both clients. The caller
should use a larger outer activity deadline (for example, 45s around a 30s
invocation) and budget separately for package resolution/startup and forced
process termination. Those outer bounds remain runtime responsibilities.

Success uses `{"output": {"response": "{...}", "model": "...", "usage": {...}}}`.
`response` is a JSON string; `model` is the selected model rather than untrusted
provider metadata. Usage contains prompt, completion and total token counts.
Provider metadata is always `{}`. Failure emits a stable `pydantic: validation`,
`authentication`, `provider`, `malformed-result` or `timeout` diagnostic and exits
1 with no success output. Raw exceptions and provider bodies are never printed.
SDK debug logging and agent instrumentation are disabled. Results containing the
credential are suppressed; a diagnostic containing it is also suppressed, even
when a short credential coincides with a category. The operation creates no
artifacts or telemetry and does not alter environment variables.

`op.yaml` is the source route: `uv run --script ./main.py`, with Python/uv declared
as worker dependencies and exact direct Python pins in main.py. uv resolves its
own transitive dependencies; it is not the locked Nix environment. `op.json` is
the generated package manifest using `bin/pydantic`. The Nix wrapper includes
Python and the locked libraries, and needs neither Go nor uv at invocation. The
implementation is a single main.py, which the existing installer copies fully.
No immutable selector for this change is verified until its package is built,
tested, published and downloaded on a clean worker. `published` is mutable.

[host.yaml](examples/host.yaml) declares public endpoint/model/prompt and a secret
scalar key and forwards them through an ordinary include to
[inference.yaml](examples/inference.yaml). At submission bind `api_key` to
`${{ context.secrets.your_host_key }}` rather than fetching it in Cortex. Replace
the example's explicit placeholder with the verified immutable commit before
use. The leaf marks the same recipe input secret and the operation marks
`api.api_key` with `x-secret: true`. The host controls the leaf's response schema.
Actual include propagation, assistant schema/citation semantics, durable
discussions, runtime deadlines and storage/retention tests belong to
cortex-assistant. Operation fixtures do not establish those behaviors.

Runtime-managed secret inputs may still be retained by JobDB or execution
records. Operation output scans establish no runtime retention or deletion
policy. The owner must check those boundaries separately on c2j v0.0.122 and
JobDB v0.0.74; this change does not modify those targets or Go 1.26.

Run both the candidate-packaged Go process suite and deterministic source Python
binding suite with `make -C pydantic test`. CI and native verification share this
entrypoint. Build the candidate package first and set `C2OPS_TEST_FLAKE=path:/absolute/checkout`.
The runner resolves Python and test-only PyYAML from the existing Nix pins.
Verification supplies `PYDANTIC_TEST_PYTHON` and `PYTHONPATH` explicitly from those
same packages. Loopback binding is required; no live credentials are needed.

For individual Python checks with the pinned interpreter:

```sh
python pydantic/test_binding.py
# Against actual installed bytes, using the package's Python plus test-only PyYAML:
python pydantic/test_binding.py --module "$package/libexec/main.py" --launcher "$package/bin/pydantic"
```

The trusted fixture invoker may pass `--fixture-loopback` to the process, or call
`infer(..., test_loopback=True)` from Python. These permit HTTP only on literal
127.0.0.1 or ::1. Neither manifest supplies this argument and no invocation field
can enable it. Never add the fixture argument to a deployed manifest. Tests keep
authorized-header observations in memory, outside output scans, and scan child
stdout/stderr, operation-created files, and unexpected telemetry requests. No
Docker or live provider is needed.
