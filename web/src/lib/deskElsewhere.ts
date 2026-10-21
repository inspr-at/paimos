// SPDX-License-Identifier: AGPL-3.0-only
// Waiting on you across instances (AEON-1495, owner only). This instance names
// its sibling instances to the workspace owner; the owner's browser then reads
// each sibling's owner summary with the owner's own session there (a cookie the
// sibling issued while the owner used it). Nothing read from a sibling is sent
// to this instance or stored: it lives in this tab's memory until the identity
// changes. A sibling that cannot be read is one quiet line, never an error page.
import { onScopeDispose, ref, watch } from 'vue'
import { api } from './api'
import { useSession } from '../stores/session'

export interface DeskSibling { tag: string; origin: string }
export interface DeskInstances { here: string; siblings: DeskSibling[] }
export interface ElsewhereLine { id: string; projectKey: string; title: string; askedBy: string; held: boolean; url: string }
export interface ElsewhereSummary { lines: ElsewhereLine[]; open: number; held: number; upcoming: string }
export interface ElsewhereRead extends DeskSibling { state: 'reading' | 'ok' | 'error'; summary: ElsewhereSummary | null; shown: number }
export type ElsewhereDiscovery = 'pending' | 'known' | 'unknown'

const TAG = /^[a-z][a-z0-9-]{0,15}$/
const SUMMARY_LINES = 5, TITLE_MAX = 200, NAME_MAX = 120, READ_TIMEOUT = 8_000, SIBLING_BODY_MAX = 64_000
export const ELSEWHERE_REFRESH = 60_000
// Lines one sibling may occupy in the band. The band keeps this many slots when a read fails.
export const ELSEWHERE_LINES = 2

const text = (value: unknown, max: number) => typeof value === 'string' ? value.trim().slice(0, max) : ''
function origin(value: unknown): string {
  if (typeof value !== 'string') return ''
  try { const url = new URL(value); return url.origin === value && (url.protocol === 'https:' || url.hostname === 'localhost' || url.hostname === '127.0.0.1') ? value : '' } catch { return '' }
}

/** This instance's tag and, for the owner, its siblings. Also refreshes (or clears) the owner's summary cookie here. */
export async function readInstances(signal?: AbortSignal): Promise<DeskInstances> {
  const response = await api('/decision-desk/instances', { signal })
  if (!response.ok) throw new Error('Instances could not be read.')
  const body = await response.json().catch(() => null) as { here?: unknown; siblings?: unknown } | null
  const here = text(body?.here, 16)
  const siblings = (Array.isArray(body?.siblings) ? body.siblings : []).slice(0, 4).flatMap((row: { tag?: unknown; origin?: unknown }) => {
    const tag = text(row?.tag, 16), at = origin(row?.origin)
    return TAG.test(tag) && at && tag !== here ? [{ tag, origin: at }] : []
  })
  return { here: TAG.test(here) ? here : '', siblings }
}

/** A sibling's answer, checked strictly: first lines only, links only into that sibling. */
export function parseSummary(sibling: DeskSibling, body: unknown): ElsewhereSummary | null {
  const raw = body as { instance?: unknown; items?: unknown; counts?: { open?: unknown; held?: unknown }; upcoming?: { label?: unknown } } | null
  if (!raw || raw.instance !== sibling.tag || !Array.isArray(raw.items)) return null
  const open = raw.counts?.open, held = raw.counts?.held
  if (!Number.isInteger(open) || !Number.isInteger(held) || (open as number) < 0 || (held as number) < 0) return null
  const lines = raw.items.slice(0, SUMMARY_LINES).flatMap((item: Record<string, unknown>) => {
    const id = text(item?.id, 64), title = text(item?.title, TITLE_MAX), href = typeof item?.href === 'string' ? item.href : ''
    if (!id || !title || !href.startsWith('/') || href.startsWith('//')) return []
    return [{ id, projectKey: text(item.project_key, 40), title, askedBy: text(item.asked_by, NAME_MAX), held: item.held === true, url: new URL(href, sibling.origin).href }]
  })
  return { lines, open: open as number, held: held as number, upcoming: text(raw.upcoming?.label, TITLE_MAX) }
}

// Count the bytes actually read. A missing or smaller Content-Length must not admit the rest of the body.
async function boundedJSON(response: Response, limit: number): Promise<unknown> {
  const declared = Number(response.headers.get('content-length'))
  if (Number.isFinite(declared) && declared > limit) {
    await response.body?.cancel()
    throw new Error('too much')
  }
  if (!response.body) throw new Error('empty body')
  const reader = response.body.getReader(), chunks: Uint8Array[] = []
  let size = 0
  try {
    for (;;) {
      const chunk = await reader.read()
      if (chunk.done) break
      size += chunk.value.byteLength
      if (size > limit) { await reader.cancel(); throw new Error('too much') }
      chunks.push(chunk.value)
    }
  } finally { reader.releaseLock() }
  const bytes = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength }
  return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes))
}

