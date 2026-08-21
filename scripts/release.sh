#!/usr/bin/env bash
# Protected-main release flow: prepare the four release files on a dedicated
# branch, open/reuse one PR whose commit carries DCO sign-off, wait for
# protected auto-merge, tag the exact PR merge commit, then wait for the
# published release evidence.
#
# Usage:
#   scripts/release.sh patch|minor|major|<x.y.z> [--no-edit]
#   scripts/release.sh                            # report commits since tag

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

NO_EDIT=0
ARGS=()
for arg in "$@"; do
  case "$arg" in
    --no-edit) NO_EDIT=1 ;;
    *) ARGS+=("$arg") ;;
  esac
done
MODE="${ARGS[0]:-}"
case "${EDITOR:-}" in
  ""|true|:|cat|tee) NO_EDIT=1 ;;
esac
if [[ "${RELEASE_NO_EDIT:-}" == "1" ]]; then
  NO_EDIT=1
fi

MERGE_TIMEOUT="${RELEASE_MERGE_TIMEOUT:-1800}"
MERGE_POLL="${RELEASE_MERGE_POLL:-10}"
if ! [[ "$MERGE_TIMEOUT" =~ ^[0-9]+$ && "$MERGE_TIMEOUT" -gt 0 &&
        "$MERGE_POLL" =~ ^[0-9]+$ && "$MERGE_POLL" -gt 0 ]]; then
  echo "error: RELEASE_MERGE_TIMEOUT and RELEASE_MERGE_POLL must be positive integers" >&2
  exit 2
fi

EXPECTED_FILES=$'README.md\nVERSION\ndocs/CHANGELOG.md\ndocs/INSTALL.md'
EXTERNAL_STAGE_MANIFEST='backend/contracts/fixtures/external-stage/manifest-v1.json'

