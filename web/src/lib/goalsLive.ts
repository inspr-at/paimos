// SPDX-License-Identifier: AGPL-3.0-only
// The Goals view's live line (AEON-1494): the tenant event stream in live mode
// (/api/events/stream?after=latest) says when a goal, an item or the workspace
// status note changed, so the view reads its list again at once instead of at
// the next minute. Events carry no values the view shows; the list read stays
// the one source of what is on screen. The state feeds the footer's freshness
// stamp: Live, or Reconnecting after the stream dropped.
import { watchStreamHealth } from './streamHealth'

export type GoalsLiveState = 'off' | 'connecting' | 'live' | 'reconnecting'
export const GOAL_STREAM_EVENTS = ['goal.created', 'goal.updated', 'goal.items_changed', 'goal.eta_changed', 'goal.passed_changed', 'goal.status_note_changed'] as const

export interface GoalsLiveSource {
  readyState?: number
  onerror: ((event: Event) => unknown) | null
  addEventListener(type: string, listener: (event: MessageEvent) => void): void
  close(): void
}
export interface GoalsLiveOptions {
  // A goal event arrived, or the stream came back after a gap: the list may be stale.
  changed: () => void
  state: (state: GoalsLiveState) => void
  // Null when this environment has no EventSource: the line then stays off.
  open?: (url: string) => GoalsLiveSource | null
  retryMs?: readonly number[]
  url?: string
}
const CLOSED = 2

export function openGoalsLive(options: GoalsLiveOptions): { close(): void } {
  const open = options.open ?? (url => typeof EventSource === 'undefined' ? null : new EventSource(url) as unknown as GoalsLiveSource)
  const retryMs = options.retryMs ?? [2_000, 5_000, 15_000, 30_000]
  let source: GoalsLiveSource | null = null
  let last: number | null = null, connected = false, retries = 0, closed = false
  let retry: ReturnType<typeof setTimeout> | undefined
  let current: GoalsLiveState = 'off'
  const set = (next: GoalsLiveState) => { if (current !== next) { current = next; options.state(next) } }
  // Display sleep can stop a stream without an error: start again from now and re-read.
  let health: ReturnType<typeof watchStreamHealth> | undefined
  const recover = () => {
    if (closed || !connected) return
    source?.close(); source = null; last = null
    set('reconnecting'); options.changed(); connect()
  }
  function connect() {
    clearTimeout(retry); retry = undefined
    let opened: GoalsLiveSource | null
    try { opened = open(`${options.url ?? '/api/events/stream'}?after=${last === null ? 'latest' : last}`) } catch { schedule(); return }
    if (!opened) { set('off'); return }
    const stream = source = opened
    health ??= watchStreamHealth(recover)
    set(connected ? 'reconnecting' : 'connecting')
    const mine = (run: (event: MessageEvent) => void) => (event: MessageEvent) => { if (source !== stream || closed) return; health?.heard(); run(event) }
    stream.addEventListener('stream.ready', mine(event => {
      let data: { after?: unknown; resumed?: unknown }
      try { data = JSON.parse(event.data) } catch { return }
      if (typeof data.after !== 'number') return
      // The first connection starts with the list read the view already makes; a
      // later one re-reads unless the server resumed exactly where it stopped.
      const resumed = data.resumed === true && last !== null && data.after >= last
      const first = !connected
      last = data.after; connected = true; retries = 0
      set('live')
      if (!first && !resumed) options.changed()
    }))
    stream.addEventListener('stream.ping', mine(() => {}))
    for (const type of GOAL_STREAM_EVENTS) stream.addEventListener(type, mine(event => {
      const id = Number(event.lastEventId)
      if (Number.isFinite(id) && id > (last ?? 0)) last = id
      options.changed()
    }))
    stream.onerror = () => {
      if (source !== stream || closed) return
      if (stream.readyState === CLOSED) { stream.close(); source = null; schedule() } else set('reconnecting')
    }
  }
  function schedule() {
    set('reconnecting')
    const wait = retryMs[Math.min(retries, retryMs.length - 1)]
    retries++
    retry = setTimeout(() => { retry = undefined; if (!closed && !source) connect() }, wait)
  }
  connect()
  return {
    close() {
      closed = true; clearTimeout(retry); health?.stop()
      source?.close(); source = null
      set('off')
    },
  }
}
