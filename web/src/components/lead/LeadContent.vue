<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, toRef, watch } from 'vue'
import { useRouter } from 'vue-router'
import { can } from '../../lib/authz'
import { leadActionAvailable, leadActionReason, decisionLine, LEAD_WORDS, unmanagedLeadCopy } from '../../lib/lead'
import { leadOverlay, openStartLead } from '../../lib/leadOverlay'
import { toast } from '../../lib/toast'
import { usePoller } from '../../lib/usePolledData'
import { useLeadSummary } from '../../lib/useLeadSummary'
import { useProjectLeads } from '../../stores/projectLeads'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import TicketPeekLink from '../TicketPeekLink.vue'
import { admissionHistory, panelModelLine } from '../../lib/leadPanel'
import { elapsed } from '../../lib/agentState'
import { relativeTime } from '../../lib/work'
import { useAgents } from '../../stores/agents'
import AgentGlyph from '../agents/AgentGlyph.vue'

// Lead's body shares the projection with the tree and chat. Session actions
// stay in the panel head while this content scrolls.
const props = defineProps<{ projectId: string; projectKey: string; routeKey: string; section?: string; sessionStarted?: string }>()
const emit = defineEmits<{ chat: [question: string] }>()
const agents = useAgents()
const w = LEAD_WORDS
const leads = useProjectLeads(), queue = useWorkQueue(), session = useSession(), router = useRouter()
const summary = useLeadSummary(toRef(props, 'projectId'), toRef(props, 'projectKey'))
const { view, lead, band, queued, leadSession, workers, workersKnown, workersPartial, decisions, complete, gateIds, mergedIds, questions: projectQuestions, questionsPartial, keyOf } = summary
const questions = computed(() => can('questions.read', props.projectId) ? projectQuestions.value : [])
// Relative times move on while the panel is open.
const now = ref(Date.now())
let clock: ReturnType<typeof setInterval> | undefined
const mayStart = computed(() => session.identity?.principal.kind === 'person' && leadActionAvailable(lead.value, 'start'))
const mayDecide = computed(() => session.identity?.principal.kind === 'person' && can('questions.read', props.projectId) && can('questions.decide', props.projectId))
const poller = usePoller(() => leads.load(props.projectId), 15_000)
watch(() => [props.projectId, session.identity?.tenant.id, session.identity?.principal.id], () => { void leads.load(props.projectId); void queue.load(props.projectId) }, { immediate: true })
onMounted(async () => { poller.start(); clock = setInterval(() => { now.value = Date.now() }, 10_000); await nextTick(); showSection() })
// The queue chip on an Agents lead row opens the panel at Next up (AEON-1210), also when it is open already.
const nextUp = ref<HTMLElement>()
function showSection() {
  if (props.section !== 'next-up' && leadOverlay.section !== 'next-up' || !nextUp.value) return false
  leadOverlay.section = null
  // Only the panel's own body scrolls; the page behind it stays where it is.
  const body = nextUp.value.closest<HTMLElement>('.pane-body')
  if (body) body.scrollTop += nextUp.value.getBoundingClientRect().top - body.getBoundingClientRect().top
  nextUp.value.focus({ preventScroll: true })
  return true
}
watch(() => [props.section, leadOverlay.section], () => { void nextTick(showSection) })
onBeforeUnmount(() => { poller.stop(); clearInterval(clock) })

