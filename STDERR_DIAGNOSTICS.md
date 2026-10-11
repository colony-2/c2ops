# Op stderr handling

Stdout is the extension JSON protocol. Child diagnostics must not be mixed into
it. Forward stderr where it provides useful execution feedback; retain deliberate
capture for parsing, artifacts, or concise errors.

| Ops | Handling |
| --- | --- |
| Codex, skill-run | Live CLI stderr is teed to the parent and `stderr.txt`. Version checks retain stderr in startup errors, including argv, resolved executable and exit status. Git skill setup captures output and includes it on failure. |
| Aider, Kimi | Live CLI stderr is teed to the parent while retaining artifact text. Stdout stays captured for result parsing. Partial lines are forwarded; Kimi's timeout covers both execution and stderr draining. |
| LLM, LLM2 | Git stdout is a filename protocol. Failed commands retain stderr in the returned error; failures in optional staged/untracked queries are logged even when collection continues. Successful discovery probes stay quiet. |
| GHA, GHA-many | Git and Docker probes include captured output in failures. Workflow logs are deliberately captured through the existing structured hooks and published as log artifacts. |
| Pydantic, LiteLLM, Jev | Provider libraries run in-process; these ops do not capture child stderr. Existing exception diagnostics remain. Jev deliberately reports the exception type to avoid echoing request data from SDK validation errors. |
| Rule gate | Runs in-process and already reports failures on stderr. |

The parent worker controls when collected stderr appears in its UI or job logs.
Forwarding from an op does not change the runner's own buffering policy.

The normal CI test workflow runs the stderr regressions through `make test`:
Python subprocess tests cover live output, success/failure, UTF-8 partial lines,
launch failures, and timeout cleanup. Go tests cover retained Codex artifacts
and parent diagnostics, plus Git failure details without contaminating filenames.
