// SPDX-License-Identifier: AGPL-3.0-only
import { api } from '../api'
import type { QuoteProfileDefinition, QuoteProfileSnapshot } from './types'
import { decimalCents } from './layout'
import { clone } from './profileForm'

export interface QuoteProfile { id: string; name: string; revision: number; definition: QuoteProfileDefinition; archived: boolean }
export const profileAssetUrl = (id: string) => `/api/quote-profiles/assets/${encodeURIComponent(id)}`
const json = async <T>(pending: Promise<Response>): Promise<T> => {
  const response = await pending
  if (!response.ok) {
    const data = await response.json().catch(() => ({})) as { error?: string }
    throw new Error(data.error || `Profile request failed (${response.status})`)
  }
  return response.json() as Promise<T>
}
export const listProfiles = () => json<QuoteProfile[]>(api('/quote-profiles'))
export const saveProfile = (name: string, definition: QuoteProfileDefinition, existing?: QuoteProfile) => json<QuoteProfile>(api(existing ? `/quote-profiles/${existing.id}` : '/quote-profiles', {
  method: existing ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ name, definition, ...(existing ? { expected_revision: existing.revision } : {}) }),
}))
export const archiveProfile = async (id: string) => { const response = await api(`/quote-profiles/${id}`, { method: 'DELETE' }); if (!response.ok) throw new Error(`Archive failed (${response.status})`) }
export const undoProfile = (profile: QuoteProfile) => json<QuoteProfile>(api(`/quote-profiles/${profile.id}/undo`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: profile.revision }) }))
export const uploadProfileAsset = (file: Blob) => json<{ id: string; content_type: string; sha256?: string; size?: number }>(api('/quote-profiles/assets', { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: file }))
// One profile, now or at a kept revision (issued quotes show the one they froze).
export const getProfile = (id: string, revision?: number) => json<QuoteProfile>(api(`/quote-profiles/${encodeURIComponent(id)}${revision ? `?revision=${revision}` : ''}`))
// A copy starts as its own profile at revision 1; undoing it archives the copy.
export const duplicateProfile = (profile: QuoteProfile, name: string) => saveProfile(name, clone(profile.definition))
// Snapshots the profile's current revision into an editable draft.
export const selectQuoteProfile = (quoteId: string, expectedDraftRevision: number, profileId: string) => json<{ draft_revision: number }>(api(`/quotes/${encodeURIComponent(quoteId)}/profile`, {
  method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_draft_revision: expectedDraftRevision, profile_id: profileId }),
}))
// A snapshot for the editor's preview: its id changes with the fonts, so a face
// added, swapped or removed before saving loads under a fresh family name.
export function previewSnapshot(definition: QuoteProfileDefinition, key = 'draft'): QuoteProfileSnapshot {
  let hash = 0
  for (const ch of JSON.stringify(definition.fonts)) hash = (hash * 31 + ch.charCodeAt(0)) >>> 0
  return { id: `preview-${key.replace(/[^a-z0-9]/gi, '')}-${hash.toString(36)}`, revision: 1, definition }
}

// A snapshot as the editor may read it (AEON-274). Profiles stored before the
// server normalised them can carry null where a list or map belongs (fonts:
// null crashed the quote page); read those as empty. A well-formed snapshot is
// returned as is, so identity and reactivity stay unchanged; a malformed one is
// copied, never mutated, so a save still sends the document as it was loaded.
const LISTS = ['fonts'] as const
const MAPS = ['colors', 'typography', 'cover', 'sections', 'labels', 'page', 'positions_table', 'totals', 'payment_terms', 'acceptance', 'footer'] as const
const isMap = (value: unknown) => typeof value === 'object' && value !== null && !Array.isArray(value)
export function normalizeProfile<T extends QuoteProfileSnapshot | null | undefined>(profile: T): T {
  const d = profile?.definition as Record<string, unknown> | null | undefined
  if (!profile) return profile
  const columns = isMap(d?.positions_table) ? (d!.positions_table as Record<string, unknown>).columns : undefined
  if (isMap(d) && LISTS.every(key => Array.isArray(d![key])) && MAPS.every(key => isMap(d![key])) && Array.isArray(columns)) return profile
  const next: Record<string, unknown> = { ...(isMap(d) ? d : {}) }
  for (const key of LISTS) if (!Array.isArray(next[key])) next[key] = []
  for (const key of MAPS) if (!isMap(next[key])) next[key] = {}
  const table = next.positions_table as Record<string, unknown>
  if (!Array.isArray(table.columns)) next.positions_table = { ...table, columns: [] }
  return { ...profile, definition: next as unknown as QuoteProfileDefinition }
}

