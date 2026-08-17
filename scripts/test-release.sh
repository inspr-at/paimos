#!/usr/bin/env bash
set -euo pipefail

export GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL=/dev/null

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/paimos-release-test.XXXXXX")
trap 'rm -rf "$TMP_ROOT"' EXIT

fail() {
  echo "test-release: $*" >&2
  exit 1
}

write_stub_scripts() {
  local repo="$1"
  mkdir -p "$repo/scripts"
  cp "$ROOT/scripts/release.sh" "$repo/scripts/release.sh"
  cp "$ROOT/scripts/_deploy-lib.sh" "$repo/scripts/_deploy-lib.sh"
  cp "$ROOT/scripts/wait-release-ci.sh" "$repo/scripts/wait-release-ci.sh"

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

  cat > "$bin/docker" <<'DOCKER'
#!/usr/bin/env bash
[[ "$*" == "manifest inspect ghcr.io/inspr-at/paimos:1.0.1" ]]
DOCKER

  cat > "$bin/gh" <<'GH'
#!/usr/bin/env bash
set -euo pipefail

state=${FAKE_GH_STATE:?}
origin=${FAKE_GH_ORIGIN:?}
log="$state/calls.log"
mkdir -p "$state"
printf '%s\n' "$*" >> "$log"

merge_pr() {
  [[ -f "$state/pr-open" && -f "$state/auto-merge" ]] || return 0
  [[ ! -f "$state/wrong-method" ]] || return 0
  [[ "${FAKE_GH_DEFER_MERGE:-0}" != "1" ]] || return 0
  [[ ! -f "$state/pr-merged" ]] || return 0
  git --git-dir="$origin" merge-base --is-ancestor \
    refs/heads/main refs/heads/release/v1.0.1 || return 0

  work="$state/merge-work"
  git clone -q "$origin" "$work" >/dev/null 2>&1
  git -C "$work" config user.name 'Release Merge Bot'
  git -C "$work" config user.email 'release-merge@example.test'
  git -C "$work" switch -q main >/dev/null 2>&1
  git -C "$work" merge -q --squash origin/release/v1.0.1 >/dev/null 2>&1
  git -C "$work" commit -q --signoff -m 'release: v1.0.1 (#1)' >/dev/null 2>&1
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
  if git --git-dir="$origin" show-ref --verify --quiet refs/heads/release/v1.0.1; then
    git --git-dir="$origin" update-ref refs/pull/1/head refs/heads/release/v1.0.1
  fi
}

mutate_pr_head_if_requested() {
  [[ -f "$state/mutate-head" && -f "$state/auto-merge" && ! -f "$state/head-mutated" ]] || return 0
  work="$state/mutate-work"
  git clone -q "$origin" "$work" >/dev/null 2>&1
  git -C "$work" config user.name 'Unexpected Release Author'
  git -C "$work" config user.email 'unexpected@example.test'
  git -C "$work" switch -q release/v1.0.1
  printf 'unexpected same-file change\n' >> "$work/README.md"
  git -C "$work" add README.md
  git -C "$work" commit -q --no-gpg-sign --signoff -m 'mutate release head during wait'
  git -C "$work" push -q origin release/v1.0.1
  sync_pr_ref
  touch "$state/head-mutated"
}

pr_head_oid() {
  git --git-dir="$origin" rev-parse refs/pull/1/head
}

case "${1:-} ${2:-}" in
  'repo view')
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
        merge_oid=$(<"$state/merge-oid")
        method=SQUASH
        [[ -f "$state/wrong-method" ]] && method=REBASE
        printf '[{"number":1,"state":"MERGED","url":"https://example.test/pr/1","headRefName":"release/v1.0.1","headRefOid":"%s","baseRefName":"%s","autoMergeRequest":{"mergeMethod":"%s"},"mergeCommit":{"oid":"%s"}}]\n' "$head_oid" "$base_ref" "$method" "$merge_oid"
      else
        method=SQUASH
        [[ -f "$state/wrong-method" ]] && method=REBASE
        auto=null
        [[ -f "$state/auto-merge" ]] && auto="{\"mergeMethod\":\"$method\"}"
        printf '[{"number":1,"state":"OPEN","url":"https://example.test/pr/1","headRefName":"release/v1.0.1","headRefOid":"%s","baseRefName":"%s","autoMergeRequest":%s,"mergeCommit":null}]\n' "$head_oid" "$base_ref" "$auto"
      fi
    else
      printf '%s\n' '[]'
    fi
    ;;
  'pr create')
    [[ "$*" == *'--base main'* && "$*" == *'--head release/v1.0.1'* ]]
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
      merge_oid=$(<"$state/merge-oid")
      method=SQUASH
      [[ -f "$state/wrong-method" ]] && method=REBASE
      printf '{"number":1,"state":"MERGED","url":"https://example.test/pr/1","headRefName":"release/v1.0.1","headRefOid":"%s","baseRefName":"%s","mergeStateStatus":"CLEAN","autoMergeRequest":{"enabledAt":"now","mergeMethod":"%s"},"mergeCommit":{"oid":"%s"}}\n' "$head_oid" "$base_ref" "$method" "$merge_oid"
    else
      auto=null
      merge_status=CLEAN
      method=SQUASH
      [[ -f "$state/wrong-method" ]] && method=REBASE
      [[ -f "$state/auto-merge" ]] && auto="{\"enabledAt\":\"now\",\"mergeMethod\":\"$method\"}"
      git --git-dir="$origin" merge-base --is-ancestor \
        refs/heads/main refs/heads/release/v1.0.1 || merge_status=BEHIND
      printf '{"number":1,"state":"OPEN","url":"https://example.test/pr/1","headRefName":"release/v1.0.1","headRefOid":"%s","baseRefName":"%s","mergeStateStatus":"%s","autoMergeRequest":%s,"mergeCommit":null}\n' "$head_oid" "$base_ref" "$merge_status" "$auto"
    fi
    ;;
  'run list')
    [[ "$*" == *'--branch v1.0.1'* ]]
    if printf '%s\n' "$*" | grep -q 'workflowName == \\"release\\"'; then
      printf '2\tcompleted\tsuccess\thttps://example.test/run/release\n'
    else
      printf '1\tcompleted\tsuccess\thttps://example.test/run/ci\n'
    fi
    ;;
  *)
    echo "unexpected fake gh call: $*" >&2
    exit 64
    ;;
