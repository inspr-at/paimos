# Interactive harness commands

On macOS and Linux, `aeon harness run` registers an interactive command before
launching it, heartbeats its session and records how it stopped. For example:

```sh
aeon --instance INSTANCE harness run --label LABEL --ticket PROJECT-123 \
  --role worker --harness claude -- claude
```

`--parent-session UUID` is optional for AI harnesses; it attaches the new session
to an existing parent when the caller has permission. Instance selection,
registration authority and ticket rules apply as for other harness sessions.

When stdin is the foreground controlling terminal, the wrapper gives its owned
child process group foreground access before execution. Stdin, stdout and stderr
keep their original descriptors, including redirected output. On exit or launch
failure the wrapper restores the original foreground group and terminal settings.
Window changes and terminal input reach the child directly. Owned stop controls
continue to target only the isolated process group launched by the wrapper.

A terminal already owned by another foreground group is refused with a reason
and a hint on stderr. Run the wrapper in the foreground to use an interactive
command. Redirecting stdin keeps the existing non-interactive behavior; the
wrapper does not allocate a terminal or turn piped input into an interactive
session.

Successful commands return 0. Child exit codes are preserved; every nonzero child
exit also gets a wrapper diagnostic on stderr, even if the child printed nothing.
A child killed by a signal returns 1 with the signal in the diagnostic. Wrapper
interruption returns 143 after stopping the child. Usage failures return 2;
registration, launch and cleanup failures return 1. A child failure retains its
own exit code when cleanup also fails. Diagnostics omit child arguments and
prompts. Failed cleanup names the private state directory for recovery with
`harness run-heartbeat --state-dir DIR` and retains the stop intent.

The regression fixture uses a real PTY and a harmless child executable, with
isolated local `pma` and `ppm` configurations, each with and without a parent.
It checks foreground input, both output streams, registration, stop and terminal
restoration. A table covers usage, preflight, registration, launch, child and
cleanup diagnostics. It makes no vendor model calls or live instance requests.

AEON-1410 validation: the approved mbp2606 runner passed the PTY and failure-table
tests, including redirected output, worker/coordinator roles, nonzero child exit,
failed launch and SIGTERM recovery. Existing lifecycle and scoped-stop tests also
passed with the race detector; the final PTY suite, including foreign-terminal
refusal, and the process-ownership package passed with the race detector too.
Canonical manifest tests passed 30 cases and the
source ownership audit passed 33 checks; the two new tests are ESSENTIAL under
the existing S3 CLI source ownership. The merge-main static run was refused by
the remote night guard (exit 3), so it remains a coordinator/CI check.
