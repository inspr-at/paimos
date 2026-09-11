#!/usr/bin/env bash
# Side-effect-free release-version grammar and calendar-cut policy helpers.
# Source this file; it intentionally does not change shell options or cwd.

# Three eras, oldest first (PAI-979 / INSPR-395):
#   legacy SemVer            x.y.z                      closed since 26.08.31
#   INSPR calendar v1        yy.mm.dd[.hh.mm]           closed after the first v2 cut
#   INSPR calendar v2        YYMMDDhhmmss.0.0           current default (UTC, SemVer-syntactic)
# A v2 coordinate is the UTC reservation second as the SemVer MAJOR segment with
# MINOR and PATCH fixed at 0.0. It is fixed-width, so string order equals time
# order, and it is syntactically valid SemVer without carrying SemVer meaning.
PAIMOS_RELEASE_SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+$'
PAIMOS_RELEASE_CALENDAR_RE='^[0-9]{2}\.[0-9]{2}\.[0-9]{2}(\.[0-9]{2}\.[0-9]{2})?$'
PAIMOS_RELEASE_CALENDAR_V2_RE='^[1-9][0-9]{11}\.0\.0$'
PAIMOS_RELEASE_VERSION_ERE='([1-9][0-9]{11}\.0\.0|[0-9]{2}\.[0-9]{2}\.[0-9]{2}(\.[0-9]{2}\.[0-9]{2})?|[0-9]+\.[0-9]+\.[0-9]+)'
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

