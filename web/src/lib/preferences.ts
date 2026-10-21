// SPDX-License-Identifier: AGPL-3.0-only
// Per-person server preferences. Every read, debounce and write tail belongs to
// one tenant/principal epoch, including across same-document sign-in changes.
import { computed, reactive, ref, shallowRef, type Ref } from 'vue'
import { api } from './api.ts'

type Json = Record<string, unknown>
type Owner = { tenant: { id: string }; principal: { id: string } } | null
interface Preference {
  key: string; owner: string; epoch: number; value: Ref<Json | null>; ready: Promise<void>
  loaded: boolean; reading?: Promise<boolean>
  timer?: ReturnType<typeof setTimeout>; tail?: Promise<boolean>
  // The promise for a delay-0 save that has not reached the network yet, and the
  // resolvers for a save a newer one replaced or a sign-out cancelled.
  soon?: Promise<boolean>; settle?: (ok: boolean | Promise<boolean>) => void; delaySettle?: (ok: boolean | Promise<boolean>) => void
  // The object a keepalive write already handed to fetch. pagehide must not send it again.
  inflightBody?: Json; tailKeepalive?: boolean
}
const owner = shallowRef('')
// Read-only ownership fence for caches that follow the same authentication epoch.
export const preferenceOwner = computed(() => owner.value)
let epoch = 0
// Advanced whenever the owner changes, including a sign-out and back in as the same person.
// A write captures it and drops the result when a later resume belongs to another epoch.
export function preferenceGeneration(): number { return epoch }
let authenticationCurrent = () => true
const cache = new Map<string, Preference>()
const flights = new Set<AbortController>()
const failures = new Set<(key: string) => void>()
// Writes in flight, and the saved preferences whose last write failed, for the footer on Settings
// (AEON-785). Ids carry the owner. Only a preference this module holds can be saved again, with the
// value it holds now; other callers (developer settings, say) report their own failure where they ask.
export const preferenceSaves = reactive({ saving: new Set<string>(), failed: new Set<string>() })
const inflight = new Map<string, number>()
export function onPreferenceFailure(listener: (key: string) => void) { failures.add(listener); return () => { failures.delete(listener) } }

