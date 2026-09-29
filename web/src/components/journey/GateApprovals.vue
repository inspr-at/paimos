<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import type { Approval } from '../../lib/agents'
import { canDecideApproval } from '../../lib/agentState'
import { can } from '../../lib/authz'
import { expiresIn, expiresSoon, RISK_LABEL, riskFor } from '../../lib/agentState'
import { gateApprovalState, GATE_LABEL, GATE_OF_ACTION, offeredApproval, STAGE_OF_GATE, type Gate } from '../../lib/journey'
import { useJourneyContext } from '../../lib/journeyContext'
import { relativeTime } from '../../lib/work'
import { useAgents } from '../../stores/agents'
import { useJourney } from '../../stores/journey'
import AppIcon from '../AppIcon.vue'
import { askerOf } from './asker'

// A gate as approvals, in the agents workspace's "Needs you" pattern: who asks,
// for which gate, how risky and until when, with Approve and Deny. Decided ones
// stay as a record line. When the stage's one primary button already approves
// this request and takes the step, the row offers only Deny: one decision, one
// primary action.
const props = defineProps<{ gate: Gate; approvals: Approval[]; on: string; canDecide: boolean; now: number; me: string | null }>()
const emit = defineEmits<{ decided: [approval: Approval, decision: 'approved' | 'denied'] }>()
const agents = useAgents()
const journeys = useJourney()
const ctx = useJourneyContext()
const stage = computed(() => ctx.journey.value.stages.find(s => s.key === STAGE_OF_GATE[props.gate]))
const live = computed(() => props.approvals.filter(a => a.decision === null && stateOf(a) === 'pending'))
const mayDecide = (approval: Approval) => props.canDecide && canDecideApproval(approval, can)
// The request the stage's primary button approves along with its step.
const covered = computed(() => {
  const journey = ctx.journey.value
  if (GATE_OF_ACTION[journey.next_action.key] !== props.gate || journey.next_action.key === 'decide' || ctx.next.value.disabled) return null
  return offeredApproval(ctx.approvals.value, journey, props.gate, props.now)?.id ?? null
})
const past = computed(() => props.approvals.filter(a => !live.value.includes(a)).sort((a, b) => {
  const priority = (id: string) => id === stage.value?.gate_approval_id ? 2 : id === stage.value?.gate_offer_id ? 1 : 0
  return priority(b.id) - priority(a.id)
}).slice(0, 3))
const open = ref<{ id: string; mode: 'approve' | 'deny' } | null>(null)
const reason = ref('')
const error = ref('')
const saving = ref(false)
const area = ref<HTMLTextAreaElement[]>([])
async function begin(approval: Approval, mode: 'approve' | 'deny') {
  if (!mayDecide(approval)) return
  open.value = { id: approval.id, mode }; reason.value = ''; error.value = ''
  await nextTick(); area.value[0]?.focus()
}
function cancel() { open.value = null; error.value = '' }
async function submit(approval: Approval) {
  if (!open.value || saving.value || !mayDecide(approval)) return
  const decision = open.value.mode === 'approve' ? 'approved' : 'denied'
  if (decision === 'denied' && !reason.value.trim()) { error.value = 'Say why, so the agent can change course.'; return }
  saving.value = true; error.value = ''
  try {
    await agents.decide(approval, decision, reason.value.trim())
    await journeys.load(ctx.journey.value.project_node_id, true)
    open.value = null
    emit('decided', approval, decision)
  } catch (e) { error.value = e instanceof Error ? e.message : 'The decision was not recorded.' } finally { saving.value = false }
}
function keys(event: KeyboardEvent, approval: Approval) {
  if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); void submit(approval) }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); cancel() }
}
const who = (approval: Approval) => askerOf(agents, approval.agent_principal_id, approval.agent_name)
const decidedBy = (approval: Approval) => approval.decided_by_principal_id && approval.decided_by_principal_id === props.me ? 'you' : 'someone else'
const stateOf = (approval: Approval) => gateApprovalState(approval, stage.value, props.now)
const timeOf = (approval: Approval) => new Date(stage.value?.gate_offer_id === approval.id ? stage.value.gate_offer_expires_at ?? approval.expires_at : approval.expires_at).toLocaleString()
function outcome(approval: Approval) {
  switch (stateOf(approval)) {
    case 'applied': return `Applied by ${decidedBy(approval)}${stage.value?.gate_live ? '' : ' · no longer live'}`
    case 'approved_live': return `Approved by ${decidedBy(approval)}`
    case 'expired': return `${approval.decision === null ? 'Request' : 'Approval'} expired at ${timeOf(approval)}`
    case 'revoked': return 'Approval revoked'
    case 'rejected': return `Denied by ${decidedBy(approval)}`
    default: return 'Approval no longer available'
  }
}
const needsFreshRequest = computed(() => {
  if (stage.value?.gate_live && ctx.journey.value.next_action.approval_request_id !== stage.value.gate_offer_id) return false
  const offer = props.approvals.find(a => a.id === stage.value?.gate_offer_id)
  return offer ? ['expired', 'revoked', 'rejected', 'grant_missing'].includes(stateOf(offer)) : false
})
// Deployment and other high-risk gates stay on the warning tone. Red is reserved for an action that removes something.
function destructive(approval: Approval) {
  return /(?:^|\.)(delete|revoke|destroy|purge|remove)(?:\.|$)/.test(approval.scope)
}
</script>