fail() {
  echo "error: $*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

latest_release_tag() {
  git tag --sort=-creatordate | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true
}

changed_worktree_files() {
  { git diff --name-only HEAD; git diff --cached --name-only; git ls-files --others --exclude-standard; } |
    LC_ALL=C sort -u | grep -v '^$' || true
}

changed_commit_files() {
  local base="$1" head="$2"
  git diff --name-only "$base" "$head" | LC_ALL=C sort -u | grep -v '^$' || true
}

assert_expected_file_set() {
  local actual="$1" context="$2"
  if [[ "$actual" != "$EXPECTED_FILES" ]]; then
    echo "error: $context changed files do not match the release contract" >&2
    echo "expected:" >&2
    printf '%s\n' "$EXPECTED_FILES" | sed 's/^/  /' >&2
    echo "actual:" >&2
    printf '%s\n' "$actual" | sed 's/^/  /' >&2
    exit 1
  fi
}

assert_release_files_at() {
  local commit="$1" version="$2"
  local content

  git cat-file -e "${commit}^{commit}" 2>/dev/null || fail "release commit is unavailable: $commit"
  [[ "$(git show "$commit:VERSION")" == "$version" ]] ||
    fail "$commit does not carry VERSION=$version"

  content=$(git show "$commit:README.md")
  grep -qF "<code>v$version</code>" <<<"$content" ||
    fail "$commit does not carry the v$version README badge"

  content=$(git show "$commit:docs/CHANGELOG.md")
  grep -qF "## [$version] — " <<<"$content" ||
    fail "$commit does not carry the $version changelog entry"
  if grep -qF 'TODO fill in before committing' <<<"$content"; then
    fail "$commit still contains the unreviewed changelog placeholder"
  fi

  content=$(git show "$commit:docs/INSTALL.md")
  [[ $(grep -cF "VER=$version" <<<"$content") -ge 2 ]] ||
    fail "$commit does not carry both pinned install examples for $version"
  grep -qE "paimos --version[[:space:]]+# $version" <<<"$content" ||
    fail "$commit does not carry the $version CLI smoke example"
}

# The external-stage v1 manifest is deliberately outside the four mutable
# release files: adapters pin the first reviewed Paimos release forever, so a
# later patch release must not rewrite that provenance. The feature train must
# replace PENDING_RELEASE_TAG before it reaches main. For the first release the
# future tag is allowed; after that, the pinned tag must resolve to the exact
# same manifest blob.
assert_external_stage_release_pin() {
  local ref="$1" new_tag="$2" manifest pin current_oid pinned_oid

  git cat-file -e "$ref:$EXTERNAL_STAGE_MANIFEST" 2>/dev/null ||
    fail "$ref does not carry the external-stage v1 release manifest"
  manifest=$(git show "$ref:$EXTERNAL_STAGE_MANIFEST")
  pin=$(jq -er '
    if type == "object" and (.paimos_release | type) == "string"
    then .paimos_release
    else error("missing paimos_release")
    end
  ' <<<"$manifest") || fail "$ref carries an invalid external-stage v1 release manifest"

  [[ "$pin" != 'PENDING_RELEASE_TAG' ]] ||
    fail "external-stage v1 still has PENDING_RELEASE_TAG; finalize its immutable Paimos release pin before publishing"
  [[ "$pin" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
    fail "external-stage v1 paimos_release is not a stable SemVer tag: $pin"

  if git cat-file -e "refs/tags/$pin^{commit}" 2>/dev/null; then
    git cat-file -e "refs/tags/$pin:$EXTERNAL_STAGE_MANIFEST" 2>/dev/null ||
      fail "external-stage v1 pinned tag $pin does not carry its manifest"
    current_oid=$(git rev-parse "$ref:$EXTERNAL_STAGE_MANIFEST")
    pinned_oid=$(git rev-parse "refs/tags/$pin:$EXTERNAL_STAGE_MANIFEST")
    [[ "$current_oid" == "$pinned_oid" ]] ||
      fail "external-stage v1 manifest differs from immutable pinned tag $pin"
  else
    [[ "$pin" == "$new_tag" ]] ||
      fail "external-stage v1 pins unavailable tag $pin instead of the release being prepared ($new_tag)"
  fi
}

assert_release_delta() {
  local base="$1" head="$2" version="$3"
  local first_heading entry_count changed entry_date validate_tmp history_bytes base_first_heading head_second_heading

  changed=$(changed_commit_files "$base" "$head")
  assert_expected_file_set "$changed" "$head relative to $base"
  assert_release_files_at "$head" "$version"

  validate_tmp=$(mktemp -d "${TMPDIR:-/tmp}/paimos-release-validate.XXXXXX")
  git show "$head:VERSION" > "$validate_tmp/actual"
  printf '%s\n' "$version" > "$validate_tmp/expected"
  if ! cmp -s "$validate_tmp/actual" "$validate_tmp/expected"; then
    rm -rf "$validate_tmp"
    fail "$head contains non-deterministic VERSION content"
  fi

  git show "$head:README.md" > "$validate_tmp/actual"
  git show "$base:README.md" |
    sed -E "s|<code>v[0-9]+\.[0-9]+\.[0-9]+</code>|<code>v$version</code>|" > "$validate_tmp/expected"
  if ! cmp -s "$validate_tmp/actual" "$validate_tmp/expected"; then
    rm -rf "$validate_tmp"
    fail "$head contains non-deterministic README.md changes"
  fi

  git show "$head:docs/INSTALL.md" > "$validate_tmp/actual"
  git show "$base:docs/INSTALL.md" |
    sed -E \
      -e "s|VER=[0-9]+\.[0-9]+\.[0-9]+|VER=$version|g" \
      -e "s|(paimos --version[[:space:]]+# )[0-9]+\.[0-9]+\.[0-9]+|\1$version|" > "$validate_tmp/expected"
  if ! cmp -s "$validate_tmp/actual" "$validate_tmp/expected"; then
    rm -rf "$validate_tmp"
    fail "$head contains non-deterministic docs/INSTALL.md changes"
  fi

  git show "$base:docs/CHANGELOG.md" > "$validate_tmp/base-changelog"
  git show "$head:docs/CHANGELOG.md" > "$validate_tmp/head-changelog"
  sed '/^## \[/,$d' "$validate_tmp/base-changelog" > "$validate_tmp/expected"
  sed '/^## \[/,$d' "$validate_tmp/head-changelog" > "$validate_tmp/actual"
  if ! cmp -s "$validate_tmp/actual" "$validate_tmp/expected"; then
    rm -rf "$validate_tmp"
    fail "$head changed the CHANGELOG preamble"
  fi

  sed -n '/^## \[/,$p' "$validate_tmp/base-changelog" > "$validate_tmp/expected"
  history_bytes=$(wc -c < "$validate_tmp/expected" | tr -d '[:space:]')
  tail -c "$history_bytes" "$validate_tmp/head-changelog" > "$validate_tmp/actual"
  if ! cmp -s "$validate_tmp/actual" "$validate_tmp/expected"; then
    rm -rf "$validate_tmp"
    fail "$head changed prior CHANGELOG history"
  fi

  base_first_heading=$(awk '/^## \[/{print; exit}' "$validate_tmp/base-changelog")
  head_second_heading=$(awk '/^## \[/{headings++; if (headings == 2) {print; exit}}' "$validate_tmp/head-changelog")
  if [[ "$head_second_heading" != "$base_first_heading" ]]; then
    rm -rf "$validate_tmp"
    fail "$head inserted unexpected CHANGELOG sections before prior history"
  fi

  first_heading=$(awk '/^## \[/{print; exit}' "$validate_tmp/head-changelog")
  entry_date=${first_heading#"## [$version] — "}
  if [[ "$first_heading" == "$entry_date" || ! "$entry_date" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
    rm -rf "$validate_tmp"
    fail "$head does not prepend [$version] with an ISO date"
  fi
  entry_count=$(awk -v heading="## [$version] " \
    'index($0, heading) == 1 {count++} END {print count+0}' "$validate_tmp/head-changelog")
  if [[ "$entry_count" != "1" ]]; then
    rm -rf "$validate_tmp"
    fail "$head must contain exactly one [$version] CHANGELOG entry"
  fi
  rm -rf "$validate_tmp"
}

run_release_gates_at_ref() {
  local ref="$1" gate_tmp gate_status=0
  gate_tmp=$(mktemp -d "${TMPDIR:-/tmp}/paimos-release-gates.XXXXXX")
  rmdir "$gate_tmp"
  git worktree add --quiet --detach "$gate_tmp" "$ref"
  "$gate_tmp/scripts/check-claims.sh" || gate_status=1
  "$gate_tmp/scripts/check-release-hygiene.sh" || gate_status=1
  git worktree remove --force "$gate_tmp"
  [[ "$gate_status" -eq 0 ]] || fail "release gates failed at validated PR head $ref"
}

remote_branch_exists() {
  git ls-remote --exit-code --heads origin "$RELEASE_BRANCH" >/dev/null 2>&1
}

fetch_release_branch() {
  git fetch --quiet origin "+refs/heads/$RELEASE_BRANCH:refs/remotes/origin/$RELEASE_BRANCH"
}

assert_release_branch() {
  local head="origin/$RELEASE_BRANCH"
  local base changed

  git cat-file -e "${head}^{commit}" 2>/dev/null ||
    fail "remote release branch is unavailable: $RELEASE_BRANCH"
  if git show-ref --verify --quiet "refs/heads/$RELEASE_BRANCH"; then
    [[ "$(git rev-parse "$RELEASE_BRANCH")" == "$(git rev-parse "$head")" ]] ||
      fail "local $RELEASE_BRANCH differs from origin/$RELEASE_BRANCH"
  fi
  base=$(git merge-base origin/main "$head")
  [[ -n "$base" ]] || fail "release branch has no merge-base with origin/main"
  changed=$(changed_commit_files "$base" "$head")
  assert_expected_file_set "$changed" "origin/$RELEASE_BRANCH"
  assert_release_delta "$base" "$head" "$NEW"
  "$ROOT/scripts/check-dco.sh" "$base" "$head" >/dev/null ||
    fail "release branch contains a commit without its author's DCO sign-off"
}

fetch_and_validate_pr_head() {
  local json="$1" api_head fetched_head base
  api_head=$(printf '%s\n' "$json" | jq -r '.headRefOid // empty')
  [[ -n "$api_head" ]] || fail "release PR has no head OID"
  PR_HEAD_REF="refs/paimos/release-prs/$PR_NUMBER/head"
  git fetch --quiet origin "+refs/pull/$PR_NUMBER/head:$PR_HEAD_REF"
  fetched_head=$(git rev-parse "$PR_HEAD_REF")
  [[ "$fetched_head" == "$api_head" ]] ||
    fail "fetched PR head $fetched_head differs from API head $api_head"
  base=$(git merge-base origin/main "$PR_HEAD_REF")
  [[ -n "$base" ]] || fail "release PR head has no merge-base with origin/main"
  assert_release_delta "$base" "$PR_HEAD_REF" "$NEW"
  "$ROOT/scripts/check-dco.sh" "$base" "$PR_HEAD_REF" >/dev/null ||
    fail "release PR head contains a commit without its author's DCO sign-off"
  VALIDATED_HEAD_OID="$fetched_head"
}

assert_pr_head_pinned() {
  local json="$1" api_head fetched_head
  api_head=$(printf '%s\n' "$json" | jq -r '.headRefOid // empty')
  [[ "$api_head" == "$VALIDATED_HEAD_OID" ]] ||
    fail "release PR head changed after validation: expected $VALIDATED_HEAD_OID, got $api_head"
  git fetch --quiet origin "+refs/pull/$PR_NUMBER/head:$PR_HEAD_REF"
  fetched_head=$(git rev-parse "$PR_HEAD_REF")
  [[ "$fetched_head" == "$VALIDATED_HEAD_OID" ]] ||
    fail "fetched release PR head changed after validation"
}

assert_squash_auto_merge() {
  local json="$1" method
  method=$(printf '%s\n' "$json" | jq -r '.autoMergeRequest.mergeMethod // empty')
  [[ "$method" == "SQUASH" ]] ||
    fail "release PR is missing protected squash auto-merge provenance (got: ${method:-none})"
}

find_release_pr() {
  local json count
  json=$(gh pr list \
    --repo "$REPO" \
    --state all \
    --head "$RELEASE_BRANCH" \
    --limit 100 \
    --json number,state,url,headRefName,headRefOid,baseRefName,autoMergeRequest,mergeCommit)
  count=$(printf '%s\n' "$json" | jq 'length')
  if [[ "$count" -gt 1 ]]; then
    fail "multiple PRs exist for $RELEASE_BRANCH; refusing an ambiguous release"
  fi
  if [[ "$count" -eq 1 ]]; then
    printf '%s\n' "$json" | jq '.[0]'
  fi
}

validate_pr_identity() {
  local json="$1"
  [[ "$(printf '%s\n' "$json" | jq -r '.baseRefName')" == "main" ]] ||
    fail "release PR does not target main"
  [[ "$(printf '%s\n' "$json" | jq -r '.headRefName')" == "$RELEASE_BRANCH" ]] ||
    fail "release PR does not use $RELEASE_BRANCH"
}

assert_open_pr_head_matches_remote() {
  local json="$1" pr_head remote_head
  pr_head=$(printf '%s\n' "$json" | jq -r '.headRefOid')
  remote_head=$(git rev-parse "origin/$RELEASE_BRANCH")
  [[ "$pr_head" == "$remote_head" ]] ||
    fail "release PR head $pr_head differs from origin/$RELEASE_BRANCH at $remote_head"
}

checkout_release_branch_for_sync() {
  local remote_oid current
  fetch_release_branch
  remote_oid=$(git rev-parse "origin/$RELEASE_BRANCH")
  current=$(git rev-parse --abbrev-ref HEAD)
  [[ -z "$(changed_worktree_files)" ]] ||
    fail "working tree became dirty while waiting for release merge"

  if [[ "$current" == "$RELEASE_BRANCH" ]]; then
    [[ "$(git rev-parse HEAD)" == "$remote_oid" ]] ||
      fail "local $RELEASE_BRANCH differs from origin/$RELEASE_BRANCH"
    return
  fi

  if git show-ref --verify --quiet "refs/heads/$RELEASE_BRANCH"; then
    [[ "$(git rev-parse "$RELEASE_BRANCH")" == "$remote_oid" ]] ||
      fail "local $RELEASE_BRANCH differs from origin/$RELEASE_BRANCH"
    git switch "$RELEASE_BRANCH" >/dev/null
  else
    git switch -c "$RELEASE_BRANCH" --track "origin/$RELEASE_BRANCH" >/dev/null
  fi
}

sync_release_branch() {
  checkout_release_branch_for_sync
  git fetch --quiet origin main
  if git merge-base --is-ancestor origin/main HEAD; then
    return
  fi

  echo "Release PR is behind main; merging current origin/main with DCO sign-off." >&2
  git merge --no-commit --no-ff --no-gpg-sign origin/main >&2
  if ! "$ROOT/scripts/check-claims.sh" >&2 ||
     ! "$ROOT/scripts/check-release-hygiene.sh" >&2; then
    git merge --abort
    fail "release gates failed after merging current origin/main"
  fi
  git commit --no-gpg-sign --signoff \
    -m "chore(release): sync $NEW_TAG with main" >&2
  git push origin "$RELEASE_BRANCH"
  fetch_release_branch
  assert_release_branch
  VALIDATED_HEAD_OID=$(git rev-parse "origin/$RELEASE_BRANCH")
}

ensure_auto_merge() {
  local pr_json="$1" auto head_oid
  auto=$(printf '%s\n' "$pr_json" | jq -r 'if .autoMergeRequest == null then "none" else "enabled" end')
  if [[ "$auto" == "enabled" ]]; then
    assert_squash_auto_merge "$pr_json"
    return
  fi
  head_oid=$(printf '%s\n' "$pr_json" | jq -r '.headRefOid')
  gh pr merge "$PR_NUMBER" \
    --repo "$REPO" \
    --auto \
    --squash \
    --delete-branch \
    --match-head-commit "$head_oid" >/dev/null
  echo "Protected auto-merge enabled for $PR_URL." >&2
}

wait_for_protected_merge() {
  local start now pr_json state merge_status merge_oid
  start=$(date +%s)
  while true; do
    now=$(date +%s)
    if (( now - start > MERGE_TIMEOUT )); then
      fail "timed out after ${MERGE_TIMEOUT}s waiting for protected merge: $PR_URL"
    fi

    pr_json=$(gh pr view "$PR_NUMBER" \
      --repo "$REPO" \
      --json number,state,url,headRefName,headRefOid,baseRefName,mergeStateStatus,autoMergeRequest,mergeCommit)
    validate_pr_identity "$pr_json"
    assert_pr_head_pinned "$pr_json"
    state=$(printf '%s\n' "$pr_json" | jq -r '.state')
    case "$state" in
      MERGED)
        merge_oid=$(printf '%s\n' "$pr_json" | jq -r '.mergeCommit.oid // empty')
        [[ -n "$merge_oid" ]] || fail "merged release PR has no merge commit"
        assert_squash_auto_merge "$pr_json"
        MERGE_OID="$merge_oid"
        return
        ;;
      CLOSED)
        fail "release PR closed without merge: $PR_URL"
        ;;
      OPEN) ;;
      *) fail "unexpected release PR state: $state" ;;
    esac

    merge_status=$(printf '%s\n' "$pr_json" | jq -r '.mergeStateStatus // "UNKNOWN"')
    case "$merge_status" in
      BEHIND)
        sync_release_branch
        ;;
      DIRTY)
        fail "release PR has merge conflicts: $PR_URL"
        ;;
      *)
        ensure_auto_merge "$pr_json"
        ;;
    esac
    echo "Waiting for protected checks/merge ($merge_status): $PR_URL" >&2
    sleep "$MERGE_POLL"
  done
}

