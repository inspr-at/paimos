# Lead handover and short review/fix jobs

A coordinator registered with the same principal, harness and native session
reference automatically takes over its stopped or heartbeat-lost predecessor's
live children. The transaction records one `harness.adopted` event per child and
`harness.handed_over` on the old lead; stopped children remain historical.

For opt-in chat, a native reference and every linked alias retain one person,
chat role and project across registrations. Relationships persist even before
first binding, so binding one reference claims the connected aliases without
replaying older generations. Registration, replay, renaming and binding share
one database store and table guards. Conflicting ownership rolls back the whole
write; components larger than 1024 references fail closed. Ownership is never
implicitly released by stopping, archiving or handing over a generation.
Replacement claims precede stop-trigger reply-obligation events as well as
registration events. This identity groundwork remains disabled by default.

A healthy lead is never replaced. `harness run-heartbeat --role coordinator
--source-session NATIVE_UUID` uses that stable native reference; use a fresh
private state directory for the new process generation. After an unclean
restart, the helper retries an active-generation registration conflict at the
heartbeat interval while its owner process lives, logging each retry. Once the
predecessor's heartbeat expires, registration and child adoption proceed
automatically. For a different native session, pass `--succeeds OLD_SESSION_UUID`
to `harness register` or `harness run-heartbeat`. The predecessor must belong to
the same principal and project. A handed-over generation cannot revive through
a late heartbeat.

A Claude coordinator can register Claude children with the same authenticated
`--agent`, `--parent-session LEAD_UUID` and a distinct private session reference
and worker lease for each child. Child registration ignores the launcher's
ambient `CLAUDE_CODE_SESSION_ID`, which belongs to the parent. For a child's own
native binding, give `harness run-heartbeat` that child's `--source-session UUID`;
manual registration can use the child's native ID as its private session ref.
Explicit vendor references remain unique among active generations per tenant and
agent across all harnesses. Registration conflicts name the conflicting field
without returning private values. Vendor binding conflicts also append
`(sha256:<16 hex characters>)`, a fingerprint of the supplied reference using
the first eight bytes of `SHA-256("aeon.harness.ref\0" + reference)`. The same
reference has the same fingerprint on insertion and replay; the reference
itself and worker lease remain private. Concurrent children use separate
`--harness-session-file` and `--worker-lease-file` files.

On **Agents**, the old lead links to its successor and adopted workers link back
to the old lead. Drag a live worker to a live lead, or choose **Move to lead…**
from its menu. A person must own both registrations or be an owner/admin in the
same project and hold `harness.write`; sessions whose legacy owner is unknown
require an admin. The server checks revisions and rejects cycles. The toast's
**Undo** rechecks rights and hierarchy changes; an ordinary heartbeat does not
invalidate it. Moves preserve ticket bindings and estimates.

For flywheel gates and fixers, wrap the existing command without changing its
own review, sandbox or permission arguments. Retain the launcher's environment
sanitization (including the three `CLAUDE_CODE_*` messaging variables):

```sh
aeon harness run --project AEON --ticket AEON-322 --label "Handover review" \
  --role reviewer --harness claude --parent-session "$LEAD_SESSION" \
  -- claude -p "Review the prepared AEON-322 diff read-only"
aeon harness run --project AEON --ticket AEON-322 --label "Handover fixes" \
  --role fixer --harness claude --parent-session "$LEAD_SESSION" \
  -- claude -p "Apply only the accepted AEON-322 fixes"
```

The helper registers before launching, heartbeats while the command runs and
marks the generation stopped on success, nonzero exit, launch failure or
SIGTERM. It preserves stdout, stderr and the command's exit code; SIGTERM exits
143 after settling the session. It signals only the process group it launched,
using the existing ownership fence, with a five-second TERM grace period.
`reviewer` and `fixer` are worker jobs with a public activity note; reviewers
default to `scout`, fixers to `ship`. Project defaults to the ticket prefix,
agent to the authenticated caller, and harness to a recognized command name.
No command text or arguments are sent to the server. The default private state
directory is retained for recovery; `--state-dir` selects an explicit new one.
A failed stop prints that directory and retains the existing heartbeat retry
intent. Reuse `run-heartbeat` to settle it; `run` refuses to launch a second job
from an already-used directory. These helpers confer no additional permissions
and do not replace the ticket's worker marker or release review gates.
