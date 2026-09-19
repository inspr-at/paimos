# Contributing to PAIMOS

Thanks for considering a contribution! PAIMOS is AGPL-3.0-only, developed
openly, and happy to take pull requests from anyone.

## Before you start

- **Bugs**: check existing issues, then open a new one with repro
  steps. A failing test case in your PR is 10× more valuable than a
  description.
- **Features**: open an issue or discussion first so we can agree on
  direction before you invest time. The bar for new surface area is
  higher than for bug fixes and internal-quality improvements.
- **Security**: do **not** open public issues. See
  [`SECURITY.md`](SECURITY.md) for the reporting process.

## Development setup

### Requirements

- Go 1.23+
- Node.js 22+
- Docker (for local smoke tests)
- `devenv` (recommended; provides a pinned Go + Node toolchain)

### First run

```bash
git clone https://github.com/inspr-at/paimos.git
cd paimos

# with devenv
devenv shell -- bash -c "cd backend && DATA_DIR=../data STATIC_DIR=../frontend/dist go run ."

# separate terminal
devenv shell -- bash -c "cd frontend && npm install && npm run dev"
```

Frontend dev server: <http://localhost:5173>; API: <http://localhost:8888>.
Vite proxies `/api/*` to the Go backend.

First run: set `ADMIN_PASSWORD` before starting the backend to seed the
initial admin user.

## Running tests

```bash
# backend
cd backend && go test ./...

# frontend
cd frontend && npm test
```

## Code style

- **Go**: `gofmt`, `goimports`. CI will block unformatted diffs.
- **TypeScript / Vue**: strict TS. Run `npm run typecheck` before
  submitting. Vue SFCs use `<script setup lang="ts">`.
