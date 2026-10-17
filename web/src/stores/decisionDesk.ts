// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useSession } from './session'
import { onAccessRevalidate } from '../lib/authz'
import { deskReadAborted, loadDeskProjection, type DeskProjection } from '../lib/decisionDesk'
import { onProjectionWake } from '../lib/agents'
import { getDoctrineInbox } from '../lib/doctrine'
import { readDoctrineProjects } from '../lib/decisionDeskApi'

// The badge uses the server's total, never session presence or a loaded page.
export const useDecisionDesk = defineStore('decisionDesk', () => {
  const session = useSession()
  const owner = computed(() => session.identity?.principal.kind === 'person' ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
  const projection = ref<DeskProjection | null>(null), error = ref(''), loading = ref(false)
  const failures = ref(0), failedSince = ref<number | null>(null), refreshedAt = ref<number | null>(null)
  let generation = 0, controller: AbortController | undefined, flight: Promise<boolean> | undefined
  let wakeTimer: ReturnType<typeof setTimeout> | undefined
  // A rule change names no project; its ticket does. One cache for the Agents panel and the
  // header preview, so the preview opens with what the panel already read (AEON-1339).
  const ruleProjects = ref(new Map<string, string>()), ruleProjectError = ref('')
  let ruleTurn = 0, ruleFlight: { page: DeskProjection; done: Promise<void> } | undefined
  // Waking a suspended poll cancels its read but keeps the last good state.
  // Identity/permission resets still clear everything synchronously below.
  function cancelRefresh() {
    generation++; controller?.abort(); controller = undefined; flight = undefined
    clearTimeout(wakeTimer); wakeTimer = undefined; loading.value = false
  }
  function invalidate() {
    cancelRefresh()
    projection.value = null; error.value = ''; failures.value = 0; failedSince.value = null; refreshedAt.value = null
    ruleTurn++; ruleFlight = undefined; ruleProjects.value = new Map(); ruleProjectError.value = ''
  }
  function resolveRuleProjects(): Promise<void> {
    const page = projection.value, identity = owner.value
    if (!page || !identity) return Promise.resolve()
    if (ruleFlight?.page === page) return ruleFlight.done
    const ids = new Set(page.items.filter(item => item.kind === 'doctrine' && !item.project_id && !ruleProjects.value.has(item.id)).map(item => item.id))
    if (!ids.size) return Promise.resolve()
    const turn = ++ruleTurn
    ruleProjectError.value = ''
    // Polls replace the projection; a late read for an older one or another person changes nothing.
    const current = () => turn === ruleTurn && projection.value === page && identity === owner.value && session.authenticationCurrent()
    const done = (async () => {
      try {
        const inbox = await getDoctrineInbox()
        if (!current()) return
        const rules = inbox.items.filter(rule => ids.has(rule.id))
        const tickets = await readDoctrineProjects(rules)
        if (!current()) return
        const resolved = new Map(ruleProjects.value)
        for (const rule of rules) {
          const project = rule.ticket ? tickets.get(rule.ticket) : undefined
          if (project) resolved.set(rule.id, project)
          else if (!rule.ticket) resolved.set(rule.id, '') // A workspace rule is resolved without a project.
        }
        ruleProjects.value = resolved
      } catch {
        if (current()) ruleProjectError.value = 'The projects of some rule changes could not be read.'
      } finally { if (turn === ruleTurn) ruleFlight = undefined }
    })()
    ruleFlight = { page, done }
    return done
  }
  function refresh(): Promise<boolean> {
    if (!owner.value || !session.authenticationCurrent()) return Promise.resolve(false)
    if (flight) return flight
    const identity = owner.value, turn = generation
    const abort = new AbortController(); controller = abort; loading.value = true
    const current = () => turn === generation && identity === owner.value && session.authenticationCurrent()
    flight = (async () => {
      try {
        const page = await loadDeskProjection(abort.signal)
        if (!current() || abort.signal.aborted) return false
        projection.value = page; error.value = ''; failures.value = 0; failedSince.value = null; refreshedAt.value = Date.now()
        return true
      } catch (cause) {
        if (current() && !deskReadAborted(cause, abort.signal)) {
          failures.value++; failedSince.value ??= Date.now()
          // Previews and the first load retain their existing error treatment;
          // a refresh never replaces good data with an error or an empty count.
          if (!projection.value) error.value = cause instanceof Error ? cause.message : 'Decision Desk could not be read. Retry.'
        }
        return false
      } finally { if (current()) { loading.value = false; flight = undefined; controller = undefined } }
    })()
    return flight
  }
  // One one-second event window, then at most one trailing read if a request
  // is already running. Twenty live events cannot create twenty refetches.
  function requestRefresh() {
    if (wakeTimer || !owner.value || !session.authenticationCurrent()) return
    const turn = generation
    wakeTimer = setTimeout(() => {
      wakeTimer = undefined
      const read = () => { if (turn === generation) void refresh() }
      if (flight) void flight.then(read)
      else read()
    }, 1000)
  }
  watch(owner, () => { invalidate(); void refresh() }, { immediate: true, flush: 'sync' })
  const stopAccess = onAccessRevalidate({ reset: invalidate, refresh: () => { void refresh() } })
  const stopWakes = onProjectionWake(requestRefresh)
  onScopeDispose(() => { stopWakes(); stopAccess(); invalidate() })
  return { projection, error, loading, failures, failedSince, refreshedAt, refresh, cancelRefresh, invalidate, ruleProjects, ruleProjectError, resolveRuleProjects, count: computed(() => projection.value?.counts.open ?? null) }
})
