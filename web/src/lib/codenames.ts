// SPDX-License-Identifier: AGPL-3.0-only
import { shallowReactive } from 'vue'
import type { ToastCounts, ToastRelease } from './toast.ts'

// Marketing names front and centre (AEON-430): a release is known by its
// codename; the calendar version ("26·09·30 11:53") is revealed on hover and
// numbers stay in the release notes. The names reach the page with the release
// history and /api/version; this map lets any surface that only has a version
// (a release control, a settings line) name it.
const names = shallowReactive(new Map<string, string>())

const canonical = (version: string) => version.replace(/^v/, '')

export function rememberCodename(version: string | null | undefined, name: string | null | undefined) {
  const key = canonical(version ?? '')
  if (key && name && names.get(key) !== name) names.set(key, name)
}

export function rememberCodenames(releases: readonly { version: string; codename?: string }[]) {
  for (const release of releases) rememberCodename(release.version, release.codename)
}

// The marketing name of a version, or '' when none is known (a build without
// history, a release of another product, a release not reserved yet).
export const codenameOf = (version: string | null | undefined): string => names.get(canonical(version ?? '')) ?? ''

// The one line a screen reader or a title gets: the name, then its version.
export function releaseAria(name: string, version: string): string {
  return name ? `${name}, version ${canonical(version)}` : `version ${canonical(version)}`
}

// The update toast's sentence, which ends at the name (AEON-1655). The name
// comes from the server's release detail, else from what /api/version already
// told this page; only a release with no known name shows as its version.
// Counts of the release's notes by kind follow the sentence when there are any.
export function updateToast(wordmark: string, version: string, detailName: string | undefined, counts?: ToastCounts): { message: string; release: ToastRelease } {
  const name = detailName || codenameOf(version)
  const before = `${wordmark} was updated to `
  return { message: `${before}${name || version}`, release: { version, name, before, after: '', ...(counts?.items.length ? { counts } : {}) } }
}
