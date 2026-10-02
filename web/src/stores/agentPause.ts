// SPDX-License-Identifier: AGPL-3.0-only
import { computed, ref, shallowRef, watch } from 'vue'
import { defineStore } from 'pinia'
import { can } from '../lib/authz'
import { message, readPauseDefault, savePauseDefault, readEstimateInterval, type HarnessSession } from '../lib/agents'
import { leavingReport, pauseSession, resumeSession } from '../lib/agentRows'
import { clockTime, cooperative, liveSession, pausedSession, type LeavingReport, type PauseLevel, type WindDownScope } from '../lib/agentPause'
import { toast } from '../lib/toast'
import { useAgents } from './agents'
import { useSession } from './session'
export type PauseMode = 'pause' | 'stop' | 'resume' | 'pause-all' | 'resume-all'
interface Target { id: string; project: string; run: string | null; control?: string; name: string }
export const useAgentPause = defineStore('agentPause', () => {
  const agents = useAgents(), session = useSession()
  const owner = computed(() => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
  const person = computed(() => session.identity?.principal.kind === 'person')
  const defaultLevel = ref<PauseLevel>('pause'), interval = ref<number | null>(null), settingsError = ref('')
  const workersIncluded = ref(false), leadTarget = shallowRef<Target | null>(null)
  const workerOptions = computed(() => leadTarget.value ? agents.sessions.filter(s => s.parent_harness_session_id === leadTarget.value!.id && eligible(s, 'pause')) : [])
  function includeWorkers(value: boolean) {
    if (value && workerOptions.value.length >= 200) { error.value = 'Select at most 200 agents at once.'; return }
    workersIncluded.value = value
    if (!leadTarget.value) return
    targets.value = [leadTarget.value, ...(value ? workerOptions.value.map(s => ({ id: s.id, project: s.project_id, run: s.run_id, control: s.pause?.control_id, name: s.display_label || s.agent?.name || s.host })) : [])]
  }
  const mode = ref<PauseMode | null>(null), targets = shallowRef<Target[]>([]), anchor = shallowRef<HTMLElement | null>(null)
  const busy = ref(false), error = ref(''), level = ref<PauseLevel>('pause'), note = ref('')
  const overrides = ref<Record<string, PauseLevel | 'keep'>>({})
  const resumeIds = ref<string[]>([])
  const report = ref<LeavingReport | null>(null), reportIds = ref<string[]>([]), leavingError = ref(''), leavingBusy = ref(false)
  const settled = computed(() => reportIds.value.length > 0 && reportIds.value.every(id => { const s = agents.sessionById(id); return !!s && !liveSession(s) && s.stop_reason !== 'heartbeat_lost' && !s.archived_at }))
  const completedAt = computed(() => { const times = reportIds.value.map(id => { const s = agents.sessionById(id); return Date.parse(s?.stopped_at || s?.pause?.paused_at || '') }).filter(Number.isFinite); return times.length ? Math.max(...times) : null })
  let announcedCompletion: string | null = null
  watch([() => report.value?.request_id, settled, leavingError], ([request, done, failed]) => {
    if (request && done && !failed && request !== announcedCompletion) { announcedCompletion = request; toast(completedAt.value === null ? 'Wind-down done.' : `Wind-down done at ${clockTime(completedAt.value)}.`) }
  })
  let epoch = 0, reads = 0, defaultsRead = 0
  let levelEdits = 0
  watch(level, () => { levelEdits++ }, { flush: 'sync' })
  watch(owner, () => {
    epoch++; reads++; defaultsRead++
    announcedCompletion = null
    mode.value = null; leadTarget.value = null; workersIncluded.value = false; targets.value = []; resumeIds.value = []; anchor.value = null; busy.value = false; error.value = ''; note.value = ''; overrides.value = {}
    report.value = null; reportIds.value = []; leavingError.value = ''; leavingBusy.value = false; defaultLevel.value = 'pause'; interval.value = null; settingsError.value = ''
  }, { flush: 'sync' })
  const permitted = (s: HarnessSession) => person.value && !s.watch && can('harness.control', s.project_id)
  const windDownPermitted = (s: HarnessSession) => permitted(s) && s.owner_principal_id === (report.value?.owner_principal_id ?? session.identity?.principal.id)
  const rows = computed(() => targets.value.map(t => ({ target: t, session: agents.sessionById(t.id) })).filter((row): row is { target: Target; session: HarnessSession } => !!row.session))
  const eligible = (s: HarnessSession, intent: PauseMode) => permitted(s) && (intent.includes('resume') ? pausedSession(s) : liveSession(s) && (intent !== 'pause' || cooperative(s)) && (intent === 'stop' || !s.pause || ['cancelled', 'resumed'].includes(s.pause.state)))
  function open(intent: PauseMode, sessions: readonly HarnessSession[], trigger?: HTMLElement | null) {
    if (busy.value) return
    const eligibleRows = sessions.filter(s => eligible(s, intent)).slice(0, 201)
    if (!eligibleRows.length) { toast('No eligible sessions to control.'); return }
    if (eligibleRows.length > 200) { toast('Select at most 200 agents at once.', { tone: 'error' }); return }
    mode.value = intent; level.value = defaultLevel.value === 'stop_now' && intent === 'pause' ? 'pause' : defaultLevel.value
    note.value = ''; error.value = ''; overrides.value = {}; anchor.value = trigger ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null)
    targets.value = eligibleRows.map(s => ({ id: s.id, project: s.project_id, run: s.run_id, control: s.pause?.control_id, name: s.display_label || s.agent?.name || s.host }))
    resumeIds.value = intent.includes('resume') ? targets.value.map(t => t.id) : []
    leadTarget.value = intent === 'pause' && eligibleRows[0]?.role === 'coordinator' ? targets.value[0]! : null; workersIncluded.value = false
    const turn = epoch, edits = levelEdits
    void loadSettings().then(() => { if (turn === epoch && mode.value === intent && levelEdits === edits) level.value = defaultLevel.value })
  }
  function close() { if (busy.value) return; mode.value = null; anchor.value?.focus({ preventScroll: true }) }
  async function loadSettings() {
    if (!person.value) return
    const turn = epoch, read = ++defaultsRead
    const results = await Promise.allSettled([readPauseDefault(), readEstimateInterval()])
    if (turn !== epoch || read !== defaultsRead) return
    const [defaults, estimates] = results
    if (defaults.status === 'fulfilled') { defaultLevel.value = defaults.value.default_level === 'stop_now' ? 'pause' : defaults.value.default_level; settingsError.value = '' }
    else settingsError.value = message(defaults.reason)
    interval.value = estimates.status === 'fulfilled' ? estimates.value.interval_minutes : null
  }
  async function setDefault(value: PauseLevel) {
    const turn = epoch; defaultsRead++; settingsError.value = ''
    try { const saved = await savePauseDefault(value); if (turn === epoch) { defaultLevel.value = saved.default_level; toast(`Default when pausing: ${saved.default_level.replaceAll('_', ' ')}.`) } }
    catch (e) { if (turn === epoch) settingsError.value = message(e) }
  }
  async function refreshLeaving() {
    if (!person.value || leavingBusy.value) return
    const turn = epoch, read = ++reads
    try {
      const answer = await leavingReport()
      if (turn !== epoch || read !== reads || leavingBusy.value) return
      reportIds.value = agents.admitSessions(answer.items).map(s => s.id); report.value = answer.report; leavingError.value = ''
    } catch (e) { if (turn === epoch && read === reads) leavingError.value = message(e) }
  }
  async function windDown(deadline: string, scope: WindDownScope) {
    if (!person.value || leavingBusy.value) return false
    const turn = epoch; leavingBusy.value = true; leavingError.value = ''; reads++
    try {
      const answer = await leavingReport('PUT', { deadline_at: deadline, hosts: scope.hosts, ...(scope.agents ? { agents: [...scope.agents] } : {}) })
      if (turn !== epoch) return false
      report.value = answer.report; reportIds.value = agents.admitSessions(answer.items).map(s => s.id)
      void agents.afterWrite(); toast(`Wind-down started · done by ${new Date(deadline).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}.`)
      return true
    } catch (e) { if (turn === epoch) leavingError.value = message(e); return false }
    finally { if (turn === epoch) leavingBusy.value = false }
  }
  async function clearWindDown(quiet: boolean) {
    if (!person.value || leavingBusy.value || !report.value?.deadline_at) return
    const previous = report.value, identity = owner.value, turn = epoch
    leavingBusy.value = true; reads++; leavingError.value = ''
    try {
      const answer = await leavingReport('DELETE')
      if (turn !== epoch || report.value?.request_id !== previous.request_id) return
      report.value = answer.report; reportIds.value = agents.admitSessions(answer.items).map(s => s.id); void agents.afterWrite()
      if (quiet) return
      const cancelledRead = reads
      toast(answer.report.stop_in_flight ? 'Pending wind-down requests withdrawn. A claimed stop cannot be recalled.' : 'Pending wind-down requests withdrawn. Already paused agents stay paused.', {
        action: Date.parse(previous.deadline_at!) > Date.now() ? { label: 'Undo', run: async () => {
          if (owner.value !== identity || reads !== cancelledRead || report.value?.request_id || report.value?.deadline_at || leavingBusy.value) return
          await refreshLeaving()
          if (owner.value !== identity || reads !== cancelledRead + 1 || leavingError.value || report.value?.request_id || report.value?.deadline_at || Date.parse(previous.deadline_at!) <= Date.now()) return
          await windDown(previous.deadline_at!, { hosts: previous.hosts, ...(previous.agents ? { agents: previous.agents } : {}) })
        } } : undefined,
      })
    } catch (e) { if (turn === epoch && report.value?.request_id === previous.request_id) leavingError.value = message(e) }
    finally { if (turn === epoch) leavingBusy.value = false }
  }
  async function cancelWindDown() { await clearWindDown(settled.value) }
  async function dismissReport() { if (settled.value) await clearWindDown(true) }
  function selectedLevel(s: HarnessSession): PauseLevel | 'keep' {
    if (mode.value === 'stop') return 'stop_now'
    if (overrides.value[s.id]) return overrides.value[s.id]!
    return cooperative(s) || level.value === 'stop_now' ? level.value : 'keep'
  }
  async function submit() {
    if (busy.value || !mode.value) return
    const intent = mode.value, turn = epoch, captured = targets.value.map(t => ({ ...t })), text = note.value, resumeSelection = new Set(resumeIds.value)
    const levels = new Map(rows.value.map(row => [row.target.id, selectedLevel(row.session)]))
    busy.value = true; error.value = ''; const failures: string[] = [], succeeded: string[] = []
    // Continuations ask leads first. A receipt is a durable request, not proof of launch.
    captured.sort((a, b) => Number(agents.sessionById(b.id)?.role === 'coordinator') - Number(agents.sessionById(a.id)?.role === 'coordinator'))
    for (const target of captured) {
      if (turn !== epoch) break
      if (intent.includes('resume') && !resumeSelection.has(target.id)) continue
      const s = agents.sessionById(target.id), choice = levels.get(target.id)
      if (choice === 'keep' && !intent.includes('resume')) continue
      if (!s || s.project_id !== target.project || s.run_id !== target.run || s.pause?.control_id !== target.control || !eligible(s, intent)) { failures.push(`${target.name}: session changed; reopen the dialog.`); continue }
      try {
        if (intent.includes('resume')) {
          const result = await resumeSession(target.project, target.id)
          if (turn !== epoch) break
          agents.recordSession(result.session); if (result.successor) agents.recordSession(result.successor)
        } else {
          if (!choice || choice === 'keep') throw new Error('No pause level selected.')
          const result = await pauseSession(target.project, target.id, choice, choice === 'stop_now' ? '' : text)
          if (turn !== epoch) break
          agents.recordSession(result)
        }
        succeeded.push(target.id)
      } catch (e) { failures.push(`${target.name}: ${message(e)}`) }
    }
    if (turn !== epoch) return
    busy.value = false
    if (succeeded.length) toast(intent.includes('resume') ? `${succeeded.length} resume request${succeeded.length === 1 ? '' : 's'} saved · awaiting continuation.` : `${succeeded.length} pause/stop request${succeeded.length === 1 ? '' : 's'} sent.`, { tone: failures.length ? 'error' : 'info' })
    if (failures.length) { targets.value = targets.value.filter(t => !succeeded.includes(t.id)); resumeIds.value = resumeIds.value.filter(id => !succeeded.includes(id)); error.value = `${succeeded.length} accepted; ${failures.length} failed. ${failures.join(' ')}` }
    else close()
  }
  return { workersIncluded, workerOptions, includeWorkers, resumeIds, person, defaultLevel, interval, settingsError, loadSettings, setDefault, permitted, windDownPermitted, eligible, mode, targets, rows, open, close, busy, error, level, note, overrides, selectedLevel, submit, report, reportIds, settled, completedAt, leavingError, leavingBusy, refreshLeaving, windDown, cancelWindDown, dismissReport }
})
