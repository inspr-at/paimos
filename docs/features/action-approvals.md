# Outward action requests

Agents can record a proposed outward effect with `POST /api/projects/{projectId}/action-requests`.
The request captures what, why, the exact effect and target, reversibility and
reversal plan, requester, project and a deadline within 24 hours. An idempotency
key binds these immutable facts and their SHA-256 digest; changing facts requires
a new key and approval. This works with existing agent principals and requires
current `approvals.request` authority and key scope in the project.

Pending requests appear as approvals on the existing Decision Desk. The native
rationale carries every action fact, including the digest. The same source feeds
phone review, passkey verification and existing opt-in notifications. A person
needs current project `approvals.read`, `approvals.decide` and
`approvals.decide_high`, and must give a reason. The project lead can decide with
those project permissions. Agent keys, including workstation agents, cannot
decide these requests. The Desk uses the project action adapter; authorization
on existing general approval routes stays unchanged.

Approval records consent for the exact effect; this module does not execute the
effect or create an `agent_permission_grants` row. Policy and tool enforcement
are integrated separately by AEON-754's policy wiring slice. Existing
messaging-only agent restrictions still apply until that integration.

Decline and automatic expiry record a reason. Approved, denied, expired and
refused outcomes atomically enqueue an existing durable inbox message and its
delivery receipt. `GET /api/projects/{projectId}/action-requests/{requestId}`
returns the facts, outcome, reason and current receipt under project read access;
agents can read only their own requests. A queued receipt means accepted for
delivery. Only a real inbox acknowledgement or supported transport confirmation
moves it to `handed_off`; delivery failures remain visible.

The server connects the existing inbox writer to HTTP decisions, verified phone
decisions and expiry. Missing delivery wiring fails closed. If outcome acceptance
fails, the decision and its outcome roll back together; the request stays pending
and no successful decision is reported.

`human_input` explicitly declares `none`, `secrets`, `tokens` or `manual_steps`.
The latter three are refused, recorded and reported to the agent without
creating a decidable approval. Common English and German asks directed at a
person for credentials or manual work in free text are also refused. Mentioning
credential rotation, password policies or deploy/merge approval does not itself
trigger a refusal. These recognizers supplement the explicit declaration; they
do not claim to classify arbitrary natural language. Credential values must never
be included in a request.

Tenant kinds default to `deploy`, `trade`, `money`, `secrets`,
`production_change` and `external_message`. Here `secrets` means using existing
authorized secret tooling, never asking a person for credentials. A person with
`settings.manage` can configure at most 64 overrides at
`PUT /api/action-request-kinds/{kind}` using the current revision; `settings.read`
permits reading the effective defaults and overrides. Every kind requires
high-risk person approval. A disabled kind cannot receive a new request or
approval of a pending request; declining remains possible.

Action facts, decisions and terminal outcomes have tenant and project RLS.
`approval.proposed`, `approval.approved` and `approval.denied` retain the native
audit path. `action_request.requested`, `.approved`, `.denied`, `.expired` and
`.refused` record the exact facts and outcome, while existing inbox events record
delivery progress. Kind changes append `action_request.kind_configured`.

The expiry worker reads bounded tenant keyset batches and resolves at most eight
due requests per tenant per pass. It acquires the shared tenant/tree fence before
request rows, and finishes all record writes before the event counter. A worker
failure leaves the request retryable; expired requests cannot be approved while
waiting for the next pass. No additional screen or layout is introduced.