const time = (iso?: string | null) => iso ? new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : ''
const minutes = (iso?: string | null) => iso ? Math.max(0, Math.round((now.value - new Date(iso).getTime()) / 60000)) : 0
const nowCopy = computed(() => {
  if (unmanaged.value && lead.value?.state === 'paused') return 'Continue from the session itself; PAIMOS cannot restart an unmanaged process.'
  if (lead.value?.state === 'working') return leadSession.value?.current_activity?.text || leadSession.value?.activity_note || band.value.now
  return band.value.now
})
// Workers by their real parent: unknown while the tree can't be read, a lower bound while only part of it was.
const workerCount = computed(() => workersKnown.value ? `${workers.value.length}${workersPartial.value ? '+' : ''}` : '—')
const reported = computed(() => {
  const s = leadSession.value
  if (!s?.heartbeat_at) return 'Not reported yet'
  const seconds = Math.max(0, Math.round((now.value - new Date(s.heartbeat_at).getTime()) / 1000))
  return seconds < 90 ? `reported ${seconds} s ago` : `reported ${Math.round(seconds / 60)} min ago`
})
const unmanaged = computed(() => {
  const principal = view.value?.principal
  return (leadSession.value?.management_mode ?? (principal?.session === lead.value?.session_id ? principal?.management : undefined)) === 'unmanaged'
})
const history = computed(() => admissionHistory(decisions.value, { complete: complete.value, error: view.value?.decisionsError ?? '', sessionStarted: props.sessionStarted ?? leadSession.value?.since, now: now.value }))
const recent = computed(() => [...decisions.value].reverse().slice(0, 6).map(d => ({ id: d.event_id, at: time(d.recorded_at), text: decisionLine(d, id => keyOf(id) ?? 'a ticket') })))
const next = computed(() => (queued.value ?? []).filter(item => !item.target_agent_id).slice(0, 3))
const href = (key: string) => `/p/${encodeURIComponent(props.routeKey)}/${encodeURIComponent(key)}`

async function cancelAdoption() {
  if (!leadActionAvailable(lead.value, 'cancel_adoption') || leads.busy[props.projectId]) return
  const project = props.projectId, who = `${session.identity?.tenant.id}:${session.identity?.principal.id}`
  try {
    const result = await leads.cancelAdoption(project)
    if (result && project === props.projectId && who === `${session.identity?.tenant.id}:${session.identity?.principal.id}`) toast('Adoption cancelled', { timeout: 5200 })
  } catch (e) { if (project === props.projectId && who === `${session.identity?.tenant.id}:${session.identity?.principal.id}`) toast(e instanceof Error ? e.message : 'The adoption was not cancelled.', { tone: 'error' }) }
}
const workerView = (id?: string | null) => agents.views.find(v => v.session.id === id)
const workerRows = computed(() => workers.value.map(worker => {
  const view = workerView(worker.session_id)
  return { worker, view, name: worker.ticket ? `${worker.ticket.key} · ${worker.ticket.title}` : view?.name || worker.name || 'Worker',
    model: view ? panelModelLine(view.session, agents.models, view.model) : '',
    account: view ? [view.harness, view.session.account_label || view.account].filter(Boolean).join(' · ') : '' }
}))
function openWorker(id?: string | null) { if (id) void router.push(`/agents/${encodeURIComponent(id)}`) }
</script>

