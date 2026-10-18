// SPDX-License-Identifier: AGPL-3.0-only
// The footer's centre (AEON-785, design AEON-721): one state dot and one line
// about the screen in front of you. Each screen publishes { tone, full, short,
// aria, action } or nothing through the shared `footerSummary`; nothing means an
// empty centre. Free of components so the rules can be unit tested.
import { computed, onScopeDispose, shallowRef, watch } from 'vue'

// Problem red is for agent problems and failures only; attention amber for what
// needs a person; deliberate teal for a wind-down; calm and idle are quiet.
export type FooterTone = 'problem' | 'attention' | 'deliberate' | 'calm' | 'idle'
// The count carries the line; the exception takes its tone's colour.
export interface FooterPart { text: string; as?: 'count' | 'exception' }
export interface FooterSummary {
  tone: FooterTone
  // Count noun · state · the one exception: at most three parts, one line.
  full: readonly FooterPart[]
  // Narrower footers and phones.
  short: readonly FooterPart[]
  // The whole sentence, ending in what a click does. A screen reader reads this.
  aria: string
  // The obvious thing; without one the summary is plain text.
  action?: () => void
  // A skeleton bar of fixed width: nothing to say yet, no ping.
  loading?: boolean
  // A line that appears and disappears (Saving, Not saved). It must not change who the neighbours are.
  transient?: boolean
  // Phone release layout of the summary underneath a transient line. True while
  // that summary stays (needs you); false when the centre was empty.
  phone?: boolean
}
// When the screen's data last landed, and whether it has stopped arriving.
export interface FooterLive { updatedAt: number | null; paused: boolean }

export const count = (value: number | string): FooterPart => ({ text: typeof value === 'number' ? value.toLocaleString('en-GB') : value, as: 'count' })
export const said = (text: string): FooterPart => ({ text })
export const exception = (text: string): FooterPart => ({ text, as: 'exception' })
export const plain = (parts: readonly FooterPart[]) => parts.map(part => part.text).join('')
// Phones hide the release time only for a summary that stays. A transient line
// keeps the release where that staying summary left it: pending needs keep the
// version hidden, and an empty centre keeps it shown.
export function phoneRoom(summary: FooterSummary | null) {
  if (!summary) return false
  if (summary.transient) return summary.phone === true
  return true
}
export const noun = (n: number, one: string, many = `${one}s`) => n === 1 ? one : many

export const LOADING: FooterSummary = { tone: 'idle', full: [], short: [], aria: 'Loading', loading: true }
export const PAUSED_FULL = 'Updates paused · retrying'
export const PAUSED_SHORT = 'Paused'

// ---------- Who speaks ----------
// A sheet over a page speaks while it is open (the last to register), and the
// page speaks again when the sheet goes.
interface Publication { summary: FooterSummary | null; live: FooterLive | null }
const publications = shallowRef(new Map<symbol, Publication>())
const speaker = computed(() => {
  let top: [symbol, Publication] | null = null
  for (const entry of publications.value) top = entry
  return top
})
export const footerSummary = computed(() => speaker.value?.[1].summary ?? null)
export const footerLive = computed(() => speaker.value?.[1].live ?? null)
// Changes when another screen takes over, so one screen's timestamps never read as another's fresh data.
export const footerSource = computed(() => speaker.value?.[0] ?? null)

function publish(owner: symbol, summary: FooterSummary | null, live: FooterLive | null) {
  const next = new Map(publications.value)
  next.set(owner, { summary, live })
  publications.value = next
}
function retract(owner: symbol) {
  if (!publications.value.has(owner)) return
  const next = new Map(publications.value)
  next.delete(owner)
  publications.value = next
}

// Call from a screen's setup. The getters are reactive; the screen's summary
// follows them and leaves with the screen, never taking another's place.
export function useFooterSummary(summary: () => FooterSummary | null, live?: () => FooterLive | null) {
  const owner = Symbol('footer-summary')
  // Only the getters are tracked: publishing reads the shared map and must not subscribe to it.
  watch(() => [summary(), live?.() ?? null] as const, ([said, ticking]) => publish(owner, said, ticking), { immediate: true, flush: 'sync' })
  onScopeDispose(() => retract(owner))
}

// ---------- The ping ----------
// One ring each time fresh data lands, never more than one per `gap` ms.
export function pingGate(gap = 2000, clock: () => number = Date.now) {
  let last = Number.NEGATIVE_INFINITY
  return () => {
    const now = clock()
    if (now - last < gap) return false
    last = now
    return true
  }
}
// Fresh data is a later update time on the same screen; the first reading and a
// screen change are not news.
export function landed(previous: { source: symbol | null; at: number | null } | null, next: { source: symbol | null; at: number | null }) {
  return !!previous && previous.source === next.source && previous.at !== null && next.at !== null && next.at > previous.at
}
