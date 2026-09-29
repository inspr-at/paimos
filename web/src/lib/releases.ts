// SPDX-License-Identifier: AGPL-3.0-only
// The release history (schema inspr.release-history.v1, served by GET /api/releases)
// and what the release sheet derives from it: days, change groups, stats, compare
// ranges and search. Free of Vue for unit tests.
import { api } from './api.ts'

export type ChangeGroup = 'features' | 'fixes' | 'other'
export interface LinkedTicket { key: string; pill_en: string; pill_de: string; benefit_en: string; benefit_de: string }
export interface ReleaseChange { commit: string; subject: string; type: 'feat' | 'fix' | 'test' | 'docs' | 'release' | 'refactor' | 'chore' | 'other'; scope: string; tickets: string[]; at: string; group?: ChangeGroup; linked_tickets?: LinkedTicket[] }
export interface ReleaseRun { name: string; url: string; status: string; conclusion: string }
export interface ReleaseEvidence {
  source_commit: string; source_url: string; image: { reference: string; digest: string } | null
  ci: ReleaseRun | null; release_run: ReleaseRun | null; release_url: string; unavailable: string[]
}
export interface ReleaseNoteItem { id: string; key: string; pill_en: string; pill_de: string; benefit_en: string; benefit_de: string }
export interface ReleaseNotes { source: string; fallback?: 'historical-tag-headline'; snapshot_sha256: string; captured_at: string | null; release_revision: number; items: ReleaseNoteItem[]; gaps: string[]; hidden: number; written_after_release?: boolean }
export interface Release {
  notes?: ReleaseNotes
  version: string; tag: string; release_channel: string; release_sequence: number; state: 'published' | 'reserved'
  reserved_at: string | null; tagged_at: string | null; published_at: string | null; headline: string
  tickets: string[]; changes: ReleaseChange[]; changes_omitted: number; evidence: ReleaseEvidence
}
export interface ReleaseHistory {
  schema: string; product: string; repository: string; version_scheme: string; generated_at: string
  source: 'git+github' | 'git' | 'none'; current: string; live_since: string; releases: Release[]
}

export async function getReleases(): Promise<ReleaseHistory> {
  const response = await api('/releases')
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    throw new Error(typeof body?.error === 'string' ? body.error : `The release history could not be loaded (${response.status})`)
  }
  const body = await response.json() as ReleaseHistory
  if (body.schema !== 'inspr.release-history.v1' || !Array.isArray(body.releases)) throw new Error('The server sent an unknown release history format.')
  return body
}

// One release from the server's history; null when it has none for that version.
export async function getRelease(version: string): Promise<Release | null> {
  try {
    const response = await api(`/releases/${encodeURIComponent(version)}`)
    return response.ok ? await response.json() as Release : null
  } catch { return null }
}

// When a release happened: published, else tagged, else reserved.
export function releasedAt(r: Pick<Release, 'published_at' | 'tagged_at' | 'reserved_at'>): string | null {
  return r.published_at ?? r.tagged_at ?? r.reserved_at
}
const time = (r: Release) => { const at = releasedAt(r); return at ? Date.parse(at) : 0 }

// ---------- Days ----------
export function dayKey(at: number) {
  const d = new Date(at)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}
export interface Day { key: string; label: string; releases: Release[] }
// Groups newest first; labels read "Today", "Yesterday", "Tuesday, 22 September".
export function groupByDay(releases: Release[], now: number): Day[] {
  const days: Day[] = []
  const n = new Date(now), today = dayKey(now), yesterday = dayKey(new Date(n.getFullYear(), n.getMonth(), n.getDate() - 1).getTime())
  for (const r of [...releases].sort((a, b) => b.version.localeCompare(a.version))) {
    const at = time(r)
    const key = at ? dayKey(at) : 'unknown'
    let day = days.find(d => d.key === key)
    if (!day) {
      const label = key === today ? 'Today' : key === yesterday ? 'Yesterday' : at ? new Date(at).toLocaleDateString('en-GB', { weekday: 'long', day: 'numeric', month: 'long', ...(new Date(at).getFullYear() !== new Date(now).getFullYear() ? { year: 'numeric' } : {}) }) : 'Date unknown'
      day = { key, label, releases: [] }
      days.push(day)
    }
    day.releases.push(r)
  }
  return days
}

