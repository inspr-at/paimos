# PAIMOS Release + Deploy

Single-source-of-truth rule: **the git tag is the version.** Everything else
(`VERSION` file, `docs/CHANGELOG.md`, Docker tags, running containers) is
derived from or pinned to it.

> Bringing a fresh PAIMOS deployment online? Walk
> [`HARDENING.md`](HARDENING.md) before exposing it to users. This
> document covers release / rollback / image lifecycle; the hardening
> guide covers TLS / auth / files / audit / secrets / backups against
> the [`THREAT_MODEL.md`](THREAT_MODEL.md) invariants.

One production instance pulls from the registry:

| Instance | Host                  | Auth                | Storage           | Deploy mechanism |
| -------- | --------------------- | ------------------- | ----------------- | ---------------- |
| **ppm**  | `pm.barta.cm` (csb1)  | SSH key (`mba@100.64.0.4:2222`, Tailscale IP) | named volume | NixOS composeStack (since OPS-116) — **not** `deploy.sh` |

Registry: `ghcr.io/inspr-at/paimos`. Images produced per-commit on `main`
(`:latest`, `:sha-<short>`) and per release tag. Legacy SemVer tags retain
their `:X.Y.Z`, `:X.Y`, and `:X` aliases; calendar tags publish only their
exact `:yy.mm.dd[.hh.mm]` value and never mutable numeric aliases.
CI source of truth: [`.github/workflows/ci-v2.yml`](../.github/workflows/ci-v2.yml).

---

## The four steps

```
just release [patch|minor|major|x.y.z|yy.mm.dd[.hh.mm]] # protected PR → merge-commit tag → evidence
just verify-release <tag>                # verify signature + SBOM attestations + provenance before deploy
# deploy ppm via the composeStack path — see "Deploying ppm" below
just doc-sync [tag]                      # file a "doc/site sync follow-up" ticket in PAIMOS
```

Plus a read-only status helper:

```
just status                              # last 5 release tags + commits since last release tag
just wait-release-ci <tag>               # wait for tag workflows before verification/deploy
```

The standard sequence after a feature lands on `main` is **release →
verify-release → deploy → doc-sync**. The first three cut, verify, and roll
out the new build; `doc-sync` maintains a **single rolling PAIMOS ticket**,
appending a per-release section with a four-item checklist (README, `docs/`,
the canonical `../inspr-at` checkout for `inspr-at/inspr-site`, and
brand/screenshots) so the user-facing surfaces
don't drift out of sync with the code. It finds that ticket by the stable
`Doc/site sync follow-up` title prefix, is a no-op if the release is already
covered, and only files a new ticket when no open one exists. (Before
PAI-751 the lookup matched the full title, which embeds the tag — so reuse
was unreachable and every release filed a fresh ticket.) `release.sh` waits for the tag workflows (`ci` + `release`) before
printing the `verify-release` and `doc-sync` reminders as part of its "Next:"
output, so "image tag exists" is never confused with "release evidence is
complete."

## Deploying ppm (composeStack, since OPS-116)

csb1's containers are reconciled from the NixOS system closure by the
composeStack module — there is **no docker-compose.yml on the host** to
edit, so `just deploy-ppm` / `scripts/deploy.sh ppm` fail at "stop ppm"
by design. The image pin lives in the **nixcfg** repo at
`hosts/csb1/docker/compose-spec.nix` and must never float (OPS-116 QA
caught a would-be downgrade from a floating reconcile). The proven
sequence (v5.1.0 → v5.6.2):

1. `just release <version>` → protected release PR/merge → wait for tag CI → `just verify-release v<version>`.
2. **Volume backup on csb1** — throwaway alpine tars `csb1_ppm_data` to
   `/home/mba/paimos-backups/ppm/<utc-ts>/data.tar.gz`, plus a
   `manifest.yaml` naming pre- and target images.
3. **Bump the pin** in nixcfg `hosts/csb1/docker/compose-spec.nix` to
   `ghcr.io/inspr-at/paimos:<version>`. `main` is branch-protected: PR,
   checks, `gh pr merge --squash --auto`.
