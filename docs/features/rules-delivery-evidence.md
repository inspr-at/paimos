# Run-bound rules delivery evidence

Managed Claude runs use a five-minute, server-verified full-pack manifest before
launch. The unchanged D1 renderer includes the compiled safety floor, authorized
pinned doctrine files, the run's committed `AGENTS.md`, and all applicable
database rule layers. Agentd attests the repository delta; the server independently
renders the authorized doctrine and database content and compares the final
SHA-256. Neither rule bodies nor repository contents are stored in the manifest.

After the bridge observes SDK session startup, it acknowledges the SHA-256 of
the UTF-8 rules string it received. Agentd compares that value with the bytes it
sent and the served manifest before reporting delivery. A mismatch refuses
startup; failure to record the acknowledgement returns an error and fences the
owned run through the existing protocol-failure handling.

`POST /api/projects/{projectId}/harness-sessions/{sessionId}/rules-delivery-manifest`
issues a manifest. `POST` on the adjacent `rules-delivery-evidence` route records
the acknowledgement; `GET` reads it, including after stop/archive. Issuance alone
returns an explicit null evidence field when read. Both writes require
`harness.worker`, the paired run content capability, the exact active harness
lease, and the current run/computer/daemon generation. Foreign, rebound,
expired, or hash-mismatched requests are rejected before replay. Identical
retries return their original metadata; divergent reuse conflicts.

A successful first acknowledgement appends the audit event
`harness.rules_delivery_acknowledged`; a replay appends no additional event.

The tenant-RLS table `harness_rules_deliveries` freezes issuance metadata and
permits one acknowledgement. Its evidence type is
`agentd_transmitted_bytes_acknowledged`. It describes transmission and a bridge
startup acknowledgement; it does not prove model obedience or grant authority.
New agentd runs no longer create legacy pre-launch receipts. The separate
`harness_rules_receipts` history retains its
`worker_reported_received` semantics.