<template>
  <div class="gate-approvals">
    <ul v-if="live.length" class="items" :aria-label="`${GATE_LABEL[gate]} requests`">
      <li v-for="approval in live" :key="approval.id" class="item" :class="riskFor(approval)" :aria-label="`${GATE_LABEL[gate]} on ${on}, asked by ${who(approval).name}`">
        <span class="mark" aria-hidden="true"><AppIcon name="shield" :size="15" /></span>
        <div class="body">
          <p class="line1">
            <strong class="what" :data-tip="approval.scope">{{ GATE_LABEL[gate] }}</strong>
            <span class="risk-chip" :class="[riskFor(approval), { destructive: riskFor(approval) === 'high' && destructive(approval) }]"><AppIcon v-if="riskFor(approval) === 'high'" name="alert" :size="11" />{{ RISK_LABEL[riskFor(approval)] }}</span>
            <time class="expiry" :class="{ soon: expiresSoon(approval, now) }" :datetime="approval.expires_at" :data-tip="new Date(approval.expires_at).toLocaleString()"><AppIcon name="clock" :size="12" />{{ expiresIn(approval, now) }}</time>
          </p>
          <p class="line2"><span class="asks">Asked by</span><span v-if="who(approval).harness" class="harness mono">{{ who(approval).harness }}</span><strong :data-tip="who(approval).tip || undefined">{{ who(approval).name }}</strong></p>
          <p v-if="approval.rationale" class="why">“{{ approval.rationale }}”</p>
          <form v-if="open?.id === approval.id" class="decision" @submit.prevent="submit(approval)">
            <label :for="`gate-reason-${approval.id}`">{{ open.mode === 'approve' ? 'Reason (optional)' : 'Why not? The agent sees this.' }}</label>
            <textarea :id="`gate-reason-${approval.id}`" ref="area" v-model="reason" class="field" rows="2" maxlength="4000" @keydown="keys($event, approval)" />
            <p v-if="error" class="error" role="alert">{{ error }}</p>
            <div class="decision-actions">
              <span class="hint"><kbd class="keycap"><AppIcon name="enter" /></kbd> to {{ open.mode }} · <kbd class="keycap">esc</kbd> to cancel</span>
              <button type="button" class="btn sm ghost" @click="cancel">Cancel</button>
              <button type="submit" class="btn sm" :class="open.mode === 'approve' ? 'primary' : 'deny'" :disabled="saving"><AppIcon :name="open.mode === 'approve' ? 'check' : 'close'" :size="13" />{{ open.mode === 'approve' ? 'Approve gate' : 'Deny gate' }}</button>
            </div>
          </form>
        </div>
        <div v-if="open?.id !== approval.id && mayDecide(approval)" class="row-actions">
          <button type="button" class="btn sm ghost" @click="begin(approval, 'deny')"><AppIcon name="close" :size="13" />Deny</button>
          <button v-if="covered !== approval.id" type="button" class="btn sm approve-soft" @click="begin(approval, 'approve')"><AppIcon name="check" :size="13" />Approve</button>
        </div>
      </li>
    </ul>
    <ul v-if="past.length" class="records" aria-label="Earlier gate decisions">
      <li v-for="approval in past" :key="approval.id" class="record">
        <AppIcon :name="stateOf(approval) === 'approved_live' ? 'check' : stateOf(approval) === 'rejected' || stateOf(approval) === 'revoked' ? 'close' : 'clock'" :size="12" :class="stateOf(approval)" />
        <span>{{ outcome(approval) }}</span>
        <span class="faint" :data-tip="`Asked by ${who(approval).name}`">· asked {{ relativeTime(approval.proposed_at, { now }) }}</span>
      </li>
    </ul>
    <p v-if="needsFreshRequest" class="fresh-note">The agent asks again for a fresh gate request.</p>
  </div>
