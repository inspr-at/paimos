# New project from START

AEON-1441. START (Aithema) hands a conversation to Paimos with a "Projekt
erstellen" button. Paimos opens its New project sheet over Projects, prefilled
from the conversation, and asks only what START cannot know: the key, the
owner, an optional target date and which open points become tickets.

## Handoff

START submits a top-level browser form (navigation, never fetch, no CORS) to
`POST /import/project-draft` with one field, `payload`: the strict v1 JSON
document (`api/areas/import-drafts.yaml`, `ProjectDraftPayloadV1`). The route is
public and cookie-free: it runs outside the API middleware, so no session is
read, renewed or trusted, and nothing in the payload chooses a tenant, project
or destination.

- The draft lands in the one import tenant the instance configures:
  `AEON_IMPORT_TENANT_SLUG`, default the bootstrap tenant; `off` turns the
  handoff off. START's own host option decides which Paimos instance it posts
  to; nothing is hard-wired to one instance.
- Bounds, checked before anything is stored: 512 KiB wire payload, title ≤ 200,
  summary ≤ 4,000, facts and open points ≤ 50 × 500, transcript ≤ 2,000 turns,
  unknown fields refused, version other than 1 refused with its own page. A
  client may post 20 times a minute (the shared public sliding window); the
  client is the transport peer, or the first untrusted `X-Forwarded-For` hop
  when the peer is in `AEON_TRUSTED_PROXIES` (default loopback, `none` trusts no
  proxy). At most 200 live drafts and 32 MiB per tenant.
- Success is `303` to `/new-project#draft=<capability>&lang=<de|en>`. The
  capability is 256 random bits in the fragment, which browsers never send, so
  no server or proxy log records it; only its SHA-256 is stored. Refusals are
  small pages in the conversation's (or browser's) language with
  `Cache-Control: no-store` and `Referrer-Policy: no-referrer`.
- A draft expires after 30 minutes; a sweep deletes expired drafts every minute,
  whether or not anyone posts again.

## The assistant

The router moves the capability out of the address before any other rule runs
and keeps it in the tab; a person who first signs in returns to the bare
`/new-project`. The sheet reads the draft with `GET /api/import/project-draft`
(capability in the `Aeon-Import-Draft` header). Reading never consumes it.

The sheet shows the source line, name (from the title), key (suggested; German
letters are spelt out first, so "Vorbestell-App für die Bäckerei" suggests
VAFDB), owner (people only, default you), target date, one checkbox per open
point (all on) and, read-only, what comes along: the summary, the facts, the
transcript and the knowledge entry they become. Phones fold that part into one
row. Loading uses skeletons at final heights; expired, used and someone else's
draft all show one message that reveals nothing.

## Create

`POST /api/import/project-draft/create` runs one transaction under the
access-change lock: it fences the draft row, creates the project (owner as its
assignee, the target date as `end_date`), one work item per chosen open point,
the memory entry "From the START conversation" (summary, facts, open points,
source and language; conversation text escaped so it renders as text) and the
transcript as a text attachment on the project, all through the ordinary node,
knowledge and attachment guards, then marks the draft used and appends the
events last (metadata `imported_from: start`). Any failure rolls everything
back and leaves the draft usable. A second person, or a second create after the
first, gets the same not-found answer; a retry by the person who created it
gets the same project (`replayed: true`). Used drafts keep only ids for a day.

The sheet then opens the project's tickets with "Project created from START ·
N tickets".
