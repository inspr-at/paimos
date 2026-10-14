<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { answerFor, CUSTOM_ANSWER, deskProject, draftFor, fieldTarget, kindLabels, projectSwitch, type DeskClose, type DeskProject, type DeskTicket, type DeskWho, macPlatform, outcomeLabels, outcomeUnavailable, stampDescription, stampWhy, submitModifier, suggestedUnavailableNote, type DeskDraft, type DeskItem, type DeskOutcome } from '../../lib/decisionDesk'
import { deliveryTime, loadDeskContext, type DeskContext } from '../../lib/decisionDeskApi'
import { RISK_LABEL, riskFor, scopeLabel } from '../../lib/agentState'
import { contentUrl, fileKind, hasThumbnail } from '../../lib/attachments'
import AttachmentLightbox from '../work/AttachmentLightbox.vue'
import AppIcon, { type IconName } from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import MarkdownBody from '../MarkdownBody.vue'
import HarnessMark from './HarnessMark.vue'
import TargetSummary from '../deploy/TargetSummary.vue'
import { StepupNavigation, stepupRows } from '../../lib/stepup'

// AEON-1339: the memo as the approved rev 11 artifact draws it (glass frame, folder tab,
// one sheet outline, the tilted outcome stamp), on the Decide rules of AEON-1343.
const props = defineProps<{
  items: DeskItem[]; round: string[]; start: string; arrivalsCount: number; pendingRuleIds?: readonly string[]; allowed: (item: DeskItem) => boolean
  decide: (item: DeskItem, draft: DeskDraft, requestId: string, signal?: AbortSignal) => Promise<DeskItem>
  canRevoke?: (item: DeskItem) => boolean; revoke?: (item: DeskItem) => Promise<DeskItem>
  editRule?: (item: DeskItem) => Promise<void>
  // The signal aborts when the memo closes; late step-up responses start nothing.
  stepupMethod?: string
  /** Names the asking agent (session name, harness, model); undefined while unknown. */
  who?: (item: DeskItem) => DeskWho | undefined
  /** The ticket an item is about, from the desk's one bounded lookup. */
  ticket?: (item: DeskItem) => DeskTicket | undefined
  /** Opens this decided item ready to change (the Undo of the toast after closing). */
  change?: string
}>()
const emit = defineEmits<{ close: [result?: DeskClose]; recorded: [item: DeskItem] }>()
const dialog = ref<HTMLDialogElement>(), memo = ref<HTMLElement>(), decideButton = ref<HTMLButtonElement>(), pagerButton = ref<HTMLButtonElement>()
const frame = ref<HTMLElement>(), stack = ref<HTMLElement>(), tab = ref<HTMLElement>(), sheet = ref<SVGSVGElement>(), hint = ref<HTMLElement>()
const index = ref(Math.max(0, props.round.indexOf(props.start))), pager = ref(false), jumpIndex = ref(0), busy = ref(false), status = ref(''), editing = ref(''), slam = ref(false)
const drafts = ref<Record<string, DeskDraft>>({}), skipped = ref(new Set<string>()), context = ref<DeskContext>({ attachments: [], related: [], outcomes: [], warnings: [] }), contextLoading = ref(false)
const lightbox = ref<InstanceType<typeof AttachmentLightbox>>(), viewing = ref(false), brokenThumbs = ref(new Set<string>())
const contexts = new Map<string, DeskContext>()
// Decided questions reopened with Change (or Undo, which acts as Change until a withdraw exists, AEON-1340).
const changing = ref(new Set<string>(props.change && props.round.includes(props.change) ? [props.change] : []))
// The round's own decisions, in order; after the last one the sheet says "All decided".
const done = ref<string[]>([]), finished = ref(false), holdClose = ref(false)
const item = computed(() => finished.value ? undefined : props.items.find(item => item.id === props.round[index.value]))
const draft = computed(() => item.value ? drafts.value[item.value.id] : undefined)
const question = computed(() => item.value?.kind === 'question' || item.value?.kind === 'handover')
// The decided look: the landed stamp, the chosen row checked, the delivery line, Change and Next.
const decidedView = computed(() => !!item.value?.decided && !changing.value.has(item.value.id))
// AEON-1057: the project of the item shown before, kept for this desk session, so a change of
// project fills the key chip however the person moved (J/K, jump list, Decide & next).
const project = computed(() => item.value ? deskProject(item.value) : undefined)
const switchedFrom = ref<DeskProject>()
let shown: { id: string; project: DeskProject } | undefined
const mac = macPlatform(navigator.platform)
const outcomes: DeskOutcome[] = ['once', 'always', 'requirement', 'doctrine']
const outcomeKeys = ['O', 'A', 'R', 'D']
const STAMP: Record<DeskOutcome, { icon: IconName; tone: string }> = { once: { icon: 'message', tone: 'ok' }, always: { icon: 'pin', tone: 'teal' }, requirement: { icon: 'req', tone: 'gold' }, doctrine: { icon: 'book', tone: 'red' } }
interface MemoLine { tone: string; icon: IconName; bold: string; text?: string; grace?: number }
interface LegendEntry { keys: string[]; or?: string[]; range?: [string, string]; text: string }
const expired = ref(false), now = ref(Date.now())
let contextGeneration = 0, live = true, expiryTimer: ReturnType<typeof setTimeout> | undefined, closeTimer: ReturnType<typeof setTimeout> | undefined, opener: HTMLElement | null = null
let decision: AbortController | undefined, observer: ResizeObserver | undefined, clock: ReturnType<typeof setInterval> | undefined, narrowToast: ReturnType<typeof setTimeout> | undefined