assert_release_merge() {
  local merge_oid="$1" changed parent_count merge_tree head_tree
  git fetch --quiet origin main
  git cat-file -e "${merge_oid}^{commit}" 2>/dev/null ||
    fail "PR merge commit was not fetched from origin/main: $merge_oid"
  git merge-base --is-ancestor "$merge_oid" origin/main ||
    fail "PR merge commit is not on origin/main: $merge_oid"
  parent_count=$(git rev-list --parents -n1 "$merge_oid" | awk '{print NF-1}')
  [[ "$parent_count" == "1" ]] ||
    fail "release PR was not squash-merged; expected one parent, got $parent_count"
  changed=$(changed_commit_files "$merge_oid^" "$merge_oid")
  assert_expected_file_set "$changed" "protected-main merge $merge_oid"
  assert_release_delta "$merge_oid^" "$merge_oid" "$NEW"
  merge_tree=$(git rev-parse "$merge_oid^{tree}")
  head_tree=$(git rev-parse "$PR_HEAD_REF^{tree}")
  [[ "$merge_tree" == "$head_tree" ]] ||
    fail "protected-main merge tree differs from validated PR head $VALIDATED_HEAD_OID"
}

tag_release_merge() {
  local merge_oid="$1" existing_oid
  if git rev-parse "$NEW_TAG" >/dev/null 2>&1; then
    existing_oid=$(git rev-list -n1 "${NEW_TAG}^{}")
    [[ "$existing_oid" == "$merge_oid" ]] ||
      fail "$NEW_TAG already points to $existing_oid, expected PR merge $merge_oid"
    echo "$NEW_TAG already tags the correct protected-main merge commit."
  else
    git tag -a --no-sign "$NEW_TAG" "$merge_oid" -m "release $NEW"
    echo "Created $NEW_TAG at protected-main merge $merge_oid."
  fi
  git push origin "refs/tags/$NEW_TAG"
}

