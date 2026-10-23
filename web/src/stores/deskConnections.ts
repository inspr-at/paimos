// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useSession } from './session'
import { ConnectionEnded, readInstances, readSibling, type DeskSibling, type ElsewhereDiscovery, type ElsewhereSummary } from '../lib/deskElsewhere'
import { deviceSubscriptionHash, phoneSettings } from '../lib/phoneApprovals'

export type ConnectionState = 'not connected' | 'connecting' | 'reading' | 'ok' | 'unreachable' | 'ended'
export interface InstanceRead extends DeskSibling {
  state: ConnectionState; summary: ElsewhereSummary | null; account: string; connectedAt: string; expiresAt: string
  expanded: boolean; hiddenUntil: number; slots: number
}
interface BrowserGrant { token: string; origin: string; account: string; createdAt: string; expiresAt: string }
interface Pending { owner: string; tag: string; origin: string; until: number; returnTo: string }
const PREFIX = 'aeon.desk-connections.', PENDING = `${PREFIX}pending.`, NOTICES = 'aeon:desk-notifications'
const TOKEN = /^desk_[\da-f-]{36}_[\da-f-]{36}_[A-Za-z0-9_-]{43}$/i
const STATE = /^[A-Za-z0-9_-]{43}$/
const storageGet = (key: string) => { try { return localStorage.getItem(key) } catch { return null } }
const storagePut = (key: string, value: string | null) => { try { if (value === null) localStorage.removeItem(key); else localStorage.setItem(key, value) } catch { /* This tab still works without persistent storage. */ } }
function readJSON<T>(key: string): T | null { try { return JSON.parse(storageGet(key) ?? 'null') as T | null } catch { return null } }
const emptyRead = (s: DeskSibling): InstanceRead => ({ ...s, state: 'not connected', summary: null, account: '', connectedAt: '', expiresAt: '', expanded: false, hiddenUntil: 0, slots: 0 })