// ---------- Changes ----------
export const GROUP_OF: Record<ReleaseChange['type'], ChangeGroup | null> = {
  feat: 'features', fix: 'fixes', test: 'other', docs: 'other', refactor: 'other', chore: 'other', other: 'other',
  // The version bump is the release itself, not a change in it.
  release: null,
}
// The server's group wins, so every client agrees. Older manifests omit it
// and the commit type decides. A version bump stays out of the groups.
export function changeGroup(c: ReleaseChange): ChangeGroup | null {
  if (c.type === 'release') return null
  if (c.group === 'features' || c.group === 'fixes' || c.group === 'other') return c.group
  return GROUP_OF[c.type]
}
export function groupChanges(changes: ReleaseChange[]): Record<ChangeGroup, ReleaseChange[]> {
  const out: Record<ChangeGroup, ReleaseChange[]> = { features: [], fixes: [], other: [] }
  for (const c of changes) { const g = changeGroup(c); if (g) out[g].push(c) }
  return out
}
export interface TicketChangeLine { key: string; pill: string; benefit: string; pillLang: 'en' | 'de'; benefitLang: 'en' | 'de'; commits: ReleaseChange[] }
export interface PresentedChanges { features: TicketChangeLine[]; fixes: TicketChangeLine[]; other: ReleaseChange[] }
// One line per visible ticket inside Features and Fixes. The pill and benefit
// follow the viewer's language. A feature or fix commit with no visible ticket
// joins Other, cleaned subject and all. A ticket can appear in both groups
// when its commits do.
function visibleNotes(change: ReleaseChange, locale?: string | null): LinkedTicket[] {
  const out: LinkedTicket[] = []
  const seen = new Set<string>()
  for (const note of change.linked_tickets ?? []) {
    const key = note.key?.trim()
    if (!key || seen.has(key)) continue
    const text = localizedNote(note, locale)
    if (!text.pill.trim() && !text.benefit.trim()) continue
    seen.add(key)
    out.push({ ...note, key })
  }
  return out
}
export function presentChanges(changes: ReleaseChange[], locale?: string | null): PresentedChanges {
  const buckets: Record<'features' | 'fixes', Map<string, TicketChangeLine>> = { features: new Map(), fixes: new Map() }
  const other: ReleaseChange[] = []
  for (const change of changes) {
    const group = changeGroup(change)
    if (!group) continue
    const notes = group === 'other' ? [] : visibleNotes(change, locale)
    if (group === 'other' || !notes.length) { other.push(change); continue }
    for (const note of notes) {
      let line = buckets[group].get(note.key)
      if (!line) {
        const text = localizedNote(note, locale)
        line = { key: note.key, pill: text.pill, benefit: text.benefit, pillLang: text.pillLang, benefitLang: text.benefitLang, commits: [] }
        buckets[group].set(note.key, line)
      }
      if (!line.commits.some(existing => existing.commit === change.commit)) line.commits.push(change)
    }
  }
  return { features: [...buckets.features.values()], fixes: [...buckets.fixes.values()], other }
}
// ---------- Display ----------
// Headlines and subjects as people read them, next to their ticket chips: the keys
// the chips already show are left out and the text starts with a capital. Display
// only; the history keeps the text as tagged.
const ONLY_KEY = /^[A-Z][A-Z0-9]{1,9}-[1-9]\d{0,6}$/
export const sentence = (text: string) => text.charAt(0).toUpperCase() + text.slice(1)
export function displayText(text: string, keys: Iterable<string>) {
  const shown = new Set(keys)
  const drop = (part: string) => ONLY_KEY.test(part) && shown.has(part)
  let out = text
    // "(AEON-67, AEON-75)" goes; "(WIP, AEON-13)" keeps "(WIP)".
    .replace(/\s*\(([^()]*)\)/g, (whole, inner: string) => {
      const parts = inner.split(/\s*[,;&+/]\s*|\s+and\s+/).map(p => p.trim()).filter(Boolean)
      const kept = parts.filter(p => !drop(p))
      if (kept.length === parts.length) return whole
      return kept.length ? ` (${kept.join(', ')})` : ''
    })
    // "AEON-75: edit and delete" reads "edit and delete".
    .replace(/^\s*([A-Z][A-Z0-9]{1,9}-[1-9]\d{0,6})\s*[:\-–]\s*/, (whole, key: string) => drop(key) ? '' : whole)
  out = out.replace(/\s+([,.;:])/g, '$1').replace(/[,;:\s]+$/, '').replace(/\s{2,}/g, ' ').trim()
  return sentence(out || text)
}
// Regenerated archives without snapshots keep their historical display. A real
// snapshot keeps its membership authoritative even when empty, hidden or incomplete.
export function hasUsableNotes(r: Pick<Release, 'notes'>): r is Pick<Release, 'notes'> & { notes: ReleaseNotes } {
  return !!r.notes && r.notes.source !== 'unavailable'
}
// The viewer's language. German locales use DE; everything else, including a
// missing profile, uses EN.
export function noteLocale(locale?: string | null): 'en' | 'de' {
  return locale?.trim().toLowerCase().startsWith('de') ? 'de' : 'en'
}
export interface LocalizedNote { pill: string; benefit: string; pillLang: 'en' | 'de'; benefitLang: 'en' | 'de' }
// One ticket's pill and sentence in the viewer's language. An empty German
// field falls back to English; English is never replaced by an empty string.
export function localizedNote(item: Pick<ReleaseNoteItem, 'pill_en' | 'pill_de' | 'benefit_en' | 'benefit_de'>, locale?: string | null): LocalizedNote {
  const de = noteLocale(locale) === 'de'
  const pillDe = item.pill_de.trim(), pillEn = item.pill_en.trim()
  const benefitDe = item.benefit_de.trim(), benefitEn = item.benefit_en.trim()
  const pillLang = de && pillDe ? 'de' : 'en'
  const benefitLang = de && benefitDe ? 'de' : 'en'
  return { pill: pillLang === 'de' ? pillDe : pillEn, benefit: benefitLang === 'de' ? benefitDe : benefitEn, pillLang, benefitLang }
}
export const HISTORICAL_TAG_FALLBACK = 'historical-tag-headline'
export const HISTORICAL_TAG_LABEL = 'Historical tag headline'
export const WRITTEN_AFTER_LABEL = 'Notes written after release'
// A backfilled snapshot was captured after publication. The hint stays off
// when the notes are only the historical headline.
export function writtenAfterRelease(r: Pick<Release, 'notes'>): boolean {
  return hasUsableNotes(r) && r.notes.written_after_release === true
}
// Tag headlines are only the fallback when member benefits were never captured.
export function historicalTagFallback(r: Pick<Release, 'notes' | 'headline'>): boolean {
  if (!r.headline.trim()) return false
  if (hasUsableNotes(r)) return false
  if (!r.notes) return true
  return r.notes.fallback === HISTORICAL_TAG_FALLBACK || r.notes.source === 'unavailable'
}
export function emptyNotesLine(locale?: string | null) {
  return noteLocale(locale) === 'de' ? 'Nur interne Änderungen.' : 'Internal changes only.'
}
export function hiddenNoteLine(count: number, locale?: string | null) {
  if (noteLocale(locale) === 'de') return count === 1 ? 'Ein Ticket ist in den Release Notes ausgeblendet.' : `${count} Tickets sind in den Release Notes ausgeblendet.`
  return count === 1 ? 'One ticket is hidden from release notes.' : `${count} tickets are hidden from release notes.`
}
export function displayHeadline(r: Pick<Release, 'headline' | 'tickets' | 'changes' | 'notes'>, locale?: string | null) {
  if (hasUsableNotes(r)) {
    const pills = r.notes.items.map(item => localizedNote(item, locale).pill).filter(Boolean).join(' · ')
    if (pills) return pills
    if (r.notes.gaps.length) return 'Release notes unavailable'
    // Internal-only releases keep a useful list title; snapshot membership still wins.
  }
  return displayText(r.headline, ticketsOf(r as Release))
}
const PACKAGE_PREFIX = /^P\d+\.(?:x|\d+)\s*:\s*/i
const LEADING_TICKET = /^[A-Z][A-Z0-9]{1,9}-[1-9]\d{0,6}\s*[:\-–]\s*/
// A subject without its conventional prefix ("feat(AEON-74): wide lists" reads "Wide lists").
// Package prefixes ("P0.x:", "P0.3:") and a leading ticket key go too.
export function plainSubject(subject: string, tickets: string[] = []) {
  const m = /^[a-z]+(?:\([^)]*\))?!?:\s*(.+)$/.exec(subject)
  let text = m ? m[1] : subject
  for (let i = 0; i < 4; i++) {
    const next = text.replace(PACKAGE_PREFIX, '').replace(LEADING_TICKET, '')
    if (next === text) break
    text = next
  }
  return displayText(text, tickets)
}

