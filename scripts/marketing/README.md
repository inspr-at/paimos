# scripts/marketing/

Reproducible marketing captures for the public sites (PAI-746; automation
was tracked as PAI-695). Replaces the hand-driven steps in the ppm runbook
`refresh-site-marketing-captures` (#3594).

## Rules these scripts enforce

- Captures come **only** from the local seeded demo workspace. Never from
  `pm.barta.cm`, `paimos.agm.ng`, or any instance holding real data.
- Viewport 1600×1000 @2x, dev-login banner and 2FA nag hidden, animations
  and carets frozen so re-shoots are stable.
- Every published caption states provenance: *"Demo workspace, Paimos
  vX.Y.Z"*. The scripts do not write captions — that lives with the site —
  but the version in them must match the build you captured.

## Use

```sh
just dev-up                      # backend :8888, vite :5173, fixtures seeded
source ~/Secrets/dev/PAIMOS_DEV_LOGIN_TOKEN.env

sqlite3 data/paimos.db < scripts/marketing/demo-polish.sql
sqlite3 data/paimos.db < scripts/marketing/demo-intake-events.sql

OUT_DIR=/tmp/captures \
NODE_PATH=scripts/.visual-tooling/node_modules \
  node scripts/marketing/capture-views.cjs      # dashboard, issues, board, search
OUT_DIR=/tmp/captures \
NODE_PATH=scripts/.visual-tooling/node_modules \
  node scripts/marketing/capture-intake.cjs     # the voice-intake workbench
```

`scripts/.visual-tooling/` is bootstrapped by `just shot` on first use.

## Why the demo data needs polishing first

The raw dev fixtures are built for exercising filters, not for being
photographed. `demo-polish.sql` fixes what reads as broken on camera:

- fixture users are named `dev_admin` / `debug-admin`, which show up in the
  greeting, the sidebar, avatars and every reporter column;
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
