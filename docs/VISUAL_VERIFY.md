# Visual verification (screenshots of the dev UI)

For UI/display work, render the actual app instead of editing layout/CSS blind.
`just shot` drives the local dev UI with headless Chromium and writes a PNG, so
every visual change can come with a before/after frame.

## One-time setup

```sh
# 1. dev-login token (gitignored, local-only, dev_login builds only):
mkdir -p ~/Secrets/dev
printf 'PAIMOS_DEV_LOGIN_TOKEN=%s\n' "$(openssl rand -hex 32)" \
  > ~/Secrets/dev/PAIMOS_DEV_LOGIN_TOKEN.env
chmod 600 ~/Secrets/dev/PAIMOS_DEV_LOGIN_TOKEN.env
```

Playwright + Chromium are bootstrapped automatically on first `just shot` into
`scripts/.visual-tooling/` (gitignored). This is **not** a frontend
dependency — keeping the browser download out of every CI `npm ci`.

## Use

```sh
just dev-up                       # terminal 1: backend :8888 + vite :5173 (dev-login)

just shot                         # terminal 2: first seeded project's issue list → /tmp/paimos-shot.png
just shot /issues                 # a specific route
just shot /sprint-board /tmp/sb.png   # route + output path
```

Routes are the SPA paths (`/`, `/issues`, `/projects/:id`, `/sprint-board`,
`/customers`, …). The script dev-logs-in as `dev_admin` over the vite `/api`
proxy, navigates, waits for async lists to settle, and screenshots at
1440×900 @2x.

## Knobs (env)

| var | default | meaning |
|---|---|---|
| `PAIMOS_DEV_URL` | `http://localhost:5173` | dev frontend origin |
| `PAIMOS_DEV_USER` | `dev_admin` | dev-login user (also `dev_member`, `dev_external`, …) |
| `PAIMOS_DEV_LOGIN_TOKEN` | from token file | overrides the token file |

## Regression baseline

PAI-673 adds an opt-in Playwright screenshot baseline for the daily-work
surfaces that most often regress: project issue list, issue detail / AI
Workbench, settings, customer detail, and the customer portal dashboard. Each
surface is captured at desktop and narrow widths.

```sh
just visual-baseline                    # compare against committed baselines
just visual-baseline --update-snapshots # refresh baselines after intended UI changes
```

`just visual-baseline` boots the same throwaway dev-login stack as
`scripts/e2e.sh`, generates local fixture passwords at runtime, and needs no
production secrets. It always overrides any inherited `DATA_DIR` with a fresh
throwaway database, so snapshots cannot depend on a developer's accumulated
local issues or profile edits. Internal admin captures use the `debug-admin`
fixture so 2FA reminder chrome does not pollute the baseline. The test freezes
the browser clock so relative timestamps do not churn the PNGs. Baselines live in
`frontend/e2e/__screenshots__/`; review changed images in git before committing
an update.

## Agent Mode candidate verification

Agent Mode's deterministic Playwright gate runs against the exact production
bundle rather than the Vite development server:

```sh
cd frontend
npm run build
PAI805_SELF_HOST_DIST=1 \
PAI805_SHOT_DIR=/tmp/pai811-shots \
  npx playwright test e2e/agent-mode-visual.spec.ts --project=chromium
```

Use stable, viewport-oriented filenames such as `desktop-1.png`,
`desktop-10.png`, `desktop-100.png`, and `phone-390.png`; do not encode review
sequence (`fixed`, `final`, and similar) in evidence names. Generated taste
references and candidate screenshots belong in the governing PPM issue's
attachments, not in the repository.

For responsive claims, measure the exact widths named in the claim. The
PAI-811 matrix is 390, 520, 640, 736, 900, 1024, and 1440 CSS pixels, plus a
512×450 viewport at device scale 2 for the 200%-effective-zoom boundary. At
every applicable width, prove no horizontal document overflow and no painted
intersection among the canvas, cards, narration/control surface, and embedded
ticket panel. At 640 px and below, where the compact feed dock is hidden, also
prove the app-header live chip has a nonzero rectangle and nonempty Live or
Offline status text.

Do not use fixture text alone as evidence that essential selected-delivery
facts are readable. The responsive CSS must structurally disable ellipsis and
allow safe wrapping for the selected title, activity, actor, stage position,
and reported time. Browser assertions should additionally inspect computed
`white-space`, `overflow`, and `text-overflow`, then compare both horizontal
and vertical scroll/client geometry.

Reduced-motion verification must inspect every visible element and pseudo
element in the Agent Mode shell. Flag any nonzero animation and any nonzero
transition; an explicit exemption may cover only chromatic properties such as
`color`, `background-color`, and border colors. Exercise Detail 1, 10, and 100,
including representative phone, control-challenge, and embedded-ticket states.

These gates prove only the named local candidate and viewport/state matrix.
They do not prove human taste acceptance, protected CI, merge inclusion,
release artifacts, deployment, or live behavior. Record those distinct states
only in the governing PPM acceptance trail.

## Notes

- Functional/layout breakage and obvious visual bugs are catchable here;
  aesthetic "does it look right" is a human call — the PNG closes that loop.
- For interactive checks (click a button, fill a form, then capture), extend
  `scripts/visual-shot.cjs` — it's a plain Playwright script.
