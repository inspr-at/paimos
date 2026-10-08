# Agent conversation foundation

AEON-618 R1 introduces a separate `chat-v1` identity contract in
`api/openapi.yaml`. The server mounts it disabled by default; integration
packages may opt in with `chat.New(pool, chat.Options{Enabled: true})`.
This foundation does not enable chat delivery, wake, history migration or
native process controls. Existing inbox, session and CLI contracts stay intact.
History, read-marker, delivery, receipt, cancellation and stream definitions
are reserved contracts marked `x-aeon-package` for R2/R3; R1 does not mount them.

A person creates their own project role, resolves its lasting conversation,
and selects an existing external inbox registration using the expected binding
epoch. Lead identity is stored as `person_project`: one lead per person and
project. Workers require distinct assignment slots. Handover preserves the
conversation and prior binding records; a native session cannot be reused for
another private role or person. Automation binding delegation is deferred.

Workers use their scoped agent key and existing `X-Aeon-Worker-Lease` through
`POST /api/chat-deliveries/binding/resolve`. The proof is checked against the
exact live external registration (heartbeat or initial registration within two
minutes), person, project, role and epoch. Public references and session IDs
alone grant no access. Readiness omits account, quota, model, host and native
reference metadata; no input/wake/receipt capability is advertised before
receiver qualification. Registration and binding do not extend execution
permissions or start an agent.

New inbox rows have an internal `chat_thread_id` discriminator and restrictive
participant RLS. Legacy reads, ACKs, replies, managed drains, streams, receipts,
sweepers and notifications exclude that mode. The database rejects legacy
transport projections of chat rows and cross-mode replies. Chat event hints
are private, and nested legacy transactions clear verified chat context.
Migration `1141_chat_identity.sql` preserves all existing rows without backfill.