prepare_release_branch() {
  local current changed tmp today base
  current=$(git rev-parse --abbrev-ref HEAD)
  changed=$(changed_worktree_files)

  case "$current" in
    main)
      [[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/main)" ]] ||
        fail "local main is not exactly current origin/main"
      if [[ -n "$changed" ]]; then
        [[ "$changed" == "docs/CHANGELOG.md" ]] ||
          fail "before release preparation, only a reviewed docs/CHANGELOG.md entry may be dirty"
        grep -qE "^## \[$NEW\] " docs/CHANGELOG.md ||
          fail "dirty docs/CHANGELOG.md does not contain a reviewed [$NEW] entry"
      fi
      git show-ref --verify --quiet "refs/heads/$RELEASE_BRANCH" &&
        fail "local branch already exists without a matching remote PR: $RELEASE_BRANCH"
      git switch -c "$RELEASE_BRANCH" >/dev/null
      ;;
    "$RELEASE_BRANCH")
      if [[ -z "$changed" && "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]]; then
        base=$(git merge-base origin/main HEAD)
        assert_release_delta "$base" HEAD "$NEW"
        "$ROOT/scripts/check-dco.sh" "$base" HEAD >/dev/null ||
          fail "prepared local release branch contains a commit without its author's DCO sign-off"
        "$ROOT/scripts/check-claims.sh"
        "$ROOT/scripts/check-release-hygiene.sh"
        git push -u origin "$RELEASE_BRANCH"
        fetch_release_branch
        assert_release_branch
        return
      fi
      if [[ -n "$changed" ]]; then
        while IFS= read -r file; do
          printf '%s\n' "$EXPECTED_FILES" | grep -qxF "$file" ||
            fail "unexpected dirty file while resuming $RELEASE_BRANCH: $file"
        done <<<"$changed"
      fi
      ;;
    *)
      fail "start a release from main or $RELEASE_BRANCH (currently $current)"
      ;;
  esac

  "$ROOT/scripts/check-claims.sh"
  today=$(date -u +%Y-%m-%d)

  if [[ $NO_EDIT -eq 1 ]] && ! grep -qE "^## \[$NEW\] " docs/CHANGELOG.md; then
    fail "non-interactive release requires a reviewed ## [$NEW] changelog entry before invocation"
  fi

  printf '%s\n' "$NEW" > VERSION

  tmp=$(mktemp)
  sed -E "s|<code>v[0-9]+\.[0-9]+\.[0-9]+</code>|<code>v$NEW</code>|" README.md > "$tmp"
  mv "$tmp" README.md

  tmp=$(mktemp)
  sed -E \
    -e "s|VER=[0-9]+\.[0-9]+\.[0-9]+|VER=$NEW|g" \
    -e "s|(paimos --version[[:space:]]+# )[0-9]+\.[0-9]+\.[0-9]+|\1$NEW|" \
    docs/INSTALL.md > "$tmp"
  mv "$tmp" docs/INSTALL.md

  if grep -qE "^## \[$NEW\] " docs/CHANGELOG.md; then
    tmp=$(mktemp)
    sed -E "s|^## \[$NEW\] .*|## [$NEW] — $today|" docs/CHANGELOG.md > "$tmp"
    mv "$tmp" docs/CHANGELOG.md
  else
    tmp=$(mktemp)
    {
      awk 'BEGIN{p=1} /^## \[/{p=0} p' docs/CHANGELOG.md
      printf '## [%s] — %s\n\n' "$NEW" "$today"
      printf '### Added — TODO fill in before committing\n\n'
      git log "$LAST_TAG..origin/main" --format='- %s' -- backend/ frontend/src/ docs/ scripts/ || true
      printf '\n'
      awk 'f{print} /^## \[/{if(!f){f=1; print}}' docs/CHANGELOG.md
    } > "$tmp"
    mv "$tmp" docs/CHANGELOG.md
    echo "Opening CHANGELOG in \$EDITOR (${EDITOR:-vi}) for review…"
    "${EDITOR:-vi}" docs/CHANGELOG.md
  fi

  "$ROOT/scripts/check-release-hygiene.sh"
  changed=$(changed_worktree_files)
  assert_expected_file_set "$changed" "release preparation"

  git add VERSION README.md docs/CHANGELOG.md docs/INSTALL.md
  if git diff --cached --quiet; then
    fail "release preparation produced no commit"
  fi
  git commit --no-gpg-sign --signoff -m "release: $NEW_TAG"
  base=$(git merge-base origin/main HEAD)
  assert_release_delta "$base" HEAD "$NEW"
  if [[ "${RELEASE_TEST_FAILPOINT:-}" == "before-branch-push" ]]; then
    fail "injected interruption before release branch push"
  fi
  git push -u origin "$RELEASE_BRANCH"
  fetch_release_branch
  assert_release_branch
}

