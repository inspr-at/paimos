#!/usr/bin/env bash
set -euo pipefail

export GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL=/dev/null

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
REAL_GIT=$(type -a -p git | awk '$0 != "/usr/bin/git" { print; exit }')
[[ -n "$REAL_GIT" ]] || REAL_GIT=/usr/bin/git
REAL_DATE=$(command -v date)
export REAL_DATE REAL_GIT
TMP_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/paimos-release-test.XXXXXX")
trap 'rm -rf "$TMP_ROOT"' EXIT

MANUAL_RECOVERY_REASON='manual_squash_merge_missing_auto_merge_provenance'
IMMEDIATE_AUTO_MERGE_RECOVERY_REASON='canonical_auto_merge_immediate_merge_post_merge_request_missing'
IMMEDIATE_RECOVERY_VERSION='5.20.0'
MANUAL_RECOVERY_VERSION='5.19.0'

fail() {
  echo "test-release: $*" >&2
  exit 1
}

next_calendar_day() {
  local iso="$1"
  if "$REAL_DATE" -u -d "$iso + 1 day" +%y.%m.%d 2>/dev/null; then
    return
  fi
  "$REAL_DATE" -j -u -v+1d -f '%Y-%m-%d' "$iso" +%y.%m.%d
}

write_stub_scripts() {
  local repo="$1"
  mkdir -p "$repo/scripts"
  cp "$ROOT/scripts/release.sh" "$repo/scripts/release.sh"
  cp "$ROOT/scripts/release-version.sh" "$repo/scripts/release-version.sh"
  cp "$ROOT/scripts/_deploy-lib.sh" "$repo/scripts/_deploy-lib.sh"
  cp "$ROOT/scripts/wait-release-ci.sh" "$repo/scripts/wait-release-ci.sh"
  cp "$ROOT/scripts/wait-backend-full.sh" "$repo/scripts/wait-backend-full.sh"

  cp "$ROOT/scripts/check-dco.sh" "$repo/scripts/check-dco.sh"
  cat > "$repo/scripts/check-claims.sh" <<'GATE'
#!/usr/bin/env bash
GATE_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
[[ -z "${FAKE_GATE_LOG:-}" ]] || printf 'claims\n' >> "$FAKE_GATE_LOG"
if [[ "${FAKE_CLAIMS_FAIL_ON_CONTENT:-0}" == "1" && -f "$GATE_ROOT/claims-broken" ]]; then
  exit 1
fi
if [[ -n "${FAKE_CLAIMS_FAIL_MARKER:-}" && -f "$FAKE_CLAIMS_FAIL_MARKER" ]]; then
  current=$(git -C "$GATE_ROOT" rev-parse --abbrev-ref HEAD)
  [[ -n "${FAKE_CLAIMS_FAIL_ON_BRANCH:-}" && "$current" != "$FAKE_CLAIMS_FAIL_ON_BRANCH" ]]
fi
GATE
  cat > "$repo/scripts/check-release-hygiene.sh" <<'GATE'
#!/usr/bin/env bash
[[ -z "${FAKE_GATE_LOG:-}" ]] || printf 'hygiene\n' >> "$FAKE_GATE_LOG"
exit 0
GATE
  chmod +x "$repo/scripts/"*.sh
}

write_fake_commands() {
  local bin="$1"
  mkdir -p "$bin"

  cat > "$bin/git" <<'GIT'
#!/usr/bin/env bash
set -euo pipefail

real_git=${REAL_GIT:?}
advance_main() {
  local label="$1" advance_work="$FAKE_GH_STATE/$1-advance-work"
  "$real_git" clone -q "${FAKE_GH_ORIGIN:?}" "$advance_work" >/dev/null 2>&1
  "$real_git" -C "$advance_work" config user.name 'Protected Main Bot'
  "$real_git" -C "$advance_work" config user.email 'protected-main@example.test'
  "$real_git" -C "$advance_work" switch -q main
  printf 'main moved during %s\n' "$label" > "$advance_work/$label-main.txt"
  "$real_git" -C "$advance_work" add "$label-main.txt"
  "$real_git" -C "$advance_work" commit -q --signoff -m "advance main during $label"
  FAKE_GH_SERVER_MERGE=1 "$real_git" -C "$advance_work" push -q origin main
}

if [[ ( "${FAKE_ADVANCE_MAIN_DURING_TAG_AUDIT:-0}" == "1" ||
        "${FAKE_ADVANCE_MAIN_DURING_POST_AUDIT_TAG_QUERY:-0}" == "1" ) &&
      "$*" == *'refs/paimos/release-origin-tags/'* ]]; then
  "$real_git" "$@"
  count_file="${FAKE_GH_STATE:?}/tag-audit-count"
  count=0
  [[ ! -f "$count_file" ]] || count=$(<"$count_file")
  count=$((count + 1))
  printf '%s\n' "$count" > "$count_file"
  if [[ "$count" -eq 2 && "${FAKE_ADVANCE_MAIN_DURING_TAG_AUDIT:-0}" == "1" ]]; then
    advance_main historical-tag-audit
  elif [[ "$count" -eq 2 ]]; then
    touch "$FAKE_GH_STATE/post-audit-tag-query-ready"
  fi
  exit 0
fi
if [[ "${FAKE_ADVANCE_MAIN_DURING_POST_AUDIT_TAG_QUERY:-0}" == "1" &&
      "${1:-}" == "ls-remote" && "$*" == *"refs/tags/v${FAKE_RELEASE_VERSION:?}^{}"* &&
      -f "$FAKE_GH_STATE/post-audit-tag-query-ready" &&
      ! -f "$FAKE_GH_STATE/post-audit-tag-query-advanced" ]]; then
  output=$("$real_git" "$@")
  advance_main post-audit-tag-query
  touch "$FAKE_GH_STATE/post-audit-tag-query-advanced"
  printf '%s' "$output"
  exit 0
fi
exec "$real_git" "$@"
GIT

  cat > "$bin/date" <<'DATE'
#!/usr/bin/env bash
if [[ -n "${FAKE_GH_STATE:-}" && -f "$FAKE_GH_STATE/vienna-date" ]]; then
  value=$(<"$FAKE_GH_STATE/vienna-date")
  case "${1:-}" in
    +%y.%m.%d) printf '%s\n' "$value"; exit 0 ;;
    +%Y-%m-%d)
      IFS=. read -r year month day <<<"$value"
      printf '20%s-%s-%s\n' "$year" "$month" "$day"
      exit 0
      ;;
  esac
fi
exec "${REAL_DATE:?}" "$@"
DATE

  cat > "$bin/docker" <<'DOCKER'
#!/usr/bin/env bash
[[ "$*" == "manifest inspect ghcr.io/inspr-at/paimos:${FAKE_RELEASE_VERSION:-1.0.1}" ]]
DOCKER

  cat > "$bin/gh" <<'GH'
#!/usr/bin/env bash
set -euo pipefail

state=${FAKE_GH_STATE:?}
origin=${FAKE_GH_ORIGIN:?}
release_version=${FAKE_RELEASE_VERSION:-1.0.1}
release_tag="v$release_version"
release_branch="release/$release_tag"
log="$state/calls.log"
mkdir -p "$state"
printf '%s\n' "$*" >> "$log"

merge_pr() {
  [[ -f "$state/pr-open" && -f "$state/auto-merge" ]] || return 0
  [[ ! -f "$state/wrong-method" ]] || return 0
  [[ "${FAKE_GH_DEFER_MERGE:-0}" != "1" ]] || return 0
  [[ ! -f "$state/pr-merged" ]] || return 0
  git --git-dir="$origin" merge-base --is-ancestor \
    refs/heads/main "refs/heads/$release_branch" || return 0

  work="$state/merge-work"
  git clone -q "$origin" "$work" >/dev/null 2>&1
  git -C "$work" config user.name 'Release Merge Bot'
  git -C "$work" config user.email 'release-merge@example.test'
  git -C "$work" switch -q main >/dev/null 2>&1
  git -C "$work" merge -q --squash "origin/$release_branch" >/dev/null 2>&1
  git -C "$work" commit -q --signoff -m "release: $release_tag (#1)" >/dev/null 2>&1
  FAKE_GH_SERVER_MERGE=1 git -C "$work" push -q origin main >/dev/null 2>&1
  git -C "$work" rev-parse HEAD > "$state/merge-oid"
  if [[ "${FAKE_GH_ADVANCE_AFTER_MERGE:-0}" == "1" ]]; then
    printf 'later main change\n' > "$work/later.txt"
    git -C "$work" add later.txt
    git -C "$work" commit -q --signoff -m 'unrelated change after release merge' >/dev/null 2>&1
    FAKE_GH_SERVER_MERGE=1 git -C "$work" push -q origin main >/dev/null 2>&1
  fi
  touch "$state/pr-merged"
}

sync_pr_ref() {
  [[ -f "$state/pr-open" ]] || return 0
  if git --git-dir="$origin" show-ref --verify --quiet "refs/heads/$release_branch"; then
    git --git-dir="$origin" update-ref refs/pull/1/head "refs/heads/$release_branch"
  fi
}

mutate_pr_head_if_requested() {
  [[ -f "$state/mutate-head" && -f "$state/auto-merge" && ! -f "$state/head-mutated" ]] || return 0
  work="$state/mutate-work"
  git clone -q "$origin" "$work" >/dev/null 2>&1
  git -C "$work" config user.name 'Unexpected Release Author'
  git -C "$work" config user.email 'unexpected@example.test'
  git -C "$work" switch -q "$release_branch"
  printf 'unexpected same-file change\n' >> "$work/README.md"
  git -C "$work" add README.md
  git -C "$work" commit -q --no-gpg-sign --signoff -m 'mutate release head during wait'
  git -C "$work" push -q origin "$release_branch"
  sync_pr_ref
  touch "$state/head-mutated"
}

pr_head_oid() {
  git --git-dir="$origin" rev-parse refs/pull/1/head
}

pr_merge_oid() {
  if [[ -f "$state/override-merge-oid" ]]; then
    cat "$state/override-merge-oid"
  else
    cat "$state/merge-oid"
  fi
}

