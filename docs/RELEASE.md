# Release

Aeon uses INSPR Calendar Versioning, currently INSPR-CalVer3 (`inspr-calver-3`). The coordinate is `YYMMDDhhmmss.0.0`: two-digit year, month, day, hour, minute, and second in UTC, then `.0.0`. It is SemVer-shaped and fixed width. `version.json` is the only source of that coordinate. The fields that matter for a release are `version_scheme`, `version`, `release_channel`, and `release_sequence`.

### From CalVer2 to CalVer3

Releases up to `260929113854.0.0` (release 10, sequence 105) were reserved under `inspr-calendar-v2` (INSPR-CalVer2). Both schemes share one coordinate, so versions keep sorting as time and every earlier tag stays exactly as published. From release 11 on, a reservation writes `"version_scheme": "inspr-calver-3"` in `version.json` with a coordinate later than `260929113854.0.0`; `scripts/verify-release.mjs` rejects a new reservation that still declares `inspr-calendar-v2` (`LAST_CALVER2` in that script, `releasehistory.LastCalVer2` in Go). Only the reservation changes; `release_sequence` continues.

The version pill uses the shared INSPR renderer: six segments (`YY·MM·DD hh:mm`, seconds on hover, focus or tap), never the `v` or `.0.0`. Copying always yields the exact canonical version, `.0.0` included. The label table (`schemes.json`) names the schemes INSPR-CalVer3, INSPR-CalVer2 and INSPR-CalVer1.

The git tag is `v` plus the `version` field, for example `v260926064658.0.0`. Create an annotated tag with a message: `git tag -a "$tag" -m "Release $tag"`. `scripts/release-tag.mjs` checks that a pushed tag has that shape; the release workflow rejects it unless `git cat-file -t "refs/tags/$tag"` returns `tag`. `scripts/verify-release.mjs` checks that `version.json` matches the scheme and that the vendored calendar presentation bundle under `web/src/vendor/calendar-version-display` matches `scripts/calendar-version-bundle-pin.json`. `just release-check` runs the verifier. A production web build runs the same check before it emits assets.

Development builds leave the linker version at `dev`. A release build sets:

```
-X github.com/inspr-at/paimos/internal/version.Version=<version>
```

with `-trimpath`. The server image, `aeon-cli`, and Linux `paimos-agentd` use `CGO_ENABLED=0` (`Dockerfile` for the image). Darwin `paimos-agentd` is built on macOS with `CGO_ENABLED=1` and links LocalAuthentication. `scripts/build-release-binaries.sh` is the build used by `.github/workflows/release.yml`.

## Workflow

A push of a `v*` tag runs `.github/workflows/release.yml`.

1. Check out the repository with tags, so release history can see earlier coordinates.
2. Validate the tag and run `scripts/verify-release.mjs --release`. Fail if `version.json` disagrees with the tag.
3. Refuse a coordinate whose GitHub release already exists, including drafts (the authenticated, paginated release list includes them). macOS runners build darwin `paimos-agentd` with CGO enabled, then sign it with Developer ID (team P66J39QV6V, hardened runtime) and notarize it in the `release-signing` environment before upload (docs/AGENT_INTEGRATION.md, Signed release daemon). The Ubuntu job builds Linux `paimos-agentd` statically and all `aeon-cli` targets with CGO off, then checks the darwin binaries.
4. Generate the release-history manifest embedded in the server image. That file is produced at release time. It is not committed.
5. Run the image smoke gate. Publishing waits for it.
6. Refuse a coordinate whose GHCR image tag already exists, including a tag left by a partial earlier run. Push the image to `ghcr.io/inspr-at/aeon:<version>`. There is no `latest` tag.
7. Create the GitHub release once as a **draft** with the CLI, signed/notarized darwin `paimos-agentd`, Linux `paimos-agentd`, and `SHA256SUMS`. Existing releases are never uploaded to or overwritten. The notes name the image and its digest. A successful tag build ends here; it does not publish the draft or open a Homebrew PR.
8. The release coordinator deploys that exact image digest through the normal deployment gates and verifies the live server's version and health. Only then publish the existing draft as described below. Failed or incomplete verification leaves it a draft.
9. Publication triggers `.github/workflows/homebrew-tap.yml` (`release: published`). Its `homebrew-tap` job validates the exact event tag, rejects drafts and prereleases, and reads public release metadata before downloading `SHA256SUMS`. It renders `Formula/aeon-agentd.rb` from that release's darwin checksums and, when `HOMEBREW_TAP_APP_ID` and `HOMEBREW_TAP_APP_KEY` are present in the `homebrew-tap` environment, opens a pull request on `inspr-at/homebrew-tap`. The formula installs the signed, notarized darwin bytes with `bin.install` and does not rebuild or re-sign them. If either secret is absent the job logs `homebrew tap bump skipped: app secrets absent` and succeeds. The stable 105 sample is [docs/homebrew/aeon-agentd.rb](homebrew/aeon-agentd.rb).