create_release_pr() {
  local body
  body=$(printf '%s\n' \
    "## Release $NEW_TAG" \
    "" \
    "Protected-main release prepared by scripts/release.sh." \
    "" \
    "- release commit carries the author's DCO sign-off" \
    "- release hygiene and claim gates passed locally" \
    "- tag will be created only at this PR's protected-main merge commit")
  gh pr create \
    --repo "$REPO" \
    --base main \
    --head "$RELEASE_BRANCH" \
    --title "release: $NEW_TAG" \
    --body "$body" >/dev/null
  echo "Opened protected release PR for $NEW_TAG."
}

cleanup_checkout() {
  local current local_oid remote_oid
  current=$(git rev-parse --abbrev-ref HEAD)
  git fetch --quiet origin main
  if [[ "$current" == "$RELEASE_BRANCH" ]]; then
    [[ -z "$(changed_worktree_files)" ]] ||
      fail "cannot clean up: release checkout is dirty"
    git switch main >/dev/null
  fi
  if [[ "$(git rev-parse --abbrev-ref HEAD)" == "main" ]]; then
    git merge --ff-only origin/main >/dev/null
    if git show-ref --verify --quiet "refs/heads/$RELEASE_BRANCH"; then
      local_oid=$(git rev-parse "$RELEASE_BRANCH")
      [[ "$local_oid" == "$VALIDATED_HEAD_OID" ]] ||
        fail "refusing to delete drifted local $RELEASE_BRANCH at $local_oid"
      git branch -D "$RELEASE_BRANCH" >/dev/null
    fi
    remote_oid=$(git ls-remote --heads origin "$RELEASE_BRANCH" | awk '{print $1}')
    if [[ -n "$remote_oid" ]]; then
      [[ "$remote_oid" == "$VALIDATED_HEAD_OID" ]] ||
        fail "refusing to delete drifted origin/$RELEASE_BRANCH at $remote_oid"
      git push origin --delete "$RELEASE_BRANCH" >/dev/null
    fi
  fi
  git update-ref -d "$PR_HEAD_REF"
}

