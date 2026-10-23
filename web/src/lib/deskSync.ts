// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1700 (Tick UI spec v1): the Decision Desk's one sync line. It says only what the Desk knows: the last
// good read, a read under way, a live change waiting for its read, counted failures, the browser's network
// and visibility, and the real schedule of the next read. It invents no times.
import { displayLanguage, type DisplayLanguage } from './displayLanguage'

export type DeskSyncState = 'normal' | 'just' | 'checking' | 'change' | 'retrying' | 'quiet' | 'escalated' | 'offline' | 'never'
/** What the screen reader hears about: failures and their recovery, never a tick or a check. */
export type DeskSyncHealth = 'ok' | 'quiet' | 'escalated'
export interface DeskSyncInput {
  now: number
  /** The last good read of the whole desk; null before the first. */
  checkedAt: number | null
  /** A read under way, and whether a live change asked for it. */
  busy: boolean; change: boolean
  /** Failures counted since the last good read, and the refresh notice they give (deskRefreshNotice). */
  failures: number; notice: 'quiet' | 'escalated' | null
  /** AEON-1631: rows kept while a later read missed the desk, and the automatic retries' phase. */
  stale: boolean; retry: 'idle' | 'retrying' | 'exhausted'
  online: boolean; visible: boolean
  /** The live-event stream that wakes the Desk is connected. */
  live: boolean
  /** The earliest scheduled poll and a pending automatic retry; null when none is scheduled. */
  pollAt: number | null; retryAt: number | null
}
/** Visible words. A bare count reserves "59". A countdown (`unit`) keeps the number and that unit in one box. `short` is the ≤ 520 px form. */
export type DeskSyncPart = { text: string; short?: string } | { n: number; unit?: string }
export interface DeskSyncLine {
  state: DeskSyncState; health: DeskSyncHealth
  icon: 'dot' | 'refresh' | 'alert' | 'cloud-off' | null
  warn: boolean; spin: boolean; live: boolean
  parts: DeskSyncPart[]
  /** Try again is offered (the escalated state). */
  retry: boolean
  /** The full sentence with clock times, read on focus. */
  sentence: string
  /** The hover, focus and long-press tip with the exact clock. */
  tip: string
}

const JUST_MS = 10_000
// Fixed at 24 hours, as the Desk's other clock times (the refresh notice, AEON-1477).
export function deskClock(at: number, seconds = false) {
  return new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', ...(seconds ? { second: '2-digit' } : {}), hour12: false })
}
/** Whole seconds until a scheduled instant; a read that is due now still says 1 s until it starts. */
export const secondsUntil = (at: number, now: number) => Math.max(1, Math.ceil((at - now) / 1000))

type Ago = { kind: 'just' } | { kind: 's' | 'min'; n: number } | { kind: 'at'; clock: string }
function ago(at: number, now: number): Ago {
  const s = Math.max(0, Math.floor((now - at) / 1000))
  if (now - at < JUST_MS) return { kind: 'just' }
  if (s < 60) return { kind: 's', n: s }
  if (s < 3600) return { kind: 'min', n: Math.floor(s / 60) }
  return { kind: 'at', clock: deskClock(at) }
}

