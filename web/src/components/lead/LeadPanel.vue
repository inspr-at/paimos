<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, toRef, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import { canPause, canResume, checksSummary, decisionLine, LEAD_WORDS, startChecks } from '../../lib/lead'
import { closeLeadPanel, leadOverlay, openLeadPause } from '../../lib/leadOverlay'
import { toast } from '../../lib/toast'
import { usePoller } from '../../lib/usePolledData'
import { useLeadSummary } from '../../lib/useLeadSummary'
import { useProjectLeads } from '../../stores/projectLeads'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import TicketPeekLink from '../TicketPeekLink.vue'
import LeadBot from './LeadBot.vue'

// The one detailed home of a lead (the ticket panel pattern): Right now,
// Workers, Next up, Before every start and Recent. Long content scrolls in the
// body; Pause or Resume stays in the pinned footer.
const props = defineProps<{ projectId: string; projectKey: string; routeKey: string }>()
const w = LEAD_WORDS
const leads = useProjectLeads(), queue = useWorkQueue(), session = useSession(), router = useRouter()
const summary = useLeadSummary(toRef(props, 'projectId'), toRef(props, 'projectKey'))
const { view, lead, band, queued, leadSession, workers, decisions, complete, gateIds, mergedIds, questions, questionsPartial, keyOf } = summary
const closeButton = ref<HTMLButtonElement>()
const dial = ref<{ running: number; total: number } | null>(null)
// Relative times move on while the panel is open.
const now = ref(Date.now())
let clock: ReturnType<typeof setInterval> | undefined
const mayControl = computed(() => session.identity?.principal.kind === 'person' && can('harness.control', props.projectId))
const mayStart = computed(() => mayControl.value && can('run.create', props.projectId))
const poller = usePoller(() => leads.load(props.projectId), 15_000)
async function readDial() {
  const who = session.identity?.principal.id
  try {
    const response = await api('/agents/plan')
    if (!response.ok || who !== session.identity?.principal.id) return
    const plan = await response.json() as { total: number; running_total: number }
    dial.value = { running: plan.running_total, total: plan.total }
  } catch { dial.value = null }
}
watch(() => [props.projectId, session.identity?.principal.id], () => { dial.value = null; void leads.load(props.projectId); void queue.load(props.projectId); void readDial() }, { immediate: true })
onMounted(async () => { poller.start(); clock = setInterval(() => { now.value = Date.now() }, 10_000); await nextTick(); closeButton.value?.focus() })
onBeforeUnmount(() => { poller.stop(); clearInterval(clock) })

const time = (iso?: string | null) => iso ? new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : ''
const minutes = (iso?: string | null) => iso ? Math.max(0, Math.round((now.value - new Date(iso).getTime()) / 60000)) : 0
const subtitle = computed(() => {
  const s = leadSession.value
  return [band.value.status, s && lead.value?.state === 'working' ? `since ${time(s.since)}` : '', s?.model].filter(Boolean).join(' · ')
})
const nowCopy = computed(() => {
  if (lead.value?.state === 'working') {
    const room = dial.value ? Math.max(0, dial.value.total - dial.value.running) : null
    const activity = leadSession.value?.current_activity?.text || leadSession.value?.activity_note
    return [activity, `${workers.value.length} ${workers.value.length === 1 ? 'worker runs' : 'workers run'}.`, room === null ? '' : room ? `${room} more ${room === 1 ? 'fits' : 'fit'} within the dial.` : 'The dial is full.'].filter(Boolean).join(' ')
  }
  return band.value.now
})
const reported = computed(() => {
  const s = leadSession.value
  if (!s?.heartbeat_at) return 'Not reported yet'
  const seconds = Math.max(0, Math.round((now.value - new Date(s.heartbeat_at).getTime()) / 1000))
  return seconds < 90 ? `Reported ${seconds} seconds ago` : `Reported ${Math.round(seconds / 60)} minutes ago`
})
const checks = computed(() => startChecks(lead.value, decisions.value, dial.value))
const recent = computed(() => [...decisions.value].reverse().slice(0, 6).map(d => ({ id: d.event_id, at: time(d.recorded_at), text: decisionLine(d, id => keyOf(id) ?? 'a ticket') })))
const next = computed(() => (queued.value ?? []).filter(item => !item.target_agent_id).slice(0, 3))
const href = (key: string) => `/p/${encodeURIComponent(props.routeKey)}/${encodeURIComponent(key)}`
const harnessName = (h: string) => ({ codex: 'Codex', claude: 'Claude', cursor: 'Cursor', grok: 'Grok', pi: 'Pi', gemini: 'Gemini', opencode: 'OpenCode' } as Record<string, string>)[h] ?? h

