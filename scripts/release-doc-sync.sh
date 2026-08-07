#!/usr/bin/env bash
# Files a single PAIMOS ticket — "Doc/site sync follow-up — vX.Y.Z" —
# with a four-item checklist, a diff summary since the previous tag,
# and a snapshot of the ../paimos-site sibling repo's state. Run after
# `just release` so README / docs / website / screenshots don't drift.
#
# Usage:
#   scripts/release-doc-sync.sh                    # uses the latest tag
#   scripts/release-doc-sync.sh v1.10.3            # explicit tag
#   scripts/release-doc-sync.sh --dry-run v1.10.3  # print the drafted body
#   scripts/release-doc-sync.sh --yes --no-edit v1.10.3
#
# Re-runnable: if an open ticket with the same title exists, the script
# prints that key and exits instead of creating a duplicate.

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

release_tags() {
  git tag --sort=-creatordate | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$'
}

usage() {
  cat >&2 <<'USAGE'
usage: scripts/release-doc-sync.sh [--dry-run] [--yes] [--no-edit] [tag]

Flags:
  --dry-run     draft the ticket body and print it; do not open an editor or write
  --yes, -y     file without the confirmation prompt
  --no-edit     skip opening $EDITOR before filing
USAGE
}

DRY_RUN=0
YES=0
NO_EDIT=0
TAG=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      ;;
    --yes|-y)
      YES=1
      ;;
    --no-edit)
      NO_EDIT=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    -*)
      echo "error: unknown flag: $1" >&2
      usage
      exit 1
      ;;
    *)
      if [[ -n "$TAG" ]]; then
        echo "error: multiple tags provided: $TAG and $1" >&2
        usage
        exit 1
      fi
      TAG="$1"
      ;;
  esac
  shift
done

if [[ -z "$TAG" ]]; then
  TAG=$(release_tags | head -1 || true)
  if [[ -z "$TAG" ]]; then
    echo "error: no semver release tags exist on this repo" >&2
    exit 1
  fi
  echo "Using latest release tag: $TAG"
fi

if ! git rev-parse "$TAG" >/dev/null 2>&1; then
  echo "error: tag $TAG does not exist" >&2
  exit 1
fi
if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: tag $TAG is not a semver release tag (expected vX.Y.Z)" >&2
  exit 1
fi
# PAI-751: the lookup must match on a STABLE prefix, not on the full
# title. The title embeds the tag, so an exact-title search could only
# ever find a re-run of the *same* release — never the previous
# release's open ticket. The documented "files or reuses a single
# ticket" behaviour was therefore unreachable, and ten tickets
# accumulated for one product line.
TITLE_PREFIX="Doc/site sync follow-up"
TITLE="$TITLE_PREFIX — $TAG"
# Marker line that identifies this release's section inside a rolling
# ticket. Used both when creating and when appending, so re-running
# doc-sync for the same tag is a no-op either way.
SECTION_MARKER="## $TITLE_PREFIX — $TAG"

find_existing_ticket() {
  command -v jq >/dev/null 2>&1 || return 0
  local status json key
  for status in new backlog in-progress qa; do
    json=$(paimos --json issue list -p PAI --status "$status" --limit 100 2>/dev/null || true)
    [[ -n "$json" ]] || continue
    key=$(printf '%s' "$json" \
      | jq -r --arg p "$TITLE_PREFIX" \
          'first(.issues[]? | select(.title | startswith($p)) | .issue_key) // empty' 2>/dev/null || true)
    if [[ -n "$key" && "$key" != "null" ]]; then
      printf '%s\n' "$key"
      return 0
    fi
  done
}

# Previous release tag — the line right after $TAG in chronological order,
# ignoring operational/bookmark tags such as pai-open-start-*.
PREV=$(release_tags | awk -v t="$TAG" '$0==t{getline; print; exit}')
if [[ -z "$PREV" ]]; then
  echo "warning: no prior tag found before $TAG; falling back to HEAD~20 for diff range" >&2
  PREV="HEAD~20"
