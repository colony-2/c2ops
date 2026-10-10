# Base image omits sed from PATH, breaking pnpm-generated CLI launchers

Target repository: `colony-2/base`

## Summary

The base image successfully installs `@openai/codex` with its bundled pnpm,
but the generated `node_modules/.bin/codex` launcher exits immediately because
`sed` is unavailable on PATH. GNU sed is already present in the image's Nix
store; exposing its bin directory makes the same installed CLI work.

This is reproducible without c2j, c2ops, credentials, or a provider request.

## Verified environment

| Item | Value |
| --- | --- |
| Image | `ghcr.io/colony-2/base:build-38077543859-1` |
| ARM64 image manifest | `sha256:82cd73ffc362bac9a5d85415e274cd7a2aea8ed08edb9cd1df8e7e1d5e72147a` |
| Platform tested | Native Linux ARM64 (`aarch64`) |
| pnpm | `12.9.0` |
| Node | `v24.21.0` |
| Installed CLI | `@openai/codex@0.162.1` |
| Default PATH | `/usr/local/bin:/root/.nix-profile/bin:/bin` |
| Investigation date | 2026-10-10 |

AMD64 behavior has not been independently reproduced in this investigation.

## Standalone reproduction

Run on an ARM64 Docker host, or one configured for ARM64 emulation. This starts
a fresh disposable container and downloads the CLI from npm.

```sh
docker run --rm -i --platform linux/arm64 --entrypoint /bin/sh \
  ghcr.io/colony-2/base:build-38077543859-1 <<'SH'
set -eu
uname -m
pnpm --version
node --version
printf 'PATH=%s\n' "$PATH"
if command -v sed; then exit 99; fi
mkdir -p /tmp/codex-launcher-repro
cd /tmp/codex-launcher-repro
printf '%s\n' '{"private":true}' > package.json
CI=true pnpm add --save-exact -- @openai/codex@0.162.1
for attempt in first reused; do
  printf '\n%s launcher invocation\n' "$attempt"
  if ./node_modules/.bin/codex --version; then
    echo 'Unexpected success without sed'
    exit 1
  else
    printf 'exit status: %s\n' "$?"
  fi
done
for sed_binary in /nix/store/*-gnused-*/bin/sed; do
  if [ -x "$sed_binary" ]; then
    export PATH="${sed_binary%/*}:$PATH"
    break
  fi
done
printf '\nWith GNU sed exposed on PATH\n'
command -v sed
./node_modules/.bin/codex --version
SH
```

The initial `command -v sed` check intentionally rejects an image that already
exposes sed. The store glob is a diagnostic workaround, not a proposed permanent
image configuration.

## Actual result

Installation succeeds. Both invocations of the generated launcher fail:

```text
./node_modules/.bin/codex: line 30: sed: command not found
./node_modules/.bin/codex: line 50: cd: null directory
exit status: 1
```

After changing only PATH, without reinstalling the package:

```text
With GNU sed exposed on PATH
/nix/store/2jk00rxplr3qjmjl2q46l7izbf8gxcs6-gnused-4.10/bin/sed
codex-cli 0.162.1
```

## Expected result

A CLI installed with the image's bundled pnpm should execute successfully from
the default worker environment, including when its existing installation is
reused. This example should print `codex-cli 0.162.1` with exit status zero.

## Cause and impact

pnpm writes a shell launcher that uses `sed` while resolving its own directory.
That launcher runs later, using the invocation's PATH. Having GNU sed somewhere
in the Nix store does not make it discoverable by the shell. Nix packaging of
pnpm does not automatically wrap executable scripts pnpm subsequently generates
in its external package cache.

This caused a production Codex op to fail at its preliminary version check
despite successful c2j tool setup. The exact reported c2ops output was
`/nix/store/czns5caf18is7dfcdlcsr0dy820mfwjp-c2ops-codex-0.1.0`. It reproduced in
about 3 ms with both freshly prepared and reused c2j tool caches. Adding only
GNU sed to PATH made that unchanged op reach `version.ready` with CLI `0.162.1`.
The probe then deliberately stopped on invalid checkpoint metadata, before any
provider request.

Other pnpm-generated launchers using these utilities can be affected. This
investigation establishes the Codex failure; it does not establish that every
pnpm package fails. A native Nix Codex package avoids this particular launcher
path, but the base-image defect still affects its supported pnpm execution path.

## Suggested fix and regression coverage

- Expose `pkgs.gnused` through the image's normal runtime profile/PATH. Avoid
  hardcoding a particular Nix store hash.
- Ensure other generated-launcher helpers, including `readlink` and `uname`
  from coreutils, are available in that same environment. They were not the
  failing commands in this reproduction.
- Add a container smoke test on native ARM64 and AMD64: install the pinned CLI,
  invoke `./node_modules/.bin/codex --version` directly, then invoke it again
  from the existing installation. Use the image's default PATH and assert the
  expected version and exit status. Checking only `pnpm --version` or successful
  package installation misses the defect.

c2ops commit `31017a4` adds a downstream mitigation by binding GNU sed/coreutils
in op wrappers with pnpm dependencies. It also preserves version-probe stderr,
which previously hid the launcher error behind `exit status 1`. The base fix
would make the environment usable by other callers without that mitigation.