export function defaultProfile(): QuoteProfileDefinition {
  return {
    schema: 'inspr.document-profile.v1', layout_variant: 'classic-v1', locale: 'de-AT', fonts: [],
    colors: { ink: '#253335', muted: '#637477', soft: '#91a1a3', accent: '#287f78', rule: '#d5dfdf', paper: '#ffffff' },
    typography: { body_pt: '10', title_pt: '17', section_pt: '18', table_pt: '9.6', footer_pt: '7.5' },
    page: { width_mm: '210', height_mm: '297', top_mm: '18', right_mm: '20', bottom_mm: '16', left_mm: '22' },
    cover: { top_mm: '11', title_gap_mm: '7', columns_gap_mm: '8', columns_padding_mm: '6' },
    sections: { numbering: 'upper-roman', heading_case: 'upper' },
    positions_table: { columns: [
      { key: 'position', width_mm: '9' }, { key: 'description', width_mm: '71' }, { key: 'quantity', width_mm: '15' },
      { key: 'unit', width_mm: '22' }, { key: 'unit_price', width_mm: '24' }, { key: 'total', width_mm: '27' },
    ], separator: 'rule', repeat_header: true },
    totals: { vat: 'note', discount: 'hidden', net_label: 'Nettosumme' },
    payment_terms: { position: 'sections', heading: 'Zahlungsbedingungen' },
    acceptance: { signature_columns: 2, gap_mm: '14', lead_mm: '28' },
    footer: { width_mm: '33', offset_mm: '0', page_number_format: 'SEITE {page} VON {total}' },
    labels: { quote: 'ANGEBOT', recipient: 'Auftraggeber', positions: 'Leistungsaufstellung', number: 'Angebotsnummer', date: 'Angebotsdatum', customer: 'Kundennummer', valid: 'Gültig bis', contact: 'Ansprechpartner', project: 'Projektreferenz', net: 'Nettosumme', signature_customer: 'Ort, Datum, Unterschrift Auftraggeber', signature_sender: 'Ort, Datum, Unterschrift Auftragnehmer' },
  }
}

// A date as the document prints it: de-AT "21.09.2026", English in the European
// order "21/09/2026" (a quote from Graz is not written for the US).
export function profileDate(snapshot: QuoteProfileSnapshot | null | undefined, iso: string): string {
  if (!iso) return ''
  const profile = normalizeProfile(snapshot)
  const locale = profile?.definition.locale === 'en' ? 'en-GB' : 'de-AT'
  return new Intl.DateTimeFormat(locale, { day: '2-digit', month: '2-digit', year: 'numeric', timeZone: 'UTC' }).format(new Date(`${iso}T00:00:00Z`))
}
export const profileLabel = (profile: QuoteProfileSnapshot | null | undefined, key: string, fallback: string) => normalizeProfile(profile)?.definition.labels[key] || fallback
export const pageNumber = (profile: QuoteProfileSnapshot | null | undefined, page: number, total: number) => (normalizeProfile(profile)?.definition.footer.page_number_format || '{page} / {total}').replaceAll('{page}', String(page)).replaceAll('{total}', String(total))
export function profileMoney(cents: number, currency: string, snapshot: QuoteProfileSnapshot | null | undefined): string {
  const profile = normalizeProfile(snapshot)
  const [whole, fraction] = decimalCents(cents).split('.')
  const mark = profile?.definition.locale === 'en' ? ',' : '.'
  const decimal = profile?.definition.locale === 'en' ? '.' : ','
  const amount = `${whole!.replace(/\B(?=(\d{3})+(?!\d))/g, mark)}${decimal}${fraction}`
  return profile?.definition.layout_variant === 'classic-v1' && profile.definition.locale !== 'en' && currency === 'EUR' ? `€ ${amount}` : `${amount} ${currency}`
}

