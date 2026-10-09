# Adopt a running project lead

A person with `harness.control` and `run.create` can select **Adopt a running
session** on the project lead card. The list contains only their canonical
person-owned root coordinators in that project, with fresh reporting (at most
two minutes old), and no pending pause, stop or archive. It shows the computer,
harness and latest reporting time. Pages use bounded keyset pagination.

**Confirm adoption** creates an owned lead intent if absent, or selects a
session for the same owner's existing unbound intent. Confirmation preserves
the current session and its context. The card shows **Adoption confirmed ·
waiting for session proof** until the session supplies its existing lease:

```sh
aeon harness lead claim --project AEON \
  --harness-session-file /path/to/existing-session-reference \
  --worker-lease-file /path/to/existing-worker-lease
```

Both files are private regular files. The session file holds the existing
native harness reference used at registration, rather than the public Aeon
session UUID. The command reads the current lead revision unless
`--expected-revision` is specified, then proves the lease in a request header.
Neither proof is printed. A person must confirm first; the command cannot
create an intent or choose its owner.

The fenced claim rechecks person authority, session ownership, root role,
reporting freshness and pending controls. Only the confirmed session receives a
lead generation and a dispatch credential. Adoption counts an already running
process, so it does not run admission for a new process. Automatic lead launch
remains disabled pending AEON-603; future worker launches and queue dispatch
still require their existing live gates. Existing bound leads use ordinary
succession, and paused intents need explicit restart. Adoption never launches,
restarts, stops or reparents a session or worker.

Unmanaged leads show **Unmanaged: steering limited to messages and pause**.
Their card and detail panel offer cooperative pause, with no Start, Stop,
Cancel start or Resume control for their process. A stopped unmanaged session
continues from the session itself. Lead decisions and the queue remain visible.
The audit records `lead.adoption_requested` and `lead.claim_checked` without
private proof values.