# Shared Gregorian day check for yy/mm/dd fields (10#-safe, no date parsing).
release_version::valid_ymd() {
  local year="$1" month="$2" day="$3" max_day
  (( 10#$month >= 1 && 10#$month <= 12 && 10#$day >= 1 )) || return 1
  case "$month" in
    01|03|05|07|08|10|12) max_day=31 ;;
    04|06|09|11) max_day=30 ;;
    02)
      max_day=28
      if (( (2000 + 10#$year) % 400 == 0 || ((2000 + 10#$year) % 4 == 0 && (2000 + 10#$year) % 100 != 0) )); then
        max_day=29
      fi
      ;;
    *) return 1 ;;
  esac
  (( 10#$day <= max_day ))
}

release_version::is_calendar_v2() {
  local version="${1#v}" stamp year month day hour minute second
  [[ "$version" =~ $PAIMOS_RELEASE_CALENDAR_V2_RE ]] || return 1
  stamp="${version%%.*}"
  year="${stamp:0:2}" month="${stamp:2:2}" day="${stamp:4:2}"
  hour="${stamp:6:2}" minute="${stamp:8:2}" second="${stamp:10:2}"
  (( 10#$hour <= 23 && 10#$minute <= 59 && 10#$second <= 59 )) || return 1
  release_version::valid_ymd "$year" "$month" "$day"
}

release_version::is_semver() {
  local version="${1#v}"
  [[ "$version" =~ $PAIMOS_RELEASE_SEMVER_RE && "$version" != "6.0.0" ]] || return 1
  # Legacy SemVer lines only ever had a single-digit major (0.x–5.x). Anything
  # wider belongs to a calendar grammar, so a malformed or impossible calendar
  # coordinate (two, ten, twelve, fourteen digits…) can never fall through as
  # legacy SemVer.
  [[ "$version" =~ ^[0-9]\. ]]
}

release_version::is_supported() {
  release_version::is_calendar_v2 "$1" || release_version::is_calendar "$1" || release_version::is_semver "$1"
}

# Any calendar era (v1 or v2): exact Docker tag, no mutable numeric aliases.
release_version::is_any_calendar() {
  release_version::is_calendar_v2 "$1" || release_version::is_calendar "$1"
}

release_version::kind() {
  if release_version::is_calendar_v2 "$1"; then
    printf 'calendar-v2\n'
  elif release_version::is_calendar "$1"; then
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

release_version::calendar_iso_date() {
  local version="${1#v}" year month day
  release_version::is_calendar "$version" || return 1
  IFS=. read -r year month day _ _ <<<"$version"
  printf '20%s-%s-%s\n' "$year" "$month" "$day"
}

# v2 coordinates are reserved in UTC, never in a local zone.
release_version::utc_coordinate() {
  printf '%s.0.0\n' "$(date -u +%y%m%d%H%M%S)"
}

release_version::utc_date_compact() {
  date -u +%y%m%d
}

release_version::utc_iso_date() {
  date -u +%Y-%m-%d
}

release_version::calendar_v2_iso_date() {
  local version="${1#v}" stamp
  release_version::is_calendar_v2 "$version" || return 1
  stamp="${version%%.*}"
  printf '20%s-%s-%s\n' "${stamp:0:2}" "${stamp:2:2}" "${stamp:4:2}"
}

release_version::calendar_v2_day() {
  local version="${1#v}"
  printf '%s\n' "${version:0:6}"
}

release_version::calendar_v2_stamp() {
  local version="${1#v}"
  printf '%s\n' "${version%%.*}"
}

# Same-day and strictly-later policy for a v2 reservation. Existing tags are
# supplied by the caller (one per line). A coordinate is accepted when it
# carries today's UTC day, is later than every published v2 coordinate, and is
# not in the future; an exact already-published tag is resume evidence.
release_version::calendar_v2_reservation_policy() {
  local version="${1#v}" existing_tags="${2:-}" tag exact=0 stamp now_stamp
  release_version::is_calendar_v2 "$version" || return 1
  stamp="$(release_version::calendar_v2_stamp "$version")"
  while IFS= read -r tag; do
    tag="${tag#v}"
    [[ -n "$tag" ]] || continue
    [[ "$tag" == "$version" ]] && exact=1
    if [[ "$tag" != "$version" ]] && release_version::is_calendar_v2 "$tag"; then
      (( 10#$(release_version::calendar_v2_stamp "$tag") < 10#$stamp )) || return 1
    fi
  done <<<"$existing_tags"
  (( exact == 1 )) && return 0
  [[ "$(release_version::calendar_v2_day "$version")" == "$(release_version::utc_date_compact)" ]] || return 1
  now_stamp="$(date -u +%y%m%d%H%M%S)"
  (( 10#$stamp <= 10#$now_stamp ))
}

# True once any v2 coordinate has been published: every older era is closed.
release_version::has_calendar_v2_tag() {
  local tag
  while IFS= read -r tag; do
    [[ -n "$tag" ]] && release_version::is_calendar_v2 "$tag" && return 0
  done <<<"${1:-}"
  return 1
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
  local version="${1#v}" existing_tags="${2:-}" day exact=0 prior=0 tag
  release_version::is_calendar "$version" || return 1
  day="$(release_version::calendar_day "$version")"
  while IFS= read -r tag; do
    tag="${tag#v}"
    [[ "$tag" == "$version" ]] && exact=1
    [[ "$tag" != "$version" && ( "$tag" == "$day" || "$tag" == "$day".* ) ]] && prior=1
  done <<<"$existing_tags"
  if release_version::has_recut_suffix "$version"; then
    # A recut always needs a different, already-published same-day release.
    # Its own exact tag is resume evidence, never evidence of the prior cut.
    (( prior == 1 )) || return 1
    (( exact == 1 )) && return 0
  else
    # An exact published tag may be a resume after midnight. The protected
    # release flow must still prove it targets the canonical merge.
    (( exact == 1 )) && return 0
    (( prior == 0 )) || return 1
  fi
  release_version::is_calendar_cut_today "$version"
}

release_version::tag_filter() {
  local tag
  while IFS= read -r tag; do
    [[ "$tag" == v* ]] && release_version::is_supported "$tag" && printf '%s\n' "$tag"
  done
  return 0
}
