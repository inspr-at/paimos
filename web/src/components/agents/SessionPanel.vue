<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { brand } from '../../lib/brand'
import { api, getNode } from '../../lib/api'
import { computed, onMounted, ref, watch } from 'vue'
import type { Approval, ProjectMessage, SessionControl } from '../../lib/agents'
import { RUN_OUTCOME, cost, elapsed, runDuration, runModel, scopeLabel, stopReasonLabel, tokens } from '../../lib/agentState'
import { absoluteTime, relativeTime, statusMeta } from '../../lib/work'
import { useAgents, type SessionView } from '../../stores/agents'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import Avatar from '../Avatar.vue'
import LiveDot from './LiveDot.vue'
import AgentGlyph from './AgentGlyph.vue'
import { activityOf, currentStep, type ActivitySession } from './activity'

// One session in the docked panel: who and where, the bound ticket, recent runs with
// outcome and duration, telemetry, and the message thread with a composer.
const props = defineProps<{ view: SessionView | undefined; loading: boolean; now: number; canWrite: boolean; controlBlock: (view: SessionView, kind: SessionControl['kind']) => string }>()
const emit = defineEmits<{ close: []; control: [view: SessionView, kind: SessionControl['kind']]; review: [approval: Approval] }>()
const agents = useAgents()
const session = useSession()
const root = ref<HTMLElement>()
const draft = ref('')
const level = ref<'simple' | 'steer'>('simple')
const replyTo = ref<ProjectMessage | null>(null)
const sending = ref(false)
const sendError = ref('')
const thread = ref<HTMLElement>()
const composeInput = ref<HTMLTextAreaElement>()
const detail = ref<(ActivitySession & { id: string }) | null>(null)
const ticketState = ref('')

const s = computed(() => props.view?.session)
const me = computed(() => session.identity?.principal.id ?? '')
const pending = computed(() => s.value ? agents.pending.filter(a => a.agent_principal_id === s.value!.agent_principal_id) : [])
const recentRuns = computed(() => s.value ? agents.recentRuns(s.value.agent_principal_id).slice(0, 8) : [])
const run = computed(() => props.view?.run)
const messages = computed(() => s.value ? agents.thread(s.value).slice(-40) : [])
const address = computed(() => s.value ? agents.addressOf(s.value.agent_principal_id) : '')
const activity = computed(() => detail.value?.id === s.value?.id ? detail.value : props.view ? activityOf(props.view) : null)
const timeline = computed(() => activity.value?.activity_history ?? [])
const step = computed(() => props.view ? activity.value?.activity_note || currentStep(props.view) : '')
const meta = computed(() => props.view ? [props.view.account, props.view.model].filter(Boolean).join(' · ') : '')
watch(() => [s.value?.id, props.view ? activityOf(props.view).activity_note : ''], async () => {
  const current = s.value
  if (!current) return
  try {
    const response = await api(`/projects/${encodeURIComponent(current.project_id)}/harness-sessions/${encodeURIComponent(current.id)}`)
    if (response.ok) {
      const body = await response.json() as ActivitySession & { id: string }
      if (s.value?.id === current.id) detail.value = body
    }
  } catch { /* The list still shows the latest step if detail is unavailable. */ }
}, { immediate: true })
watch(() => props.view?.ticket?.id, async id => {
  ticketState.value = ''
  if (!id) return
  try { const node = await getNode(id); if (props.view?.ticket?.id === id) ticketState.value = statusMeta(node.state).label } catch { /* Ticket summary remains useful. */ }
}, { immediate: true })
const composeBlock = computed(() => {
  if (!s.value) return ''
  if (agents.messagingState === 'error') return 'Messages could not be loaded right now. Close and reopen the session to try again.'
  if (agents.messagingState === 'forbidden') return 'Messages are open to workspace admins.'
  if (!address.value) return `${props.view!.name} has no message address yet. It gets one when it registers a message target.`
  if (s.value.phase === 'stopped') return 'This session has stopped. Messages reach the agent’s next session.'
  return ''
})
const authorOf = (m: ProjectMessage) => m.sender_principal_id === me.value ? 'You' : m.sender_principal_id === s.value?.agent_principal_id ? props.view!.name : agents.askerName(m.sender_principal_id).name
const fromAgent = (m: ProjectMessage) => m.sender_principal_id === s.value?.agent_principal_id