for cmd in git gh jq cmp; do
  require_command "$cmd"
done

git fetch --tags --quiet origin
git fetch --quiet origin main
LAST_TAG=$(latest_release_tag)
[[ -n "$LAST_TAG" ]] || fail "no semver release tags yet — create v0.1.0 manually first"
LAST_VERSION="${LAST_TAG#v}"
IFS=. read -r LAST_MAJOR LAST_MINOR LAST_PATCH <<<"$LAST_VERSION"

if [[ -z "$MODE" ]]; then
  echo "Last release: $LAST_TAG"
  echo
  echo "All commits since $LAST_TAG:"
  git log "$LAST_TAG..origin/main" --oneline
  echo
  echo "Runtime-relevant (backend/ frontend/src/):"
  git log "$LAST_TAG..origin/main" --oneline -- backend/ frontend/src/ || echo "  (none)"
  echo
  echo "Re-run with: patch | minor | major | <x.y.z>"
  exit 0
fi

case "$MODE" in
  patch) NEW="$LAST_MAJOR.$LAST_MINOR.$((LAST_PATCH + 1))" ;;
  minor) NEW="$LAST_MAJOR.$((LAST_MINOR + 1)).0" ;;
  major) NEW="$((LAST_MAJOR + 1)).0.0" ;;
  *)
    [[ "$MODE" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
      fail "mode must be patch|minor|major|<x.y.z> (got: $MODE)"
    NEW="$MODE"
    ;;
esac
NEW_TAG="v$NEW"
RELEASE_BRANCH="release/$NEW_TAG"
assert_external_stage_release_pin origin/main "$NEW_TAG"
REPO=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
[[ -n "$REPO" ]] || fail "could not resolve GitHub repository"
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
case "$CURRENT_BRANCH" in
  main|"$RELEASE_BRANCH") ;;
  *) fail "start or resume $NEW_TAG from main or $RELEASE_BRANCH (currently $CURRENT_BRANCH)" ;;