<template>
  <div class="lead-content pane-body">
      <p v-if="view?.error" class="small warn" role="alert">{{ view.error }}</p>
      <div v-if="questions.length" class="needs" role="note">
        <AppIcon name="clock" :size="16" /><strong>{{ mayDecide ? 'It asks you' : 'Waits for a project admin' }}</strong>
        <p>{{ questions[0]!.input.question }}</p>
        <button v-if="mayDecide" type="button" class="btn primary sm" @click="emit('chat', questions[0]!.id)">Answer in Chat</button>
        <RouterLink v-else class="btn ghost sm" :to="{ path: '/decision-desk', query: { item: `q:${questions[0]!.id}` } }">Open on the Desk<AppIcon name="chevron-right" :size="13" /></RouterLink>
      </div>
      <p v-if="questionsPartial" class="small warn" data-partial><RouterLink to="/decision-desk">Not every open question could be read here; see the Decision Desk</RouterLink></p>
      <section class="p-sec" aria-labelledby="lp-now">
        <div class="p-head"><h3 id="lp-now">Right now</h3><span class="small">{{ reported }}</span></div>
        <p class="now-copy">{{ nowCopy }}</p>
        <p v-if="unmanaged" class="small" data-unmanaged>{{ unmanagedLeadCopy }}</p>
        <p v-if="lead?.reason === 'adoption_pending'" class="now-act"><button type="button" class="btn" data-act="cancel-adoption" :disabled="!leadActionAvailable(lead, 'cancel_adoption') || leads.busy[projectId]" :data-tip="leadActionReason(lead, 'cancel_adoption')" @click="cancelAdoption">Cancel adoption</button></p>
        <p v-else-if="!lead?.process_active && lead?.state !== 'paused'" class="now-act"><button type="button" class="btn primary sm" data-act="start" :disabled="!mayStart || leads.busy[projectId]" :data-tip="leadActionReason(lead, 'start')" @click="mayStart && openStartLead([projectId], $event.currentTarget as HTMLElement)"><AppIcon name="play" :size="13" />Start a lead</button></p>
        <dl class="facts">
          <div><dt>Workers</dt><dd :data-tip="!workersKnown ? 'Workers could not be read' : workersPartial ? 'Not all sessions could be read' : undefined">{{ workerCount }}</dd></div>
          <div><dt>Queued</dt><dd>{{ queued ? queued.length : '—' }}</dd></div>
          <div><dt>In the gate</dt><dd>{{ gateIds ? gateIds.length : '—' }}</dd></div>
          <div><dt>Merged today</dt><dd>{{ mergedIds ? mergedIds.length : '—' }}</dd></div>
        </dl>
      </section>
      <section class="p-sec" aria-labelledby="lp-workers">
        <div class="p-head"><h3 id="lp-workers">Workers</h3><span class="small">Chosen by role</span></div>
        <p v-if="!workersKnown" class="small warn">Its workers can’t be read right now.</p>
        <p v-else-if="workersPartial" class="small warn" data-partial>Not every session of this project could be read; more workers may run.</p>
        <p v-else-if="!workers.length" class="small faint">{{ lead?.state === 'paused' ? 'No worker runs while paused.' : 'No workers yet.' }}</p>
        <div v-if="workerRows.length" class="worker-list" role="table" aria-label="Lead workers">
          <div class="worker-columns" role="row"><span role="columnheader">Session</span><span role="columnheader">Time</span><span role="columnheader">Execution</span><span role="columnheader">Computer</span><span role="columnheader">Beat</span></div>
          <div v-for="row in workerRows" :key="row.worker.session_id ?? `${row.worker.harness}:${row.worker.since}`" class="worker-row" role="row">
            <button type="button" class="wk worker" :aria-label="`${row.name}: ${row.view?.status.label || row.worker.phase}`" :data-tip="[row.name, row.worker.current_activity?.text || row.worker.activity_note, row.model, row.account, row.view?.session.host].filter(Boolean).join(' · ')" @click="openWorker(row.worker.session_id)">
              <span class="worker-session" role="cell">
                <AgentGlyph v-if="row.view" :view="row.view" :size="26" /><span v-else class="vendor"><HarnessMark :harness="row.worker.harness" :size="15" /></span>
                <span class="wk-t"><span v-clip-tip="row.name" class="worker-name">{{ row.name }}</span><small v-clip-tip>{{ row.worker.current_activity?.text || row.worker.activity_note }}</small></span>
              </span>
              <span class="size worker-time" role="cell">{{ row.view ? elapsed(row.view.session, now) : `${minutes(row.worker.since)} min` }}</span>
              <span class="worker-execution" role="cell"><span v-clip-tip>{{ row.model || 'Model not reported' }}</span><small v-clip-tip>{{ row.account }}</small></span>
              <span v-clip-tip class="worker-computer" role="cell">{{ row.view?.session.host || 'Not reported' }}</span>
              <span class="size worker-beat" role="cell">{{ row.worker.heartbeat_at ? relativeTime(row.worker.heartbeat_at, { now }) : 'Not reported' }}</span>
            </button>
          </div>
        </div>
      </section>
      <section id="next-up" ref="nextUp" class="p-sec" tabindex="-1" aria-labelledby="lp-next">
        <div class="p-head"><h3 id="lp-next">Next up</h3><span class="small">Queue order · {{ queued ? `${queued.length} queued` : 'unavailable' }}</span></div>
        <p v-if="!queued" class="small warn">The queue can’t be read right now.</p>
        <p v-else-if="!next.length" class="small faint">Nothing is queued. Queue work on a ticket with <kbd class="keycap">Q</kbd>.</p>
        <TicketPeekLink v-for="item in next" :key="item.ticket_id" class="wk queued-item" :ticket-key="item.key" :href="href(item.key)">
          <AppIcon name="queue" :size="14" />
          <span class="wk-t"><span class="key">{{ item.key }}</span>{{ item.title }}<small>{{ item.waiting_reason ? `Waits: ${item.waiting_reason}` : item.expected_model ? `${item.expected_model}${item.expected_effort ? `, ${item.expected_effort}` : ''}` : `Model chosen by role when the ${w.l} picks it up` }}</small></span>
          <span v-if="item.estimate_hours" class="size">{{ item.estimate_hours }} h</span>
        </TicketPeekLink>
      </section>
      <section class="p-sec" aria-labelledby="lp-checks">
        <div class="p-head"><h3 id="lp-checks">Before every start</h3><span class="small admission-caption" :class="history.tone" data-tip="From the admission report of the last start."><AppIcon v-if="history.tone === 'held'" name="pause" :size="13" /><AppIcon v-else-if="history.tone === 'unreadable'" name="alert" :size="13" />{{ history.caption }}</span></div>
        <button v-if="history.tone === 'unreadable' && !history.rows.length" type="button" class="btn sm ghost" @click="leads.load(projectId)">Try again</button>
        <p v-if="history.note" class="small">{{ history.note }}</p>
        <ul v-if="history.rows.length" class="checks">
          <li v-for="row in history.rows" :key="row.kind" :class="{ 'bad-row': row.state === 'held' || row.state === 'unreadable' }" :data-check="row.kind">
            <AppIcon :name="row.state === 'passed' ? 'check' : row.state === 'held' ? 'pause' : row.state === 'unreadable' ? 'alert' : 'clock'" :size="14" /><span>{{ row.detail }}</span>
          </li>
        </ul>
      </section>
      <section class="p-sec" aria-labelledby="lp-recent">
        <div class="p-head"><h3 id="lp-recent">Recent</h3><span class="small">From the project’s event log</span></div>
        <p v-if="view?.decisionsError" class="small warn">{{ view.decisionsError }}</p>
        <p v-else-if="!complete && lead?.session_id" class="small faint">Reading the {{ w.l }}’s history…</p>
        <p v-else-if="!recent.length" class="small faint">Nothing reported yet.</p>
        <ul v-else class="log"><li v-for="r in recent" :key="r.id"><time>{{ r.at }}</time><span>{{ r.text }}</span></li></ul>
      </section>
  </div>
