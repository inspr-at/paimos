// SPDX-License-Identifier: AGPL-3.0-only
export type DisplayLanguage = 'en' | 'de'

// The shell and app pages are not fully translated yet. Keep their controls,
// cards and built-in data in English together. Region/date preferences do not
// change this policy. Enable German here only with complete app translations.
// Release notes are independently translated content with their own DE/EN
// choice (AEON-323), including the profile default when no choice was made.
export function displayLanguage(preferredLocale?: string | null, surface: 'app' | 'release-notes' = 'app'): DisplayLanguage {
  if (surface === 'release-notes' && preferredLocale?.trim().toLowerCase().startsWith('de')) return 'de'
  return 'en'
}