### Publish after live verification (AEON-356)

Run this explicit step from the release coordinator's checked-out release commit, only after recording the successful live verification against the image digest in the draft notes. Confirm the tag, draft state and complete nine-asset set (eight binaries plus `SHA256SUMS`); do not publish a draft from a failed or partial tag workflow.

```sh
tag="v$(node -p 'require("./version.json").version')"
gh release view "$tag" --repo inspr-at/paimos --json tagName,isDraft,assets,body
# After live verification and inspection above:
gh release edit "$tag" --repo inspr-at/paimos --draft=false
gh release view "$tag" --repo inspr-at/paimos --json tagName,isDraft,publishedAt,url
```

Use the coordinator's approved GitHub CLI identity (or an approved GitHub App identity) with release write access. Do not publish using a workflow's `GITHUB_TOKEN`: GitHub suppresses downstream release-event workflows for that token. Publishing via this CLI step emits `release.published`, which starts the Homebrew workflow at the release tag. The new workflow must be included in the tagged commit. See GitHub's [release event reference](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#release) and [workflow token restrictions](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow#triggering-a-workflow-from-a-workflow).

Confirm `isDraft: false`, public asset availability, and the Homebrew workflow result/PR before considering distribution complete. If the tap job fails, fix its cause and rerun that job; do not rerun the tag build, toggle publication to retrigger it, replace assets, or reuse the coordinate. The tap PR still follows its own checks and merge approval. Missing app secrets mean no automatic PR; record that result for the coordinator to resolve before claiming Homebrew is updated.

Drafts are excluded from public release discovery and GitHub's `latest` endpoint. The tap script uses unauthenticated exact-tag metadata and asset requests, with an explicit published-state check before any tap mutation. The server's installation guide already pins the running server version and uses unauthenticated exact asset URLs; it never enumerates authenticated drafts or substitutes `latest`. Between server deployment and publication those downloads fail closed, and Homebrew still offers its previously merged release. Publication makes the same pinned URLs available without a server rebuild. Never give these consumers credentials to read drafts. See GitHub's [release API visibility rules](https://docs.github.com/en/rest/releases/releases#list-releases).

## Image smoke gate

`scripts/smoke-image.sh` builds the release image and exercises it before anything is published. It checks the pinned Chromium and tini packages, their licenses, and `NOTICE`. It then starts a disposable Postgres and the server with mounted secret files, and checks startup, the database role, UID 65532, health, and headers. The script's dev mode is only for authenticated upload and quote calls. Live OIDC is not part of the gate, because the database is disposable and has no identity provider.

The gate needs Docker. It is a release check, not the day-to-day `just test` run.

## Release assets