case "${1:-} ${2:-}" in
  'repo view')
    if [[ "${FAKE_VIENNA_FLIP_ON_REPO_VIEW:-0}" == "1" ]]; then
      printf '%s\n' "${FAKE_VIENNA_NEXT_DAY:?}" > "$state/vienna-date"
    fi
    printf '%s\n' 'example/paimos'
    ;;
  'pr list')
    if [[ -f "$state/pr-open" ]]; then
      sync_pr_ref
      base_ref=main
      [[ -f "$state/wrong-base" ]] && base_ref=develop
      head_oid=$(pr_head_oid)
      [[ -f "$state/wrong-head" ]] && head_oid=0000000000000000000000000000000000000000
      if [[ -f "$state/pr-merged" ]]; then
        merge_oid=$(pr_merge_oid)
        method=SQUASH
        [[ -f "$state/wrong-method" ]] && method=REBASE
        auto="{\"mergeMethod\":\"$method\"}"
        [[ -f "$state/missing-auto-merge-provenance" ]] && auto=null
        printf '[{"number":1,"state":"MERGED","url":"https://example.test/pr/1","headRefName":"%s","headRefOid":"%s","baseRefName":"%s","autoMergeRequest":%s,"mergeCommit":{"oid":"%s"}}]\n' "$release_branch" "$head_oid" "$base_ref" "$auto" "$merge_oid"
      else
        method=SQUASH
        [[ -f "$state/wrong-method" ]] && method=REBASE
        auto=null
        [[ -f "$state/auto-merge" ]] && auto="{\"mergeMethod\":\"$method\"}"
        printf '[{"number":1,"state":"OPEN","url":"https://example.test/pr/1","headRefName":"%s","headRefOid":"%s","baseRefName":"%s","autoMergeRequest":%s,"mergeCommit":null}]\n' "$release_branch" "$head_oid" "$base_ref" "$auto"
      fi
    else
      printf '%s\n' '[]'
    fi
    ;;
  'pr create')
    [[ "$*" == *'--base main'* && "$*" == *"--head $release_branch"* ]]
    touch "$state/pr-open"
    sync_pr_ref
    printf '%s\n' 'https://example.test/pr/1'
    ;;
  'pr merge')
    [[ "$*" == *'--auto'* && "$*" == *'--squash'* && "$*" == *'--delete-branch'* && "$*" == *'--match-head-commit'* ]]
    touch "$state/auto-merge"
    ;;
  'pr view')
    sync_pr_ref
    mutate_pr_head_if_requested
    merge_pr
    base_ref=main
    [[ -f "$state/wrong-base" ]] && base_ref=develop
    head_oid=$(pr_head_oid)
    [[ -f "$state/wrong-head" ]] && head_oid=0000000000000000000000000000000000000000
    if [[ -f "$state/pr-merged" ]]; then
      merge_oid=$(pr_merge_oid)
      method=SQUASH
      [[ -f "$state/wrong-method" ]] && method=REBASE
      auto="{\"enabledAt\":\"now\",\"mergeMethod\":\"$method\"}"
      [[ -f "$state/missing-auto-merge-provenance" ]] && auto=null
      printf '{"number":1,"state":"MERGED","url":"https://example.test/pr/1","headRefName":"%s","headRefOid":"%s","baseRefName":"%s","mergeStateStatus":"CLEAN","autoMergeRequest":%s,"mergeCommit":{"oid":"%s"}}\n' "$release_branch" "$head_oid" "$base_ref" "$auto" "$merge_oid"
    else
      auto=null
      merge_status=CLEAN
      method=SQUASH
      [[ -f "$state/wrong-method" ]] && method=REBASE
      [[ -f "$state/auto-merge" ]] && auto="{\"enabledAt\":\"now\",\"mergeMethod\":\"$method\"}"
      git --git-dir="$origin" merge-base --is-ancestor \
        refs/heads/main "refs/heads/$release_branch" || merge_status=BEHIND
      printf '{"number":1,"state":"OPEN","url":"https://example.test/pr/1","headRefName":"%s","headRefOid":"%s","baseRefName":"%s","mergeStateStatus":"%s","autoMergeRequest":%s,"mergeCommit":null}\n' "$release_branch" "$head_oid" "$base_ref" "$merge_status" "$auto"
    fi
    ;;
  'pr checks')
    [[ "$*" == *'--required'* && "$*" == *'--json name,state,bucket,workflow'* ]]
    state_value=SUCCESS
    bucket=pass
    check_name=test
    [[ ! -f "$state/checks-unnamed" ]] || check_name=
    if [[ -f "$state/checks-pending" ]]; then
      state_value=PENDING
      bucket=pending
    elif [[ -f "$state/checks-failed" ]]; then
      state_value=FAILURE
      bucket=fail
    elif [[ -f "$state/checks-skipped" ]]; then
      state_value=SKIPPED
      bucket=skipping
    fi
    if [[ -f "$state/checks-empty" ]]; then
      printf '[]\n'
    else
      printf '[{"name":"%s","state":"%s","bucket":"%s","workflow":"ci"},{"name":"dco","state":"SUCCESS","bucket":"pass","workflow":"ci"}]\n' \
        "$check_name" "$state_value" "$bucket"
    fi
    ;;
  'run list')
    if [[ "$*" == *'--workflow backend-full.yml'* ]]; then
      head_sha=
      previous=
      for argument in "$@"; do
        if [[ "$previous" == '--commit' ]]; then
          head_sha=$argument
          break
        fi
        previous=$argument
      done
      [[ "$head_sha" =~ ^[0-9a-f]{40}$ ]]
      printf '%s\n' "$head_sha" > "$state/backend-full-head"
      if [[ "${FAKE_VIENNA_FLIP_AFTER_BACKEND:-0}" == "1" ]]; then
        printf '%s\n' "${FAKE_VIENNA_NEXT_DAY:?}" > "$state/vienna-date"
      fi
      if [[ "${FAKE_ADVANCE_MAIN_AFTER_BACKEND:-0}" == "1" ]]; then
        advance_work="$state/backend-advance-work"
        git clone -q "$origin" "$advance_work" >/dev/null 2>&1
        git -C "$advance_work" config user.name 'Protected Main Bot'
        git -C "$advance_work" config user.email 'protected-main@example.test'
        git -C "$advance_work" switch -q main
        printf 'later protected-main commit\n' > "$advance_work/later-main.txt"
        git -C "$advance_work" add later-main.txt
        git -C "$advance_work" commit -q --signoff -m 'advance protected main after assurance'
        FAKE_GH_SERVER_MERGE=1 git -C "$advance_work" push -q origin main
      fi
      conclusion=success
      [[ ! -f "$state/backend-full-failed" ]] || conclusion=failure
      printf '[{"databaseId":3,"headSha":"%s","status":"completed","conclusion":"%s","url":"https://example.test/run/backend-full"}]\n' \
        "$head_sha" "$conclusion"
    elif printf '%s\n' "$*" | grep -q 'workflowName == \\"release\\"'; then
      [[ "$*" == *"--branch $release_tag"* ]]
      printf '2\tcompleted\tsuccess\thttps://example.test/run/release\n'
    else
      [[ "$*" == *"--branch $release_tag"* ]]
      printf '1\tcompleted\tsuccess\thttps://example.test/run/ci\n'
    fi
    ;;
  'run view')
    [[ "$*" == *'run view 3'* && "$*" == *'--json jobs'* ]]
    printf '{"jobs":[{"name":"backend-full","status":"completed","conclusion":"success"}]}\n'
    ;;
  *)
    echo "unexpected fake gh call: $*" >&2
    exit 64
    ;;
esac
GH

  chmod +x "$bin/date" "$bin/docker" "$bin/gh" "$bin/git"
}

setup_repo() {
  local name="$1"
  local future_release="${2:-v1.0.1}"
  local base="$TMP_ROOT/$name"
  local origin="$base/origin.git"
  local repo="$base/repo"
  local pin_commit

  mkdir -p "$base"
  git init -q --bare "$origin"
  git init -q -b main "$repo"
  git -C "$repo" config user.name 'Release Author'
  git -C "$repo" config user.email 'release-author@example.test'
  mkdir -p "$repo/docs"
  mkdir -p "$repo/backend/contracts/fixtures/external-stage"
  mkdir -p "$repo/backend/externalstage"
  printf '1.0.0\n' > "$repo/VERSION"
  printf '<code>v1.0.0</code>\n' > "$repo/README.md"
  printf '# Changelog\n\n## [1.0.0] — 2026-01-01\n\n- Initial.\n' > "$repo/docs/CHANGELOG.md"
  printf 'VER=1.0.0\nVER=1.0.0\npaimos --version    # 1.0.0\n' > "$repo/docs/INSTALL.md"
  printf '%s\n' '{"fixture":"dependency-janus-v1"}' > \
    "$repo/backend/contracts/fixtures/external-stage/dependency-janus-v1.json"
  printf '%s\n' '{"fixture":"owner-pharos-v1"}' > \
    "$repo/backend/contracts/fixtures/external-stage/owner-pharos-v1.json"
  printf '%s\n' 'package externalstage' > "$repo/backend/externalstage/contract.go"
  write_stub_scripts "$repo"
  git -C "$repo" add .
  git -C "$repo" commit -q --signoff -m 'add external-stage v1 contract bytes'
  pin_commit=$(git -C "$repo" rev-parse HEAD)
  printf '%s\n' \
    "{\"paimos_commit\":\"$pin_commit\",\"paimos_release\":\"$future_release\"}" > \
    "$repo/backend/contracts/fixtures/external-stage/manifest-v1.json"
  git -C "$repo" add backend/contracts/fixtures/external-stage/manifest-v1.json
  git -C "$repo" commit -q --signoff -m 'pin external-stage v1 release'
  git -C "$repo" tag -a v1.0.0 -m 'release 1.0.0'
  git -C "$repo" remote add origin "$origin"
  git -C "$repo" push -q -u origin main --tags
  git --git-dir="$origin" symbolic-ref HEAD refs/heads/main
  cat > "$origin/hooks/pre-receive" <<'HOOK'
#!/usr/bin/env bash
while read -r _old _new ref; do
  if [[ "$ref" == "refs/heads/main" && "${FAKE_GH_SERVER_MERGE:-0}" != "1" ]]; then
    echo 'direct main push rejected by protected-main fixture' >&2
    exit 1
  fi
done
HOOK
  chmod +x "$origin/hooks/pre-receive"
  git -C "$repo" config commit.gpgSign true
  git -C "$repo" config tag.gpgSign true
  git -C "$repo" config user.signingkey unusable-test-key
  printf '%s\n' "$repo"
}

set_external_stage_manifest_field() {
  local repo="$1" field="$2" value="$3"
  local manifest="$repo/backend/contracts/fixtures/external-stage/manifest-v1.json"
  local next="$manifest.next"

  jq --arg field "$field" --arg value "$value" '.[$field] = $value' \
    "$manifest" > "$next"
  mv "$next" "$manifest"
}

add_external_stage_v2_contract() {
  local repo="$1" release="$2" pin_commit
  git -C "$repo" switch -q -c external-stage-v2-publication
  mkdir -p "$repo/backend/contracts/fixtures/external-stage-v2"
  printf '%s\n' '{"fixture":"owner-pharos-v2"}' > \
    "$repo/backend/contracts/fixtures/external-stage-v2/owner-pharos-v2.json"
  printf '%s\n' '{"schema":"external-stage-v2"}' > \
    "$repo/backend/contracts/external-stage-v2.schema.json"
  printf '%s\n' 'package externalstage' > "$repo/backend/externalstage/contract_v2.go"
  printf '%s\n' 'package externalstage' > "$repo/backend/externalstage/service_v2.go"
  git -C "$repo" add backend/contracts/fixtures/external-stage-v2/owner-pharos-v2.json \
    backend/contracts/external-stage-v2.schema.json backend/externalstage/contract_v2.go \
    backend/externalstage/service_v2.go
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'add external-stage v2 contract bytes'
  pin_commit=$(git -C "$repo" rev-parse HEAD)
  printf '%s\n' \
    "{\"schema_major\":2,\"media_type\":\"application/vnd.paimos.external-stage.v2+json\",\"paimos_commit\":\"$pin_commit\",\"paimos_release\":\"$release\"}" > \
    "$repo/backend/contracts/fixtures/external-stage-v2/manifest-v2.json"
  git -C "$repo" add backend/contracts/fixtures/external-stage-v2/manifest-v2.json
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'pin external-stage v2 release'
  git -C "$repo" switch -q main
  git -C "$repo" merge -q --no-ff --no-gpg-sign --signoff external-stage-v2-publication \
    -m 'merge external-stage v2 publication'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
}

prepend_release_notes() {
  local repo="$1"
  local version="${2:-1.0.1}"
  local tmp="$repo/docs/CHANGELOG.md.next"
  {
    printf '# Changelog\n\n## [%s] — 2026-01-02\n\n### Fixed\n\n- Protected releases.\n\n' "$version"
    sed -n '/^## \[1.0.0\]/,$p' "$repo/docs/CHANGELOG.md"
  } > "$tmp"
  mv "$tmp" "$repo/docs/CHANGELOG.md"
}

prepend_release_notes_preserving_history() {
  local repo="$1" version="$2"
  local tmp="$repo/docs/CHANGELOG.md.next"
  {
    printf '# Changelog\n\n## [%s] — 2026-01-02\n\n### Fixed\n\n- Protected releases.\n\n' "$version"
    sed -n '/^## \[/,$p' "$repo/docs/CHANGELOG.md"
  } > "$tmp"
  mv "$tmp" "$repo/docs/CHANGELOG.md"
}

commit_unreleased_notes() {
  local repo="$1"
  local tmp="$repo/docs/CHANGELOG.md.next"
  {
    printf '# Changelog\n\n## [Unreleased]\n\n### Added\n\n- Pending feature.\n\n'
    sed -n '/^## \[1.0.0\]/,$p' "$repo/docs/CHANGELOG.md"
    printf '\n## [Unreleased]\n\n- Historical unreleased heading.\n\n'
    printf '## [0.9.0] — 2025-12-01\n\n- Older release.\n'
  } > "$tmp"
  mv "$tmp" "$repo/docs/CHANGELOG.md"
  git -C "$repo" add docs/CHANGELOG.md
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'add unreleased notes'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
}

