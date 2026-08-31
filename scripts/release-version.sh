#!/usr/bin/env bash
# Side-effect-free release-version grammar and calendar-cut policy helpers.
# Source this file; it intentionally does not change shell options or cwd.

PAIMOS_RELEASE_SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+$'
PAIMOS_RELEASE_CALENDAR_RE='^[0-9]{2}\.[0-9]{2}\.[0-9]{2}(\.[0-9]{2}\.[0-9]{2})?$'
PAIMOS_RELEASE_VERSION_ERE='([0-9]{2}\.[0-9]{2}\.[0-9]{2}(\.[0-9]{2}\.[0-9]{2})?|[0-9]+\.[0-9]+\.[0-9]+)'
PAIMOS_RELEASE_TAG_ERE="^v${PAIMOS_RELEASE_VERSION_ERE}$"

release_version::strip_v() {
  printf '%s\n' "${1#v}"
}

release_version::is_calendar() {
  local version="${1#v}" year month day hour minute max_day
  [[ "$version" =~ $PAIMOS_RELEASE_CALENDAR_RE ]] || return 1
  IFS=. read -r year month day hour minute <<<"$version"
  if [[ -n "$hour" ]]; then
    (( 10#$hour <= 23 && 10#$minute <= 59 )) || return 1
  fi
  (( 10#$month >= 1 && 10#$month <= 12 && 10#$day >= 1 )) || return 1
  case "$month" in
    01|03|05|07|08|10|12) max_day=31 ;;
    04|06|09|11) max_day=30 ;;
    02)
      max_day=28
      # yy maps to 20yy; the full Gregorian rule keeps the helper portable
      # across GNU/Linux and macOS release runners without date parsing.
      if (( (2000 + 10#$year) % 400 == 0 || ((2000 + 10#$year) % 4 == 0 && (2000 + 10#$year) % 100 != 0) )); then
        max_day=29
      fi
      ;;
  esac
  (( 10#$day <= max_day ))
}

release_version::is_semver() {
  local version="${1#v}"
  [[ "$version" =~ $PAIMOS_RELEASE_SEMVER_RE && "$version" != "6.0.0" ]] || return 1
  # Two-digit-major values belong exclusively to the calendar grammar, so a
  # malformed or impossible calendar date cannot fall through as legacy SemVer.
  [[ ! "$version" =~ ^[0-9]{2}\. ]]
}

release_version::is_supported() {
  release_version::is_calendar "$1" || release_version::is_semver "$1"
}

release_version::kind() {
  if release_version::is_calendar "$1"; then
    printf 'calendar\n'
  elif release_version::is_semver "$1"; then
    printf 'semver\n'
  else
    printf 'invalid\n'
    return 1
  fi
}

release_version::vienna_date() {
  TZ=Europe/Vienna date +%y.%m.%d
}

release_version::vienna_iso_date() {
  TZ=Europe/Vienna date +%Y-%m-%d
}

release_version::calendar_day() {
  local version="${1#v}"
  printf '%s\n' "${version:0:8}"
}

release_version::has_recut_suffix() {
  local version="${1#v}"
  [[ "$version" =~ ^[0-9]{2}\.[0-9]{2}\.[0-9]{2}\.[0-9]{2}\.[0-9]{2}$ ]]
}

release_version::is_calendar_cut_today() {
  release_version::is_calendar "$1" &&
    [[ "$(release_version::calendar_day "$1")" == "$(release_version::vienna_date)" ]]
}

# A suffix is reserved for a second cut on the same Vienna day. The caller
# supplies existing tags (one per line), keeping this helper deterministic and
# free of repository/network access.
release_version::calendar_recut_policy() {
  local version="${1#v}" existing_tags="${2:-}" day found=0 tag
  release_version::is_calendar_cut_today "$version" || return 1
  day="$(release_version::calendar_day "$version")"
  while IFS= read -r tag; do
    tag="${tag#v}"
    [[ "$tag" == "$day" || "$tag" == "$day".* ]] && found=1
  done <<<"$existing_tags"
  if release_version::has_recut_suffix "$version"; then
    (( found == 1 ))
  else
    (( found == 0 ))
  fi
}

release_version::tag_filter() {
  local tag
  while IFS= read -r tag; do
    [[ "$tag" == v* ]] && release_version::is_supported "$tag" && printf '%s\n' "$tag"
  done
  return 0
}
