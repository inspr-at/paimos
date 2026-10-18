// SPDX-License-Identifier: AGPL-3.0-only
export type DisplayLanguage = 'en' | 'de'

// The shell and app pages are not fully translated yet. Keep their controls,
// cards and built-in data in English together. Region/date preferences do not
// change this policy. Enable German here only with complete app translations.
// Release notes are independently translated content with their own DE/EN
// choice (AEON-323), including the profile default when no choice was made.
// The START handoff sheet is the same kind of surface (AEON-1441): both
// languages are complete there, and it follows the handoff or the browser
// before the draft is read. It does not switch the rest of the app.
export function displayLanguage(preferredLocale?: string | null, surface: 'app' | 'release-notes' | 'start' = 'app'): DisplayLanguage {
  if ((surface === 'release-notes' || surface === 'start') && preferredLocale?.trim().toLowerCase().startsWith('de')) return 'de'
  return 'en'
}