run_release() {
  local repo="$1" state="$2" origin
  shift 2
  (
    cd "$repo"
    origin=$(git remote get-url origin)
    PATH="$TMP_ROOT/fake-bin:$PATH" \
      FAKE_GH_STATE="$state" \
      FAKE_GH_ORIGIN="$origin" \
      FAKE_RELEASE_VERSION="${FAKE_RELEASE_VERSION:-1.0.1}" \
      FAKE_GATE_LOG="${FAKE_GATE_LOG:-}" \
      FAKE_CLAIMS_FAIL_MARKER="${FAKE_CLAIMS_FAIL_MARKER:-}" \
      FAKE_CLAIMS_FAIL_ON_BRANCH="${FAKE_CLAIMS_FAIL_ON_BRANCH:-}" \
      FAKE_CLAIMS_FAIL_ON_CONTENT="${FAKE_CLAIMS_FAIL_ON_CONTENT:-0}" \
      FAKE_VIENNA_FLIP_ON_REPO_VIEW="${FAKE_VIENNA_FLIP_ON_REPO_VIEW:-0}" \
      FAKE_VIENNA_FLIP_AFTER_BACKEND="${FAKE_VIENNA_FLIP_AFTER_BACKEND:-0}" \
      FAKE_VIENNA_NEXT_DAY="${FAKE_VIENNA_NEXT_DAY:-}" \
      FAKE_ADVANCE_MAIN_AFTER_BACKEND="${FAKE_ADVANCE_MAIN_AFTER_BACKEND:-0}" \
      FAKE_ADVANCE_MAIN_DURING_TAG_AUDIT="${FAKE_ADVANCE_MAIN_DURING_TAG_AUDIT:-0}" \
      FAKE_ADVANCE_MAIN_DURING_POST_AUDIT_TAG_QUERY="${FAKE_ADVANCE_MAIN_DURING_POST_AUDIT_TAG_QUERY:-0}" \
      RELEASE_MERGE_TIMEOUT="${RELEASE_MERGE_TIMEOUT:-10}" \
      RELEASE_MERGE_POLL=1 \
      ./scripts/release.sh "$@"
  )
}

assert_one_pr() {
  local state="$1"
  [[ $(grep -c '^pr create' "$state/calls.log") -eq 1 ]] || fail 'release PR was duplicated'
}

write_recovery_receipt() {
  local repo="$1" release="$2" pull_request="$3" approved_head="$4" merge_commit="$5" reason="$6"
  local file_release="${7:-$release}"
  local path="$repo/scripts/release/recovery/$file_release.json"

  mkdir -p "$(dirname "$path")"
  jq -n \
    --arg release "$release" \
    --argjson pull_request "$pull_request" \
    --arg approved_head "$approved_head" \
    --arg merge_commit "$merge_commit" \
    --arg incident_reason "$reason" \
    '{
      schema_version: 1,
      release: $release,
      pull_request: $pull_request,
      approved_head: $approved_head,
      merge_commit: $merge_commit,
      incident_reason: $incident_reason
    }' > "$path"
}

publish_recovery_receipt() {
  local repo="$1" label="$2" release="$3" pull_request="$4" approved_head="$5" merge_commit="$6" reason="$7"
  local file_release="${8:-$release}"

  write_recovery_receipt "$repo" "$release" "$pull_request" "$approved_head" "$merge_commit" "$reason" "$file_release"
  git -C "$repo" add "scripts/release/recovery/$file_release.json"
  git -C "$repo" commit -q --no-gpg-sign --signoff -m "$label"
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q --force origin HEAD:main
}

assert_recovery_rejected() {
  local repo="$1" state="$2" origin="$3" label="$4" expected="${5:-}"
  local version="${6:-$IMMEDIATE_RECOVERY_VERSION}" output

  output=$(mktemp "$state/rejection.XXXXXX")
  if FAKE_RELEASE_VERSION="$version" run_release "$repo" "$state" "$version" --no-edit >"$output" 2>&1; then
    fail "release recovery accepted $label"
  fi
  if [[ -n "$expected" ]] && ! grep -qF "$expected" "$output"; then
    cat "$output" >&2
    fail "release recovery rejected $label at the wrong gate"
  fi
  rm "$output"
  ! git --git-dir="$origin" show-ref --verify --quiet "refs/tags/v$version" ||
    fail "release recovery tagged after rejecting $label"
}

test_missing_auto_merge_recovery_receipt() {
  local repo state origin head merge receipt_work receipt_path valid_main
  local base_parent base_tree head_tree side_parent wrong_parent wrong_tree nonancestor output bad_reason
  repo=$(setup_repo recovery-receipt "v$IMMEDIATE_RECOVERY_VERSION")
  state="$TMP_ROOT/recovery-receipt/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo" "$IMMEDIATE_RECOVERY_VERSION"
  mkdir -p "$state"
  touch "$state/missing-auto-merge-provenance"

  if FAKE_RELEASE_VERSION="$IMMEDIATE_RECOVERY_VERSION" \
     run_release "$repo" "$state" "$IMMEDIATE_RECOVERY_VERSION" --no-edit >/dev/null 2>&1; then
    fail 'recovery fixture unexpectedly completed its missing-provenance merge'
  fi
  [[ -f "$state/pr-merged" ]] || fail 'recovery fixture did not produce the merged PR'
  head=$(git --git-dir="$origin" rev-parse refs/pull/1/head)
  merge=$(<"$state/merge-oid")
  assert_recovery_rejected "$repo" "$state" "$origin" 'a missing receipt' \
    'missing tracked release recovery receipt on origin/main'

  write_recovery_receipt "$repo" "v$IMMEDIATE_RECOVERY_VERSION" 1 "$head" "$merge" "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  assert_recovery_rejected "$repo" "$state" "$origin" 'an untracked receipt' \
    'working tree is dirty while reusing the canonical release PR'
  rm -rf "$repo/scripts/release/recovery"

  receipt_work="$TMP_ROOT/recovery-receipt/receipt-work"
  git clone -q "$origin" "$receipt_work"
  git -C "$receipt_work" config user.name 'Recovery Reviewer'
  git -C "$receipt_work" config user.email 'recovery-reviewer@example.test'
  publish_recovery_receipt "$receipt_work" 'add reviewed recovery receipt' \
    "v$IMMEDIATE_RECOVERY_VERSION" 1 "$head" "$merge" "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  valid_main=$(git -C "$receipt_work" rev-parse HEAD)
  receipt_path="$receipt_work/scripts/release/recovery/v$IMMEDIATE_RECOVERY_VERSION.json"

  printf '\n' >> "$receipt_path"
  assert_recovery_rejected "$receipt_work" "$state" "$origin" 'a dirty tracked receipt' \
    'working tree is dirty while reusing the canonical release PR'
  git -C "$receipt_work" restore "scripts/release/recovery/v$IMMEDIATE_RECOVERY_VERSION.json"

  publish_recovery_receipt "$receipt_work" 'mutate recovery release' \
    "v$MANUAL_RECOVERY_VERSION" 1 "$head" "$merge" "$MANUAL_RECOVERY_REASON" "v$IMMEDIATE_RECOVERY_VERSION"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a wrong release receipt' \
    "targets v$MANUAL_RECOVERY_VERSION instead of v$IMMEDIATE_RECOVERY_VERSION"

  publish_recovery_receipt "$receipt_work" 'mutate recovery pull request' \
    "v$IMMEDIATE_RECOVERY_VERSION" 2 "$head" "$merge" "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a wrong PR receipt' \
    'targets PR 2 instead of PR 1'

  publish_recovery_receipt "$receipt_work" 'mutate recovery head' \
    "v$IMMEDIATE_RECOVERY_VERSION" 1 0000000000000000000000000000000000000000 "$merge" "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a wrong approved-head receipt' \
    'approved head does not match live validated PR head'

  publish_recovery_receipt "$receipt_work" 'mutate recovery merge' \
    "v$IMMEDIATE_RECOVERY_VERSION" 1 "$head" 0000000000000000000000000000000000000000 "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a wrong merge receipt' \
    'merge does not match live GitHub PR JSON'

  for bad_reason in \
    "$MANUAL_RECOVERY_REASON" \
    '' \
    Canonical_auto_merge_immediate_merge_post_merge_request_missing \
    ' canonical_auto_merge_immediate_merge_post_merge_request_missing' \
    canonical_auto_merge_immediate_merge_post_merge_request_missing_ \
    canonical_auto_merge_immediate_merge_post_merge_request_miss; do
    publish_recovery_receipt "$receipt_work" 'mutate recovery reason' \
      "v$IMMEDIATE_RECOVERY_VERSION" 1 "$head" "$merge" "$bad_reason"
    assert_recovery_rejected "$repo" "$state" "$origin" "mismatched incident reason [$bad_reason]" \
      'carries an unrecognized incident reason'
  done

  publish_recovery_receipt "$receipt_work" 'restore exact recovery receipt' \
    "v$IMMEDIATE_RECOVERY_VERSION" 1 "$head" "$merge" "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  jq '.unexpected = "not allowed"' "$receipt_path" > "$receipt_path.next"
  mv "$receipt_path.next" "$receipt_path"
  git -C "$receipt_work" add "scripts/release/recovery/v$IMMEDIATE_RECOVERY_VERSION.json"
  git -C "$receipt_work" commit -q --no-gpg-sign --signoff -m 'add unexpected recovery field'
  FAKE_GH_SERVER_MERGE=1 git -C "$receipt_work" push -q origin HEAD:main
  assert_recovery_rejected "$repo" "$state" "$origin" 'a receipt with unreviewed fields' \
    'invalid release recovery receipt'

  publish_recovery_receipt "$receipt_work" 'restore exact recovery receipt again' \
    "v$IMMEDIATE_RECOVERY_VERSION" 1 "$head" "$merge" "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  valid_main=$(git -C "$receipt_work" rev-parse HEAD)

  touch "$state/checks-pending"
  assert_recovery_rejected "$repo" "$state" "$origin" 'pending required checks' \
    'approved PR head has missing, pending, or failed required checks'
  rm "$state/checks-pending"
  touch "$state/checks-failed"
  assert_recovery_rejected "$repo" "$state" "$origin" 'failed required checks' \
    'approved PR head has missing, pending, or failed required checks'
  rm "$state/checks-failed"
  touch "$state/checks-empty"
  assert_recovery_rejected "$repo" "$state" "$origin" 'an empty required-check set' \
    'approved PR head has missing, pending, or failed required checks'
  rm "$state/checks-empty"

  git -C "$receipt_work" tag -a --no-sign "v$IMMEDIATE_RECOVERY_VERSION" "$merge" -m 'premature recovery tag'
  git -C "$receipt_work" push -q origin "v$IMMEDIATE_RECOVERY_VERSION"
  output=$(mktemp "$state/rejection-tag.XXXXXX")
  if FAKE_RELEASE_VERSION="$IMMEDIATE_RECOVERY_VERSION" \
     run_release "$repo" "$state" "$IMMEDIATE_RECOVERY_VERSION" --no-edit >"$output" 2>&1; then
    fail 'release recovery accepted a pre-existing release tag'
  fi
  grep -qF "release recovery requires absent tag v$IMMEDIATE_RECOVERY_VERSION" "$output" ||
    fail 'pre-existing recovery tag was rejected at the wrong gate'
  rm "$output"
  git -C "$receipt_work" tag -d "v$IMMEDIATE_RECOVERY_VERSION" >/dev/null
  git --git-dir="$origin" tag -d "v$IMMEDIATE_RECOVERY_VERSION" >/dev/null
  git -C "$repo" tag -d "v$IMMEDIATE_RECOVERY_VERSION" >/dev/null

  base_parent=$(git --git-dir="$origin" rev-parse "$merge^")
  base_tree=$(git --git-dir="$origin" rev-parse "$base_parent^{tree}")
  head_tree=$(git --git-dir="$origin" rev-parse "$head^{tree}")
  side_parent=$(printf 'unrelated side parent\n' | git -C "$receipt_work" commit-tree "$base_tree" -p "$base_parent")
  wrong_parent=$(printf 'non-squash merge\n' | git -C "$receipt_work" commit-tree "$head_tree" -p "$base_parent" -p "$side_parent")
  git -C "$receipt_work" switch -q --detach "$wrong_parent"
  publish_recovery_receipt "$receipt_work" 'pin a two-parent merge' \
    "v$IMMEDIATE_RECOVERY_VERSION" 1 "$head" "$wrong_parent" "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  printf '%s\n' "$wrong_parent" > "$state/override-merge-oid"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a two-parent merge' \
    'release recovery merge is not a one-parent squash'

  wrong_tree=$(printf 'wrong release tree\n' | git -C "$receipt_work" commit-tree "$base_tree" -p "$base_parent")
  git -C "$receipt_work" switch -q --detach "$wrong_tree"
  publish_recovery_receipt "$receipt_work" 'pin a wrong-tree merge' \
    "v$IMMEDIATE_RECOVERY_VERSION" 1 "$head" "$wrong_tree" "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  printf '%s\n' "$wrong_tree" > "$state/override-merge-oid"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a merge whose tree differs from the approved head' \
    'release recovery merge tree differs from approved PR head'

  nonancestor=$(printf 'nonancestor release merge\n' | git -C "$receipt_work" commit-tree "$head_tree" -p "$base_parent")
  git -C "$receipt_work" update-ref refs/heads/nonancestor-recovery-test "$nonancestor"
  git -C "$repo" fetch -q "$receipt_work" \
    refs/heads/nonancestor-recovery-test:refs/recovery-tests/nonancestor
  git -C "$receipt_work" switch -q --detach "$merge"
  publish_recovery_receipt "$receipt_work" 'pin a nonancestor merge' \
    "v$IMMEDIATE_RECOVERY_VERSION" 1 "$head" "$nonancestor" "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  printf '%s\n' "$nonancestor" > "$state/override-merge-oid"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a merge outside current main history' \
    'release recovery merge is not an ancestor of current origin/main'

  rm "$state/override-merge-oid"
  git --git-dir="$origin" update-ref refs/heads/main "$valid_main"
  FAKE_RELEASE_VERSION="$IMMEDIATE_RECOVERY_VERSION" \
    run_release "$repo" "$state" "$IMMEDIATE_RECOVERY_VERSION" --no-edit >/dev/null
  grep -q '^pr checks 1 .*--required .*--json name,state,bucket,workflow' "$state/calls.log" ||
    fail 'release recovery did not query required checks for the approved PR head'
  [[ $(git --git-dir="$origin" rev-parse "refs/tags/v$IMMEDIATE_RECOVERY_VERSION^{}") == "$merge" ]] ||
    fail 'exact recovery receipt tagged a commit other than the reviewed squash merge'
}

