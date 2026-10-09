<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, toRef, watch } from 'vue'
import { useRouter } from 'vue-router'
import { can } from '../../lib/authz'
import { canLaunchLead, canPause, canResume, leadLaunchReason, LEAD_WORDS } from '../../lib/lead'
import { useLeadCardFold } from '../../lib/leadCardFold'
import { openLeadPanel, openLeadPause, openStartLead } from '../../lib/leadOverlay'
import { toast } from '../../lib/toast'
import { usePoller } from '../../lib/usePolledData'
import { useLeadSummary } from '../../lib/useLeadSummary'
import { useProjectLeads } from '../../stores/projectLeads'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import LeadBot from './LeadBot.vue'
import LeadLine from './LeadLine.vue'

// AEON-741: the lead is the centre of a project. One sentence says whether it
// runs, what it does and what comes next; one primary action per state.
const props = defineProps<{ projectId: string; projectKey: string; routeKey: string }>()
const w = LEAD_WORDS
const leads = useProjectLeads(), queue = useWorkQueue(), session = useSession(), router = useRouter()
const summary = useLeadSummary(toRef(props, 'projectId'), toRef(props, 'projectKey'))
const { lead, band, leadSession, workers, stations, questions, questionsPartial } = summary
const person = computed(() => session.identity?.principal.kind === 'person')
const mayControl = computed(() => person.value && can('harness.control', props.projectId))
const mayStart = computed(() => mayControl.value && can('run.create', props.projectId))
const busy = computed(() => !!leads.busy[props.projectId])
// Shown once the lead has been read: no permission or an older server shows nothing,
// rather than a band that appears and vanishes again.
const readError = computed(() => leads.views[props.projectId]?.error ?? '')
// AEON-1027: the "No lead" card folds to one line, per person and project. It opens in its final
// shape (the preference is read first), and a project that gets a lead forgets the fold.
const viewer = computed(() => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
const hasLead = computed(() => !!lead.value && band.value.state !== 'none')
const { ready: foldReady, collapsed: folded, moving, toggle: toggleFold, settle: settleFold } = useLeadCardFold(toRef(props, 'projectId'), viewer, hasLead)
const shown = computed(() => !!lead.value && (band.value.state !== 'none' || foldReady.value))
const foldTip = computed(() => `${folded.value ? 'Unfold' : 'Fold'} the ${w.l} card`)
const poller = usePoller(() => leads.load(props.projectId), 15_000)
watch(() => [props.projectId, session.identity?.principal.id], () => { void leads.load(props.projectId); void queue.load(props.projectId) }, { immediate: true })
onMounted(() => poller.start())
onBeforeUnmount(() => poller.stop())

const time = (iso?: string | null) => iso ? new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : ''
const stateDetail = computed(() => {
  const s = leadSession.value
  if (!s || !lead.value) return ''
  const parts: string[] = []
  if (lead.value.state === 'working') parts.push(`since ${time(s.since)}`, `${workers.value.length} ${workers.value.length === 1 ? 'worker' : 'workers'}`)
  if (s.model) parts.push([s.model, s.reasoning_effort].filter(Boolean).join(', '))
  return parts.join(' · ')
})
const nowLine = computed(() => {
  if (lead.value?.state === 'working') return leadSession.value?.current_activity?.text || leadSession.value?.activity_note || ''
  return band.value.now
})
// Primary action: disabled with its reason rather than hidden, so the slot never moves.
const action = computed(() => {
  const b = band.value, l = lead.value
  switch (b.action) {
    case 'start': return { label: b.actionLabel, icon: 'play' as const, primary: true, disabled: !mayStart.value || !canLaunchLead(l), tip: !canLaunchLead(l) ? leadLaunchReason(l) : mayStart.value ? `One per project. Starts nothing until the ${w.l} picks up queued work.` : `Starting a ${w.l} needs permission to run agents in this project` }
    case 'cancel': return { label: b.actionLabel, icon: null, primary: false, disabled: !mayControl.value || !canPause(l), tip: 'Nothing has started yet. Queued work stays queued.' }
    case 'pause': return { label: b.actionLabel, icon: b.state === 'starting' ? null : 'pause' as const, primary: false, disabled: !mayControl.value || !canPause(l), tip: mayControl.value ? 'Stops new work now; running workers finish their step' : 'Only its owner can pause it' }
    case 'resume': return { label: b.actionLabel, icon: 'play' as const, primary: true, disabled: !mayStart.value || !canResume(l) || !canLaunchLead(l), tip: !canLaunchLead(l) ? leadLaunchReason(l) : canResume(l) ? `Restarts the ${w.l} through the usual start checks` : `Waits until the ${w.l}’s session has stopped` }
    case 'dial': return { label: b.actionLabel, icon: 'gauge' as const, primary: false, disabled: false, tip: 'The dial and its limits on the Agents page' }
    case 'computers': return { label: b.actionLabel, icon: 'monitor' as const, primary: true, disabled: false, tip: 'Accounts and computers on the Agents page' }
    default: return null
  }
})
async function act(event: MouseEvent) {
  const from = event.currentTarget as HTMLElement, a = band.value.action
  if (action.value?.disabled || busy.value) return
  if (a === 'start') openStartLead([props.projectId], from)
  else if (a === 'pause' && band.value.state !== 'starting') openLeadPause(props.projectId, from)
  else if (a === 'pause' || a === 'cancel') await control('cancel')
  else if (a === 'resume') await control('resume')
  else if (a === 'dial') void router.push('/agents')
  else if (a === 'computers') void router.push({ path: '/agents', hash: '#ac-title' })
}
async function control(kind: 'cancel' | 'resume') {
  const project = props.projectId, who = session.identity?.principal.id
  try {
    const next = kind === 'resume' ? await leads.start(project) : await leads.pause(project)
    if (!next || project !== props.projectId || who !== session.identity?.principal.id) return
    toast(kind === 'resume' ? `${props.projectKey} ${w.l} restarting` : 'Start cancelled', { timeout: 5200 })
  } catch (e) {
    if (project === props.projectId) toast(e instanceof Error ? e.message : 'The lead did not change.', { tone: 'error' })
  }
}
const menu = ref<HTMLElement | null>(null)
function openSession() { const id = lead.value?.session_id; menu.value = null; if (id) void router.push(`/agents/${id}`) }
const openPanel = (from: HTMLElement) => { if (lead.value && lead.value.state !== 'none') openLeadPanel(props.projectId, from) }
const deskLink = (id: string) => ({ path: '/decision-desk', query: { needs: `q:${id}` } })
</script>

<template>
  <section v-if="shown" class="lead" :class="{ folded, moving }" :data-state="band.state" aria-labelledby="lead-title" aria-live="polite">
    <div class="lead-top">
      <span class="lead-bot"><LeadBot :busy="band.busy" /></span>
      <div class="lead-who">
        <h2 id="lead-title">
          <template v-if="band.state === 'none'">{{ band.title }}</template>
          <button v-else type="button" class="name-link" :aria-label="`${band.title}: open details`" @click="openPanel($event.currentTarget as HTMLElement)">{{ band.title }}<AppIcon name="chevron-right" :size="12" /></button>
        </h2>
        <span class="lead-sep" aria-hidden="true">·</span>
        <p class="lead-state">
          <template v-if="readError"><AppIcon name="alert" :size="13" /><span class="warn">Not refreshed: {{ readError }}</span></template>
          <template v-else>
            <span v-if="band.tone === 'live'" class="live-mark" aria-hidden="true" />
            <span v-else-if="band.tone === 'wait'" class="dot wait" aria-hidden="true" />
            <span v-else-if="band.tone === 'warn'" class="dot warn" aria-hidden="true" />
            <AppIcon v-else name="pause" :size="13" />
            <span :class="{ warn: band.tone === 'warn' }"><b>{{ band.status }}</b><template v-if="stateDetail"> · {{ stateDetail }}</template></span>
          </template>
        </p>
      </div>
      <div class="lead-acts">
        <button v-if="action && action.label" type="button" class="btn act-main" :class="{ primary: action.primary }" :aria-disabled="action.disabled || busy" :aria-describedby="['start', 'resume'].includes(band.action) && !canLaunchLead(lead) ? 'lead-launch-reason-band' : undefined" :data-tip="action.tip" data-act="main" @click="act">
          <AppIcon v-if="action.icon" :name="action.icon" :size="15" />{{ action.label }}
        </button>
        <button v-if="band.state !== 'none' && lead?.session_id" type="button" class="icon-btn" :aria-label="`More ${w.l} actions`" aria-haspopup="menu" :aria-expanded="!!menu" @click="menu = menu ? null : $event.currentTarget as HTMLElement"><AppIcon name="more" /></button>
        <button v-if="band.state === 'none'" type="button" class="icon-btn flat fold-btn" aria-controls="lead-fold" :aria-expanded="!folded" :aria-label="`What the ${w.l} does`" :data-tip="foldTip" data-act="fold" @click="toggleFold"><AppIcon name="chevron-right" :size="16" /></button>
      </div>
    </div>

    <p v-if="['start', 'resume'].includes(band.action) && !canLaunchLead(lead)" id="lead-launch-reason-band" class="lead-now" data-launch-reason>{{ leadLaunchReason(lead) }}</p>

    <!-- The fold hides only the details; the head and Start lead stay where they are. -->
    <div v-if="band.state === 'none'" id="lead-fold" class="lead-fold" :inert="folded || undefined" @transitionend="settleFold" @transitioncancel="settleFold">
      <div class="lead-fold-inner">
        <ul class="empty-lead">
          <li><AppIcon name="queue" :size="14" /><span>Picks up what people queue, in their order, and sizes each item.</span></li>
          <li><AppIcon name="agent" :size="14" /><span>Chooses harness, model and thinking by role, and starts workers within your dial.</span></li>
          <li><AppIcon name="inbox" :size="14" /><span>Drives gate, fix, merge and release. You only see its questions, on the Decision Desk.</span></li>
        </ul>
        <p class="lead-foot"><AppIcon name="shield" :size="14" /><span>{{ band.foot }}</span></p>
      </div>
    </div>
    <template v-else>
      <p v-if="nowLine" class="lead-now">{{ nowLine }}</p>
      <LeadLine :stations="stations" :route-key="routeKey" :empty="band.state === 'starting'" @open="openPanel" />
      <div v-if="questions.length || questionsPartial" class="asks">
        <div v-for="q in questions.slice(0, 2)" :key="q.id" class="ask">
          <span class="ask-icon"><AppIcon name="inbox" :size="16" /></span>
          <h3>{{ q.input.question }}</h3>
          <p>The {{ projectKey }} {{ w.l }} asks. {{ q.input.options.length }} {{ q.input.options.length === 1 ? 'option' : 'options' }}<template v-if="q.input.recommend"> and its recommendation</template> wait on the Decision Desk.</p>
          <RouterLink class="btn primary sm" :to="deskLink(q.id)">Answer</RouterLink>
        </div>
        <p v-if="questions.length > 2" class="asks-more"><RouterLink to="/decision-desk">{{ questions.length - 2 }} more{{ questionsPartial ? ' and possibly others' : '' }} on the Decision Desk</RouterLink></p>
        <p v-else-if="questionsPartial" class="asks-more" data-partial><RouterLink to="/decision-desk">Not every open question could be read here; see the Decision Desk</RouterLink></p>
      </div>
    </template>
    <p v-if="band.state !== 'none'" class="lead-foot"><AppIcon name="shield" :size="14" /><span>{{ band.foot }}</span></p>

    <FloatingPanel v-if="menu" :anchor="menu" align="end" :width="300" :label="`More ${w.l} actions`" @close="restore => { if (restore) menu?.focus(); menu = null }">
      <div role="menu" class="lead-menu">
        <button type="button" role="menuitem" class="mi" @click="openSession"><AppIcon name="agent" /><span class="t">Open session</span><span class="d">The {{ w.l }}’s own session: chat, steps, handover.</span></button>
      </div>
    </FloatingPanel>
  </section>
</template>

<style scoped>
.lead { --lead-px: 20px; margin: 22px 0 16px; padding: 18px var(--lead-px) 6px; border: 1px solid var(--line); border-radius: var(--radius); background: var(--surface-raised-2); box-shadow: 0 24px 48px -36px color-mix(in srgb, var(--shadow-color) 35%, transparent); }
.lead-top { display: grid; grid-template-columns: 44px minmax(0, 1fr) auto; grid-template-areas: 'bot who acts'; align-items: start; gap: 4px 14px; }
.lead-bot { grid-area: bot; display: grid; place-items: center; width: 44px; height: 44px; border-radius: 14px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.lead-who { grid-area: who; min-width: 0; display: grid; gap: 1px; }
.lead-who h2 { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; margin: 0; font: 400 21px/1.25 var(--serif); letter-spacing: -.015em; color: var(--ink); }
.name-link { display: inline-flex; align-items: center; gap: 6px; min-height: 24px; padding: 0; border: 0; background: none; color: var(--ink); font: inherit; letter-spacing: inherit; text-align: left; cursor: pointer; }
.name-link:hover { color: var(--teal-ink); }
.name-link svg { color: var(--ink-3); }
.lead-state { display: flex; align-items: center; gap: 8px; min-height: 20px; margin: 0; font-size: 12.5px; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.lead-state b { color: var(--ink); font-weight: 600; }
.lead-state .warn, .lead-state .warn b { color: var(--warn-ink); }
.lead-acts { grid-area: acts; display: flex; align-items: center; gap: 6px; margin-top: 2px; }
.act-main[aria-disabled="true"] { opacity: .55; cursor: default; }
/* AEON-1027: the head row has one height whether the card is open or folded, so Start lead and the
   fold control keep their place; the details below fold by height alone (nothing above moves). */
.lead[data-state="none"] { padding-top: 14px; padding-bottom: 14px; }
.lead[data-state="none"] .lead-acts { margin-top: 0; }
.lead[data-state="none"] .lead-foot { padding-bottom: 0; }
.lead-sep { display: none; color: var(--ink-3); }
.lead.folded .lead-who { display: flex; flex-wrap: wrap; align-items: center; column-gap: 10px; row-gap: 0; }
.lead.folded .lead-sep { display: inline; }
.fold-btn :deep(svg) { transition: transform .22s ease; }
.fold-btn[aria-expanded="true"] :deep(svg) { transform: rotate(90deg); }
.lead-fold { display: grid; grid-template-rows: 1fr; margin-inline: calc(var(--lead-px) * -1); }
.lead.folded .lead-fold { grid-template-rows: 0fr; visibility: hidden; }
.lead.moving .lead-fold { transition: grid-template-rows .26s cubic-bezier(.2, .7, .2, 1), visibility 0s .26s; }
.lead.moving:not(.folded) .lead-fold { transition: grid-template-rows .26s cubic-bezier(.2, .7, .2, 1), visibility 0s 0s; }
.lead-fold-inner { min-height: 0; min-width: 0; padding-inline: var(--lead-px); }
.lead.folded .lead-fold-inner, .lead.moving .lead-fold-inner { overflow: hidden; }
@media (min-width: 721px) { .lead[data-state="none"] .lead-top { align-items: center; min-height: 48px; } }
.lead-now { margin: 14px 0 0 58px; max-width: 72ch; color: var(--ink); font-size: 15px; line-height: 1.5; }
.empty-lead { margin: 14px 0 6px 58px; padding: 0; list-style: none; display: grid; gap: 8px; max-width: 64ch; }
.empty-lead li { display: grid; grid-template-columns: 18px minmax(0, 1fr); gap: 10px; font-size: 13.5px; color: var(--ink-2); }
.empty-lead li svg { margin-top: 3px; color: var(--teal-ink); }
.asks { margin: 6px -20px 0; border-top: 1px solid var(--line); }
.ask { display: grid; grid-template-columns: 32px minmax(0, 1fr) auto; align-items: center; gap: 2px 14px; padding: 14px 20px; }
.ask + .ask { border-top: 1px solid var(--line); }
.ask-icon { grid-row: span 2; align-self: start; display: grid; place-items: center; width: 32px; height: 32px; border-radius: 50%; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); }
.ask h3 { margin: 0; font-size: 14px; font-weight: 600; color: var(--ink); overflow-wrap: anywhere; }
.ask p { grid-column: 2; margin: 0; font-size: 13px; color: var(--ink-2); }
.ask > .btn { grid-column: 3; grid-row: 1 / span 2; }
.asks-more { margin: 0; padding: 0 20px 10px 66px; font-size: 12.5px; }
.lead-foot { display: flex; align-items: center; gap: 8px; margin: 6px -20px 0; padding: 10px 20px 8px; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink-3); }
.lead-foot svg { flex: none; color: var(--ink-3); }
.lead-menu { display: grid; }
.mi { display: grid; grid-template-columns: 20px minmax(0, 1fr); gap: 2px 10px; width: 100%; padding: 9px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; cursor: pointer; }
.mi:hover, .mi:focus-visible { background: var(--row-hover); outline: none; }
.mi svg { grid-row: span 2; margin-top: 2px; color: var(--ink-2); }
.mi .t { font-size: 13.5px; font-weight: 600; }
.mi .d { color: var(--ink-3); font-size: 12px; line-height: 1.4; }
.live-mark { display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--teal); box-shadow: 0 0 0 3px color-mix(in srgb, var(--teal) 18%, transparent); }
.dot { display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; }
.dot.warn { background: var(--gold); }
.dot.wait { box-shadow: inset 0 0 0 1.8px var(--gold); }
@media (max-width: 720px) {
  .lead { --lead-px: 14px; padding: 16px var(--lead-px) 4px; }
  /* Phone: glyph and controls share the first row; name and state grow below them. */
  .lead-top { grid-template-columns: 40px minmax(0, 1fr) auto; grid-template-areas: 'bot acts more' 'who who who'; align-items: center; row-gap: 10px; }
  .lead-bot { width: 40px; height: 40px; }
  .lead-acts { display: contents; }
  .lead-acts .icon-btn { grid-area: more; }
  .lead-acts .act-main { grid-area: acts; justify-self: end; min-height: 44px; }
  .lead-acts .fold-btn { width: 44px; height: 44px; }
  /* Phone: the name and state already sit on their own row below the controls; folded, they stay stacked. */
  .lead.folded .lead-who { display: grid; }
  .lead.folded .lead-sep { display: none; }
  .lead-now, .empty-lead { margin-left: 0; }
  .asks, .lead-foot { margin-left: -14px; margin-right: -14px; }
  .ask { grid-template-columns: 32px minmax(0, 1fr); padding: 14px; }
  .ask > .btn { grid-column: 2; grid-row: auto; justify-self: stretch; margin-top: 10px; min-height: 44px; }
  .asks-more { padding-left: 60px; }
  .lead-foot { padding: 10px 14px 8px; }
}
@media (pointer: coarse) { .mi { min-height: 52px; } .fold-btn { width: 44px; height: 44px; } }
@media (prefers-reduced-motion: reduce) { .lead.moving .lead-fold, .fold-btn :deep(svg) { transition: none; } }
</style>