// One store per signed-in person, shared by every Desk surface. Raw credentials
// are private browser data: never returned by this store or written via api().
export const useDeskConnections = defineStore('deskConnections', () => {
  const session = useSession()
  const owner = computed(() => session.identity?.principal.kind === 'person' ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
  const here = ref(''), reads = ref<InstanceRead[]>([]), discovery = ref<ElsewhereDiscovery>('pending')
  const noticeEnabled = ref(false)
  let generation = 0, noticeTurn = 0, abort = new AbortController(), discoveryFlight: Promise<void> | undefined
  const grants = new Map<string, BrowserGrant>(), flights = new Map<string, Promise<void>>(), attempted = new Map<string, number>()
  const pending = new Map<string, string>()
  const key = (tag: string, suffix = '') => `${PREFIX}${owner.value}.${tag}${suffix}`
  const current = (who: string, turn: number) => who === owner.value && turn === generation && session.authenticationCurrent()
  function patch(tag: string, value: Partial<InstanceRead>) { reads.value = reads.value.map(row => row.tag === tag ? { ...row, ...value } : row) }
  function save(tag: string) { const grant = grants.get(tag); storagePut(key(tag), grant ? JSON.stringify(grant) : null) }
  function restore(row: InstanceRead) {
    const grant = readJSON<BrowserGrant>(key(row.tag))
    const prefs = readJSON<{ expanded?: boolean; hiddenUntil?: number; slots?: number; ended?: boolean }>(key(row.tag, '.view'))
    row.expanded = prefs?.expanded === true; row.hiddenUntil = Number(prefs?.hiddenUntil) || 0; row.slots = Math.min(5, Math.max(0, Number(prefs?.slots) || 0))
    if (!grant || grant.origin !== row.origin || !TOKEN.test(grant.token) || !Number.isFinite(Date.parse(grant.expiresAt))) { if (prefs?.ended) row.state = 'ended'; return }
    row.account = typeof grant.account === 'string' ? grant.account.slice(0, 120) : ''; row.connectedAt = grant.createdAt; row.expiresAt = grant.expiresAt
    if (Date.parse(grant.expiresAt) <= Date.now()) { row.state = 'ended'; row.slots = 0; storagePut(key(row.tag), null); storagePut(key(row.tag, '.view'), JSON.stringify({ ...prefs, ended: true, slots: 0 })); return }
    grants.set(row.tag, grant); row.state = 'reading'
  }
  function view(tag: string) {
    const row = reads.value.find(r => r.tag === tag)
    if (row) storagePut(key(tag, '.view'), JSON.stringify({ expanded: row.expanded, hiddenUntil: row.hiddenUntil, slots: row.slots, ended: row.state === 'ended' }))
  }
  function expand(tag: string) { const row = reads.value.find(r => r.tag === tag); if (row) { patch(tag, { expanded: !row.expanded }); view(tag) } }
  function dismiss(tag: string) { patch(tag, { hiddenUntil: Date.now() + 30 * 86400_000 }); view(tag) }
  function ended(tag: string) { grants.delete(tag); flights.delete(tag); save(tag); patch(tag, { state: 'ended', summary: null, slots: 0 }); view(tag) }

  async function notificationPreference() {
    const who = owner.value, turn = generation, notice = ++noticeTurn
    const valid = () => current(who, turn) && notice === noticeTurn
    if (!who) return
    noticeEnabled.value = false
    try {
      const hash = await deviceSubscriptionHash()
      if (!hash || !valid()) { if (valid()) noticeEnabled.value = false; return }
      const settings = await phoneSettings(abort.signal)
      if (valid()) noticeEnabled.value = settings.subscriptions.some(sub => sub.endpoint_hash === hash && sub.desk_enabled)
    } catch { if (valid()) noticeEnabled.value = false }
  }
  async function notify(tag: string, summary: ElsewhereSummary, baseline: boolean, who: string, turn: number, grant: BrowserGrant) {
    const run = async () => {
      if (!current(who, turn) || grants.get(tag) !== grant) return
      const seenKey = `${PREFIX}${who}.${tag}.seen`, saved = readJSON<unknown>(seenKey)
      const seen = new Set(Array.isArray(saved) ? saved.filter((value): value is string => typeof value === 'string').slice(0, 5000) : [])
      for (const line of summary.lines) {
        const id = `${line.kind}:${line.id}`
        if (seen.has(id) || seen.size >= 5000) continue
        seen.add(id); storagePut(seenKey, JSON.stringify([...seen]))
        if (baseline || !noticeEnabled.value || typeof Notification === 'undefined' || Notification.permission !== 'granted') continue
        try {
          const notice = new Notification(`PAIMOS · ${tag} · Decision waiting`, { body: line.title, tag: `desk-${tag}-${id}` })
          notice.onclick = () => { notice.close(); if (current(who, turn) && grants.get(tag) === grant) window.open(line.url, '_blank', 'noopener,noreferrer') }
        } catch {
          // iOS and some installed browsers require service-worker delivery.
          // The home URL stays in a fragment, never in a request to this server.
          try {
            const registration = await navigator.serviceWorker?.getRegistration('/')
            if (!current(who, turn) || grants.get(tag) !== grant) return
            const fragment = new URLSearchParams({ instance: tag, url: line.url, owner: who })
            await registration?.showNotification(`PAIMOS · ${tag} · Decision waiting`, { body: line.title, tag: `desk-${tag}-${id}`, data: { path: `/instances/open#${fragment}` } })
          } catch { /* A failed delivery never affects the in-app count. */ }
        }
      }
    }
    // Across tabs, a durable claim precedes delivery. Never send anything to a server.
    if (typeof navigator !== 'undefined' && navigator.locks) await navigator.locks.request(`${PREFIX}${who}.notify`, run)
    else run()
  }
  function readOne(tag: string, force = false): Promise<void> {
    if (flights.has(tag)) return flights.get(tag)!
    const row = reads.value.find(r => r.tag === tag), grant = grants.get(tag)
    if (!row || !grant || !owner.value || !session.authenticationCurrent()) return Promise.resolve()
    if (Date.parse(grant.expiresAt) <= Date.now()) { ended(tag); return Promise.resolve() }
    if (!force && Date.now() - (attempted.get(tag) ?? 0) < 25_000) return Promise.resolve()
    attempted.set(tag, Date.now())
    const who = owner.value, turn = generation, signal = abort.signal, baseline = !storageGet(key(tag, '.seen'))
    const valid = () => current(who, turn) && grants.get(tag) === grant && !signal.aborted
    if (!row.summary) patch(tag, { state: 'reading' })
    const flight = (async () => {
      try {
        const summary = await readSibling(row, grant.token, signal)
        if (!valid()) return
        if (Date.parse(grant.expiresAt) <= Date.now()) { ended(tag); return }
        patch(tag, { state: 'ok', summary, slots: summary.lines.length }); view(tag)
        await notify(tag, summary, baseline, who, turn, grant)
      } catch (error) {
        if (!valid()) return
        if (error instanceof ConnectionEnded) ended(tag)
        else patch(tag, { state: 'unreachable' }) // Retain the last answer, explicitly dated by the UI.
      } finally { if (flights.get(tag) === flight) flights.delete(tag) }
    })()
    flights.set(tag, flight); return flight
  }
  function discover(): Promise<void> {
    if (discoveryFlight) return discoveryFlight
    const who = owner.value, turn = generation
    if (!who || !session.authenticationCurrent()) return Promise.resolve()
    const flight = (async () => {
      try {
        const instances = await readInstances(abort.signal)
        if (!current(who, turn)) return
        here.value = instances.here
        reads.value = instances.siblings.map(s => { const row = emptyRead(s); restore(row); return row })
        discovery.value = 'known'
        await Promise.all(reads.value.map(row => readOne(row.tag)))
      } catch { if (current(who, turn)) discovery.value = 'unknown' }
      finally { if (discoveryFlight === flight) discoveryFlight = undefined }
    })()
    discoveryFlight = flight; return flight
  }
  async function refresh(force = false) {
    if (discovery.value !== 'known') return discover()
    for (const [tag, state] of pending) {
      const p = readJSON<Pending>(PENDING + state)
      if (!p || p.until <= Date.now()) cancel(tag)
    }
    await Promise.all(reads.value.map(row => readOne(row.tag, force)))
  }
  function connect(tag: string) {
    const row = reads.value.find(r => r.tag === tag)
    if (!row || !owner.value || !session.authenticationCurrent()) return
    cancel(tag)
    const bytes = crypto.getRandomValues(new Uint8Array(32))
    const state = btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
    const record: Pending = { owner: owner.value, tag, origin: row.origin, until: Date.now() + 10 * 60_000, returnTo: location.pathname + location.search }
    storagePut(PENDING + state, JSON.stringify(record))
    // Persistence is necessary for a separate top-level callback tab.
    if (!storageGet(PENDING + state)) { patch(tag, { state: 'unreachable' }); return }
    pending.set(tag, state); patch(tag, { state: 'connecting', hiddenUntil: 0 })
    const url = new URL('/instances/connect', row.origin); url.searchParams.set('origin', location.origin); url.searchParams.set('state', state)
    window.open(url.href, '_blank', 'noopener,noreferrer')
  }
  function cancel(tag: string) {
    const state = pending.get(tag)
    if (state) storagePut(PENDING + state, null)
    pending.delete(tag)
    const row = reads.value.find(r => r.tag === tag)
    if (row?.state === 'connecting') patch(tag, { state: grants.has(tag) ? (row.summary ? 'ok' : 'reading') : 'not connected' })
  }
  async function acceptFragment(fragment: string): Promise<string | null> {
    if (!owner.value || !session.authenticationCurrent() || fragment.length > 2048) return null
    const params = new URLSearchParams(fragment.replace(/^#/, '')), state = params.get('state') ?? ''
    if (!STATE.test(state)) return null
    const accept = async () => {
      const who = owner.value, turn = generation
      const record = readJSON<Pending>(PENDING + state)
      storagePut(PENDING + state, null) // Single use, including refused callbacks.
      if (!record || record.owner !== owner.value || record.until < Date.now()) return null
      if (discovery.value !== 'known') await discover()
      if (!current(who, turn)) return null
      const row = reads.value.find(r => r.tag === record.tag && r.origin === record.origin)
      const token = params.get('token') ?? '', expiresAt = params.get('expires_at') ?? '', createdAt = params.get('created_at') ?? ''
      if (!row || params.get('instance') !== row.tag || !TOKEN.test(token) || !Number.isFinite(Date.parse(expiresAt)) || Date.parse(expiresAt) <= Date.now() || Date.parse(expiresAt) > Date.now() + 91 * 86400_000 || !Number.isFinite(Date.parse(createdAt))) return null
      const grant: BrowserGrant = { token, origin: row.origin, expiresAt, createdAt, account: (params.get('account') ?? '').slice(0, 120) }
      flights.delete(row.tag); attempted.delete(row.tag)
      grants.set(row.tag, grant); save(row.tag); pending.delete(row.tag)
      patch(row.tag, { state: 'reading', account: grant.account, connectedAt: createdAt, expiresAt, hiddenUntil: 0, summary: null })
      view(row.tag)
      await readOne(row.tag, true)
      if (!current(who, turn)) return null
      return record.returnTo.startsWith('/') && !record.returnTo.startsWith('//') && !record.returnTo.startsWith('/\\') ? record.returnTo : '/decision-desk'
    }
    if (typeof navigator !== 'undefined' && navigator.locks) return navigator.locks.request(`${PREFIX}connect.${state}`, accept)
    return accept()
  }
  function disconnect(tag: string): (() => void) | undefined {
    const row = reads.value.find(r => r.tag === tag), grant = grants.get(tag), who = owner.value, turn = generation
    if (!row || !grant) return
    cancel(tag); grants.delete(tag); flights.delete(tag); attempted.delete(tag); save(tag)
    patch(tag, { ...emptyRead(row), expanded: row.expanded })
    view(tag)
    return () => {
      if (!current(who, turn) || grants.has(tag) || Date.parse(grant.expiresAt) <= Date.now()) return
      grants.set(tag, grant); save(tag); patch(tag, { state: 'reading', account: grant.account, expiresAt: grant.expiresAt, connectedAt: grant.createdAt })
      void readOne(tag, true)
    }
  }
  function reset() {
    for (const state of pending.values()) storagePut(PENDING + state, null)
    generation++; noticeTurn++; abort.abort(); abort = new AbortController()
    grants.clear(); flights.clear(); attempted.clear(); pending.clear(); discoveryFlight = undefined
    here.value = ''; reads.value = []; discovery.value = owner.value ? 'pending' : 'known'; noticeEnabled.value = false
  }
  watch(owner, () => { reset(); void notificationPreference(); void discover() }, { immediate: true, flush: 'sync' })
  const focus = async () => { await notificationPreference(); void refresh(true) }
  const storage = (event: StorageEvent) => {
    if (event.key?.startsWith(PREFIX) && owner.value) {
      // Callback/disconnect in another tab: restore only this person's configured rows.
      for (const row of reads.value) {
        if (event.key === key(row.tag)) {
          grants.delete(row.tag); flights.delete(row.tag); attempted.delete(row.tag)
          const next = emptyRead(row); restore(next); patch(row.tag, next)
          void readOne(row.tag, true)
        }
      }
    }
  }
  if (typeof window !== 'undefined') { window.addEventListener('focus', focus); window.addEventListener('storage', storage); window.addEventListener(NOTICES, notificationPreference) }
  onScopeDispose(() => { reset(); if (typeof window !== 'undefined') { window.removeEventListener('focus', focus); window.removeEventListener('storage', storage); window.removeEventListener(NOTICES, notificationPreference) } })
  const count = computed(() => reads.value.reduce((n, r) => n + (r.summary?.open ?? 0), 0))
  const complete = computed(() => discovery.value === 'known' && reads.value.every(r => r.state === 'ok'))
  const allClear = computed(() => complete.value && count.value === 0)
  const breakdown = computed(() => reads.value.map(r => r.state === 'ok' ? `${r.summary?.open ?? 0} on ${r.tag}` : `${r.tag}: ${r.state}${r.summary ? ` (as of ${new Date(r.summary.asOf).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })})` : ''}`))
  return { here, reads, discovery, count, complete, allClear, breakdown, noticeEnabled, refresh, connect, cancel, acceptFragment, disconnect, expand, dismiss }
})
