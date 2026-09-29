<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import type { Approval, ProjectMessage } from '../../lib/agents'
import { RISK_LABEL, expiresIn, expiresSoon, riskFor, scopeLabel, type Asker, type Resource } from '../../lib/agentState'
import { confirmAction } from '../../lib/confirm'
import { relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import TargetSummary from '../deploy/TargetSummary.vue'
import HarnessMark from './HarnessMark.vue'
import AgentStateMark from '../indicators/AgentStateMark.vue'
import { useAgentAppearance } from '../../lib/agentAppearance'
const { appearance } = useAgentAppearance()

// What waits on Markus: permission requests (approve or deny, with a reason the agent
// sees) and held action requests. Decided and expired requests fold into a history.
type Held = ProjectMessage & { projectId: string }
const props = defineProps<{
  pending: Approval[]; held: Held[]; history: Approval[]; now: number; cursor: string; canDecide: boolean; canDecideApproval: (approval: Approval) => boolean; canResolve: boolean; canRevoke: boolean; loaded: boolean
  asker: (principalId: string, fallbackName?: string | null) => Asker; resource: (approval: Approval) => Resource
  decide: (approval: Approval, decision: 'approved' | 'denied', reason: string) => Promise<void>
  revoke: (approval: Approval) => Promise<void>
  resolve: (request: Held, decision: 'resolved' | 'dismissed', note: string) => Promise<void>
}>()
const emit = defineEmits<{ focusRow: [id: string]; openAgent: [principalId: string] }>()

type Mode = 'approve' | 'deny' | 'resolve' | 'dismiss'
const open = ref<{ id: string; mode: Mode } | null>(null)
const reason = ref('')
const busy = ref(false)
const error = ref('')
const showHistory = ref(false)
const revoked = ref(new Set<string>())
const reasonField = ref<HTMLTextAreaElement[]>()

async function begin(id: string, mode: Mode) {
  if (!props.canDecide || busy.value) return
  const approval = props.pending.find(item => item.id === id)
  if (approval && !props.canDecideApproval(approval)) return
  if (!approval && !props.canResolve) return
  if (open.value?.id !== id) { reason.value = ''; error.value = '' }
  open.value = { id, mode }
  emit('focusRow', `${mode === 'resolve' || mode === 'dismiss' ? 'm' : 'a'}:${id}`)
  await nextTick()
  reasonField.value?.[0]?.focus()
}
function cancel() {
  const current = open.value
  open.value = null; reason.value = ''; error.value = ''
  if (current) void nextTick(() => document.querySelector<HTMLElement>(`[data-row="${current.mode === 'resolve' || current.mode === 'dismiss' ? 'm' : 'a'}:${current.id}"]`)?.focus())
}
async function submit(approval: Approval) {
  if (!open.value || busy.value || !props.canDecideApproval(approval)) return
  busy.value = true; error.value = ''
  try {
    await props.decide(approval, open.value.mode === 'approve' ? 'approved' : 'denied', reason.value.trim())
    open.value = null; reason.value = ''
  } catch (e) { error.value = e instanceof Error ? e.message : 'The decision was not recorded. Please try again.' }
  finally { busy.value = false }
}
async function settle(request: Held) {
  if (!open.value || busy.value) return
  busy.value = true; error.value = ''
  try {
    await props.resolve(request, open.value.mode === 'dismiss' ? 'dismissed' : 'resolved', reason.value.trim())
    open.value = null; reason.value = ''
  } catch (e) { error.value = e instanceof Error ? e.message : 'Your answer was not recorded. Please try again.' }
  finally { busy.value = false }
}
function noteKeys(event: KeyboardEvent, request: Held) {
  if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); void settle(request) }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); cancel() }
}
function reasonKeys(event: KeyboardEvent, approval: Approval) {
  if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); void submit(approval) }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); cancel() }
}
const named = (approval: Approval) => props.asker(approval.agent_principal_id, approval.agent_name)
async function revoke(approval: Approval) {
  const ok = await confirmAction({ title: 'Revoke this permission?', body: `${named(approval).name} loses “${scopeLabel(approval.scope)}” right away. Work it already started is not undone.`, confirmLabel: 'Revoke', danger: true })
  if (!ok) return
  try { await props.revoke(approval); revoked.value = new Set([...revoked.value, approval.id]) }
  catch (e) { error.value = e instanceof Error ? e.message : 'Revoking did not work. Please try again.' }
}
const outcome = (approval: Approval) => revoked.value.has(approval.id) ? 'Revoked' : approval.decision === 'approved' ? 'Approved' : approval.decision === 'denied' ? 'Denied' : 'Expired'
const count = computed(() => props.pending.length + props.held.length)
// The asker's harness shows as its mark; the address label maps back to the key.
const harnessOf = (label: string) => label.toLowerCase()
defineExpose({ begin, cancel, isOpen: () => !!open.value })
</script>

