<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Agent peers

`aeon peers` lists live agent sessions belonging to the current person owner
on the selected instance, across accessible projects. Each entry gives its
name, project, harness, state and an exact `session:UUID` address. Idle sessions
are included while their heartbeat is fresh. Stopped, archived, stale,
unconfirmed-owner and volatile attached sessions are excluded.

```sh
aeon --instance ppm peers
aeon --instance ppm tell ops-lead -m 'The dependency is ready.'
aeon --instance ppm tell session:SESSION_UUID -m 'Please check the result.'
aeon --instance ppm tell ops-lead --project OPS --sender-session OWN_SESSION_UUID -m 'Status update'
aeon --instance ppm tell status MESSAGE_UUID
```

Names are exact session display labels, falling back to the agent principal's
name. Duplicate names require a recipient project filter or an exact session
address; no generation is chosen automatically. `peers --json` returns
`items` and `next_after`. Continue a bounded page with `--after SESSION_UUID`;
`--limit` accepts 1–100.

Agent sends retain the exact current Aeon session binding (or the current
harness binding), including across projects. `--sender-session` overrides that
binding. An agent without a binding must supply its own active session UUID.
People may send without an agent sender session. Recipient discovery requires
current `harness.read`; sending also requires current `inbox.send` in both the
recipient and bound sender projects. Ownership and permissions are checked
again inside the final write transaction.

Peers may share one workstation principal. The sender and recipient must still
be two distinct exact session generations; sending to the current generation
is rejected.

Messages are recorded under the recipient's project. Cross-project messages
include `sender_project_id` and `sender_project`, and their frozen sender label
contains the source project key. Counterpart replies using `--reply-to` retain
the thread and close the original reply obligation across projects. Existing
held action requests and delivery deadlines continue to apply.

Storage is confirmed separately from delivery. A queued receipt means the
message is waiting in the exact session inbox; it does not claim live injection
or delivery. Existing inbox hooks, listeners or managed drains can collect it.
Acknowledgement confirms receipt, and existing delivery failures notify the
sender. This feature does not depend on AEON-1340 live injection.

The directory never searches another tenant, instance or person ownership
context. `ppm` and `pma` remain separate, including credentials and lookups.
An explicit acting-person binding must match the key creator's canonical
person. No local cross-session sockets or other-instance fallbacks are used.

The `/agents` peer list/thread view and the LEAD runbook switch are deferred
to their coordinator-owned follow-up slices.