esac
GH

  chmod +x "$bin/docker" "$bin/gh"
}

setup_repo() {
  local name="$1"
  local base="$TMP_ROOT/$name"
  local origin="$base/origin.git"
  local repo="$base/repo"

  mkdir -p "$base"
  git init -q --bare "$origin"
  git init -q -b main "$repo"
  git -C "$repo" config user.name 'Release Author'
  git -C "$repo" config user.email 'release-author@example.test'
  mkdir -p "$repo/docs"
  printf '1.0.0\n' > "$repo/VERSION"
  printf '<code>v1.0.0</code>\n' > "$repo/README.md"
  printf '# Changelog\n\n## [1.0.0] — 2026-01-01\n\n- Initial.\n' > "$repo/docs/CHANGELOG.md"
  printf 'VER=1.0.0\nVER=1.0.0\npaimos --version    # 1.0.0\n' > "$repo/docs/INSTALL.md"
  write_stub_scripts "$repo"
  git -C "$repo" add .
  git -C "$repo" commit -q --signoff -m 'initial release'
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

prepend_release_notes() {
  local repo="$1"
  local tmp="$repo/docs/CHANGELOG.md.next"
  {
    printf '# Changelog\n\n## [1.0.1] — 2026-01-02\n\n### Fixed\n\n- Protected releases.\n\n'
    sed -n '/^## \[1.0.0\]/,$p' "$repo/docs/CHANGELOG.md"
  } > "$tmp"
  mv "$tmp" "$repo/docs/CHANGELOG.md"
}

run_release() {
  local repo="$1" state="$2"
  shift 2
  (
    cd "$repo"
    PATH="$TMP_ROOT/fake-bin:$PATH" \
      FAKE_GH_STATE="$state" \
      FAKE_GH_ORIGIN="$(git remote get-url origin)" \
      FAKE_GATE_LOG="${FAKE_GATE_LOG:-}" \
      FAKE_CLAIMS_FAIL_MARKER="${FAKE_CLAIMS_FAIL_MARKER:-}" \
      FAKE_CLAIMS_FAIL_ON_BRANCH="${FAKE_CLAIMS_FAIL_ON_BRANCH:-}" \
      FAKE_CLAIMS_FAIL_ON_CONTENT="${FAKE_CLAIMS_FAIL_ON_CONTENT:-0}" \
      RELEASE_MERGE_TIMEOUT="${RELEASE_MERGE_TIMEOUT:-10}" \
      RELEASE_MERGE_POLL=1 \
      ./scripts/release.sh "$@"
  )
}

assert_one_pr() {
  local state="$1"
  [[ $(grep -c '^pr create' "$state/calls.log") -eq 1 ]] || fail 'release PR was duplicated'
}

test_protected_release_and_resume_states() {
  local repo state origin merge_oid tag_oid
  repo=$(setup_repo success)
  state="$TMP_ROOT/success/gh-state"
  origin=$(git -C "$repo" remote get-url origin)
  prepend_release_notes "$repo"

  FAKE_GH_ADVANCE_AFTER_MERGE=1 run_release "$repo" "$state" patch --no-edit >/dev/null
  merge_oid=$(<"$state/merge-oid")
  tag_oid=$(git --git-dir="$origin" rev-parse 'refs/tags/v1.0.1^{}')
  [[ "$tag_oid" == "$merge_oid" ]] || fail 'tag does not point at protected-main merge commit'
  [[ "$tag_oid" != "$(git --git-dir="$origin" rev-parse refs/heads/main)" ]] ||
    fail 'tag followed a later main commit instead of the release PR merge'
  [[ $(git --git-dir="$origin" show refs/pull/1/head:VERSION) == '1.0.1' ]] || fail 'release PR head version mismatch'
  git --git-dir="$origin" show -s --format='%B' refs/pull/1/head | grep -q 'Signed-off-by: Release Author <release-author@example.test>' || fail 'release commit lacks DCO sign-off'
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

write_fake_commands "$TMP_ROOT/fake-bin"
test_protected_release_and_resume_states
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

echo 'test-release: ok'