| Asset | Where |
| --- | --- |
| Server image | `ghcr.io/inspr-at/aeon:<version>`, linux/amd64, provenance enabled |
| `aeon-cli-darwin-arm64`, `aeon-cli-darwin-amd64`, `aeon-cli-linux-amd64`, `aeon-cli-linux-arm64` | GitHub release for the `v` tag. Install the file as `aeon`; a symlink named `paimos` selects paimos mode. |
| `paimos-agentd-darwin-arm64`, `paimos-agentd-darwin-amd64`, `paimos-agentd-linux-arm64`, `paimos-agentd-linux-amd64` | Same GitHub release. Darwin binaries link LocalAuthentication. Linux binaries are static. The current Nix package is named `aeon-agentd` and builds `bin/aeon-agentd`. Both names come from `cmd/aeon-agentd`. |
| `SHA256SUMS` | Same GitHub release, covering the CLI and agentd files above. Check it with `sha256sum -c` or `shasum -a 256 -c` before installing. |
| Flake | `flake.nix` in this repository. `packages.<system>.aeon` is the CLI plus a `paimos` symlink. `packages.<system>.aeon-agentd` is the supervisor. The version is the `version` field of `version.json`. |

Nix install of the CLI:

```
nix profile install github:inspr-at/aeon#aeon
```

The flake reference keeps resolving after the repository is renamed to `inspr-at/paimos`, because GitHub redirects the old name.

The four binaries are cross-built, not a claim that all user-service lifecycles work. `.github/workflows/pairing-platform.yml` runs an isolated fake-executable launchd or systemd-user fixture on `macos-15` (arm64), `macos-15-intel` (amd64), `ubuntu-24.04-arm` (arm64), and `ubuntu-24.04` (amd64). A platform is qualified only after that runner's real service install, status, drain, and removal check passes on the integrated commit. Other macOS releases and Linux distributions have no lifecycle evidence from this matrix.

Published coordinates are immutable. The existing stable86 release `v260927181849.0.0` predates the fourth daemon target: its `paimos-agentd-darwin-arm64` asset answered HTTP 200 and its `paimos-agentd-linux-arm64` asset answered HTTP 404 in read-only HEAD checks on 2026-09-27. A guide serving that version must omit Linux arm64 rather than point at a future asset or rewrite stable86.

Screenshot data for a dev tenant is `aeon demo seed`. See [DEMO.md](DEMO.md). That command is not part of the release tag workflow.

## Ticket benefits and release-note snapshots (AEON-256)

Tickets store `pill_en`, `pill_de`, `benefit_en`, `benefit_de` and
`hide_from_release_notes` in `nodes.fields`. Migration `0896` adds their optional
schema properties for every tenant and replaces `aeon_seed_node_kinds` for new
tenants. It changes no node values, translations, events or release artifacts.
Custom unrelated properties and constraints stay in place. Schema requirements
are deliberately optional so incomplete drafts can be created with warnings.

The generic node API requires both pills (2–4 whitespace-separated words) and
both nonblank benefits when a ticket enters a built-in completed state (`done`,
`accepted` or `delivered`) from outside that set, including creation in any of
those states, direct PATCH, bulk changes, CLI calls and bulk undo. Normal updates
check the final fields and state while holding the node row lock. Bulk skips an
incomplete ticket with a reason; bulk undo rejects the entire invalid reversal.
An already-completed ticket remains editable, including transitions within that
set, without fabricated backfills; reopening and completing it again invokes the
requirement. Hiding a ticket is not an exception. Sentence count, positive plain
language and translation fidelity are
editorial requirements, not claimed as machine-verified. This is an application
transition rule, not a SQL constraint: historical import/migration writers retain
their existing behavior. Cancellation, archival and tenant-defined state names
are not guessed to mean successful completion.

For a new release, read the supported authenticated endpoint
`GET /api/projects/{projectId}/releases/{releaseId}/note-snapshot` using a
project-scoped `releases.read` and `nodes.read` key (or an authorized person). The tenant comes
from authentication, not an input parameter. The export uses one SQL statement:
`journey_tickets.release_node_id` is the sole membership source, joined by tenant
and project; `nodes.fields` supplies exactly the five benefit properties.
Backlog tickets, Git mentions and a project's other releases are not membership.
Deleted/unavailable public members remain explicit gap entries. `captured_at`,
release revision and each member's `updated_at` record the observation. No live API or
classic database is contacted by the history builder.