test_existing_manual_recovery_reason_remains_accepted() {
  local repo state origin head merge receipt_work
  repo=$(setup_repo manual-recovery-reason "v$MANUAL_RECOVERY_VERSION")
  state="$TMP_ROOT/manual-recovery-reason/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo" "$MANUAL_RECOVERY_VERSION"
  mkdir -p "$state"
  touch "$state/missing-auto-merge-provenance"

  if FAKE_RELEASE_VERSION="$MANUAL_RECOVERY_VERSION" \
     run_release "$repo" "$state" "$MANUAL_RECOVERY_VERSION" --no-edit >/dev/null 2>&1; then
    fail 'manual-recovery fixture unexpectedly completed its missing-provenance merge'
  fi
  head=$(git --git-dir="$origin" rev-parse refs/pull/1/head)
  merge=$(<"$state/merge-oid")
  receipt_work="$TMP_ROOT/manual-recovery-reason/receipt-work"
  git clone -q "$origin" "$receipt_work"
  git -C "$receipt_work" config user.name 'Recovery Reviewer'
  git -C "$receipt_work" config user.email 'recovery-reviewer@example.test'

  publish_recovery_receipt "$receipt_work" 'swap immediate reason onto manual recovery' \
    "v$MANUAL_RECOVERY_VERSION" 1 "$head" "$merge" "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  assert_recovery_rejected "$repo" "$state" "$origin" \
    'the v5.20 immediate-auto-merge reason on v5.19' \
    'carries an unrecognized incident reason' "$MANUAL_RECOVERY_VERSION"

  publish_recovery_receipt "$receipt_work" 'add manual recovery receipt' \
    "v$MANUAL_RECOVERY_VERSION" 1 "$head" "$merge" "$MANUAL_RECOVERY_REASON"

  FAKE_RELEASE_VERSION="$MANUAL_RECOVERY_VERSION" \
    run_release "$repo" "$state" "$MANUAL_RECOVERY_VERSION" --no-edit >/dev/null
  [[ $(git --git-dir="$origin" rev-parse "refs/tags/v$MANUAL_RECOVERY_VERSION^{}") == "$merge" ]] ||
    fail 'manual recovery reason tagged a commit other than the reviewed squash merge'
}

test_committed_recovery_receipts_are_exact() {
  jq -e --arg reason "$MANUAL_RECOVERY_REASON" '
    . == {
      schema_version: 1,
      release: "v5.19.0",
      pull_request: 159,
      approved_head: "6aa1ab5f1e32cd75ac4f669911ad6f40a327b15d",
      merge_commit: "f9607563ad73c844823dcea447df92a9ebf5f9d0",
      incident_reason: $reason
    }
  ' "$ROOT/scripts/release/recovery/v5.19.0.json" >/dev/null ||
    fail 'committed v5.19.0 recovery receipt drifted'

  jq -e --arg reason "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON" '
    . == {
      schema_version: 1,
      release: "v5.20.0",
      pull_request: 163,
      approved_head: "5d8aa5876b6924b6533bd3ff6b1dfdb1e91effe2",
      merge_commit: "a202cb7992aef948fc1fd391bbee63f2c946400c",
      incident_reason: $reason
    }
  ' "$ROOT/scripts/release/recovery/v5.20.0.json" >/dev/null ||
    fail 'committed v5.20.0 recovery receipt is missing or drifted'

  jq -e --arg reason "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON" '
    . == {
      schema_version: 1,
      release: "v26.09.01",
      pull_request: 193,
      approved_head: "38803cef876cab1595141ee103cc56217a42d39c",
      merge_commit: "47a8151719f7fb04d6d9a0677ced18e2b122e05b",
      incident_reason: $reason
    }
  ' "$ROOT/scripts/release/recovery/v26.09.01.json" >/dev/null ||
    fail 'committed v26.09.01 recovery receipt is missing or drifted'

  jq -e --arg reason "$MANUAL_RECOVERY_REASON" '
    . == {
      schema_version: 1,
      release: "v26.09.02",
      pull_request: 201,
      approved_head: "84e253d5c9f4fb7da48e39e43b2571d83c868073",
      merge_commit: "2abd06b307bc832214341de50b765aae9b833f09",
      incident_reason: $reason
    }
  ' "$ROOT/scripts/release/recovery/v26.09.02.json" >/dev/null ||
    fail 'committed v26.09.02 recovery receipt is missing or drifted'
}

test_calendar_missing_provenance_receipt_recovery() {
  local version="$1" reason="$2" fixture="$3" bad_reason
  local repo state origin head merge receipt_work valid_main output

  repo=$(setup_repo "$fixture" "v$version")
  state="$TMP_ROOT/$fixture/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo" "$version"
  mkdir -p "$state"
  printf '%s\n' "$version" > "$state/vienna-date"
  touch "$state/missing-auto-merge-provenance"

  output="$TMP_ROOT/$fixture/missing-output"
  if FAKE_RELEASE_VERSION="$version" \
     run_release "$repo" "$state" "$version" --no-edit >"$output" 2>&1; then
    fail "$version recovery accepted a missing receipt"
  fi
  grep -qF 'release PR is missing protected squash auto-merge provenance' "$output" ||
    fail "$version missing-provenance interruption reported the wrong failure"
  [[ -f "$state/pr-merged" ]] ||
    fail "$version missing-receipt fixture did not produce the protected squash merge"
  head=$(git --git-dir="$origin" rev-parse refs/pull/1/head)
  merge=$(<"$state/merge-oid")
  assert_recovery_rejected "$repo" "$state" "$origin" \
    'a missing calendar recovery receipt' \
    'missing tracked release recovery receipt on origin/main' "$version"

  receipt_work="$TMP_ROOT/$fixture/receipt-work"
  git clone -q "$origin" "$receipt_work"
  git -C "$receipt_work" config user.name 'Recovery Reviewer'
  git -C "$receipt_work" config user.email 'recovery-reviewer@example.test'
  printf '\n# Reviewed calendar receipt recovery.\n' >> "$receipt_work/scripts/release.sh"
  printf '%s\n' '#!/usr/bin/env bash' '# Reviewed calendar recovery fixture.' > \
    "$receipt_work/scripts/test-release.sh"
  git -C "$receipt_work" add scripts/release.sh scripts/test-release.sh
  git -C "$receipt_work" commit -q --no-gpg-sign --signoff \
    -m 'add reviewed calendar recovery verifier'

  publish_recovery_receipt "$receipt_work" 'add wrong calendar recovery receipt' \
    "v$version" 2 "$head" "$merge" "$reason"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a wrong calendar recovery receipt' \
    'targets PR 2 instead of PR 1' "$version"

  publish_recovery_receipt "$receipt_work" 'mutate calendar recovery head' \
    "v$version" 1 0000000000000000000000000000000000000000 "$merge" "$reason"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a wrong calendar recovery head' \
    'approved head does not match live validated PR head' "$version"

  publish_recovery_receipt "$receipt_work" 'mutate calendar recovery merge' \
    "v$version" 1 "$head" 0000000000000000000000000000000000000000 "$reason"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a wrong calendar recovery merge' \
    'merge does not match live GitHub PR JSON' "$version"

  bad_reason="$MANUAL_RECOVERY_REASON"
  [[ "$reason" != "$MANUAL_RECOVERY_REASON" ]] || bad_reason="$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON"
  publish_recovery_receipt "$receipt_work" 'mutate calendar recovery reason' \
    "v$version" 1 "$head" "$merge" "$bad_reason"
  assert_recovery_rejected "$repo" "$state" "$origin" 'a wrong calendar recovery reason' \
    'carries an unrecognized incident reason' "$version"

  publish_recovery_receipt "$receipt_work" 'add exact calendar recovery receipt' \
    "v$version" 1 "$head" "$merge" "$reason"
  valid_main=$(git -C "$receipt_work" rev-parse HEAD)

  printf '%s\n' 'unrelated post-release source' > "$receipt_work/unrelated.txt"
  git -C "$receipt_work" add unrelated.txt
  git -C "$receipt_work" commit -q --no-gpg-sign --signoff \
    -m 'add unrelated post-release source'
  FAKE_GH_SERVER_MERGE=1 git -C "$receipt_work" push -q origin HEAD:main
  assert_recovery_rejected "$repo" "$state" "$origin" \
    'an unrelated calendar recovery delta' \
    'audited release recovery contains an unrelated file: unrelated.txt' "$version"

  git --git-dir="$origin" update-ref refs/heads/main "$valid_main"
  FAKE_RELEASE_VERSION="$version" \
    run_release "$repo" "$state" "$version" --no-edit >/dev/null
  [[ $(git --git-dir="$origin" rev-parse "refs/tags/v$version^{}") == "$merge" ]] ||
    fail "$version receipt recovery did not tag the canonical protected release merge"
}

test_protected_release_and_resume_states() {
  local repo state origin merge_oid tag_oid output
  repo=$(setup_repo success)
  state="$TMP_ROOT/success/gh-state"
  output="$TMP_ROOT/success/release-output"
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo"

  FAKE_GH_ADVANCE_AFTER_MERGE=1 run_release "$repo" "$state" patch --no-edit >"$output" 2>&1
  merge_oid=$(<"$state/merge-oid")
  tag_oid=$(git --git-dir="$origin" rev-parse 'refs/tags/v1.0.1^{}')
  [[ "$tag_oid" == "$merge_oid" ]] || fail 'tag does not point at protected-main merge commit'
  [[ "$tag_oid" != "$(git --git-dir="$origin" rev-parse refs/heads/main)" ]] ||
    fail 'tag followed a later main commit instead of the release PR merge'
  [[ $(git --git-dir="$origin" show refs/pull/1/head:VERSION) == '1.0.1' ]] || fail 'release PR head version mismatch'
  git --git-dir="$origin" show -s --format='%B' refs/pull/1/head | grep -q 'Signed-off-by: Release Author <release-author@example.test>' || fail 'release commit lacks DCO sign-off'
  grep -q '^pr checks 1 .*--required .*--json name,state,bucket,workflow' "$state/calls.log" ||
    fail 'normal auto-merge path did not inspect exact-head required checks'
  grep -q 'Required checks already green for exact release head.*waiting only for protected merge' "$output" ||
    fail 'normal auto-merge path did not distinguish green checks from the remaining merge wait'
  ! git -C "$repo" show-ref --verify --quiet refs/heads/release/v1.0.1 || fail 'local release branch was not cleaned up'
  ! git --git-dir="$origin" show-ref --verify --quiet refs/heads/release/v1.0.1 || fail 'remote release branch was not cleaned up'
  assert_one_pr "$state"

  printf 'dirty\n' > "$repo/unrelated.tmp"
  if run_release "$repo" "$state" 1.0.1 --no-edit >/dev/null 2>&1; then
    fail 'release accepted a dirty tree while resuming a merged PR'
  fi
  rm "$repo/unrelated.tmp"

  git -C "$repo" tag -d v1.0.1 >/dev/null
  git --git-dir="$origin" tag -d v1.0.1 >/dev/null
  run_release "$repo" "$state" 1.0.1 --no-edit >/dev/null
  [[ $(git --git-dir="$origin" rev-parse 'refs/tags/v1.0.1^{}') == "$merge_oid" ]] || fail 'merged-but-untagged resume tagged the wrong commit'
  assert_one_pr "$state"

  run_release "$repo" "$state" 1.0.1 --no-edit >/dev/null
  assert_one_pr "$state"
}

