<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { capacityWaitText } from '../../lib/capacityWait'
import { brand } from '../../lib/brand'
import { getNode } from '../../lib/api'
import { can } from '../../lib/authz'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Approval, SessionControl } from '../../lib/agents'
import { RUN_OUTCOME, approvalRun, cost, elapsed, runDuration, runModel, scopeLabel, stopReasonLabel, tokens } from '../../lib/agentState'
import { absoluteTime, relativeTime, statusMeta } from '../../lib/work'
import { useAgents, type SessionView } from '../../stores/agents'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import TicketPeekLink from '../TicketPeekLink.vue'
import { useRoute, useRouter } from 'vue-router'
import SessionChat from './SessionChat.vue'
import SessionTabs from './SessionTabs.vue'
import { initialTab, saveTab, type SessionTab } from './sessionChat'
import { useVisualViewport } from '../../lib/visualViewport'
import AgentStateLabel from './AgentStateLabel.vue'
import AgentGlyph from './AgentGlyph.vue'
import ProvenanceDetail from './ProvenanceDetail.vue'
import SessionStateEvidence from './SessionStateEvidence.vue'
import ListeningLabel from './ListeningLabel.vue'
import SessionRecovery from './SessionRecovery.vue'
import RemoveSessionDialog from './RemoveSessionDialog.vue'
import ManagedSessionControls from './ManagedSessionControls.vue'
import LiveWatch from './LiveWatch.vue'
import { activityOf, currentStep, currentActivity, activityDurations } from './activity'
import { cleanActivityNote } from '../../lib/activityPrivacy'
import { metadataChangeText, metadataChanges } from './metadataHistory'
import EtaCell from '../work/EtaCell.vue'
import DeliveryRating from '../work/DeliveryRating.vue'
import { etaFromSession } from '../../lib/eta'
import { quickRemoval } from './sessionActions'
import { sessionEtaEligible } from './sessionRow'
import ServiceTierBlock from './ServiceTierBlock.vue'
import { tierHistoryText, tierRunCostLabel, tierCostAmount } from '../../lib/serviceTier'
import { useServiceTiers } from '../../stores/serviceTiers'
const serviceTiers = useServiceTiers()

// One session in the docked panel: who and where, the bound ticket, then two tabs:
// Overview (now, details, work, runs, provenance) and Messages (thread and composer).
const props = defineProps<{ view: SessionView | undefined; loading: boolean; now: number; canWrite: boolean; controlBlock: (view: SessionView, kind: SessionControl['kind']) => string }>()
const emit = defineEmits<{ close: []; control: [view: SessionView, kind: SessionControl['kind']]; review: [approval: Approval] }>()
const agents = useAgents()
const auth = useSession()
const root = ref<HTMLElement>()
const thread = ref<HTMLElement>()
const recovery = ref<{ open: () => void }>()
const removal = ref<{ remove: () => void }>()
// Same breakpoint as the full-height sheet. Only a managed session with the
// control capability collapses Recover, Remove and settings into one overflow.
const phoneMedia = window.matchMedia('(max-width: 720px)')
const phone = ref(phoneMedia.matches)
function syncPhone() { phone.value = phoneMedia.matches }
const managedControls = computed(() => {
  const session = props.view?.session
  return !!session && session.management_mode === 'managed' && session.advertised_capabilities.includes('managed_control_v1')
})
// A watched session is read-only (AEON-258): no controls to collapse.
const compactControls = computed(() => phone.value && managedControls.value && !reported.value?.watch)
const showRecover = computed(() => {
  const session = props.view?.session
  return !!session && !session.archived_at && (can('harness.recover', session.project_id) || can('harness.force_stop', session.project_id))
})
const showRemove = computed(() => {
  const session = props.view?.session
  return !!session && auth.identity?.principal.kind === 'person' && !session.archived_at && can('harness.read', session.project_id)
})
// The last tab is remembered per viewer; ?tab=messages deep-links (AEON-273).
const route = useRoute()
const router = useRouter()
const tab = ref<SessionTab>(initialTab(route.query.tab))
const unread = ref(0)
function selectTab(next: SessionTab) {
  tab.value = next
  saveTab(next)
  if (route.query.tab !== undefined) { const query = { ...route.query }; delete query.tab; void router.replace({ query }) }
}
watch(() => route.query.tab, value => { if (value === 'messages' || value === 'overview') tab.value = value })
useVisualViewport(root)
const ticketState = ref('')