At reservation, the release coordinator reviews that export and freezes its
public projection in `internal/releasehistory/data/product-notes.json` **before
tagging** (AEON-372):

```sh
go run ./internal/releasehistory/packnotes -repo . -snapshot SNAPSHOT.json -reserve VERSION -tenant TENANT_UUID -project AEON_PROJECT_UUID
```

`VERSION` must match `version.json`. Both UUIDs bind the source to the selected
PPM tenant and AEON project. This explicit reserve step may freeze an unpublished
preview; historical snapshot imports must already be frozen. Historic ticket
exports use the separate explicit workflow below. Use the configured API
client with `releases.read`/`nodes.read` to obtain the export. Keep the raw export
in its authorized local context: it may contain hidden text and tenant IDs and
must not be committed as the public projection. The generated file contains
only public ticket keys, pills, benefits, captured groups and capture provenance.
Review and commit it with the reservation. Identical reruns are idempotent
only with that same export file: a fresh export has a new `captured_at` and
conflicts. Restore the file before re-reserving; do not export the preview again.
Conflicting entries fail instead of rewriting a reserved version.

Every release also has a codename (AEON-430): an alliterative science-fiction
name such as "Cool Chip" or "Solar Star", a pure function of
`release_sequence` from the frozen, append-only lists in
`internal/releasehistory/codename/words.txt`. The letter steps through a
fixed cycle of the 15 letters rich enough for thousands of good names
(A B C D E F G H I L M P R S T), so release 1 is A and neighbouring releases
start differently. Within a letter short names come first, and no name
repeats before sequence 43,913 (50,419 names in version 1). The reserve step above writes it into `version.json` as
`"codename"`, right after `release_sequence`; without a snapshot, run
`just release-codename` (`go run ./internal/releasehistory/codename/stamp -repo .`)
once `release_sequence` is set. Both are idempotent and refuse a name that
differs from the sequence's. The codename is presentation only: the version
stays the identity, and every earlier release has its name from the same
function. To change the lists, append a new `version N from S` block with `S`
above every reserved sequence; never edit, reorder or delete a line, so no
existing name moves. Two-word collisions with obscure titles are an accepted
residual risk. A reported collision is added to the pair deny list in the next
list version; names of already-published releases never change.

For historical backfill where snapshots exist, export stored snapshots as `VERSION.json` in
one directory and run the same command with `-snapshots DIRECTORY -tenant
TENANT_UUID -project AEON_PROJECT_UUID`. A run with no exports imports only
authoritative snapshots already in local tags and reports missing versions.
Alternatively, save an authorized PPM `GET /api/releases` response, record that
workspace's `tenant_id` and `project_node_id` on the saved file, and pass
`-history HISTORY.json -tenant TENANT_UUID -project AEON_PROJECT_UUID`. Prefer
`-history` over `-snapshots` for backfill. A snapshot file from before group
storage has no group, and this snapshot import cannot derive one offline; those embedded
items then take a group from the viewing tenant's own tickets, which usually
means Features. `-history` records the group the serving workspace already
derived. The two identifiers must match the flags; a file for another workspace
is rejected. This consumes only `database-snapshot` or immutable tag-snapshot
notes, never `changes.linked_tickets`. It records each ticket's group, including
a group the server derived from the live classification when the snapshot itself
had none. The explicit historic-ticket workflow below is the only import of current
ticket fields for missing snapshots; release PR bodies and pills.tsv are not sources.
Existing tags and artifacts stay unchanged; the new
binary carries the backfill. Migration `0997` captures future groups alongside
the five note fields. Older snapshots have no group. Serving those classifies
every commit ticket from the current classification, not only the tickets the
capture tells: a hidden bug, a ticket with no pill or benefit, and a commit
ticket that was not a release member. A bug is a fix and a visible benefit is a
feature, the same rule as AEON-289. Only those two facts are read. The captured
pill and benefit stay frozen, and live pill or benefit text never enters
`linked_tickets`. The history export above records that group, so other
workspaces see Fixes from the embedded notes.