const t = (text: string, short?: string): DeskSyncPart => short === undefined ? { text } : { text, short }
// `unit` rides in the number's box (the width of "59" plus that unit), start-aligned, so spare width follows the unit.
const n = (value: number, unit?: string): DeskSyncPart => unit === undefined ? { n: value } : { n: value, unit }
// The separator keeps to the word before it, so a wrapped line never starts with "·".
const SEP = t('\u00a0· ')
// The one app language is English (AEON-1609); German is catalog input for AEON-1610, impersonal.
export const DESK_SYNC_COPY = {
  en: {
    live: 'Live',
    checked: (a: Ago) => a.kind === 'just' ? [t('checked just now')] : a.kind === 'at' ? [t(`checked at ${a.clock}`)] : [t('checked '), n(a.n), t(a.kind === 's' ? ' s ago' : ' min ago')],
    next: (s: number) => [t('next check in ', 'next in '), n(s, ' s')],
    checking: 'Checking now…', change: 'Change arrived · updating…', retrying: 'Checking again…',
    // ≤ 520 px the two long phrases shorten as "next in" does, so a phone keeps the line on one line (CI's sans is wide).
    quiet: (s: number | null, shown: string) => [t('Couldn’t check'), ...(s === null ? [] : [SEP, t('trying again in ', 'retry in '), n(s, ' s')]), ...(shown ? [SEP, t(`showing ${shown}`)] : [])],
    escalated: (shown: string) => [t(`Not updated since ${shown}`)],
    offline: (shown: string) => [t('Offline'), ...(shown ? [SEP, t(`showing ${shown}`)] : []), SEP, t('checks resume when you’re back', 'resumes when online')],
    tryAgain: 'Try again',
    tip: (last: string, next: string) => `Last check ${last}${next ? ` · next ${next}` : ''}`,
    sentence: {
      normal: (live: boolean, last: string, next: string) => `${live ? 'Live. ' : ''}Last check ${last}.${next ? ` Next check ${next}.` : ''}`,
      checking: (last: string) => `Checking the Desk now.${last ? ` Last check ${last}.` : ''}`,
      change: 'A change arrived. Updating the Desk now.',
      retrying: (last: string) => `Checking the Desk again.${last ? ` Last check ${last}.` : ''}`,
      quiet: (next: string, shown: string) => `Couldn’t check the Desk.${next ? ` Trying again at ${next}.` : ''}${shown ? ` Showing the list from ${shown}.` : ''}`,
      escalated: (shown: string) => `The Desk hasn’t updated since ${shown}. Try again is available.`,
      offline: (shown: string) => `Offline.${shown ? ` Showing the list from ${shown}.` : ''} Checks resume when you’re back.`,
    },
    announce: { quiet: 'Couldn’t check the Desk. Trying again.', escalated: (shown: string) => `The Desk hasn’t updated since ${shown}. Try again is available.`, recovered: 'The Desk is up to date again.', arrived: (count: number) => `${count} new on the Desk.` },
  },
  de: {
    live: 'Live',
    checked: (a: Ago) => a.kind === 'just' ? [t('gerade geprüft')] : a.kind === 'at' ? [t(`um ${a.clock} geprüft`)] : [t('vor '), n(a.n), t(a.kind === 's' ? ' s geprüft' : ' min geprüft')],
    next: (s: number) => [t('nächste Prüfung in ', 'nächste in '), n(s, ' s')],
    checking: 'Wird geprüft…', change: 'Änderung eingetroffen · wird aktualisiert…', retrying: 'Erneuter Versuch…',
    quiet: (s: number | null, shown: string) => [t('Prüfung fehlgeschlagen'), ...(s === null ? [] : [SEP, t('neuer Versuch in ', 'erneut in '), n(s, ' s')]), ...(shown ? [SEP, t(`Stand ${shown}`)] : [])],
    escalated: (shown: string) => [t(`Seit ${shown} nicht aktualisiert`)],
    offline: (shown: string) => [t('Offline'), ...(shown ? [SEP, t(`Stand ${shown}`)] : []), SEP, t('Prüfung startet wieder, sobald online', 'weiter, sobald online')],
    tryAgain: 'Erneut versuchen',
    tip: (last: string, next: string) => `Letzte Prüfung ${last}${next ? ` · nächste ${next}` : ''}`,
    sentence: {
      normal: (live: boolean, last: string, next: string) => `${live ? 'Live. ' : ''}Letzte Prüfung ${last}.${next ? ` Nächste Prüfung ${next}.` : ''}`,
      checking: (last: string) => `Der Desk wird geprüft.${last ? ` Letzte Prüfung ${last}.` : ''}`,
      change: 'Eine Änderung ist eingetroffen. Der Desk wird aktualisiert.',
      retrying: (last: string) => `Erneuter Versuch, den Desk zu prüfen.${last ? ` Letzte Prüfung ${last}.` : ''}`,
      quiet: (next: string, shown: string) => `Der Desk konnte nicht geprüft werden.${next ? ` Neuer Versuch um ${next}.` : ''}${shown ? ` Stand ${shown}.` : ''}`,
      escalated: (shown: string) => `Der Desk wurde seit ${shown} nicht aktualisiert. Erneut versuchen ist verfügbar.`,
      offline: (shown: string) => `Offline.${shown ? ` Stand ${shown}.` : ''} Die Prüfung startet wieder, sobald eine Verbindung besteht.`,
    },
    announce: { quiet: 'Der Desk konnte nicht geprüft werden. Neuer Versuch läuft.', escalated: (shown: string) => `Der Desk wurde seit ${shown} nicht aktualisiert. Erneut versuchen ist verfügbar.`, recovered: 'Der Desk ist wieder aktuell.', arrived: (count: number) => `${count} neu auf dem Desk.` },
  },
} as const
export const deskSyncCopy = (lang: DisplayLanguage = displayLanguage()) => DESK_SYNC_COPY[lang]