const s = computed(() => props.view?.session)
const pending = computed(() => s.value?.run_id && s.value.phase !== 'stopped' && s.value.needs_attention !== false ? agents.pending.filter(a => a.agent_principal_id === s.value!.agent_principal_id && approvalRun(a) === s.value!.run_id) : [])
const recentRuns = computed(() => s.value ? agents.recentRuns(s.value.agent_principal_id).slice(0, 8) : [])
const run = computed(() => props.view?.run)
// The detail read lands in the ledger like every other copy of the session, so what is
// shown here is always the row the ledger holds: a detail that arrives late never
// replaces a newer list row, and the list never hides the detail's history (AEON-449).
const activity = computed(() => props.view ? activityOf(props.view) : null)
const reported = computed(() => s.value)
// A watched session has no Messages tab, so it always shows the overview with the live view.
const pane = computed<SessionTab>(() => reported.value?.watch ? 'overview' : tab.value)
const hasWork = computed(() => !!(reported.value?.brief || reported.value?.worktree || reported.value?.branch || reported.value?.commits?.length))
const timeline = computed(() => activity.value?.agent_activity_mode === 'off' ? [] : (activity.value?.activity_history ?? []).flatMap(item => {
  const note = cleanActivityNote(item.note)
  return note ? [{ ...item, note }] : []
}))
const currentTimeline = computed(() => activity.value?.agent_activity_mode === 'off' ? [] : activityDurations((activity.value?.current_activity_history ?? []).filter(item => activity.value?.agent_activity_mode !== 'tool_activity' || item.source === 'auto'), props.now, s.value?.stopped_at))
const tierState = computed(() => s.value ? serviceTiers.state(s.value) : undefined)
const runCost = computed(() => tierState.value?.run_cost?.run_id === run.value?.id ? tierState.value.run_cost : undefined)
const metadataHistory = computed(() => [
  ...metadataChanges(s.value?.metadata_history).map((item, i) => ({ id: `metadata-${i}`, at: item.at, text: metadataChangeText(item) })),
  ...(tierState.value?.history ?? []).map(item => ({ id: `tier-${item.id}`, at: item.at, text: tierHistoryText(item) })),
].sort((a, b) => Date.parse(b.at) - Date.parse(a.at)))
const step = computed(() => {
  if (!props.view) return ''
  return currentActivity(props.view, props.now) || currentStep(props.view)
})
const setupLine = computed(() => {
  const r = reported.value
  if (!props.view) return ''
  return [props.view.harness, r?.harness_version].filter(Boolean).join(' ')
})
const modelLine = computed(() => [reported.value?.model || props.view?.model, reported.value?.reasoning_effort].filter(Boolean).join(' · '))
// Fetch only the selected session, and refresh when a heartbeat changes metadata
// even if the activity note stays the same. The list does not carry history.
watch([
  () => s.value?.id,
  () => s.value?.project_id,
  () => s.value?.heartbeat_at,
  () => s.value?.display_label,
  () => s.value?.model,
  () => s.value?.reasoning_effort,
  () => props.view ? activityOf(props.view).activity_note : '',
], async (_current, _previous, onCleanup) => {
  const current = s.value
  if (!current) return
  const controller = new AbortController()
  onCleanup(() => controller.abort())
  try {
    await agents.loadSessionDetail(current.project_id, current.id, controller.signal)
  } catch { /* The list still shows the latest step if detail is unavailable. */ }
}, { immediate: true })
watch(() => props.view?.ticket?.id, async id => {
  ticketState.value = ''
  if (!id) return
  try { const node = await getNode(id); if (props.view?.ticket?.id === id) ticketState.value = statusMeta(node.state).label } catch { /* Ticket summary remains useful. */ }
}, { immediate: true })
watch(() => s.value?.id, async id => {
  if (!id || !s.value) return
  thread.value?.scrollTo({ top: 0 })
  await agents.refreshAgentRuns(s.value.agent_principal_id)
}, { immediate: true })
onMounted(() => { root.value?.focus({ preventScroll: true }); phoneMedia.addEventListener('change', syncPhone) })
onBeforeUnmount(() => phoneMedia.removeEventListener('change', syncPhone))