watch(() => s.value?.id, async id => {
  if (!id || !s.value) return
  draft.value = ''; replyTo.value = null; sendError.value = ''
  thread.value?.scrollTo({ top: 0 })
  await Promise.all([agents.refreshAgentRuns(s.value.agent_principal_id), agents.refreshThread(s.value.project_id)])
}, { immediate: true })
onMounted(() => root.value?.focus({ preventScroll: true }))

async function send() {
  if (!s.value || !draft.value.trim() || sending.value || composeBlock.value) return
  sending.value = true; sendError.value = ''
  try {
    await agents.send(s.value, address.value, draft.value.trim(), level.value, replyTo.value?.id)
    draft.value = ''; replyTo.value = null
  } catch (e) { sendError.value = e instanceof Error ? e.message : 'The message was not sent. Please try again.' }
  finally { sending.value = false }
}
function composerKeys(event: KeyboardEvent) {
  if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) { event.preventDefault(); void send() }
}
function control(kind: SessionControl['kind']) { if (props.view && !props.controlBlock(props.view, kind)) emit('control', props.view, kind) }
const workShape: Record<string, string> = { ship: 'Ship: building a change', scout: 'Scout: investigating', unknown: '' }
function focusComposer() { composeInput.value?.focus() }
defineExpose({ focus: () => root.value?.focus({ preventScroll: true }) })
</script>