<template>
  <section class="queue glass-card" :class="{ clear: loaded && !count }" :style="appearance('waiting')" aria-labelledby="needs-title">
    <header class="card-head">
      <h2 id="needs-title">Needs you</h2>
      <span v-if="count" class="count-badge">{{ count }}</span>
      <p v-else-if="loaded" class="all-clear"><AppIcon name="check" :size="14" />Nothing waits on you</p>
    </header>

    <div v-if="!loaded" class="skeleton-rows" role="status" aria-label="Loading requests">
      <div v-for="i in 2" :key="i" class="sk-row"><span class="skeleton mark-sk" /><span class="sk-lines"><span class="skeleton" :style="{ width: `${34 + i * 9}%` }" /><span class="skeleton" style="width: 22%" /></span></div>
    </div>

    <ul v-else-if="count" class="items" aria-label="Requests waiting for you">
      <li
        v-for="approval in pending" :key="approval.id" class="item agent-state-surface" :class="[riskFor(approval), { active: cursor === `a:${approval.id}`, open: open?.id === approval.id }]"
        :data-row="`a:${approval.id}`" tabindex="-1" :aria-label="`${scopeLabel(approval.scope)}, asked by ${named(approval).name}`"
        @click="emit('focusRow', `a:${approval.id}`)" @focusin="emit('focusRow', `a:${approval.id}`)"
      >
        <span class="mark"><AgentStateMark state="waiting" :size="18" /></span>
        <div class="body">
          <p class="line1">
            <strong class="what" :title="approval.scope">{{ scopeLabel(approval.scope) }}</strong>
            <span class="meta">
              <time class="expiry" :class="{ soon: expiresSoon(approval, now) }" :datetime="approval.expires_at" :data-tip="new Date(approval.expires_at).toLocaleString()">{{ expiresIn(approval, now) }}</time>
              <span class="risk" :class="riskFor(approval)"><AppIcon v-if="riskFor(approval) === 'high'" name="alert" :size="12" />{{ RISK_LABEL[riskFor(approval)] }}</span>
            </span>
          </p>
          <p class="line2">
            <button type="button" class="who" :data-tip="named(approval).harness ? `${named(approval).harness} agent · open its session` : 'Open its session'" @click.stop="emit('openAgent', approval.agent_principal_id)">
              <HarnessMark v-if="named(approval).harness" class="who-mark" :harness="harnessOf(named(approval).harness)" :size="13" />
              <span v-else class="who-icon" aria-hidden="true"><AppIcon name="agent" :size="12" /></span>
              <span class="who-name">{{ named(approval).name }}</span>
            </button>
            <span class="phrase">
              <span class="asks">on</span>
              <RouterLink v-if="resource(approval).href && resource(approval).key" class="res-key" :to="resource(approval).href!" @click.stop>{{ resource(approval).key }}</RouterLink>
              <span v-else-if="resource(approval).key" class="res-key plain">{{ resource(approval).key }}</span>
              <RouterLink v-else-if="resource(approval).href" class="res-link" :to="resource(approval).href!" @click.stop>{{ resource(approval).label }}</RouterLink>
              <span v-else class="res-label">{{ resource(approval).label }}</span>
              <!-- The title follows a key; without a key the label already is the title. -->
              <span v-if="resource(approval).title && resource(approval).key" class="res-title" :title="resource(approval).title">{{ resource(approval).title }}</span>
            </span>
          </p>
          <TargetSummary :approval="approval" compact />
          <p v-if="approval.rationale" class="why">{{ approval.rationale }}</p>
          <form v-if="open?.id === approval.id" class="decision" @submit.prevent="submit(approval)" @click.stop>
            <label :for="`reason-${approval.id}`">{{ open.mode === 'approve' ? 'Reason (optional)' : 'Why not? The agent sees this.' }}</label>
            <textarea
              :id="`reason-${approval.id}`" ref="reasonField" v-model="reason" class="field" rows="2" maxlength="4000"
              :placeholder="open.mode === 'approve' ? 'Fine for this run.' : 'Use the staging account instead.'" :disabled="busy" @keydown="reasonKeys($event, approval)"
            />
            <p v-if="error" class="error" role="alert"><AppIcon name="alert" :size="13" />{{ error }}</p>
            <div class="decision-actions">
              <span class="hint"><kbd class="keycap"><AppIcon name="enter" /></kbd> to {{ open.mode }} · <kbd class="keycap">esc</kbd> to cancel</span>
              <button type="button" class="btn sm ghost" :disabled="busy" @click="cancel">Cancel</button>
              <button type="submit" class="btn sm" :class="open.mode === 'approve' ? 'primary' : 'deny'" :disabled="busy">
                <AppIcon :name="open.mode === 'approve' ? 'check' : 'close'" :size="13" />{{ busy ? 'Saving…' : open.mode === 'approve' ? 'Approve permission' : 'Deny permission' }}
              </button>
            </div>
          </form>
        </div>
        <div v-if="open?.id !== approval.id && canDecideApproval(approval)" class="row-actions">
          <button type="button" class="btn sm ghost" aria-keyshortcuts="d" @click.stop="begin(approval.id, 'deny')"><AppIcon name="close" :size="13" />Deny</button>
          <button type="button" class="btn sm" :class="cursor === `a:${approval.id}` ? 'primary' : 'approve-soft'" aria-keyshortcuts="a" @click.stop="begin(approval.id, 'approve')"><AppIcon name="check" :size="13" />Approve</button>
        </div>
      </li>
      <li
        v-for="request in held" :key="request.id" class="item held agent-state-surface" :class="{ active: cursor === `m:${request.id}`, open: open?.id === request.id }" :data-row="`m:${request.id}`" tabindex="-1"
        :aria-label="`Action request from ${asker(request.sender_principal_id).name}`" @click="emit('focusRow', `m:${request.id}`)" @focusin="emit('focusRow', `m:${request.id}`)"
      >
        <span class="mark"><AgentStateMark state="waiting" :size="18" /></span>
        <div class="body">
          <p class="line1"><strong class="what">Action request</strong><time v-if="request.created_at" class="expiry sent-at" :datetime="request.created_at">Held for you · {{ relativeTime(request.created_at, { now, long: true }) }}</time></p>
          <p class="line2">
            <button type="button" class="who" @click.stop="emit('openAgent', request.sender_principal_id)">
              <HarnessMark v-if="asker(request.sender_principal_id).harness" class="who-mark" :harness="harnessOf(asker(request.sender_principal_id).harness)" :size="13" />
              <span v-else class="who-icon" aria-hidden="true"><AppIcon name="agent" :size="12" /></span>
              <span class="who-name">{{ asker(request.sender_principal_id).name }}</span>
            </button>
            <span class="asks">to</span><span class="to" :title="request.to">{{ request.to.split(':').pop() }}</span>
          </p>
          <p class="why body-text">{{ request.body }}</p>
          <form v-if="open?.id === request.id" class="decision" @submit.prevent="settle(request)" @click.stop>
            <label :for="`note-${request.id}`">Note (optional)</label>
            <textarea
              :id="`note-${request.id}`" ref="reasonField" v-model="reason" class="field" rows="2" maxlength="8000"
              :placeholder="open.mode === 'resolve' ? 'Merged it myself after CI.' : 'Not needed; camy already has the lock.'" :disabled="busy" @keydown="noteKeys($event, request)"
            />
            <p class="fine-print">Resolving records your answer. The held message is not delivered.</p>
            <p v-if="error" class="error" role="alert"><AppIcon name="alert" :size="13" />{{ error }}</p>
            <div class="decision-actions">
              <span class="hint"><kbd class="keycap"><AppIcon name="enter" /></kbd> to {{ open.mode }} · <kbd class="keycap">esc</kbd> to cancel</span>
              <button type="button" class="btn sm ghost" :disabled="busy" @click="cancel">Cancel</button>
              <button type="submit" class="btn sm" :class="open.mode === 'resolve' ? 'primary' : ''" :disabled="busy">
                <AppIcon :name="open.mode === 'resolve' ? 'check' : 'close'" :size="13" />{{ busy ? 'Saving…' : open.mode === 'resolve' ? 'Mark resolved' : 'Dismiss request' }}
              </button>
            </div>
          </form>
        </div>
        <div v-if="open?.id !== request.id" class="row-actions">
          <button type="button" class="btn sm ghost answer" @click.stop="emit('openAgent', request.sender_principal_id)"><AppIcon name="send" :size="13" />Answer</button>
          <template v-if="canResolve">
            <button type="button" class="btn sm ghost" aria-keyshortcuts="d" @click.stop="begin(request.id, 'dismiss')"><AppIcon name="close" :size="13" />Dismiss</button>
            <button type="button" class="btn sm" :class="cursor === `m:${request.id}` ? 'primary' : 'approve-soft'" aria-keyshortcuts="a" @click.stop="begin(request.id, 'resolve')"><AppIcon name="check" :size="13" />Resolve</button>
          </template>
        </div>
      </li>
    </ul>

    <footer v-if="loaded && history.length" class="history">
      <button type="button" class="history-toggle" :aria-expanded="showHistory" aria-controls="approval-history" @click="showHistory = !showHistory">
        <AppIcon name="chevron-right" :size="12" class="chev" :class="{ turned: showHistory }" />Decided<span class="mono">{{ history.length }}</span>
      </button>
      <ul v-if="showHistory" id="approval-history" class="history-list">
        <li v-for="approval in history.slice(0, 20)" :key="approval.id" class="past" :class="outcome(approval).toLowerCase()">
          <AppIcon :name="outcome(approval) === 'Approved' ? 'check' : outcome(approval) === 'Expired' ? 'clock' : 'close'" :size="13" class="past-icon" />
          <span class="past-what">{{ scopeLabel(approval.scope) }}</span>
          <span class="past-who">{{ named(approval).name }}</span>
          <span class="past-outcome">{{ outcome(approval) }}</span>
          <time class="past-time" :datetime="approval.proposed_at">{{ relativeTime(approval.proposed_at, { now }) }}</time>
          <button v-if="approval.decision === 'approved' && !revoked.has(approval.id) && canRevoke" type="button" class="btn sm ghost revoke" @click="revoke(approval)">Revoke</button>
          <TargetSummary class="past-target" :approval="approval" compact />
        </li>
      </ul>
    </footer>
  </section>