// AEON-291: offer Interrupt and Stop only where they work right now; a session
// outside the product gets one quiet line instead of disabled buttons.
const works = (kind: SessionControl['kind']) => !!props.view && !props.controlBlock(props.view, kind)
const outside = computed(() => !!s.value && s.value.management_mode === 'unmanaged' && s.value.phase !== 'stopped' && !s.value.archived_at)
const quick = computed(() => !!props.view && quickRemoval(props.view))
function pickTier() {
  const view = props.view, anchor = root.value?.querySelector<HTMLElement>('[aria-label="More session actions"]')
  if (view && anchor) serviceTiers.open(view.session, view.name, anchor)
}
function control(kind: SessionControl['kind']) { if (props.view && !props.controlBlock(props.view, kind)) emit('control', props.view, kind) }
defineExpose({ focus: () => root.value?.focus({ preventScroll: true }) })
</script>

<template>
  <aside ref="root" class="session-panel" aria-label="Session details" tabindex="-1">
    <!-- The identity stays in view while runs and messages scroll below it. -->
    <header class="panel-head">
      <div class="head-top">
        <template v-if="view && !loading">
          <AgentGlyph :view="view" :size="36" />
          <h2 class="name" :title="view.name">{{ view.name }}</h2>
          <AgentStateLabel :state="view.status.state" :label="view.status.label" />
        </template>
        <span class="spacer" />
        <button type="button" class="icon-btn sm flat" aria-label="Close session details" aria-keyshortcuts="Escape" data-tip="Close · Esc" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
      </div>
      <div v-if="view && !loading" class="head-sub">
        <TicketPeekLink v-if="view.ticket" class="ticket-detail" :ticket-key="view.ticket.key" :href="view.ticket.href" :tip="view.ticket.title"><span class="ticket-chip">{{ view.ticket.key }}</span><span class="head-ticket">{{ view.ticket.title }}</span></TicketPeekLink>
        <span v-if="ticketState" class="ticket-status">{{ ticketState }}</span>
      </div>
      <div v-if="view && !loading && !compactControls" class="head-actions">
        <span class="host-meta">{{ view.harness }}<template v-if="view.session.host"> on {{ view.session.host }}</template></span>
        <span class="spacer" />
        <template v-if="!reported?.watch && !view.session.advertised_capabilities.includes('managed_control_v1')">
          <button v-if="works('interrupt')" type="button" class="btn sm ghost" data-tip="Stop the current turn" @click="control('interrupt')"><AppIcon name="interrupt" :size="14" />Interrupt</button>
          <button v-if="works('stop')" type="button" class="btn sm ghost stop" data-tip="End this session" @click="control('stop')"><AppIcon name="halt" :size="14" />Stop</button>
        </template>
        <SessionRecovery v-if="!reported?.watch" :session="view.session" />
        <RemoveSessionDialog :session="view.session" :label="view.name" :quick="quick" />
      </div>
      <p v-if="view && !loading && outside" class="outside-note">Runs outside {{ brand.short_name }} — stop it in its terminal</p>
      <ManagedSessionControls v-if="view && !loading && !reported?.watch" :session="reported || view.session" :now="now" :run-status="view.run?.status">
        <template v-if="compactControls" #more>
          <button v-if="view && !serviceTiers.unavailable(view.session)" type="button" role="menuitem" class="menu-item" :disabled="!!serviceTiers.state(view.session).pending" @click="pickTier"><AppIcon name="gauge" :size="16" /><span class="mi-text">Change tier…</span></button>
          <button v-if="showRecover" type="button" role="menuitem" class="menu-item" @click="recovery?.open()"><AppIcon name="wrench" :size="16" /><span class="mi-text"><span>Recover</span></span></button>
          <button v-if="showRemove" type="button" role="menuitem" class="menu-item" :aria-label="`Remove ${view.name}`" @click="removal?.remove()"><AppIcon name="trash" :size="16" /><span class="mi-text"><span>{{ quick ? 'Remove' : 'Remove…' }}</span></span></button>
        </template>
      </ManagedSessionControls>
      <SessionRecovery v-if="view && !loading && compactControls" ref="recovery" hide-trigger :session="view.session" />
      <RemoveSessionDialog v-if="view && !loading && compactControls" ref="removal" hide-trigger :session="view.session" :label="view.name" :quick="quick" />
      <SessionTabs v-if="view && !loading && !reported?.watch" :selected="tab" :unread="tab === 'messages' ? 0 : unread" @select="selectTab" />
    </header>

    <!-- Until the first load completes the body stays a placeholder, so runs and
         messages arrive together instead of pushing each other down. -->
    <div v-if="loading" class="scroll" role="status" aria-label="Loading session">
      <div class="sk"><span class="skeleton w40" /><span class="skeleton w70" /><span class="skeleton w90" /><span class="skeleton w60" /></div>
    </div>
    <div v-else-if="!view" class="scroll">
      <div class="gone">
        <span class="gone-icon"><AppIcon name="agent" :size="20" /></span>
        <h2>This session is not here</h2>
        <p>It may belong to a project you cannot see, or the server no longer lists it.</p>
        <button type="button" class="btn" @click="emit('close')">Back to agents</button>
      </div>
    </div>

    <div v-else v-show="pane === 'overview'" id="session-panel-overview" ref="thread" class="scroll" role="tabpanel" aria-labelledby="session-tab-overview">
      <div v-if="pending.length" class="callout" role="note">
        <AppIcon name="shield" :size="15" />
        <div class="callout-text">
          <strong>{{ pending.length === 1 ? 'Waiting for your permission' : `${pending.length} requests wait for you` }}</strong>
          <span>{{ scopeLabel(pending[0].scope) }}</span>
        </div>
        <button type="button" class="btn sm primary" @click="emit('review', pending[0])">Review</button>
      </div>

      <LiveWatch v-if="reported?.watch" :session="reported" />

      <section class="now-block" aria-labelledby="now-title">
        <h3 id="now-title" class="sr-only">Now</h3>
        <p v-if="view.session.archived_at" class="now-meta">Archived registration · process state unknown. No process was stopped by recovery.</p>
        <strong class="now-step">{{ step }}</strong>
        <p class="now-meta">
          <span :data-tip="`Since ${absoluteTime(view.session.created_at)}`">{{ view.session.stopped_at ? 'Ran' : 'Running' }} {{ elapsed(view.session, now) }}</span>
          <template v-if="view.session.stopped_at"> · {{ view.status.state === 'done' ? 'Done' : stopReasonLabel(view.session.stop_reason) || 'Ended' }} {{ relativeTime(view.session.stopped_at, { now }) }}</template>
          <template v-else-if="view.session.heartbeat_at"> · heartbeat <time :datetime="view.session.heartbeat_at" :data-tip="absoluteTime(view.session.heartbeat_at)">{{ relativeTime(view.session.heartbeat_at, { now }) }}</time></template>
          <template v-else> · no heartbeat yet</template>
        </p>
        <p v-if="view.session.phase !== 'stopped' && !view.session.stopped_at && !view.session.archived_at" class="now-meta now-listen"><ListeningLabel :session="view.session" :now="now" /></p>
        <p v-if="sessionEtaEligible(view) && (etaFromSession(view.session) || view.session.phase === 'working')" class="now-meta now-eta"><EtaCell align="start" labelled :eta="etaFromSession(view.session)" :now="now" :missing="view.session.phase === 'working'" /></p>
        <SessionStateEvidence :view="view" :now="now" />
        <ol v-if="currentTimeline.length" class="activity-timeline" aria-label="Current activity history">
          <li v-for="(item, index) in currentTimeline.slice(0, 6)" :key="`${item.at}-${index}`"><time :datetime="item.at" :title="absoluteTime(item.at)">{{ item.duration }}</time><span>{{ item.text }}</span></li>
        </ol>
        <ol v-else-if="timeline.length" class="activity-timeline" aria-label="Recent activity">
          <li v-for="(item, index) in timeline.slice(0, 6)" :key="`${item.at}-${index}`"><time :datetime="item.at">{{ relativeTime(item.at, { now }) }}</time><span>{{ item.note }}</span></li>
        </ol>
        <DeliveryRating v-if="s && s.phase === 'stopped'" :session-id="s.id" />
      </section>

      <ServiceTierBlock :key="view.session.id" :session="view.session" :name="view.name" />

      <section class="block first" aria-labelledby="setup-title">
        <h3 id="setup-title" class="eyebrow">Details</h3>
        <dl class="facts">
          <div class="fact"><dt>Project</dt><dd>
            <RouterLink v-if="view.projectKey" class="project-link" :to="`/p/${encodeURIComponent(view.projectKey)}`">{{ view.projectTitle || view.projectKey }}</RouterLink>
            <span v-else class="muted">Workspace</span>
          </dd></div>
          <div class="fact"><dt>Runs on</dt><dd><span>{{ view.session.host }}</span><span class="muted">{{ view.session.role === 'coordinator' ? 'lead session' : 'worker' }} · {{ view.session.management_mode === 'managed' ? `owned by ${brand.short_name}` : 'runs on its own' }}</span></dd></div>
          <div v-if="modelLine" class="fact"><dt>Model</dt><dd class="mono">{{ modelLine }}<span v-if="run && run.model_evidence === 'vendor_reported'" class="evidence" data-tip="Reported by the vendor, not only requested"><AppIcon name="check" :size="11" /></span></dd></div>
          <div v-if="reported?.account_label || view.account" class="fact"><dt>Account</dt><dd>{{ reported?.account_label || view.account }}</dd></div>
          <div class="fact"><dt>Harness</dt><dd>{{ setupLine }}</dd></div>
        </dl>
        <ol v-if="metadataHistory.length" class="metadata-history" aria-label="Recent session changes">
          <li v-for="item in metadataHistory" :key="item.id"><time :datetime="item.at">{{ relativeTime(item.at, { now }) }}</time><span>{{ item.text }}</span></li>
        </ol>
        <p v-if="tierState?.history_truncated" class="muted">Showing the latest 50 tier events.</p>
      </section>

      <section v-if="hasWork" class="block" aria-labelledby="work-title">
        <h3 id="work-title" class="eyebrow">Work</h3>
        <dl class="facts">
          <div v-if="reported?.brief" class="fact wide"><dt>Brief</dt><dd>{{ reported.brief }}</dd></div>
          <div v-if="reported?.branch" class="fact"><dt>Branch</dt><dd class="mono">{{ reported.branch }}</dd></div>
          <div v-if="reported?.worktree" class="fact wide"><dt>Worktree</dt><dd class="mono">{{ reported.worktree }}</dd></div>
          <div v-if="reported?.commits?.length" class="fact wide"><dt>Commits</dt><dd><ol class="commits"><li v-for="commit in reported.commits" :key="commit.sha"><code>{{ commit.sha }}</code><span>{{ commit.subject }}</span></li></ol></dd></div>
        </dl>
      </section>

      <section v-if="run" class="block" aria-labelledby="telemetry-title">
        <h3 id="telemetry-title" class="eyebrow">Current run</h3>
        <p v-if="run.wait" class="outside-note">{{ capacityWaitText(run.wait, 'Agents', now) }}</p>
        <div class="telemetry">
          <div class="metric"><span class="metric-label">Status</span><span class="run-chip" :class="run.wait?.code === 'vendor' ? '' : RUN_OUTCOME[run.status].tone">{{ run.wait?.code === 'vendor' ? 'Throttled' : RUN_OUTCOME[run.status].label }}</span></div>
          <div class="metric"><span class="metric-label">Tokens in</span><b>{{ tokens(run.input_tokens) }}</b></div>
          <div class="metric"><span class="metric-label">Tokens out</span><b>{{ tokens(run.output_tokens) }}</b></div>
          <div class="metric"><span class="metric-label">Cost</span><b>{{ runCost ? tierCostAmount(runCost) : cost(run.cost_micros) }}</b><small v-if="runCost" class="tier-cost">{{ tierRunCostLabel(runCost) }}</small><small v-if="runCost">{{ runCost.provisional ? 'Provisional token estimate' : 'Token estimate' }} · not billed cost</small></div>
        </div>
      </section>

      <section v-if="view.session.management_mode === 'managed' && recentRuns.length" class="block" aria-labelledby="runs-title">
        <h3 id="runs-title" class="eyebrow">Recent runs</h3>
        <div class="runs" role="table" aria-label="Recent runs">
          <div class="run-row run-head" role="row">
            <span role="columnheader">Outcome</span><span role="columnheader">Model</span><span role="columnheader" class="run-tokens">Tokens</span>
            <span role="columnheader" class="run-duration">Took</span><span role="columnheader" class="run-when">Started</span>
          </div>
          <div v-for="item in recentRuns" :key="item.id" class="run-row" role="row">
            <span role="cell"><span class="run-chip" :class="RUN_OUTCOME[item.status].tone">{{ RUN_OUTCOME[item.status].label }}</span></span>
            <span role="cell" class="run-model mono" :data-tip="runModel(item) || undefined">{{ runModel(item) || 'Agent run' }}</span>
            <span role="cell" class="run-tokens mono" :data-tip="`${item.input_tokens.toLocaleString()} in · ${item.output_tokens.toLocaleString()} out`">{{ tokens(item.input_tokens + item.output_tokens) }}</span>
            <span role="cell" class="run-duration mono">{{ runDuration(item, now) || '—' }}</span>
            <time role="cell" class="run-when" :datetime="item.created_at" :data-tip="absoluteTime(item.started_at ?? item.created_at)">{{ relativeTime(item.started_at ?? item.created_at, { now }) }}</time>
          </div>
        </div>
      </section>

      <ProvenanceDetail v-if="s" :project-id="s.project_id" :session-id="s.id" :now="now" />
    </div>
    <SessionChat v-if="view && !loading && !reported?.watch" v-show="tab === 'messages'" id="session-panel-messages" :view="view" :now="now" :can-write="canWrite" :active="tab === 'messages'"
      role="tabpanel" aria-labelledby="session-tab-messages" @unread="unread = $event" />
  </aside>
