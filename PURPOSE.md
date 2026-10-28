# c2ops purpose

c2ops implements and packages extension operations that c2j recipes invoke as
external processes. It supplies reusable integrations for model inference,
coding agents, skill execution, GitHub Actions, typed evaluation, and
deterministic policy checks. The operation catalog and usage examples live in
[README.md](README.md); the package inventory is [nix/ops.json](nix/ops.json).

## Responsibilities

- Maintain `llm` and `llm2` for simple and richer model inference, and the
  Python-backed alternatives `litellm` and `pydantic`.
- Maintain `codex`, `aider`, and `kimi` coding-agent integrations, including
  their operation-specific session and artifact handling. The `skill.run`
  entrypoint in `codex/run_skill` runs a requested skill and validates its
  structured output; its Nix package attribute is `skill-run`.
- Maintain `gha` and `gha-many` workflow integrations, `jev` typed evaluation,
  and `rule_gate` deterministic recipe policy checks.
- Maintain operation manifests, input/output schemas, process wrappers,
  diagnostics, and compatibility tests for these integrations.
- Build and publish the Nix runtime packages for `x86_64-linux` and
  `aarch64-linux`, including compiled Go executables, Python environments, and
  required CLI wrappers. Packaging and release conventions are documented in
  [NIX_PACKAGES.md](NIX_PACKAGES.md) and implemented in
  [flake.nix](flake.nix), [nix/packages.nix](nix/packages.nix), and
  [the CI workflow](.github/workflows/test.yml).

The LLM adapter abstractions described in [AGENTS.md](AGENTS.md) are part of
this responsibility: for example, [llm2/pkg/adapters](llm2/pkg/adapters)
provides provider interfaces, streaming, file handling, and tool execution.
They are one component of the broader extension-operation repository.

## Interfaces and dependencies

Each operation accepts a JSON object on stdin and emits a JSON result envelope
on stdout, with operation-specific outputs and, where supported, artifact or
object references. Diagnostics use stderr. Manifests declare schemas and
invocation details; [EXTENSION_OPS.md](EXTENSION_OPS.md) defines the extension
contract and [STDERR_DIAGNOSTICS.md](STDERR_DIAGNOSTICS.md) describes diagnostics.
Per-operation guides and manifests define the supported inputs, session
formats, and artifacts; see [the Codex guide](codex/README.md), for example.

Recipes select operations through local/Git manifests or explicit Nix package
selectors. The `published` branch selects packages that completed publication;
a verified commit selector provides reproducibility. Local/Git Go manifests
invoke published packages rather than compiling the checkout.

Provider SDKs, upstream agent CLIs, c2j integration APIs, and workflow clients
are dependencies of the implementations. Go module files, Python script
metadata, operation manifests, and Nix expressions determine these dependencies;
[DEPENDENCIES.md](DEPENDENCIES.md) records compatibility and validation guidance.
The [Makefile](Makefile) exposes build, test, and manifest checks, while
[nix/checks.nix](nix/checks.nix) checks packaged contracts.

## Ownership boundaries

c2ops owns the operation implementations and their packaging and integration
contracts. c2j owns recipe interpretation, extension resolution and preparation,
and the execution engine that invokes them. Coordinating recipes own job
submission, routing, review, and orchestration policy; `rule_gate` supplies
checks for those callers rather than owning their workflow.

Model services, upstream coding-agent tools, GitHub Actions, and their
credentials and infrastructure remain external dependencies. c2ops adapts
their interfaces and handles operation-local execution, sessions, and artifacts;
it does not own those services or the runtime's object storage and scheduling.