esac

echo "Release target: $LAST_TAG → $NEW_TAG"
echo "Protected branch: $RELEASE_BRANCH"

PR_JSON=$(find_release_pr)
if [[ -n "$PR_JSON" ]]; then
  [[ -z "$(changed_worktree_files)" ]] ||
    fail "working tree is dirty while reusing the canonical release PR"
  validate_pr_identity "$PR_JSON"
  PR_NUMBER=$(printf '%s\n' "$PR_JSON" | jq -r '.number')
  PR_URL=$(printf '%s\n' "$PR_JSON" | jq -r '.url')
  PR_STATE=$(printf '%s\n' "$PR_JSON" | jq -r '.state')
  echo "Reusing release PR: $PR_URL ($PR_STATE)"
else
  if git rev-parse "$NEW_TAG" >/dev/null 2>&1; then
    fail "$NEW_TAG exists without its canonical $RELEASE_BRANCH PR"
  fi
  if remote_branch_exists; then
    [[ -z "$(changed_worktree_files)" ]] ||
      fail "working tree is dirty while reusing origin/$RELEASE_BRANCH"
    fetch_release_branch
    assert_release_branch
  else
    prepare_release_branch
  fi
  create_release_pr
  PR_JSON=$(find_release_pr)
  [[ -n "$PR_JSON" ]] || fail "release PR was not discoverable after creation"
  validate_pr_identity "$PR_JSON"
  PR_NUMBER=$(printf '%s\n' "$PR_JSON" | jq -r '.number')
  PR_URL=$(printf '%s\n' "$PR_JSON" | jq -r '.url')
  PR_STATE=$(printf '%s\n' "$PR_JSON" | jq -r '.state')
