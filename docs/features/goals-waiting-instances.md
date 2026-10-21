# Goals: Waiting on you across instances (AEON-1495)

The owner of several PAIMOS instances (for example `ppm`, personal and INSPR, and
`pma`, Augmentoring) sees every question that waits for them in one band on Goals,
read-only, with a link into each instance's Desk. Design truth: Tick's approved
`aeon-1494-goals-inflight-gaps-approved.html`, decision 2 (attached to AEON-1494).

## What the band shows

- Each line: instance tag · project or ticket key · question (one line) · "asked by X" ·
  Open. A sibling's Open opens that instance's Desk in a new tab (external-link icon).
- Order: this instance first, held items first within each instance. At most three
  lines from this instance and two from each sibling.
- Count: "4 · 1 on pma". It adds what each sibling reported; an unreadable sibling adds
  nothing.
- A sibling that cannot be read is one quiet line in the same slot: "pma couldn't be
  read · Try again". This instance's lines stay. Its tip says how to fix it: sign in on
  pma and open Goals there once.
- Coming: the person's next upcoming step (a human check on work that has not reached
  Done yet), in this instance's band only.
- Phone: the count, the first question with its tag, "N more · 1 on pma", Open Desk.
- The band paints once the sibling reads settle (each read is bounded to 8 s), so
  arriving lines never push the goals. Siblings are read again every minute while the
  tab is visible, and on Try again.
- People who are not the workspace owner, and every agent, see this instance only:
  no tags, no sibling reads.

## Desk projection additions

- `asked_by` on Desk items: a question's first asker, an approval's asking agent.
- `upcoming`: up to five person steps the reader owns that are not due yet.

## Concept: reading a sibling without new credentials

**Decision.** The owner's browser reads each sibling with the owner's own session
there. No server ever talks to another instance, and no key, token or secret is added.

1. Deployment configuration names the instances: `AEON_INSTANCE_TAG=ppm` and
   `AEON_SIBLING_INSTANCES=pma=https://pm.augmentoring.com` (exact HTTPS origins, at
   most four). No tenant or request can add a sibling. Without it the feature is off.
2. `GET /api/decision-desk/instances` names this instance and, for the workspace owner
   only, its siblings.
3. Each instance answers `GET /api/decision-desk/owner-summary` for its workspace owner:
   at most five first lines (project key, question, asker name, held, app link), the
   open and held counts and the first upcoming step. No context, findings, options or
   answers. Browser sessions only; agent keys and keys acting for a person are refused.
4. **Why a second cookie.** The session cookie is `SameSite=Lax`, so a cross-site read
   from ppm's page to pma never carries it (and making the session `SameSite=None`
   would open every route to cross-site requests). Instead, when the owner uses Goals on
   pma, pma also sets `aeon_desk_peek`: a copy of the same session, `HttpOnly`,
   `Secure`, `SameSite=None`, scoped to `/api/decision-desk/owner-summary`. The server
   accepts it only for that exact GET route, only when the request's `Origin` is a
   configured sibling and never next to an `Authorization` header. Every other route,
   method and origin ignores it. Only the configured sibling origin receives
   `Access-Control-Allow-Origin` with credentials. Signing out clears it, a revoked
   session revokes it, and a non-owner who still holds one has it cleared on their next Goals
   visit. A stale copy never clears the session cookie of the instance it was read from.
5. Application documents add the sibling origins to their Content-Security-Policy
   `connect-src` (portal documents and the API keep theirs), so the browser may make
   these reads at all.
6. **Limit.** Browsers that block third-party cookies (Safari, Firefox's strict mode,
   Chromium with third-party cookies off) never send the copy, so the sibling shows its
   quiet "couldn't be read" line. That is the honest result, not an error.

**Trust contexts.** ppm (personal and INSPR) and pma (Augmentoring) stay separate:
neither server reads, stores or logs the other's data, and no credential moves between
them. The two are joined only on the owner's screen, from the owner's own sessions,
and only in memory for the open tab. Agents and keys never learn that a sibling exists.

**Account-context matrix (AEON-1044).** The matrix decides which agent accounts may
work in which context; Markus may use any account. This feature runs no agent and uses
no agent account or key, so the matrix is unchanged. The summary is the owner's own read
of the owner's own Desk.

**Legal note.** Data minimisation: first lines only, no storage, no server-side
processing outside the instance that owns the data. Each instance stays the only
processor of its own data; the other instance's page only displays it in the owner's
browser. The owner is the same person in both contexts.