// ---------- Stats ----------
export interface ReleaseStats { today: number; week: number; last: number | null; median: number | null; cadence: number[] }
export function stats(releases: Release[], now: number, days = 14): ReleaseStats {
  const published = releases.filter(r => r.state === 'published' && time(r)).sort((a, b) => time(a) - time(b))
  const todayKey = dayKey(now)
  const d = new Date(now)
  // Calendar arithmetic, not multiples of 24 h, so daylight saving changes do not shift days.
  const midnight = (back: number) => new Date(d.getFullYear(), d.getMonth(), d.getDate() - back).getTime()
  const monday = midnight((d.getDay() + 6) % 7)
  const gaps: number[] = []
  for (let i = 1; i < published.length; i++) gaps.push(time(published[i]) - time(published[i - 1]))
  gaps.sort((a, b) => a - b)
  const median = gaps.length ? (gaps.length % 2 ? gaps[(gaps.length - 1) / 2] : (gaps[gaps.length / 2 - 1] + gaps[gaps.length / 2]) / 2) : null
  const cadence = Array.from({ length: days }, (_, i) => {
    const key = dayKey(midnight(days - 1 - i))
    return published.filter(r => dayKey(time(r)) === key).length
  })
  return {
    today: published.filter(r => dayKey(time(r)) === todayKey).length,
    week: published.filter(r => time(r) >= monday).length,
    last: published.length ? time(published[published.length - 1]) : null,
    median, cadence,
  }
}
// "3 h 12 min", "2 days", "45 min".
export function span(ms: number) {
  const minutes = Math.round(ms / 60_000)
  if (minutes < 60) return `${Math.max(1, minutes)} min`
  const hours = Math.floor(minutes / 60), rest = minutes % 60
  if (hours < 24) return rest ? `${hours} h ${rest} min` : `${hours} h`
  const days = Math.round(hours / 24)
  return `${days} ${days === 1 ? 'day' : 'days'}`
}

