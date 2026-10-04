// SPDX-License-Identifier: AGPL-3.0-only
import { parts } from '../vendor/calendar-version-display/version.js'
import { releasedAt, type Release } from './releases'
import { relativeTime } from './work'

// The running build's coordinate is the fallback when its history has not loaded.
// Publication/tag metadata wins once available; never use a newer server release.
export function footerReleaseContent(value: string, scheme: string | undefined, name: string, count: number | null, release: Release | undefined, now: number, failed = false) {
  const coordinate = parts(value, scheme ?? '')
  const fallback = coordinate ? `20${coordinate.yy}-${coordinate.mm}-${coordinate.dd}T${coordinate.hh}:${coordinate.mi}:${coordinate.ss}Z` : ''
  const stamp = (release && releasedAt(release)) || fallback
  const date = new Date(stamp)
  const valid = Number.isFinite(date.getTime())
  const day = valid ? date.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' }) : ''
  const time = valid ? date.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', timeZone: 'UTC' }) : ''
  return {
    heading: 'Running release', name,
    version: value || (failed ? 'Version unavailable' : 'Loading release…'),
    released: valid ? `Released ${day}, ${time} UTC · ${relativeTime(stamp, { now })}` : '',
    action: 'Click for all releases', fresh: count !== null && count > 0 ? `${count} new` : '',
  }
}
