# Step-up requests

An agent with `approvals.request` and target read access can request one
protected change. A person holding the target permission approves with an
existing passkey or a fresh OIDC sign-in. Requests expire after 15 minutes.
Decline needs no additional authentication; only the requesting agent can
withdraw. The first outcome is final.

The server adds a native `stepup` source to the Decision Desk projection.
Pending requests hold work and sort with held approvals by expiry. The
request API supplies before/after snapshots and method-bearing decided history.
The desk and phone UI extensions are separate delivery packages.

The first typed mutation is a shipped feature override, requiring
`settings.manage`. Its payload is `{ "kind": "feature", "key":
"workspace-summary", "project_id": null, "enabled": true,
"expected_revision": 0 }`. The before snapshot is `{ "key":
"workspace-summary", "project_id": null, "override": null, "revision": 0 }`
for an untouched workspace override. `before_hash` is SHA-256 of canonical
JSON (sorted object keys, compact separators). Explicit `enabled: null` means
inherit. Additional protected operations need code-owned typed adapters that
retain the existing operation's validation and transaction locks; arbitrary
routes, SQL, secrets, key changes and scope grants are unsupported.

`POST /api/stepup-requests` creates a request; list/get return only owned or
currently authorized requests. Decisions submit `request_digest` and
`revision`. `POST .../{id}/options` uses an existing passkey when available,
otherwise returns an OIDC `authorize_url` using the existing client/callback,
`prompt=login`, `max_age=0`, nonce, PKCE and signed state. The callback requires
the same browser person and OIDC subject, with verified `auth_time` after the
Approve action. Both methods bind the proof to person, request and decision,
with one use and a two-minute expiry. Provider tokens and assertions never
enter request records or audit events.

Approval acquires tenant, tree, request and target fences, rechecks the person's
current target permission, then verifies the proof and before/after hashes.
The target mutation and `applied` outcome commit together as that person.
Changed targets become `stale`; rolled-back apply failures become `failed`.
No waiting/approving intermediate state is persisted, avoiding orphaned work
after interruption. Terminal outcomes are `applied`, `declined`, `expired`,
`stale`, `withdrawn` and `failed`. Audit records contain the requester, decider,
verified method/authentication time, snapshots, digest, expiry and outcome.
Expiry is materialized on authorized get/decision or decided-history reads;
the pending desk immediately excludes expired requests.

List endpoints use bounded keysets (at most 100 requests and 1000 projects).
Migration 1307 creates the RLS-protected native table and widens only the two
1092 phone-kind checks to admit `stepup`, preserving all older writer values.
The exact-byte classifier exception covers those CHECK replacements and
requires the coordinator's normal review and release gates.