// ---------- Compare ----------
// Everything that shipped after `from` up to and including `to` (published releases).
export function compare(releases: Release[], a: string, b: string) {
  const [older, newer] = a < b ? [a, b] : [b, a]
  const between = releases.filter(r => r.state === 'published' && r.version > older && r.version <= newer).sort((x, y) => y.version.localeCompare(x.version))
  const changes = between.flatMap(r => r.changes)
  const seen = new Set<string>()
  const unique = changes.filter(c => (seen.has(c.commit) ? false : (seen.add(c.commit), true)))
  const tickets = [...new Set(between.flatMap(ticketsOf))].sort(naturalKey)
  return { older, newer, releases: between, changes: unique, groups: groupChanges(unique), tickets }
}
export function naturalKey(a: string, b: string) { return a.localeCompare(b, 'en', { numeric: true }) }

// ---------- Search and filters ----------
export interface ReleaseFilter { q: string; features: boolean; fixes: boolean; tickets: boolean }
export const ticketsOf = (r: Release) => [...new Set(hasUsableNotes(r) ? r.notes.items.map(item => item.key) : [...r.tickets, ...r.changes.flatMap(c => c.tickets)])].sort(naturalKey)
// The same lines the row counts. A feature or fix commit with no visible
// ticket is Other, so it must not pass the Features or Fixes filter.
export function matches(r: Release, f: ReleaseFilter, locale?: string | null) {
  const presented = presentChanges(r.changes, locale)
  if (f.features && !presented.features.length) return false
  if (f.fixes && !presented.fixes.length) return false
  if (f.tickets && !ticketsOf(r).length) return false
  const q = f.q.trim().toLowerCase()
  if (!q) return true
  return !!r.notes?.items.some(item => [item.pill_en, item.pill_de, item.benefit_en, item.benefit_de, item.key].some(text => text.toLowerCase().includes(q))) || r.version.includes(q) || r.headline.toLowerCase().includes(q) || r.tickets.some(t => t.toLowerCase().includes(q))
    || r.changes.some(c => c.subject.toLowerCase().includes(q) || c.tickets.some(t => t.toLowerCase().includes(q)) || (c.linked_tickets ?? []).some(t => [t.pill_en, t.pill_de, t.benefit_en, t.benefit_de, t.key].some(text => text.toLowerCase().includes(q))))
}

// ---------- New since the last visit ----------
// Releases newer than the one the person last saw; nothing on a first visit.
export function newSince(releases: Release[], lastSeen: string | null) {
  if (!lastSeen) return new Set<string>()
  return new Set(releases.filter(r => r.state === 'published' && r.version > lastSeen).map(r => r.version))
}
export const shortCommit = (sha: string) => sha.slice(0, 7)

// ---------- One notice after a deploy ----------
// Calendar versions are fixed-width, so string order is version order.
// The releases store uses the same test.
const CALENDAR_VERSION = /^\d{12}\.\d+\.\d+$/
export function isCalendarVersion(value: string | null | undefined): value is string {
  return !!value && CALENDAR_VERSION.test(value)
}

// A page older than the server already says a newer version is live. That
// version missing from this build's history is the same fact, so the history
// shows one notice, never both.
export function releaseNotice(pageVersion: string | null | undefined, serverVersion: string, missingVersion: string): 'update' | 'missing' | null {
  if (isCalendarVersion(pageVersion) && isCalendarVersion(serverVersion) && serverVersion > pageVersion) return 'update'
  return missingVersion ? 'missing' : null
}
