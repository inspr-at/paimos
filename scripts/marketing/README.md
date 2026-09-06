# scripts/marketing/

Reproducible marketing captures for the public sites (seeded in PAI-746,
orchestrated end-to-end by PAI-695). Replaces the hand-driven steps in the
ppm runbook `refresh-site-marketing-captures` (#3594).

## Rules these scripts enforce

- Captures come **only** from the local seeded demo workspace. Never from
  `pm.barta.cm`, `paimos.agm.ng`, or any instance holding real data.
- Still viewport 1600×1000 @2x and video viewport 1280×800, with the dev-login
  banner and 2FA nag hidden and distracting animations/carets frozen.
- Every published caption states provenance: *"Demo workspace, Paimos
  v<release-version>"*. The scripts do not write captions — that lives with the site —
  but the version in them must match the build you captured.

## One-command use

```sh
just dev-up                    # terminal 1: backend :8888 + Vite :5173
just marketing-captures       # terminal 2: regenerate + verify + publish
```

The second command writes fixture data and the selected site working tree. It
bootstraps Playwright, sources the same local dev-login token as `dev-up` without printing it, polishes the gitignored fixture DB
through the dev binary's initialized SQLite connection,
captures all seven current site images plus two short product flows, transcodes
the flows to Safari-compatible fast-start H.264, and publishes everything into
`../inspr-at`. The seven stills include Habitat Home with its coordinator
setup action and the fixture-backed Agent Mode supervision surface, so releases that change
orchestrator setup, project selection, command hints, or delivery density
cannot leave the public product gallery behind.
Pass a different reviewed site worktree when needed:

```sh
just marketing-captures ../inspr-at-pai-695
```

For a separately booted synthetic stack, select its origins, data directory and
reviewed site working copy explicitly. API and app must use the same loopback
hostname so the development login cookie remains scoped correctly. Defaults
remain `localhost:8888`, `localhost:5173`, this repository's `data/`, and the
sibling site directory.

```sh
PAIMOS_CAPTURE_API_URL=http://127.0.0.1:59441 \
PAIMOS_CAPTURE_APP_URL=http://127.0.0.1:59442 \
PAIMOS_CAPTURE_DATA_DIR=/tmp/paimos-marketing/data \
./scripts/marketing/refresh-captures.sh --dry-run /tmp/reviewed-inspr-site
```

`--dry-run` validates and prints configuration only: no token reads, network,
DB writes, browser installation/launch, or site writes. After reviewing the
selected seeded stack and target, omit `--dry-run` to capture. The script does
not start or reconfigure servers. `PAIMOS_CAPTURE_SITE_DIR` also selects a site
when no positional site path is supplied. Keep existing previews and unrelated
site-branch changes out of the capture run.

The publisher is owned by the selected **inspr-site** working copy. Before a
refresh, confirm its inventory includes `ui-session-home.png` and its version
validation supports the shipped calendar version, including a same-day recut.
The Paimos wrapper cannot add a missing gallery asset to an older site publisher.

Configuration and Home selector regression checks need no server or vendor:
`node --test scripts/marketing/capture-config.test.cjs`.

The command refuses a live/non-local instance, an app version that does not
match `VERSION`, or backend/frontend code that differs from the corresponding
shipped tag. The site-side publisher checks 3200×2000 output, hashes every
asset, records the release commit, and verifies the three annotated hotspots
against DOM landmarks captured from `/issues/PAI-1`. It also rejects video
drift outside the 1280×800, 24 fps, 5–15 second, no-audio H.264 contract or the
3 MiB combined page-weight budget.

## Why the demo data needs polishing first

The raw dev fixtures are built for exercising filters, not for being
photographed. `demo-polish.sql` fixes what reads as broken on camera:

- fixture databases created before PAI-697 may still show `dev_admin` in the
  greeting and sidebar; the polish step converges them to the canonical
  synthetic Mara Ellis identity used by current seeds;
- seven ACME issues share the title "Reporting CSV export", so Recent
  Issues looks like a seeding bug.

`demo-intake-events.sql` seeds one finished Voice Intake session
(transcript, spec, summaries, ticket preview, impacts, project match) as
**session id 1**, used purely as a template.

## Why the intake capture is not a plain screenshot

The workbench resumes a session only from in-memory store state, so a cold
page load always renders the empty state — which is exactly what the
flagship 5.x surface must *not* show on the website. `capture-intake.cjs`
therefore creates a real session through the UI, **copies** the template
artifacts onto it, then SPA-navigates away and back so `onMounted`
re-hydrates from the database.

It copies rather than moves: moving empties the template and every
subsequent run silently captures an empty workbench.