- **Commit messages**: conventional-ish prefixes
  (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:`). Subject line
  ≤72 chars. Body paragraphs explain *why*, not *what*.
- **Comments**: only when the *why* is non-obvious. Don't restate what
  well-named code already says.
- **Public API surface**: `/api/openapi.json` is the *public, stable
  scriptable contract* — a deliberately curated subset of the canonical
  resource surface (PAI-294). If you add or change a route meant for the
  SPA, the `paimos` CLI, or external integrations, document it in
  `backend/handlers/openapi.json` in the same PR. Internal/admin one-off
  endpoints (imports, dev tooling, branding, SSO/TOTP, AI ops, portal
  internals) are intentionally omitted. The `TestOpenAPIContractRoutesExist`
  guard fails CI if the spec references a route that no longer exists.

## Developer Certificate of Origin

New contributions use the unmodified [Developer Certificate of Origin 1.1](DCO.md).
A `Signed-off-by: Name <email>` trailer records that you have the right to submit
the contribution under the project licence. It is not a cryptographic signature
or a guarantee of correctness. Use the same identity as the commit author;
a GitHub-associated noreply address is fine. Sign-offs remain in public history.
This applies to maintainers and outside contributors alike, from adoption onward;
existing history is not rewritten. After reading the DCO, create each new commit
with `git commit -s` using your own name and GitHub-associated email.

Fork the repository, create a branch from the current upstream `main`, implement
and test your change, then push to your fork and open a pull request to `main`.
Describe the change, its purpose, tests, and any limitations. Contributors need
no write access to this repository. The maintainer reviews agent findings and
decides whether to merge; passing checks never grants an agent merge authority.

The required `dco` check validates every commit introduced by a PR, including
merge commits on the contributor branch. An empty or incomplete range fails.
Local checks require Python 3 and full Git history; deepen a shallow checkout
with `git fetch --unshallow` first. When merging upstream updates into your
branch, use `git merge --signoff upstream/main` after fetching upstream.
It reads real Git trailers, so a sign-off quoted in prose does not count.
Missing sign-offs must be supplied by the contributor, not invented by a reviewer
or agent. Do not rewrite shared history to repair them without explicit agreement.

Bots are not exempt. Dependabot's native `Signed-off-by` service address is
accepted for its exact GitHub author identity; other bots use their own matching
author/sign-off identity. This checks declarations, not account authenticity.
For agent-assisted work, the human contributor must understand and authorize
their DCO declaration; the agent must not invent identities or sign for others.

GitHub web commits require sign-off. For squash merges, retain the original
commit messages and move their existing sign-off declarations into the final
trailer block; an indented or quoted sign-off is not a trailer. Check that the
final author still has a matching declaration. Never invent a contributor's
sign-off. Use a regular merge when combining authors would obscure provenance.
Release and deployment remain maintainer-controlled. Existing review and CI
requirements still apply; DCO introduces no second-maintainer requirement.

Enable the repository pre-commit hook once with `git config core.hooksPath .githooks`.

## Pull request flow

1. Fork the repo and branch from `main` (`feat/…`, `fix/…`, `docs/…`).
2. Write the change + tests. Keep the diff focused — one concern per
   PR.
3. Ensure `go test ./...`, `npm run typecheck`, `npm test`, and
   `npm run build` all pass locally.
4. Commit with DCO sign-off (`git commit -s`).
5. Push and open a PR. Describe **what changed and why** in the body;
   link the issue.

PR normal tests omit individual tests selected by the affected race plan.
Direct packages keep their package-specific race plans; dependency-only
packages race their named concurrency, replay, and recovery contracts.
Handler security invariants run in the affected lanes; the separate invariant
job runs only the unsupported-platform contracts. Race-dependent packages and
their importers retain normal tests, and new guards fail the gate self-test.
Quality self-tests and dev-login checks follow their changed inputs (clock-based
checks always run); main publication reuses PR quality/frontend/E2E assurance,
and reruns live-database security scans (govulncheck and npm audit). Full backend
execution runs nightly and before release tags; tag publication checks completed
exact-code evidence without holding a polling runner.

### Protected `main` and break glass

The active GitHub ruleset for `main` requires a pull request and successful
`dco`, `test`, `e2e`, and `security-scan` checks. Force pushes and branch
deletion are blocked. Zero approving reviews are required because PAIMOS is
currently maintained by one person; the pull request and hosted gates provide
the durable review trail without pretending that an author can independently
approve their own change. Only after the maintainer explicitly approves the
reviewed PR may an agent merge or enable auto-merge; let the gates complete
before merging. The live policy is publicly inspectable as
[ruleset 20708526](https://github.com/inspr-at/paimos/rules/20708526).

Repository administrators have a **pull-request-only** bypass for break-glass
recovery. It is reserved for an incident or a broken required-check mechanism,
not ordinary delivery. The administrator must still open a PR, explicitly
choose GitHub's bypass action, record the reason in that PR and the relevant
PAI issue, and repair or restore the normal gate in the same incident. Direct
pushes to `main` are not a break-glass path.
The additional DCO contribution ruleset has no bypass: the legacy ruleset
exception cannot skip `dco` or `dco-tests`. A broken DCO gate requires an
explicitly authorized, recorded ruleset repair; it is never an automatic bypass.

The `dco` job enforces the contribution policy above. Preserving source commit
messages alone does not guarantee a valid final squash trailer; verify it as
described above.

## What makes a PR easier to review

- Small and focused. A 50-line PR gets reviewed same-day; a 5,000-line
  PR waits.
- Self-contained. No drive-by renames of unrelated files.
- Justified. If it's a trade-off, name the trade-off.
- Tested. New code path → new test. Bug fix → test that would have
  failed pre-fix.

## What we're likely to push back on

- Features that add surface area without clear user demand
- Dependencies on services beyond SQLite + optional MinIO + optional
  SMTP (PAIMOS's "zero-dep by default" stance is load-bearing)
- UI changes that break keyboard-first flows
- Backwards-incompatible API or DB-schema changes without a migration
  path
- Contributions that require giving up the AGPL-3.0-only license (no
  re-licensing without a real CLA, which we don't have)

## Issue labels

- `good-first-issue` — small, well-scoped, good to cut your teeth on
- `help-wanted` — bigger than trivial, would welcome outside help
- `bug` / `feature` / `docs` / `security`
- `needs-discussion` — design direction unclear; comment before coding

## Getting help

Open a discussion on GitHub or file an issue tagged `question`. Keep it
here in the open — DMs don't scale.