test_exhaustive_backend_failure_blocks_tag_creation() {
  local repo state origin
  repo=$(setup_repo backend-full-failure)
  state="$TMP_ROOT/backend-full-failure/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo"
  mkdir -p "$state"
  touch "$state/backend-full-failed"

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release created a tag without exact-head exhaustive backend assurance'
  fi
  ! git --git-dir="$origin" show-ref --verify --quiet refs/tags/v1.0.1 ||
    fail 'failed exact-head exhaustive backend assurance still created a release tag'
  grep -q '^run list .*--workflow backend-full.yml .*--commit ' "$state/calls.log" ||
    fail 'release did not query exact-head exhaustive backend assurance before tagging'
  [[ "$(<"$state/backend-full-head")" == "$(<"$state/merge-oid")" ]] ||
    fail 'release queried exhaustive backend assurance for a head other than its protected merge'
}

test_unnamed_required_check_is_not_reused_as_green() {
  local repo state output
  repo=$(setup_repo unnamed-required-check)
  state="$TMP_ROOT/unnamed-required-check/gh-state"
  output="$TMP_ROOT/unnamed-required-check/output"
  prepend_release_notes "$repo"
  mkdir -p "$state"
  touch "$state/checks-unnamed"

  run_release "$repo" "$state" patch --no-edit >"$output" 2>&1
  ! grep -q 'Required checks already green for exact release head' "$output" ||
    fail 'release reused unnamed required-check evidence as exact-head green'
}

test_open_pr_is_reused_after_timeout() {
  local repo state gate_log claim_marker
  repo=$(setup_repo open-resume)
  state="$TMP_ROOT/open-resume/gh-state"
  prepend_release_notes "$repo"

  if FAKE_GH_DEFER_MERGE=1 RELEASE_MERGE_TIMEOUT=2 run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release unexpectedly succeeded while merge was deferred'
  fi
  assert_one_pr "$state"
  touch "$state/wrong-head"
  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release reused a PR whose head differed from the remote branch'
  fi
  rm "$state/wrong-head"
  touch "$state/wrong-base"
  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release reused a PR targeting the wrong base branch'
  fi
  rm "$state/wrong-base"
  touch "$state/wrong-method"
  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted non-squash auto-merge provenance'
  fi
  rm "$state/wrong-method"
  (
    cd "$repo"
    git switch -q main
    printf 'main advanced\n' > main-only.txt
    git add main-only.txt
    git commit -q --no-gpg-sign --signoff -m 'main advances during release checks'
    FAKE_GH_SERVER_MERGE=1 git push -q origin main
  )
  gate_log="$state/gates.log"
  claim_marker="$state/fail-claims"
  touch "$claim_marker"
  if FAKE_GATE_LOG="$gate_log" \
     FAKE_CLAIMS_FAIL_MARKER="$claim_marker" \
     FAKE_CLAIMS_FAIL_ON_BRANCH=release/v1.0.1 \
     run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted a behind-main sync whose claims gate failed'
  fi
  rm "$claim_marker"
  FAKE_GATE_LOG="$gate_log" run_release "$repo" "$state" patch --no-edit >/dev/null
  [[ $(grep -c '^claims$' "$gate_log") -ge 2 ]] || fail 'claims gate was not rerun for the accepted release head'
  assert_one_pr "$state"
  git --git-dir="$(git -C "$repo" remote get-url origin)" show -s --format='%B' refs/pull/1/head |
    grep -q 'Signed-off-by: Release Author <release-author@example.test>' ||
    fail 'behind-branch sync commit lacks DCO sign-off'
}

test_mid_wait_head_mutation_is_rejected() {
  local repo state
  repo=$(setup_repo head-mutation)
  state="$TMP_ROOT/head-mutation/gh-state"
  prepend_release_notes "$repo"
  mkdir -p "$state"
  touch "$state/mutate-head"

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted a PR head that changed during the merge wait'
  fi
  [[ -f "$state/head-mutated" ]] || fail 'head-mutation fixture did not execute'
  ! git --git-dir="$(git -C "$repo" remote get-url origin)" show-ref --verify --quiet refs/tags/v1.0.1 ||
    fail 'release tagged a mutated PR head'
}

test_local_pre_push_interruption_resumes() {
  local repo state origin
  repo=$(setup_repo local-resume)
  state="$TMP_ROOT/local-resume/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  awk 'BEGIN {for (i = 1; i <= 6000; i++) print "- Historical release note " i}' >> "$repo/docs/CHANGELOG.md"
  git -C "$repo" add docs/CHANGELOG.md
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'add production-scale changelog history'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
  prepend_release_notes "$repo"

  if RELEASE_TEST_FAILPOINT=before-branch-push run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'pre-push interruption fixture unexpectedly completed'
  fi
  [[ $(git -C "$repo" rev-parse --abbrev-ref HEAD) == 'release/v1.0.1' ]] || fail 'interruption did not leave the prepared local branch'
  [[ -z $(git -C "$repo" status --porcelain) ]] || fail 'interrupted prepared branch is dirty'
  ! git --git-dir="$origin" show-ref --verify --quiet refs/heads/release/v1.0.1 || fail 'interruption unexpectedly pushed the branch'

  run_release "$repo" "$state" patch --no-edit >/dev/null
  [[ $(git --git-dir="$origin" rev-parse 'refs/tags/v1.0.1^{}') == "$(<"$state/merge-oid")" ]] ||
    fail 'pre-push resume tagged the wrong commit'
}

test_same_file_drift_is_rejected() {
  local repo state
  repo=$(setup_repo same-file-drift)
  state="$TMP_ROOT/same-file-drift/gh-state"
  prepend_release_notes "$repo"
  if RELEASE_TEST_FAILPOINT=before-branch-push run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'same-file drift setup unexpectedly completed'
  fi
  printf 'malicious-but-marker-preserving change\n' >> "$repo/README.md"
  git -C "$repo" add README.md
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'drift within an expected release file'
  git -C "$repo" push -q -u origin release/v1.0.1
  git -C "$repo" switch -q main

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted non-deterministic drift within README.md'
  fi
}

test_trailing_newline_drift_is_rejected() {
  local repo state
  repo=$(setup_repo newline-drift)
  state="$TMP_ROOT/newline-drift/gh-state"
  prepend_release_notes "$repo"
  if RELEASE_TEST_FAILPOINT=before-branch-push run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'newline drift setup unexpectedly completed'
  fi
  printf '\n' >> "$repo/VERSION"
  git -C "$repo" add VERSION
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'add invisible release-file newline drift'
  git -C "$repo" push -q -u origin release/v1.0.1
  git -C "$repo" switch -q main

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted trailing-newline drift in VERSION'
  fi
}

test_missing_changelog_history_newline_is_rejected() {
  local repo state changelog
  repo=$(setup_repo changelog-newline-drift)
  state="$TMP_ROOT/changelog-newline-drift/gh-state"
  prepend_release_notes "$repo"
  if RELEASE_TEST_FAILPOINT=before-branch-push run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'changelog newline drift setup unexpectedly completed'
  fi
  changelog=$(<"$repo/docs/CHANGELOG.md")
  printf '%s' "$changelog" > "$repo/docs/CHANGELOG.md"
  git -C "$repo" add docs/CHANGELOG.md
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'remove prior changelog history newline'
  git -C "$repo" push -q -u origin release/v1.0.1
  git -C "$repo" switch -q main

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted a missing final newline in prior CHANGELOG history'
  fi
}

test_reused_pr_gates_the_pinned_head() {
  local repo state origin old_main external
  repo=$(setup_repo pinned-head-gates)
  state="$TMP_ROOT/pinned-head-gates/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  old_main=$(git -C "$repo" rev-parse main)
  external="$TMP_ROOT/pinned-head-gates/external"
  git clone -q "$origin" "$external"
  git -C "$external" config user.name 'Main Author'
  git -C "$external" config user.email 'main@example.test'
  printf 'broken claim fixture\n' > "$external/claims-broken"
  git -C "$external" add claims-broken
  git -C "$external" commit -q --no-gpg-sign --signoff -m 'introduce failing claim state'
  FAKE_GH_SERVER_MERGE=1 git -C "$external" push -q origin main
  git -C "$repo" fetch -q origin main
  git -C "$repo" merge -q --ff-only origin/main
  prepend_release_notes "$repo"

  if FAKE_GH_DEFER_MERGE=1 RELEASE_MERGE_TIMEOUT=2 run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'reused-head gate setup unexpectedly merged'
  fi
  git -C "$repo" switch -q main
  git -C "$repo" reset -q --hard "$old_main"
  if FAKE_CLAIMS_FAIL_ON_CONTENT=1 run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release gated a reused PR against stale local main instead of the pinned head'
  fi
}

test_unsigned_merged_resume_is_rejected() {
  local repo state origin work
  repo=$(setup_repo unsigned-resume)
  state="$TMP_ROOT/unsigned-resume/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo"
  run_release "$repo" "$state" patch --no-edit >/dev/null

  work="$TMP_ROOT/unsigned-resume/rewrite"
  git clone -q "$origin" "$work"
  git -C "$work" config user.name 'Unsigned Author'
  git -C "$work" config user.email 'unsigned@example.test'
  git -C "$work" fetch -q origin refs/pull/1/head
  git -C "$work" switch -q --detach FETCH_HEAD
  git -C "$work" commit -q --amend --no-gpg-sign -m 'unsigned release head'
  git -C "$work" push -q --force origin HEAD:refs/pull/1/head

  if run_release "$repo" "$state" 1.0.1 --no-edit >/dev/null 2>&1; then
    fail 'merged resume accepted an unsigned PR head'
  fi
}

test_remote_branch_drift_is_rejected() {
  local repo state
  repo=$(setup_repo branch-drift)
  state="$TMP_ROOT/branch-drift/gh-state"
  (
    cd "$repo"
    git switch -q -c release/v1.0.1
    printf 'drift\n' > unexpected.txt
    git add unexpected.txt
    git commit -q --no-gpg-sign --signoff -m 'unexpected release branch content'
    git push -q -u origin release/v1.0.1
    git switch -q main
  )

  if run_release "$repo" "$state" 1.0.1 --no-edit >/dev/null 2>&1; then
    fail 'release accepted a drifted remote branch'
  fi
}

test_tag_drift_is_rejected() {
  local repo state origin base_oid
  repo=$(setup_repo tag-drift)
  state="$TMP_ROOT/tag-drift/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo"
  run_release "$repo" "$state" patch --no-edit >/dev/null

  base_oid=$(git --git-dir="$origin" rev-parse 'refs/tags/v1.0.0^{}')
  git -C "$repo" tag -d v1.0.1 >/dev/null
  git --git-dir="$origin" tag -d v1.0.1 >/dev/null
  git -C "$repo" tag -a --no-sign v1.0.1 "$base_oid" -m 'drifted tag'
  git -C "$repo" push -q origin v1.0.1
  if run_release "$repo" "$state" 1.0.1 --no-edit >/dev/null 2>&1; then
    fail 'release accepted a tag pointing at the wrong commit'
  fi
}

