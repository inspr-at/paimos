// SPDX-License-Identifier: AGPL-3.0-only
// The release history (schema inspr.release-history.v1, served by GET /api/releases)
// and what the release sheet derives from it: days, change groups, stats, compare
// ranges and search. Free of Vue for unit tests.
import { api } from './api.ts'

export type ChangeGroup = 'features' | 'fixes' | 'other'
// group: the ticket's own features or fixes; older servers omit it.
export interface LinkedTicket { key: string; pill_en: string; pill_de: string; benefit_en: string; benefit_de: string; group?: 'features' | 'fixes' }
export interface ReleaseChange { commit: string; subject: string; type: 'feat' | 'fix' | 'test' | 'docs' | 'release' | 'refactor' | 'chore' | 'other'; scope: string; tickets: string[]; at: string; group?: ChangeGroup; linked_tickets?: LinkedTicket[] }
export interface ReleaseRun { name: string; url: string; status: string; conclusion: string }
export interface ReleaseEvidence {
  source_commit: string; source_url: string; image: { reference: string; digest: string } | null
  ci: ReleaseRun | null; release_run: ReleaseRun | null; release_url: string; unavailable: string[]
}
export interface ReleaseNoteItem { id: string; key: string; pill_en: string; pill_de: string; benefit_en: string; benefit_de: string }
export interface ReleaseNotes { source: string; fallback?: 'historical-tag-headline'; snapshot_sha256: string; captured_at: string | null; release_revision: number; items: ReleaseNoteItem[]; gaps: string[]; hidden: number; written_after_release?: boolean }
// How a release introduces itself (AEON-305): the theme is the kicker, the
// headline one sentence, the intro two or three. German may be empty.
export interface ReleasePresentation { theme_en: string; theme_de: string; headline_en: string; headline_de: string; intro_en: string; intro_de: string; revision: number; updated_at: string }
export interface Release {
  notes?: ReleaseNotes
  presentation?: ReleasePresentation
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
type TicketText = Pick<ReleaseNoteItem, 'key' | 'pill_en' | 'pill_de' | 'benefit_en' | 'benefit_de'> & { group?: string }
// The tickets a release tells about, in order: a captured snapshot's items when
// there is one, else each visible linked ticket as its commits first name it.
// A ticket with neither pill nor benefit in the viewer's language is not told.
function toldTickets(changes: ReleaseChange[], locale?: string | null, items?: TicketText[] | null): TicketText[] {
  const out: TicketText[] = []
  const seen = new Set<string>()
  const linked = () => changes.filter(c => { const g = changeGroup(c); return g === 'features' || g === 'fixes' }).flatMap(c => c.linked_tickets ?? [])
  for (const note of items ?? linked()) {
    const key = note.key?.trim()
    if (!key || seen.has(key)) continue
    const text = localizedNote(note, locale)
    if (!text.pill.trim() && !text.benefit.trim()) continue
    seen.add(key)
    out.push({ ...note, key })
  }
  return out
}
// Feature or fix, per ticket and never per commit, since a commit naming
// several tickets carries only the strongest group: the note's own group, else
// the server's group for this ticket on any commit, else the ticket's type as
// its own commits show it (a commit of its own without a conventional prefix
// carries it; a shared one grouped features holds no bug), else the commits'
// feat and fix prefixes, else a shared commit the server made a fix (older
// servers). A ticket with none of these is a feature.
function ticketGroup(key: string, commits: ReleaseChange[], note?: { group?: string }): 'features' | 'fixes' {
  const kind = (g?: string) => g === 'features' || g === 'fixes' ? g : null
  const told = kind(note?.group) ?? commits.flatMap(c => c.linked_tickets ?? []).map(t => t.key === key ? kind(t.group) : null).find(g => g)
  if (told) return told
  let bug = false, notBug = false, feat = false, fix = false, sharedFix = false
  for (const c of commits) {
    if (c.type === 'other' && c.group === 'fixes') { if (c.tickets.length === 1) bug = true; else sharedFix = true }
    if (c.type === 'other' && c.group === 'features') notBug = true
    if (c.type === 'feat') feat = true
    if (c.type === 'fix') fix = true
  }
  if (bug) return 'fixes'
  if (notBug || feat) return 'features'
  return fix || sharedFix ? 'fixes' : 'features'
}
// Every release reads the same (AEON-305): one block per told ticket under
// Features or Fixes, with the pill and benefit in the viewer's language and
// every commit that names the ticket. A told ticket without commits still gets
// its block. Commits no told ticket claims are Other; the version bump is left out.
export function presentChanges(changes: ReleaseChange[], locale?: string | null, items?: TicketText[] | null): PresentedChanges {
  const seen = new Set<string>()
  const commits = changes.filter(c => {
    if (!changeGroup(c) || seen.has(c.commit)) return false
    seen.add(c.commit)
    return true
  })
  const out: PresentedChanges = { features: [], fixes: [], other: [] }
  const claimed = new Set<string>()
  for (const note of toldTickets(changes, locale, items)) {
    const mine = commits.filter(c => c.tickets.includes(note.key))
    for (const c of mine) claimed.add(c.commit)
    const text = localizedNote(note, locale)
    out[ticketGroup(note.key, mine, note)].push({ key: note.key, pill: text.pill, benefit: text.benefit, pillLang: text.pillLang, benefitLang: text.benefitLang, commits: mine })
  }
  out.other = commits.filter(c => !claimed.has(c.commit))
  return out
}
// A release as the sheet shows it: a captured snapshot decides which tickets are
// told and their text; without one, the linked tickets do.
export function presentRelease(r: Pick<Release, 'notes' | 'changes'>, locale?: string | null): PresentedChanges {
  return presentChanges(r.changes, locale, hasUsableNotes(r) ? r.notes.items : null)
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
// The release history's own language and view, chosen in its header (AEON-323).
// The address wins, then this person's last choice on this device, then the
// profile locale. The view is Highlights unless the address says Details.
export type ReleaseLang = 'en' | 'de'
export type ReleaseView = 'highlights' | 'details'
const first = (value: unknown) => Array.isArray(value) ? value[0] : value
export function releaseLang(value: unknown, profileLocale?: string | null, remembered?: string | null): ReleaseLang {
  const raw = first(value)
  if (raw === 'en' || raw === 'de') return raw
  return remembered === 'en' || remembered === 'de' ? remembered : noteLocale(profileLocale)
}
export function releaseView(value: unknown): ReleaseView {
  return first(value) === 'details' ? 'details' : 'highlights'
}
// Where a person's language choice is kept on this device.
export const releaseLangKey = (principalId: string) => `aeon.release-history.lang.${principalId}`
// Details: a short technical line for a list row, the first few commit subjects.
export function technicalLine(r: Pick<Release, 'changes'>, limit = 3): string {
  const seen = new Set<string>()
  const parts: string[] = []
  for (const c of r.changes) {
    if (!changeGroup(c)) continue
    const text = plainSubject(c.subject, c.tickets)
    if (!text || seen.has(text)) continue
    seen.add(text)
    parts.push(text)
    if (parts.length === limit) break
  }
  return parts.join(' · ')
}
export interface LocalizedNote { pill: string; benefit: string; pillLang: 'en' | 'de'; benefitLang: 'en' | 'de' }
// One text in the chosen language; a missing one falls back to the other
// language, never to an empty string while either has words. The lang tells
// the reader (and the fallback badge) which one it is.
export function pickText(en: string | null | undefined, de: string | null | undefined, locale?: string | null): { text: string; lang: ReleaseLang } {
  const want = noteLocale(locale)
  const texts = { en: (en ?? '').trim(), de: (de ?? '').trim() }
  const other: ReleaseLang = want === 'de' ? 'en' : 'de'
  if (texts[want] || !texts[other]) return { text: texts[want], lang: want }
  return { text: texts[other], lang: other }
}
// One ticket's pill and sentence in the chosen language, each falling back on its own.
export function localizedNote(item: Pick<ReleaseNoteItem, 'pill_en' | 'pill_de' | 'benefit_en' | 'benefit_de'>, locale?: string | null): LocalizedNote {
  const pill = pickText(item.pill_en, item.pill_de, locale), benefit = pickText(item.benefit_en, item.benefit_de, locale)
  return { pill: pill.text, benefit: benefit.text, pillLang: pill.lang, benefitLang: benefit.lang }
}
export interface LocalizedPresentation { theme: string; headline: string; intro: string; themeLang: 'en' | 'de'; headlineLang: 'en' | 'de'; introLang: 'en' | 'de' }
// A release's theme, headline and intro in the chosen language, each falling
// back to the other one. Null when the release has no headline in either.
export function localizedPresentation(r: Pick<Release, 'presentation'>, locale?: string | null): LocalizedPresentation | null {
  const p = r.presentation
  if (!p) return null
  const theme = pickText(p.theme_en, p.theme_de, locale), headline = pickText(p.headline_en, p.headline_de, locale), intro = pickText(p.intro_en, p.intro_de, locale)
  if (!headline.text) return null
  return { theme: theme.text, headline: headline.text, intro: intro.text, themeLang: theme.lang, headlineLang: headline.lang, introLang: intro.lang }
}
// The pills a release tells, features first, for its title and rail line, each
// with the language it is shown in.
function toldLabels(r: Pick<Release, 'notes' | 'changes'>, locale?: string | null) {
  const p = presentRelease(r, locale)
  return [...p.features, ...p.fixes].map(line => line.pill ? { text: line.pill, lang: line.pillLang } : { text: line.benefit, lang: line.benefitLang })
}
// A name shown in the chosen language, or in the other one when any part of it
// had to fall back: that is the language its badge names.
export interface NamedText { text: string; lang: ReleaseLang }
function named(r: Pick<Release, 'changes' | 'notes' | 'presentation'>, locale: string | null | undefined, all: boolean): NamedText & { themed: boolean } {
  const want = noteLocale(locale)
  const presented = localizedPresentation(r, locale)
  if (presented) return presented.theme ? { text: presented.theme, lang: presented.themeLang, themed: true } : { text: presented.headline, lang: presented.headlineLang, themed: true }
  const labels = toldLabels(r, locale).slice(0, all ? undefined : 1)
  return { text: labels.map(l => l.text).join(' · '), lang: labels.find(l => l.lang !== want)?.lang ?? want, themed: false }
}
// A short name for a release in toasts and lists: its theme (or headline), else
// its first pill, else nothing, so callers show only version and date.
// The Git tag message is evidence and never a name outside Evidence.
export function releaseName(r: Pick<Release, 'changes' | 'notes' | 'presentation'>, locale?: string | null): NamedText {
  const { text, lang } = named(r, locale, false)
  return { text, lang }
}
export function releaseTitle(r: Pick<Release, 'changes' | 'notes' | 'presentation'>, locale?: string | null) {
  return releaseName(r, locale).text
}
// The rail's second line: the theme, else the pills. Git tag headlines are
// evidence, not names, so they are not shown there.
export function railLine(r: Release, locale?: string | null): { text: string; themed: boolean; lang: ReleaseLang } {
  return named(r, locale, true)
}
// Text split around each case-insensitive hit of the search query, for <mark>.
export function markParts(text: string, query?: string | null): { text: string; hit: boolean }[] {
  const q = query?.trim()
  if (!q) return [{ text, hit: false }]
  const out: { text: string; hit: boolean }[] = []
  const lower = text.toLowerCase(), needle = q.toLowerCase()
  let at = 0
  for (let i = lower.indexOf(needle); i !== -1; i = lower.indexOf(needle, at)) {
    if (i > at) out.push({ text: text.slice(at, i), hit: false })
    out.push({ text: text.slice(i, i + needle.length), hit: true })
    at = i + needle.length
  }
  if (at < text.length) out.push({ text: text.slice(at), hit: false })
  return out
}
export const HISTORICAL_TAG_FALLBACK = 'historical-tag-headline'
export const WRITTEN_AFTER_LABEL = 'Notes written after release'
export function writtenAfterLine(locale?: string | null) {
  return noteLocale(locale) === 'de' ? 'Notizen nach dem Release geschrieben' : WRITTEN_AFTER_LABEL
}
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
// The sheet's empty and compare lines in the chosen language (AEON-323).
export function releaseCopy(locale?: string | null) {
  const de = noteLocale(locale) === 'de'
  const plural = (n: number, one: string, many: string) => n === 1 ? one : many
  return de ? {
    nothingShipped: 'Unter dieser Version wurde nichts ausgeliefert.',
    noChanges: 'Zwischen diesem und dem vorherigen Release sind keine Änderungen verzeichnet.',
    omitted: (n: number) => `${plural(n, 'Eine weitere Änderung ist', `${n} weitere Änderungen sind`)} hier nicht aufgeführt.`,
    compareNone: 'Zwischen diesen Releases sind keine Änderungen verzeichnet.',
    comparePick: 'Zweites Release wählen',
    compareHint: ['Mit', 'durch die Liste gehen und', ' drücken oder ein Release anklicken. Die Änderungen vom älteren zum neueren werden hier zusammengezählt.'],
    comparingFrom: 'Vergleich ab',
    compareTap: 'Das andere Ende antippen.',
    compareOther: ['Das andere Ende mit', 'oder einem Klick wählen.'],
    noMatch: 'Kein Release passt',
    noMatchText: (count: number, q: string) => `Nichts in ${count} Releases passt zu ${q ? `„${q}“` : 'diesen Filtern'}.`,
    clear: 'Suche und Filter zurücksetzen',
    noHistory: 'Kein Release-Verlauf in diesem Build',
    noHistoryText: (dev: boolean) => `Release-Builds enthalten den Verlauf aller Tags. Dieser wurde ohne ihn gebaut${dev ? ', wie Entwicklungs-Builds' : ''}.`,
  } : {
    nothingShipped: 'Nothing shipped under this version.',
    noChanges: 'No changes are recorded between this release and the one before it.',
    omitted: (n: number) => `And ${n} more ${plural(n, 'change', 'changes')} not listed here.`,
    compareNone: 'No changes are recorded between these releases.',
    comparePick: 'Pick a second release',
    compareHint: ['Move through the list with', 'and press', ', or click a release. The changes from the older to the newer one are added up here.'],
    comparingFrom: 'Comparing from',
    compareTap: 'Tap the other end.',
    compareOther: ['Pick the other end with', 'or a click.'],
    noMatch: 'No release matches',
    noMatchText: (count: number, q: string) => `Nothing in ${count} releases fits ${q ? `“${q}”` : 'these filters'}.`,
    clear: 'Clear search and filters',
    noHistory: 'No release history in this build',
    noHistoryText: (dev: boolean) => `Release builds carry the history of every tag. This one was built without it${dev ? ', as development builds are' : ''}.`,
  }
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
// The filters follow the blocks the detail and the row counts show. Search
// looks at the text the chosen language and view show (AEON-323): both views
// show the name, pills, ticket keys, commit subjects and SHAs (Highlights opens
// a folded list on a hit); Highlights adds the benefits, Details the evidence
// it shows open.
export function matches(r: Release, f: ReleaseFilter, locale?: string | null, view: ReleaseView = 'highlights') {
  const presented = presentRelease(r, locale)
  if (f.features && !presented.features.length) return false
  if (f.fixes && !presented.fixes.length) return false
  if (f.tickets && !ticketsOf(r).length) return false
  const q = f.q.trim().toLowerCase()
  if (!q) return true
  const lines = [...presented.features, ...presented.fixes]
  const named = localizedPresentation(r, locale)
  const commits = [...lines.flatMap(line => line.commits), ...presented.other]
  const texts = [
    r.version, ...ticketsOf(r), ...r.tickets,
    ...(named ? [named.theme, named.headline, named.intro] : []),
    ...lines.map(line => line.pill || line.benefit),
    ...(view === 'highlights' ? lines.map(line => line.benefit) : evidenceSearch(r).texts),
    ...commits.flatMap(c => [plainSubject(c.subject, c.tickets), ...c.tickets]),
  ]
  const ids = [...commits.map(c => c.commit), ...(view === 'details' ? evidenceSearch(r).ids : [])]
  return texts.some(text => text.toLowerCase().includes(q)) || ids.some(id => idMatches(id, q))
}
// Hashes match from their start, as people type them, and from four characters,
// so a word like "dead" or "cafe" does not hit every hex string that holds it.
export function idMatches(id: string, q: string) {
  const needle = q.trim().toLowerCase().replace(/^sha256:/, '')
  return needle.length >= 4 && id.toLowerCase().replace(/^sha256:/, '').startsWith(needle)
}

// A CI or release run's outcome in words: "passed", "failed", "in progress".
const RUN_WORD: Record<string, string> = { success: 'passed', failure: 'failed', cancelled: 'cancelled', skipped: 'skipped', timed_out: 'timed out' }
export const runWord = (run: { status: string; conclusion: string }) => run.conclusion ? (RUN_WORD[run.conclusion] ?? run.conclusion.replace(/_/g, ' ')) : run.status.replace(/_/g, ' ')
// What Details shows open under Evidence, for search: words, and the hashes.
export function evidenceSearch(r: Release): { texts: string[]; ids: string[] } {
  const ev = r.evidence
  const runs = [ev?.ci, ev?.release_run].flatMap(run => run ? [run.name, runWord(run)] : [])
  return {
    texts: [r.headline, r.tag, r.notes?.source ?? '', ...runs, ev?.image?.reference ?? '', ...(ev?.unavailable ?? [])].filter(Boolean),
    ids: [r.notes?.snapshot_sha256 ?? '', ev?.source_commit ?? '', ev?.image?.digest ?? ''].filter(Boolean),
  }
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

// The history can still name the build this page loaded. The update poll may
// already have seen a newer version on the server. Talk about the newer of the
// two, so a cached history does not pretend the deploy is missing.
export function liveServer(historyCurrent: string, available?: string | null): string {
  if (isCalendarVersion(available) && (!isCalendarVersion(historyCurrent) || available > historyCurrent)) return available
  return historyCurrent
}

// A page older than the server already says a newer version is live. That
// version missing from this build's history is the same fact, so the history
// shows one notice, never both.
export function releaseNotice(pageVersion: string | null | undefined, serverVersion: string, missingVersion: string, available?: string | null): 'update' | 'missing' | null {
  const server = liveServer(serverVersion, available)
  if (isCalendarVersion(pageVersion) && isCalendarVersion(server) && server > pageVersion) return 'update'
  return missingVersion ? 'missing' : null
}
