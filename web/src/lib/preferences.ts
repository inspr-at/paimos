// SPDX-License-Identifier: AGPL-3.0-only
// Per-person server preferences. Every read, debounce and write tail belongs to
// one tenant/principal epoch, including across same-document sign-in changes.
import { computed, reactive, ref, shallowRef, type Ref } from 'vue'
import { api } from './api.ts'

type Json = Record<string, unknown>
type Owner = { tenant: { id: string }; principal: { id: string } } | null
interface Preference {
  key: string; owner: string; epoch: number; value: Ref<Json | null>; ready: Promise<void>
  timer?: ReturnType<typeof setTimeout>; channel?: MessageChannel; tail?: Promise<void>
}
const owner = shallowRef('')
let epoch = 0
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
    pref.channel?.port1.close(); pref.channel?.port2.close()
  }
  for (const flight of flights) flight.abort()
  flights.clear(); cache.clear(); owner.value = next
  preferenceSaves.saving.clear(); preferenceSaves.failed.clear(); inflight.clear()
}
function capture() {
  const person = owner.value, started = epoch
  return () => !!person && person === owner.value && started === epoch && authenticationCurrent()
}
export async function readPreference(key: string): Promise<Json | null> {
  const current = capture(), controller = new AbortController()
  if (!current()) return null
  flights.add(controller)
  try {
    const response = await api(`/preferences/${encodeURIComponent(key)}`, { signal: controller.signal })
    if (!response.ok || !current()) return null
    const body = await response.json()
    return current() && body?.value && typeof body.value === 'object' && !Array.isArray(body.value) ? body.value : null
  } catch { return null }
  finally { flights.delete(controller) }
}
export async function writePreference(key: string, value: Json): Promise<boolean> {
  const current = capture(), controller = new AbortController()
  if (!current()) return false
  const id = `${owner.value}/${key}`
  inflight.set(id, (inflight.get(id) ?? 0) + 1)
  preferenceSaves.saving.add(id)
  flights.add(controller)
  try {
    const response = await api(`/preferences/${encodeURIComponent(key)}`, { method: 'PUT', signal: controller.signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value }) })
    if (!current()) return false
    if (!response.ok) failures.forEach(listener => listener(key))
    return response.ok
  } catch { if (current()) failures.forEach(listener => listener(key)); return false }
  finally {
    flights.delete(controller)
    const left = (inflight.get(id) ?? 1) - 1
    if (left > 0) inflight.set(id, left)
    else { inflight.delete(id); preferenceSaves.saving.delete(id) }
  }
}
const owned = (pref: Preference) => pref.owner === owner.value && pref.epoch === epoch && !!pref.owner && authenticationCurrent()
function preference(key: string): Preference | null {
  if (!owner.value) return null
  const scopedKey = `${owner.value}/${key}`
  const existing = cache.get(scopedKey)
  if (existing) return existing
  const pref: Preference = { key, owner: owner.value, epoch, value: ref(null), ready: Promise.resolve() }
  cache.set(scopedKey, pref)
  pref.ready = readPreference(key).then(stored => { if (owned(pref) && stored && pref.value.value === null) pref.value.value = stored })
  return pref
}
// A slow move must finish before its Undo is sent. The tail checks ownership
// when it actually starts, not only when it is enqueued.
function persist(pref: Preference) {
  const job = (pref.tail ?? Promise.resolve()).catch(() => undefined).then(async () => {
    if (!owned(pref)) return
    const value = pref.value.value
    if (!value) return
    const saved = await writePreference(pref.key, value)
    if (owned(pref)) { if (saved) preferenceSaves.failed.delete(`${pref.owner}/${pref.key}`); else preferenceSaves.failed.add(`${pref.owner}/${pref.key}`) }
  }).finally(() => { if (pref.tail === job) pref.tail = undefined })
  pref.tail = job
}
function writeSoon(pref: Preference) {
  if (pref.channel) return
  const channel = pref.channel = new MessageChannel()
  channel.port1.onmessage = () => {
    channel.port1.close(); channel.port2.close(); pref.channel = undefined
    if (owned(pref) && pref.timer === undefined) persist(pref)
  }
  ;(channel.port1 as MessagePort & { unref?: () => void }).unref?.()
  ;(channel.port2 as MessagePort & { unref?: () => void }).unref?.()
  channel.port2.postMessage(undefined)
}
export function usePreference<T extends object>(key: string) {
  // Existing consumers follow the new owner with an empty value and a new read;
  // every queued callback retains its original Preference object and epoch.
  const value = computed<T | null>(() => preference(key)?.value.value as T | null ?? null)
  function save(next: T, delay = 400) {
    const pref = preference(key)
    if (!pref || !owned(pref)) return
    pref.value.value = next as Json
    if (pref.timer !== undefined) clearTimeout(pref.timer)
    pref.timer = undefined
    if (delay <= 0) { writeSoon(pref); return }
    pref.timer = setTimeout(() => { pref.timer = undefined; if (owned(pref)) persist(pref) }, delay)
  }
  return { value, get ready() { return preference(key)?.ready ?? Promise.resolve() }, save }
}
// Saves again what a preference holds now, where its last write failed.
export function retryFailedPreferences() {
  for (const pref of cache.values()) if (owned(pref) && preferenceSaves.failed.has(`${pref.owner}/${pref.key}`)) persist(pref)
}