test_pending_external_stage_release_pin_is_rejected() {
  local repo state
  repo=$(setup_repo pending-external-stage-pin)
  state="$TMP_ROOT/pending-external-stage-pin/gh-state"
  set_external_stage_manifest_field "$repo" paimos_release PENDING_RELEASE_TAG
  git -C "$repo" add backend/contracts/fixtures/external-stage/manifest-v1.json
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'leave external-stage release pending'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
  prepend_release_notes "$repo"

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted PENDING_RELEASE_TAG in the external-stage manifest'
  fi
}

test_unavailable_external_stage_release_pin_is_rejected() {
  local repo state
  repo=$(setup_repo unavailable-external-stage-pin)
  state="$TMP_ROOT/unavailable-external-stage-pin/gh-state"
  set_external_stage_manifest_field "$repo" paimos_release v9.9.9
  git -C "$repo" add backend/contracts/fixtures/external-stage/manifest-v1.json
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'pin an unavailable external-stage release'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
  prepend_release_notes "$repo"

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted an unavailable external-stage tag unrelated to the prepared release'
  fi
}

test_invalid_external_stage_commit_pin_is_rejected() {
  local repo state
  repo=$(setup_repo invalid-external-stage-commit-pin)
  state="$TMP_ROOT/invalid-external-stage-commit-pin/gh-state"
  set_external_stage_manifest_field "$repo" paimos_commit AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
  git -C "$repo" add backend/contracts/fixtures/external-stage/manifest-v1.json
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'pin an invalid external-stage commit'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
  prepend_release_notes "$repo"

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted a noncanonical external-stage commit pin'
  fi
}

test_unavailable_external_stage_commit_pin_is_rejected() {
  local repo state
  repo=$(setup_repo unavailable-external-stage-commit-pin)
  state="$TMP_ROOT/unavailable-external-stage-commit-pin/gh-state"
  set_external_stage_manifest_field "$repo" paimos_commit ffffffffffffffffffffffffffffffffffffffff
  git -C "$repo" add backend/contracts/fixtures/external-stage/manifest-v1.json
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'pin an unavailable external-stage commit'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
  prepend_release_notes "$repo"

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted an unavailable external-stage commit pin'
  fi
}

test_nonancestor_external_stage_commit_pin_is_rejected() {
  local repo state tree nonancestor
  repo=$(setup_repo nonancestor-external-stage-commit-pin)
  state="$TMP_ROOT/nonancestor-external-stage-commit-pin/gh-state"
  tree=$(git -C "$repo" rev-parse 'HEAD^{tree}')
  nonancestor=$(printf 'unrelated external-stage pin\n' | git -C "$repo" commit-tree "$tree")
  set_external_stage_manifest_field "$repo" paimos_commit "$nonancestor"
  git -C "$repo" add backend/contracts/fixtures/external-stage/manifest-v1.json
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'pin an unrelated external-stage commit'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
  prepend_release_notes "$repo"

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted a non-ancestor external-stage commit pin'
  fi
}

test_external_stage_pinned_byte_drift_is_rejected() {
  local repo state
  repo=$(setup_repo external-stage-byte-drift)
  state="$TMP_ROOT/external-stage-byte-drift/gh-state"
  printf '%s\n' '{"fixture":"owner-pharos-v1","drifted":true}' > \
    "$repo/backend/contracts/fixtures/external-stage/owner-pharos-v1.json"
  git -C "$repo" add backend/contracts/fixtures/external-stage/owner-pharos-v1.json
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'drift external-stage v1 fixture bytes'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
  prepend_release_notes "$repo"

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted drifted external-stage v1 bytes'
  fi
}

test_external_stage_v2_bytes_without_manifest_are_rejected() {
  local repo state output
  repo=$(setup_repo external-stage-v2-missing-manifest)
  state="$TMP_ROOT/external-stage-v2-missing-manifest/gh-state"
  output="$TMP_ROOT/external-stage-v2-missing-manifest/output"
  printf '%s\n' 'package externalstage' > "$repo/backend/externalstage/contract_v2.go"
  git -C "$repo" add backend/externalstage/contract_v2.go
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'add unpinned external-stage v2 bytes'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
  prepend_release_notes "$repo"

  if run_release "$repo" "$state" patch --no-edit >"$output" 2>&1; then
    fail 'release accepted external-stage v2 bytes without their manifest'
  fi
  grep -qF 'carries external-stage v2 bytes without the required release manifest' "$output" ||
    fail 'missing external-stage v2 manifest rejection was not explicit'
}

test_external_stage_v2_release_pin_accepts_exact_cut_and_rejects_service_drift() {
  local repo state drift_repo drift_state output
  repo=$(setup_repo external-stage-v2-positive v26.09.05)
  state="$TMP_ROOT/external-stage-v2-positive/gh-state"
  add_external_stage_v2_contract "$repo" v26.09.05
  prepend_release_notes "$repo" 26.09.05
  FAKE_RELEASE_VERSION=26.09.05 run_release "$repo" "$state" 26.09.05 --no-edit >/dev/null

  drift_repo=$(setup_repo external-stage-v2-service-drift v26.09.05)
  drift_state="$TMP_ROOT/external-stage-v2-service-drift/gh-state"
  output="$TMP_ROOT/external-stage-v2-service-drift/output"
  add_external_stage_v2_contract "$drift_repo" v26.09.05
  printf '%s\n' 'package externalstage // drifted' > "$drift_repo/backend/externalstage/service_v2.go"
  git -C "$drift_repo" add backend/externalstage/service_v2.go
  git -C "$drift_repo" commit -q --no-gpg-sign --signoff -m 'drift external-stage v2 validation'
  FAKE_GH_SERVER_MERGE=1 git -C "$drift_repo" push -q origin main
  prepend_release_notes "$drift_repo" 26.09.05
  if FAKE_RELEASE_VERSION=26.09.05 run_release "$drift_repo" "$drift_state" 26.09.05 --no-edit >"$output" 2>&1; then
    fail 'release accepted drifted external-stage v2 service validation'
  fi
  grep -qF 'external-stage v2 file differs from pinned commit: backend/externalstage/service_v2.go' "$output" ||
    fail 'external-stage v2 service drift rejection was not explicit'
}

test_existing_tag_external_stage_manifest_drift_is_rejected() {
  local repo state
  repo=$(setup_repo external-stage-manifest-drift)
  state="$TMP_ROOT/external-stage-manifest-drift/gh-state"
  set_external_stage_manifest_field "$repo" paimos_release v1.0.0
  git -C "$repo" add backend/contracts/fixtures/external-stage/manifest-v1.json
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'rewrite external-stage v1 manifest metadata'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main
  prepend_release_notes "$repo"

  if run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'release accepted external-stage manifest drift from an existing pinned tag'
  fi
}

test_canonical_unreleased_is_consumed() {
  local repo state origin base_history released history first_heading
  repo=$(setup_repo canonical-unreleased)
  state="$TMP_ROOT/canonical-unreleased/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  commit_unreleased_notes "$repo"
  base_history="$TMP_ROOT/canonical-unreleased/base-history"
  released="$TMP_ROOT/canonical-unreleased/released-changelog"
  history="$TMP_ROOT/canonical-unreleased/released-history"
  sed -n '/^## \[1.0.0\]/,$p' "$repo/docs/CHANGELOG.md" > "$base_history"

  run_release "$repo" "$state" patch --no-edit >/dev/null
  git --git-dir="$origin" show refs/pull/1/head:docs/CHANGELOG.md > "$released"

  first_heading=$(awk '/^## \[/{print; exit}' "$released")
  [[ "$first_heading" == "## [1.0.1] — "* ]] ||
    fail 'release did not consume the active leading Unreleased heading'
  [[ $(grep -c '^## \[Unreleased\]$' "$released" || true) -eq 1 ]] ||
    fail 'release did not preserve the historical Unreleased heading exactly once'
  [[ $(grep -c '^## \[1.0.1\] — ' "$released" || true) -eq 1 ]] ||
    fail 'release did not create exactly one versioned heading'
  grep -qF -- '- Pending feature.' "$released" ||
    fail 'release dropped the canonical Unreleased content'
  sed -n '/^## \[1.0.0\]/,$p' "$released" > "$history"
  cmp -s "$base_history" "$history" ||
    fail 'release changed older released history while consuming Unreleased'
}

test_duplicate_unreleased_is_rejected() {
  local repo state output
  repo=$(setup_repo duplicate-unreleased)
  state="$TMP_ROOT/duplicate-unreleased/gh-state"
  commit_unreleased_notes "$repo"
  perl -0pi -e 's/## \[1\.0\.0\]/## [Unreleased]\n\n- Duplicate active note.\n\n## [1.0.0]/' "$repo/docs/CHANGELOG.md"
  git -C "$repo" add docs/CHANGELOG.md
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'duplicate unreleased heading'
  FAKE_GH_SERVER_MERGE=1 git -C "$repo" push -q origin main

  output="$TMP_ROOT/duplicate-unreleased/output"
  if run_release "$repo" "$state" patch --no-edit >"$output" 2>&1; then
    fail 'release accepted duplicate Unreleased headings'
  fi
  grep -qF 'duplicate or non-canonical leading [Unreleased] section' "$output" ||
    fail 'duplicate Unreleased rejection reported the wrong failure'
  ! grep -q '^pr create' "$state/calls.log" 2>/dev/null ||
    fail 'duplicate Unreleased headings reached PR creation'
}

test_versioned_entry_cannot_leave_stale_unreleased() {
  local repo state output tmp
  repo=$(setup_repo stale-unreleased)
  state="$TMP_ROOT/stale-unreleased/gh-state"
  commit_unreleased_notes "$repo"
  tmp="$repo/docs/CHANGELOG.md.next"
  {
    printf '# Changelog\n\n## [1.0.1] — 2026-01-02\n\n### Fixed\n\n- Protected releases.\n\n'
    sed -n '/^## \[Unreleased\]/,$p' "$repo/docs/CHANGELOG.md"
  } > "$tmp"
  mv "$tmp" "$repo/docs/CHANGELOG.md"

  output="$TMP_ROOT/stale-unreleased/output"
  if run_release "$repo" "$state" patch --no-edit >"$output" 2>&1; then
    fail 'release accepted a versioned entry followed by stale Unreleased notes'
  fi
  grep -qF 'reviewed [1.0.1] entry must consume, not retain, the leading [Unreleased] section' "$output" ||
    fail 'stale Unreleased rejection reported the wrong failure'
  ! grep -q '^pr create' "$state/calls.log" 2>/dev/null ||
    fail 'stale Unreleased notes reached PR creation'
}

test_unreleased_consumption_rejects_prior_history_tamper() {
  local repo state output
  repo=$(setup_repo unreleased-history-tamper)
  state="$TMP_ROOT/unreleased-history-tamper/gh-state"
  commit_unreleased_notes "$repo"

  if RELEASE_TEST_FAILPOINT=before-branch-push run_release "$repo" "$state" patch --no-edit >/dev/null 2>&1; then
    fail 'prior-history tamper fixture unexpectedly completed'
  fi
  perl -0pi -e 's/- Initial\./- Tampered prior release./' "$repo/docs/CHANGELOG.md"
  git -C "$repo" add docs/CHANGELOG.md
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'tamper with prior release history'
  git -C "$repo" push -q -u origin release/v1.0.1
  git -C "$repo" switch -q main

  output="$TMP_ROOT/unreleased-history-tamper/output"
  if run_release "$repo" "$state" patch --no-edit >"$output" 2>&1; then
    fail 'release accepted prior-history tampering after Unreleased consumption'
  fi
  grep -qF 'did not consume [Unreleased] exactly or changed prior CHANGELOG history' "$output" ||
    fail 'prior-history tamper rejection reported the wrong failure'
}

test_canonical_unreleased_interactive_path_is_deterministic() {
  local repo state editor
  repo=$(setup_repo canonical-unreleased-interactive)
  state="$TMP_ROOT/canonical-unreleased-interactive/gh-state"
  editor="$TMP_ROOT/canonical-unreleased-interactive/editor-must-not-run"
  commit_unreleased_notes "$repo"
  printf '#!/usr/bin/env bash\nexit 99\n' > "$editor"
  chmod +x "$editor"

  EDITOR="$editor" run_release "$repo" "$state" patch >/dev/null
}

