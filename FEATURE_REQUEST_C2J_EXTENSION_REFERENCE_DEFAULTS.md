# Feature request: consistent config and secret references in extension defaults

Let extension manifests use the same whole-value config and secret references,
including `orValue(...)`, that recipes can already pass as inputs. An op should
be able to declare its conventional tenant settings once, without every recipe
repeating those bindings.

Status: proposal. The secret-reference default shown below is not supported by
the current contract. This request does not depend on placing references in a
surrounding object default as a workaround.

## Problem and current behavior

c2ops has two extension ops, `codex` and `skill-run`, with optional external API
settings. Today every recipe must repeat:

```yaml
api:
  base_url: '${{ context.config.?codex_api_base_url.orValue(null) }}'
  api_key: '${{ context.secrets.?codex_api_key.orValue(null) }}'
```

These recipe inputs work: config resolves to a public value or JSON null, while
an unavailable optional secret omits `api_key`. The op activates the proxy only
when both URL and credentials are available, retaining normal Codex behavior
otherwise.

Moving the same expressions into extension defaults changes their meaning:

- Ordinary extension defaults support templates, such as the existing
  `workdir_path` default `{{ context.environment.op.workdir }}`.
- An `x-secret: true` property's `default` is a literal public fallback.
  A string that looks like a secret-reference expression is passed literally;
  it does not bind the omitted input to that named secret.
- Secret references supplied by recipes already support literal string
  fallbacks and `orValue(null)` omission. Authors cannot declare the equivalent
  reference as the extension's default input binding.

This distinction is documented in [SECRETS_AND_CONFIG.md](SECRETS_AND_CONFIG.md),
under “Optional secrets and public fallbacks.” It is also explicit in c2j
v0.0.69 (used by c2ops tests) and v0.0.75 (source inspected):

- `pkg/secrets/schema.go`: `ApplySchemaDefaults` states that it never substitutes
  a named secret or evaluates default strings.
- `pkg/worker/ops/tool_setup_test.go`:
  `TestPreparationAttachesExtensionSecretDefaults` declares
  `default: '${{ context.secrets.never_resolve }}'` and expects that exact literal
  string after materialization. This is an existing minimal reproducer.

An author can therefore accidentally pass template text as a bearer token,
without any lookup of the intended secret. Changing the location of a binding
from recipe input to manifest default should not silently change its meaning.

## Requested manifest behavior

Support this declaration in source manifests and packaged Nix manifests:

```yaml
input_schema:
  type: object
  properties:
    api:
      type: [object, "null"]
      additionalProperties: false
      properties:
        base_url:
          type: [string, "null"]
          default: '${{ context.config.?codex_api_base_url.orValue(null) }}'
        api_key:
          type: string
          x-secret: true
          minLength: 1
          default: '${{ context.secrets.?codex_api_key.orValue(null) }}'
```

A recipe omitting `api` should receive these default bindings, including creation
of the enclosing object when needed. It should not need to know the setting
names or declare them again. Explicit `api: null` should disable the whole
binding and perform neither lookup. Explicitly supplied fields should win over
defaults; the treatment of partial objects must be documented and tested.

For this Codex example, with the settings provider configured:

| Tenant values | Materialized default input | Codex behavior |
|---|---|---|
| Both available | URL and resolved key | Use external API |
| Neither available | Null URL; key omitted | Preserve normal behavior |
| URL only | URL; key omitted | Preserve normal behavior |
| Key only | Null URL and resolved key | Preserve normal behavior; do not forward the unused key |

The last column is the op's policy, not a generic c2j rule.

## Resolution and compatibility requirements

1. Treat a supported whole-value reference default as a deferred input binding,
   using the same parser and task-time materialization as an explicit recipe
   reference. Resolve it under the consuming task's execution lease. Submission,
   manifest evaluation, and replay of completed tasks must not retrieve values.
2. Preserve existing public literal defaults. Separate “which reference fills
   an omitted input” from “which literal value replaces an unavailable secret.”
   A reference default must not become a literal fallback attached to a
   caller-supplied secret or trigger a second secret lookup when that secret
   is unavailable. Explicit reference fallbacks retain precedence.
3. Preserve optional-secret semantics: `orValue(null)` omits the property;
   `orValue("")` supplies an empty string. Do not reapply the default after
   omission. Required fields and string constraints still apply; an available
   empty secret is present and fails `minLength: 1`.
4. Preserve config semantics: absent config resolves to null or its specified
   fallback. Existing null, false, zero, and empty values are not missing.
5. Keep references opaque until materialization, enforce `x-secret` on receiving
   fields, and persist references rather than secret values. Do not expose
   resolved credentials in manifests, Nix derivations, durable inputs, or logs.
6. Keep the existing expression restrictions: whole-value references, static
   names, and supported literal fallbacks. This request does not require
   arbitrary CEL evaluation, interpolation, routing on secrets, or dynamic
   secret names.
7. Missing or denied names may select their documented fallback. Provider
   outages, an unconfigured provider, lease loss, cancellation, and timeouts
   must still fail. “No values set” is different from “no provider configured.”

Because c2j currently tests expression-looking secret defaults as literal text,
this needs a documented compatibility decision. Prefer consistent `default`
semantics with a migration path for intentional literals; if that cannot be
introduced compatibly, provide an explicit reference-default annotation with
identical resolution behavior. Do not require nesting tricks or op-specific
JobDB access to obtain the same result.

## Acceptance tests

- Run the same binding through an explicit recipe input and an omitted input
  with a manifest reference default; compare resolved values and omission for
  present, missing, and denied secrets and present/missing config.
- Cover both source `op.yaml` and Nix `c2j`/installed `op.json` manifests, including
  nested fields when the enclosing object is omitted.
- Cover explicit overrides, partial objects, whole-object null opt-out,
  required fields, empty secrets, and incompatible config types.
- Verify existing literal defaults remain literal, explicit fallbacks are not
  replaced, and an omitted optional field does not cause repeated lookup/default
  application.
- Execute a real extension through the worker with a test settings provider;
  verify its stdin contains materialized values while durable records contain
  references and logs redact secrets.
- Verify provider errors fail, completed-task replay performs no lookup, and
  unsupported reference placement fails before launching the extension.
