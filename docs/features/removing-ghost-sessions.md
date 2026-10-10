# Removing ghost sessions (AEON-265)

People with `harness.read` access to a project can remove a session from its
row's overflow menu (**Remove from Agents…**) or with **Remove** in the session
panel, then confirm once. The record immediately leaves
active views and counts; the **Removed** filter retains its ticket links and
history. `POST /projects/{projectId}/harness-sessions/{sessionId}/remove` takes
`{"reason":"..."}` (1–240 characters), uses the current locked record without a
client revision, and is idempotent even after another tab removed it. It accepts
unmanaged, legacy managed, offline, stopped, and pending-force registrations.

Removal revokes the generation: late heartbeat, mark-stopped, receipt and control
completion writes return 410. It releases outstanding deliveries and rejects
pending controls, recording `harness.removed` with actor, session and reason.
It never signals a process or asserts remote exit. A force signal already delivered
cannot be recalled; legacy daemons may independently react to the revoked lease.
The existing verified force-stop action in **Recover** keeps its identity and
confirmation checks; it cannot target an already revoked generation.

**Clear stale** (shown in the Sessions header only when stale sessions exist)
confirms once with the count, then calls the project-scoped
`POST /projects/{projectId}/harness-sessions/remove-stale` with a reason. The server
rechecks eligibility under row locks: the last accepted heartbeat must be older
than 15 minutes, falling back to creation time for sessions with no heartbeat.
A recently stopped record with a recent heartbeat is retained. One request removes
at most 200 records; the response lists them with the server cutoff and `more`,
and the UI repeats while `more` is true. Every removal event of one request carries
the same server-generated `batch_id` and the cutoff; retries produce no duplicate
removal audit.

Deploy approvals can carry optional `target` metadata: `hosts` or `environment`,
`service`, `change`, and optionally `image`. Targets appear on the deployment
card, Needs you, and approval history; missing targets read “Target not
named” and keep existing approval behavior. `target_digest_sha256` identifies the
recorded metadata and does not add an authority check (enforcement is AEON-287).
The approval contract is `approvals/1.1`. Targets stay in storage and audit events,
and the web reads them from approvals, labelled “named by the agent”. Managed
agents can pass `target` and an optional `release_node_id` to
`aeon_request_approval`. AEON-723 retires the Journey deployment view,
stage handoffs and the external-stage CLI; compatibility paths retain their
reporter contract headers and return authenticated 410 errors.
