// SPDX-License-Identifier: AGPL-3.0-only
import { onReset } from './position.ts'
import { watchStreamHealth } from './streamHealth.ts'
import { usePoller } from './usePolledData.ts'

export const RECONCILE_MS = 120_000
export const REFRESH_BATCH_MS = 400
const ACCESS_EVENTS = ['authz.workspace_role_changed', 'authz.binding_reassigned', 'authz.role_updated', 'authz.role_deleted', 'binding.set', 'binding.removed', 'principal.deactivated']
type HintListener = (type?: string, data?: unknown) => void
interface Subscriber { events: ReadonlySet<string>; changed: HintListener; connection: (live: boolean) => void; recover: () => void }
const subscribers = new Set<Subscriber>()
let source: EventSource | undefined
let health: ReturnType<typeof watchStreamHealth> | undefined
let connected = false
const listening = new Set<string>()

function listen(name: string) {
  if (!source || listening.has(name)) return
  listening.add(name)
  const current = source
  current.addEventListener(name, event => {
    if (source !== current) return
    health?.heard()
    for (const subscriber of subscribers) if (subscriber.events.has(name)) subscriber.changed(name, (event as MessageEvent).data)
  })
}
function close() {
  source?.close(); source = undefined; connected = false; listening.clear()
  health?.stop(); health = undefined
}
function connect() {
  if (!subscribers.size || typeof EventSource === 'undefined') return
  const current = source = new EventSource('/api/events/stream?after=latest')
  health = watchStreamHealth(() => {
    close()
    for (const subscriber of subscribers) { subscriber.connection(false); subscriber.recover() }
    connect()
  })
  current.onopen = () => {
    if (source !== current) return
    health?.heard(); connected = true
    for (const subscriber of subscribers) { subscriber.connection(true); subscriber.changed() }
  }
  current.onerror = () => {
    if (source !== current) return
    connected = false
    for (const subscriber of subscribers) subscriber.connection(false)
  }
  for (const name of ['stream.ping', 'stream.ready']) current.addEventListener(name, () => { if (source === current) health?.heard() })
  for (const subscriber of subscribers) for (const name of subscriber.events) listen(name)
}
// The stream carries hints only. Every subscriber rereads its authorized API;
// it never adopts resource snapshots from the stream. All mounted consumers
// share this transport, including the existing agents subscriptions.
export function subscribeBrowserEvents(events: readonly string[], changed: HintListener, connection: (live: boolean) => void = () => {}, recover: () => void = () => changed()) {
  if (typeof EventSource === 'undefined') return () => {}
  const subscriber: Subscriber = { events: new Set(events), changed, connection, recover }
  subscribers.add(subscriber)
  if (!source) connect()
  else {
    for (const name of events) listen(name)
    const current = source
    if (connected) queueMicrotask(() => { if (source === current && connected && subscribers.has(subscriber)) { connection(true); changed() } })
  }
  return () => { subscribers.delete(subscriber); if (!subscribers.size) close() }
}
// A transport opened with the previous viewer's cookies cannot be reused after
// an identity/access reset. Deferred reopening lets the synchronous reset finish.
onReset(() => { close(); queueMicrotask(() => { if (!source) connect() }) })

/** One refresh lifecycle per resource in its owning store, however many views mount it. */
export function createSharedRequests(read: (key: string) => Promise<unknown> | void, options: {
  events: readonly string[]
  relevant?: (key: string, type: string, data: unknown) => boolean
  invalidate: (key: string) => void
  enabled?: () => boolean
  period?: number
  immediate?: boolean
}) {
  const entries = new Map<string, { users: number; poller: ReturnType<typeof usePoller>; timer?: ReturnType<typeof setTimeout> }>()
  let stopEvents: (() => void) | undefined
  function changed(type?: string, data?: unknown) {
    for (const [key, entry] of entries) {
      if (type && !ACCESS_EVENTS.includes(type) && options.relevant && !options.relevant(key, type, data)) continue
      options.invalidate(key)
      // Opening/recovering a transport fences the initial read too. Reconcile
      // immediately so that read cannot leave the first snapshot behind a timer.
      if (!type) {
        clearTimeout(entry.timer); entry.timer = undefined
        entry.poller.tick(true)
        continue
      }
      // A fixed window cannot be postponed indefinitely by a busy stream.
      entry.timer ??= setTimeout(() => { entry.timer = undefined; entry.poller.tick(true) }, REFRESH_BATCH_MS)
    }
  }
  function watch(key: string) {
    if (!key) return () => {}
    let entry = entries.get(key)
    if (!entry) {
      const poller = usePoller(() => read(key), options.period ?? RECONCILE_MS, { enabled: options.enabled, invalidate: () => options.invalidate(key) })
      entry = { users: 0, poller }
      entries.set(key, entry)
      stopEvents ??= subscribeBrowserEvents([...options.events, ...ACCESS_EVENTS], changed, live => { if (!live) changed() })
      poller.start(options.immediate ?? true)
    }
    entry.users++
    let stopped = false
    return () => {
      if (stopped) return
      stopped = true
      if (entries.get(key) !== entry || --entry.users) return
      clearTimeout(entry.timer); entry.poller.stop(); entries.delete(key)
      if (!entries.size) { stopEvents?.(); stopEvents = undefined }
    }
  }
  function stop() {
    for (const entry of entries.values()) { clearTimeout(entry.timer); entry.poller.stop() }
    entries.clear(); stopEvents?.(); stopEvents = undefined
  }
  return { watch, stop }
}
