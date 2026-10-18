// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { displayLanguage } from '../src/lib/displayLanguage'
import { draftLanguage } from '../src/lib/importDraft'
import { displayLanguageViolations } from '../scripts/check-display-language.mjs'
import { releaseLang } from '../src/lib/releases'

const HANDOFF = 'A'.repeat(43)

// Risk: a German regional profile translates a single card inside an English page.
it('keeps the incomplete app in English and preserves the independent release-note choice', () => {
  for (const locale of [undefined, null, '', 'de', 'de-AT', 'DE-de', 'en-GB', 'fr-FR']) expect(displayLanguage(locale)).toBe('en')
  expect(displayLanguage(' de-AT ', 'release-notes')).toBe('de')
  expect(displayLanguage('fr-FR', 'release-notes')).toBe('en')
  expect(releaseLang('en', 'de-AT', 'de')).toBe('en')
  expect(releaseLang(undefined, 'de-AT', 'en')).toBe('en')
  expect(releaseLang(undefined, 'de-AT')).toBe('de')
  expect(displayLanguage('de-AT')).toBe('en')
})

it('rejects direct and aliased locale detectors while allowing language selection and region formatting', () => {
  for (const source of [
    "const de = profile.profile?.locale.startsWith('de')",
    "const de = /^de\\b/i.test(profile.profile?.locale ?? '')",
    "const locale = profile.profile?.locale; const de = locale === 'de'",
    "const de = 'de-AT' === profile.locale",
    "const de = profile . locale === 'de'",
    "const de = document.documentElement.lang.startsWith('de')",
    "const de = route.query.lang === 'de'",
    "const de = locale.toLowerCase().startsWith('de')",
  ]) expect(displayLanguageViolations(source, 'components/NewCard.vue.ts'), source).not.toHaveLength(0)
  const allowed = "const language = displayLanguage(profile.profile?.locale); const de = language === 'de'; new Intl.DateTimeFormat(profile.profile?.locale); // profile.locale.startsWith('de')"
  expect(displayLanguageViolations(allowed, 'lib/words.ts')).toEqual([])
  expect(displayLanguageViolations("<script setup>const de = profile.locale === 'de'</script>", 'components/New.vue')).toHaveLength(1)
  expect(displayLanguageViolations('<template><Card :german="profile.profile?.locale === \'de\'" /></template>', 'components/New.vue')).toHaveLength(1)
})

// Risk: the START sheet detects the browser language on its own, so one card
// can be German while the rest of the app stays English, or a German browser
// is ignored before the draft is read (AEON-1441).
it('follows the START handoff language, else the browser, through the shared helper', () => {
  expect(displayLanguage('de-AT', 'start')).toBe('de')
  expect(displayLanguage(' DE-de ', 'start')).toBe('de')
  expect(displayLanguage('en-GB', 'start')).toBe('en')
  expect(displayLanguage('fr-FR', 'start')).toBe('en')
  expect(displayLanguage(undefined, 'start')).toBe('en')
  expect(displayLanguage('de-AT')).toBe('en')
  const source = readFileSync(new URL('../src/lib/importDraft.ts', import.meta.url), 'utf8')
  expect(displayLanguageViolations(source, 'lib/importDraft.ts'), source).toEqual([])
  expect(draftLanguage({ draft: HANDOFF, lang: 'en' }, 'de-AT')).toBe('en')
  expect(draftLanguage({ draft: HANDOFF, lang: 'de' }, 'en-GB')).toBe('de')
  expect(draftLanguage({ draft: HANDOFF, lang: null }, 'de-AT')).toBe('de')
  expect(draftLanguage(null, 'de')).toBe('de')
  expect(draftLanguage(null, 'fr-FR')).toBe('en')
  expect(draftLanguage(null, ' de-AT ')).toBe('de')
})

// This whole-source guard also catches files that were not in AEON-998's inventory.
// Reading every source file exceeds the 5s default when the unit suite shares a runner.
it('has no app locale switches outside the shared helper', () => {
  const violations: unknown[] = []
  function scan(directory: URL, prefix = '') {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const filename = `${prefix}${entry.name}`, path = new URL(entry.name, directory)
      if (entry.isDirectory()) scan(new URL(`${entry.name}/`, directory), `${filename}/`)
      else if (/\.(ts|vue)$/.test(filename)) violations.push(...displayLanguageViolations(readFileSync(path, 'utf8'), filename))
    }
  }
  scan(new URL('../src/', import.meta.url))
  expect(violations).toEqual([])
}, 20_000)
