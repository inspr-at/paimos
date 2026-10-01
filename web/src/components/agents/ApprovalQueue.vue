<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import type { Approval, ProjectMessage } from '../../lib/agents'
import { HARNESS_NAME, LOGIN_COMMAND, type AccountRow } from '../../lib/capacity'
import { toast } from '../../lib/toast'
import { RISK_LABEL, expiresIn, expiresSoon, riskFor, scopeLabel, type Asker, type Resource } from '../../lib/agentState'
import { confirmAction } from '../../lib/confirm'
import { relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import TargetSummary from '../deploy/TargetSummary.vue'
import HarnessMark from './HarnessMark.vue'
import AgentStateMark from '../indicators/AgentStateMark.vue'
import { useAgentAppearance } from '../../lib/agentAppearance'
import { createApprovalSettle, settledAnnouncement, settledLine, settledWord, type Settling } from './approvalSettle'
const { appearance } = useAgentAppearance()

// What waits on Markus: permission requests (approve or deny, with a reason the agent
// sees), held action requests, and accounts that need a new vendor sign-in. The desk
// shows it only when something waits; decided and expired requests fold into a
// quiet history (history-only mode when nothing waits, so Revoke stays reachable).
type Held = ProjectMessage & { projectId: string }
const props = defineProps<{
  pending: Approval[]; held: Held[]; history: Approval[]; now: number; cursor: string; canDecide: boolean; canDecideApproval: (approval: Approval) => boolean; canResolve: boolean; canRevoke: boolean; loaded: boolean
  signins?: AccountRow[]; historyOnly?: boolean
  asker: (principalId: string, fallbackName?: string | null) => Asker; resource: (approval: Approval) => Resource
  decide: (approval: Approval, decision: 'approved' | 'denied', reason: string) => Promise<Pick<Approval, 'decision'>>
  revoke: (approval: Approval) => Promise<void>
  resolve: (request: Held, decision: 'resolved' | 'dismissed', note: string) => Promise<void>
}>()
// settling: a decided card is still on screen, so the page keeps this card mounted.
const emit = defineEmits<{ focusRow: [id: string]; openAgent: [principalId: string]; settling: [active: boolean]; announce: [text: string] }>()

type Mode = 'approve' | 'deny' | 'resolve' | 'dismiss'
const open = ref<{ id: string; mode: Mode } | null>(null)
const reason = ref('')
const busy = ref(false)
const error = ref('')
const showHistory = ref(false)
const revoked = ref(new Set<string>())
const reasonField = ref<HTMLTextAreaElement[]>()
const root = ref<HTMLElement>()
const decidedToggle = ref<HTMLButtonElement>()

// A decision answers on its card, then folds into Decided (AEON-505, approvalSettle.ts).
const reducedMotion = () => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false
// The outcome starts at the card's old height and eases to its own, so nothing jumps.
const fitted = reactive(new Map<string, number>())
async function fit(id: string) {
  await nextTick()
  const card = root.value?.querySelector<HTMLElement>(`[data-settled="${CSS.escape(id)}"]`)
  if (!card) return
  const locked = card.style.height
  card.style.height = 'auto'
  const height = card.getBoundingClientRect().height
  card.style.height = locked
  void card.offsetHeight
  fitted.set(id, height)
}
function settledHeight(id: string, entry: Settling) {
  const height = entry.phase === 'collapsing' ? 0 : fitted.get(id) ?? entry.height
  return height === undefined ? undefined : `${height}px`
}
const decisions = createApprovalSettle({
  decide: (approval, decision, reason) => props.decide(approval, decision, reason),
  reducedMotion,
  measure: id => root.value?.querySelector(`[data-row="a:${CSS.escape(id)}"]`)?.getBoundingClientRect().height,
  onConfirmed: entry => {
    open.value = null; reason.value = ''
    emit('announce', settledAnnouncement(entry.decision!, named(entry.approval).name, entry.approval.scope, entry.reason))
    focusPast(entry.approval.id)
    void fit(entry.approval.id)
  },
  onSettled: entry => { fitted.delete(entry.approval.id) },
})
watch(decisions.active, active => emit('settling', active), { flush: 'sync' })
onBeforeUnmount(() => { if (decisions.active.value) emit('settling', false); decisions.stop() })
// The decided card's place goes to the next request, else to Decided; focus moves
// only if it was here, and never scrolls the page.
function focusPast(id: string) {
  const here = document.activeElement
  if (here && here !== document.body && !root.value?.contains(here)) return
  const list = [...(root.value?.querySelectorAll<HTMLElement>('[data-row^="a:"], [data-row^="m:"]') ?? [])]
  const at = list.findIndex(el => el.dataset.row === `a:${id}`)
  const next = at === -1 ? undefined : list[at + 1] ?? list[at - 1]
  emit('focusRow', next?.dataset.row ?? '')
  ;(next ?? decidedToggle.value)?.focus({ preventScroll: true })
}
// Pending requests plus decided ones still on screen, each in its place.
const rows = computed(() => {
  const waiting = new Set(props.pending.map(item => item.id))
  const leaving = [...decisions.entries.values()].filter(entry => !waiting.has(entry.approval.id)).map(entry => entry.approval)
  const list = leaving.length ? [...props.pending, ...leaving].sort((a, b) => Date.parse(a.expires_at) - Date.parse(b.expires_at)) : props.pending
  return list.map(approval => {
    const entry = decisions.entries.get(approval.id)
    return { approval, settled: entry && entry.phase !== 'confirming' ? entry : undefined }
  })
})
// Decided takes a request when its card starts to fold, so the count ticks then.
const decided = computed(() => props.history.filter(item => !decisions.holding(item.id)))
const ticks = ref(0)
watch(() => decided.value.length, (count, before) => { if (count > before) ticks.value++ })

async function begin(id: string, mode: Mode) {
  if (!props.canDecide || busy.value || decisions.entries.has(id)) return
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
  if (busy.value) return
  const current = open.value
  open.value = null; reason.value = ''; error.value = ''
  if (current) void nextTick(() => document.querySelector<HTMLElement>(`[data-row="${current.mode === 'resolve' || current.mode === 'dismiss' ? 'm' : 'a'}:${current.id}"]`)?.focus())
}
async function submit(approval: Approval) {
  if (!open.value || busy.value || !props.canDecideApproval(approval)) return
  busy.value = true; error.value = ''
  try { await decisions.submit(approval, open.value.mode === 'approve' ? 'approved' : 'denied', reason.value.trim()) }
  catch (e) { error.value = e instanceof Error ? e.message : 'The decision was not recorded. Please try again.' }
  finally { busy.value = false }
  // A failed call leaves the card actionable, the reason kept and focused again.
  if (error.value) { await nextTick(); reasonField.value?.[0]?.focus() }
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
const count = computed(() => props.pending.length + props.held.length + (props.signins?.length ?? 0))
// What the card shows: a decided request counts as waiting until it folds.
const shown = computed(() => rows.value.length + props.held.length + (props.signins?.length ?? 0))
const badge = computed(() => count.value + rows.value.filter(row => !props.pending.includes(row.approval) && decisions.holding(row.approval.id)).length)
const vendor = (row: AccountRow) => HARNESS_NAME[row.harness] ?? row.harness
async function copyLogin(row: AccountRow) {
  const command = LOGIN_COMMAND[row.harness]
  if (!command) return
  try { await navigator.clipboard?.writeText(command) } catch { /* the toast still names it */ }
  toast(`Copied: ${command} — run it on ${row.host}.`)
}
// The asker's harness shows as its mark; the address label maps back to the key.
const harnessOf = (label: string) => label.toLowerCase()
defineExpose({ begin, cancel, isOpen: () => !!open.value })
</script>

<template>
  <section v-if="historyOnly" class="decided" aria-label="Decided requests">
    <div class="history">
      <button type="button" class="history-toggle" :aria-expanded="showHistory" aria-controls="approval-history" @click="showHistory = !showHistory">
        <AppIcon name="chevron-right" :size="12" class="chev" :class="{ turned: showHistory }" />Decided<span :key="ticks" class="mono" :class="{ tick: ticks }">{{ decided.length }}</span>
      </button>
      <ul v-if="showHistory" id="approval-history" class="history-list">
        <li v-for="approval in decided.slice(0, 20)" :key="approval.id" class="past" :class="outcome(approval).toLowerCase()">
          <AppIcon :name="outcome(approval) === 'Approved' ? 'check' : outcome(approval) === 'Expired' ? 'clock' : 'close'" :size="13" class="past-icon" />
          <span class="past-what">{{ scopeLabel(approval.scope) }}</span>
          <span class="past-who">{{ named(approval).name }}</span>
          <span class="past-outcome">{{ outcome(approval) }}</span>
          <time class="past-time" :datetime="approval.proposed_at">{{ relativeTime(approval.proposed_at, { now }) }}</time>
          <button v-if="approval.decision === 'approved' && !revoked.has(approval.id) && canRevoke" type="button" class="btn sm ghost revoke" @click="revoke(approval)">Revoke</button>
        </li>
      </ul>
    </div>
  </section>
  <section v-else ref="root" class="queue glass-card" :class="{ clear: loaded && !shown }" :style="appearance('waiting')" aria-labelledby="needs-title">
    <header class="card-head">
      <h2 id="needs-title">Needs you</h2>
      <span v-if="badge" class="count-badge">{{ badge }}</span>
    </header>

    <div v-if="!loaded" class="skeleton-rows" role="status" aria-label="Loading requests">
      <div v-for="i in 2" :key="i" class="sk-row"><span class="skeleton mark-sk" /><span class="sk-lines"><span class="skeleton" :style="{ width: `${34 + i * 9}%` }" /><span class="skeleton" style="width: 22%" /></span></div>
    </div>

    <ul v-else-if="shown" class="items" aria-label="Requests waiting for you">
      <template v-for="{ approval, settled } in rows" :key="approval.id">
        <!-- Confirmed by the server: the outcome in the card's own place, then the fold. -->
        <li
          v-if="settled" class="item settled" :class="[settled.decision, settled.phase]" :data-settled="approval.id"
          :style="{ height: settledHeight(approval.id, settled) }"
        >
          <span class="mark settled-mark" aria-hidden="true"><AppIcon :name="settled.decision === 'approved' ? 'check' : 'close'" :size="16" /></span>
          <div class="settled-body">
            <p class="settled-line"><strong class="settled-word">{{ settledWord(settled.decision!) }}</strong><span class="sep" aria-hidden="true">·</span><span>{{ settledLine(settled.decision!, named(approval).name, approval.scope) }}</span></p>
            <p v-if="settled.reason" class="settled-reason">“{{ settled.reason }}”</p>
          </div>
        </li>
        <li
          v-else class="item agent-state-surface" :class="[riskFor(approval), { active: cursor === `a:${approval.id}`, open: open?.id === approval.id }]"
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
      </template>
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
      <li v-for="row in signins ?? []" :key="row.id" class="item signin" :data-row="`g:${row.id}`" tabindex="-1" :aria-label="`${vendor(row)} needs a new sign-in on ${row.host}`">
        <span class="mark key"><AppIcon name="key" :size="16" /></span>
        <div class="body">
          <p class="line1"><strong class="what">{{ vendor(row) }} needs a new sign-in on {{ row.host }}</strong></p>
          <p class="line2 signin-help"><template v-if="LOGIN_COMMAND[row.harness]">Run <code>{{ LOGIN_COMMAND[row.harness] }}</code> there · </template>agents skip this account until then</p>
        </div>
        <div v-if="LOGIN_COMMAND[row.harness]" class="row-actions">
          <button type="button" class="btn sm" @click.stop="copyLogin(row)"><AppIcon name="copy" :size="13" />Copy command</button>
        </div>
      </li>
    </ul>

    <!-- Reserve Decided's space before the first answer, so confirmation cannot
         insert a footer below a card whose original height is still held (AEON-505). -->
    <footer v-if="loaded" class="history">
      <button ref="decidedToggle" type="button" class="history-toggle" :aria-expanded="showHistory" aria-controls="approval-history" @click="showHistory = !showHistory">
        <AppIcon name="chevron-right" :size="12" class="chev" :class="{ turned: showHistory }" />Decided<span :key="ticks" class="mono" :class="{ tick: ticks }">{{ decided.length }}</span>
      </button>
      <ul v-if="showHistory" id="approval-history" class="history-list">
        <li v-for="approval in decided.slice(0, 20)" :key="approval.id" class="past" :class="outcome(approval).toLowerCase()">
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
/* Something waits: a warm full tint and a soft gold ring, never an edge bar. */
.queue:not(.clear) { background: linear-gradient(165deg, color-mix(in srgb, var(--gold-2) 16%, var(--surface-raised-2)), color-mix(in srgb, var(--gold-2) 10%, var(--glass)) 60%); box-shadow: var(--shadow), 0 0 0 1px color-mix(in srgb, var(--gold) 38%, transparent); }
.queue:not(.clear) .item + .item { box-shadow: 0 -1px 0 color-mix(in srgb, var(--gold) 18%, transparent); }
.item.signin .mark.key { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); }
.signin-help code { padding: 1px 6px; border-radius: 5px; background: var(--surface-sunken); font: 500 12px var(--mono); color: var(--ink); }
.decided { padding: 0 4px; }
.decided .history { border-top: 0; padding: 0; }
.card-head { display: flex; align-items: center; gap: 10px; min-height: 48px; padding: 10px 18px 8px; }
.queue.clear .card-head { padding-bottom: 10px; }
.card-head h2 { font-size: 17px; font-weight: 600; }
.count-badge { display: inline-grid; place-items: center; min-width: 20px; height: 20px; padding: 0 6px; border-radius: 999px; background: var(--gold); color: #fff; font: 700 11px/1 var(--mono); font-variant-numeric: tabular-nums; }
.all-clear { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--ink-3); }
.skeleton-rows { display: grid; gap: 4px; padding: 0 8px 8px; }
.sk-row { display: flex; align-items: center; gap: 12px; padding: 12px 12px 12px 10px; }
.mark-sk { flex-shrink: 0; width: 30px; height: 30px; border-radius: 9px; }
.sk-lines { display: grid; gap: 8px; flex: 1; min-width: 0; }
.all-clear svg { color: var(--ok); }
.items { margin: 0; padding: 0 8px 8px; list-style: none; display: flex; flex-direction: column; gap: 4px; }
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
/* A decided request (AEON-505): the outcome in the card's place, a quiet full tint and a
   hairline ring (approved in the ok hue, denied neutral), then a height fold whose negative
   margin takes the list gap with it, so nothing below jumps when the card goes. */
.queue .items .item.settled { grid-template-columns: 30px minmax(0, 1fr); align-items: center; align-content: center; overflow: hidden; cursor: default; }
.queue .items .item.settled.approved { background: color-mix(in srgb, var(--ok) 8%, var(--surface-raised)); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--ok) 24%, transparent); }
.queue .items .item.settled.denied { background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line-2); }
.item.settled .settled-mark { color: var(--ink-2); background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); }
.item.settled.approved .settled-mark { color: var(--ok); background: color-mix(in srgb, var(--ok) 14%, var(--surface-raised)); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--ok) 32%, transparent); }
.settled-body { display: grid; gap: 2px; min-width: 0; }
.settled-line { font-size: 14px; line-height: 1.4; color: var(--ink); }
.settled-word { font-weight: 650; }
.approved .settled-word { color: var(--ok); }
.settled-line .sep { margin: 0 6px; color: var(--ink-3); }
.settled-reason { font-size: 13px; line-height: 1.45; color: var(--ink-2); overflow: hidden; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; }
.item.settled.collapsing { padding-top: 0; padding-bottom: 0; margin-bottom: -4px; opacity: 0; }
@media (prefers-reduced-motion: no-preference) {
  .item.settled { transition: height .28s cubic-bezier(.4, 0, .2, 1), padding .28s cubic-bezier(.4, 0, .2, 1), margin .28s cubic-bezier(.4, 0, .2, 1), opacity .2s ease; }
  .item.settled.success .settled-mark, .item.settled.success .settled-body { animation: settle-in .22s cubic-bezier(.2, 0, 0, 1) both; }
  .history-toggle .tick { display: inline-block; animation: count-tick .32s cubic-bezier(.2, 0, 0, 1) both; }
}
@keyframes settle-in { from { opacity: 0; transform: translateY(3px); } }
@keyframes count-tick { from { opacity: 0; transform: translateY(70%); } 60% { color: var(--ink); } }
</style>