async function resume() {
  const project = props.projectId, who = session.identity?.principal.id
  try {
    const result = await leads.start(project)
    if (result && project === props.projectId && who === session.identity?.principal.id) toast(`${props.projectKey} ${w.l} restarting`, { timeout: 5200 })
  } catch (e) { if (project === props.projectId) toast(e instanceof Error ? e.message : 'The lead did not restart.', { tone: 'error' }) }
}
function keydown(event: KeyboardEvent) {
  if (event.key !== 'Escape' || leadOverlay.start || leadOverlay.pause) return
  if ((event.target as HTMLElement).matches('input,textarea,select,[contenteditable="true"]')) return
  event.preventDefault(); event.stopPropagation(); closeLeadPanel()
}
function openSession() { const id = lead.value?.session_id; if (id) { closeLeadPanel(false); void router.push(`/agents/${id}`) } }
</script>

<template>
  <aside class="lead-panel" aria-labelledby="lead-pane-title" @keydown="keydown">
    <header class="pane-bar">
      <span class="lead-bot"><LeadBot :busy="band.busy" /></span>
      <div class="pane-title"><h2 id="lead-pane-title">{{ projectKey }} {{ w.l }}</h2><p>{{ subtitle }}</p></div>
      <button v-if="lead?.session_id" type="button" class="icon-btn" :aria-label="`Open the ${w.l}’s session`" data-tip="Open session" @click="openSession"><AppIcon name="agent" /></button>
      <button ref="closeButton" type="button" class="btn sm ghost pane-close" aria-label="Close details" @click="closeLeadPanel()"><kbd class="keycap hint">Esc</kbd><AppIcon name="close" :size="14" /></button>
    </header>
    <div class="pane-body">
      <div v-if="questions.length" class="needs" role="note">
        <AppIcon name="inbox" :size="16" /><strong>{{ questions.length }} {{ questions.length === 1 ? 'question' : 'questions' }} for you</strong>
        <p>{{ questions[0]!.input.question }}</p>
        <RouterLink class="btn primary sm" :to="{ path: '/decision-desk', query: { needs: `q:${questions[0]!.id}` } }">Answer</RouterLink>
      </div>
      <p v-if="questionsPartial" class="small warn" data-partial><RouterLink to="/decision-desk">Not every open question could be read here; see the Decision Desk</RouterLink></p>
      <section class="p-sec" aria-labelledby="lp-now">
        <div class="p-head"><h3 id="lp-now">Right now</h3><span class="small">{{ reported }}</span></div>
        <p class="now-line">
          <span v-if="band.tone === 'live'" class="live-mark" aria-hidden="true" /><span v-else-if="band.tone === 'wait'" class="dot wait" aria-hidden="true" /><span v-else-if="band.tone === 'warn'" class="dot warn" aria-hidden="true" /><AppIcon v-else name="pause" :size="13" />{{ band.status }}
        </p>
        <p class="now-copy">{{ nowCopy }}</p>
        <dl class="facts">
          <div><dt>Workers</dt><dd>{{ workers.length }}</dd><dd class="sub">this project</dd></div>
          <div><dt>Queued</dt><dd>{{ queued ? queued.length : '—' }}</dd><dd class="sub">{{ next[0] ? `next ${next[0].key}` : queued ? 'nothing waits' : 'can’t be read' }}</dd></div>
          <div><dt>Gate</dt><dd>{{ gateIds ? gateIds.length : '—' }}</dd><dd class="sub">{{ gateIds?.[0] ? keyOf(gateIds[0]) ?? '…' : gateIds ? 'none' : 'not read yet' }}</dd></div>
          <div><dt>Merged</dt><dd>{{ mergedIds ? mergedIds.length : '—' }}</dd><dd class="sub">today</dd></div>
        </dl>
      </section>
      <section class="p-sec" aria-labelledby="lp-workers">
        <div class="p-head"><h3 id="lp-workers">Workers</h3><span class="small">Chosen by role</span></div>
        <p v-if="!workers.length" class="small faint">{{ lead?.state === 'paused' ? 'No worker runs while paused.' : 'No workers yet.' }}</p>
        <div v-for="s in workers" :key="s.session_id ?? `${s.harness}:${s.since}`" class="wk">
          <span class="vendor"><HarnessMark :harness="s.harness" :size="15" /></span>
          <span class="wk-t"><TicketPeekLink v-if="s.ticket" class="key" :ticket-key="s.ticket.key" :href="href(s.ticket.key)" />{{ s.ticket?.title ?? 'No ticket bound' }}</span>
          <span class="st"><span class="live-mark" aria-hidden="true" />{{ s.phase === 'working' ? `${minutes(s.since)} min` : s.phase }}</span>
          <span class="wk-s">{{ [harnessName(s.harness), [s.model, s.reasoning_effort].filter(Boolean).join(', '), s.current_activity?.text || s.activity_note].filter(Boolean).join(' · ') }}</span>
        </div>
      </section>
      <section class="p-sec" aria-labelledby="lp-next">
        <div class="p-head"><h3 id="lp-next">Next up</h3><span class="small">Queue order · Move to top on the ticket</span></div>
        <p v-if="!queued" class="small warn">The queue can’t be read right now.</p>
        <p v-else-if="!next.length" class="small faint">Nothing is queued. Queue work on a ticket with <kbd class="keycap">Q</kbd>.</p>
        <div v-for="item in next" :key="item.ticket_id" class="wk">
          <span class="vendor"><AppIcon name="queue" :size="14" /></span>
          <span class="wk-t"><TicketPeekLink class="key" :ticket-key="item.key" :href="href(item.key)" />{{ item.title }}</span>
          <span class="st"><span v-if="item.estimate_hours" class="size">{{ item.estimate_hours }} h</span></span>
          <span class="wk-s">{{ item.waiting_reason ? `Waits: ${item.waiting_reason}` : item.expected_model ? `${item.expected_model}${item.expected_effort ? `, ${item.expected_effort}` : ''}` : `Model chosen by role when the ${w.l} picks it up` }}</span>
        </div>
      </section>
      <section class="p-sec" aria-labelledby="lp-checks">
        <div class="p-head"><h3 id="lp-checks">Before every start</h3><span class="small">{{ checksSummary(checks) }}</span></div>
        <ul class="checks">
          <li v-for="c in checks" :key="c.kind" :class="{ 'bad-row': c.state === 'unreadable' || c.state === 'full' }" :data-check="c.kind">
            <span :class="c.state === 'ok' ? 'ok' : c.state === 'unknown' ? 'unknown' : 'bad'"><AppIcon :name="c.state === 'ok' ? 'check' : c.state === 'unknown' ? 'clock' : 'alert'" :size="14" /></span>
            <span>{{ c.label }}</span><span>{{ c.detail }}</span>
          </li>
        </ul>
        <p class="footnote"><AppIcon name="shield" :size="14" /><span>If any check can’t be read, nothing starts. Running agents are never stopped by a check.</span></p>
      </section>
      <section class="p-sec" aria-labelledby="lp-recent">
        <div class="p-head"><h3 id="lp-recent">Recent</h3><span class="small">From the project’s event log</span></div>
        <p v-if="view?.decisionsError" class="small warn">{{ view.decisionsError }}</p>
        <p v-else-if="!complete && lead?.session_id" class="small faint">Reading the {{ w.l }}’s history…</p>
        <p v-else-if="!recent.length" class="small faint">Nothing reported yet.</p>
        <ul v-else class="log"><li v-for="r in recent" :key="r.id"><time>{{ r.at }}</time><span>{{ r.text }}</span></li></ul>
      </section>
    </div>
    <footer class="pane-foot">
      <template v-if="lead?.state === 'paused'">
        <p>{{ canResume(lead) ? 'Restarts through the usual start checks.' : 'Resume waits until its session has stopped.' }}</p>
        <button type="button" class="btn primary" data-act="resume" :aria-disabled="!mayStart || !canResume(lead) || leads.busy[projectId]" @click="mayStart && canResume(lead) && resume()"><AppIcon name="play" :size="15" />Resume</button>
      </template>
      <template v-else>
        <p>Workers finish their step; nothing new starts.</p>
        <button type="button" class="btn" data-act="pause" :aria-disabled="!mayControl || !canPause(lead)" @click="mayControl && canPause(lead) && openLeadPause(projectId, $event.currentTarget as HTMLElement)"><AppIcon name="pause" :size="15" />Pause…</button>
      </template>
    </footer>
  </aside>