A release with no capture uses that same classification for `changes[].group`.
It loses live-text Highlights by design: the no-live-text rule keeps pill and
benefit text out of the response, so Highlights has nothing to show until a
capture exists. Compare still follows the classified group.

A capture that already stores a group keeps that group and its frozen text.
Commit tickets the capture does not name — a hidden member, a member with no
pill or benefit, and a ticket that was not a release member — still take
`changes[].group` from those same two live facts. The note on that
classification stays empty, so live pill and benefit text never reaches
`linked_tickets` or Highlights. Tickets the capture already grouped are not
read again. A shared commit still takes the strongest group. Compare follows
that group.

`internal/releasehistory/generate` also reads legacy `release-notes/VERSION.json`
files **from their matching annotated Git tags**, including under `-offline`.
Later ticket edits cannot change those notes. The generated manifest records the exact
file SHA-256, tag/path, capture time, revision, both languages, hidden count and
gaps. Portable notes are the additive `notes.public_items`, with no tenant UUIDs;
the existing `notes.items` schema stays unchanged. Members sort by recorded position, then key and ID. Exact duplicate IDs
collapse; conflicting duplicates, duplicate keys, malformed metadata and a
recorded version that differs from the tag fail the build. If the release had no
assigned version at capture time (candidate registration can happen later), the
file's tagged path is the explicit coordinator-supplied build binding; that
missing recorded version stays a visible provenance gap. It is not inferred
from a ticket, timestamp or Git headline.

Missing snapshot files produce empty notes with a membership/field-data gap.
Incomplete visible tickets produce field-specific gaps; they get no invented
translation or Git-headline benefit. Hidden tickets contribute no text or key to
the public notes; they contribute only to the hidden count, even when benefit
fields are incomplete or the member was unavailable at capture.
Technical Git headlines, legacy top-level `tickets` references and changes remain
evidence; membership claims come only from the snapshot. The release detail shows
each captured ticket as one block under Features or Fixes (pill as heading, key,
benefit sentence, commits folded), exactly as it shows linked tickets of a release
without a snapshot; the tag message appears only under Evidence. Old v1 manifests without the optional
`notes` member and regenerated records with `notes.source = "unavailable"` keep
their ticket references, ticket filter and commits, but their tag message is not
shown as a title; their Git text is not presented as benefit notes or membership.
Available snapshots remain authoritative even when empty or incomplete. This
additive reader boundary does not modify any existing published artifact or
legacy tag.

Integration acceptance still belongs to the coordinator: review migration0896,
select and approve the production release/project mapping, capture/review/commit
an authorized snapshot before the next tag, verify its digest in the resulting
artifact, and live-test both languages. Automatic production snapshot capture,
server-side signed/sealed snapshots, GitHub release-body rendering, translation
backfills and company-rule publication are not implemented here. An omitted
snapshot is intentionally a visible gap, not a successful benefit-note release.
The writing-rule proposal is `docs/proposals/ticket-benefit-writing.json`, using
AR1's draft Rule DTO. It must be imported as a draft at the fetched revision and
published separately by an authorized human; it changes no effective harness
files or company rules.

### Historic product notes without journey membership (AEON-398)

Historic AEON tickets often have no journey release assignment. For published
releases without a capture, use the same Git tag history and union of release
and commit ticket keys as the history builder. This is explicitly later
`release-manifest-tickets` evidence, not original journey membership. No
database write, migration, tag rewrite or deployment is involved.

The history also recognizes historical lightweight release tags when their
committed `version.json` matches the tag and the coordinate is not an unpublished
reservation. Their channel and sequence come from that file, and their release
time comes from the tagged commit's committer date. Keep published tags unchanged;
new releases require annotated tags as described above.

From a full checkout with release tags, use a configured PPM agent client (or
wrapper) with `nodes.read`. It must select the approved PPM tenant; no credential
is passed on the command line. Both source UUIDs are explicit:

```sh
go run ./internal/releasehistory/exporthistoric -repo . -client /path/to/ppm-client -tenant TENANT_UUID -project AEON_PROJECT_UUID -out tmp/historic-notes.json
# Review the ignored local export, then freeze only its public projection:
go run ./internal/releasehistory/packnotes -repo . -historic tmp/historic-notes.json -tenant TENANT_UUID -project AEON_PROJECT_UUID
```

The exporter verifies the authenticated tenant, resolves each node's kind and
project ancestry, and reads the four bilingual note fields, type, tags and hide
flag. The output must be ignored and inside this checkout; an existing file is
never overwritten. Keep it local: hidden text is present for the projection
check and must never be committed. API failures, missing tickets, wrong source
bindings, duplicate keys and invalid hide flags fail the import rather than
publishing a partial history. A later release can repeat these two commands with
a new export filename; already captured versions remain unchanged.

`packnotes -historic` reuses `ParseTicketMeta` and the live linked-note projection
for Features/Fixes, including bug kinds, types and tags. Hidden notes are omitted;
tickets with no note text stay under Other. Missing translations stay empty.
The public bundle contains only ticket keys, existing note text, groups and
capture provenance. Its digest covers the version, manifest membership source,
capture time and selected ticket observations; `written_after_release` is true.
Only published versions without captures are added; reservations and existing
snapshots are skipped. Review and commit `internal/releasehistory/data/product-notes.json`
with the code. Export counts distinguish releases and ticket occurrences across
releases (classified, Other and hidden); one ticket can occur in several releases.

Regression: `TestHistoricNotesNonPPMTenant` builds historic Git membership,
imports the export and serves it without tenant ticket data. The Playwright
`release-historic-tenant.spec.ts` consumes that HTTP output and exercises both
groups, the release filters and Highlights/Details at desktop and phone widths.
`GET /api/releases` and `GET /api/releases/{version}` support agent keys with
`releases.read`; presentation writes remain person-only.

## Historical note backfill (AEON-290)

After migration `0939`, the deployed binary supports an offline maintenance
command against its configured database (no Git checkout, API login or network
history lookup). Both dry-run and apply require an explicit **active person**
with workspace `roles.manage` authority. Agents and inactive people are denied;
there is no implicit operator. Tenant and project visibility come from the
person's live bindings. The command does not run migrations.

```sh
aeon release-notes backfill --tenant inspr --project AEON --actor-principal-id PERSON_UUID --all-missing
aeon release-notes backfill --tenant inspr --project AEON --actor-principal-id PERSON_UUID --release VERSION --apply
```

The container entry point is `/paimos release-notes backfill` with the same
flags. Omit `--apply` for a read-only plan. `--release` and `--all-missing` are
mutually exclusive; omitting both means all missing snapshots in that project.
The JSON report lists version, tickets found, notes count and hidden count for
each planned/inserted capture, plus `excluded_keys` (resolved manifest tickets
that are not completed) and `gap_keys` (captured public tickets with incomplete
fields or unavailable members), plus unchanged and skipped counts. Hidden
tickets never contribute gap keys.

For historical tags, membership is the union of ticket keys in the embedded
manifest's release and listed commits, resolved only within the selected tenant
and project. Only completed tickets (`done`, `accepted`, `delivered`) are
captured; other states are excluded and reported. This is an explicit
approximation from Git evidence, recorded as
`membership_source: release-manifest-tickets`; it is not original journey
membership. Unresolved keys are not treated as tickets. The current five benefit
fields and their update times are captured once, including hidden tickets for
provenance. Public notes contain only the hidden count, never their benefit text
or missing-field warnings; this is decided when reading the frozen snapshot.
`released_at` uses the manifest publication time, or its tag time when publication
time is absent; neither is replaced with the capture time. Captures without an
original release time are skipped. Reservations are never captured.