const blocked = computed(() => !item.value || !props.allowed(item.value) || !!item.value.unavailable || expired.value || busy.value)
const trimDeclinable = computed(() => item.value?.kind === 'key_trim' && !item.value.decided && props.allowed(item.value) && !expired.value && !busy.value && (!item.value.unavailable || item.value.unavailable === item.value.keyTrim?.blocked_reason))
// Step-up and key trim decide through their own Approve / Decline pair.
const nativePending = computed(() => (item.value?.kind === 'key_trim' || item.value?.kind === 'stepup') && !item.value.decided)
const stepupPending = computed(() => item.value?.kind === 'stepup' && !item.value.decided)
const stepupChange = computed(() => item.value?.stepup ? stepupRows(item.value.stepup, item.value.projectName) : undefined)
const editable = computed(() => !!item.value && !blocked.value && (!item.value.decided || (question.value && changing.value.has(item.value.id))))
const trimRestorable = computed(() => item.value?.keyTrim?.state === 'applied' && !!item.value.keyTrim.restore_until && Date.parse(item.value.keyTrim.restore_until) > now.value)
const isOpen = (id: string) => { const row = props.items.find(row => row.id === id); return !!row && (!row.decided || changing.value.has(id)) && !row.linkOut }
const openLeft = computed(() => props.round.filter(id => id !== item.value?.id && isOpen(id)).length)
const asker = computed(() => item.value ? whoOf(item.value) : undefined)
function whoOf(row: DeskItem): DeskWho | undefined { return props.who?.(row) ?? (row.askers?.[0]?.name ? { name: row.askers[0].name } : undefined) }
const cap = (text: string) => text.charAt(0).toUpperCase() + text.slice(1)
const askerName = computed(() => asker.value?.name ?? (item.value?.kind === 'approval' && item.value.approval ? `Agent ${item.value.approval.agent_principal_id.slice(0, 8)}` : 'the agent'))
const ticketRef = computed<DeskTicket | undefined>(() => {
  const row = item.value
  if (!row) return undefined
  if (context.value.ticket) return { key: context.value.ticket.key, title: context.value.ticket.title }
  return props.ticket?.(row) ?? (row.ticketKey ? { key: row.ticketKey, title: '' } : undefined)
})
const suggestedNote = computed(() => item.value && question.value ? suggestedUnavailableNote(item.value) : '')
// Why Decide cannot decide yet, and what to focus. Empty when it can (Markus 10-14 09:57: never a silent no-op).
const decideBlock = computed<{ why: string; focus: string }>(() => {
  const current = item.value, sent = draft.value
  if (finished.value || !current || !sent) return { why: '', focus: '' }
  if (decidedView.value && !(current.kind === 'key_trim' && trimRestorable.value)) return { why: '', focus: '' }
  if (expired.value) return { why: 'This request expired.', focus: '' }
  if (current.unavailable) return { why: current.unavailable, focus: '' }
  if (!props.allowed(current)) return { why: 'You don’t have permission to decide this.', focus: '' }
  if (busy.value) return { why: status.value || 'Recording the decision…', focus: '' }
  if (current.kind === 'key_trim' || current.kind === 'stepup') return { why: '', focus: '' }
  const choice = current.choices.find(row => row.id === sent.optionId)
  if (!choice) return { why: current.kind === 'approval' ? 'Choose Approve or Deny first.' : current.kind === 'rule' ? 'Choose Propose the change or Not now first.' : question.value ? 'Choose an answer first.' : 'Choose what to do first.', focus: 'choice' }
  if (choice.unavailable) return { why: choice.unavailable, focus: 'choice' }
  if (choice.field && !sent.answer.trim()) return { why: current.kind === 'action' ? `Write your reply to ${askerName.value} first.` : 'Write your answer first, or pick an option.', focus: 'answer' }
  if (question.value && outcomeUnavailable(current, sent.outcome)) {
    const other = outcomes.some(outcome => !outcomeUnavailable(current, outcome))
    // With no usable stamp at all, the restriction is lasting and stays beside the action.
    return other ? { why: `${outcomeLabels[sent.outcome]} isn’t available here. Pick another stamp.`, focus: 'stamp' } : { why: stampWhy(current, sent.outcome), focus: '' }
  }
  if (current.kind === 'rule' && sent.optionId === 'dismiss' && !sent.reason.trim()) return { why: `Give ${askerName.value} a reason for Not now.`, focus: 'reason' }
  return { why: '', focus: '' }
})
// The top bar names a lasting restriction beside the action; a passing note replaces it.
const topStatus = computed(() => status.value || (decideBlock.value.focus ? '' : decideBlock.value.why))
const primaryBlocked = computed(() => !!decideBlock.value.why)
const lastOpen = computed(() => !!item.value && !decidedView.value && openLeft.value === 0)
const primary = computed(() => {
  const current = item.value
  if (finished.value || !current) return 'Done'
  if (current.kind === 'stepup') return current.decided ? (openLeft.value ? 'Next' : 'Close') : 'Approve'
  if (current.kind === 'key_trim') return !current.decided ? 'Approve' : trimRestorable.value ? 'Restore' : openLeft.value ? 'Next' : 'Close'
  if (decidedView.value) return openLeft.value ? 'Next' : 'Close'
  if (current.decided) return lastOpen.value ? 'Replace' : 'Replace & next'
  return lastOpen.value ? 'Decide' : 'Decide & next'
})
const ghost = computed<'skip' | 'decline' | 'change' | ''>(() => {
  const current = item.value
  if (finished.value || !current) return ''
  if (nativePending.value) return 'decline'
  if (decidedView.value) return question.value ? 'change' : ''
  return 'skip'
})
// One reserved slot beside Skip for the native Revoke / Edit actions, only in rounds that can use it.
const nativeSlot = computed(() => (!!props.revoke && props.round.some(id => props.items.find(row => row.id === id)?.kind === 'approval')) || (!!props.editRule && props.round.some(id => props.items.find(row => row.id === id)?.kind === 'rule')))
const announcement = ref('')
const trimRequests = new Map<string, string>()
const plainText = (value: unknown) => typeof value === 'string' ? value : ''
function doctrineReviewPath(id: string, state?: string) {
  return state === 'pending' || (!state && props.pendingRuleIds?.includes(id))
    ? `/decision-desk?item=r:${encodeURIComponent(id)}` : '/settings/agent-rules'
}
const clockTime = (value?: string) => { const at = value ? Date.parse(value) : NaN; return Number.isFinite(at) ? new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' }).format(at) : '' }
function ago(value?: string) {
  const at = value ? Date.parse(value) : NaN
  if (!Number.isFinite(at)) return ''
  const minutes = Math.max(0, Math.round((now.value - at) / 60_000))
  return minutes < 1 ? 'just now' : minutes < 60 ? `${minutes} min ago` : minutes < 1440 ? `${Math.round(minutes / 60)} h ago` : minutes < 2880 ? 'yesterday' : `${Math.round(minutes / 1440)} days ago`
}
// Seconds left in the server's ten-second grace; a client clock that runs off never shows more than the grace.
const graceLeft = (row: DeskItem) => row.deliverAfter ? Math.min(10, Math.max(0, Math.round((Date.parse(row.deliverAfter) - now.value) / 1000))) : 0
// The answer's own delivery, in one line where the open memo shows what the agent does meanwhile.
// It stops at "waiting for it to read" until the read receipt exists (Tick, AEON-1340).
const deliveryLine = computed<MemoLine | undefined>(() => {
  const row = item.value
  if (!row || !decidedView.value) return undefined
  if (row.kind !== 'question' && row.kind !== 'handover') return { tone: 'd-sent', icon: 'check' as IconName, ...decidedText(row) }
  if (row.deliveryState === 'failed') return { tone: 'd-failed', icon: 'alert' as IconName, bold: 'Not delivered', text: 'The answer is recorded; delivery to the agent failed.' }
  if (row.deliveryState === 'pending' && graceLeft(row) > 0) return { tone: 'd-grace', icon: 'clock' as IconName, bold: `Decided ${clockTime(row.decidedAt)}`.trim(), grace: graceLeft(row) }
  if (row.deliveryState === 'pending' || row.deliveryState === 'delivered') return { tone: 'd-sent', icon: 'send' as IconName, bold: `Sent to ${askerName.value} ${clockTime(row.deliverAfter)}`.trim(), text: 'waiting for it to read' }
  return { tone: 'd-sent', icon: 'check' as IconName, bold: `Decided ${clockTime(row.decidedAt)}`.trim(), text: '' }
})
// Decisions outside the question grace keep their own immediate semantics, said in plain words.
function decidedText(row: DeskItem): { bold: string; text: string } {
  const who = cap(askerName.value)
  if (row.kind === 'approval') {
    if (revokedHere.value.has(row.id)) return { bold: 'Revoked', text: 'The original decision stays in history.' }
    return row.optionId === 'approved' ? { bold: 'Approved', text: `${who} can go ahead.` } : row.optionId === 'denied' ? { bold: 'Denied', text: `${who} sees your reason.` } : { bold: row.answer || 'Closed', text: '' }
  }
  if (row.kind === 'action') {
    // A reply inside its grace is recorded, not sent yet (the inbox reports `pending`).
    if (row.optionId === 'reply') return row.deliveryState === 'pending' ? { bold: 'Decided', text: `sends to ${askerName.value} shortly` } : { bold: `Sent to ${askerName.value}`, text: 'waiting for it to read' }
    return { bold: row.optionId === 'resolved' ? 'Resolved' : 'Dismissed', text: '' }
  }
  if (row.kind === 'rule') return row.optionId === 'propose' ? { bold: 'Pull request opened', text: 'You merge it as usual.' } : { bold: 'Not now', text: `${who} gets your reason.` }
  return { bold: row.answer || 'Decided', text: row.delivery || '' }
}
const revokedHere = ref(new Set<string>())
// What the agent does meanwhile (rev 11's line, from the live meanwhile).
const meanLine = computed<MemoLine | undefined>(() => {
  const row = item.value
  if (!row || decidedView.value) return undefined
  const text = row.meanwhile && !['carries on', 'parked', 'paused', 'stopped', 'Waiting for a person.'].includes(row.meanwhile) ? row.meanwhile : ''
  if (row.kind === 'approval') return { tone: 'park', icon: 'clock' as IconName, bold: `${cap(askerName.value)} waits for this.`, text: [row.expiresAt ? expiresIn(row.expiresAt) : '', row.approval ? `${RISK_LABEL[riskFor(row.approval)]}.` : ''].filter(Boolean).join(' · ') }
  if (row.kind === 'action') return { tone: 'stop', icon: 'stop' as IconName, bold: `${cap(askerName.value)} waits for this.`, text: '' }
  if (row.kind === 'rule') return { tone: 'carry', icon: 'arrow' as IconName, bold: `${cap(askerName.value)} carries on meanwhile.`, text: 'The current rule stays until a pull request changes it.' }
  if (row.kind === 'stepup' || row.kind === 'key_trim' || row.kind === 'tier') return { tone: 'park', icon: 'clock' as IconName, bold: row.meanwhile, text: row.expiresAt ? expiresIn(row.expiresAt) : '' }
  const kind = row.meanwhileKind ?? (row.held ? 'parked' : 'carries_on')
  if (kind === 'parked') return { tone: 'park', icon: 'park' as IconName, bold: ticketRef.value ? `Parked ${ticketRef.value.key}.` : 'Parked.', text }
  if (kind === 'stopped') return { tone: 'stop', icon: 'stop' as IconName, bold: `${cap(askerName.value)} waits for this.`, text }
  if (kind === 'paused') return { tone: 'park', icon: 'hold' as IconName, bold: `${cap(askerName.value)} paused this work.`, text }
  return { tone: 'carry', icon: 'arrow' as IconName, bold: `${cap(askerName.value)} carries on meanwhile.`, text }
})
function expiresIn(at: string) { const minutes = Math.ceil((Date.parse(at) - now.value) / 60_000); return minutes < 1 ? 'Expires now' : minutes < 120 ? `Expires in ${minutes} min` : `Expires ${deliveryTime(at)}` }
// The outcome on the stamp: the chosen one previewed, landed once decided; "Not decided" without a choice.
const impression = computed<{ word: string; tone: string } | undefined>(() => {
  const row = item.value, sent = draft.value
  if (!row || !sent) return undefined
  if (question.value) return sent.optionId ? { word: outcomeLabels[sent.outcome], tone: STAMP[sent.outcome].tone } : undefined
  const choice = sent.optionId || (row.decided ? row.optionId : '')
  if (!choice) return undefined
  if (row.kind === 'approval' || row.kind === 'tier') return choice === 'approved' || choice === 'approve' ? { word: 'Approved', tone: 'ok' } : { word: 'Denied', tone: 'red' }
  if (row.kind === 'action') return choice === 'reply' ? { word: 'Replied', tone: 'ok' } : choice === 'resolved' ? { word: 'Resolved', tone: 'ok' } : { word: 'Dismissed', tone: 'ink' }
  if (row.kind === 'rule') return choice === 'propose' ? { word: 'PR proposed', tone: 'gold' } : { word: 'Not now', tone: 'ink' }
  if (row.kind === 'key_trim') return row.keyTrim?.state === 'applied' ? { word: 'Trimmed', tone: 'ok' } : row.keyTrim?.state === 'restored' ? { word: 'Restored', tone: 'teal' } : { word: 'Declined', tone: 'ink' }
  if (row.kind === 'stepup') return row.answer?.startsWith('Approved') ? { word: 'Approved', tone: 'ok' } : { word: 'Declined', tone: 'ink' }
  return undefined
})
const choiceText = (row: DeskItem, choice: DeskItem['choices'][number]) => {
  if (row.kind === 'approval') return choice.id === 'approved' ? `${cap(askerName.value)} goes ahead.` : `${cap(askerName.value)} stops and sees your reason.`
  if (row.kind === 'action') return choice.id === 'reply' ? 'It gets your message in its inbox, as a reply, and carries on.' : choice.id === 'resolved' ? 'Marks it resolved.' : 'Dismisses it.'
  if (row.kind === 'rule') return choice.id === 'propose' ? 'Opens the pull request, as the Doctrine inbox does today; you merge it as usual.' : `${cap(askerName.value)} gets your reason.`
  if (choice.id === CUSTOM_ANSWER) return ''
  return choice.description
}
const choiceTitle = (row: DeskItem, choice: DeskItem['choices'][number]) => row.kind === 'action' && choice.id === 'reply' ? `Reply to ${askerName.value}` : choice.title
const fieldPlaceholder = (row: DeskItem) => row.kind === 'action' ? 'Your reply; typing here picks it' : 'Your own answer; typing here picks it'
const noteField = computed(() => {
  const row = item.value
  if (!row || nativePending.value || row.kind === 'stepup' || row.kind === 'key_trim') return undefined
  if (row.kind === 'approval') return { label: 'Reason', aria: 'Reason', placeholder: `${askerName.value} sees it (optional)` }
  if (row.kind === 'rule') return { label: 'Reason', aria: 'Reason', placeholder: `Needed for Not now; ${askerName.value} sees it` }
  if (row.kind === 'action' || row.kind === 'tier') return { label: 'P.S.', aria: 'P.S. for the agent', placeholder: 'A note for the agent (optional)' }
  return { label: 'P.S.', aria: 'P.S. for the agent', placeholder: `A note for ${askerName.value} (optional)` }
})
// Rule diffs read line by line: a removed line and its replacement, each with a full tint (no edge bar).
const ruleLines = computed(() => {
  const parts = item.value?.rule?.diff ?? []
  const lines: { op: 'del' | 'ins' | 'eq'; text: string }[] = []
  let current: { op: string; text: string }[] = []
  const flush = () => {
    if (!current.length) return
    const changed = current.some(part => part.op !== 'eq')
    const text = (ops: string[]) => current.filter(part => ops.includes(part.op)).map(part => part.text).join('')
    if (!changed) lines.push({ op: 'eq', text: text(['eq']) })
    else {
      if (current.some(part => part.op === 'del')) lines.push({ op: 'del', text: text(['eq', 'del']) })
      if (current.some(part => part.op === 'ins')) lines.push({ op: 'ins', text: text(['eq', 'ins']) })
    }
    current = []
  }
  for (const part of parts) {
    const pieces = part.text.split('\n')
    pieces.forEach((piece, at) => { if (at) flush(); if (piece) current.push({ op: part.op, text: piece }) })
  }
  flush()
  // Without a word diff, the rule today is removed and the proposed text inserted, line by line.
  if (!lines.length && item.value?.rule) {
    const { base, proposed } = item.value.rule
    if (base && base !== proposed) lines.push(...base.split('\n').filter(Boolean).map(text => ({ op: 'del' as const, text })))
    if (proposed) lines.push(...proposed.split('\n').filter(Boolean).map(text => ({ op: 'ins' as const, text })))
  }
  return lines.slice(0, 400)
})
const legend = computed<LegendEntry[]>(() => {
  const row = item.value
  if (!row) return []
  const move: LegendEntry = { keys: ['left', 'right'], or: ['J', 'K'], text: 'move' }
  if (decidedView.value) return [{ keys: ['enter'], text: 'next open item' }, ...(question.value ? [{ keys: ['C'], text: 'change this decision' }] : []), move, ...(project.value?.href ? [{ keys: ['P'], text: 'open the project' }] : []), { keys: ['esc'], text: 'close' }]
  if (nativePending.value) return [{ keys: ['enter'], text: 'approve' }, move, { keys: ['esc'], text: 'close' }]
  const n = Math.min(9, row.choices.length)
  return [{ keys: ['up', 'down'], range: n ? ['1', String(n)] as [string, string] : undefined, text: 'choose' }, ...(question.value ? [{ keys: ['O', 'A', 'R', 'D'], text: 'stamp' }] : []),
    { keys: ['enter'], text: lastOpen.value ? 'decide' : 'decide & next' }, { keys: ['mod', 'enter'], text: 'while typing' }, { keys: ['S'], text: 'skip' }, move,
    ...(project.value?.href ? [{ keys: ['P'], text: 'open the project' }] : []), { keys: ['esc'], text: 'leaves a field, then closes' }]
})
const fold = ref(false), keysOpen = ref(false)
// The done view's summary: what this round decided, and when the last answer goes out.
const doneSummary = computed(() => {
  const rows = done.value.map(id => props.items.find(row => row.id === id)).filter((row): row is DeskItem => !!row)
  const questions = rows.filter(row => row.kind === 'question' || row.kind === 'handover'), other = rows.length - questions.length
  const keep = questions.length === 1 ? `, stamped ${outcomeLabels[questions[0]!.outcome]}` : ''
  const parts = [questions.length ? `${questions.length} answered${keep}` : '', other ? `${other} other decision${other > 1 ? 's' : ''}` : ''].filter(Boolean)
  const last = rows.at(-1)
  const grace = last && (last.kind === 'question' || last.kind === 'handover') && last.deliveryState === 'pending' && graceLeft(last) > 0 ? last : undefined
  return { text: parts.length ? `${parts.join(' · ')}.` : 'Nothing was decided in this round.', grace, agent: grace ? whoOf(grace)?.name ?? 'the agent' : '' }
})

let pendingDecisionFocus: { id: string; revision: number } | undefined
watch([() => item.value?.id, () => item.value?.revision, primaryBlocked], ([id, revision, disabled]) => {
  const pending = pendingDecisionFocus
  if (!pending) return
  if (id !== pending.id || revision !== pending.revision) { pendingDecisionFocus = undefined; return }
  if (!disabled) {
    pendingDecisionFocus = undefined
    // A permission read may finish after the next memo renders. Restore the
    // action only if the person has not already moved to another control.
    if (live && document.activeElement === memo.value) decideButton.value?.focus({ preventScroll: true })
  }
}, { flush: 'post' })
function focusDecision() {
  pendingDecisionFocus = undefined
  if (!decideButton.value || !item.value) return
  if (primaryBlocked.value) {
    pendingDecisionFocus = { id: item.value.id, revision: item.value.revision }
    memo.value?.focus({ preventScroll: true })
  } else decideButton.value.focus({ preventScroll: true })
}
watch(item, async (current, previous) => {
  const turn = ++contextGeneration
  if (current && current.id !== shown?.id) {
    const here = deskProject(current)
    switchedFrom.value = projectSwitch(shown?.project, here); shown = { id: current.id, project: here }
    if (switchedFrom.value) announcement.value = `${announcement.value} Now in ${here.name}. The item before was in ${switchedFrom.value.name}.`.trim()
  }
  if (current && (!drafts.value[current.id] || current.decided && current.id === previous?.id && !previous.decided && current.kind !== 'question' && current.kind !== 'handover')) drafts.value[current.id] = draftFor(current)
  if (current?.id !== previous?.id) { editing.value = ''; pager.value = false; keysOpen.value = false; brokenThumbs.value = new Set() }
  // A source that changed under the memo replaces any passing note with its lasting restriction.
  else if (current?.unavailable && !previous?.unavailable) status.value = ''
  // One read per ticket for this memo session: revisiting an item shows its context at once.
  const ticketKey = current?.ticketId || current?.ticketKey || ''
  context.value = contexts.get(ticketKey) ?? { attachments: [], related: [], outcomes: [], warnings: [] }; contextLoading.value = false
  clearTimeout(expiryTimer)
  expired.value = !!current?.expiresAt && Date.parse(current.expiresAt) <= Date.now()
  if (current?.expiresAt && !expired.value) expiryTimer = setTimeout(() => { expired.value = true; status.value = 'This request expired. No permission was granted.'; announcement.value = status.value }, Math.min(2_147_483_647, Math.max(0, Date.parse(current.expiresAt) - Date.now())))
  void nextTick(measure)
  if (ticketKey && !contexts.has(ticketKey)) {
    contextLoading.value = true
    const result = await loadDeskContext(current!.ticketId ?? '', current!.ticketKey)
    if (!live) return
    if (!result.warnings.length) contexts.set(ticketKey, result)
    if (turn === contextGeneration && item.value?.id === current!.id) { context.value = result; contextLoading.value = false }
  }
}, { immediate: true })
function touch() { if (draft.value) draft.value.dirty = true; if (!busy.value) status.value = '' }
function setStatus(text: string) {
  status.value = text; announcement.value = text
  // Narrow desks have no room for the status beside the buttons: it shows as a toast instead.
  clearTimeout(narrowToast)
  if (narrow.value && text) { toastText.value = text; narrowToast = setTimeout(() => { toastText.value = '' }, 3200) }
}
const toastText = ref(''), narrow = ref(false)
async function focusField(name: string) {
  editing.value = name
  await nextTick()
  const field = dialog.value?.querySelector<HTMLInputElement>(`[data-field="${name}"]`)
  field?.focus(); field?.setSelectionRange(field.value.length, field.value.length)
}
function finishEditing() {
  const label = editing.value === 'answer' ? (item.value?.kind === 'action' ? 'reply' : 'answer') : item.value?.kind === 'approval' || item.value?.kind === 'rule' ? 'reason' : 'note'
  editing.value = ''; setStatus(`Your ${label} is set. Enter decides.`)
  memo.value?.focus({ preventScroll: true })
}
function choose(id: string) {
  if (!editable.value || !draft.value) return
  if (item.value?.choices.find(choice => choice.id === id)?.unavailable) return
  draft.value.optionId = id; touch()
  if (item.value?.choices.find(choice => choice.id === id)?.field) void focusField('answer')
}
function typed(id: string) { if (draft.value && draft.value.optionId !== id) draft.value.optionId = id; touch() }
function stamp(outcome: DeskOutcome) {
  if (!item.value || !draft.value) return
  const why = stampWhy(item.value, outcome)
  if (why) { setStatus(why); return }
  if (!editable.value) return
  draft.value.outcome = outcome; touch()
}
function navigate(delta: number, notice = '') {
  if (busy.value || finished.value || props.round.length < 2) return
  index.value = (index.value + delta + props.round.length) % props.round.length
  const next = props.items.find(item => item.id === props.round[index.value])
  announcement.value = `${notice ? notice + ' ' : ''}Memo ${index.value + 1} of ${props.round.length}. ${next?.title ?? 'Unavailable item'}`
}
function jumpTo(at: number, notice = '') {
  index.value = at
  const next = props.items.find(item => item.id === props.round[at])
  announcement.value = `${notice ? notice + ' ' : ''}Memo ${at + 1} of ${props.round.length}. ${next?.title ?? 'Unavailable item'}`
}
// The next open item after this one, then from the start; skipped items only when nothing else is open.
function nextOpen(from: number, includeSkipped = false) {
  for (let step = 1; step <= props.round.length; step++) {
    const at = (from + step) % props.round.length, id = props.round[at]!
    if (at !== from && isOpen(id) && (includeSkipped || !skipped.value.has(id))) return at
  }
  return -1
}
function declineOrSkip() { if (ghost.value === 'decline') void submit('decline'); else if (ghost.value === 'change') changeDecision(); else if (ghost.value === 'skip') skip() }
function skip() {
  const current = item.value
  if (!current || busy.value || finished.value) return
  if (changing.value.has(current.id)) {
    // A changed answer that is skipped stays as it was recorded.
    const kept = new Set(changing.value); kept.delete(current.id); changing.value = kept; drafts.value[current.id] = draftFor(current)
    const next = nextOpen(index.value)
    if (next >= 0) jumpTo(next, 'Kept as it was.')
    setStatus('Kept as it was.'); return
  }
  skipped.value = new Set([...skipped.value, current.id])
  const next = nextOpen(index.value)
  if (next < 0) { dialog.value?.close(); emit('close', { afterLast: false, skipped: true }); return }
  jumpTo(next, 'Skipped. It stays on the desk.'); setStatus('Skipped. It stays on the desk.')
}
// Change reopens a decided answer for a replacement; inside the grace that is the Undo (AEON-1340 adds a real withdraw).
function changeDecision(id = item.value?.id) {
  const row = props.items.find(row => row.id === id)
  if (!row || !id || busy.value || (row.kind !== 'question' && row.kind !== 'handover') || !row.decided) return
  clearTimeout(closeTimer); finished.value = false; holdClose.value = false
  changing.value = new Set([...changing.value, id])
  drafts.value[id] = { ...draftFor(row), dirty: false }
  const at = props.round.indexOf(id)
  if (at >= 0) index.value = at
  setStatus(row.deliveryState === 'pending' && graceLeft(row) > 0 ? 'Change your answer before it goes out. Closing keeps it as it is.' : 'Change your answer. Deciding again sends a correction.')
  void nextTick(() => memo.value?.focus({ preventScroll: true }))
}
function advance(decided: string, notice: string) {
  done.value = [...done.value.filter(id => id !== decided), decided]
  const next = nextOpen(index.value)
  if (next >= 0) { jumpTo(next, notice); return }
  // Skipped items are still open: go back to them instead of closing.
  const back = nextOpen(index.value, true)
  if (back >= 0) { const id = props.round[back]!; skipped.value.delete(id); jumpTo(back, `${notice} Back to the one you skipped.`); status.value = 'Back to the one you skipped.'; return }
  allDecided(notice)
}
// The last decision: "All decided" for 1.2 s, with Undo, then the desk closes back to where it was opened.
function allDecided(notice = '') {
  finished.value = true; status.value = ''; pager.value = false
  announcement.value = `${notice} All decided. The desk closes by itself.`.trim()
  scheduleClose()
  void nextTick(() => decideButton.value?.focus({ preventScroll: true }))
}
function scheduleClose() {
  clearTimeout(closeTimer)
  closeTimer = setTimeout(() => { if (holdClose.value) { scheduleClose(); return } closeAfterLast() }, 1200)
}
function closeAfterLast() {
  const last = props.items.find(row => row.id === done.value.at(-1))
  dialog.value?.close(); emit('close', { afterLast: true, last })
}
async function submit(trimAction?: 'approve' | 'decline' | 'restore') {
  if (finished.value) { closeAfterLast(); return }
  const current = item.value
  if (current && decidedView.value && !(current.kind === 'key_trim' && trimRestorable.value)) { const next = nextOpen(index.value, true); if (next >= 0) jumpTo(next); else close(); return }
  const trim = (current?.kind === 'key_trim' && (!current.decided || trimRestorable.value)) || (current?.kind === 'stepup' && !current.decided)
  const sentDraft = trim && draft.value ? { ...draft.value, optionId: trimAction ?? (current?.decided ? 'restore' : 'approve'), dirty: true } : draft.value
  if (!current || !sentDraft || busy.value) return
  if (blocked.value && !(trimAction === 'decline' && trimDeclinable.value)) { explain(); return }
  if (!trim && decideBlock.value.why) { explain(); return }
  if (!trim && !answerFor(current, sentDraft)) { explain(); return }
  const capturedId = current.id, capturedRevision = current.revision, capturedIndex = index.value
  const payload = { ...sentDraft }, requestKey = JSON.stringify([capturedId, capturedRevision, payload.optionId])
  const requestId = trim ? trimRequests.get(requestKey) ?? crypto.randomUUID() : crypto.randomUUID()
  if (trim) trimRequests.set(requestKey, requestId)
  busy.value = true; slam.value = !trim && !!impression.value
  status.value = current.kind === 'stepup' && payload.optionId === 'approve' ? (props.stepupMethod === 'Sign in again' ? 'Opening the fresh sign-in…' : 'Waiting for your passkey…') : 'Recording the decision…'
  let navigating = false
  try {
    decision = new AbortController()
    const result = await props.decide({ ...current }, payload, requestId, decision.signal)
    if (!live) return
    if (item.value?.id !== capturedId || item.value.revision !== capturedRevision || index.value !== capturedIndex || (item.value.unavailable && !(trimAction === 'decline' && item.value.unavailable === current.keyTrim?.blocked_reason)) || !props.allowed(item.value)) {
      slam.value = false; busy.value = false
      setStatus('The source changed while recording. Reopen it to confirm the result.')
      return
    }
    drafts.value[capturedId] = draftFor(result); skipped.value.delete(capturedId)
    const reopened = new Set(changing.value); reopened.delete(capturedId); changing.value = reopened
    emit('recorded', result); now.value = Date.now()
    const notice = `Decision recorded for ${current.title}. ${deliveryNote(result)}`.trim()
    announcement.value = notice
    status.value = ''
    await landed()
    slam.value = false
    await nextTick()
    if (live) {
      busy.value = false
      advance(capturedId, notice); await nextTick(); if (!finished.value) focusDecision()
    }
  } catch (cause) {
    // The fresh sign-in replaces this page; the desk reopens the request afterwards.
    if (cause instanceof StepupNavigation) { navigating = true; slam.value = false; setStatus(cause.message); return }
    if (live && item.value?.id === capturedId) {
      busy.value = false
      const why = cause instanceof Error ? cause.message : 'The decision was not confirmed. Your answer is kept.'
      setStatus(why); announcement.value = `Decision not confirmed. ${why}`
    }
  } finally { if (live && !navigating) { busy.value = false; slam.value = false } }
}
// What the server said about delivery, for the announcement after a decision.
function deliveryNote(row: DeskItem) { return row.kind === 'question' || row.kind === 'handover' ? (row.deliveryState === 'pending' ? 'It goes out after the ten-second grace.' : '') : row.delivery ?? '' }
// The stamp slams only on Decide (none with reduced motion); the round waits for it to land.
function landed() {
  if (!slam.value || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return Promise.resolve()
  return new Promise<void>(resolve => {
    const stampEl = dialog.value?.querySelector('[data-testid="desk-stamp"]')
    const timer = setTimeout(resolve, 460)
    stampEl?.addEventListener('animationend', () => { clearTimeout(timer); resolve() }, { once: true })
  })
}
// A Decide that can't decide yet explains itself beside the button and moves focus to what is missing.
function explain() {
  const block = decideBlock.value
  if (!block.why) return
  setStatus(block.why)
  void nextTick(() => {
    const root = dialog.value
    const target = block.focus === 'choice' ? root?.querySelector<HTMLElement>('[data-testid="desk-choices"] button:not(:disabled)')
      : block.focus === 'answer' ? root?.querySelector<HTMLElement>('[data-field="answer"]')
      : block.focus === 'reason' ? root?.querySelector<HTMLElement>('[data-field="reason"]')
      : block.focus === 'stamp' ? root?.querySelector<HTMLElement>('[data-testid="desk-stamps"] button:not([aria-disabled="true"])') ?? root?.querySelector<HTMLElement>('[data-testid="desk-stamps"] button') : null
    if (target) { target.focus({ preventScroll: false }); target.scrollIntoView?.({ block: 'nearest' }); if (block.focus === 'answer' || block.focus === 'reason') editing.value = block.focus }
  })
}
async function editRuleProposal() {
  const current = item.value
  if (!current || current.kind !== 'rule' || current.decided || blocked.value || !props.editRule) return
  const id = current.id, revision = current.revision
  busy.value = true
  try { await props.editRule({ ...current }) }
  catch (cause) { if (live && item.value?.id === id && item.value.revision === revision) setStatus(cause instanceof Error ? cause.message : 'The native proposal editor could not be opened.') }
  finally { if (live) busy.value = false }
}
async function revokeGrant() {
  const current = item.value
  if (!current || busy.value || !props.revoke || !props.canRevoke?.(current)) return
  const id = current.id, revision = current.revision
  busy.value = true; status.value = 'Revoking the grant…'
  try {
    const result = await props.revoke({ ...current })
    if (!live || item.value?.id !== id || item.value.revision !== revision) return
    emit('recorded', result)
    await nextTick()
    if (!live || item.value?.id !== id || item.value.revision !== revision) return
    revokedHere.value = new Set([...revokedHere.value, id]); setStatus('The grant was revoked. The original decision stays in history.')
  } catch (cause) {
    if (live && item.value?.id === id) setStatus(cause instanceof Error ? cause.message : 'Revocation was not confirmed.')
  } finally { if (live) busy.value = false }
}
function openPager() { if (busy.value || finished.value) return; pager.value = !pager.value; jumpIndex.value = index.value; if (pager.value) void nextTick(() => dialog.value?.querySelector<HTMLElement>('[role="listbox"]')?.focus()) }
function pickJump(at: number) { if (busy.value) return; pager.value = false; status.value = ''; jumpTo(at); void nextTick(() => pagerButton.value?.focus({ preventScroll: true })) }
function close() { if (viewing.value) return; if (finished.value) { closeAfterLast(); return } dialog.value?.close(); emit('close') }
function openProject() {
  const href = project.value?.href
  if (!href) return
  // A new tab without access back to the desk; the desk stays as it is.
  const opened = window.open(href, '_blank')
  if (opened) opened.opener = null
}
function keys(event: KeyboardEvent) {
  if (viewing.value || event.isComposing || event.defaultPrevented || (event.repeat && event.key === 'Enter')) return
  const field = fieldTarget(event.target)
  if (event.key === 'Enter' && submitModifier(event, mac)) { event.preventDefault(); if (field) (event.target as HTMLElement).blur(); void submit(); return }
  // All other browser/OS combinations remain native, including Shift navigation.
  if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return
  if (field) {
    if (event.key === 'Enter' || event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); finishEditing() }
    return
  }
  if (pager.value) {
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); pager.value = false; pagerButton.value?.focus() }
    else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); jumpIndex.value = (jumpIndex.value + (event.key === 'ArrowDown' ? 1 : -1) + props.round.length) % props.round.length; void nextTick(() => dialog.value?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' })) }
    else if (event.key === 'Enter') { event.preventDefault(); pickJump(jumpIndex.value) }
    return
  }
  if (keysOpen.value && event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); keysOpen.value = false; return }
  const key = event.key.toLowerCase()
  if (finished.value) {
    if (key === 'escape' || (key === 'enter' && !(event.target instanceof HTMLButtonElement && event.target !== decideButton.value))) { event.preventDefault(); event.stopPropagation(); closeAfterLast() }
    return
  }
  if (/^[1-9]$/.test(key)) { event.preventDefault(); const choice = item.value?.choices[Number(key) - 1]; if (choice) choose(choice.id) }
  else if (outcomeKeys.includes(key.toUpperCase()) && question.value) { const outcome = outcomes[outcomeKeys.indexOf(key.toUpperCase())]!; if (item.value && editable.value && !outcomeUnavailable(item.value, outcome)) { event.preventDefault(); stamp(outcome) } }
  else if (key === 'j' || key === 'arrowright') { event.preventDefault(); status.value = ''; navigate(1) }
  else if (key === 'k' || key === 'arrowleft') { event.preventDefault(); status.value = ''; navigate(-1) }
  else if (key === 'arrowup' || key === 'arrowdown') {
    event.preventDefault()
    if (!editable.value) return
    const choices = item.value?.choices ?? [], at = choices.findIndex(choice => choice.id === draft.value?.optionId)
    const choice = choices[(at + (key === 'arrowdown' ? 1 : -1) + choices.length) % choices.length]; if (choice) choose(choice.id)
  } else if (key === 's' && ghost.value === 'skip') { event.preventDefault(); skip() }
  else if (key === 'c' && ghost.value === 'change') { event.preventDefault(); changeDecision() }
  else if (key === 'p') { if (project.value?.href) { event.preventDefault(); openProject() } }
  else if (key === 'enter' && (event.target === memo.value || event.target === decideButton.value)) { event.preventDefault(); void submit() }
  else if (key === 'escape') { event.preventDefault(); event.stopPropagation(); close() }
}
async function showAttachment(at: number) { const attachment = context.value.attachments[at]; if (!attachment) return; viewing.value = true; await nextTick(); lightbox.value?.open(attachment.id) }