/** One sibling read with the owner's session there. No retry loop: Try again is the person's. */
export async function readSibling(sibling: DeskSibling, signal?: AbortSignal): Promise<ElsewhereSummary> {
  const timeout = AbortSignal.timeout(READ_TIMEOUT)
  const response = await fetch(`${sibling.origin}/api/decision-desk/owner-summary`, {
    credentials: 'include', mode: 'cors', cache: 'no-store', redirect: 'error', referrerPolicy: 'no-referrer',
    headers: { Accept: 'application/json' }, signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  })
  if (!response.ok) throw new Error(`${sibling.tag} answered ${response.status}`)
  let body: unknown
  try { body = await boundedJSON(response, SIBLING_BODY_MAX) }
  catch (error) {
    if (error instanceof Error && error.message === 'too much') throw new Error(`${sibling.tag} answered too much`)
    throw new Error(`${sibling.tag} answered an unknown shape`)
  }
  const summary = parseSummary(sibling, body)
  if (!summary) throw new Error(`${sibling.tag} answered an unknown shape`)
  return summary
}

/** The person's sibling reads, reset on every identity change; stale answers are dropped. */
export function useDeskElsewhere(options: { refreshEvery?: number } = {}) {
  const session = useSession()
  const here = ref(''), reads = ref<ElsewhereRead[]>([]), settled = ref(false), discovery = ref<ElsewhereDiscovery>('pending')
  let turn = 0, discovering = false, abort: AbortController | undefined, timer: ReturnType<typeof setInterval> | undefined
  const who = () => session.identity?.principal.kind === 'person' ? `${session.identity.tenant.id}:${session.identity.principal.id}` : ''
  const shownLines = (summary: ElsewhereSummary) => Math.min(ELSEWHERE_LINES, summary.lines.length)
  function reset() {
    turn++; abort?.abort(); abort = undefined; discovering = false
    here.value = ''; reads.value = []; settled.value = false; discovery.value = 'pending'
  }
  async function readOne(sibling: DeskSibling, at: number, signal: AbortSignal) {
    const current = () => at === turn && !signal.aborted
    try {
      const summary = await readSibling(sibling, signal)
      if (current()) reads.value = reads.value.map(read => read.tag === sibling.tag ? { ...read, state: 'ok', summary, shown: shownLines(summary) } : read)
    } catch {
      // Keep shown: the band holds the slots the last successful read occupied.
      if (current()) reads.value = reads.value.map(read => read.tag === sibling.tag ? { ...read, state: 'error', summary: null } : read)
    }
  }
  async function readBatch(siblings: DeskSibling[], at: number, signal: AbortSignal): Promise<ElsewhereRead[] | undefined> {
    const next: ElsewhereRead[] = siblings.map(sibling => ({ ...sibling, state: 'reading', summary: null, shown: 1 }))
    await Promise.all(next.map(async row => {
      try {
        const summary = await readSibling(row, signal)
        if (at !== turn || signal.aborted) return
        row.state = 'ok'; row.summary = summary; row.shown = shownLines(summary)
      } catch {
        if (at !== turn || signal.aborted) return
        row.state = 'error'; row.summary = null
      }
    }))
    if (at !== turn || signal.aborted) return undefined
    return next
  }
  async function load() {
    const identity = who()
    reset()
    if (!identity) { discovery.value = 'known'; settled.value = true; return }
    const at = turn, controller = new AbortController(); abort = controller
    discovering = true
    try {
      const instances = await readInstances(controller.signal)
      if (at !== turn || identity !== who()) return
      here.value = instances.here
      const next = await readBatch(instances.siblings, at, controller.signal)
      if (!next || at !== turn || identity !== who()) return
      reads.value = next
      discovery.value = 'known'
    } catch {
      // The list itself failed. That is unknown coverage, not a confirmed empty sibling list.
      if (at === turn) { discovery.value = 'unknown'; reads.value = [] }
    } finally { if (at === turn) { discovering = false; settled.value = true } }
  }
  async function retryDiscovery() {
    const identity = who(), at = turn, signal = abort?.signal
    if (!identity || !signal || signal.aborted || discovering || discovery.value === 'known') return
    discovering = true
    try {
      const instances = await readInstances(signal)
      if (at !== turn || identity !== who() || signal.aborted) return
      here.value = instances.here
      const next = await readBatch(instances.siblings, at, signal)
      if (!next || at !== turn || identity !== who()) return
      reads.value = next
      discovery.value = 'known'
    } catch {
      if (at === turn && identity === who()) discovery.value = 'unknown'
    } finally { if (at === turn) discovering = false }
  }
  /** Re-read every sibling (the periodic refresh) without blanking lines that are shown. A failed list is read again. */
  function refreshSiblings() {
    const at = turn, signal = abort?.signal
    if (!signal || signal.aborted || !settled.value) return
    if (discovery.value !== 'known') { void retryDiscovery(); return }
    for (const read of reads.value) void readOne(read, at, signal)
  }
  /** Try again for one sibling: its line reads again in place. */
  function retry(tag: string) {
    const read = reads.value.find(row => row.tag === tag), signal = abort?.signal
    if (!read || !signal) return
    reads.value = reads.value.map(row => row.tag === tag ? { ...row, state: 'reading' } : row)
    void readOne(read, turn, signal)
  }
  watch(who, () => { void load() }, { immediate: true })
  const every = options.refreshEvery ?? ELSEWHERE_REFRESH
  if (every > 0 && typeof window !== 'undefined') timer = setInterval(() => { if (document.visibilityState !== 'hidden') refreshSiblings() }, every)
  onScopeDispose(() => { clearInterval(timer); turn++; abort?.abort() })
  return { here, reads, settled, discovery, retry, refresh: refreshSiblings }
}