// The session store invokes this synchronously, before a cookie-changing action
// and whenever its tenant/person changes. Old refs and jobs cannot revive later.
export function setPreferenceOwner(who: Owner, current = () => true) {
  authenticationCurrent = current
  const next = who ? `${who.tenant.id}/${who.principal.id}` : ''
  if (next === owner.value) return
  epoch++
  for (const pref of cache.values()) {
    pref.value.value = null
    if (pref.timer !== undefined) clearTimeout(pref.timer)
    pref.timer = undefined
    pref.delaySettle?.(false); pref.delaySettle = undefined
    pref.settle?.(false); pref.settle = undefined; pref.soon = undefined
  }
  for (const flight of flights) flight.abort()
  flights.clear(); cache.clear(); owner.value = next
  preferenceSaves.saving.clear(); preferenceSaves.failed.clear(); inflight.clear()
}
function capture() {
  const person = owner.value, started = epoch
  return () => !!person && person === owner.value && started === epoch && authenticationCurrent()
}
async function readPreferenceResult(key: string): Promise<{ ok: boolean; value: Json | null }> {
  const current = capture(), controller = new AbortController()
  if (!current()) return { ok: false, value: null }
  flights.add(controller)
  try {
    const response = await api(`/preferences/${encodeURIComponent(key)}`, { signal: controller.signal })
    if (!response.ok || !current()) return { ok: false, value: null }
    const body = await response.json()
    if (!current()) return { ok: false, value: null }
    return { ok: true, value: body?.value && typeof body.value === 'object' && !Array.isArray(body.value) ? body.value : null }
  } catch { return { ok: false, value: null } }
  finally { flights.delete(controller) }
}
export async function readPreference(key: string): Promise<Json | null> {
  return (await readPreferenceResult(key)).value
}
export async function writePreference(key: string, value: Json, keepalive = false): Promise<boolean> {
  const current = capture(), controller = keepalive ? undefined : new AbortController()
  if (!current()) return false
  const id = `${owner.value}/${key}`
  inflight.set(id, (inflight.get(id) ?? 0) + 1)
  preferenceSaves.saving.add(id)
  if (controller) flights.add(controller)
  try {
    // A leaving write has no abort signal: AbortSignal.timeout(0) aborts at once,
    // and a timeout would cancel the keepalive request during unload.
    const response = await api(`/preferences/${encodeURIComponent(key)}`, {
      method: 'PUT', ...(controller ? { signal: controller.signal } : {}), ...(keepalive ? { keepalive: true } : {}),
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value }),
    }, keepalive ? 0 : undefined)
    if (!current()) return false
    if (!response.ok) failures.forEach(listener => listener(key))
    return response.ok
  } catch { if (current()) failures.forEach(listener => listener(key)); return false }
  finally {
    if (controller) flights.delete(controller)
    const left = (inflight.get(id) ?? 1) - 1
    if (left > 0) inflight.set(id, left)
    else { inflight.delete(id); preferenceSaves.saving.delete(id) }
  }
}
const owned = (pref: Preference) => pref.owner === owner.value && pref.epoch === epoch && !!pref.owner && authenticationCurrent()
function loadPreference(pref: Preference): Promise<boolean> {
  if (pref.reading) return pref.reading
  const read = readPreferenceResult(pref.key).then(result => {
    if (!owned(pref) || !result.ok) return false
    pref.loaded = true
    if (pref.value.value === null) pref.value.value = result.value
    return true
  }).finally(() => { if (pref.reading === read) pref.reading = undefined })
  pref.reading = read
  return read
}
function preference(key: string): Preference | null {
  if (!owner.value) return null
  const scopedKey = `${owner.value}/${key}`
  const existing = cache.get(scopedKey)
  if (existing) return existing
  const pref: Preference = { key, owner: owner.value, epoch, value: ref(null), ready: Promise.resolve(), loaded: false }
  cache.set(scopedKey, pref)
  pref.ready = loadPreference(pref).then(() => undefined)
  return pref
}
// A slow move must finish before its Undo is sent. The tail checks ownership
// when it actually starts, not only when it is enqueued.
// keepalive is the delay-0 path: the request has to survive a reload. The body
// is marked only once that job actually starts, so a same-turn sign-out still
// cancels it, and a later pagehide can see the value already on the wire.
function persist(pref: Preference, keepalive = false): Promise<boolean> {
  const job = (pref.tail ?? Promise.resolve(false)).catch(() => false).then(async (): Promise<boolean> => {
    if (!owned(pref)) return false
    const value = pref.value.value
    if (!value) return false
    if (keepalive) { pref.inflightBody = value; pref.tailKeepalive = true }
    else pref.tailKeepalive = false
    const saved = await writePreference(pref.key, value, keepalive)
    if (pref.tail === job && pref.inflightBody === value) pref.inflightBody = undefined
    if (owned(pref)) { if (saved) preferenceSaves.failed.delete(`${pref.owner}/${pref.key}`); else preferenceSaves.failed.add(`${pref.owner}/${pref.key}`) }
    return saved && owned(pref)
  }).finally(() => { if (pref.tail === job) { pref.tail = undefined; pref.tailKeepalive = false } })
  pref.tail = job
  return job
}
// A delay-0 save leaves on a microtask, with keepalive. A channel macrotask was
// still queued when the goals spec reloaded, and pagehide's fetch is not what
// the next document reads. Same-turn saves share `soon` until that microtask.
function writeSoon(pref: Preference): Promise<boolean> {
  if (pref.soon) return pref.soon
  pref.soon = new Promise(resolve => {
    pref.settle = resolve
    queueMicrotask(() => {
      if (pref.settle !== resolve) return
      pref.soon = undefined; pref.settle = undefined
      if (!owned(pref) || pref.timer !== undefined) { resolve(false); return }
      resolve(persist(pref, true))
    })
  })
  return pref.soon
}
export function usePreference<T extends object>(key: string) {
  // Existing consumers follow the new owner with an empty value and a new read;
  // every queued callback retains its original Preference object and epoch.
  const value = computed<T | null>(() => preference(key)?.value.value as T | null ?? null)
  // Merging shared fields requires a successful read, including an empty value.
  // A failed read can be retried; concurrent callers share the same owned flight.
  async function ensureLoaded(): Promise<boolean> {
    const pref = preference(key)
    if (!pref) return false
    await pref.ready
    if (!owned(pref)) return false
    return pref.loaded || loadPreference(pref)
  }
  function save(next: T, delay = 400): Promise<boolean> {
    const pref = preference(key)
    if (!pref || !owned(pref)) return Promise.resolve(false)
    ensureLeaveHook()
    pref.value.value = next as Json
    if (pref.timer !== undefined) clearTimeout(pref.timer)
    pref.timer = undefined
    if (pref.delaySettle) { pref.delaySettle(false); pref.delaySettle = undefined }
    if (delay <= 0) return writeSoon(pref)
    return new Promise(resolve => {
      pref.delaySettle = resolve
      pref.timer = setTimeout(() => {
        pref.timer = undefined; pref.delaySettle = undefined
        if (!owned(pref)) { resolve(false); return }
        resolve(persist(pref))
      }, delay)
    })
  }
  // True once the value in hand was read from the server or a write of it was accepted.
  // A write still queued or in flight is awaited; a failed write is false.
  function done(): Promise<boolean> {
    const pref = preference(key)
    if (!pref || !owned(pref)) return Promise.resolve(false)
    if (pref.soon) return pref.soon
    if (pref.tail) return pref.tail
    const id = `${pref.owner}/${pref.key}`
    if (preferenceSaves.failed.has(id) || preferenceSaves.saving.has(id)) return Promise.resolve(false)
    return Promise.resolve(pref.loaded)
  }
  return { value, get ready() { return preference(key)?.ready ?? Promise.resolve() }, ensureLoaded, save, done }
}
// Registered on the first save so a test that stubs window is seen then, not at import.
// Node tests have no window; the hook stays off.
let leaveHooked = false
function ensureLeaveHook() {
  if (leaveHooked || typeof window === 'undefined') return
  leaveHooked = true
  window.addEventListener('pagehide', flushLeaving)
}
// A debounced save that has not reached its timer still leaves with the page.
// A delay-0 value already on a keepalive wire is left there: sending it again
// races the copy the next document will read.
function flushLeaving() {
  for (const pref of cache.values()) {
    if (!owned(pref) || !pref.value.value) continue
    const waiting = pref.timer !== undefined || pref.soon !== undefined
    if (!waiting && !pref.tail) continue
    if (!waiting && pref.tailKeepalive && pref.inflightBody === pref.value.value) continue
    if (pref.timer !== undefined) clearTimeout(pref.timer)
    pref.timer = undefined
    const delaySettle = pref.delaySettle
    pref.delaySettle = undefined
    const settle = pref.settle
    pref.soon = undefined
    pref.settle = undefined
    const value = pref.value.value
    const request = writePreference(pref.key, value, true)
    const tracked = request.then(saved => {
      if (owned(pref)) {
        const id = `${pref.owner}/${pref.key}`
        if (saved) preferenceSaves.failed.delete(id)
        else preferenceSaves.failed.add(id)
      }
      return saved && owned(pref)
    }).finally(() => { if (pref.tail === tracked) { pref.tail = undefined; pref.tailKeepalive = false } })
    pref.tail = tracked
    pref.tailKeepalive = true
    pref.inflightBody = value
    settle?.(tracked)
    delaySettle?.(tracked)
  }
}
// Saves again what a preference holds now, where its last write failed.
export function retryFailedPreferences() {
  for (const pref of cache.values()) if (owned(pref) && preferenceSaves.failed.has(`${pref.owner}/${pref.key}`)) persist(pref)
}
