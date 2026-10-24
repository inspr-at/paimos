<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Native app sign-in handoff

AEON-1731 supplies the server half of PAPP's two-hop sign-in. It creates an
initial device-session family. Bearer route admission (B1b), refresh and
revocation (B1c), and stream enforcement (B1d) are separate packages; the
credentials issued here do not yet authenticate API requests.

The app opens `GET /api/auth/native/start` at the instance's configured
canonical HTTPS origin, with `tenant`, `state`, `code_challenge`,
`code_challenge_method=S256`, and
`redirect_uri=paimos-app://auth/callback`. App state must be a random value with
at least 128 bits of entropy, encoded as 22–128 base64url characters. The
verifier follows RFC 7636 (43–128 unreserved characters). The server validates
the request host against its configured origin; forwarding headers never select
the origin. When an `Origin` header is supplied, it must match exactly.

`native_login_transactions` binds the tenant, origin, fixed redirect and app
challenge for ten minutes. The existing OIDC flow uses its own state, nonce and
PKCE verifier. Its signed cookie adds only `native: {tx}`; the app's raw state
stays in the transaction row. The real callback resolves the OIDC person, then
locks and captures app state, binds a hashed one-use code with a 60-second
expiry, clears raw state, and commits before redirecting to the app. A native
callback creates no browser session and preserves any existing browser session.

The app redeems at `POST /api/auth/native/token` with
`{code, code_verifier, state, redirect_uri}`. Optional `device_label` defaults to
`PAIMOS App`; an optional `tenant` query parameter asserts the selected tenant.
The transaction checks every binding and current OIDC person/role membership
under the tenant access fence, then consumes the code and inserts a family in
one commit. Refusals return no credentials and make no partial write. Codes
carry a tenant routing prefix so lookup can enter forced tenant RLS before
reading any login row; the prefix grants no authority.

The response contains `access_token` (`pdv_`), `refresh_token` (`pdr_`),
`expires_in=900`, `device_session_id`, `tenant_id`, and `person_id`. Only SHA-256
credential hashes are persisted. The family ID is `device_sessions.id`, with
the person, label, created/last-used times, 15-minute access expiry, 30-day idle
expiry, 180-day absolute expiry, and nullable revocation time. Later packages
must use this family row as their lifecycle fence. Neither browser-session
authentication nor agent-key scope rules admit the new token prefixes.

Both tables force tenant RLS and classify all columns in the DSAR registry;
credential and state material is excluded from exports. Responses use
`Cache-Control: no-store`, and handoff redirects use `Referrer-Policy:
no-referrer`. Failure logging records classes only.

Each server limits starts to 20 and exchanges/callbacks to 60 per transport
peer per minute, with at most 4096 active peer/operation buckets. Forwarded peer
headers are ignored. Starts additionally have a persistent tenant limit of
120 per minute, serialized under the tenant fence. Refusals return 429 and
`Retry-After: 60`. A startup sweep and one-minute worker delete expired login
rows in bounded tenant pages and delete batches, removing abandoned raw state
by the next sweep. Worker failures are logged by class for operational repair.

`internal/auth/native_test.go` exercises the real callback against the fake
IdP, exact app-state delivery and committed clearing, two independent PKCE
hops, replay/binding/expiry refusals, active membership and identity checks,
concurrent callback/exchange winners, membership-revocation interleaving,
tenant RLS, cleanup, input bounds, rate limits, and content-free logging.
