# act: data race on the host runner PTY shutdown flag

## Environment

- Unmodified `github.com/nektos/act v0.2.89` CLI built with Go's race detector.
- Go 1.27.1, Linux ARM64, Bash and Node.js available on PATH.
- One workflow, one job, one shell step; no custom Go integration or remote actions.
- Host runner selected using the documented `-P ubuntu-latest=-self-hosted` option.

## Reproduction

Build the stock CLI and create a minimal repository:

```bash
repro_dir=$(mktemp -d)
mkdir -p "$repro_dir/bin" "$repro_dir/repo/.github/workflows"
GOBIN="$repro_dir/bin" go install -race github.com/nektos/act@v0.2.89
cd "$repro_dir/repo"
git init -b main
cat > .github/workflows/repro.yml <<'YAML'
name: PTY race
on: workflow_dispatch
jobs:
  probe:
    runs-on: ubuntu-latest
    steps:
      - run: printf 'hello from host runner\n'
YAML
git add .
git -c user.name=Fixture -c user.email=fixture@example.test \
  -c commit.gpgsign=false commit -m fixture
"$repro_dir/bin/act" workflow_dispatch \
  -W .github/workflows/repro.yml \
  -P ubuntu-latest=-self-hosted \
  --action-cache-path "$repro_dir/cache"
```

The workflow step succeeds, but the process exits with race-detector status 66.
This reproduced on the first run in the environment above. Repeat the final
command if necessary because detection depends on scheduling.

Expected: the same workflow succeeds without a data-race report.

## Observed report

Relevant excerpt (addresses and intervening frames omitted):

```text
WARNING: DATA RACE
Write ... by goroutine 29:
  github.com/nektos/act/pkg/container.(*HostEnvironment).exec()
      pkg/container/host_environment.go:342

Previous read ... by goroutine 32:
  github.com/nektos/act/pkg/container.(*ptyWriter).Write()
      pkg/container/host_environment.go:190
  ...
  github.com/nektos/act/pkg/container.copyPtyOutput()
      pkg/container/host_environment.go:266

Goroutine 32 ... created at:
  github.com/nektos/act/pkg/container.(*HostEnvironment).exec()
      pkg/container/host_environment.go:330

Found 2 data race(s)
```

The report also occurs during the runner's Node executable probe, before the
workflow shell step. Concurrent workflow jobs are not required.

## Cause

`ptyWriter.AutoStop` is an ordinary bool. The command goroutine sets it after
`cmd.Run()` returns, while the PTY output-copy goroutine reads it in `Write`.
There is no synchronization for that access. Writing the EOT byte to the PTY
after setting the bool does not synchronize the preceding read/write access in
Go's memory model.

Use an atomic flag or explicitly coordinate shutdown with the copy goroutine.
A regression test should run the host executor with `-race`, preserve trailing
output and workflow commands, and join the copy goroutine on completion.
Failure and cancellation paths also merit tests: `cmd.Run()` errors currently
return before the normal output-drain wait. Those paths are additional review
areas, not independently demonstrated failures in this reproduction.

## Upstream references

- [Documented host runner invocation](https://nektosact.com/usage/runners.html).
- [PTY writer and executor in v0.2.89](https://github.com/nektos/act/blob/v0.2.89/pkg/container/host_environment.go#L181-L353).
- [The CLI runner's normal execution path](https://github.com/nektos/act/blob/v0.2.89/pkg/runner/runner.go#L122-L218).