The version-keyed table is tenant/project protected and insert-only, with the
same immutability trigger as native snapshots. Each insert records the person,
`backfilled: true`, `label: backfilled`, capture time and one audit event. Apply
is atomic across both paths. Reruns leave existing rows unchanged. Native journey
releases still use their own membership and snapshot store, through the same
person/admin authorization and project/version selectors.

`GET /api/releases` and its detail route use stored journey snapshots first,
then explicit version-keyed backfills for the visible AEON project, then the
embedded tag/public product notes. Tenants without that project use the public
product notes too. Empty or hidden-only tenant captures remain authoritative.
Without any capture, the historical evidence remains. The overlay is computed per request and never
changes the shared embedded manifest. A malformed database snapshot is logged
without its payload and falls back for that release alone; other releases remain
available. Releases with no public notes or gaps show a quiet **Internal changes
only** in the detail (or **No notes for this release** without any commit); their
tag message is evidence only. Backfilled notes
show **Notes written after release**. No original tag, artifact, published
timestamp, or existing snapshot is rewritten. The coordinator owns production dry-run review and apply.

## Release presentation (AEON-305)

Every release introduces itself with a short **theme** (the kicker, 1–80
characters, one line), one **headline** sentence (up to 200 characters, one line)
and a short **intro** (two or three sentences, up to 600 characters), each in
English and German. The release detail shows them as a compact header above the
blocks every release shows: Features and Fixes with one block per ticket (the
pill as heading, the key, the benefit sentence and its commits folded), then
Other changes. A captured or backfilled snapshot decides which tickets are told
and their text. Without a capture, Aeon does not fill Highlights from live
ticket text: that is the no-live-text rule. Compare still uses the served change
group. A ticket is a fix when it is a bug (the served change group), else when
its commits are only `fix:`; otherwise a feature. The release list shows version,
date and theme, or the pills. A release without a presentation has no header.
The Git tag message is evidence, never a title; "Notes written after release"
is one muted line.

Presentations live in `release_presentations` (migration `0945`), keyed by
tenant, product project and calendar version, protected by tenant RLS and project
visibility, separate from tags, manifests and note snapshots. The version need not
be in the running build yet, so the presentation can be written before the new
build is live. English theme and headline are required; German fields may be
empty and readers then show English. Writing identical text changes nothing.
Every change records one `release.presentation_set` event (before and after);
removing one records `release.presentation_cleared`.

**The release agent must write the presentation for every new release**, in both
languages, as part of the release, right after the notes snapshot is final and
before announcing the release. It is written against the production database from
the deployed container, like the backfill, and is a dry run until `--apply`:

```sh
/paimos release-notes present --tenant inspr --project AEON --actor-principal-id PERSON_UUID \
  --release VERSION \
  --theme "Releases with a name" --theme-de "Releases mit Namen" \
  --headline "Every release says what it is about." --headline-de "Jedes Release sagt, worum es geht." \
  --intro "Two or three sentences." --intro-de "Zwei oder drei Sätze." \
  --apply
```

or with the six fields as a JSON file (`-` reads stdin; use `docker exec -i`),
which avoids shell quoting:

```sh
/paimos release-notes present --tenant inspr --project AEON --actor-principal-id PERSON_UUID \
  --release VERSION --file - --apply < presentation.json
# {"theme_en":"…","theme_de":"…","headline_en":"…","headline_de":"…","intro_en":"…","intro_de":"…"}
```

`--clear` removes a presentation. Outside the container the binary is `aeon`
with the same arguments. The actor is an active **person** with `releases.deploy`
on the project (workspace admins and owners); offline there is no agent key, so
the release agent names its operator, as for the backfill. The command prints a
JSON report with `applied`, `tenant_id` and `change` (`version`, `changed`,
`before`, `after`). People can also use `PUT` and `DELETE
/api/releases/{version}/presentation` (same authority; optional
`expected_revision` answers 409 when stale).

Write for the reader, not the repository: the theme names what the release is
about in a few words, the headline says what changes for them in one sentence, the
intro adds context in two or three. No ticket keys, package names or commit
jargon; those stay in the rows and the commits.
