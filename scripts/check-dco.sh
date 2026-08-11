#!/usr/bin/env bash
# Verify that every commit in a revision range carries the author's DCO sign-off.

set -euo pipefail

usage() {
  echo "usage: $0 <base-revision> [head-revision]" >&2
}

base_revision="${1:-}"
head_revision="${2:-HEAD}"

if [[ -z "$base_revision" ]]; then
  usage
  exit 2
fi

for revision in "$base_revision" "$head_revision"; do
  if ! git cat-file -e "${revision}^{commit}" 2>/dev/null; then
    echo "DCO check: revision is not a commit: $revision" >&2
    exit 2
  fi
done

commit_count=0
failure_count=0
shopt -s nocasematch

while IFS= read -r commit; do
  [[ -n "$commit" ]] || continue
  commit_count=$((commit_count + 1))

  author_name=$(git show -s --format='%an' "$commit")
  author_email=$(git show -s --format='%ae' "$commit")
  expected_value="$author_name <$author_email>"
  matched=0

  while IFS= read -r trailer; do
    trailer_key="${trailer%%:*}"
    trailer_value="${trailer#*:}"
    trailer_value="${trailer_value# }"
    if [[ "$trailer_key" == "Signed-off-by" && "$trailer_value" == "$expected_value" ]]; then
      matched=1
      break
    fi
  done < <(git show -s --format='%B' "$commit" | git interpret-trailers --parse)

  if [[ $matched -ne 1 ]]; then
    echo "DCO check: ${commit:0:12} is missing the author's sign-off:" >&2
    echo "  Signed-off-by: $expected_value" >&2
    failure_count=$((failure_count + 1))
  fi
done < <(git rev-list --reverse "${base_revision}..${head_revision}")

if [[ $commit_count -eq 0 ]]; then
  echo "DCO check: revision range contains no commits" >&2
  exit 2
fi

if [[ $failure_count -ne 0 ]]; then
  echo "DCO check: failed ($failure_count of $commit_count commits)" >&2
  exit 1
fi

echo "DCO check: passed ($commit_count commits)"