// ---------- The sheet outline: one path for tab, shoulder and sheet (Markus 10-14 09:47) ----------
// Every arc uses the sheet corners' radius on a half-pixel inset, snapped to the device pixel
// grid, so the 1 px hairline keeps one weight through both junctions and the fill has no seam.
const R_ARC = 14
const sheetPath = ref('')
function drawSheet() {
  const svg = sheet.value, tabEl = tab.value, stackEl = stack.value
  if (!svg || !tabEl || !stackEl) return
  const s = stackEl.getBoundingClientRect(), t = tabEl.getBoundingClientRect(), o = svg.getBoundingClientRect()
  if (!s.width || !s.height || !t.width) { sheetPath.value = ''; return }
  const dpr = window.devicePixelRatio || 1, snap = (v: number) => Math.round(v * dpr) / dpr, f = (n: number) => +n.toFixed(3)
  const X = (v: number) => snap(v) - o.left, Y = (v: number) => snap(v) - o.top, h = 0.5, r = R_ARC - h
  const L = X(s.left) + h, R = X(s.right) - h, T = Y(t.top) + h, S0 = Y(s.top) + h, B = Y(s.bottom) - h, TR = X(t.right) - h
  sheetPath.value = `M${f(L)} ${f(B - r)}V${f(T + r)}A${r} ${r} 0 0 1 ${f(L + r)} ${f(T)}H${f(TR - r)}A${r} ${r} 0 0 1 ${f(TR)} ${f(T + r)}V${f(S0 - r)}A${r} ${r} 0 0 0 ${f(TR + r)} ${f(S0)}H${f(R - r)}A${r} ${r} 0 0 1 ${f(R)} ${f(S0 + r)}V${f(B - r)}A${r} ${r} 0 0 1 ${f(R - r)} ${f(B)}H${f(L + r)}A${r} ${r} 0 0 1 ${f(L)} ${f(B - r)}Z`
}
// The legend never wraps: when it does not fit on one line, it folds into Keys (same height).
function fitLegend() {
  const el = hint.value
  if (!el) return
  fold.value = false
  void nextTick(() => { if (el.clientWidth && el.scrollWidth > el.clientWidth + 0.5) fold.value = true })
}
// Pattern A grows downward until the paper needs scrolling. From that point the series keeps its
// frame (AEON-541 rule 2), even for a later short memo. Phones are full height already.
const lockedHeight = ref<number>()
function measure() {
  drawSheet(); fitLegend()
  narrow.value = window.matchMedia('(max-width: 688px)').matches
  const paper = memo.value
  if (!lockedHeight.value && !narrow.value && paper && frame.value && paper.scrollHeight > paper.clientHeight + 1) lockedHeight.value = frame.value.getBoundingClientRect().height
}
let raf = 0
function scheduleMeasure() { cancelAnimationFrame(raf); raf = requestAnimationFrame(measure) }
function resized() { lockedHeight.value = undefined; scheduleMeasure() }
onMounted(() => {
  observer = new ResizeObserver(scheduleMeasure)
  for (const el of [frame.value, stack.value, tab.value]) if (el) observer.observe(el)
  window.addEventListener('resize', resized)
  clock = setInterval(() => { now.value = Date.now() }, 1000)
  measure()
})
watch(() => [finished.value, decidedView.value, keysOpen.value], () => void nextTick(scheduleMeasure))
watch(dialog, element => {
  if (!element) return
  opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
  element.showModal(); void nextTick(() => { memo.value?.focus({ preventScroll: true }); if (props.change) changeDecision(props.change) })
})
onBeforeUnmount(() => { window.removeEventListener('resize', resized); live = false; decision?.abort(); contextGeneration++; clearTimeout(expiryTimer); clearTimeout(closeTimer); clearTimeout(narrowToast); clearInterval(clock); cancelAnimationFrame(raf); observer?.disconnect(); dialog.value?.close(); if (opener?.isConnected) opener.focus({ preventScroll: true }) })
</script>