fi

case "$PR_STATE" in
  OPEN)
    [[ -z "$(changed_worktree_files)" ]] ||
      fail "working tree is dirty while release PR is open"
    fetch_release_branch
    assert_release_branch
    assert_open_pr_head_matches_remote "$PR_JSON"
    fetch_and_validate_pr_head "$PR_JSON"
    run_release_gates_at_ref "$PR_HEAD_REF"
    wait_for_protected_merge
    ;;
  MERGED)
    fetch_and_validate_pr_head "$PR_JSON"
    run_release_gates_at_ref "$PR_HEAD_REF"
    assert_squash_auto_merge "$PR_JSON"
    MERGE_OID=$(printf '%s\n' "$PR_JSON" | jq -r '.mergeCommit.oid // empty')
    [[ -n "$MERGE_OID" ]] || fail "merged release PR has no merge commit"
    ;;
  CLOSED)
    fail "release PR closed without merge: $PR_URL"
    ;;
  *)
    fail "unexpected release PR state: $PR_STATE"
    ;;
esac

assert_release_merge "$MERGE_OID"
tag_release_merge "$MERGE_OID"
cleanup_checkout

echo
echo "Pushed $NEW_TAG from protected-main merge $MERGE_OID."
echo "Waiting for ghcr.io/inspr-at/paimos:$NEW to appear…"
# shellcheck disable=SC1091
source "$ROOT/scripts/_deploy-lib.sh"
for _ in $(seq 1 60); do
  if ghcr::image_exists "ghcr.io/inspr-at/paimos:$NEW"; then
    echo "✔ ghcr.io/inspr-at/paimos:$NEW is live."
    break
  fi
  sleep 10
done
if ! ghcr::image_exists "ghcr.io/inspr-at/paimos:$NEW"; then
  fail "image is still unavailable after 10m; inspect tag CI before deploying"
fi

echo "Waiting for tag workflows to publish release evidence…"
"$ROOT/scripts/wait-release-ci.sh" "$NEW_TAG" --workflows ci,release

echo
echo "Next:"
echo "  just verify-release $NEW_TAG"
echo "  deploy ppm through the protected composeStack pin in docs/DEPLOY.md"
echo "  just doc-sync $NEW_TAG"
