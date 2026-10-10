# Extension ops in c2ops

The current protocol is documented in [EXTENSION_OPS.md](./EXTENSION_OPS.md).
Use [NIX_PACKAGES.md](./NIX_PACKAGES.md) for this repository's packages, CI,
Cachix setup, and dependency updates. [Execution tools](./EXECUTION_TOOLS.md)
describes c2j's explicit dependency scopes and setup lifecycle.

## Calling a published op

Use the package reference directly in `op:`. These coordinates follow `main`;
wait for its CI build and Cachix upload to complete after each update:

```yaml
sequence:
  - id: evaluate
    op: nix:github:colony-2/c2ops/main#rule_gate
    inputs:
      rules:
        - id: ready
          type: assert
          value: true
          message: Ready to continue.
```

The attribute identifies the operation; the worker selects its native Linux
architecture. `skill.run` uses the attribute `skill-run`. Bare op names do not
identify packages. Do not wrap Nix selectors in `extension_execution`.

Local and Git source selectors remain available for development:

```yaml
op: ./rule_gate
# or:
op: git+https://github.com/colony-2/c2ops.git//rule_gate@main
```

Source manifests declare their tool dependencies explicitly. Their `go run` and
`uv run --script` commands still compile or prepare library environments during
execution. Use Nix selectors for prebuilt ops.

## Manifests and process boundary

`op.yaml` is the schema source. `make manifests` generates each `op.json`, replacing
the development command with `bin/<package>` and retaining the schemas, defaults,
environment, timeout, and non-Nix dependency declarations. CI checks for drift.
Each Nix derivation exposes this document as `passthru.c2j` and installs the same
JSON at `$out/share/c2j/op.json`.

The process receives one JSON input object on stdin and returns a JSON envelope
on stdout. Logs go to stderr. Both input and output schemas are required. The
Nix package directory is read-only; writable paths come from inputs such as
`context.environment.op.worktree_path`, `.workdir`, `.inbox`, and `.outbox`.
Schema defaults can supply these paths and explicit inputs override them.

Op runtime tools are bound in Nix wrappers. Codex and Kimi additionally declare
versioned `pnpm:` dependencies for their CLIs; c2j prepares those before the
execution timeout starts. Library dependencies belong in the package, because
`uv:` declarations install CLI applications rather than Python project libraries.

## Objects and artifacts

Object-capable runtimes accept an `objects` map in the stdout envelope and
`$object` markers in `output`. Annotated inputs receive hydrated descriptors;
c2j supplies `C2J_OBJECT_OUTBOX` for exported state. See the
[object protocol](./GUIDE-Op-Object-Checkpoints.md) and
[Codex migration guide](./codex/MIGRATION_OBJECT_SESSIONS.md).

Extensions own their versioned contracts, such as `c2ops.codex.session/v1`,
using `x-c2j-object-type` annotations. The op validates its own metadata and
files; c2j validates references and handles storage and hydration. Treat incoming
object `ref` values as opaque JSON.

For execution issues with older c2j versions, see
[Codex troubleshooting](./codex/TROUBLESHOOTING.md). Those historical runtime
limitations are not the current Nix package contract.
