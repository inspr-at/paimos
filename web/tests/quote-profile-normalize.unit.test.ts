// SPDX-License-Identifier: AGPL-3.0-only
// AEON-274: a profile snapshot stored with null lists (fonts: null) crashed the
// quote page in profileStyle. Such snapshots read as empty lists and maps.
import { describe, expect, it } from 'vitest'
import { defaultProfile, loadProfileFonts, normalizeProfile, pageNumber, profileLabel, profileMoney, profileStyle } from '../src/lib/quotes/profile.ts'
import type { QuoteProfileSnapshot } from '../src/lib/quotes/types.ts'
import type { QuoteDocumentData } from '../src/lib/quotes/types.ts'
import { readFileSync } from 'node:fs'
import { QuoteEditor } from '../src/lib/quotes/editor.ts'

const snapshot = (): QuoteProfileSnapshot => ({ id: '7e5a0000-0000-4000-8000-000000000009', revision: 1, definition: defaultProfile() })
const broken = () => {
  const s = snapshot()
  const d = s.definition as unknown as Record<string, unknown>
  for (const key of ['fonts', 'labels', 'sections', 'cover', 'typography']) d[key] = null
  ;(d.positions_table as Record<string, unknown>).columns = null
  return s
}

describe('normalizeProfile', () => {
  it('returns a well-formed snapshot unchanged, by identity', () => {
    const s = snapshot()
    expect(normalizeProfile(s)).toBe(s)
    expect(normalizeProfile(null)).toBeNull()
    expect(normalizeProfile(undefined)).toBeUndefined()
  })

  it('reads null lists and maps as empty without mutating the stored snapshot', () => {
    const s = broken()
    const n = normalizeProfile(s)!
    expect(n.definition.fonts).toEqual([])
    expect(n.definition.labels).toEqual({})
    expect(n.definition.sections).toEqual({})
    expect(n.definition.cover).toEqual({})
    expect(n.definition.positions_table.columns).toEqual([])
    expect(n.definition.positions_table.separator).toBe('rule')
    expect(n.definition.locale).toBe('de-AT')
    expect(s.definition.fonts).toBeNull()
  })

  it('keeps the profile helpers from throwing on a null-list snapshot', async () => {
    const s = broken()
    expect(() => profileStyle(s)).not.toThrow()
    expect(profileStyle(s)['--quote-body-size']).toBe('10pt')
    expect(profileLabel(s, 'quote', 'Angebot')).toBe('Angebot')
    expect(pageNumber(s, 1, 2)).toBe('SEITE 1 VON 2')
    expect(profileMoney(123456, 'EUR', s)).toBe('€ 1.234,56')
    await expect(loadProfileFonts(s)).resolves.toBeUndefined()
  })

  it('survives a snapshot without a definition', () => {
    const s = { id: 'x', revision: 1, definition: null } as unknown as QuoteProfileSnapshot
    expect(() => profileStyle(s)).not.toThrow()
    expect(profileLabel(s, 'quote', 'Angebot')).toBe('Angebot')
  })
})

it('AEON-1682: every stored showcase draft and version hydrates with its own profile and stable IDs', async () => {
  const quotes = JSON.parse(readFileSync(new URL('./quotes/fixtures/showcase-acme.json', import.meta.url), 'utf8')) as {
    key: string; documents: Record<string, QuoteDocumentData>
  }[]
  expect(quotes.map(quote => quote.key)).toEqual(['acme-acceptance', 'acme-fitout', 'acme-platform', 'acme-retainer', 'steinwender-belegleser'])
  for (const quote of quotes) for (const document of Object.values(quote.documents)) {
    const before = JSON.stringify(document)
    const editor = new QuoteEditor(document)
    expect(editor.document.title).toBe(document.title)
    expect(editor.document.net_total_cents).toBe(document.net_total_cents)
    expect(editor.document.sections.map(section => section.id)).toEqual(document.sections.map(section => section.id))
    const profile = normalizeProfile(document.profile)
    expect(() => profileStyle(profile)).not.toThrow()
    // No DOM is needed to normalize/derive the profile; font loading itself
    // remains the real CSP/font browser case.
    expect(profileStyle(profile)['--quote-body-size']).toBeTruthy()
    expect(JSON.stringify(document)).toBe(before)
  }
  const retainer = quotes.find(quote => quote.key === 'acme-retainer')!.documents.draft!
  expect(retainer.profile!.definition.locale).toBe('en')
  expect(retainer.profile!.definition.fonts).toEqual([])
  const stored = structuredClone(retainer.profile!)
  for (const key of ['fonts', 'labels', 'sections', 'cover', 'typography']) (stored.definition as unknown as Record<string, unknown>)[key] = null
  expect(profileStyle(stored)['--quote-body-size']).toBeTruthy()
  await expect(loadProfileFonts(stored)).resolves.toBeUndefined()
  expect(stored.definition.fonts).toBeNull()
})
