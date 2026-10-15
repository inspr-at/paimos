// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useSession } from './session'
import { onAccessRevalidate } from '../lib/authz'
import { loadDeskProjection, type DeskProjection } from '../lib/decisionDesk'
import { getDoctrineInbox } from '../lib/doctrine'
import { readDoctrineProjects } from '../lib/decisionDeskApi'

// The badge uses the server's total, never session presence or a loaded page.
export const useDecisionDesk = defineStore('decisionDesk', () => {
  const session = useSession()
  const owner = computed(() => session.identity?.principal.kind === 'person' ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
  const projection = ref<DeskProjection | null>(null), error = ref(''), loading = ref(false)
  let generation = 0, controller: AbortController | undefined, flight: Promise<void> | undefined
  // A rule change names no project; its ticket does. One cache for the Agents panel and the
  // header preview, so the preview opens with what the panel already read (AEON-1339).
  const ruleProjects = ref(new Map<string, string>()), ruleProjectError = ref('')
  let ruleTurn = 0, ruleFlight: { page: DeskProjection; done: Promise<void> } | undefined
  function invalidate() {
    generation++; controller?.abort(); controller = undefined; flight = undefined
    projection.value = null; error.value = ''; loading.value = false
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
  function refresh(): Promise<void> {
    if (!owner.value) return Promise.resolve()
    if (flight) return flight
    const identity = owner.value, turn = generation
    const abort = new AbortController(); controller = abort; loading.value = true
    const current = () => turn === generation && identity === owner.value && session.authenticationCurrent()
    flight = (async () => {
      try {
        const page = await loadDeskProjection(abort.signal)
        if (current()) { projection.value = page; error.value = '' }
      } catch (cause) {
        if (current()) { projection.value = null; error.value = cause instanceof Error ? cause.message : 'Decision Desk could not be read. Retry.' }
      } finally { if (current()) { loading.value = false; flight = undefined; controller = undefined } }
    })()
    return flight
  }
  watch(owner, () => { invalidate(); void refresh() }, { immediate: true, flush: 'sync' })
  const stopAccess = onAccessRevalidate({ reset: invalidate, refresh: () => { void refresh() } })
  onScopeDispose(() => { stopAccess(); invalidate() })
  return { projection, error, loading, refresh, invalidate, ruleProjects, ruleProjectError, resolveRuleProjects, count: computed(() => projection.value?.counts.open ?? null) }
})