4. **On csb1**: `git pull` in `/home/mba/Code/nixcfg`, then
   `sudo nixos-rebuild switch --flake .#csb1` (mba has passwordless
   sudo; csb1's login shell is fish — pipe scripts to `bash -s`).
5. **Verify**: `docker ps` shows the new image; `curl
   https://pm.barta.cm/api/health` reports the exact version;
   `env -u PAIMOS_URL -u PAIMOS_API_KEY -u PPM_URL -u PPMAPIKEY paimos --instance ppm doctor`
   passes. The clean environment is load-bearing: an explicit named instance
   refuses ambient env-only URL/key targets rather than mixing credentials.

Rollback = restore the volume tarball **and** repin the previous image
in compose-spec.nix **and** rebuild — DB migrations are one-way, so the
tarball always travels with the image (see Rollback below).

The live operational copy of this procedure is the ppm knowledge-plane
runbook `ppm-deploy-composestack` (#4278).

## `just release`

1. Starts from current `main` (or the matching release branch when resuming).
   The only permitted initial dirty state is a reviewed, uncommitted
   `docs/CHANGELOG.md` entry for a non-interactive release.
2. If no argument: dumps commits since the last release tag (all + runtime-only) and
   exits. Look at the output, decide patch/minor/major, re-run.
3. Accepts `patch|minor|major` while the product remains on its legacy SemVer
   line, or an explicit calendar `yy.mm.dd[.hh.mm]` cut matching the actual
   Vienna day. The suffix is reserved for a same-day recut; `6.0.0` is rejected.
4. Creates deterministic `release/v<version>`, updates `VERSION`, refreshes the
   README badge and pinned install examples, and prepends a draft CHANGELOG
   entry pre-seeded from commit subjects. Interactive runs open `$EDITOR`;
   non-interactive runs require the reviewed entry before invocation.
5. Runs `scripts/check-release-hygiene.sh`: README badge must match `VERSION`,
   README's health example must stay generic (`<VERSION>`), and
   `docs/CHANGELOG.md` must not contain the auto-generated TODO stub.
6. Commits with DCO sign-off (`release: v<version>`), pushes only the release
   branch, and opens or reuses one PR against protected `main`.
7. Enables squash auto-merge, waits for the required hosted checks, and tags
   the exact PR merge commit after proving it is on `origin/main` and changes
   only the four release files. There is no direct-main push or ruleset bypass.
8. Polls ghcr for up to 10 minutes until the new image tag is visible, then
   waits for the tag-push GitHub Actions workflows (`ci` + `release`) to
   succeed before printing the next-step deploy commands. If a workflow fails,
   release exits non-zero and points at the failed run; do not deploy that tag.

Retries are explicit checkpoints, not a second release: the same version
reuses a matching open PR, finishes a merged-but-untagged PR, or accepts an
existing tag only when it resolves to that PR's merge commit. Any release-file,
branch, PR target, merge-commit, or tag drift is rejected. A behind branch is
merged with current `origin/main` locally using a merge commit carrying the
author's DCO sign-off, so the required checks can rerun without an unsigned
GitHub-generated update.

An exceptional merged PR with missing GitHub auto-merge provenance is not a
manual-tag invitation. It can resume only after a separate reviewed main-branch
change adds the exact receipt under `scripts/release/recovery/`; the script then
revalidates live PR identity, approved-head required checks, squash parent/tree,
main ancestry, and tag absence before creating the tag. The ordinary protected
auto-merge path is unchanged.

**Picking the level (what the AI looks at):** if `git log vLAST..HEAD` contains
commits that touch files under `backend/` or `frontend/src/`, lean **minor**.
Breaking API or schema changes → **major**. Pure docs / brand / scripts →
**patch**. The `release.sh` no-arg output breaks this down for you.

## `just verify-release <tag>`

Run this after the tag workflows have completed and before deploying a
release tag. `just release` does that wait automatically; if you are resuming
or checking a tag cut elsewhere, run `just wait-release-ci v<version>` first:

```
just verify-release v<version>
```

It wraps `scripts/verify-release.sh` and checks:

1. cosign image signature against GitHub Actions OIDC identity.
2. CycloneDX SBOM attestations on the image.
3. GitHub provenance attestation for the OCI image.
4. The public claim matrix release gate.

Local prerequisites: `cosign`, `gh`, and `jq`.

This applies to release tags. Untagged `sha-*` canaries are CI images, not
fully published releases, so they do not have the same release-evidence
surface.

## `scripts/deploy.sh` (generic compose instances — **not ppm**)

`deploy.sh` remains the mechanism for instances that run from a plain
`docker-compose.yml` on the host. ppm stopped being one at OPS-116;
running `just deploy-ppm` against it fails at "stop ppm" because no
compose file exists there. Keep this section for any future
compose-based instance.

Deploy targets are explicit by default:

| Target form | Meaning |
| ----------- | ------- |
| `v2.4.8` / `2.4.8` or `v26.08.31` / `26.08.31` | Exact release image. Use after `just release`. |
| `sha-4808a9f` | Immutable image for a pushed `main` commit. Use for untagged canaries. |
| `current` | Resolves to `sha-$(git rev-parse --short HEAD)`. Convenience for local HEAD. |
| omitted | Allowed only when local `HEAD` is not ahead of the latest release tag. If `HEAD` has untagged commits, deploy aborts before any remote change. |

Use `just deploy-ppm-preflight <target>` or
`scripts/deploy.sh ppm <target> --preflight` to resolve the target, verify
the image exists, and inspect the remote current image without stopping the
service.

For each instance:

1. Resolves the target. Aborts if omitted target is ambiguous or if the image
   isn't on ghcr yet.
2. SSH pre-flight: reads current image + image digest from the running
   container, aborts if target == current.
3. `docker compose stop <service>`.
4. Backup:
   - **bind storage** → throwaway `alpine` container tarring the bind path.
   - **volume storage** → throwaway `alpine` container tarring the volume.
5. Validate: `gzip -t`, count archive entries, verify the DB file is
   present.
6. Write a `manifest.yaml` next to the tarball (pre-image, pre-image-id,
   target-image, paths).
7. `sed` the compose image pin from old → new tag, `docker compose pull`,
   `up -d`.
8. Verify the restarted container reports the requested image.
9. Tail logs for 5 seconds (surfaces migration output + "server starting").
10. External `curl /api/health` from your laptop, up to 24s of retries.
    Full supported release targets must report that exact `version`; `sha-*` targets
    must report a `version` containing the deployed short SHA.

**On any failure in steps 2–10**, the script prints the exact rollback
command for the host and exits non-zero. It does **not** auto-rollback.

Artifacts produced on the remote host:

```
$BACKUP_ROOT/<UTC-timestamp>/
  data.tar.gz                  # authoritative rollback state
  docker-compose.yml.pre       # compose file from before the deploy
  manifest.yaml                # pre/post images, ids, paths
$COMPOSE_DIR/
  docker-compose.yml.bak.<ts>  # compose file before the sed edit
```

## Per-instance config

Each instance has a small conf file in `scripts/`:

- `scripts/deploy.ppm.conf` — ssh target, compose dir, service,
  volume name, DB filename, backup root, instance URL.

If you spin up a second instance, copy this file and change the values.

**SSH target — use Tailscale IPs, not MagicDNS names.** MagicDNS is **off**
on this tailnet (permanently — it caused API/automation resolution failures),
so `*.ts.barta.cm` names do **not** resolve. Point `SSH_TARGET` at the host's
stable Tailscale IP as `user@ip` and set `SSH_PORT` for the ssh port (a raw
`user@ip` can't inherit a port from `~/.ssh/config`). `ppm` uses
`SSH_TARGET=mba@100.64.0.4` + `SSH_PORT=2222`. Tailscale IPs (100.64.0.0/10,
CGNAT) are non-routable outside the tailnet, so committing them is not a
secret leak. Running the deploy still requires Tailscale to be up on the
machine you deploy from.

Deploy preflight uses SSH plus the public health endpoint; it does not
require a PAIMOS API key for that instance. For a richer read-only operator
smoke, configure a matching `paimos` CLI instance and run:

```
env -u PAIMOS_URL -u PAIMOS_API_KEY -u PPM_URL -u PPMAPIKEY \
  paimos --instance ppm doctor
```

If an operator only has SSH deploy access for an instance, `doctor` may be
unavailable locally even though deploy preflight works.

## Rollback (if a deploy goes sideways)

Each successful deploy prints the rollback one-liner as the last step. It
restores the tarball and repins compose to the previous image. Paraphrased:

> For full restore scenarios beyond a recent-deploy rollback (forensic /
> partial restore, captured drill timing, RPO/RTO targets, common
> failure modes), see [`BACKUP_RESTORE.md`](BACKUP_RESTORE.md).

```bash
# on the host
cd $COMPOSE_DIR
docker compose stop <service>
# bind storage:
tar -xzf <backup>/data.tar.gz -C $(dirname $DATA_PATH)/ --overwrite
# or volume storage:
docker run --rm -v <volume>:/dst -v <backup>:/src alpine \
  sh -c 'cd /dst && rm -rf ./* && tar -xzf /src/data.tar.gz'
sed -i 's|paimos:[^ ]*|<previous-image>|' docker-compose.yml
docker compose up -d <service>
```

**ppm (composeStack) rollback** — same principle, different pin: stop
the container, restore the volume tarball as above, repin the previous
image in nixcfg `hosts/csb1/docker/compose-spec.nix` (PR + merge), and
`sudo nixos-rebuild switch --flake .#csb1` on the host.

**Critical:** schema migrations in `backend/db/db.go` are one-way. Rolling
back the image without restoring the tarball may leave the old binary
staring at a schema it doesn't understand. Always restore the DB too.

## What this replaces

- `just deploy-ppm` / `scripts/deploy.sh ppm` for the ppm instance:
  replaced by the composeStack pin-bump + `nixos-rebuild` path above
  (OPS-116). The script remains valid for compose-based instances.
- Ad-hoc `ssh csb1 'docker compose pull && up -d'`: impossible on the
  composeStack host and replaced by the reconcile.
- Manual `VERSION` + README badge + install-example edits: replaced by
  `just release`, which carries the reviewed CHANGELOG entry through a
  protected release PR and tags only its exact merge commit.

## What this deliberately leaves out

- **Staging environment.** There isn't one. ppm acts as a soft canary
  because you use it yourself before any second operator sees a tag.
- **MinIO attachment snapshots.** A version bump doesn't touch stored
  attachments, so the backup is DB + data dir only. If you need bucket
  snapshots, `docker exec minio mc mirror …` handles it separately.
- **Secrets rotation, Cloudflare config, TLS certs.** Out of scope for
  image bumps; treat as infra work.