<template>
  <dialog ref="dialog" class="desk-dlg" aria-label="Decision Desk memo" @keydown="keys" @cancel.prevent="close">
    <div ref="frame" class="desk" :style="lockedHeight ? { height: `${lockedHeight}px` } : undefined" data-testid="desk-frame">
      <svg ref="sheet" class="sheet-shape" aria-hidden="true" focusable="false"><path :d="sheetPath" /></svg>
      <div class="desk-top desk-toolbar" data-testid="desk-actions">
        <div ref="tab" class="folder-tab" data-testid="desk-tab">
          <span v-if="project?.key && !finished" class="ft-key" :class="{ switched: !!switchedFrom }" :data-tip="switchedFrom ? `${project.name}: another project than the item before (${switchedFrom.name})` : project.name" data-testid="desk-tab-project">{{ project.key }}</span>
          <span class="ft-kind">{{ finished ? 'Round finished' : item ? kindLabels[item.kind] : 'Memo' }}</span>
          <button v-if="round.length > 1 && !finished" ref="pagerButton" class="ft-pager" type="button" aria-haspopup="listbox" :aria-expanded="pager" :disabled="busy" data-tip="All items in this round" data-testid="desk-pager" @click="openPager">{{ index + 1 }} of {{ round.length }}<AppIcon name="chevron" :size="13" /></button>
        </div>
        <span id="desk-status" class="desk-status" role="status" data-testid="desk-status">{{ topStatus }}<template v-if="!topStatus && arrivalsCount"><span class="arrival-hint">{{ arrivalsCount }} new for the next round</span></template></span>
        <div class="desk-acts action-buttons">
          <span v-if="nativeSlot" class="native-action">
            <button v-if="revoke" type="button" class="desk-btn ghost sm" :style="{ visibility: item?.kind === 'approval' && item.decided && item.optionId === 'approved' ? 'visible' : 'hidden' }" :disabled="busy || !item || !canRevoke?.(item)" data-testid="desk-revoke" @click="revokeGrant">Revoke</button>
            <button v-if="editRule" type="button" class="desk-btn ghost sm" :style="{ visibility: item?.kind === 'rule' && !item.decided ? 'visible' : 'hidden' }" :disabled="blocked || !editable" data-testid="desk-edit-rule" @click="editRuleProposal">Edit</button>
          </span>
          <button type="button" class="desk-btn ghost" :class="{ slot: !ghost }" :aria-hidden="!ghost || undefined" :tabindex="ghost ? undefined : -1" :disabled="busy || !ghost || (ghost === 'decline' && ((item?.kind === 'key_trim' && !trimDeclinable) || (stepupPending && blocked)))" :aria-keyshortcuts="ghost === 'skip' ? 'S' : ghost === 'change' ? 'C' : undefined" data-testid="desk-skip" @click="declineOrSkip"><span class="lbl">{{ ghost === 'decline' ? 'Decline' : ghost === 'change' ? 'Change' : 'Skip' }}</span><KeyCap v-if="ghost === 'skip'" k="S" /><KeyCap v-else-if="ghost === 'change'" k="C" /></button>
          <button ref="decideButton" class="desk-btn primary" type="button" :aria-disabled="primaryBlocked || undefined" :aria-describedby="topStatus ? 'desk-status' : undefined" aria-keyshortcuts="Enter" data-testid="desk-decide" @click="submit()">
            <AppIcon :name="finished || !decidedView ? 'check' : 'arrow'" :size="15" />
            <span v-if="stepupPending" class="stepup-approve"><span class="lbl">{{ primary }}</span><small data-testid="stepup-method">{{ stepupMethod || 'Passkey or sign-in' }}</small></span><span v-else class="lbl">{{ primary }}</span>
            <KeyCap k="enter" />
          </button>
        </div>
        <button class="desk-x" type="button" aria-label="Close memo" data-tip="Close · esc" data-testid="desk-close" @click="close"><AppIcon name="close" :size="16" /></button>
      </div>
      <div v-if="pager" class="jump" data-testid="desk-jump">
        <p class="jump-head" data-testid="desk-jump-head">{{ round.filter(id => items.find(row => row.id === id)?.decided).length }} decided · {{ round.filter(id => skipped.has(id) && !items.find(row => row.id === id)?.decided).length }} skipped · {{ round.filter(id => !items.find(row => row.id === id)?.decided && !skipped.has(id)).length }} open</p>
        <ol class="jump-list" role="listbox" tabindex="0" aria-label="Jump to a memo" :aria-activedescendant="`jump-${jumpIndex}`">
          <li v-for="(id, at) in round" :id="`jump-${at}`" :key="id" class="jump-row" :class="{ now: at === index, done: items.find(row => row.id === id)?.decided, skipped: skipped.has(id) }" role="option" :aria-selected="jumpIndex === at" :data-testid="`desk-jump-${at}`" @click="pickJump(at)">
            <span class="jr-n">{{ at + 1 }}</span>
            <span class="jr-kind" data-testid="desk-jump-project">{{ [items.find(row => row.id === id)?.projectKey, kindLabels[items.find(row => row.id === id)?.kind ?? 'question']].filter(Boolean).join(' · ') }}</span>
            <span class="jr-q">{{ items.find(row => row.id === id)?.title ?? 'Unavailable item' }}</span>
            <small class="jr-state">{{ items.find(row => row.id === id)?.decided ? 'Decided' : skipped.has(id) ? 'Skipped' : '' }}</small>
          </li>
        </ol>
      </div>
      <span class="sr-only" role="status" aria-live="polite" data-testid="desk-announcement">{{ announcement }}</span>
      <div ref="stack" class="stack">
        <article ref="memo" class="paper" :class="{ decided: decidedView }" tabindex="-1" data-testid="desk-paper">
          <div v-if="finished" class="done-view" data-testid="desk-all-decided">
            <span class="done-mark"><AppIcon name="check" :size="22" /></span>
            <h2>All decided</h2>
            <p class="done-sum">{{ doneSummary.text }}<template v-if="doneSummary.grace"> It goes to {{ doneSummary.agent }} in <span class="n">{{ graceLeft(doneSummary.grace) }}</span> s.</template></p>
            <p class="done-acts"><button v-if="doneSummary.grace" type="button" class="btn sm" data-testid="desk-undo" @pointerenter="holdClose = true" @pointerleave="holdClose = false" @focus="holdClose = true" @blur="holdClose = false" @click="changeDecision(doneSummary.grace.id)"><AppIcon name="rollback" :size="13" />Undo</button><span>The desk closes by itself.</span></p>
          </div>
          <template v-else-if="item && draft">
            <header class="memo-head memo-heading">
              <div class="stamp-slot" aria-live="polite">
                <span class="stamp-big" :class="[`t-${impression?.tone ?? 'ink'}`, { empty: !impression, landed: decidedView && !!impression, slam }]" :aria-label="impression ? (decidedView ? `Stamped ${impression.word}` : `Will be stamped ${impression.word}`) : 'Not decided yet'" data-testid="desk-stamp">{{ impression?.word ?? 'Not decided' }}</span>
              </div>
              <div class="memo-headtext">
                <h2 class="memo-q">{{ item.kind === 'action' && item.context && item.context.length <= 280 ? item.context : item.title }}</h2>
                <dl class="memo-meta" data-testid="desk-project-line">
                  <div v-if="project"><dt>Project</dt><dd><a v-if="project.href" class="proj-link" :href="project.href" target="_blank" rel="noopener" :data-tip="`Open ${project.name} in a new tab · key P`" data-testid="desk-project-link" ><AppIcon name="folder" :size="14" /><span>{{ project.name }}</span><AppIcon class="pj-open" name="external" :size="12" /></a><span v-else class="proj-quiet" data-testid="desk-project-link"><AppIcon name="folder" :size="13" />{{ project.name }}</span></dd></div>
                  <div v-if="asker || item.askers?.length"><dt>From</dt><dd><span class="from-one" :data-tip="asker?.model ? `${asker.model}` : undefined" data-testid="desk-from"><HarnessMark v-if="asker?.harness" :harness="asker.harness" :size="14" />{{ askerName }}</span></dd></div>
                  <div><dt>Re</dt><dd data-testid="desk-re"><template v-if="ticketRef"><RouterLink v-if="context.ticket" class="res-key" :to="`/work/${context.ticket.id}`">{{ ticketRef.key }}</RouterLink><span v-else class="res-key">{{ ticketRef.key }}</span><span v-if="ticketRef.title" class="re-title">{{ ticketRef.title }}</span></template><span v-else-if="item.ticketId || item.ticketKey" class="re-title faint">The ticket</span><span v-else class="re-title">{{ item.kind === 'rule' ? item.rule?.heading || item.title : item.kind === 'stepup' || item.kind === 'key_trim' ? kindLabels[item.kind] : 'Across tickets' }}</span></dd></div>
                  <div v-if="item.createdAt"><dt>Received</dt><dd>{{ ago(item.createdAt) }}</dd></div>
                </dl>
              </div>
              <p v-if="deliveryLine" class="memo-line" :class="deliveryLine.tone" data-testid="desk-line"><AppIcon :name="deliveryLine.icon" :size="14" /><span class="lt"><b>{{ deliveryLine.bold }}</b><template v-if="deliveryLine.grace"><span class="sep">·</span>sends to {{ askerName }} in <span class="n">{{ deliveryLine.grace }}</span> s<span class="sep">·</span><button type="button" class="linkish" data-testid="desk-line-undo" @click="changeDecision()">Undo</button></template><template v-else-if="deliveryLine.text"><span class="sep">·</span>{{ deliveryLine.text }}</template></span></p>
              <p v-else-if="meanLine" class="memo-line" :class="meanLine.tone" data-testid="desk-line"><AppIcon :name="meanLine.icon" :size="14" /><span class="lt"><b>{{ meanLine.bold }}</b> {{ meanLine.text }}</span></p>
              <p v-else class="memo-line" data-testid="desk-line" />
            </header>
            <div class="memo-body" data-testid="desk-body">
              <section v-if="item.keyTrim" class="memo-answer key-trim-evidence" aria-label="Key trim evidence" data-testid="key-trim-evidence">
                <h3 class="eyebrow">{{ item.keyTrim.key_name }} · {{ item.keyTrim.owner_name }}</h3>
                <p>Evidence observed {{ new Date(item.keyTrim.evidence.observed_at).toLocaleString() }}; snapshot recorded {{ new Date(item.keyTrim.created_at).toLocaleString() }}.</p>
                <h3 class="eyebrow">Scopes kept ({{ item.keyTrim.candidate_scopes.length }})</h3>
                <p class="scope-list">{{ item.keyTrim.candidate_scopes.join(', ') || 'None' }}</p>
                <h3 class="eyebrow">Scopes dropped ({{ item.keyTrim.evidence.risks.length }})</h3>
                <table><thead><tr><th>Scope / evidence</th><th>Risk if dropped</th></tr></thead><tbody><tr v-for="risk in item.keyTrim.evidence.risks" :key="risk.scope"><td><code>{{ risk.scope }}</code><p>{{ risk.evidence }}</p><small>{{ item.keyTrim.usage.find(use => use.scope === risk.scope)?.last_used_at ? `Last recorded use: ${new Date(item.keyTrim.usage.find(use => use.scope === risk.scope)!.last_used_at!).toLocaleString()}` : 'No recorded use; earlier use is unknown.' }}</small></td><td>{{ risk.risk_if_dropped }}</td></tr></tbody></table>
                <p>Usage timestamps can lag by up to one minute. Approval rechecks the last 24 hours and the exact live scope set.</p>
                <p v-if="item.keyTrim.restore_until">Restore until {{ new Date(item.keyTrim.restore_until).toLocaleString() }}. An intervening scope edit or changed grant authority can prevent restore.</p>
                <p v-if="item.keyTrim.state === 'restored'">Previous scopes restored: <span class="scope-list">{{ item.keyTrim.previous_scopes.join(', ') }}</span></p>
              </section>
              <section v-else-if="item.stepup && stepupChange" class="memo-answer stepup-change" aria-label="The change" data-testid="stepup-change">
                <div class="stepup-diff">
                  <div data-testid="stepup-before"><h3 class="eyebrow">{{ item.stepup.state === 'stale' ? 'Before · no longer current' : 'Before' }}</h3><dl><div v-for="row in stepupChange.before" :key="row.label" :class="{ changed: row.changed }"><dt>{{ row.label }}</dt><dd>{{ row.value }}</dd></div></dl></div>
                  <span class="stepup-arrow"><AppIcon name="arrow" :size="18" /></span>
                  <div data-testid="stepup-after"><h3 class="eyebrow">After</h3><dl><div v-for="row in stepupChange.after" :key="row.label" :class="{ changed: row.changed }"><dt>{{ row.label }}</dt><dd>{{ row.value }}</dd></div></dl></div>
                </div>
                <p v-if="item.decided" class="stepup-answer" data-testid="stepup-answer">{{ item.answer }}</p>
              </section>
              <section v-else class="memo-answer" aria-label="Your decision">
                <pre v-if="item.rule && ruleLines.length" class="diff" aria-label="The proposed change" data-testid="desk-rule-diff"><template v-for="(line, at) in ruleLines" :key="at"><del v-if="line.op === 'del'" class="ln"><span class="sg">−</span><span class="tx">{{ line.text }}</span></del><ins v-else-if="line.op === 'ins'" class="ln"><span class="sg">+</span><span class="tx">{{ line.text }}</span></ins><span v-else class="ln"><span class="sg" /><span class="tx">{{ line.text }}</span></span></template></pre>
                <p v-if="item.rule?.outdated" class="m-note">The rule changed since this was proposed. Edit against the current rule first.</p>
                <h3 id="desk-h-answer" class="eyebrow">Your decision</h3>
                <div class="choices" role="radiogroup" aria-labelledby="desk-h-answer" data-testid="desk-choices">
                  <div v-for="(choice, at) in item.choices" :key="choice.id" class="choice-li" :data-testid="`choice-row-${at}`">
                    <div class="choice" :class="{ checked: draft.optionId === choice.id, off: !!choice.unavailable, 'has-field': !!choice.field }">
                      <button type="button" class="choice-hit" role="radio" :aria-checked="draft.optionId === choice.id" :disabled="!editable || !!choice.unavailable" :data-tip="choice.unavailable || undefined" :data-testid="`choice-${at}`" @click="choose(choice.id)">
                        <span class="ck" aria-hidden="true" /><KeyCap v-if="at < 9" :k="String(at + 1)" /><span v-else />
                        <span class="c-text"><span class="c-name">{{ choiceTitle(item, choice) }}<span v-if="item.recommended === choice.id" class="rec-tag">Recommended</span></span><span v-if="choiceText(item, choice)" class="c-desc">{{ choiceText(item, choice) }}</span><span v-if="choice.unavailable" class="c-desc">{{ choice.unavailable }}</span><span v-if="item.recommended === choice.id && item.why" class="c-why"><AppIcon name="arrow" :size="12" />{{ item.why }}</span></span>
                      </button>
                      <span v-if="choice.field && !decidedView" class="field-row"><input v-model="draft.answer" data-field="answer" class="ul-field" :aria-label="choiceTitle(item, choice)" maxlength="8000" :disabled="!editable" :placeholder="fieldPlaceholder(item)" enterkeyhint="done" autocomplete="off" @input="typed(choice.id)" @focus="editing = 'answer'" @blur="editing === 'answer' && (editing = '')" /><span class="field-hint" aria-hidden="true"><KeyCap k="enter" /> done</span></span>
                      <span v-else-if="choice.field && draft.optionId === choice.id && draft.answer" class="c-answer" data-testid="desk-answer-set">{{ draft.answer }}</span>
                    </div>
                  </div>
                </div>
                <div v-if="question" class="stamps">
                  <h3 id="desk-h-stamp" class="eyebrow">Stamp it as</h3>
                  <div class="stamp-row" role="radiogroup" aria-labelledby="desk-h-stamp" data-testid="desk-stamps">
                    <button v-for="(outcome, at) in outcomes" :key="outcome" type="button" class="stamp" :class="`t-${STAMP[outcome].tone}`" role="radio" :aria-checked="draft.outcome === outcome" :aria-disabled="stampWhy(item, outcome) || decidedView || !editable ? 'true' : undefined" :data-off="stampWhy(item, outcome) ? '' : undefined" :data-faded="decidedView && draft.outcome !== outcome && !stampWhy(item, outcome) ? '' : undefined" :tabindex="decidedView ? -1 : undefined" :aria-keyshortcuts="stampWhy(item, outcome) ? undefined : outcomeKeys[at]" :data-tip="stampWhy(item, outcome) || `${stampDescription({ ...item, suggested: undefined }, outcome)} · key ${outcomeKeys[at]}`" :data-testid="`stamp-${outcome}`" @click="stamp(outcome)"><AppIcon :name="STAMP[outcome].icon" :size="13" /><span v-if="stampWhy(item, outcome)">{{ outcomeLabels[outcome] }}</span><span v-else><u>{{ outcomeLabels[outcome].charAt(0) }}</u>{{ outcomeLabels[outcome].slice(1) }}</span></button>
                  </div>
                  <p class="stamp-desc" data-testid="desk-stamp-desc">{{ stampDescription(item, draft.outcome) }}<template v-if="suggestedNote && !decidedView"> <span class="sug-off" tabindex="0" :data-tip="stampWhy(item, item.suggested ?? item.outcome)" data-testid="desk-suggest-off">{{ suggestedNote }}</span></template></p>
                </div>
                <p v-if="noteField && !decidedView" class="ps"><label class="ps-label" for="desk-note">{{ noteField.label }}</label><span class="field-row"><input id="desk-note" v-model="draft.reason" data-field="reason" class="ul-field" :aria-label="noteField.aria" maxlength="2000" :disabled="!editable" :placeholder="noteField.placeholder" enterkeyhint="done" autocomplete="off" @input="touch" @focus="editing = 'reason'" @blur="editing === 'reason' && (editing = '')" /><span class="field-hint" aria-hidden="true"><KeyCap k="enter" /> done</span></span></p>
                <p v-else-if="noteField && draft.reason" class="ps"><span class="ps-label">{{ noteField.label }}</span><span class="c-answer">{{ draft.reason }}</span></p>
                <p v-if="item.fromRecord && !item.decided" class="m-note">{{ item.fromRecord }}</p>
              </section>
              <aside class="margin context-column" aria-label="Background">
                <template v-if="item.approval">
                  <section class="m-sec"><h3 class="eyebrow">Why</h3><p>{{ item.approval.rationale || 'No reason supplied by the agent.' }}</p></section>
                  <section class="m-sec" aria-label="Permission request"><h3 class="eyebrow">What it runs</h3><p class="mono-line">{{ scopeLabel(item.approval.scope) }}</p><p>{{ item.approval.resource_kind === 'tenant' ? 'The whole workspace' : `${item.approval.resource_kind === 'node' ? 'Ticket' : 'Run'} ${item.approval.resource_id || 'not named'}` }}</p><p>{{ RISK_LABEL[riskFor(item.approval)] }}</p><p>Asked by {{ askerName }} · expires <time :datetime="item.approval.expires_at">{{ deliveryTime(item.approval.expires_at) }}</time></p><TargetSummary :approval="item.approval" /></section>
                </template>
                <template v-else-if="item.rule">
                  <section class="m-sec"><h3 class="eyebrow">Why</h3><p>{{ item.rule.why || item.context || 'No reason supplied by the proposer.' }}</p></section>
                  <section class="m-sec" aria-label="Rule proposal details"><h3 class="eyebrow">The rule today</h3><p>{{ item.rule.base || item.rule.label }}</p><p>A merged pull request changes it; agents pick it up with the next pin.</p><p v-if="item.prUrl"><a class="src-link" :href="item.prUrl" target="_blank" rel="noopener noreferrer">Open the doctrine pull request <AppIcon name="external" :size="12" /></a></p></section>
                </template>
                <template v-else-if="item.keyTrim || item.stepup">
                  <section class="m-sec"><h3 class="eyebrow">Why</h3><p>{{ item.context }}</p></section>
                  <section v-if="item.findings" class="m-sec"><h3 class="eyebrow">What it changes</h3><p class="pre">{{ item.findings }}</p></section>
                  <section class="m-sec"><h3 class="eyebrow">Afterwards</h3><p>{{ item.destination }}</p></section>
                </template>
                <template v-else>
                  <section v-if="item.kind === 'action' && item.context && item.context.length > 280" class="m-sec"><h3 class="eyebrow">The request</h3><MarkdownBody class="md" :body="item.context" /></section>
                  <section v-else-if="item.kind !== 'action' && item.context" class="m-sec"><h3 class="eyebrow">{{ item.kind === 'tier' ? 'Why' : 'Background' }}</h3><MarkdownBody class="md" :body="item.context" /></section>
                  <section v-if="item.findings && item.kind !== 'action'" class="m-sec"><h3 class="eyebrow">What the agent found</h3><MarkdownBody class="md" :body="item.findings" /></section>
                </template>
                <section v-if="item.ticketId || item.ticketKey" class="m-sec" data-testid="desk-ticket"><h3 class="eyebrow">From the ticket</h3>
                  <p v-if="contextLoading" role="status">Reading the ticket…</p>
                  <template v-if="context.ticket">
                    <p class="tk-line"><RouterLink class="res-key" :to="`/work/${context.ticket.id}`">{{ context.ticket.key }}</RouterLink><span>{{ context.ticket.state }}</span><template v-if="plainText(context.ticket.fields.priority)"><span class="tk-dot">·</span><span>{{ plainText(context.ticket.fields.priority) }} priority</span></template><a class="tk-open" :href="`/work/${context.ticket.id}`" target="_blank" rel="noopener" :data-tip="`Open ${context.ticket.key} in a new tab`">Open <AppIcon name="external" :size="12" /></a></p>
                    <p v-if="plainText(context.ticket.fields.pr_url)" class="tk-pr">Pull request <a :href="plainText(context.ticket.fields.pr_url)" target="_blank" rel="noopener noreferrer">{{ plainText(context.ticket.fields.pr_url).replace(/^https?:\/\/github\.com\//, '') }}</a><template v-if="context.outcomes.some(outcome => outcome.kind === 'ci_result')"> · {{ context.outcomes.filter(outcome => outcome.kind === 'ci_result').length }} check{{ context.outcomes.filter(outcome => outcome.kind === 'ci_result').length === 1 ? '' : 's' }} recorded</template></p>
                    <template v-if="plainText(context.ticket.fields.acceptance_criteria)"><p class="tk-sub">Acceptance criteria</p><ul><li v-for="(line, at) in plainText(context.ticket.fields.acceptance_criteria).split('\n').map(line => line.replace(/^\s*[-*]\s+/, '').trim()).filter(Boolean).slice(0, 20)" :key="at">{{ line }}</li></ul></template>
                  </template>
                  <p v-for="warning in context.warnings" :key="warning" class="context-warning">{{ warning }}</p>
                </section>
                <section v-if="context.related.length" class="m-sec"><h3 class="eyebrow">Related</h3><ul class="rel"><li v-for="related in context.related" :key="related.id"><p class="rel-top"><RouterLink :to="`/work/${related.id}`"><b>{{ related.key }}</b> {{ related.title }}</RouterLink></p></li></ul></section>
                <section v-if="question" class="m-sec" data-testid="desk-answer-goes"><h3 class="eyebrow">The answer goes</h3><p>To {{ askerName }}’s inbox<template v-if="(item.askers?.length ?? 0) > 1"> and {{ (item.askers?.length ?? 1) - 1 }} more agent{{ (item.askers?.length ?? 1) > 2 ? 's' : '' }} who asked the same</template><template v-if="ticketRef"><br>Also as a comment on {{ ticketRef.key }}</template></p></section>
                <section v-if="item.kind === 'action'" class="m-sec"><h3 class="eyebrow">The reply goes</h3><p>To {{ askerName }}’s inbox, as a reply to its request.</p></section>
                <section v-if="item.kind === 'approval'" class="m-sec"><h3 class="eyebrow">Afterwards</h3><p>Approvals land in Decided, where Revoke stays.</p></section>
                <section v-if="item.kind === 'tier'" class="m-sec"><h3 class="eyebrow">Afterwards</h3><p>Applied at the next safe point of the session.</p></section>
                <section v-if="item.outcomeEffects?.length && item.decided" class="m-sec" aria-label="Outcome effects" data-testid="desk-outcome-effects"><h3 class="eyebrow">Afterwards</h3>
                  <div v-for="effect in item.outcomeEffects" :key="effect.id" class="effect">
                    <p>{{ effect.state === 'failed' ? `The ${outcomeLabels[item.outcome]} outcome failed; the answer stays recorded.` : effect.state === 'pending' ? `The ${outcomeLabels[item.outcome]} outcome applies ${deliveryTime(effect.deliver_after)}.` : effect.state === 'replaced' ? 'Replaced by a correction.' : `The ${outcomeLabels[item.outcome]} outcome is applied.` }}</p>
                    <p v-if="effect.state === 'failed' && (effect.error_message || effect.error_code)">{{ effect.error_message || 'The source did not supply a reason.' }}</p>
                    <p v-if="effect.state === 'failed'">{{ effect.effect_data?.retryable === false ? 'It won’t retry by itself. Change the answer to try again.' : effect.effect_data?.retryable === true ? 'It retries by itself.' : '' }}</p>
                    <p v-if="effect.effect_data?.ticket_id"><RouterLink class="src-link" :to="`/work/${encodeURIComponent(effect.effect_data.ticket_id)}`">Open the ticket</RouterLink></p>
                    <p v-if="effect.effect_data?.knowledge_id"><a class="src-link" :href="`/api/nodes/${encodeURIComponent(effect.effect_data.knowledge_id)}`" target="_blank" rel="noopener noreferrer">Open the decision in Knowledge</a></p>
                    <p v-if="effect.effect_data?.doctrine_id"><RouterLink class="src-link" :to="doctrineReviewPath(effect.effect_data.doctrine_id, effect.doctrine_state ?? 'pending')">Review the doctrine draft</RouterLink></p>
                    <p v-for="review in effect.effect_data?.review_required" :key="`${review.kind}:${review.ref}`">{{ review.why }} <RouterLink class="src-link" :to="review.kind === 'criterion' ? `/work/${encodeURIComponent(review.ref)}` : doctrineReviewPath(review.ref, review.ref === effect.effect_data?.doctrine_id ? effect.doctrine_state : undefined)">{{ review.kind === 'criterion' ? 'Open the criterion to review' : 'Open the draft to review' }}</RouterLink></p>
                  </div>
                </section>
                <section v-if="context.attachments.length" class="m-sec"><h3 class="eyebrow">Attachments · {{ context.attachments.length }}</h3><div class="thumbs"><button v-for="(attachment, at) in context.attachments" :key="attachment.id" type="button" class="thumb" :aria-label="`Open ${attachment.caption || attachment.name}`" @click="showAttachment(at)"><span class="thumb-img"><img v-if="hasThumbnail(attachment) && !brokenThumbs.has(attachment.id)" :src="contentUrl(attachment.id, 'thumb')" alt="" @error="brokenThumbs.add(attachment.id)" /><span v-else class="thumb-kind">{{ fileKind(attachment) }}</span></span><span class="thumb-name" :data-tip="attachment.caption || attachment.name">{{ attachment.caption || attachment.name }}</span></button></div></section>
                <p v-if="item.source && !item.approval && (!!item.rule || (!item.ticketId && !item.ticketKey))" class="m-sec"><a v-if="item.source.startsWith('/api/')" class="src-link" :href="item.source" target="_blank" rel="noopener noreferrer">Source <AppIcon name="external" :size="12" /></a><RouterLink v-else class="src-link" :to="item.source">Source <AppIcon name="external" :size="12" /></RouterLink></p>
              </aside>
            </div>
          </template>
          <p v-else class="gone">This item is no longer accessible. Close the memo and refresh the desk.</p>
        </article>
      </div>
      <div v-if="!finished && item" class="desk-foot" :class="{ fold }" data-testid="desk-keys">
        <p ref="hint" class="desk-hint"><span v-for="(entry, at) in legend" :key="at"><template v-for="(k, i) in entry.keys" :key="k + i"><KeyCap :k="k" /></template><template v-if="entry.or"> or <KeyCap v-for="k in entry.or" :key="k" :k="k" /></template><template v-if="entry.range"> or <KeyCap :k="entry.range[0]" />–<KeyCap :k="entry.range[1]" /></template> {{ entry.text }}<AppIcon v-if="entry.text === 'open the project'" name="external" :size="11" /></span></p>
        <div class="keys"><button type="button" class="keys-btn" :aria-expanded="keysOpen" aria-controls="desk-keys-pop" @click="keysOpen = !keysOpen"><AppIcon name="keyboard" :size="15" />Keys</button>
          <dl v-show="keysOpen" id="desk-keys-pop" class="keys-pop pop"><div v-for="(entry, at) in legend" :key="at" class="kp-row"><dt><template v-for="(k, i) in entry.keys" :key="k + i"><KeyCap :k="k" /></template><template v-if="entry.or"> or <KeyCap v-for="k in entry.or" :key="k" :k="k" /></template><template v-if="entry.range"> or <KeyCap :k="entry.range[0]" />–<KeyCap :k="entry.range[1]" /></template></dt><dd>{{ entry.text }}</dd></div></dl>
        </div>
      </div>
      <div v-if="toastText" class="dlg-toast" role="status"><AppIcon name="alert" :size="15" /><span>{{ toastText }}</span></div>
    </div>
    <AttachmentLightbox ref="lightbox" :items="context.attachments" :ticket-key="context.ticket?.key || ''" :can-write="false" :set-caption="async () => false" @close="viewing = false" />
  </dialog>
</template>

<style scoped>
/* ---------- The desk as a dialog: anchored at the top, it grows downward ---------- */
.desk-dlg { width: min(1120px, calc(100vw - 48px)); max-width: none; max-height: none; margin: 7vh auto auto; padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.desk-dlg::backdrop { background: var(--scrim); backdrop-filter: blur(3px); }
/* A glass frame carries the controls on top; the memo below grows downward, so nothing the pointer uses ever moves. */
.desk { position: relative; isolation: isolate; display: grid; grid-template-columns: minmax(0, 320px) minmax(0, 1fr) auto auto; grid-template-rows: minmax(70px, auto) minmax(0, 1fr) auto; grid-template-areas: "tab status acts x" "stack stack stack stack" "foot foot foot foot"; column-gap: 12px; min-width: 0; max-height: calc(100dvh - 7vh - 16px); padding: 0 16px 14px; border: 1px solid var(--glass-edge); border-radius: 20px; background: linear-gradient(165deg, color-mix(in srgb, var(--aqua-3) 55%, var(--surface-raised-2)), var(--glass) 60%); -webkit-backdrop-filter: blur(24px) saturate(1.2); backdrop-filter: blur(24px) saturate(1.2); color: var(--ink); box-shadow: var(--shadow-pop), var(--shadow); }
.sheet-shape { position: absolute; left: 0; top: 0; z-index: 0; width: 100%; height: 100%; overflow: visible; pointer-events: none; }
.sheet-shape path { fill: var(--paper); stroke: var(--paper-edge); stroke-width: 1; stroke-linejoin: round; }
.desk-top { display: contents; }
.folder-tab, .desk-status, .desk-acts, .desk-x, .stack, .desk-foot { position: relative; z-index: 1; }
/* The folder tab: cut from the memo's paper by the one sheet outline. */
.folder-tab { grid-area: tab; align-self: end; display: flex; align-items: center; gap: 9px; min-width: 0; height: 44px; padding: 0 18px; color: var(--ink); font-size: 14px; font-weight: 650; white-space: nowrap; }
.ft-kind { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
/* The project once (Markus 10-14 09:45): its key in the ticket-key style, filled when the project changed. */
.ft-key { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 7px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 11.5px/1 var(--mono); font-variant-ligatures: none; letter-spacing: .02em; }
.ft-key.switched { background: color-mix(in srgb, var(--teal) 18%, var(--paper)); box-shadow: inset 0 0 0 1.5px var(--teal); }
.ft-pager { display: inline-flex; align-items: center; gap: 4px; flex: none; height: 30px; margin-left: auto; margin-right: -8px; padding: 0 8px 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font: 500 12.5px/1 var(--mono); font-variant-numeric: tabular-nums; }
@media (hover: hover) { .ft-pager:hover { background: var(--row-hover); color: var(--ink); } }
.ft-pager[aria-expanded="true"] { background: var(--row-selected); color: var(--ink); }
.ft-pager svg { color: var(--ink-3); }
.desk-status { grid-area: status; align-self: center; margin-top: 14px; min-width: 0; max-height: 2.8em; overflow: hidden; text-align: right; font-size: 12.5px; font-weight: 550; line-height: 1.4; color: var(--ink-2); }
.arrival-hint { color: var(--ink-3); font-weight: 450; }
.desk-acts { grid-area: acts; align-self: center; display: flex; align-items: center; gap: 10px; margin-top: 14px; padding-bottom: 8px; }
.desk-btn { display: inline-flex; align-items: center; justify-content: center; gap: 8px; height: 42px; padding: 0 22px; border: 0; border-radius: 999px; font-size: 13.5px; font-weight: 650; white-space: nowrap; cursor: pointer; }
.desk-btn.ghost { min-width: 128px; border: 1px solid var(--glass-edge); background: var(--btn-bg); color: var(--ink); box-shadow: var(--shadow-btn); }
.desk-btn.ghost.sm { min-width: 0; padding: 0 16px; }
.desk-btn.ghost.slot { visibility: hidden; }
@media (hover: hover) { .desk-btn.ghost:hover:not(:disabled) { background: var(--btn-bg-hover); box-shadow: var(--shadow-btn-hover); } }
.desk-btn :deep(.keycap) { margin-left: 2px; }
.desk-btn.primary { min-width: 212px; background: var(--brass), var(--brass-solid); color: var(--brass-ink); box-shadow: var(--brass-shadow); }
.desk-btn.primary :deep(.keycap) { background: var(--brass-keycap-bg); box-shadow: inset 0 0 0 1px var(--brass-keycap-line); color: var(--brass-ink); }
@media (hover: hover) { .desk-btn.primary:hover:not([aria-disabled="true"]) { filter: brightness(1.05); } }
/* Decide while it can't decide yet: muted, but it still answers a click with its reason. */
.desk-btn.primary[aria-disabled="true"] { opacity: .4; cursor: default; filter: none; }
.desk-btn:disabled { opacity: .4; cursor: default; }
.desk-btn.ghost.slot:disabled { opacity: 1; }
/* The primary keeps one width whatever its label (212 px holds the longest, "Replace & next"). */
.lbl { white-space: nowrap; }
.stepup-approve { display: inline-grid; justify-items: center; line-height: 1.2; }.stepup-approve small { font-size: 11px; font-weight: 500; opacity: .8; }
.native-action { display: grid; }.native-action > button { grid-area: 1 / 1; }
.desk-x { grid-area: x; align-self: center; display: grid; place-items: center; width: 40px; height: 40px; margin-top: 14px; margin-bottom: 8px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); cursor: pointer; }
@media (hover: hover) { .desk-x:hover { background: var(--row-hover); color: var(--ink); } }
.desk :is(.desk-btn, .desk-x, .ft-pager, .stamp, .choice-hit, .keys-btn, .linkish, .sug-off, .thumb, .proj-link, .res-key, .tk-open, .src-link):focus-visible { outline: none; box-shadow: var(--focus-ring); }
/* The jump list: an overlay under the folder tab, one row per item with its state. */
.jump { position: absolute; z-index: 6; top: 64px; left: 16px; width: min(520px, calc(100% - 32px)); max-height: min(420px, 60vh); overflow: auto; padding: 6px; border: 1px solid var(--glass-edge); border-radius: 14px; background: var(--surface-raised); box-shadow: var(--shadow-pop); color: var(--ink); }
.jump-head { margin: 0; padding: 6px 10px 8px; font-size: 12px; color: var(--ink-3); }
.jump-list { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; outline: none; }
.jump-row { display: grid; grid-template-columns: 22px 128px minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 38px; padding: 6px 10px; border-radius: 9px; font-size: 13px; cursor: pointer; }
@media (hover: hover) { .jump-row:hover { background: var(--row-hover); } }
.jump-row.now { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.jump-row[aria-selected="true"] { background: var(--row-selected); }
.jump-list:focus-visible .jump-row[aria-selected="true"] { box-shadow: var(--focus-ring); }
.jr-n { font: 500 12px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.jr-kind { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; color: var(--ink-2); }
.jr-q { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.jr-state { font-size: 12px; white-space: nowrap; color: var(--ink-3); }
.jump-row.done .jr-state { color: var(--ok); font-weight: 600; }
.jump-row.done .jr-q { color: var(--ink-2); font-weight: 500; }
.jump-row.skipped .jr-state { color: var(--ink-2); }
/* ---------- The memo: one scroll container, the sheet ---------- */
.stack { grid-area: stack; display: flex; flex-direction: column; min-height: 0; }
.paper { position: relative; flex: 0 1 auto; min-height: 0; margin: 1px; overflow: hidden auto; overscroll-behavior: contain; padding: 0 32px 30px; border-radius: 0 13px 13px 13px; background: transparent; color: var(--ink); box-shadow: var(--paper-shadow); scrollbar-width: thin; scrollbar-color: var(--line-2) transparent; outline: none; }
.desk[style] .stack, .desk[style] .paper { flex: 1 1 auto; }
.paper::-webkit-scrollbar { width: 10px; }
.paper::-webkit-scrollbar-thumb { border: 3px solid transparent; border-radius: 999px; background: var(--line-2) padding-box; }
/* The memo takes focus for its keys; its ring was the thick green outline in Markus's shot. */
.paper:focus, .paper:focus-visible { outline: none; }
.memo-head { display: grid; grid-template-columns: minmax(0, 1fr) 210px; gap: 0 20px; padding: 26px 0 18px; border-bottom: 1px solid var(--paper-line); }
.memo-head > .stamp-slot { grid-column: 2; grid-row: 1 / span 2; }
.memo-head > .memo-headtext { grid-column: 1; grid-row: 1; }
.memo-head > .memo-line { grid-column: 1; grid-row: 2; margin-top: 10px; }
.memo-headtext { display: grid; gap: 10px; min-width: 0; align-content: start; }
/* Where the stamp lands: a fixed slot at the top right; the stamp only ever moves by transform. */
.stamp-slot { display: grid; place-items: center; min-height: 92px; }
.stamp-big { --t: var(--ok); grid-area: 1 / 1; display: inline-flex; align-items: center; padding: 11px 16px; border: 2.5px solid var(--t); border-radius: 6px; background: color-mix(in srgb, var(--t) 7%, var(--paper)); box-shadow: inset 0 0 0 3px var(--paper), inset 0 0 0 4.5px color-mix(in srgb, var(--t) 80%, transparent); color: color-mix(in srgb, var(--t) 80%, var(--ink)); font: 800 17px/1 var(--mono); letter-spacing: .18em; text-transform: uppercase; white-space: nowrap; transform: rotate(-8deg); }
.stamp-big.empty { --t: var(--ink-3); border-style: dashed; border-color: color-mix(in srgb, var(--ink-3) 70%, transparent); background: transparent; box-shadow: none; font-size: 12px; font-weight: 600; letter-spacing: .14em; }
.stamp-big.landed { background: color-mix(in srgb, var(--t) 14%, var(--paper)); }
@media (prefers-reduced-motion: no-preference) {
  .stamp-big.slam { animation: stamp-slam .44s cubic-bezier(.3, .7, .3, 1) both; }
  @keyframes stamp-slam { 0% { transform: rotate(-8deg); } 40% { transform: rotate(-3deg) translateY(-26px) scale(1.4); opacity: .7; } 72% { transform: rotate(-9deg) scale(.9); opacity: 1; background: color-mix(in srgb, var(--t) 22%, var(--paper)); } 100% { transform: rotate(-8deg) scale(1); background: color-mix(in srgb, var(--t) 14%, var(--paper)); } }
}
.t-ok { --t: var(--ok); } .t-teal { --t: var(--teal-ink); } .t-gold { --t: var(--gold-ink); } .t-red { --t: var(--danger); } .t-ink { --t: var(--ink-2); }
.memo-q { max-width: 62ch; margin: 0; font: 500 24px/1.25 var(--serif); letter-spacing: -.016em; color: var(--ink); text-wrap: balance; overflow-wrap: anywhere; }
.memo-meta { display: flex; flex-wrap: wrap; gap: 4px 22px; margin: 0; font-size: 13px; }
.memo-meta > div { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
.memo-meta dt { font: 500 10px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.memo-meta dd { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; margin: 0; min-width: 0; color: var(--ink); }
.proj-link { display: inline-flex; align-items: center; gap: 6px; color: var(--ink); font-weight: 600; white-space: nowrap; text-decoration: none; border-radius: 4px; }
.proj-link > svg { color: var(--ink-3); }
.proj-link .pj-open { color: var(--teal-ink); }
@media (hover: hover) { a.proj-link:hover, a.proj-link:hover > svg { color: var(--teal-ink); } }
.proj-quiet { display: inline-flex; align-items: center; gap: 5px; color: var(--ink-2); white-space: nowrap; }
.proj-quiet svg { color: var(--ink-3); }
.from-one { display: inline-flex; align-items: center; gap: 5px; font-weight: 600; cursor: default; }
.res-key { display: inline-flex; align-items: center; padding: 3px 6px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 11.5px/1 var(--mono); font-variant-ligatures: none; text-decoration: none; white-space: nowrap; }
a.res-key:hover { text-decoration: underline; }
.re-title { color: var(--ink); overflow-wrap: anywhere; }
/* One line under the meta, the same slot open and decided: what the agent does meanwhile, then the delivery truth. */
.memo-line { display: flex; align-items: flex-start; gap: 8px; min-height: 19.5px; margin: 0; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.memo-line > svg { flex: none; margin-top: 3px; color: var(--ink-3); }
.memo-line b { color: var(--ink); font-weight: 650; }
.memo-line:is(.park, .stop) :is(b, svg) { color: var(--queue-wait-ink); }
.memo-line.carry > svg { color: var(--agent-working); }
.memo-line.d-sent > svg { color: var(--teal-ink); }
.memo-line.d-failed :is(b, svg) { color: var(--danger); }
.memo-line .lt { min-width: 0; overflow-wrap: anywhere; }
.memo-line .sep, .done-sum .sep { margin: 0 6px; color: var(--ink-3); }
.n { display: inline-block; min-width: 2ch; text-align: right; font-variant-numeric: tabular-nums; }
.linkish { display: inline; padding: 0; border: 0; border-radius: 4px; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; }
.linkish:hover { text-decoration: underline; text-underline-offset: 2px; }
.memo-body { display: grid; grid-template-columns: minmax(0, 1.55fr) minmax(0, 1fr); gap: 0 36px; padding-top: 20px; }
.memo-answer { display: grid; gap: 20px; align-content: start; min-width: 0; }
.memo-answer > .eyebrow { margin: 0 0 -12px; }
.eyebrow { margin: 0; }
.margin { display: grid; gap: 18px; align-content: start; min-width: 0; padding-left: 26px; border-left: 1px solid var(--paper-line); }
.m-sec { display: grid; gap: 6px; margin: 0; min-width: 0; }
.m-sec p, .m-sec li, .m-note { margin: 0; font-size: 13px; line-height: 1.55; color: var(--ink-2); overflow-wrap: anywhere; }
.m-sec ul { display: grid; gap: 5px; margin: 0; padding-left: 16px; }
.m-sec li::marker { color: var(--ink-3); }
.m-sec .mono-line { font: 12px/1.5 var(--mono); color: var(--ink); }
.m-sec .pre { white-space: pre-wrap; }
.md { display: grid; gap: 8px; }
.md :deep(p), .md :deep(li) { margin: 0; font-size: 13px; line-height: 1.55; color: var(--ink-2); overflow-wrap: anywhere; }
.md :deep(ul), .md :deep(ol) { display: grid; gap: 4px; margin: 0; padding-left: 16px; }
.md :deep(li::marker) { color: var(--ink-3); }
.md :deep(code) { padding: 1px 5px; border-radius: 5px; background: var(--code-bg); font: 500 12px var(--mono); color: var(--ink); }
.tk-line, .tk-pr { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; }
.tk-dot { color: var(--ink-3); }
.tk-open { display: inline-flex; align-items: center; gap: 3px; margin-left: auto; color: var(--teal-ink); font-weight: 600; white-space: nowrap; text-decoration: none; border-radius: 4px; }
.tk-open:hover, .src-link:hover { text-decoration: underline; text-underline-offset: 2px; }
.m-sec .tk-sub { margin-top: 6px; font: 500 10px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.tk-pr a { color: var(--teal-ink); overflow-wrap: anywhere; }
.rel { display: grid; gap: 8px; margin: 0; padding: 0 !important; list-style: none; }
.rel a { color: var(--ink); text-decoration: none; }
.rel b { font: 600 12px var(--mono); color: var(--teal-ink); }
.context-warning { color: var(--warn-ink) !important; }
.src-link { display: inline-flex; align-items: center; gap: 4px; color: var(--teal-ink); font-weight: 600; text-decoration: none; border-radius: 4px; }
.effect { display: grid; gap: 4px; }
.thumbs { display: grid; grid-template-columns: repeat(auto-fill, minmax(104px, 1fr)); gap: 10px; }
.thumb { display: grid; gap: 5px; min-width: 0; padding: 0; border: 0; border-radius: 7px; background: transparent; color: var(--ink-2); text-align: left; cursor: zoom-in; }
.thumb-img { position: relative; display: grid; place-items: center; aspect-ratio: 4 / 3; overflow: hidden; border-radius: 7px; background: var(--sheet-back); box-shadow: 0 0 0 1px var(--paper-line); }
.thumb-img img { display: block; width: 100%; height: 100%; object-fit: cover; }
.thumb-kind { font: 600 11px var(--mono); color: var(--ink-3); text-transform: uppercase; }
@media (hover: hover) { .thumb:hover .thumb-img { box-shadow: 0 0 0 2px var(--teal); } }
.thumb-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11.5px; }
/* Choices: rows on hairlines; the chosen one gets a quiet wash. The description wraps in the row. */
.choices { margin: 0; padding: 0; list-style: none; border-top: 1px solid var(--paper-line); }
.choices > .choice-li { border-bottom: 1px solid var(--paper-line); }
.choice { display: grid; }
@media (hover: hover) { .choice:hover:not(.off) { background: color-mix(in srgb, var(--teal) 4%, transparent); } }
.choice.checked { background: color-mix(in srgb, var(--teal) 9%, transparent); }
.choice-hit { display: grid; grid-template-columns: 18px 20px minmax(0, 1fr); gap: 0 12px; align-items: start; width: 100%; padding: 12px 10px; border: 0; border-radius: 0; background: transparent; color: var(--ink); text-align: left; font: inherit; cursor: pointer; }
.choice.has-field .choice-hit { padding-bottom: 4px; }
.choice-hit:disabled { cursor: default; }
.ck { width: 18px; height: 18px; margin-top: 1px; border-radius: 50%; background: var(--paper); box-shadow: inset 0 0 0 1.5px var(--ink-3); }
.choice.checked .ck { background: radial-gradient(circle, var(--teal) 0 4.5px, var(--paper) 5px); box-shadow: inset 0 0 0 1.5px var(--teal); }
.choice-hit :deep(.keycap) { margin-top: 1px; }
.c-text { display: grid; gap: 3px; min-width: 0; }
.c-name { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; font-size: 14px; font-weight: 650; color: var(--ink); overflow-wrap: anywhere; }
.c-desc { font-size: 13px; line-height: 1.45; color: var(--ink-2); overflow-wrap: anywhere; }
.c-why { display: flex; gap: 5px; font-size: 13px; line-height: 1.45; color: var(--teal-ink); overflow-wrap: anywhere; }
.c-why > svg { flex: none; margin-top: 3px; }
.c-answer { font-size: 14px; font-weight: 650; line-height: 1.45; color: var(--ink); overflow-wrap: anywhere; }
.choice > .c-answer { padding: 0 10px 12px 72px; }
.rec-tag { flex: none; height: 18px; padding: 0 6px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 9.5px/18px var(--mono); letter-spacing: .08em; text-transform: uppercase; white-space: nowrap; }
.choice > .field-row { padding: 0 10px 12px 72px; }
.field-row { display: flex; align-items: center; gap: 10px; min-width: 0; flex: 1; }
.ul-field { flex: 1; width: 100%; min-width: 0; height: 34px; margin-top: 2px; padding: 0 8px; border: 0; border-bottom: 1.5px solid var(--line-2); border-radius: 0; background: transparent; color: var(--ink); font: inherit; font-size: 14px; }
.ul-field::placeholder { color: var(--ink-3); }
/* Editing vs set: while you type, a tint, a strong underline and "Enter done"; once set, plain bold text. */
.ul-field:focus { outline: none; border-bottom: 2px solid var(--teal); background: color-mix(in srgb, var(--teal) 8%, transparent); box-shadow: none; }
.ul-field:not(:focus):not(:placeholder-shown) { border-bottom-color: transparent; font-weight: 650; color: var(--ink); }
@media (hover: hover) { .ul-field:not(:focus):not(:placeholder-shown):hover { border-bottom-color: var(--line-2); } }
.ul-field:disabled { color: var(--ink); -webkit-text-fill-color: var(--ink); }
.field-hint { display: inline-flex; align-items: center; gap: 4px; flex: none; visibility: hidden; font-size: 11.5px; color: var(--teal-ink); white-space: nowrap; }
.field-row:has(.ul-field:focus) .field-hint { visibility: visible; }
/* Decided: the chosen row keeps its tint and its filled radio; the others step back. */
.paper.decided .choice-hit { cursor: default; }
.paper.decided .choice-hit:disabled { opacity: 1; }
.re-title.faint { color: var(--ink-3); }
@media (hover: hover) { .paper.decided .choice:not(.checked):hover { background: transparent; } }
.paper.decided .choice:not(.checked) :is(.c-name, .c-desc, .c-why) { color: var(--ink-3); }
.paper.decided .choice:not(.checked) :deep(.keycap) { opacity: .55; }
/* Stamps: what the answer becomes. The chosen one is inked; its line below never changes size. */
.stamps { display: grid; gap: 10px; }
.stamp-row { display: flex; flex-wrap: wrap; gap: 10px 12px; }
.stamp { --t: var(--ink-2); position: relative; display: inline-flex; align-items: center; gap: 7px; height: 36px; padding: 0 13px; border: 1.5px solid var(--t); border-radius: 5px; background: transparent; box-shadow: inset 0 0 0 2px var(--paper), inset 0 0 0 3px color-mix(in srgb, var(--t) 60%, transparent); color: color-mix(in srgb, var(--t) 80%, var(--ink)); font: 700 11px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; cursor: pointer; }
@media (prefers-reduced-motion: no-preference) { .stamp { transition: color .2s ease, border-color .2s ease, background-color .2s ease, box-shadow .2s ease, opacity .2s ease; } }
.stamp[aria-checked="false"] { --t: var(--ink-3); }
@media (hover: hover) { .stamp:hover:not([aria-disabled="true"]):not([aria-checked="true"]) { --t: var(--ink-2); } }
.stamp[aria-checked="true"] { background: color-mix(in srgb, var(--t) 9%, transparent); }
.stamp u { text-decoration-thickness: 1.5px; text-underline-offset: 3px; }
/* An unavailable stamp is visibly disabled and still readable (3.4:1 light, 3.6:1 dark), with its reason. */
.stamp[data-off] { --t: color-mix(in srgb, var(--ink-3) 80%, var(--paper)); opacity: 1; border-style: dashed; box-shadow: none; cursor: not-allowed; }
.stamp[data-off] svg { opacity: .8; }
.paper.decided .stamp { cursor: default; }
.paper.decided .stamp[data-faded] { opacity: .45; }
.stamp-desc { height: 40px; margin: 0; overflow: hidden; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; font-size: 12.5px; line-height: 20px; color: var(--ink-2); }
.sug-off { color: var(--ink-2); text-decoration: underline dotted var(--line-2); text-underline-offset: 3px; cursor: help; white-space: nowrap; border-radius: 4px; }
.ps { display: flex; align-items: baseline; gap: 14px; margin: 0; }
.ps-label { flex: none; font: 600 15px/1 var(--serif); color: var(--ink-2); }
.diff { margin: 0; padding: 4px; border-radius: 8px; background: color-mix(in srgb, var(--ink) 3%, transparent); font: 12.5px/1.65 var(--mono); color: var(--ink); white-space: pre-wrap; overflow-wrap: anywhere; }
.diff .ln { display: grid; grid-template-columns: 16px minmax(0, 1fr); padding: 1px 8px 1px 6px; border-radius: 5px; text-decoration: none; }
.diff .sg { color: var(--ink-3); }
.diff ins.ln { background: color-mix(in srgb, var(--ok) 13%, transparent); }
.diff ins.ln .sg { color: var(--ok); }
.diff del.ln { background: color-mix(in srgb, var(--danger) 9%, transparent); color: var(--ink-2); }
.diff del.ln .tx { text-decoration: line-through; text-decoration-color: color-mix(in srgb, var(--danger) 60%, transparent); }
.diff del.ln .sg { color: var(--danger); }
.gone { padding: 26px 0; font-size: 13px; color: var(--ink-2); }
/* Native records keep their own evidence. */
.stepup-diff { display: grid; grid-template-columns: minmax(0, 1fr) 28px minmax(0, 1fr); align-items: start; }.stepup-arrow { display: grid; place-items: center; height: 46px; margin-top: 22px; color: var(--ink-3); }
.stepup-diff dl { margin: 6px 0 0; font-size: 12.5px; }.stepup-diff dl > div { display: grid; grid-template-columns: minmax(70px, auto) minmax(0, 1fr); gap: 10px; padding: 9px 8px; border-bottom: 1px solid var(--paper-line); }.stepup-diff dt { color: var(--ink-3); }.stepup-diff dd { margin: 0; overflow-wrap: anywhere; white-space: pre-wrap; }
[data-testid="stepup-before"] .changed dd { color: var(--ink-3); }[data-testid="stepup-after"] .changed { background: var(--row-hover); }[data-testid="stepup-after"] .changed dd { font-weight: 650; }
.stepup-answer { font-size: 13px; font-weight: 550; margin: 0; }
.key-trim-evidence table { width: 100%; table-layout: fixed; border-collapse: collapse; font-size: 12.5px; }
.key-trim-evidence th, .key-trim-evidence td { text-align: left; vertical-align: top; padding: 10px 8px 10px 0; border-bottom: 1px solid var(--paper-line); overflow-wrap: anywhere; }
.key-trim-evidence p { margin: 0; font-size: 12.5px; color: var(--ink-2); line-height: 1.5; }
.scope-list { overflow-wrap: anywhere; font-family: var(--mono); }
/* ---------- All decided: the sheet says so for 1.2 s, with Undo, then closes ---------- */
.done-view { display: grid; justify-items: start; gap: 10px; padding: 34px 0 30px; }
.done-mark { display: grid; place-items: center; width: 44px; height: 44px; border-radius: 14px; color: var(--ok); background: color-mix(in srgb, var(--ok) 12%, var(--surface-raised)); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--ok) 30%, transparent); }
.done-view h2 { margin: 0; font: 500 22px/1.2 var(--serif); letter-spacing: -.012em; color: var(--ink); }
.done-sum { margin: 0; font-size: 13.5px; line-height: 1.5; color: var(--ink-2); }
.done-acts { display: flex; align-items: center; gap: 12px; margin: 4px 0 0; font-size: 12.5px; color: var(--ink-3); }
@media (prefers-reduced-motion: no-preference) { .done-view { animation: settle-in .22s cubic-bezier(.2, 0, 0, 1) both; } @keyframes settle-in { from { opacity: 0; transform: translateY(3px); } } }
/* ---------- Under the sheet: the legend on one line, or a Keys disclosure; never wrapping ---------- */
.desk-foot { grid-area: foot; display: flex; align-items: center; min-height: 30px; margin: 10px 4px 0; }
.desk-hint { display: flex; flex: 1; flex-wrap: nowrap; align-items: center; gap: 4px 16px; min-width: 0; margin: 0; overflow: hidden; white-space: nowrap; font-size: 11.5px; color: var(--ink-3); }
.desk-hint > span { display: inline-flex; flex: none; align-items: center; gap: 3px; }
.desk-foot.fold .desk-hint { position: absolute; visibility: hidden; pointer-events: none; }
.keys { position: relative; display: none; }
.desk-foot.fold .keys { display: block; }
.keys-btn { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 10px 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font-size: 12px; font-weight: 600; cursor: pointer; }
@media (hover: hover) { .keys-btn:hover { background: var(--row-hover); color: var(--ink); } }
.keys-btn[aria-expanded="true"] { background: var(--row-selected); color: var(--ink); }
.keys-pop { position: absolute; left: 0; bottom: 36px; z-index: 8; display: grid; grid-template-columns: max-content max-content; gap: 6px 14px; margin: 0; padding: 12px 14px; font-size: 12px; color: var(--ink-2); white-space: nowrap; }
.kp-row { display: contents; }
.keys-pop dt { display: inline-flex; align-items: center; gap: 3px; justify-content: flex-end; }
.keys-pop dd { margin: 0; }
@media (pointer: coarse) and (hover: none) { .desk-foot { display: none; } }
/* On narrow desks the status shows as a toast above the action bar, never over it. */
.dlg-toast { position: fixed; z-index: 9; left: 50%; bottom: calc(84px + env(safe-area-inset-bottom)); transform: translateX(-50%); display: flex; align-items: center; gap: 10px; width: max-content; max-width: calc(100vw - 32px); min-height: 40px; padding: 8px 14px; border-radius: 16px; background: var(--tip-bg); color: var(--tip-ink); font-size: 13.5px; box-shadow: var(--shadow-pop); }
@media (prefers-reduced-motion: no-preference) { .dlg-toast { animation: toast-in .2s ease; } @keyframes toast-in { from { opacity: 0; transform: translate(-50%, 8px); } } }
/* Touch: 44 px hit areas without growing the visuals. */
@media (pointer: coarse) {
  .stamp { height: 40px; }
  .ul-field { height: 44px; }
  .sug-off { white-space: normal; }
  :is(.linkish, .proj-link, .res-key, .tk-open, .keys-btn, .ft-pager, .desk-x, .desk-btn, .stamp) { position: relative; }
  :is(.linkish, .proj-link, .res-key, .tk-open, .keys-btn, .ft-pager, .desk-x, .desk-btn, .stamp)::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: max(100%, 44px); transform: translate(-50%, -50%); }
}
/* Narrow desks (a phone, a small window; the artifact's 640 px desk): one column, background first, actions in a bottom bar. */
@media (max-width: 688px) {
  .desk { grid-template-columns: minmax(0, 1fr) auto; grid-template-rows: minmax(52px, auto) minmax(0, 1fr) auto; grid-template-areas: "tab x" "stack stack" "acts acts"; }
  .folder-tab { justify-self: start; width: min(230px, 100%); height: 40px; padding: 0 14px; font-size: 13.5px; }
  .desk-status { display: none; }
  .desk-x { margin-bottom: 4px; }
  .desk-acts { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.4fr); gap: 8px; margin: 0; padding: 10px 0 0; }
  .desk-acts .native-action { grid-column: 1 / -1; grid-row: 2; justify-self: start; }
  .desk-acts .desk-btn { min-width: 0; height: 48px; font-size: 14.5px; }
  .desk-btn :deep(.keycap) { display: none; }
  .jump { top: 60px; left: 10px; width: calc(100% - 20px); }
  .jump-row { grid-template-columns: 22px minmax(0, 1fr) auto; }
  .jr-kind { display: none; }
  .stack, .paper { flex: 1 1 auto; }
  .paper { padding: 0 18px 24px; }
  .memo-head { display: block; padding-top: 20px; }
  .memo-head > .stamp-slot { float: right; width: 120px; min-height: 56px; margin: -2px -6px 4px 10px; place-items: start end; padding-top: 2px; }
  .memo-head > .memo-headtext { display: block; }
  .memo-headtext > .memo-meta { margin-top: 12px; }
  .memo-head > .memo-line { clear: both; margin-top: 10px; min-height: 39px; }
  .stamp-big { padding: 8px 10px; border-width: 2px; font-size: 11.5px; letter-spacing: .12em; }
  .memo-q { font-size: 20px; }
  .memo-body { grid-template-columns: minmax(0, 1fr); gap: 18px; }
  .margin { order: -1; padding: 0 0 16px; border-left: 0; border-bottom: 1px solid var(--paper-line); }
  .stamp-desc { height: 60px; -webkit-line-clamp: 3; line-clamp: 3; }
  .desk-foot { display: none; }
  .choice > .field-row, .choice > .c-answer { padding-left: 10px; }
  .stepup-diff { grid-template-columns: minmax(0, 1fr); }.stepup-arrow { height: 32px; margin: 4px 0; transform: rotate(90deg); }
}
@media (max-width: 560px) {
  .desk-dlg { width: 100vw; height: 100dvh; margin: 0; }
  .desk { height: 100%; max-height: 100dvh; padding: env(safe-area-inset-top) 10px calc(10px + env(safe-area-inset-bottom)); border-radius: 0; }
}
</style>