</template>

<style scoped>
.queue { overflow: clip; container: queue / inline-size; }
.card-head { display: flex; align-items: center; gap: 10px; min-height: 48px; padding: 10px 18px 8px; }
.queue.clear .card-head { padding-bottom: 10px; }
.card-head h2 { font-size: 15px; font-weight: 650; }
.count-badge { display: inline-grid; place-items: center; min-width: 20px; height: 20px; padding: 0 6px; border-radius: 999px; background: var(--gold); color: #fff; font: 700 11px/1 var(--mono); font-variant-numeric: tabular-nums; }
.all-clear { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--ink-3); }
.skeleton-rows { display: grid; gap: 4px; padding: 0 8px 8px; }
.sk-row { display: flex; align-items: center; gap: 12px; padding: 12px 12px 12px 10px; }
.mark-sk { flex-shrink: 0; width: 30px; height: 30px; border-radius: 9px; }
.sk-lines { display: grid; gap: 8px; flex: 1; min-width: 0; }
.all-clear svg { color: var(--ok); }
.items { margin: 0; padding: 0 8px 8px; list-style: none; display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; }
.item { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; gap: 12px; align-items: start; padding: 12px 12px 12px 10px; border-radius: 12px; outline: none; cursor: default; }
@media (hover: hover) { .item:hover { background: var(--row-hover); } }
.item.active { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.item.open { background: var(--row-selected); }
.mark { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 9px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.item.medium .mark { background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .35); color: var(--gold-ink); }
.item.high .mark { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.item.held .mark { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.body { display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; min-width: 0; }
.line1 { display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 10px; min-height: 22px; }
.what { font-size: 14px; font-weight: 650; color: var(--ink); }
/* Risk is secondary: quiet words; only high risk earns colour, with an icon. */
.risk { display: inline-flex; align-items: center; gap: 4px; color: var(--ink-3); font-size: 12px; white-space: nowrap; }
.meta { display: inline-flex; align-items: baseline; gap: 8px; }
.risk::before { content: '·'; margin-right: 4px; color: var(--ink-3); }
.res-link { color: var(--ink); font-weight: 550; text-decoration: none; }
.res-link:hover { color: var(--teal-ink); text-decoration: underline; }
.risk.high { color: var(--danger); font-weight: 600; }
.expiry { font-size: 12px; color: var(--ink-3); white-space: nowrap; font-variant-numeric: tabular-nums; }
.expiry.soon { color: var(--gold-ink); font-weight: 600; }
.line2 { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 6px; font-size: 12.5px; color: var(--ink-2); }
.phrase { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 6px; min-width: 0; }
.who { display: inline-flex; align-items: center; gap: 5px; height: 24px; margin-left: -4px; padding: 0 6px 0 4px; border: 0; border-radius: 7px; background: transparent; color: var(--ink); font-size: 12.5px; font-weight: 600; }
.who-icon { display: inline-grid; place-items: center; width: 16px; height: 16px; border-radius: 999px; color: var(--ink-2); flex: none; }
.who-mark { color: var(--ink-2); }
.who-name { line-height: 1.3; }
.who:hover { background: var(--row-hover); color: var(--teal-ink); }
.who:focus-visible { box-shadow: var(--focus-ring); }
.to { color: var(--ink); font-weight: 550; }
/* Phones: who asks, what for and on what stack as three short lines; the asker and
   the resource are finger-sized chips a line apart, so their reach never meets. */
@media (max-width: 600px) {
  .who { z-index: 1; height: 32px; }
  .phrase { display: contents; }
}
.asks { color: var(--ink-3); }
.res-key { display: inline-flex; align-items: center; font: 600 11.5px/1 var(--mono); color: var(--teal-ink); text-decoration: none; padding: 3px 7px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); font-variant-ligatures: none; }
@media (max-width: 600px) { .res-key { z-index: 1; min-height: 28px; padding: 0 8px; } }
.res-key:hover { text-decoration: underline; }
.res-key.plain { color: var(--ink-2); background: var(--chip-bg); }
.res-key.plain:hover { text-decoration: none; }
.res-title { min-width: 0; max-width: 42ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); }
.res-label { color: var(--ink); }
.why { margin-top: 2px; font-size: 13px; color: var(--ink-2); line-height: 1.45; overflow: hidden; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; }
.body-text { -webkit-line-clamp: 3; line-clamp: 3; color: var(--ink); }
.row-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; align-self: center; min-width: 0; max-width: 100%; }
.decision { display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; margin-top: 8px; }
.decision label { font-size: 12px; font-weight: 600; color: var(--ink-2); }
.decision textarea { width: 100%; min-height: 58px; resize: vertical; padding: 8px 10px; font: inherit; font-size: 13.5px; line-height: 1.4; }
.decision-actions { display: flex; align-items: center; justify-content: flex-end; gap: 8px; }
.decision-actions .hint { margin-right: auto; display: inline-flex; align-items: center; gap: 4px; font-size: 11.5px; color: var(--ink-3); }
/* One primary at a time: only the selected request's Approve is filled. */
.btn.approve-soft { border-color: transparent; background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.btn.approve-soft:hover { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--teal); }
.btn.deny { color: #fff; background: var(--danger); border-color: transparent; }
.btn.deny:hover { filter: brightness(1.06); background: var(--danger); }
.fine-print { font-size: 11.5px; color: var(--ink-3); }
.answer { color: var(--teal-ink); }
.row-actions .btn.ghost:not(.answer) { color: var(--ink-2); }
.error { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--danger); }
.history { border-top: 1px solid var(--line); padding: 4px 10px 6px; }
.history-toggle { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; }
.history-toggle:hover { background: var(--row-hover); color: var(--ink); }
.history-toggle:focus-visible { box-shadow: var(--focus-ring); }
.history-toggle .mono { font-size: 11px; color: var(--ink-3); font-weight: 500; }
.chev { color: var(--ink-3); }
.chev.turned { transform: rotate(90deg); }
.past-target { grid-column: 1 / -1; width: 100%; }
.history-list { margin: 2px 0 0; padding: 0; list-style: none; }
.past { display: grid; grid-template-columns: 16px minmax(0, 1.4fr) minmax(0, 1fr) 76px 80px 64px; align-items: center; gap: 10px; min-height: 32px; padding: 0 8px; border-radius: 8px; font-size: 12.5px; color: var(--ink-2); }
@media (hover: hover) { .past:hover { background: var(--row-hover); } }
.past-icon { color: var(--ink-3); }
.past.approved .past-icon { color: var(--ok); }
.past.denied .past-icon, .past.revoked .past-icon { color: var(--danger); }
.past-what { color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.past-who { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.past-outcome { font-weight: 600; }
.past.approved .past-outcome { color: var(--ok); }
.past.denied .past-outcome, .past.revoked .past-outcome { color: var(--danger); }
.past-time { color: var(--ink-3); text-align: right; white-space: nowrap; }
.revoke { justify-self: end; height: 24px; padding: 0 8px; font-size: 12px; }
/* Narrow (beside the panel, or a phone): actions go under the request. */
@container queue (max-width: 760px) {
  .item { grid-template-columns: 30px minmax(0, 1fr); }
  .row-actions { grid-column: 2; justify-self: start; }
}
@media (max-width: 720px) {
  .card-head { padding: 12px 14px 10px; }
  .item { grid-template-columns: 30px minmax(0, 1fr); padding: 12px 10px; }
  .row-actions { grid-column: 2; justify-self: start; }
  .row-actions { flex-wrap: nowrap; gap: 4px; }
  .row-actions .btn { height: 40px; padding: 0 10px; }
  .decision-actions .hint { display: none; }
  .decision-actions .btn { height: 40px; }
  .res-title { max-width: 100%; }
  .past { grid-template-columns: 16px minmax(0, 1fr) auto; }
  .past-who, .past-time { display: none; }
  .revoke { grid-column: 2 / -1; justify-self: start; }
}
.item .mark { color: var(--agent-state-color); background: color-mix(in srgb, var(--agent-state-color) 10%, var(--surface-raised)); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--agent-state-color) 25%, transparent); }
.count-badge { background: var(--agent-state-color); color: var(--surface-raised); }
</style>
