# act host executor: unsynchronized PTY shutdown flag

Observed with `github.com/nektos/act v0.2.89`, Go 1.27.1, Linux ARM64, while
building c2ops' container-free single-job executor. This report has not been
submitted upstream.

Reproduce from this repository:

```sh
cd gha
go test -race -count=1 ./internal/jobexec -run TestOnlySelectedJobRunsWithRecordedDependencyOutputs
```

The ordinary test succeeds. The race detector reports a write from
`(*HostEnvironment).exec` in `pkg/container/host_environment.go:342`, concurrent
with a read from `(*ptyWriter).Write` at line 190. The writer runs through
`copyPtyOutput` in the goroutine started by `exec`. Even act's Node executable
probe can trigger it; no Docker daemon, remote action or concurrent jobs are
needed.

Relevant code in the pinned upstream version:

```go
// exec, after cmd.Run:
writer.AutoStop = true

// ptyWriter.Write, on the copy goroutine:
if w.AutoStop && len(buf) > 0 && buf[len(buf)-1] == 4 { ... }
```

`AutoStop` is an ordinary bool with no lock or atomic synchronization. Suggested
fix: make the shutdown flag atomic or coordinate shutdown with the copy goroutine;
also audit failure/cancellation paths for joining output goroutines before
returning and for stopping subprocess descendants. A public non-PTY host mode
would be useful for noninteractive job execution, but must still drain output
and preserve workflow command handling.

Acceptance: the reproducer passes with `-race`; successful, failed and cancelled
commands drain their output and leave no running copy goroutines/descendants;
add-mask/output command handling and post steps still work.

c2ops does not disable the race detector or fork act to hide this. Its normal
executor tests and embedded integration suite pass. The integration suite can
itself run with `-race`, but its compiled helper subprocess is a normal binary,
so that check does not qualify act's internals. The new helper remains
experimental; this issue is a production qualification gate.
