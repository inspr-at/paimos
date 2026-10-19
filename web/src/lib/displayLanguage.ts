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
// Definitions has an approved bilingual copy catalog (AEON-1317), including
// its workspace-change notices and Desk item; keep that choice scoped here.
export function displayLanguage(preferredLocale?: string | null, surface: 'app' | 'release-notes' | 'start' | 'definitions' = 'app'): DisplayLanguage {
  if ((surface === 'release-notes' || surface === 'start' || surface === 'definitions') && preferredLocale?.trim().toLowerCase().startsWith('de')) return 'de'
  return 'en'
}