</template>

<style scoped>
.lead-panel { position: fixed; z-index: 40; top: calc(var(--header-h) + 10px); right: 10px; bottom: calc(var(--footer-h, 0px) + 10px); width: min(560px, calc(100vw - 20px)); display: flex; flex-direction: column; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); -webkit-backdrop-filter: blur(20px) saturate(1.15); backdrop-filter: blur(20px) saturate(1.15); }
@media (min-width: 1100px) { .lead-panel { width: var(--panel-w); } }
.pane-bar { display: flex; align-items: center; gap: 10px; flex: none; min-height: 62px; padding: 10px 10px 10px 16px; border-bottom: 1px solid var(--line); }
.lead-bot { display: grid; place-items: center; flex: none; width: 36px; height: 36px; border-radius: 11px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.pane-title { flex: 1; min-width: 0; }
.pane-title h2 { margin: 0; font-size: 16px; }
.pane-title p { margin: 0; font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.pane-close { gap: 6px; padding: 4px 8px 4px 6px; color: var(--ink-2); }
.pane-body { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; padding: 18px 22px 28px; }
.pane-foot { flex: none; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 16px; border-top: 1px solid var(--line); border-radius: 0 0 var(--radius) var(--radius); background: var(--surface-raised-2); }
.pane-foot p { margin: 0; font-size: 12.5px; color: var(--ink-3); }
.pane-foot .btn[aria-disabled="true"] { opacity: .55; cursor: default; }
.needs { display: grid; grid-template-columns: 18px minmax(0, 1fr) auto; align-items: center; gap: 2px 10px; margin-bottom: 20px; padding: 12px 14px; border-radius: 12px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); }
.needs strong { color: var(--ink); font-size: 13.5px; }
.needs p { grid-column: 2; margin: 0; font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.needs .btn { grid-column: 3; grid-row: 1 / span 2; }
.p-sec { padding: 18px 0 20px; border-top: 1px solid var(--line); }
.pane-body > .p-sec:first-child, .needs + .p-sec { padding-top: 0; border-top: 0; }
.p-head { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 2px 12px; margin-bottom: 12px; }
.p-head h3 { margin: 0; font-size: 14px; font-weight: 600; }
.small { margin: 0; font-size: 12.5px; color: var(--ink-3); }
.small.warn { color: var(--warn-ink); }
.faint { color: var(--ink-3); }
.now-line { display: flex; align-items: center; gap: 10px; margin: 0; color: var(--ink); font-weight: 600; }
.now-copy { margin: 4px 0 0; font-size: 13px; color: var(--ink-2); }
.facts { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px 12px; margin: 16px 0 0; }
.facts dt { font: 500 10.5px/1.5 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.facts dd { margin: 3px 0 0; color: var(--ink); font-size: 16px; font-weight: 600; font-variant-numeric: tabular-nums; line-height: 1.3; }
.facts dd.sub { margin: 1px 0 0; font-size: 12px; font-weight: 400; color: var(--ink-3); }
.wk { display: grid; grid-template-columns: 28px minmax(0, 1fr) auto; align-items: center; gap: 2px 12px; margin: 0 -8px; padding: 10px 8px; border-top: 1px solid var(--line); }
.wk:first-of-type { border-top: 0; }
.vendor { grid-row: span 2; align-self: start; margin-top: 2px; display: grid; place-items: center; width: 28px; height: 28px; border-radius: 9px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink); }
.wk-t { min-width: 0; color: var(--ink); font-size: 13.5px; font-weight: 600; overflow-wrap: anywhere; }
.wk-t .key { margin-right: 6px; font: 500 12px var(--mono); color: var(--teal-ink); text-decoration: none; }
.wk-s { grid-column: 2 / -1; font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.st { display: inline-flex; align-items: center; gap: 8px; justify-self: end; font-size: 12.5px; color: var(--ink-2); }
.size { font: 600 11px/1 var(--mono); color: var(--ink); padding: 3px 5px; border-radius: 5px; box-shadow: inset 0 0 0 1px var(--line-2); }
.checks { display: grid; margin: 0; padding: 0; list-style: none; }
.checks li { display: grid; grid-template-columns: 18px minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 40px; border-top: 1px solid var(--line); font-size: 13px; color: var(--ink); }
.checks li:first-child { border-top: 0; }
.checks li > span:last-child { color: var(--ink-2); font-size: 12.5px; text-align: right; font-variant-numeric: tabular-nums; }
.checks .ok { color: var(--ok); }
.checks .unknown { color: var(--ink-3); }
.checks .bad { color: var(--warn-ink); }
.checks li.bad-row > span:last-child { color: var(--warn-ink); font-weight: 600; }
.log { margin: 0; padding: 0; list-style: none; }
.log li { display: grid; grid-template-columns: 52px minmax(0, 1fr); gap: 10px; padding: 6px 0; font-size: 13px; color: var(--ink-2); }
.log time { font: 500 12px/1.5 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.footnote { display: grid; grid-template-columns: 16px minmax(0, 1fr); gap: 8px; margin: 12px 0 0; font-size: 12.5px; color: var(--ink-3); }
.footnote svg { margin-top: 2px; }
.live-mark { display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--teal); box-shadow: 0 0 0 3px color-mix(in srgb, var(--teal) 18%, transparent); }
.dot { display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; }
.dot.warn { background: var(--gold); }
.dot.wait { box-shadow: inset 0 0 0 1.8px var(--gold); }
@media (max-width: 720px) {
  .lead-panel { z-index: 58; inset: 0; width: auto; border-radius: 0; border: 0; background: var(--canvas); -webkit-backdrop-filter: none; backdrop-filter: none; }
  .pane-bar { padding-top: max(10px, env(safe-area-inset-top)); }
  .pane-body { padding: 16px 16px 24px; }
  .pane-foot { border-radius: 0; padding: 10px 16px calc(10px + env(safe-area-inset-bottom)); background: var(--surface-raised); }
  .pane-foot p { display: none; }
  .pane-foot .btn { flex: 1; min-height: 44px; }
  .facts { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .needs { grid-template-columns: 18px minmax(0, 1fr); }
  .needs .btn { grid-column: 2; grid-row: auto; justify-self: start; margin-top: 8px; }
}
@media (pointer: coarse) { .keycap.hint { display: none; } }
</style>