</template>

<style scoped>
.gate-approvals { display: grid; gap: 8px; container: gate / inline-size; }
.fresh-note { margin: 0; color: var(--ink-3); font-size: 12px; }
.items, .records { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.item { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; gap: 10px; align-items: start; padding: 10px 10px 10px 8px; border-radius: 12px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); }
.mark { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 8px; background: var(--row-selected); color: var(--teal-ink); }
.body { display: grid; gap: 4px; min-width: 0; }
.line1 { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 8px; }
.what { font-size: 13.5px; font-weight: 600; color: var(--ink); }
.risk-chip { display: inline-flex; align-items: center; gap: 3px; height: 18px; padding: 0 7px; border-radius: 999px; font: 600 10px/18px var(--mono); letter-spacing: .06em; text-transform: uppercase; background: var(--row-selected); color: var(--teal-ink); }
.risk-chip.medium { background: transparent; box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .55); color: var(--gold-ink); }
.risk-chip.high { background: var(--gold-wash); color: var(--warn-ink); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .55); }
.risk-chip.high.destructive { background: var(--danger-bg); color: var(--danger); box-shadow: inset 0 0 0 1px var(--danger-line); }
.expiry { display: inline-flex; align-items: center; gap: 4px; margin-left: auto; font-size: 12px; color: var(--ink-3); white-space: nowrap; }
.expiry.soon { color: var(--gold-ink); }
.line2 { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 6px; font-size: 12.5px; color: var(--ink-2); min-width: 0; }
.line2 strong { color: var(--ink); font-weight: 600; }
.harness { padding: 0 6px; border-radius: 5px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 10.5px; }
.asks { color: var(--ink-3); }
.why { font-size: 12.5px; color: var(--ink-2); font-style: italic; overflow-wrap: anywhere; }
.row-actions { display: flex; gap: 6px; }
.btn.approve-soft { color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); background: var(--chip-teal-bg); }
.btn.deny { background: var(--danger-bg); color: var(--danger); box-shadow: inset 0 0 0 1px var(--danger-line); }
.decision { display: grid; gap: 6px; margin-top: 6px; }
.decision label { font-size: 12px; color: var(--ink-2); }
.decision textarea { height: auto; padding: 8px 10px; resize: vertical; }
.decision-actions { display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap; gap: 6px; }
.decision-actions .hint { margin-right: auto; font-size: 11.5px; color: var(--ink-3); }
.record { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; font-size: 12.5px; color: var(--ink-2); }
.record svg { flex-shrink: 0; color: var(--ink-3); }
.record svg.approved_live { color: var(--ok); } .record svg.rejected { color: var(--danger); }
.faint { color: var(--ink-3); }
@container gate (max-width: 460px) {
  .item { grid-template-columns: 30px minmax(0, 1fr); }
  .row-actions { grid-column: 2; }
}
</style>