<template>
  <aside ref="root" class="session-panel" aria-label="Session details" tabindex="-1">
    <!-- The identity stays in view while runs and messages scroll below it. -->
    <header class="panel-head">
      <div class="head-top">
        <template v-if="view && !loading">
          <AgentGlyph :id="view.session.agent_principal_id" :size="36" :lead="view.session.role === 'coordinator'" />
          <h2 class="name">{{ view.name }}</h2>
          <LiveDot :tone="view.status.tone" />
          <span class="state-text" :class="view.status.group">{{ view.status.label }}</span>
        </template>
        <span class="spacer" />
        <button type="button" class="icon-btn sm flat" aria-label="Close session details" aria-keyshortcuts="Escape" data-tip="Close · Esc" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
      </div>
      <div v-if="view && !loading" class="head-actions">
        <span class="harness">{{ view.harness }}</span>
        <span v-if="view.session.host" class="host-meta">on {{ view.session.host }}</span>
        <span class="spacer" />
        <button v-if="address && canWrite" type="button" class="btn sm" @click="focusComposer"><AppIcon name="inbox" :size="14" />Message</button>
        <template v-if="view.status.group !== 'stopped'">
          <button type="button" class="btn sm" :aria-disabled="!!controlBlock(view, 'interrupt')" :data-tip="controlBlock(view, 'interrupt') || 'Stop the current turn'" @click="control('interrupt')"><AppIcon name="interrupt" :size="14" />Interrupt</button>
          <button type="button" class="btn sm stop" :aria-disabled="!!controlBlock(view, 'stop')" :data-tip="controlBlock(view, 'stop') || 'End this session'" @click="control('stop')"><AppIcon name="halt" :size="14" />Stop</button>
        </template>
      </div>
      <p v-if="view && !loading" class="head-sub">
        <RouterLink v-if="view.ticket" class="ticket-chip" :to="view.ticket.href" :aria-label="`Ticket ${view.ticket.key}: ${view.ticket.title}`">{{ view.ticket.key }}</RouterLink>
        <span v-if="view.ticket" class="head-ticket">{{ view.ticket.title }}</span>
        <span v-if="meta" class="head-account"><AppIcon name="gauge" :size="12" />{{ meta }}</span>
      </p>
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

    <div v-else ref="thread" class="scroll">
      <div v-if="pending.length" class="callout" role="note">
        <AppIcon name="shield" :size="15" />
        <div class="callout-text">
          <strong>{{ pending.length === 1 ? 'Waiting for your permission' : `${pending.length} requests wait for you` }}</strong>
          <span>{{ scopeLabel(pending[0].scope) }}</span>
        </div>
        <button type="button" class="btn sm primary" @click="emit('review', pending[0])">Review</button>
      </div>

      <section class="now-block" aria-labelledby="now-title">
        <h3 id="now-title" class="eyebrow">Now</h3>
        <strong class="now-step">{{ step }}</strong>
        <p class="now-meta">Started {{ absoluteTime(view.session.created_at) }} · active {{ elapsed(view.session, now) }}</p>
        <ol v-if="timeline.length" class="activity-timeline" aria-label="Recent activity">
          <li v-for="(item, index) in timeline.slice(0, 6)" :key="`${item.at}-${index}`"><time :datetime="item.at">{{ relativeTime(item.at, { now }) }}</time><span>{{ item.note }}</span></li>
        </ol>
      </section>

      <section v-if="view.ticket" class="ticket-card" aria-labelledby="ticket-title">
        <h3 id="ticket-title" class="eyebrow">Bound ticket</h3>
        <RouterLink :to="view.ticket.href" class="ticket-detail"><span class="ticket-chip">{{ view.ticket.key }}</span><strong>{{ view.ticket.title }}</strong><span v-if="ticketState" class="ticket-status">{{ ticketState }}</span></RouterLink>
      </section>

      <dl class="facts">
        <div class="fact"><dt>Project</dt><dd>
          <RouterLink v-if="view.projectKey" class="project-link" :to="`/p/${encodeURIComponent(view.projectKey)}`"><span class="key-badge">{{ view.projectKey }}</span>{{ view.projectTitle }}</RouterLink>
          <span v-else class="muted">Workspace</span>
        </dd></div>
        <div class="fact"><dt>Runs on</dt><dd><span class="mono">{{ view.session.host }}</span><span class="muted">{{ view.session.role === 'coordinator' ? 'lead session' : 'worker' }} · {{ view.session.management_mode === 'managed' ? `owned by ${brand.short_name}` : 'runs on its own' }}</span></dd></div>
        <div v-if="view.model" class="fact"><dt>Model</dt><dd class="mono">{{ view.model }}<span v-if="run && run.model_evidence === 'vendor_reported'" class="evidence" data-tip="Reported by the vendor, not only requested"><AppIcon name="check" :size="11" /></span></dd></div>
        <div class="fact"><dt>Heartbeat</dt><dd>
          <time v-if="view.session.heartbeat_at" :datetime="view.session.heartbeat_at" :data-tip="absoluteTime(view.session.heartbeat_at)">{{ relativeTime(view.session.heartbeat_at, { now, long: true }) }}</time>
          <span v-else class="muted">Never</span>
        </dd></div>
        <div class="fact"><dt>{{ view.session.stopped_at ? 'Ran for' : 'Running' }}</dt><dd><span :data-tip="`Since ${absoluteTime(view.session.created_at)}`">{{ elapsed(view.session, now) }}</span></dd></div>
        <div v-if="workShape[view.session.work_shape]" class="fact"><dt>Work</dt><dd>{{ workShape[view.session.work_shape] }}</dd></div>
        <div v-if="view.session.stopped_at" class="fact"><dt>Stopped</dt><dd>{{ stopReasonLabel(view.session.stop_reason) || 'Stopped' }} <span class="muted">{{ relativeTime(view.session.stopped_at, { now }) }}</span></dd></div>
      </dl>

      <section v-if="run" class="block" aria-labelledby="telemetry-title">
        <h3 id="telemetry-title" class="eyebrow">Current run</h3>
        <div class="telemetry">
          <div class="metric"><span class="metric-label">Status</span><span class="run-chip" :class="RUN_OUTCOME[run.status].tone">{{ RUN_OUTCOME[run.status].label }}</span></div>
          <div class="metric"><span class="metric-label">Tokens in</span><b>{{ tokens(run.input_tokens) }}</b></div>
          <div class="metric"><span class="metric-label">Tokens out</span><b>{{ tokens(run.output_tokens) }}</b></div>
          <div class="metric"><span class="metric-label">Cost</span><b>{{ cost(run.cost_micros) }}</b></div>
        </div>
      </section>

      <section v-if="view.session.management_mode === 'managed' && recentRuns.length" class="block" aria-labelledby="runs-title">
        <h3 id="runs-title" class="eyebrow">History</h3>
        <div class="runs" role="table" aria-label="Recent runs">
          <div class="run-row run-head" role="row">
            <span role="columnheader">Outcome</span><span role="columnheader">Run</span><span role="columnheader" class="run-tokens">Tokens</span>
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

      <section class="block" aria-labelledby="messages-title">
        <h3 id="messages-title" class="eyebrow">Messages</h3>
        <p v-if="!messages.length && address" class="empty-line">No messages yet. Use the composer to contact {{ view.name }}.</p>
        <p v-else-if="!address" class="empty-line">Messages start when this agent registers a target. <RouterLink to="/settings/access/agents">Agent setup</RouterLink></p>
        <ol v-else class="thread" aria-label="Messages">
          <li v-for="m in messages" :key="m.id" class="msg" :class="{ theirs: fromAgent(m), mine: m.sender_principal_id === me }">
            <p class="msg-meta">
              <Avatar v-if="m.sender_principal_id === me" :id="me" :name="session.identity?.principal.name ?? 'You'" :size="18" />
              <Avatar v-else :name="authorOf(m)" kind="agent" :size="18" />
              <span class="msg-author">{{ authorOf(m) }}</span>
              <span v-if="!fromAgent(m) && m.sender_principal_id !== me" class="muted">to {{ m.to }}</span>
              <span v-if="m.delivery_level === 'steer'" class="msg-chip steer"><AppIcon name="bolt" :size="10" />Steer</span>
              <span v-if="m.is_action_request" class="msg-chip held">Action request</span>
              <span v-if="m.reply_obligation === 'open'" class="msg-chip open">Awaiting reply</span>
              <span v-if="m.human_resolution_outcome" class="msg-chip">{{ m.human_resolution_outcome === 'resolved' ? 'Resolved' : 'Dismissed' }}</span>
              <time v-if="m.created_at" class="msg-time" :datetime="m.created_at" :data-tip="absoluteTime(m.created_at)">{{ relativeTime(m.created_at, { now }) }}</time>
            </p>
            <p class="msg-body">{{ m.body }}</p>
            <button v-if="fromAgent(m) && canWrite && !composeBlock" type="button" class="reply" @click="replyTo = m">Reply</button>
          </li>
        </ol>
      </section>
    </div>

    <footer v-if="view && !loading && address" class="composer">
      <p v-if="composeBlock" class="compose-block"><AppIcon name="inbox" :size="13" />{{ composeBlock }}</p>
      <form v-else class="compose" @submit.prevent="send">
        <p v-if="replyTo" class="replying"><span>Replying to “{{ replyTo.body.slice(0, 80) }}{{ replyTo.body.length > 80 ? '…' : '' }}”</span><button type="button" class="icon-btn sm flat" aria-label="Cancel the reply" @click="replyTo = null"><AppIcon name="close" :size="12" /></button></p>
        <label class="sr-only" :for="`compose-${view.session.id}`">Message to {{ view.name }}</label>
        <textarea :id="`compose-${view.session.id}`" ref="composeInput" v-model="draft" class="field" rows="2" :placeholder="`Message ${view.name}…`" :disabled="!canWrite || sending" @keydown="composerKeys" />
        <p v-if="sendError" class="send-error" role="alert"><AppIcon name="alert" :size="12" />{{ sendError }}</p>
        <div class="compose-row">
          <div class="seg level" role="radiogroup" aria-label="Delivery">
            <button type="button" role="radio" :aria-checked="level === 'simple'" data-tip="Waits until the agent reads its inbox" @click="level = 'simple'">Simple</button>
            <button type="button" role="radio" :aria-checked="level === 'steer'" data-tip="Reaches the agent during its current turn" @click="level = 'steer'"><AppIcon name="bolt" :size="11" />Steer</button>
          </div>
          <span class="compose-hint" aria-hidden="true"><KeyCap k="mod" /><KeyCap k="enter" /></span>
          <button type="submit" class="btn sm primary" :disabled="!draft.trim() || sending || !canWrite"><AppIcon name="send" :size="13" />{{ sending ? 'Sending…' : 'Send' }}</button>
        </div>
      </form>
    </footer>
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
.head-actions { display: flex; align-items: center; gap: 6px; min-width: 0; margin-top: 8px; }
.head-actions .btn { display: inline-flex; align-items: center; justify-content: center; gap: 5px; white-space: nowrap; }
.host-meta { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font: 11px var(--mono); }
.name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 18px; font-weight: 650; letter-spacing: -.01em; }
.state-text { flex-shrink: 0; font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.state-text.needs { color: var(--gold-ink); }
.head-sub { display: flex; align-items: center; gap: 8px; min-width: 0; margin-top: 4px; padding-right: 8px; font-size: 12.5px; color: var(--ink-2); }
.head-ticket { min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-weight: 600; }
.head-account { flex-shrink: 0; display: inline-flex; align-items: center; gap: 5px; margin-left: auto; padding-left: 10px; box-shadow: inset 1px 0 0 var(--line-2); }
.head-account svg { color: var(--ink-3); }
@media (max-width: 600px) {
  .head-sub { flex-wrap: wrap; row-gap: 4px; }
  .head-ticket { flex-basis: calc(100% - 80px); white-space: normal; overflow: visible; overflow-wrap: anywhere; }
  .head-account { margin-left: 0; padding-left: 0; box-shadow: none; }
}
.spacer { flex: 1; }
.bar-sep { width: 1px; height: 18px; margin: 0 4px; background: var(--line-2); }
.panel-head [aria-disabled="true"] { opacity: .35; cursor: not-allowed; }
.stop:not([aria-disabled="true"]):hover { color: var(--danger); }
.scroll { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; padding: 18px 24px 24px; }
.now-block, .ticket-card { display: grid; gap: 9px; padding: 0 0 18px; margin-bottom: 18px; border-bottom: 1px solid var(--line); }
.now-step { font-size: 19px; line-height: 1.3; color: var(--ink); overflow-wrap: anywhere; }
.now-meta { font-size: 12px; color: var(--ink-2); }
.activity-timeline { display: grid; gap: 0; margin: 10px 0 0; padding: 0; list-style: none; }
.activity-timeline li { display: grid; grid-template-columns: 65px minmax(0, 1fr); gap: 10px; align-items: baseline; padding: 8px 0; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink); }
.activity-timeline time { color: var(--ink-3); font-size: 11px; white-space: nowrap; }
.activity-timeline span { overflow-wrap: anywhere; }
.ticket-detail { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; min-width: 0; color: var(--ink); text-decoration: none; }
.ticket-detail strong { min-width: 0; flex: 1 1 160px; font-size: 13px; overflow-wrap: anywhere; }
.ticket-status { padding: 4px 8px; border-radius: 999px; background: var(--chip-bg); color: var(--ink-2); font-size: 11px; white-space: nowrap; }
.harness { flex-shrink: 0; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 7px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font: 500 11px/1 var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
.callout { display: flex; align-items: center; gap: 12px; margin-bottom: 18px; padding: 12px 12px 12px 14px; border-radius: 12px; background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .35); color: var(--gold-ink); }
.callout-text { display: grid; flex: 1; min-width: 0; font-size: 12.5px; color: var(--ink-2); }
.callout-text strong { color: var(--ink); font-size: 13px; }
.facts { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); align-items: start; gap: 12px 20px; margin: 0; }
.fact { display: grid; grid-template-columns: minmax(0, 1fr); gap: 3px; min-width: 0; }
.fact.wide { grid-column: 1 / -1; }
.fact dt { font: 500 10px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.fact dd { margin: 0; font-size: 13px; color: var(--ink); overflow-wrap: anywhere; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.fact dd.mono, .fact dd .mono { font-size: 12px; font-family: var(--mono); font-variant-ligatures: none; }
.muted { color: var(--ink-3); }
.evidence { display: inline-grid; place-items: center; width: 16px; height: 16px; border-radius: 50%; background: var(--chip-teal-bg); color: var(--teal-ink); }
.project-link { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; color: var(--ink); text-decoration: none; }
.project-link:hover { color: var(--teal-ink); }
.ticket-chip { flex-shrink: 0; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 11.5px/1 var(--mono); font-variant-ligatures: none; }
.block { margin-top: 26px; }
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
.thread { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; margin: 0; padding: 0; list-style: none; }
.msg { position: relative; max-width: 88%; padding: 10px 12px; border-radius: 12px 12px 12px 4px; background: var(--comment-bg, var(--code-bg)); box-shadow: inset 0 0 0 1px var(--line); }
.msg.mine { justify-self: end; border-radius: 12px 12px 4px 12px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.msg-meta { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-bottom: 4px; font-size: 11.5px; }
.msg-author { font-weight: 650; color: var(--ink); }
.msg-time { margin-left: auto; padding-left: 8px; font-size: 11px; color: var(--ink-3); white-space: nowrap; }
.msg-chip { display: inline-flex; align-items: center; gap: 3px; height: 17px; padding: 0 6px; border-radius: 999px; font: 600 9.5px/1 var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; background: var(--chip-bg); color: var(--ink-2); }
.msg-chip.steer { background: var(--gold-wash); color: var(--gold-ink); }
.msg-chip.open { background: var(--chip-teal-bg); color: var(--teal-ink); }
.msg-body { font-size: 13.5px; line-height: 1.5; color: var(--ink); white-space: pre-wrap; overflow-wrap: anywhere; }
.reply { margin-top: 6px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; }
.reply:hover { text-decoration: underline; }
.reply:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
.composer { flex-shrink: 0; padding: 10px 14px 12px; border-top: 1px solid var(--line); background: var(--surface-raised-2); border-radius: 0 0 var(--radius) var(--radius); }
.compose { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; }
.compose textarea { width: 100%; min-height: 56px; max-height: 180px; resize: vertical; padding: 9px 11px; font: inherit; font-size: 13.5px; line-height: 1.45; }
.compose-row { display: flex; align-items: center; gap: 10px; }
.level button { display: inline-flex; align-items: center; gap: 4px; }
.compose-hint { margin-left: auto; display: inline-flex; gap: 2px; }
.compose-block { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--ink-2); padding: 6px 2px; }
.replying { display: flex; align-items: center; gap: 6px; min-width: 0; padding: 4px 4px 4px 10px; border-radius: 8px; background: var(--code-bg); font-size: 12px; color: var(--ink-2); }
.replying span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.send-error { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--danger); }
.gone { display: grid; justify-items: center; gap: 8px; padding: 56px 16px; text-align: center; }
.gone h2 { font-size: 17px; }
.gone p { font-size: 13.5px; color: var(--ink-2); }
.gone .btn { margin-top: 8px; }
.gone-icon { display: grid; place-items: center; width: 44px; height: 44px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.sk { display: grid; gap: 14px; }
.sk .w40 { width: 40%; height: 18px; } .sk .w70 { width: 70%; } .sk .w90 { width: 90%; } .sk .w60 { width: 60%; }
@media (max-width: 720px) {
  .session-panel { z-index: 40; inset: 0; width: auto; height: 100dvh; border-radius: 0; border: 0; background: var(--canvas); }
  .panel-head { padding: 6px 8px 10px 16px; }
  .head-actions { flex-wrap: wrap; }
  .head-actions .spacer { flex-basis: 100%; height: 0; }
  .head-actions .btn { flex: 1; }
  .head-top .icon-btn { width: 40px; height: 40px; }
  .head-sub { flex-wrap: wrap; row-gap: 4px; }
  .head-account { margin-left: 0; padding-left: 0; box-shadow: none; width: 100%; }
  .scroll { padding: 16px 18px 24px; }
  .telemetry { grid-template-columns: 1fr 1fr; }
  .run-row { grid-template-columns: 88px minmax(0, 1fr) 56px; }
  .run-tokens, .run-when { display: none; }
  .composer { border-radius: 0; padding: 8px 12px calc(8px + env(safe-area-inset-bottom)); background: var(--surface-raised); }
  .compose-hint { display: none; }
  .compose-row .btn { margin-left: auto; height: 40px; }
  @media (prefers-reduced-motion: no-preference) { .session-panel { animation-name: sheet-in; } @keyframes sheet-in { from { transform: translateY(24px); opacity: 0; } to { transform: none; opacity: 1; } } }
}
</style>
