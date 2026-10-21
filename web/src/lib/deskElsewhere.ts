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
export interface ElsewhereRead extends DeskSibling { state: 'reading' | 'ok' | 'error'; summary: ElsewhereSummary | null }

const TAG = /^[a-z][a-z0-9-]{0,15}$/
const SUMMARY_LINES = 5, TITLE_MAX = 200, NAME_MAX = 120, READ_TIMEOUT = 8_000
export const ELSEWHERE_REFRESH = 60_000

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

/** One sibling read with the owner's session there. No retry loop: Try again is the person's. */
export async function readSibling(sibling: DeskSibling, signal?: AbortSignal): Promise<ElsewhereSummary> {
  const timeout = AbortSignal.timeout(READ_TIMEOUT)
  const response = await fetch(`${sibling.origin}/api/decision-desk/owner-summary`, {
    credentials: 'include', mode: 'cors', cache: 'no-store', redirect: 'error', referrerPolicy: 'no-referrer',
    headers: { Accept: 'application/json' }, signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  })
  if (!response.ok) throw new Error(`${sibling.tag} answered ${response.status}`)
  const length = Number(response.headers.get('content-length') ?? 0)
  if (length > 64_000) throw new Error(`${sibling.tag} answered too much`)
  const summary = parseSummary(sibling, await response.json())
  if (!summary) throw new Error(`${sibling.tag} answered an unknown shape`)
  return summary
}

/** The person's sibling reads, reset on every identity change; stale answers are dropped. */
export function useDeskElsewhere(options: { refreshEvery?: number } = {}) {
  const session = useSession()
  const here = ref(''), reads = ref<ElsewhereRead[]>([]), settled = ref(false)
  let turn = 0, abort: AbortController | undefined, timer: ReturnType<typeof setInterval> | undefined
  const who = () => session.identity?.principal.kind === 'person' ? `${session.identity.tenant.id}:${session.identity.principal.id}` : ''
  function reset() { turn++; abort?.abort(); abort = undefined; here.value = ''; reads.value = []; settled.value = false }
  async function readOne(sibling: DeskSibling, at: number, signal: AbortSignal) {
    const current = () => at === turn && !signal.aborted
    const update = (patch: Partial<ElsewhereRead>) => { reads.value = reads.value.map(read => read.tag === sibling.tag ? { ...read, ...patch } : read) }
    try { const summary = await readSibling(sibling, signal); if (current()) update({ state: 'ok', summary }) }
    catch { if (current()) update({ state: 'error', summary: null }) }
  }
  async function load() {
    const identity = who()
    reset()
    if (!identity) { settled.value = true; return }
    const at = turn, controller = new AbortController(); abort = controller
    try {
      const instances = await readInstances(controller.signal)
      if (at !== turn || identity !== who()) return
      here.value = instances.here
      reads.value = instances.siblings.map(sibling => ({ ...sibling, state: 'reading', summary: null }))
      await Promise.all(instances.siblings.map(sibling => readOne(sibling, at, controller.signal)))
    } catch { /* Without the list there is nothing across instances to show; this instance's band stands alone. */ }
    finally { if (at === turn) settled.value = true }
  }
  /** Re-read every sibling (the periodic refresh) without blanking lines that are shown. */
  function refreshSiblings() {
    const at = turn, signal = abort?.signal
    if (!signal || !settled.value) return
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
  return { here, reads, settled, retry, refresh: refreshSiblings }
}
