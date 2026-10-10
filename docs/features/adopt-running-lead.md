# Adopt a running project lead

A person with `harness.control` and `run.create` can select **Adopt a running
session** on the project lead card. The list contains only their canonical
person-owned root coordinators in that project, with fresh reporting (at most
two minutes old), and no pending pause, stop or archive. It shows the computer,
harness and latest reporting time. Pages use bounded keyset pagination.

If the first page is empty but a reporting root coordinator has no person
owner, the panel names its label (or harness) and computer and explains that it
must be registered with a key created by the adopting person. An agent key
without a person creator cannot establish that ownership. The
explanation does not make that session eligible, and other people's owned
sessions are not named in it. Ownership and adoption authority remain unchanged.

**Confirm adoption** creates an owned lead intent if absent, or selects a
session for the same owner's existing unbound intent. Confirmation preserves
the current session and its context. The card shows **Adoption confirmed ·
waiting for session proof** until the session supplies its existing lease:

```sh
aeon harness lead claim --state-dir /path/to/existing-heartbeat-state
```

Use the directory passed to `harness run-heartbeat --state-dir`. Claim reads
the existing lease, project and registered reference from its owned private
files while the heartbeat continues running. `--project KEY` is optional with
state and must match its project when supplied. The command reads the current
lead revision unless `--expected-revision` is specified, then proves the lease
in a request header. Neither proof is printed. A person must confirm first;
the command cannot create an intent or choose its owner.

The registered reference has two shapes: a coordinator registered with
`--source-session` uses `<harness>:<lowercase source UUID>`; a registration
without that combination uses the random value in `session.ref`. New heartbeat
state records the exact reference sent at registration. Older state uses its
saved native-session binding (`index.source`) and the server's registered
harness and role to derive the same value; it never guesses from a transcript.
The public Aeon session UUID in `session.id` is not the reference proof.

Raw proof files remain supported as an alternative:

```sh
aeon harness lead claim --project AEON \
  --harness-session-file /path/to/private-registered-reference \
  --worker-lease-file /path/to/existing-heartbeat-state/lease.key
```

Both files must be private regular files. The raw reference file must contain
the actual value used at registration. In older native-coordinator state,
`session.ref` contains the random fallback and is **not** that value. If such
state lacks its saved native binding, use a private raw file containing the
actual `<harness>:<lowercase source UUID>` reference. Raw proof flags cannot be
combined with `--state-dir`.

The fenced claim rechecks person authority, session ownership, root role,
reporting freshness and pending controls. Only the confirmed session receives a
lead generation and a dispatch credential. Adoption counts an already running
process, so it does not run admission for a new process. Automatic lead launch
remains disabled pending AEON-603; future worker launches and queue dispatch
still require their existing live gates. Existing bound leads use ordinary
succession, and paused intents need explicit restart. Adoption never launches,
restarts, stops or reparents a session or worker.

While the selection is unclaimed (`adoption_pending`), the card and the detail
panel offer **Cancel** and do not offer Pause or Resume. Cancel clears only
the selected session. The session keeps running: nothing is paused, stopped
or reparented. Pausing that reason is rejected. After cancel, **Start lead**
or another adoption can proceed. A claimed generation still offers cooperative
pause.

Unmanaged leads show **Unmanaged: steering limited to messages and pause**.
Their card and detail panel offer cooperative pause once a generation is
claimed, with no Start, Stop, Cancel start or Resume control for their
process. A stopped unmanaged session continues from the session itself. Lead
decisions and the queue remain visible. The audit records
`lead.adoption_requested`, `lead.adoption_cancelled` and `lead.claim_checked`
without private proof values.