</template>

<style scoped>
.session-panel {
  position: fixed; z-index: 15; top: calc(var(--header-h) + 10px); right: 10px; bottom: calc(var(--footer-h) + 10px); width: min(560px, calc(100vw - 20px));
  display: flex; flex-direction: column; min-height: 0; outline: none;
  border-radius: var(--radius); border: 1px solid var(--glass-edge);
  background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow);
  -webkit-backdrop-filter: blur(20px) saturate(1.15); backdrop-filter: blur(20px) saturate(1.15);
}
.session-panel:focus-visible { box-shadow: var(--shadow-pop), var(--focus-ring); }
@media (min-width: 1100px) { .session-panel { width: var(--panel-w); } }
@media (prefers-reduced-motion: no-preference) {
  .session-panel { animation: panel-in .22s cubic-bezier(.2, .7, .2, 1); }
  @keyframes panel-in { from { opacity: 0; transform: translateX(24px); } to { opacity: 1; transform: none; } }
}
.panel-head { flex-shrink: 0; padding: 8px 10px 10px 18px; border-bottom: 1px solid var(--line); }
.head-top { display: flex; align-items: center; gap: 8px; min-height: 36px; }
.head-actions { display: flex; align-items: center; gap: 4px; min-width: 0; margin-top: 6px; }
.head-actions .btn { display: inline-flex; align-items: center; justify-content: center; gap: 5px; white-space: nowrap; }
.outside-note { margin: 2px 0 0; font-size: 12px; line-height: 1.4; color: var(--ink-3); }
.host-meta { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: 12px; }
.name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 18px; font-weight: 650; letter-spacing: -.01em; }
.state-text { flex-shrink: 0; font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.state-text.needs { color: var(--gold-ink); }
.head-sub { display: flex; align-items: center; gap: 8px; min-width: 0; margin-top: 6px; padding-right: 8px; font-size: 12.5px; color: var(--ink-2); }
.head-sub .ticket-detail { display: inline-flex; align-items: center; gap: 8px; min-width: 0; flex: 0 1 auto; color: var(--ink); text-decoration: none; }
.head-sub .ticket-detail:hover .head-ticket { color: var(--teal-ink); }
.head-sub .ticket-detail:focus-visible { box-shadow: var(--focus-ring); border-radius: 6px; }
.head-ticket { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
@media (max-width: 600px) {
  .head-sub { flex-wrap: nowrap; align-items: flex-start; }
  .head-sub .ticket-detail { flex: 1 1 0; align-items: flex-start; }
  .head-sub .ticket-status { padding-top: 3px; }
  .head-ticket { white-space: normal; overflow-wrap: anywhere; line-height: 1.35; }
}
.spacer { flex: 1; }
.bar-sep { width: 1px; height: 18px; margin: 0 4px; background: var(--line-2); }
.panel-head [aria-disabled="true"] { opacity: .35; cursor: not-allowed; }
.stop:not([aria-disabled="true"]):hover { color: var(--danger); }
.scroll { flex: 1; min-height: 0; overflow: hidden auto; overscroll-behavior: contain; padding: 18px 24px 24px; }
.now-block { display: grid; gap: 6px; padding: 0 0 18px; border-bottom: 1px solid var(--line); }
.now-step { font-size: 19px; line-height: 1.3; color: var(--ink); overflow-wrap: anywhere; }
.now-meta { font-size: 12px; color: var(--ink-2); }
.now-listen { display: flex; min-width: 0; }
.activity-timeline { display: grid; gap: 0; margin: 10px 0 0; padding: 0; list-style: none; }
.activity-timeline li { display: grid; grid-template-columns: 65px minmax(0, 1fr); gap: 10px; align-items: baseline; padding: 8px 0; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink); }
.activity-timeline time { color: var(--ink-3); font-size: 11px; white-space: nowrap; }
.activity-timeline span { overflow-wrap: anywhere; }
.metadata-history { display: grid; gap: 0; margin: 12px 0 0; padding: 0; list-style: none; }
.metadata-history li { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 10px; align-items: baseline; padding: 8px 0; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink); }
.metadata-history time { color: var(--ink-3); font-size: 11px; white-space: nowrap; }
.metadata-history span { overflow-wrap: anywhere; }
.ticket-status { flex: none; color: var(--ink-3); font-size: 12px; white-space: nowrap; }
.callout { display: flex; align-items: center; gap: 12px; margin-bottom: 18px; padding: 12px 12px 12px 14px; border-radius: 12px; background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .35); color: var(--gold-ink); }
.callout-text { display: grid; flex: 1; min-width: 0; font-size: 12.5px; color: var(--ink-2); }
.callout-text strong { color: var(--ink); font-size: 13px; }
.facts { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); align-items: start; gap: 12px 20px; margin: 0; }
.fact { display: grid; grid-template-columns: minmax(0, 1fr); gap: 3px; min-width: 0; }
.fact.wide { grid-column: 1 / -1; }
.fact dt { font-size: 12px; line-height: 1.4; color: var(--ink-3); }
.fact dd { margin: 0; font-size: 13px; color: var(--ink); overflow-wrap: anywhere; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.fact dd.mono, .fact dd .mono { font-size: 12px; font-family: var(--mono); font-variant-ligatures: none; }
.commits { display: grid; gap: 5px; margin: 0; padding: 0; list-style: none; }
.commits li { display: flex; gap: 8px; flex-wrap: wrap; }
.commits code { font: 12px var(--mono); color: var(--ink-2); }
.muted { color: var(--ink-3); }
.evidence { display: inline-grid; place-items: center; width: 16px; height: 16px; border-radius: 50%; background: var(--chip-teal-bg); color: var(--teal-ink); }
.project-link { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; color: var(--ink); text-decoration: none; }
.project-link:hover { color: var(--teal-ink); }
.ticket-chip { flex-shrink: 0; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 11.5px/1 var(--mono); font-variant-ligatures: none; }
.block { margin-top: 24px; }
.block.first { margin-top: 18px; }
.block h3 { margin-bottom: 10px; }
.telemetry { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; }
.metric { display: grid; gap: 4px; padding: 10px 12px; border-radius: 10px; background: var(--code-bg); }
.metric-label { font-size: 11.5px; color: var(--ink-2); }
.metric b { font: 600 15px/1.2 var(--mono); color: var(--ink); font-variant-numeric: tabular-nums; }
.run-chip { justify-self: start; display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; font: 600 10.5px/1 var(--mono); letter-spacing: .04em; font-variant-ligatures: none; background: var(--chip-bg); color: var(--ink-2); box-shadow: inset 0 0 0 1px var(--chip-line); }
.run-chip.ok { background: rgba(47, 122, 90, .1); color: color-mix(in oklab, var(--ok), var(--ink) 28%); box-shadow: inset 0 0 0 1px rgba(47, 122, 90, .3); }
.run-chip.busy { background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.run-chip.bad { background: var(--danger-bg); color: color-mix(in oklab, var(--danger), var(--ink) 28%); box-shadow: inset 0 0 0 1px var(--danger-line); }
.empty-line { font-size: 13px; color: var(--ink-3); }
.runs { display: grid; grid-template-columns: minmax(0, 1fr); }
.run-row { display: grid; grid-template-columns: 92px minmax(0, 1fr) 48px 56px 68px; align-items: center; gap: 10px; min-height: 36px; border-bottom: 1px solid var(--line); font-size: 12.5px; }
.run-head { min-height: 24px; font: 500 9.5px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.run-row:last-child { border-bottom: 0; }
.run-model { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--ink); }
.run-tokens, .run-duration { font-size: 12px; color: var(--ink-2); text-align: right; font-variant-numeric: tabular-nums; }
.run-when { font-size: 12px; color: var(--ink-3); text-align: right; white-space: nowrap; }
.run-head .run-tokens, .run-head .run-duration, .run-head .run-when { font: inherit; color: inherit; }
.gone { display: grid; justify-items: center; gap: 8px; padding: 56px 16px; text-align: center; }
.gone h2 { font-size: 17px; }
.gone p { font-size: 13.5px; color: var(--ink-2); }
.gone .btn { margin-top: 8px; }
.gone-icon { display: grid; place-items: center; width: 44px; height: 44px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.sk { display: grid; gap: 14px; }
.sk .w40 { width: 40%; height: 18px; } .sk .w70 { width: 70%; } .sk .w90 { width: 90%; } .sk .w60 { width: 60%; }
@media (max-width: 720px) {
  /* The sheet follows the visual viewport, so the keyboard never covers the composer. */
  .session-panel { z-index: 40; inset: var(--vv-top, 0px) 0 auto 0; width: auto; height: var(--vv-h, 100dvh); border-radius: 0; border: 0; background: var(--canvas); }
  .panel-head { padding: calc(6px + env(safe-area-inset-top)) 8px 10px 16px; }
  .head-actions { flex-wrap: wrap; }
  .head-actions .spacer { flex-basis: 100%; height: 0; }
  /* Up to four quiet controls share one row on phones. */
  .head-actions .btn { flex: 1 1 0; min-width: 0; min-height: 44px; padding-inline: 4px; }
  .head-top .icon-btn { width: 40px; height: 40px; }
  /* While typing (keyboard open) the thread gets the room: controls and ticket step aside. */
  .session-panel:has(#session-panel-messages textarea:focus) .head-actions,
  .session-panel:has(#session-panel-messages textarea:focus) .head-sub,
  .session-panel:has(#session-panel-messages textarea:focus) .managed-controls { display: none; }
  .scroll { padding: 16px 18px 24px; }
  .telemetry { grid-template-columns: 1fr 1fr; }
  .run-row { grid-template-columns: 88px minmax(0, 1fr) 56px; }
  .run-tokens, .run-when { display: none; }
  @media (prefers-reduced-motion: no-preference) { .session-panel { animation-name: sheet-in; } @keyframes sheet-in { from { transform: translateY(24px); opacity: 0; } to { transform: none; opacity: 1; } } }
}
</style>