setup_interrupted_calendar_descendant_recovery() {
  local name="$1" calendar_version="$2" output
  RECOVERY_REPO=$(setup_repo "$name" v1.0.0)
  RECOVERY_STATE="$TMP_ROOT/$name/gh-state"
  RECOVERY_ORIGIN=$(git -C "$RECOVERY_REPO" remote get-url origin)
  output="$TMP_ROOT/$name/initial-output"
  prepend_release_notes "$RECOVERY_REPO" "$calendar_version"
  mkdir -p "$RECOVERY_STATE"
  touch "$RECOVERY_STATE/backend-full-failed"

  if FAKE_RELEASE_VERSION="$calendar_version" \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar fixture unexpectedly tagged after failed exhaustive assurance'
  fi
  RECOVERY_MERGE=$(<"$RECOVERY_STATE/merge-oid")
  ! git --git-dir="$RECOVERY_ORIGIN" show-ref --verify --quiet "refs/tags/v$calendar_version" ||
    fail 'calendar fixture published a tag before descendant recovery'

  git -C "$RECOVERY_REPO" switch -q main
  git -C "$RECOVERY_REPO" fetch -q origin main
  git -C "$RECOVERY_REPO" merge -q --ff-only origin/main
  mkdir -p "$RECOVERY_REPO/.github/workflows"
  printf '%s\n' 'jobs:' '  backend-full-race:' '    timeout-minutes: 90' > \
    "$RECOVERY_REPO/.github/workflows/backend-full.yml"
  printf '%s\n' '#!/usr/bin/env bash' \
    "BACKEND_RACE_PACKAGE_TIMEOUT=\${BACKEND_RACE_PACKAGE_TIMEOUT:-8m}" > \
    "$RECOVERY_REPO/scripts/backend-pr-race.sh"
  printf '%s\n' '#!/usr/bin/env bash' \
    "grep -q 'timeout-minutes: 90' .github/workflows/backend-full.yml" > \
    "$RECOVERY_REPO/scripts/test-backend-pr-gate.sh"
  printf '\n# Exact-head calendar recovery waiter: 100 minutes.\n' >> \
    "$RECOVERY_REPO/scripts/wait-backend-full.sh"
  chmod +x "$RECOVERY_REPO/scripts/test-backend-pr-gate.sh"
  git -C "$RECOVERY_REPO" add \
    .github/workflows/backend-full.yml \
    scripts/backend-pr-race.sh \
    scripts/test-backend-pr-gate.sh \
    scripts/wait-backend-full.sh
  git -C "$RECOVERY_REPO" commit -q --no-gpg-sign --signoff \
    -m 'fix exhaustive race timeout'
  FAKE_GH_SERVER_MERGE=1 git -C "$RECOVERY_REPO" push -q origin main
  RECOVERY_CANDIDATE=$(git -C "$RECOVERY_REPO" rev-parse HEAD)
}

test_interrupted_calendar_descendant_recovery() {
  local calendar_version next_day output later_branch legacy_oid divergent_oid legacy_tag
  calendar_version=$(TZ=Europe/Vienna date +%y.%m.%d)
  next_day=$(next_calendar_day "$(TZ=Europe/Vienna date +%Y-%m-%d)")

  setup_interrupted_calendar_descendant_recovery calendar-descendant-success "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  FAKE_RELEASE_VERSION="$calendar_version" \
    run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >/dev/null
  [[ "$(<"$RECOVERY_STATE/backend-full-head")" == "$RECOVERY_CANDIDATE" ]] ||
    fail 'calendar recovery did not require exhaustive assurance for the corrected exact main head'
  [[ $(git --git-dir="$RECOVERY_ORIGIN" rev-parse "refs/tags/v$calendar_version^{}") == "$RECOVERY_CANDIDATE" ]] ||
    fail 'calendar recovery did not tag the corrected exact protected-main head'
  git -C "$RECOVERY_REPO" merge-base --is-ancestor "$RECOVERY_MERGE" "$RECOVERY_CANDIDATE" ||
    fail 'calendar recovery fixture lost the original release merge ancestry'
  FAKE_RELEASE_VERSION="$calendar_version" \
    run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >/dev/null
  [[ $(git --git-dir="$RECOVERY_ORIGIN" rev-parse "refs/tags/v$calendar_version^{}") == "$RECOVERY_CANDIDATE" ]] ||
    fail 'calendar recovery did not safely resume its already-exact origin tag'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-legacy-version "$calendar_version"
  legacy_oid=$(git --git-dir="$RECOVERY_ORIGIN" rev-parse 'refs/tags/v1.0.0^{}')
  for legacy_tag in v1.1.1 v1.1.2 v1.2.0 v1.2.1; do
    git -C "$RECOVERY_REPO" tag -a --no-sign "$legacy_tag" "$legacy_oid" -m "$legacy_tag"
  done
  git -C "$RECOVERY_REPO" push -q origin v1.1.1 v1.1.2 v1.2.0 v1.2.1
  rm "$RECOVERY_STATE/backend-full-failed"
  FAKE_RELEASE_VERSION="$calendar_version" \
    run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >/dev/null
  [[ $(git --git-dir="$RECOVERY_ORIGIN" rev-parse "refs/tags/v$calendar_version^{}") == "$RECOVERY_CANDIDATE" ]] ||
    fail 'calendar recovery rejected authoritative legacy ancestor tags with historical VERSION drift'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-missing-timeout "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  git -C "$RECOVERY_REPO" rm -q scripts/test-backend-pr-gate.sh
  git -C "$RECOVERY_REPO" commit -q --no-gpg-sign --signoff -m 'drop timeout contract evidence'
  FAKE_GH_SERVER_MERGE=1 git -C "$RECOVERY_REPO" push -q origin main
  output="$TMP_ROOT/calendar-descendant-missing-timeout/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery accepted missing timeout evidence'
  fi
  grep -qF 'is missing required timeout correction: scripts/test-backend-pr-gate.sh' "$output" ||
    fail 'missing timeout evidence rejection was not explicit'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-unrelated "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  printf 'unrelated product change\n' > "$RECOVERY_REPO/unrelated.txt"
  git -C "$RECOVERY_REPO" add unrelated.txt
  git -C "$RECOVERY_REPO" commit -q --no-gpg-sign --signoff -m 'unrelated product change'
  FAKE_GH_SERVER_MERGE=1 git -C "$RECOVERY_REPO" push -q origin main
  output="$TMP_ROOT/calendar-descendant-unrelated/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery accepted an unrelated descendant delta'
  fi
  grep -qF 'contains an unrelated file: unrelated.txt' "$output" ||
    fail 'unrelated calendar recovery rejection was not explicit'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-version "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  printf '0.0.0\n' > "$RECOVERY_REPO/VERSION"
  git -C "$RECOVERY_REPO" add VERSION
  git -C "$RECOVERY_REPO" commit -q --no-gpg-sign --signoff -m 'drift release version'
  FAKE_GH_SERVER_MERGE=1 git -C "$RECOVERY_REPO" push -q origin main
  output="$TMP_ROOT/calendar-descendant-version/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery accepted a descendant with VERSION drift'
  fi
  grep -qF "does not carry VERSION=$calendar_version" "$output" ||
    fail 'calendar recovery VERSION rejection was not explicit'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-wrong-tag "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  git -C "$RECOVERY_REPO" tag -a --no-sign "v$calendar_version" "$RECOVERY_MERGE" -m 'wrong recovery target'
  git -C "$RECOVERY_REPO" push -q origin "v$calendar_version"
  output="$TMP_ROOT/calendar-descendant-wrong-tag/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery accepted an existing tag outside the corrected exact main head'
  fi
  grep -qF 'not the exact protected-main recovery head' "$output" ||
    fail 'wrong existing calendar recovery tag rejection was not explicit'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-local-tag "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  git -C "$RECOVERY_REPO" tag -a --no-sign "v$calendar_version" "$RECOVERY_CANDIDATE" -m 'local-only recovery target'
  output="$TMP_ROOT/calendar-descendant-local-tag/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery published from a local-only tag'
  fi
  grep -qF 'exists only locally' "$output" ||
    fail 'local-only calendar recovery tag rejection was not explicit'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-newer-tag "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  later_branch="newer-release-tag"
  git -C "$RECOVERY_REPO" switch -q -c "$later_branch"
  printf '9.0.0\n' > "$RECOVERY_REPO/VERSION"
  git -C "$RECOVERY_REPO" add VERSION
  git -C "$RECOVERY_REPO" commit -q --no-gpg-sign --signoff -m 'create later release evidence'
  git -C "$RECOVERY_REPO" tag -a --no-sign v9.0.0 -m 'later release'
  git -C "$RECOVERY_REPO" push -q origin v9.0.0
  git -C "$RECOVERY_REPO" switch -q main
  output="$TMP_ROOT/calendar-descendant-newer-tag/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery accepted a newer or divergent origin release tag'
  fi
  grep -qF 'is newer than or divergent from the interrupted release merge: v9.0.0' "$output" ||
    fail 'newer release tag rejection was not explicit'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-divergent-tag "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  divergent_oid=$(printf 'divergent release evidence\n' |
    git -C "$RECOVERY_REPO" commit-tree "$RECOVERY_CANDIDATE^{tree}")
  git -C "$RECOVERY_REPO" tag -a --no-sign v8.0.0 "$divergent_oid" -m 'divergent release'
  git -C "$RECOVERY_REPO" push -q origin v8.0.0
  output="$TMP_ROOT/calendar-descendant-divergent-tag/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery accepted a divergent origin release tag'
  fi
  grep -qF 'is newer than or divergent from the interrupted release merge: v8.0.0' "$output" ||
    fail 'divergent release tag rejection was not explicit'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-main-race "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  output="$TMP_ROOT/calendar-descendant-main-race/output"
  if FAKE_RELEASE_VERSION="$calendar_version" FAKE_ADVANCE_MAIN_AFTER_BACKEND=1 \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery tagged after protected main moved beyond the assured head'
  fi
  grep -qF 'is not the exact current protected origin/main head' "$output" ||
    fail 'protected-main recovery race rejection was not explicit'
  ! git --git-dir="$RECOVERY_ORIGIN" show-ref --verify --quiet "refs/tags/v$calendar_version" ||
    fail 'protected-main recovery race still published a tag'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-tag-audit-race "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  output="$TMP_ROOT/calendar-descendant-tag-audit-race/output"
  if FAKE_RELEASE_VERSION="$calendar_version" FAKE_ADVANCE_MAIN_DURING_TAG_AUDIT=1 \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery tagged after main moved during the historical tag audit'
  fi
  [[ "$(<"$RECOVERY_STATE/tag-audit-count")" == 2 ]] ||
    fail 'historical tag audit race fixture did not advance main during the pre-tag audit'
  grep -qF 'is not the exact current protected origin/main head' "$output" ||
    fail 'historical tag audit main-race rejection was not explicit'
  ! git --git-dir="$RECOVERY_ORIGIN" show-ref --verify --quiet "refs/tags/v$calendar_version" ||
    fail 'historical tag audit main-race still published a tag'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-post-audit-race "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  output="$TMP_ROOT/calendar-descendant-post-audit-race/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     FAKE_ADVANCE_MAIN_DURING_POST_AUDIT_TAG_QUERY=1 \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery tagged after main moved during the post-audit tag query'
  fi
  [[ -f "$RECOVERY_STATE/post-audit-tag-query-advanced" ]] ||
    fail 'post-audit tag-query fixture did not advance protected main'
  grep -qF 'is not the exact current protected origin/main head' "$output" ||
    fail 'post-audit tag-query main-race rejection was not explicit'
  ! git --git-dir="$RECOVERY_ORIGIN" show-ref --verify --quiet "refs/tags/v$calendar_version" ||
    fail 'post-audit tag-query main-race still published a tag'

  setup_interrupted_calendar_descendant_recovery calendar-descendant-midnight "$calendar_version"
  rm "$RECOVERY_STATE/backend-full-failed"
  output="$TMP_ROOT/calendar-descendant-midnight/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     FAKE_VIENNA_FLIP_AFTER_BACKEND=1 FAKE_VIENNA_NEXT_DAY="$next_day" \
     run_release "$RECOVERY_REPO" "$RECOVERY_STATE" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recovery tagged after the Vienna cut day changed'
  fi
  grep -qF 'Vienna calendar day changed before calendar descendant recovery validation' "$output" ||
    fail 'calendar recovery midnight rejection was not explicit'
  ! git --git-dir="$RECOVERY_ORIGIN" show-ref --verify --quiet "refs/tags/v$calendar_version" ||
    fail 'calendar recovery midnight rejection still published a tag'
}