fi
echo "Diff range: $PREV..$TAG"

# Sibling repo (../paimos-site).
SITE_DIR="$(cd "$ROOT/.." && pwd)/paimos-site"
if [[ -d "$SITE_DIR/.git" ]]; then
  SITE_STATUS=$(git -C "$SITE_DIR" status -s 2>&1 | head -20)
  [[ -z "$SITE_STATUS" ]] && SITE_STATUS="(clean — no uncommitted changes)"
  SITE_RECENT=$(git -C "$SITE_DIR" log --oneline -5 2>&1 || echo "(unknown)")
else
  SITE_STATUS="(paimos-site not found at $SITE_DIR — skipped)"
  SITE_RECENT="(n/a)"
fi

# Group touched files by area for the body.
GROUPED=$(git diff --name-only "$PREV..$TAG" 2>/dev/null | awk '
  /^backend\//        { b++; next }
  /^frontend\/src\//  { f++; next }
  /^docs\//           { d++; next }
  /^scripts\//        { s++; next }
  /^Justfile$/        { s++; next }
  { o++ }
  END {
    if (b) printf "backend/         %d file(s)\n", b
    if (f) printf "frontend/src/    %d file(s)\n", f
    if (d) printf "docs/            %d file(s)\n", d
    if (s) printf "scripts/         %d file(s)\n", s
    if (o) printf "other            %d file(s)\n", o
    if (!b && !f && !d && !s && !o) print "(no files touched between " ENVIRON["PREV"] " and " ENVIRON["TAG"] ")"
  }
' PREV="$PREV" TAG="$TAG")

RUNTIME=$(git log "$PREV..$TAG" --oneline -- backend/ frontend/src/ 2>/dev/null || echo "")
[[ -z "$RUNTIME" ]] && RUNTIME="(no runtime-relevant commits)"

# Build the ticket body.
TMP=$(mktemp -t doc-sync-XXXXXX)
trap 'rm -f "$TMP"' EXIT

cat > "$TMP" <<MARKDOWN
$SECTION_MARKER

After every release we sync four surfaces. Tick each off as confirmed
or updated; close the ticket once all four are settled.

- [ ] **README.md** — update if this release adds/removes a top-level feature, changes quickstart, or invalidates screenshots.
- [ ] **docs/** — \`CHANGELOG.md\` is already updated by the release flow. Cross-check \`CONFIGURATION.md\`, \`DEVELOPER_GUIDE.md\`, \`AGENT_INTERFACE.md\`, \`AGENT_INTEGRATION.md\`, \`api-minimal.md\`, \`DATA_MODEL.md\`, \`REFERENCE_DEPLOYMENTS.md\` for stale references and runtime rows.
- [ ] **paimos-site** (\`../paimos-site\`) — public marketing copy at paimos.com. See "Sibling repo state" below.
- [ ] **Brand / screenshots** — \`docs/brand/\` and any in-repo screenshots if UI changed materially.

### Diff since $PREV

Runtime-relevant commits:

\`\`\`
$RUNTIME
\`\`\`

Touched files, grouped:

\`\`\`
$GROUPED
\`\`\`

### Sibling repo state — paimos-site

Working tree:

\`\`\`
$SITE_STATUS
\`\`\`

Last 5 commits:

\`\`\`
$SITE_RECENT
\`\`\`

### Notes

_(Use this section to record per-surface findings as you investigate. Close the ticket when every checkbox is ticked or explicitly marked "no change needed".)_
MARKDOWN

echo
echo "Drafted ticket body to $TMP"

# Reuse an open doc-sync ticket when one exists: append this release's
# section rather than filing yet another ticket (PAI-751).
EXISTING=$(find_existing_ticket)

if [[ $DRY_RUN -eq 1 ]]; then
  if [[ -n "${EXISTING:-}" ]]; then
    echo "--- dry-run: would APPEND this section to $EXISTING"
  else
    echo "--- dry-run: would CREATE a new ticket"
    echo "--- dry-run ticket title"
    echo "$TITLE"
  fi
  echo "--- dry-run section body"
  cat "$TMP"
  exit 0
fi

if [[ $NO_EDIT -eq 0 ]]; then
  echo "Opening in \$EDITOR (${EDITOR:-vi}) for review — save & quit when done…"
  "${EDITOR:-vi}" "$TMP"
else
  echo "Skipping editor (--no-edit)."
fi

echo
if [[ $YES -eq 0 ]]; then
  read -rp "File the ticket in PAIMOS? [y/N] " ANSWER
  case "$ANSWER" in
    y|Y|yes|YES) ;;
    *)
      echo "Aborted. Body kept at $TMP"
      trap - EXIT
      exit 0
      ;;
  esac
else
  echo "Filing without confirmation (--yes)."
fi

if [[ -n "${EXISTING:-}" ]]; then
  # ── Append to the rolling ticket ────────────────────────────────────
  CURRENT=$(paimos --json issue get "$EXISTING" 2>/dev/null \
            | jq -r '.description // ""' 2>/dev/null || true)
  if [[ -z "$CURRENT" ]]; then
    echo "error: could not read the description of $EXISTING — refusing to overwrite it." >&2
    echo "       Section body kept at $TMP" >&2
    trap - EXIT
    exit 1
  fi

  # Idempotent: re-running doc-sync for a tag already covered is a no-op.
  if printf '%s' "$CURRENT" | grep -Fqx "$SECTION_MARKER"; then
    echo "✔ $EXISTING already covers $TAG — nothing to append."
    exit 0
  fi

  MERGED=$(mktemp -t doc-sync-merged-XXXXXX)
  # Newest release first: the top of the ticket is the release you are
  # most likely still syncing.
  { cat "$TMP"; printf '\n---\n\n'; printf '%s\n' "$CURRENT"; } > "$MERGED"

  # Only advance "latest" when this tag really is newer. doc-sync is
  # normally run right after a release, but a backfill or an out-of-order
  # run must not make the title claim an older release is the latest.
  CUR_TITLE=$(paimos --json issue get "$EXISTING" 2>/dev/null | jq -r '.title // ""' 2>/dev/null || true)
  CUR_LATEST=$(printf '%s' "$CUR_TITLE" | sed -nE 's/.*latest (v[0-9]+\.[0-9]+\.[0-9]+).*/\1/p')
  NEW_TITLE="$TITLE_PREFIX (rolling — latest $TAG)"
  if [[ -n "$CUR_LATEST" ]]; then
    NEWEST=$(printf '%s\n%s\n' "${CUR_LATEST#v}" "${TAG#v}" | sort -V | tail -1)
    [[ "$NEWEST" == "${CUR_LATEST#v}" ]] && NEW_TITLE="$CUR_TITLE"
  fi

  paimos issue update "$EXISTING" \
    --title "$NEW_TITLE" \
    --description-file "$MERGED" >/dev/null
  rm -f "$MERGED"
  echo "✔ Appended $TAG to $EXISTING (rolling doc-sync ticket) — title: $NEW_TITLE"
else
  # ── No open ticket: file a fresh one ────────────────────────────────
  RESPONSE=$(paimos --json issue create \
    -p PAI \
    --type ticket \
    --priority medium \
    --title "$TITLE" \
    --description-file "$TMP")

  # Extract issue_key from the JSON response. paimos CLI returns the
  # created issue as a single object — pull the key out with sed.
  KEY=$(printf '%s' "$RESPONSE" | sed -nE 's/.*"issue_key"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' | head -1)

  if [[ -n "$KEY" ]]; then
    echo "✔ Filed $KEY — $TITLE"
  else
    echo "✔ Created (raw response, could not parse issue_key):"
    echo "$RESPONSE"
  fi
fi