export function profileStyle(snapshot: QuoteProfileSnapshot | null | undefined): Record<string, string> {
  const profile = normalizeProfile(snapshot)
  if (!profile) return {}
  const d = profile.definition
  const style: Record<string, string> = {
    '--ink': d.colors.ink, '--ink-2': d.colors.muted, '--ink-3': d.colors.soft, '--teal': d.colors.accent,
    '--line': d.colors.rule, '--line-2': d.colors.rule, '--quote-paper': d.colors.paper,
    '--quote-top': `${d.page.top_mm}mm`, '--quote-right': `${d.page.right_mm}mm`, '--quote-bottom': `${d.page.bottom_mm}mm`, '--quote-left': `${d.page.left_mm}mm`,
    '--quote-content-height': `${297 - Number(d.page.top_mm) - Number(d.page.bottom_mm) - 22}mm`,
    '--quote-body-size': `${d.typography.body_pt || '10'}pt`, '--quote-title-size': `${d.typography.title_pt || '17'}pt`,
    '--quote-section-size': `${d.typography.section_pt || '18'}pt`, '--quote-table-size': `${d.typography.table_pt || '9.6'}pt`,
    '--quote-footer-size': `${d.typography.footer_pt || '7.5'}pt`,
    '--quote-cover-top': `${d.cover.top_mm || '11'}mm`, '--quote-title-gap': `${d.cover.title_gap_mm || '7'}mm`,
    '--quote-columns-gap': `${d.cover.columns_gap_mm || '8'}mm`, '--quote-columns-padding': `${d.cover.columns_padding_mm || '6'}mm`,
    '--quote-signature-gap': `${d.acceptance.gap_mm}mm`, '--quote-signature-lead': `${d.acceptance.lead_mm}mm`,
    '--quote-footer-width': `${d.footer.width_mm}mm`, '--quote-footer-offset': `${d.footer.offset_mm}mm`,
    '--quote-heading-transform': d.sections.heading_case === 'upper' ? 'uppercase' : 'none',
  }
  d.positions_table.columns.forEach((column, i) => { style[`--quote-column-${i + 1}`] = `${column.width_mm}mm` })
  for (const role of ['body', 'display'] as const) if (d.fonts.some(f => f.role === role)) style[`--quote-${role}-font`] = `"QuoteProfile${profile.id.replaceAll('-', '')}${role}"`
  return style
}

const fontLoads = new Map<string, Promise<void>>()
export function loadProfileFonts(snapshot: QuoteProfileSnapshot | null | undefined): Promise<void> {
  const profile = normalizeProfile(snapshot)
  if (!profile?.definition.fonts.length) return Promise.resolve()
  const key = `${profile.id}:${profile.revision}`
  let pending = fontLoads.get(key)
  if (!pending) {
    pending = Promise.all(profile.definition.fonts.map(async face => {
      const name = `QuoteProfile${profile.id.replaceAll('-', '')}${face.role}`
      const font = new FontFace(name, `url("${profileAssetUrl(face.asset_id)}")`, { weight: String(face.weight), style: face.style })
      await font.load()
      document.fonts.add(font)
    })).then(() => undefined)
    fontLoads.set(key, pending)
  }
  return pending
}