</template>
<style scoped>
.lead-content { flex: 1; min-height: 0; min-width: 0; overflow: auto; overscroll-behavior: contain; padding: 6px 16px 16px; }
.now-act { margin: 10px 0 0; }
.admission-caption { display: inline-flex; align-items: center; gap: 6px; }
.admission-caption.held, .admission-caption.unreadable { color: var(--queue-wait-ink); }
.worker { width: 100%; border: 0; background: transparent; text-align: left; cursor: pointer; }
.worker:hover { background: var(--row-hover); }
.worker-list { container-type: inline-size; }
.worker-columns, .wk.worker { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px; }
.wk.worker { height: 56px; }
.worker-session small { overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.worker-columns { padding: 4px 8px; color: var(--ink-3); font-size: 11.5px; }
.worker-session { display: flex; align-items: center; gap: 10px; min-width: 0; }
.worker-name { display: block; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.worker-execution, .worker-computer, .worker-beat, .worker-columns > :nth-child(n+3) { display: none; }
.worker-execution, .worker-computer { min-width: 0; }
.worker-execution > span, .worker-execution small, .worker-computer { overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.worker-execution > span { display: block; }
.worker-execution small { display: block; color: var(--ink-3); font-size: 11.5px; }
@container (min-width: 560px) {
  .worker-columns, .wk.worker { grid-template-columns: minmax(0, 1fr) 4ch minmax(0, .7fr) minmax(0, .4fr) 12ch; }
  .worker-execution, .worker-computer, .worker-beat, .worker-columns > :nth-child(n+3) { display: block; }
}
.needs { display: grid; grid-template-columns: 18px minmax(0, 1fr); align-items: center; gap: 2px 10px; margin: 10px 0 4px; padding: 10px 12px; border-radius: 12px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.needs strong { color: var(--ink); font-size: 13.5px; }
.needs p { grid-column: 2; margin: 0; font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.needs .btn { grid-column: 3; grid-row: 1 / span 2; }
.p-sec { padding: 14px 0 6px; scroll-margin-top: 8px; }
.p-sec:focus { outline: none; }
.p-sec:focus-visible { border-radius: 8px; box-shadow: var(--focus-ring); }
.p-sec + .p-sec { border-top: 1px solid var(--line); }
.p-head { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 2px 12px; margin-bottom: 8px; }
.p-head h3 { margin: 0; font-size: 13px; font-weight: 600; }
.small { margin: 0; font-size: 12.5px; color: var(--ink-3); }
.small.warn { color: var(--warn-ink); }
.faint { color: var(--ink-3); }
.now-copy { margin: 4px 0 0; font-size: 13.5px; color: var(--ink); }
.facts { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; margin: 12px 0 0; }
.facts > div { padding: 8px 10px; border-radius: 10px; background: var(--surface-sunken); }
.facts dt { font: 400 11.5px/1.5 var(--font); color: var(--ink-3); }
.facts dd { margin: 0; color: var(--ink); font-size: 18px; font-weight: 650; font-variant-numeric: tabular-nums; line-height: 1.3; }
.wk { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; align-items: center; gap: 10px; width: 100%; min-height: 48px; padding: 4px 8px; border: 0; border-radius: 10px; background: transparent; text-align: left; font-size: 13px; color: var(--ink); text-decoration: none; }
.wk:hover { background: var(--row-hover); }
.vendor { display: grid; place-items: center; color: var(--ink); }
.wk-t { display: block; min-width: 0; font-size: 13px; overflow-wrap: anywhere; }
.wk-t small { display: block; font-size: 11.5px; color: var(--ink-3); }
.wk-t .key { margin-right: 6px; font: 500 12px var(--mono); color: var(--teal-ink); }
.queued-item > svg { color: var(--ink-3); justify-self: center; }
.size { font: 500 11.5px var(--mono); color: var(--ink-3); white-space: nowrap; }
.checks { display: grid; margin: 0; padding: 0; list-style: none; }
.checks li { display: grid; grid-template-columns: 18px minmax(0, 1fr); align-items: center; gap: 10px; min-height: 40px; border-top: 1px solid var(--line); font-size: 13px; color: var(--ink); }
.checks li:first-child { border-top: 0; }
.checks li > span:last-child { color: var(--ink-2); font-size: 12.5px; overflow-wrap: anywhere; }
.checks li.bad-row > span:last-child { color: var(--warn-ink); font-weight: 600; }
.log { margin: 0; padding: 0; list-style: none; }
.log li { display: grid; grid-template-columns: 44px minmax(0, 1fr); gap: 8px; padding: 4px 0; font-size: 12.5px; color: var(--ink-2); }
.log time { font: 500 11.5px var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
@media (max-width: 720px) {
  .lead-content { padding: 6px 12px 16px; }
  .btn { min-height: 44px; min-width: 44px; }
  .facts { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .needs { grid-template-columns: 18px minmax(0, 1fr); }
  .needs .btn { grid-column: 2; grid-row: auto; justify-self: start; margin-top: 8px; }
}
@media (pointer: coarse) { .btn { min-height: 44px; min-width: 44px; } }
@media (pointer: coarse) { .keycap.hint { display: none; } }
</style>