test_calendar_release_and_rejections() {
  local repo state origin output calendar_version calendar_iso next_day recut_version merge_oid wrong_oid blob_oid
  calendar_version=$(TZ=Europe/Vienna date +%y.%m.%d)
  calendar_iso=$(TZ=Europe/Vienna date +%Y-%m-%d)
  next_day=$(next_calendar_day "$calendar_iso")
  recut_version="$calendar_version.14.05"
  repo=$(setup_repo calendar-release v1.0.0)
  state="$TMP_ROOT/calendar-release/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo" "$calendar_version"

  FAKE_RELEASE_VERSION="$calendar_version" \
    run_release "$repo" "$state" "$calendar_version" --no-edit >/dev/null

  [[ $(git --git-dir="$origin" show refs/pull/1/head:VERSION) == "$calendar_version" ]] ||
    fail 'calendar release lost leading zeroes in VERSION'
  [[ $(git --git-dir="$origin" rev-parse "refs/tags/v$calendar_version^{}") == "$(<"$state/merge-oid")" ]] ||
    fail 'calendar tag did not pin the protected merge'

  # An exact published tag is a resumable checkpoint only through its
  # canonical merged PR. This rerun must prove, not recreate, that tag.
  FAKE_RELEASE_VERSION="$calendar_version" \
    run_release "$repo" "$state" "$calendar_version" --no-edit >/dev/null

  merge_oid=$(<"$state/merge-oid")
  wrong_oid=$(git --git-dir="$origin" rev-parse "$merge_oid^")
  git -C "$repo" tag -d "v$calendar_version" >/dev/null
  git -C "$repo" push -q origin ":refs/tags/v$calendar_version"
  git -C "$repo" tag -a --no-sign "v$calendar_version" "$wrong_oid" -m mismatched
  git -C "$repo" push -q origin "v$calendar_version"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     run_release "$repo" "$state" "$calendar_version" --no-edit >"$TMP_ROOT/calendar-release/mismatch" 2>&1; then
    fail 'published calendar resume accepted a tag outside the canonical merge'
  fi
  grep -qF 'expected canonical protected merge' "$TMP_ROOT/calendar-release/mismatch" ||
    fail 'mismatched published calendar tag rejection was not explicit'

  repo=$(setup_repo calendar-reject v1.0.0)
  output="$TMP_ROOT/calendar-reject/output"
  if FAKE_RELEASE_VERSION="$recut_version" \
     run_release "$repo" "$TMP_ROOT/calendar-reject/gh-state" "$recut_version" --no-edit >"$output" 2>&1; then
    fail 'calendar suffix was accepted without a prior same-day cut'
  fi
  grep -qF '.hh.mm is valid only for a same-day recut' "$output" ||
    fail 'calendar recut rejection was not explicit'

  if run_release "$repo" "$TMP_ROOT/calendar-reject-600/gh-state" 6.0.0 --no-edit >"$output" 2>&1; then
    fail 'prohibited 6.0.0 release was accepted'
  fi
  grep -qF '6.0.0 is prohibited' "$output" || fail '6.0.0 rejection was not explicit'

  GIT_COMMITTER_DATE='2030-01-01T00:00:00Z' \
    git -C "$repo" tag -a --no-sign v5.21.0 -m v5.21.0
  git -C "$repo" push -q origin v5.21.0
  if run_release "$repo" "$TMP_ROOT/calendar-reject-major/gh-state" major --no-edit >"$output" 2>&1; then
    fail 'major mode synthesized prohibited 6.0.0'
  fi
  grep -qF 'computed release 6.0.0 is prohibited' "$output" ||
    fail 'major-mode calendar guidance was not explicit'

  repo=$(setup_repo calendar-local-only v1.0.0)
  GIT_COMMITTER_DATE='2031-01-01T00:00:00Z' \
    git -C "$repo" tag -a --no-sign "v$calendar_version" -m local-only
  prepend_release_notes "$repo" "$recut_version"
  output="$TMP_ROOT/calendar-local-only/output"
  if FAKE_RELEASE_VERSION="$recut_version" \
     run_release "$repo" "$TMP_ROOT/calendar-local-only/gh-state" "$recut_version" --no-edit >"$output" 2>&1; then
    fail 'local-only calendar tag was accepted as recut evidence'
  fi
  grep -qF '.hh.mm is valid only for a same-day recut' "$output" ||
    fail 'local-only recut evidence rejection was not explicit'

  repo=$(setup_repo calendar-remote-only v1.0.0)
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo" "$calendar_version"
  FAKE_RELEASE_VERSION="$calendar_version" \
    run_release "$repo" "$TMP_ROOT/calendar-remote-only/prior-state" "$calendar_version" --no-edit >/dev/null
  git -C "$repo" tag -d "v$calendar_version" >/dev/null
  ! git -C "$repo" show-ref --verify --quiet "refs/tags/v$calendar_version" ||
    fail 'remote-only recut fixture retained a local tag'
  prepend_release_notes_preserving_history "$repo" "$recut_version"
  FAKE_RELEASE_VERSION="$recut_version" \
    run_release "$repo" "$TMP_ROOT/calendar-remote-only/gh-state" "$recut_version" --no-edit >/dev/null
  git --git-dir="$origin" rev-parse "refs/tags/v$recut_version^{}" >/dev/null ||
    fail 'origin-proven recut did not publish'

  repo=$(setup_repo calendar-wrong-version v1.0.0)
  GIT_COMMITTER_DATE='2031-01-01T00:00:00Z' \
    git -C "$repo" tag -a --no-sign "v$calendar_version" -m wrong-version
  git -C "$repo" push -q origin "v$calendar_version"
  prepend_release_notes "$repo" "$recut_version"
  output="$TMP_ROOT/calendar-wrong-version/output"
  if FAKE_RELEASE_VERSION="$recut_version" \
     run_release "$repo" "$TMP_ROOT/calendar-wrong-version/gh-state" "$recut_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recut accepted a prior tag carrying the wrong VERSION'
  fi
  grep -qF "does not carry VERSION=$calendar_version" "$output" ||
    fail 'wrong-VERSION prior tag rejection was not explicit'

  repo=$(setup_repo calendar-blob-tag v1.0.0)
  blob_oid=$(printf 'not a release commit\n' | git -C "$repo" hash-object -w --stdin)
  GIT_COMMITTER_DATE='2031-01-01T00:00:00Z' \
    git -C "$repo" tag -a --no-sign "v$calendar_version" "$blob_oid" -m blob-tag
  git -C "$repo" push -q origin "v$calendar_version"
  prepend_release_notes "$repo" "$recut_version"
  output="$TMP_ROOT/calendar-blob-tag/output"
  if FAKE_RELEASE_VERSION="$recut_version" \
     run_release "$repo" "$TMP_ROOT/calendar-blob-tag/gh-state" "$recut_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recut accepted a prior tag resolving to a blob'
  fi
  grep -qF 'does not resolve to a commit' "$output" ||
    fail 'non-commit prior tag rejection was not explicit'

  repo=$(setup_repo calendar-nonancestor v1.0.0)
  git -C "$repo" switch -q --orphan calendar-evidence
  printf '%s\n' "$calendar_version" > "$repo/VERSION"
  git -C "$repo" add -A
  git -C "$repo" commit -q --no-gpg-sign --signoff -m 'nonancestor calendar evidence'
  GIT_COMMITTER_DATE='2031-01-01T00:00:00Z' \
    git -C "$repo" tag -a --no-sign "v$calendar_version" -m nonancestor
  git -C "$repo" push -q origin "v$calendar_version"
  git -C "$repo" switch -q main
  prepend_release_notes "$repo" "$recut_version"
  output="$TMP_ROOT/calendar-nonancestor/output"
  if FAKE_RELEASE_VERSION="$recut_version" \
     run_release "$repo" "$TMP_ROOT/calendar-nonancestor/gh-state" "$recut_version" --no-edit >"$output" 2>&1; then
    fail 'calendar recut accepted a prior tag outside origin/main ancestry'
  fi
  grep -qF 'is not an ancestor of origin/main' "$output" ||
    fail 'nonancestor prior tag rejection was not explicit'

  repo=$(setup_repo calendar-no-canonical-resume v1.0.0)
  GIT_COMMITTER_DATE='2031-01-01T00:00:00Z' \
    git -C "$repo" tag -a --no-sign "v$calendar_version" -m unproven
  git -C "$repo" push -q origin "v$calendar_version"
  git -C "$repo" tag -d "v$calendar_version" >/dev/null
  output="$TMP_ROOT/calendar-no-canonical-resume/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     run_release "$repo" "$TMP_ROOT/calendar-no-canonical-resume/gh-state" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'second unsuffixed cut resumed without a canonical protected PR'
  fi
  grep -qF 'exists without its canonical' "$output" ||
    fail 'unproven unsuffixed resume rejection was not explicit'

  repo=$(setup_repo calendar-branch-midnight v1.0.0)
  prepend_release_notes "$repo" "$calendar_version"
  output="$TMP_ROOT/calendar-branch-midnight/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     FAKE_VIENNA_FLIP_ON_REPO_VIEW=1 FAKE_VIENNA_NEXT_DAY="$next_day" \
     run_release "$repo" "$TMP_ROOT/calendar-branch-midnight/gh-state" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar release materialized a branch after the Vienna day changed'
  fi
  grep -qF 'Vienna calendar day changed before release-branch materialization' "$output" ||
    fail 'branch-midnight rejection was not explicit'

  repo=$(setup_repo calendar-tag-midnight v1.0.0)
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo" "$calendar_version"
  output="$TMP_ROOT/calendar-tag-midnight/output"
  if FAKE_RELEASE_VERSION="$calendar_version" \
     FAKE_VIENNA_FLIP_AFTER_BACKEND=1 FAKE_VIENNA_NEXT_DAY="$next_day" \
     run_release "$repo" "$TMP_ROOT/calendar-tag-midnight/gh-state" "$calendar_version" --no-edit >"$output" 2>&1; then
    fail 'calendar release created an absent tag after the Vienna day changed'
  fi
  grep -qF "Vienna calendar day changed before creating absent tag v$calendar_version" "$output" ||
    fail 'tag-midnight rejection was not explicit'
  ! git --git-dir="$origin" show-ref --verify --quiet "refs/tags/v$calendar_version" ||
    fail 'tag-midnight rejection still published a tag'
}

write_fake_commands "$TMP_ROOT/fake-bin"
test_calendar_missing_provenance_receipt_recovery \
  26.09.01 "$IMMEDIATE_AUTO_MERGE_RECOVERY_REASON" calendar-immediate-recovery
test_calendar_missing_provenance_receipt_recovery \
  26.09.02 "$MANUAL_RECOVERY_REASON" calendar-manual-recovery
test_interrupted_calendar_descendant_recovery
test_calendar_release_and_rejections
test_committed_recovery_receipts_are_exact
test_canonical_unreleased_is_consumed
test_canonical_unreleased_interactive_path_is_deterministic
test_duplicate_unreleased_is_rejected
test_versioned_entry_cannot_leave_stale_unreleased
test_unreleased_consumption_rejects_prior_history_tamper
test_protected_release_and_resume_states
test_exhaustive_backend_failure_blocks_tag_creation
test_unnamed_required_check_is_not_reused_as_green
test_missing_auto_merge_recovery_receipt
test_existing_manual_recovery_reason_remains_accepted
test_open_pr_is_reused_after_timeout
test_mid_wait_head_mutation_is_rejected
test_local_pre_push_interruption_resumes
test_same_file_drift_is_rejected
test_trailing_newline_drift_is_rejected
test_missing_changelog_history_newline_is_rejected
test_reused_pr_gates_the_pinned_head
test_unsigned_merged_resume_is_rejected
test_remote_branch_drift_is_rejected
test_tag_drift_is_rejected
test_pending_external_stage_release_pin_is_rejected
test_unavailable_external_stage_release_pin_is_rejected
test_invalid_external_stage_commit_pin_is_rejected
test_unavailable_external_stage_commit_pin_is_rejected
test_nonancestor_external_stage_commit_pin_is_rejected
test_external_stage_pinned_byte_drift_is_rejected
test_external_stage_v2_bytes_without_manifest_are_rejected
test_external_stage_v2_release_pin_accepts_exact_cut_and_rejects_service_drift
test_existing_tag_external_stage_manifest_drift_is_rejected

echo 'test-release: ok'
