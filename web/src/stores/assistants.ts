// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { APIError, pauseRecurrence } from '../lib/api'
import { can, ensurePermissions, onAccessRevalidate, permissionsKnown } from '../lib/authz'
import { assistantCopy } from '../lib/assistantsCopy'
import { assistantCapabilities, assistantDeskLink, assistantPolicyEvents, assistantRequest, disconnectAssistant, listAssistants, policyChange, saveAssistantPolicy, unavailableAssistantFacts, type Assistant, type AssistantEvent, type AssistantPolicy, type AssistantSnapshot, type AssistantRoutine } from '../lib/assistants'
import { presentAssistantRoutine, readAssistantRoutines, type OwnedAssistantRoutine } from '../lib/assistantRoutines'
import { useSession } from './session'
import { useProjects } from './projects'
import { useDecisionDesk } from './decisionDesk'
import { deskItemID } from '../lib/decisionDesk'
import { subscribeBrowserEvents } from '../lib/sharedRequests'

export const useAssistants = defineStore('assistants', () => {
  const session = useSession()
  const projects = useProjects()
  const desk = useDecisionDesk()
  const owner = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
  const items = ref<Assistant[]>([]), state = ref<'loading' | 'ready' | 'error' | 'forbidden' | 'unavailable'>('loading')
  const error = ref(''), truncated = ref(false)
  const events = ref<AssistantEvent[]>([]), historyState = ref<'idle' | 'loading' | 'ready' | 'error' | 'forbidden'>('idle')
  const historyTruncated = ref(false), historyError = ref('')
  const busy = ref<Record<string, boolean>>({})
  const ownedRoutines = ref<OwnedAssistantRoutine[]>([]), routinesReady = ref(false), routinesTruncated = ref(false), routinesError = ref('')
  let routineRead = 0, routineStarted = false, routineController: AbortController | undefined, routineFlight: Promise<void> | undefined
  let generation = 0, read = 0, historyRead = 0, controller: AbortController | undefined, historyController: AbortController | undefined
  let flight: Promise<void> | undefined
  const facts = computed(() => Object.fromEntries(items.value.map(item => {
    const own = ownedRoutines.value.filter(row => row.owner === item.principal_id)
    const policyActivity = events.value.filter(event => event.after?.principal_id === item.principal_id && ['assistant.policy_evaluated', 'assistant.delegated'].includes(event.type)).map(event => ({ at: event.at, text: policyChange(event) }))
    const latest = [...own.flatMap(row => row.lastActivity ?? []), ...policyActivity].sort((a, b) => Date.parse(b.at) - Date.parse(a.at))[0] ?? null
    // Reuse the Desk's bounded, permission-checked live projection. Names are
    // presentation only: ownership always comes from the asker's principal id.
    const page = session.identity?.principal.kind === 'person' && mayRead.value ? desk.projection : null
    const requests = page?.items.filter(row => ['approval', 'question', 'action_request'].includes(row.kind) && row.asked_by?.id === item.principal_id)
    const approvals = requests ? requests.map(row => ({
      id: deskItemID(row), title: row.title, target: row.ticket_key || projects.byId(row.project_id ?? '')?.routeKey || row.project_id || assistantCopy().workspace,
      at: row.created_at, href: `${assistantDeskLink(item.principal_id)}&item=${encodeURIComponent(deskItemID(row))}`,
    })) : null
    return [item.principal_id, { ...unavailableAssistantFacts(), approvals,
      approvalsCanDecide: requests ? requests.some(row => row.can_decide === true) : undefined,
      approvalsTruncated: !!page && (page.has_more || page.items.length !== page.counts.open || desk.failures > 0),
      approvalsError: desk.failures ? assistantCopy().approvalsRefreshFailed : desk.error,
      routines: routinesReady.value ? own.map(row => row.view) : null, routinesTruncated: routinesTruncated.value || !!routinesError.value, routinesError: routinesError.value, activity: routinesReady.value ? own.flatMap(row => row.activity ?? []) : null, activityIncomplete: true, lastActivity: latest }]
  })))
  const mayConnect = computed(() => session.identity?.principal.kind === 'person' && can('agents.create') && can('keys.manage'))
  const mayRead = computed(() => session.identity?.principal.kind === 'person' ? can('members.read') : session.identity?.principal.kind === 'agent' && can('inbox.read'))
  function mayEdit(item: Assistant) { return mayConnect.value && session.identity?.principal.id === item.owner_person_id }
  function reset() {
    generation++; read++; historyRead++; controller?.abort(); historyController?.abort(); flight = undefined
    routineRead++; routineController?.abort(); routineFlight = undefined; routineStarted = false; ownedRoutines.value = []; routinesReady.value = false; routinesTruncated.value = false; routinesError.value = ''
    items.value = []; events.value = []; busy.value = {}; state.value = 'loading'; historyState.value = 'idle'
    error.value = ''; historyError.value = ''; truncated.value = false; historyTruncated.value = false
  }
  watch(owner, reset, { flush: 'sync' })
  const stopAccess = onAccessRevalidate({ reset, refresh: () => { void load() } })
  onScopeDispose(() => { reset(); stopAccess() })

  function adopt(next: Assistant) {
    const index = items.value.findIndex(item => item.principal_id === next.principal_id)
    if (index < 0) items.value.push(next)
    else if (next.revision >= items.value[index]!.revision) items.value[index] = next
  }
  function invalidate() { read++; controller?.abort(); flight = undefined }
  function load(): Promise<void> {
    if (!owner.value || !permissionsKnown()) return Promise.resolve()
    if (flight) return flight
    if (!mayRead.value) {
      items.value = []; state.value = 'forbidden'
      return Promise.resolve()
    }
    const turn = ++read, who = owner.value, epoch = generation
    controller?.abort(); controller = new AbortController()
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(30_000)])
    const request = (async () => {
      try {
        const identity = session.identity!
        const body = identity.principal.kind === 'agent' ? await (async () => {
          // The self-policy route establishes the assistant class. Its display
          // name comes from /me; vendor remains explicitly unreported.
          const snapshot = await assistantRequest<AssistantSnapshot>(`/assistants/${encodeURIComponent(identity.principal.id)}/policy`, 'GET', undefined, signal)
          return { items: [{ ...snapshot, kind: 'assistant', principal_kind: 'agent', display_name: identity.principal.name, vendor: 'unknown', messaging_only: true } as Assistant], truncated: false }
        })() : await listAssistants(signal)
        if (turn !== read || who !== owner.value || epoch !== generation) return
        const current = new Map(items.value.map(item => [item.principal_id, item]))
        // A poll begun before a successful policy write cannot rewind that row.
        items.value = body.items.map(item => (current.get(item.principal_id)?.revision ?? -1) > item.revision ? current.get(item.principal_id)! : item)
        truncated.value = body.truncated; state.value = 'ready'; error.value = ''
        if (assistantCapabilities.routines && !routineStarted && identity.principal.kind === 'person') void loadRoutines()
        if (historyState.value === 'idle' && identity.principal.kind === 'person') void loadHistory()
      } catch (failure) {
        if (turn !== read || who !== owner.value || epoch !== generation) return
        state.value = failure instanceof APIError && failure.status === 403 ? 'forbidden' : failure instanceof APIError && failure.status === 404 ? 'unavailable' : 'error'
        error.value = failure instanceof Error ? failure.message : assistantCopy().loadFailed
        if (state.value === 'forbidden') items.value = []
      }
    })().finally(() => { if (flight === request) flight = undefined })
    flight = request
    return request
  }
  async function save(item: Assistant, policy: AssistantPolicy): Promise<boolean> {
    const id = item.principal_id, who = owner.value, epoch = generation, revision = item.revision
    if (!mayEdit(item) || busy.value[id]) return false
    busy.value[id] = true
    try {
      const result = await saveAssistantPolicy(id, revision, policy)
      if (who !== owner.value || epoch !== generation) return false
      if (result.principal_id !== id) throw new Error(assistantCopy().invalidResponse)
      if ((items.value.find(row => row.principal_id === id)?.revision ?? 0) > result.revision) return false
      invalidate(); adopt({ ...item, ...result })
      await loadHistory()
      return true
    } finally { if (who === owner.value && epoch === generation) busy.value[id] = false }
  }
  async function disconnect(item: Assistant): Promise<boolean> {
    const id = item.principal_id, who = owner.value, epoch = generation
    if (!mayEdit(item) || busy.value[id]) return false
    busy.value[id] = true
    try {
      const result = await disconnectAssistant(id)
      if (who !== owner.value || epoch !== generation) return false
      if (result.principal_id !== id) throw new Error(assistantCopy().invalidResponse)
      if ((items.value.find(row => row.principal_id === id)?.revision ?? 0) > result.revision) return false
      invalidate(); adopt(result); await loadHistory()
      return true
    } finally { if (who === owner.value && epoch === generation) busy.value[id] = false }
  }
  async function loadHistory() {
    const turn = ++historyRead, who = owner.value, epoch = generation
    historyController?.abort()
    if (!can('events.read') || session.identity?.principal.kind !== 'person') { historyState.value = 'forbidden'; events.value = []; return }
    historyState.value = 'loading'; historyController = new AbortController()
    try {
      const body = await assistantPolicyEvents(Date.now(), historyController.signal)
      if (turn !== historyRead || who !== owner.value || epoch !== generation) return
      events.value = body.items; historyTruncated.value = body.truncated; historyState.value = 'ready'; historyError.value = ''
    } catch (failure) {
      if (turn !== historyRead || who !== owner.value || epoch !== generation) return
      historyState.value = 'error'; historyError.value = failure instanceof Error ? failure.message : assistantCopy().historyUnavailable
    }
  }
  function loadRoutines(): Promise<void> {
    if (!owner.value || !mayRead.value || session.identity?.principal.kind !== 'person' || !items.value.length) return Promise.resolve()
    if (routineFlight) return routineFlight
    routineStarted = true
    const turn = ++routineRead, who = owner.value, epoch = generation
    routineController?.abort(); routineController = new AbortController()
    const signal = AbortSignal.any([routineController.signal, AbortSignal.timeout(30_000)])
    const request = (async () => {
      try {
        await projects.load()
        const result = await readAssistantRoutines(new Set(items.value.map(item => item.principal_id)), id => projects.byId(id)?.routeKey, Date.now(), signal)
        const projectIDs = [...new Set(result.rows.map(row => row.wire.project_id))]
        let cursor = 0
        await Promise.all(Array.from({ length: Math.min(6, projectIDs.length) }, async () => { while (cursor < projectIDs.length) await ensurePermissions(projectIDs[cursor++]!) }))
        if (turn !== routineRead || who !== owner.value || epoch !== generation) return
        const known = new Map(ownedRoutines.value.map(row => [row.wire.id, row]))
        ownedRoutines.value = result.rows.map(row => (known.get(row.wire.id)?.wire.revision ?? 0) > row.wire.revision ? known.get(row.wire.id)! : row)
        routinesReady.value = true; routinesTruncated.value = result.truncated; routinesError.value = ''
      } catch (failure) {
        if (turn !== routineRead || who !== owner.value || epoch !== generation) return
        routineController?.abort()
        routinesError.value = failure instanceof Error ? failure.message : assistantCopy().routinesUnavailable
      }
    })().finally(() => { if (routineFlight === request) routineFlight = undefined })
    routineFlight = request
    return request
  }
  function watchPage() {
    let timer: ReturnType<typeof setTimeout> | undefined, stopped = false, identityChanged = false, routinesChanged = false, approvalsChanged = false, pendingGeneration = generation
    // Hints only. Batch named events, then read the authorized APIs; routine
    // discovery never runs from the page's 20-second identity poll.
    const stop = subscribeBrowserEvents([
      'assistant.created', 'assistant.updated', 'assistant.policy_updated', 'assistant.disabled',
      'assistant.policy_evaluated', 'assistant.delegated',
      'recurrence.created', 'recurrence.updated', 'recurrence.deleted', 'recurrence.paused', 'recurrence.resumed',
      'recurrence.occurred', 'recurrence.skipped', 'recurrence.failed', 'recurrence.agent_result',
      'approval.proposed', 'approval.approved', 'approval.denied', 'approval.revoked',
    ], event => {
      if (pendingGeneration !== generation) {
        clearTimeout(timer); timer = undefined; identityChanged = routinesChanged = approvalsChanged = false; pendingGeneration = generation
      }
      if (event?.startsWith('assistant.')) identityChanged = true
      if (event?.startsWith('approval.')) approvalsChanged = true
      else routinesChanged = true
      const epoch = generation
      timer ??= setTimeout(async () => {
        timer = undefined
        const refreshIdentity = identityChanged, refreshRoutines = routinesChanged, refreshApprovals = approvalsChanged
        identityChanged = routinesChanged = approvalsChanged = false
        if (stopped || epoch !== generation) return
        // Native approval events do not wake the shared lead projection. Read
        // the same Desk store once per burst, without another routine sweep.
        if (refreshApprovals) await desk.refresh()
        if (stopped || epoch !== generation) return
        if (refreshIdentity) { await load(); await loadHistory() }
        if (!refreshRoutines || stopped || epoch !== generation) return
        // A hint received during discovery must be read after that snapshot.
        if (routineFlight) await routineFlight
        if (!stopped && epoch === generation) await loadRoutines()
      }, 400)
    })
    return () => { stopped = true; clearTimeout(timer); stop() }
  }
  function mayManageRoutine(routine: AssistantRoutine) { return session.identity?.principal.kind === 'person' && can('recurrences.manage', routine.projectId) }
  async function toggleRoutine(assistantId: string, routine: AssistantRoutine): Promise<boolean> {
    const id = routine.id, who = owner.value, epoch = generation, binding = ownedRoutines.value.find(row => row.wire.id === id && row.owner === assistantId)
    const key = `routine:${id}`
    if (!binding || binding.wire.revision !== routine.revision || !mayManageRoutine(routine) || busy.value[key]) return false
    busy.value[key] = true
    // A pre-write discovery cannot land after the revisioned mutation.
    routineRead++; routineController?.abort(); routineFlight = undefined
    try {
      const updated = await pauseRecurrence(id, !routine.paused, routine.revision)
      if (who !== owner.value || epoch !== generation) return false
      if (updated.id !== id) throw new Error(assistantCopy().invalidResponse)
      const current = ownedRoutines.value.find(row => row.wire.id === id && row.owner === assistantId)
      if (!current || updated.revision < current.wire.revision) return false
      current.wire = updated; current.view = { ...current.view, ...presentAssistantRoutine(updated) }
      return true
    } finally { if (who === owner.value && epoch === generation) busy.value[key] = false }
  }
  watch([owner, mayRead, () => permissionsKnown()], () => { void load() }, { immediate: true })
  return { items, facts, state, error, truncated, mayConnect, mayRead, mayEdit, load, loadRoutines, watchPage, invalidate, save, disconnect, adopt, busy, events, historyState, historyTruncated, historyError, loadHistory, mayManageRoutine, toggleRoutine }
})