export function deskSyncHealth(input: Pick<DeskSyncInput, 'notice' | 'stale' | 'retry'>): DeskSyncHealth {
  // Kept rows after a missed desk read are a quiet failure while retries run, and escalate once they are spent.
  if (input.notice === 'escalated' || input.stale && input.retry === 'exhausted') return 'escalated'
  return input.notice === 'quiet' || input.stale ? 'quiet' : 'ok'
}
/** Offline explains everything else; an escalated outage keeps Try again in place through later reads. */
export function deskSyncState(input: DeskSyncInput): DeskSyncState {
  if (!input.online) return 'offline'
  const health = deskSyncHealth(input)
  if (health === 'escalated') return 'escalated'
  if (input.busy) return input.change ? 'change' : 'checking'
  if (health === 'quiet') return 'quiet'
  if (input.failures > 0) return 'retrying'
  if (input.checkedAt === null) return 'never'
  return input.now - input.checkedAt < JUST_MS ? 'just' : 'normal'
}
/** The next read: only while the page is visible and online, and only when a timer really schedules one. */
export function deskNextRead(input: Pick<DeskSyncInput, 'online' | 'visible' | 'pollAt' | 'retryAt'>): number | null {
  if (!input.online || !input.visible) return null
  const due = [input.pollAt, input.retryAt].filter((at): at is number => at !== null)
  return due.length ? Math.min(...due) : null
}
/** Reduced motion keeps the dot still and the refresh icon from turning. Capture sets reduce. */
export function deskSyncMotion(input: { reducedMotion: boolean; spin: boolean }): { spin: boolean; pulse: boolean } {
  if (input.reducedMotion) return { spin: false, pulse: false }
  return { spin: input.spin, pulse: true }
}
/** A scheduled poll reads again only after the clock moves. A frozen test clock keeps the countdown and does not start a check. */
export function deskClockMoved(last: number, now: number): boolean {
  return now !== last
}

export function deskSyncLine(input: DeskSyncInput, lang: DisplayLanguage = displayLanguage()): DeskSyncLine {
  const copy = DESK_SYNC_COPY[lang], state = deskSyncState(input), health = deskSyncHealth(input)
  const next = deskNextRead(input), wait = next === null ? null : secondsUntil(next, input.now)
  const last = input.checkedAt === null ? '' : deskClock(input.checkedAt, true), shown = input.checkedAt === null ? '' : deskClock(input.checkedAt)
  const nextClock = next === null ? '' : deskClock(next, true)
  const live = input.live && (state === 'normal' || state === 'just')
  const base = { state, health, warn: false, spin: false, live, retry: false, tip: last ? copy.tip(last, nextClock) : '' }
  switch (state) {
    case 'normal': case 'just':
      return { ...base, icon: live ? 'dot' : null, parts: [...(live ? [t(`${copy.live}\u00a0· `, '')] : []), ...copy.checked(ago(input.checkedAt!, input.now)), ...(wait === null ? [] : [SEP, ...copy.next(wait)])], sentence: copy.sentence.normal(live, last, nextClock) }
    case 'checking': case 'never':
      return { ...base, icon: 'refresh', spin: input.busy, parts: [t(copy.checking)], sentence: copy.sentence.checking(last) }
    case 'change':
      return { ...base, icon: 'refresh', spin: true, parts: [t(copy.change)], sentence: copy.sentence.change }
    case 'retrying':
      return { ...base, icon: 'refresh', parts: [t(copy.retrying)], sentence: copy.sentence.retrying(last) }
    case 'quiet':
      return { ...base, icon: 'alert', warn: true, parts: copy.quiet(wait, shown), sentence: copy.sentence.quiet(nextClock, shown) }
    case 'escalated':
      return { ...base, icon: 'alert', warn: true, retry: true, parts: copy.escalated(shown), sentence: copy.sentence.escalated(shown) }
    case 'offline':
      // Offline is the browser's state, not a Desk failure: muted words, the icon carries it.
      return { ...base, icon: 'cloud-off', parts: copy.offline(shown), sentence: copy.sentence.offline(shown) }
  }
}
/** The one sentence a health change announces, at most once per transition; null for none (§5). */
export function deskSyncAnnouncement(from: DeskSyncHealth, to: DeskSyncHealth, shown: string, lang: DisplayLanguage = displayLanguage()): string | null {
  if (from === to) return null
  const words = DESK_SYNC_COPY[lang].announce
  if (to === 'escalated') return words.escalated(shown)
  if (to === 'quiet') return from === 'ok' ? words.quiet : null
  return words.recovered
}
