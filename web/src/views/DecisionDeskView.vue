<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import DecisionDeskMemo from '../components/agents/DecisionDeskMemo.vue'
import DoctrineProposalDialog from '../components/rules/DoctrineProposalDialog.vue'
import { getDoctrine, type DoctrineSource, type DoctrineFile, type DoctrineRule, type DoctrineInboxItem, type DoctrineProposal } from '../lib/doctrine'
import { arrivals, deskItemID, deskLinkItem, draftFor, kindLabels, newRound, outcomeLabels, type DeskClose, type DeskDraft, type DeskItem, type DeskOutcome, type DeskTicket, type DeskWho } from '../lib/decisionDesk'
import AppIcon, { type IconName } from '../components/AppIcon.vue'
import KeyCap from '../components/KeyCap.vue'
import HarnessMark from '../components/agents/HarnessMark.vue'
import { agentName, harnessLabel, RISK_LABEL, riskFor } from '../lib/agentState'
import { lookupNodes } from '../lib/api'
import { captureToastOwner, toast } from '../lib/toast'
import { approvalItem, commitDesk, decisionPermission, emptySources, loadDesk, loadMoreQuestions, nativeTierAdapter, questionItem, readApproval, readQuestion, MAX_QUESTION_PAGES, type DeskRead, type DeskSources } from '../lib/decisionDeskApi'
import { phoneCapability } from '../lib/deskPhoneApproval'
import { settledElsewhere } from '../lib/stepup'
import { can, onAccessRevalidate } from '../lib/authz'
import { useSession } from '../stores/session'
import { useAgents } from '../stores/agents'
import type { KeyTrimCursors } from '../lib/keyTrim'
import { useDecisionDesk } from '../stores/decisionDesk'
import { revokeApproval } from '../lib/agents'
import { getRun } from '../lib/agentRows'
import { undoKnowledge } from '../lib/knowledge'
import { doctrineInbox, inboxChanged } from '../lib/doctrineInbox'

const session = useSession()
const agents = useAgents()
const desk = useDecisionDesk(), route = useRoute(), router = useRouter()
const makeAdapters = () => ({ approvalRun: async (id: string) => {
  const identity = session.identity
  const run = await getRun(id)
  if (session.identity !== identity || !session.authenticationCurrent()) throw new Error('The person changed while reading approval ownership.')
  return agents.admitRun(run)
}, tiers: nativeTierAdapter(async () => { await agents.refreshSessions(); return agents.sessions }, can, async (project, id) => {
  await agents.loadSessionDetail(project, id)
  return agents.sessionById(id)
}) })
let adapters = makeAdapters()
const owner = computed(() => `${session.identity?.tenant.id ?? ''}:${session.identity?.principal.id ?? ''}`)
const items = ref<DeskItem[]>([]), roundItems = ref<DeskItem[]>([]), round = ref<string[]>([]), start = ref(''), opened = ref(false)
// Same-view links can close and reopen in one render; each new round must
// discard the prior memo's index, drafts and pending context reads.
const memoRound = ref(0)
const state = ref<'loading' | 'ready'>('loading'), warnings = ref<string[]>([]), more = ref({ open: false, answered: false }), loading = ref(false), pages = ref({ open: 1, answered: 1 }), filter = ref<'open' | 'decided'>('open'), outcomeFilter = ref<DeskOutcome | 'approvals' | ''>('')
const roundBaseline = ref<string[]>([]), phoneVerification = ref<boolean | undefined>(), capabilityError = ref('')
// Names the step-up method on Approve; the server makes the actual choice.
const stepupMethod = ref('Passkey or sign-in')
const trimCursors = ref<KeyTrimCursors>({}), nextKeyTrims = ref<KeyTrimCursors>({})
const trimState = computed(() => filter.value === 'open' ? 'pending' : 'decided')
let sources: DeskSources = emptySources(), generation = 0, alive = true
let capabilityGeneration = 0
let currentRead: DeskRead | undefined
const linkError = ref(''), revoked = ref(new Set<string>())
const ruleEdit = ref<{ item: DeskItem; draft: DoctrineInboxItem; source: DoctrineSource; file: DoctrineFile; rule: DoctrineRule; owner: string }>()
let followed = ''
const changeId = ref('')
const fresh = computed(() => arrivals(items.value, roundBaseline.value))
const projectNames = ref(new Map<string, string>())
function queryPresent(value: unknown) {
  return Array.isArray(value) ? value.some(part => typeof part === 'string' && part.length > 0) : typeof value === 'string' && value.length > 0
}
// The Knowledge link arrives as ?kind=lesson&project=…. That filter used to be
// invisible, so an empty lesson list claimed the whole desk was clear.
function inScope(item: DeskItem) {
  return (!route.query.kind || item.kind === route.query.kind) && (!route.query.project || item.projectId === route.query.project)
}
const scopeKind = computed(() => typeof route.query.kind === 'string' ? route.query.kind : '')
const scopeProject = computed(() => typeof route.query.project === 'string' ? route.query.project : '')
const scopeActive = computed(() => queryPresent(route.query.kind) || queryPresent(route.query.project))
const scopeProjectName = computed(() => {
  const id = scopeProject.value
  if (!id) return ''
  return projectNames.value.get(id) || items.value.find(item => item.projectId === id && item.projectName && item.projectName !== 'Project name unavailable')?.projectName || ''
})
const scopePlace = computed(() => scopeProjectName.value || (scopeProject.value ? 'this project' : ''))
const scopeCopy = computed(() => {
  const place = scopePlace.value
  if (scopeKind.value === 'lesson' && place) return `Showing lessons in ${place}.`
  if (scopeKind.value === 'lesson') return 'Showing lessons.'
  if (place && !scopeKind.value) return `Showing items in ${place}.`
  if (place) return `Showing this view in ${place}.`
  return 'Showing a narrowed desk.'
})
const emptyCopy = computed(() => {
  if (warnings.value.length) return 'The available sources have no matching items.'
  if (filter.value === 'decided') return 'No recorded decisions in this view.'
  const place = scopePlace.value
  if (scopeActive.value && scopeKind.value === 'lesson') return place ? `No lessons wait in ${place}.` : 'No lessons are waiting.'
  if (scopeActive.value && place) return `Nothing is waiting in ${place}.`
  if (scopeActive.value) return 'Nothing in this view is waiting.'
  return 'Nothing is waiting in the available sources.'
})
const showAllTo = computed(() => {
  const query = { ...route.query }
  delete query.kind
  delete query.project
  return { path: '/decision-desk', query }
})
// A scoped Open count matches the list. The header keeps the whole-desk total.
const openCountLabel = computed(() => {
  if (!scopeActive.value) return desk.count ?? '?'
  if (state.value !== 'ready' || desk.count === null) return '?'
  return items.value.filter(item => !item.decided && inScope(item)).length
})
const decidedCountLabel = computed(() => items.value.filter(item => item.decided && (!scopeActive.value || inScope(item))).length)
const visible = computed(() => items.value.filter(item => inScope(item) && item.decided === (filter.value === 'decided') && (filter.value === 'open' || !outcomeFilter.value
  || (outcomeFilter.value === 'approvals' ? item.kind === 'approval' : (item.kind === 'question' || item.kind === 'handover') && item.outcome === outcomeFilter.value))))
// Link-out rows are decided on their settings page, so they never join a memo round.
const reviewable = computed(() => visible.value.filter(item => !item.linkOut))
const person = computed(() => session.identity?.principal.kind === 'person')
// AEON-1339: the list in rev 11's two groups; what holds something up comes first.
const heldRows = computed(() => visible.value.filter(item => item.held || item.kind === 'approval'))
const readyRows = computed(() => visible.value.filter(item => !(item.held || item.kind === 'approval')))
const openRound = computed(() => items.value.filter(item => inScope(item) && !item.decided && !item.linkOut))
// The ticket each open item is about: one bounded lookup of up to 100 ticket ids per read.
const tickets = ref(new Map<string, DeskTicket>())
let ticketTurn = 0
async function readTickets(rows: readonly DeskItem[]) {
  const turn = ++ticketTurn, identity = owner.value
  const ids = [...new Set(rows.map(row => row.ticketId).filter((id): id is string => !!id && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id) && !tickets.value.has(id)))].slice(0, 100)
  if (!ids.length) return
  try {
    const page = await lookupNodes(ids)
    if (!alive || turn !== ticketTurn || identity !== owner.value || !Array.isArray(page?.items)) return
    const next = new Map(tickets.value)
    for (const node of page.items.slice(0, 100)) if (ids.includes(node.id) && typeof node.key === 'string') next.set(node.id, { key: node.key, title: typeof node.title === 'string' ? node.title : '' })
    tickets.value = next
  } catch { /* The rows keep their project; a ticket chip waits for the next read. */ }
}
function ticketOf(item: DeskItem): DeskTicket | undefined {
  return (item.ticketId ? tickets.value.get(item.ticketId) : undefined) ?? (item.ticketKey ? { key: item.ticketKey, title: '' } : undefined)
}
// The asking agent by its exact session when known, else its message address, else the name the source gave.
function who(item: DeskItem): DeskWho | undefined {
  const asker = item.askers?.[0]
  if (!asker) return undefined
  const known = asker.sessionId ? agents.sessionById(asker.sessionId) : undefined
  if (known && known.agent_principal_id === asker.principalId) return { name: agentName(known, { [known.agent_principal_id]: agents.addressOf(known.agent_principal_id) }), harness: known.harness, model: [harnessLabel(known.harness), known.model].filter(Boolean).join(' · ') }
  if (asker.name) return { name: asker.name }
  const address = asker.principalId ? agents.addressOf(asker.principalId) : ''
  return address ? { name: address.split(':')[1] || address } : undefined
}
const KIND_ICON: Record<DeskItem['kind'], IconName> = { question: 'help', handover: 'help', approval: 'clock', action: 'send', rule: 'book', tier: 'gauge', key_trim: 'key', stepup: 'shield', account_matrix: 'users', lesson: 'book' }
const OUTCOME: Record<DeskOutcome, { icon: IconName; tone: string }> = { once: { icon: 'message', tone: 'ok' }, always: { icon: 'pin', tone: 'teal' }, requirement: { icon: 'req', tone: 'gold' }, doctrine: { icon: 'book', tone: 'bad' } }
function outcomeOf(item: DeskItem): { icon: IconName; word: string; tone: string } {
  if (item.kind === 'question' || item.kind === 'handover') return { ...OUTCOME[item.outcome], word: outcomeLabels[item.outcome] }
  if (item.kind === 'approval') return item.optionId === 'approved' ? { icon: 'check', word: 'Approved', tone: 'ok' } : item.optionId === 'denied' ? { icon: 'close', word: 'Denied', tone: 'bad' } : { icon: 'clock', word: item.answer || 'Closed', tone: 'ink' }
  if (item.kind === 'rule') return item.optionId === 'propose' ? { icon: 'book', word: 'PR proposed', tone: 'gold' } : { icon: 'close', word: 'Not now', tone: 'ink' }
  if (item.kind === 'action') return item.optionId === 'reply' ? { icon: 'send', word: 'Replied', tone: 'ok' } : item.optionId === 'resolved' ? { icon: 'check', word: 'Resolved', tone: 'ok' } : { icon: 'close', word: 'Dismissed', tone: 'ink' }
  return { icon: KIND_ICON[item.kind], word: item.answer || 'Decided', tone: 'ink' }
}
const now = ref(Date.now())
function ago(value?: string) {
  const at = value ? Date.parse(value) : NaN
  if (!Number.isFinite(at)) return ''
  const minutes = Math.max(0, Math.round((now.value - at) / 60_000))
  return minutes < 1 ? 'just now' : minutes < 60 ? `${minutes} min ago` : minutes < 1440 ? `${Math.round(minutes / 60)} h ago` : minutes < 2880 ? 'yesterday' : `${Math.round(minutes / 1440)} days ago`
}
function expiry(value?: string) {
  const at = value ? Date.parse(value) : NaN
  if (!Number.isFinite(at)) return ''
  const minutes = Math.ceil((at - now.value) / 60_000)
  return minutes < 1 ? 'expires now' : minutes < 120 ? `expires in ${minutes} min` : `expires in ${Math.round(minutes / 60)} h`
}
const soon = (value?: string) => !!value && Date.parse(value) - now.value < 30 * 60_000
const graceLeft = (item: DeskItem) => (item.kind === 'question' || item.kind === 'handover') && item.deliveryState === 'pending' && item.deliverAfter ? Math.min(10, Math.max(0, Math.round((Date.parse(item.deliverAfter) - now.value) / 1000))) : 0
// What the agent does meanwhile, as one quiet line under a question.
function meanwhileLine(item: DeskItem) {
  const text = item.meanwhile && !['carries on', 'parked', 'paused', 'stopped'].includes(item.meanwhile) ? item.meanwhile : ''
  const kind = item.meanwhileKind ?? (item.held ? 'parked' : 'carries_on'), ticket = ticketOf(item)
  const title = kind === 'parked' ? (ticket ? `Parked ${ticket.key}` : 'Parked') : kind === 'stopped' ? 'Waits for this' : kind === 'paused' ? 'Paused' : 'Carries on'
  return { held: kind !== 'carries_on', icon: (kind === 'parked' ? 'park' : kind === 'stopped' ? 'stop' : kind === 'paused' ? 'hold' : 'arrow') as IconName, title, text }
}
const recommends = (item: DeskItem) => item.recommended ? item.choices.find(choice => choice.id === item.recommended)?.title ?? '' : ''
// Approve, Deny, Resolve and Dismiss open rev 11's inline reason form; they commit through the memo's path.
const rowForm = ref<{ id: string; revision: number; mode: 'approve' | 'deny' | 'resolve' | 'dismiss'; reason: string; busy: boolean; error: string }>()
const ROW_FORM = { approve: { label: 'Reason (optional)', placeholder: 'Fine for this run.', submit: 'Approve permission', option: 'approved', icon: 'check' }, deny: { label: 'Why not? The agent sees this.', placeholder: 'Use the bundled tool instead.', submit: 'Deny permission', option: 'denied', icon: 'close' }, resolve: { label: 'Note (optional)', placeholder: 'Merged it myself after CI.', submit: 'Mark resolved', option: 'resolved', icon: 'check' }, dismiss: { label: 'Note (optional)', placeholder: 'Not needed any more.', submit: 'Dismiss request', option: 'dismissed', icon: 'close' } } as const
const mac = /Mac|iPhone|iPad|iPod/i.test(navigator.platform)
function openRowForm(item: DeskItem, mode: 'approve' | 'deny' | 'resolve' | 'dismiss') {
  rowForm.value = { id: item.id, revision: item.revision, mode, reason: '', busy: false, error: '' }
  void nextTick(() => document.getElementById(`desk-reason-${item.id}`)?.focus())
}
async function commitRow(item: DeskItem) {
  const form = rowForm.value
  if (!form || form.id !== item.id || form.busy) return
  // The record on screen: a changed revision or a vanished row is never decided from a stale form.
  const current = items.value.find(row => row.id === form.id)
  if (!current || current.revision !== form.revision || current.decided) { form.error = 'This item changed. Open it to review the current request.'; return }
  const choice = ROW_FORM[form.mode]
  if (form.mode === 'deny' && !form.reason.trim()) { form.error = 'Give the agent a reason first.'; document.getElementById(`desk-reason-${item.id}`)?.focus(); return }
  const identity = owner.value
  form.busy = true; form.error = ''
  try {
    const result = await decide(current, { ...draftFor(current), optionId: choice.option, reason: form.reason, dirty: true }, crypto.randomUUID())
    if (!alive || identity !== owner.value) return
    recorded(result)
    if (rowForm.value?.id === form.id) rowForm.value = undefined
    const name = who(current)?.name ?? 'The agent'
    toast(form.mode === 'approve' ? `Approved. ${name} can go ahead.` : form.mode === 'deny' ? `Denied. ${name} sees your reason.` : form.mode === 'resolve' ? 'Marked resolved.' : 'Dismissed.')
  } catch (cause) {
    if (alive && rowForm.value?.id === form.id) { rowForm.value.busy = false; rowForm.value.error = cause instanceof Error ? cause.message : 'The decision was not confirmed.' }
  }
}
function rowFormKeys(event: KeyboardEvent, item: DeskItem) {
  if (event.isComposing) return
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); rowForm.value = undefined; return }
  if (event.key === 'Enter' && !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)) { event.preventDefault(); void commitRow(item) }
}
// Failed sources say so in one calm line each, at the end of the card; truncation stays explicit.
const sourceLines = computed(() => {
  const lines = new Map<string, boolean>()
  const failed = (text: string) => lines.set(text, true)
  for (const warning of warnings.value) {
    if (/could not be read|unavailable|could not be confirmed|could not be hydrated/i.test(warning) && !/limited|limit is|first \d|Only|outside this page|may be incomplete|more step-up/i.test(warning)) {
      if (/^Approvals|approval/i.test(warning)) failed('Some approvals couldn’t be loaded')
      else if (/step-up/i.test(warning)) failed('Some step-up approvals couldn’t be loaded')
      else if (/Questions|question/i.test(warning)) failed(/More questions/.test(warning) ? 'More questions couldn’t be loaded' : 'Some questions couldn’t be loaded')
      else if (/rule change/i.test(warning)) failed('Some rule changes couldn’t be loaded')
      else if (/key trim/i.test(warning)) failed('Some key trims couldn’t be loaded')
      else if (/action request/i.test(warning)) failed('Some action requests couldn’t be loaded')
      else if (/tier/i.test(warning)) failed('Some tier requests couldn’t be loaded')
      else if (/Project names/i.test(warning)) failed('Project names couldn’t be loaded')
      else if (/desk could not be read/i.test(warning)) failed('The desk couldn’t be loaded')
      else failed('Some items couldn’t be loaded')
    } else lines.set(warning.replace(/\.$/, ''), false)
  }
  if (linkError.value) failed('This link’s item couldn’t be found')
  return [...lines.entries()].map(([text, retry]) => ({ text, retry }))
})
function allowed(item: DeskItem) { return person.value && decisionPermission(item, sources, can, adapters) }
async function refresh() {
  if (!session.authenticationCurrent()) return
  now.value = Date.now()
  const turn = ++generation, identity = owner.value
  loading.value = true
  try {
    await desk.refresh()
    const projection = desk.projection
    const result = await loadDesk(pages.value, adapters, trimCursors.value, desk.projection)
    const linked = deskLinkItem(route.query.item ?? route.query.needs)
    const existing = result.items.find(item => item.id === linked)
    // A selected source gets one bounded detail read even if the bulk detail
    // budget left its canonical row unavailable. Keep that row's authority.
    function includeLinked(read: DeskItem) {
      // A bounded detail read names no project for some sources; keep the listed one.
      const projectId = read.projectId || existing?.projectId || ''
      const item = { ...read, projectId, projectName: read.projectId ? read.projectName : existing?.projectName ?? read.projectName, projectKey: projectId ? result.projectKeys?.get(projectId) : undefined }
      const row = projection?.items.find(row => deskItemID(row) === linked)
      const confirmed = item.decided ? item : row ? { ...item, held: row.held, source: row.source,
        unavailable: row.can_decide === false ? 'This person may read this request but cannot decide it.' : item.unavailable }
        : { ...item, unavailable: 'This source is outside the confirmed open desk. Retry before deciding.' }
      const at = result.items.findIndex(item => item.id === linked)
      if (at < 0) result.items.push(confirmed)
      else result.items.splice(at, 1, confirmed)
    }
    if (linked.startsWith('q:') && (!existing || existing.unavailable)) {
      try {
        const question = await readQuestion(linked.slice(2))
        if (question.id !== linked.slice(2)) throw new Error('Source identity changed')
        result.sources.questions.set(linked, question)
        includeLinked(questionItem(question, existing?.projectName ?? 'Project name unavailable'))
      } catch { result.warnings.push('The linked question could not be read. Retry before reviewing it.') }
    }
    if (linked.startsWith('a:') && (!existing || existing.unavailable)) {
      try {
        const approval = await readApproval(linked.slice(2))
        if (approval.id !== linked.slice(2)) throw new Error('Source identity changed')
        result.sources.approvals.set(linked, approval)
        includeLinked(approvalItem(approval, existing?.projectName))
      } catch { result.warnings.push('The linked approval could not be read. Retry before reviewing it.') }
    }
    if (!alive || turn !== generation || identity !== owner.value) return
    applyRead(result)
    followLink()
    void readTickets(result.items.filter(item => !item.decided).slice(0, 300))
  } catch { if (turn === generation) warnings.value = ['The desk could not be read. Try refreshing.'] }
  finally { if (alive && turn === generation) { state.value = 'ready'; loading.value = false } }
}
function applyRead(result: DeskRead) {
  const present = new Set(result.items.map(item => item.id))
  result.items.push(...items.value.filter(item => item.kind === 'lesson' && item.decided && !present.has(item.id)))
  currentRead = result
  sources = result.sources; items.value = result.items; warnings.value = result.warnings; more.value = result.hasMore
  projectNames.value = result.projectNames
  nextKeyTrims.value = result.nextKeyTrims
  pages.value = { open: result.questionPositions.open?.pages ?? pages.value.open, answered: result.questionPositions.answered?.pages ?? pages.value.answered }
  // Memo content stays frozen; changed or vanished records require a deliberate reopen.
  if (opened.value) roundItems.value = roundItems.value.map(item => {
    const latest = result.items.find(row => row.id === item.id)
    if (item.expiresAt && Date.parse(item.expiresAt) <= Date.now()) return { ...item, unavailable: 'This request expired. No permission was granted.' }
    if (!latest) return { ...item, unavailable: 'The source could not be confirmed. Close and refresh before deciding.' }
    if (latest.revision !== item.revision || latest.decided !== item.decided) return { ...item, unavailable: 'The source changed. Close and reopen before deciding.' }
    return item
  })
}
async function loadMore() {
  const state = filter.value === 'open' ? 'open' : 'answered'
  if (loading.value || !currentRead || !more.value[state] || pages.value[state] >= MAX_QUESTION_PAGES) return
  const turn = ++generation, identity = owner.value, previous = currentRead
  loading.value = true
  try {
    const result = await loadMoreQuestions(previous, state, () => currentRead ?? previous)
    if (!alive || turn !== generation || identity !== owner.value) return
    applyRead(result)
  } catch { if (alive && turn === generation && identity === owner.value) warnings.value = [...new Set([...warnings.value, 'More questions could not be read. Try again or refresh.'])] }
  finally { if (alive && turn === generation) loading.value = false }
}
async function pageKeyTrims(first = false) {
  if (loading.value) return
  const state = trimState.value
  const cursor = first ? undefined : nextKeyTrims.value[state]
  if (!first && !cursor) return
  trimCursors.value = { ...trimCursors.value, [state]: cursor }
  await refresh()
}
function begin(id?: string) {
  roundBaseline.value = newRound(items.value)
  roundItems.value = [...reviewable.value]; round.value = newRound(roundItems.value)
  if (!round.value.length) return
  start.value = id && round.value.includes(id) ? id : round.value[0]!
  memoRound.value++
  opened.value = true
}
function followLink() {
  const requested = deskLinkItem(route.query.item ?? route.query.needs)
  if (!requested || requested === followed || opened.value) return
  const canonical = requested.startsWith('m:') ? [...sources.questions.entries()].find(([, question]) =>
    question.input.source_request_id === requested.slice(2) || question.askers.some(asker => asker.input.source_request_id === requested.slice(2)))?.[0] : undefined
  const item = items.value.find(row => row.id === (canonical || requested))
  if (!item) { linkError.value = 'The linked source is unavailable or outside this page. Retry or load more before reviewing it.'; return }
  filter.value = item.decided ? 'decided' : 'open'; outcomeFilter.value = ''
  changeId.value = item.decided && route.query.change === '1' ? item.id : ''
  followed = requested; linkError.value = ''; begin(item.id)
}
watch(() => route.fullPath, () => {
  if (route.query.view === 'decided') filter.value = 'decided'
  ruleEdit.value = undefined
  followed = ''; opened.value = false; followLink()
  if (linkError.value && !loading.value) void refresh()
})
async function editRule(item: DeskItem) {
  const identity = owner.value, startAt = start.value, sourceItem = sources.rules.get(item.id)
  if (!sourceItem || !allowed(item) || item.decided || item.unavailable) throw new Error('This proposal is no longer editable.')
  const layer = await getDoctrine()
  if (!alive || identity !== owner.value || !opened.value || startAt !== start.value || !allowed(item) || !roundItems.value.some(row => row.id === item.id && row.revision === item.revision && !row.decided && !row.unavailable)) return
  const source = layer.sources.find(row => row.id === sourceItem.source_id)
  const file = source?.files.find(row => row.path === sourceItem.path)
  const rule = file?.rules.find(row => row.key === sourceItem.rule_key || row.sha256 === sourceItem.base_sha256)
  if (!layer.proposals_enabled || !source || !file || !rule) throw new Error('The pinned source rule could not be verified. Refresh before editing this proposal.')
  ruleEdit.value = { item: { ...item }, draft: { ...sourceItem }, source, file, rule, owner: identity }
}
function editedRule(proposal: DoctrineProposal) {
  const edit = ruleEdit.value
  if (!edit || edit.owner !== owner.value || !alive) return
  if (!roundItems.value.some(row => row.id === edit.item.id && row.revision === edit.item.revision && !row.decided && !row.unavailable)) { ruleEdit.value = undefined; linkError.value = 'This proposal changed while editing. Refresh to confirm the result.'; return }
  if (proposal.id !== edit.draft.id) { linkError.value = 'The native proposal response did not confirm this source. Refresh before continuing.'; return }
  ruleEdit.value = undefined
  recorded({ ...edit.item, decided: true, optionId: 'propose', answer: 'Propose the change', prUrl: proposal.pr_url, delivery: 'Pull request created through the native doctrine workflow.' })
}
function canRevoke(item: DeskItem) {
  const approval = sources.approvals.get(item.id)
  return person.value && item.kind === 'approval' && item.decided && approval?.decision === 'approved' && !revoked.value.has(item.id)
    && Date.parse(approval.expires_at) > Date.now() && can('approvals.revoke')
}
async function revoke(item: DeskItem): Promise<DeskItem> {
  const identity = owner.value, approval = sources.approvals.get(item.id)
  if (!approval || !canRevoke(item)) throw new Error('The grant is no longer available to revoke.')
  await revokeApproval(approval.id)
  if (!alive || identity !== owner.value) throw new Error('The signed-in person changed. Reopen the desk.')
  revoked.value = new Set(revoked.value).add(item.id)
  return { ...item, delivery: 'The grant was revoked. The original decision stays in history.' }
}
async function decide(item: DeskItem, draft: DeskDraft, requestId: string, signal?: AbortSignal) {
  const identity = owner.value
  const declineBlockedTrim = item.kind === 'key_trim' && draft.optionId === 'decline' && item.keyTrim?.state === 'pending' && item.unavailable === item.keyTrim.blocked_reason
  if (!allowed(item) || (item.unavailable && !declineBlockedTrim)) throw new Error('This action is no longer available to this person.')
  if (item.expiresAt && Date.parse(item.expiresAt) <= Date.now()) throw new Error('This request expired.')
  if (item.kind === 'approval' && phoneVerification.value === undefined) throw new Error(capabilityError.value || 'Approval verification availability is still being checked.')
  const result = await commitDesk(item, draft, sources, requestId, phoneVerification.value === true, { ...adapters, stepup: { signal } })
  if (!alive || identity !== owner.value) throw new Error('The signed-in person changed. Reopen the desk.')
  // First decision wins: someone else's outcome is shown, never as recorded by this person.
  const elsewhere = result.stepup && settledElsewhere(result.stepup, session.identity?.principal.id ?? '')
  if (elsewhere) { recorded(result); throw new Error(elsewhere) }
  return result
}
const undoingLessons = new Set<number>()
async function undoLesson(item: DeskItem): Promise<DeskItem> {
  const identity = owner.value, id = item.id, revision = item.revision, eventId = item.undoEvent
  const latest = items.value.find(row => row.id === id)
  if (!eventId || undoingLessons.has(eventId) || !alive || !session.authenticationCurrent() || latest?.revision !== revision || latest.undoEvent !== eventId || !allowed(latest)) throw new Error('This lesson decision is no longer available to undo.')
  undoingLessons.add(eventId)
  try {
    await undoKnowledge(eventId)
    const current = items.value.find(row => row.id === id)
    if (!alive || identity !== owner.value || current?.revision !== revision || current.undoEvent !== eventId) throw new Error('The source changed while undoing. Reopen the Desk to confirm the result.')
    return { ...current, decided: false, optionId: undefined, answer: undefined, reason: undefined, delivery: undefined, undoEvent: undefined }
  } finally { undoingLessons.delete(eventId) }
}
let pendingLessonToast: DeskItem | undefined
function showLessonToast(item: DeskItem) {
  const current = captureToastOwner()
  toast(item.delivery ?? 'Lesson decision recorded.', { timeout: 8000, action: { label: 'Undo', run: () => {
    if (!current()) return
    void undoLesson(item).then(restored => { if (current()) recorded(restored) })
      .catch(cause => { if (current()) toast(cause instanceof Error ? cause.message : 'Undo did not work.', { tone: 'error' }) })
  } } })
}
// The memo already offers Undo in its pinned native action slot. Its toast
// appears on closing, where it cannot cover the phone's decision controls.
watch(opened, open => {
  if (!open && pendingLessonToast) { const item = pendingLessonToast; pendingLessonToast = undefined; showLessonToast(item) }
})
function recorded(item: DeskItem) {
  if (item.kind === 'lesson') {
    if (!item.decided && pendingLessonToast?.id === item.id) pendingLessonToast = undefined
    if (item.decided && item.undoEvent && item.undoEvent !== items.value.find(row => row.id === item.id)?.undoEvent) {
      if (opened.value) pendingLessonToast = item
      else showLessonToast(item)
    }
  }
  if (item.kind === 'rule' && item.decided && !items.value.find(row => row.id === item.id)?.decided) inboxChanged(doctrineInbox.pending - 1, item.id.slice(2))
  items.value = items.value.map(row => row.id === item.id ? item : row)
  if (currentRead) currentRead = { ...currentRead, items: items.value }
  roundItems.value = roundItems.value.map(row => row.id === item.id ? item : row)
  void desk.refresh()
}
function resetDesk() {
  pendingLessonToast = undefined
  capabilityGeneration++
  ruleEdit.value = undefined
  generation++; opened.value = false; items.value = []; roundItems.value = []; round.value = []; roundBaseline.value = []
  followed = ''; linkError.value = ''; revoked.value = new Set(); tickets.value = new Map(); ticketTurn++; rowForm.value = undefined; changeId.value = ''
  sources = emptySources(); adapters = makeAdapters(); filter.value = route.query.view === 'decided' ? 'decided' : 'open'; outcomeFilter.value = ''; pages.value = { open: 1, answered: 1 }
  projectNames.value = new Map()
  trimCursors.value = {}; nextKeyTrims.value = {}
  currentRead = undefined
  phoneVerification.value = undefined; capabilityError.value = ''; stepupMethod.value = 'Passkey or sign-in'
  warnings.value = []; more.value = { open: false, answered: false }; loading.value = false
}
watch(owner, () => {
  resetDesk()
  const identity = owner.value, turn = capabilityGeneration
  void phoneCapability().then(capability => { if (alive && identity === owner.value && turn === capabilityGeneration) { phoneVerification.value = capability.available; stepupMethod.value = capability.passkeys ? 'Passkey' : 'Sign in again' } })
    .catch(() => { if (alive && identity === owner.value && turn === capabilityGeneration) capabilityError.value = 'Approval verification availability could not be confirmed. Refresh the page before deciding.' })
  void refresh()
}, { immediate: true, flush: 'sync' })
// A refresh signal arrives before the permission response. Only an actual
// reset invalidates the frozen source; can() reacts to the completed refresh.
const stopAccess = onAccessRevalidate({ reset: resetDesk })
// How the round ended: after the last decision the desk returns to where it was opened (AEON-1339).
function closed(result?: DeskClose) {
  opened.value = false; ruleEdit.value = undefined; changeId.value = ''
  if (result?.skipped) toast('Skipped. It stays on the desk.')
  if (!result?.afterLast) return
  const last = result.last, left = last ? graceLeft(last) : 0
  if (last && left > 1) {
    const name = who(last)?.name ?? 'the agent', id = last.id
    toast(`Decided. It goes to ${name} in a few seconds.`, { timeout: (left - 1) * 1000, action: { label: 'Undo', run: () => undoAfterClose(id) } })
  }
  const from = route.query.from
  if ((from === 'preview' || from === 'agents') && typeof window.history.state?.back === 'string') {
    const target = from === 'preview' ? '[data-place="desk"] .place-main' : '#agents-desk-title'
    const stop = router.afterEach(() => { stop(); void nextTick(() => requestAnimationFrame(() => document.querySelector<HTMLElement>(target)?.focus({ preventScroll: true }))) })
    router.back()
    return
  }
  void nextTick(() => { if (document.activeElement === document.body || !document.activeElement) (document.querySelector<HTMLElement>('[data-testid="desk-one-by-one"]') ?? document.getElementById('desk-title'))?.focus({ preventScroll: true }) })
}
// Undo after closing acts as Change until a withdraw exists (AEON-1340): the memo reopens on the answer.
function undoAfterClose(id: string) {
  if (alive && route.path === '/decision-desk') {
    if (!items.value.some(row => row.id === id && row.decided)) return
    filter.value = 'decided'; outcomeFilter.value = ''; changeId.value = id; begin(id); return
  }
  void router.push({ path: '/decision-desk', query: { item: id, change: '1' } })
}
function undoRow(item: DeskItem) { filter.value = 'decided'; outcomeFilter.value = ''; changeId.value = item.id; begin(item.id) }
function beginOpen() { if (!openRound.value.length) return; filter.value = 'open'; begin() }
let clockTimer: ReturnType<typeof setInterval> | undefined
let poll: ReturnType<typeof setInterval> | undefined
const focus = () => { if (!loading.value) void refresh() }
onMounted(() => { window.addEventListener('focus', focus); poll = setInterval(() => { if (!document.hidden && !loading.value) void refresh() }, 30_000); clockTimer = setInterval(() => { now.value = Date.now() }, 1000) })
onBeforeUnmount(() => { alive = false; generation++; ticketTurn++; stopAccess(); clearInterval(poll); clearInterval(clockTimer); window.removeEventListener('focus', focus) })
</script>

<template>
  <main class="desk-page decision-desk" aria-labelledby="desk-title">
    <header class="dp-head">
      <div><h1 id="desk-title" tabindex="-1">Decision Desk</h1><p class="head-note">Agents keep working unless something is held up.</p></div>
      <!-- The slot stays on both tabs, so the tabs below never move (AEON-541); it starts the open round. -->
      <button type="button" class="btn" :style="{ visibility: openRound.length || scopeActive ? 'visible' : 'hidden' }" :disabled="!openRound.length" :aria-hidden="!openRound.length && !scopeActive || undefined" data-testid="desk-one-by-one" @click="beginOpen"><AppIcon name="arrow" :size="14" />Answer one by one</button>
    </header>
    <div class="dp-bar">
      <div class="seg view-tabs" role="radiogroup" aria-label="Show">
        <button type="button" role="radio" :aria-checked="filter === 'open'" data-testid="desk-tab-open" @click="filter = 'open'">Open<span class="n">{{ openCountLabel }}</span></button>
        <button type="button" role="radio" :aria-checked="filter === 'decided'" data-testid="desk-tab-decided" @click="filter = 'decided'">Decided<span class="n">{{ decidedCountLabel }}</span></button>
      </div>
      <div v-if="filter === 'decided'" class="seg dec-filter" role="radiogroup" aria-label="Stamp">
        <button v-for="option in ([['', 'All'], ['once', 'Once'], ['always', 'Always'], ['requirement', 'Requirement'], ['doctrine', 'Doctrine'], ['approvals', 'Approvals']] as const)" :key="option[0]" type="button" role="radio" :aria-checked="outcomeFilter === option[0]" @click="outcomeFilter = option[0]">{{ option[1] }}</button>
      </div>
    </div>
    <p v-if="scopeActive" class="scope-line" data-testid="desk-scope">{{ scopeCopy }} <RouterLink :to="showAllTo">Show all</RouterLink></p>
    <nav v-if="nextKeyTrims[trimState] || trimCursors[trimState]" class="trim-pages" aria-label="Key trim pages"><button type="button" class="btn sm ghost" :disabled="loading || !trimCursors[trimState]" @click="pageKeyTrims(true)">First key trims</button><button type="button" class="btn sm ghost" :disabled="loading || !nextKeyTrims[trimState]" @click="pageKeyTrims()">Next key trims</button><span>Up to 100 key trims per page</span></nav>
    <section class="queue glass-card list-only" :class="{ clear: filter !== 'open' || !visible.length }" :aria-label="filter === 'open' ? 'Open' : 'Decided'" :aria-busy="loading" data-testid="desk-list">
      <p v-if="state === 'loading'" class="empty-line" role="status"><AppIcon name="clock" :size="16" /><span>Reading the desk…</span></p>
      <template v-else-if="filter === 'open'">
        <p v-if="!visible.length" class="empty-line" data-testid="desk-empty"><AppIcon :name="sourceLines.some(line => line.retry) ? 'alert' : 'check'" :size="16" /><span>{{ scopeActive ? emptyCopy : sourceLines.some(line => line.retry) ? 'Nothing to show from the sources that answered' : 'Nothing waits for you' }}</span></p>
        <div v-for="group in [{ key: 'held', label: 'Waiting on you', note: 'something is held up until you answer', rows: heldRows }, { key: 'ready', label: 'When you’re ready', note: 'the agents carry on meanwhile', rows: readyRows }].filter(group => group.rows.length)" :key="group.key" class="q-group" :class="group.key">
          <p class="q-group-label">{{ group.label }}<span class="n">{{ group.rows.length }}</span><span class="group-note">{{ group.note }}</span></p>
          <ul class="items desk-list" :aria-label="group.label">
            <li v-for="item in group.rows" :key="item.id" class="item desk-row" :class="{ blocks: item.held && (item.kind === 'question' || item.kind === 'handover'), high: !!item.approval && riskFor(item.approval) === 'high', open: rowForm?.id === item.id }" :data-testid="`desk-row-${item.id}`">
              <component :is="item.linkOut ? RouterLink : 'button'" v-bind="item.linkOut ? { to: item.linkOut } : { type: 'button' }" class="row-open" :aria-label="`${kindLabels[item.kind]}: ${item.title}`" @click="item.linkOut || begin(item.id)" />
              <span class="mark" :class="`k-${item.kind}`" aria-hidden="true"><AppIcon :name="KIND_ICON[item.kind]" :size="16" /></span>
              <div class="body">
                <p class="line1"><strong class="what"><span v-if="item.kind === 'lesson'">Lesson to adopt · </span>{{ item.title }}</strong><span class="meta"><time v-if="item.expiresAt" class="expiry" :class="{ soon: soon(item.expiresAt) }" :datetime="item.expiresAt">{{ expiry(item.expiresAt) }}</time><time v-else-if="item.createdAt" class="expiry" :datetime="item.createdAt">{{ ago(item.createdAt) }}</time><span v-if="item.approval" class="risk" :class="{ high: riskFor(item.approval) === 'high' }">{{ RISK_LABEL[riskFor(item.approval)] }}</span></span></p>
                <p class="line2"><span v-if="who(item)" class="who" :data-tip="who(item)?.model || undefined"><HarnessMark v-if="who(item)?.harness" :harness="who(item)!.harness!" :size="14" /><span>{{ who(item)!.name }}</span></span><span class="phrase"><span v-if="item.kind === 'rule'" class="asks">proposes a rule change for</span><template v-else-if="item.kind === 'action'"><span class="asks">to</span><span class="to">you</span><span class="asks">on</span></template><span v-else-if="who(item)" class="asks">on</span><span class="proj-quiet" data-testid="desk-row-project"><AppIcon name="folder" :size="13" />{{ item.projectName }}</span><template v-if="ticketOf(item)"><span class="res-key">{{ ticketOf(item)!.key }}</span><span v-if="ticketOf(item)!.title" class="res-title" :data-tip="ticketOf(item)!.title">{{ ticketOf(item)!.title }}</span></template></span></p>
                <p v-if="item.approval?.resource_kind" class="target">{{ item.approval.resource_kind === 'tenant' ? 'The whole workspace' : `${item.approval.resource_kind === 'node' ? 'Ticket' : 'Run'} ${item.approval.resource_id || 'not named'}` }}</p>
                <p v-if="item.kind !== 'question' && item.kind !== 'handover' && item.context" class="why" :class="{ 'body-text': item.kind === 'action' }">{{ item.context }}</p>
                <p v-if="item.kind === 'question' || item.kind === 'handover'"><span class="status-line" :class="{ park: meanwhileLine(item).held }"><AppIcon :name="meanwhileLine(item).icon" :size="13" /><span>{{ meanwhileLine(item).title }}<span v-if="meanwhileLine(item).text" class="mt"> · {{ meanwhileLine(item).text }}</span></span></span></p>
                <p v-if="recommends(item)" class="rec-line"><span class="rec-word">Recommends</span><span>{{ recommends(item) }}</span></p>
                <p v-if="item.unavailable" class="row-note">{{ item.unavailable }}</p>
                <form v-if="rowForm?.id === item.id" class="decision" @submit.prevent="commitRow(item)" @keydown="rowFormKeys($event, item)">
                  <label :for="`desk-reason-${item.id}`">{{ ROW_FORM[rowForm.mode].label }}</label>
                  <textarea :id="`desk-reason-${item.id}`" v-model="rowForm.reason" class="field" rows="2" maxlength="2000" :placeholder="ROW_FORM[rowForm.mode].placeholder" :disabled="rowForm.busy" />
                  <p v-if="rowForm.error" class="row-note" role="alert">{{ rowForm.error }}</p>
                  <div class="decision-actions"><span class="hint"><KeyCap k="mod" /><KeyCap k="enter" /> to {{ rowForm.mode }} · <KeyCap k="esc" /> to cancel</span><button type="button" class="btn sm ghost" :disabled="rowForm.busy" @click="rowForm = undefined">Cancel</button><button type="submit" class="btn sm" :class="rowForm.mode === 'deny' ? 'deny' : rowForm.mode === 'approve' || rowForm.mode === 'resolve' ? 'primary' : ''" :disabled="rowForm.busy"><AppIcon :name="ROW_FORM[rowForm.mode].icon" :size="13" />{{ ROW_FORM[rowForm.mode].submit }}</button></div>
                </form>
              </div>
              <div v-if="rowForm?.id !== item.id" class="row-actions" data-testid="desk-row-actions">
                <RouterLink v-if="item.linkOut" class="btn sm approve-soft" :to="item.linkOut">Open in Settings</RouterLink>
                <template v-else-if="item.kind === 'approval' && allowed(item) && !item.unavailable"><button type="button" class="btn sm ghost" @click="openRowForm(item, 'deny')"><AppIcon name="close" :size="13" />Deny</button><button type="button" class="btn sm approve-soft" @click="openRowForm(item, 'approve')"><AppIcon name="check" :size="13" />Approve</button></template>
                <template v-else-if="item.kind === 'action' && allowed(item) && !item.unavailable"><button type="button" class="btn sm ghost answer" @click="begin(item.id)"><AppIcon name="send" :size="13" />Answer…</button><button type="button" class="btn sm ghost" @click="openRowForm(item, 'dismiss')"><AppIcon name="close" :size="13" />Dismiss</button><button type="button" class="btn sm approve-soft" @click="openRowForm(item, 'resolve')"><AppIcon name="check" :size="13" />Resolve</button></template>
                <button v-else type="button" class="btn sm approve-soft" @click="begin(item.id)">{{ item.kind === 'question' || item.kind === 'handover' ? 'Answer…' : 'Review…' }}</button>
              </div>
            </li>
          </ul>
        </div>
      </template>
      <template v-else>
        <p v-if="!visible.length" class="empty-line" data-testid="desk-empty"><AppIcon name="check" :size="16" /><span>{{ outcomeFilter ? 'Nothing decided with this stamp yet' : 'No decisions recorded yet' }}</span></p>
        <ul v-else class="dec-list desk-list" aria-label="Decided">
          <li v-for="item in visible" :key="item.id" class="dec desk-row" :data-testid="`desk-row-${item.id}`">
            <component :is="item.linkOut ? RouterLink : 'button'" v-bind="item.linkOut ? { to: item.linkOut } : { type: 'button' }" class="row-open" :aria-label="`${kindLabels[item.kind]}: ${item.title}`" @click="item.linkOut || begin(item.id)" />
            <span class="mark" :class="outcomeOf(item).tone" aria-hidden="true"><AppIcon :name="outcomeOf(item).icon" :size="15" /></span>
            <span class="dec-text"><span class="dec-q">{{ item.title }}</span><span v-if="item.answer" class="dec-a">{{ item.answer }}</span><span class="dec-meta"><template v-if="who(item)"><span>{{ who(item)!.name }}</span><span>·</span></template><span class="proj-quiet"><AppIcon name="folder" :size="13" />{{ item.projectName }}</span><template v-if="ticketOf(item)"><span>·</span><span>{{ ticketOf(item)!.key }}</span></template><template v-if="item.decidedAt || item.createdAt"><span>·</span><span>decided {{ ago(item.decidedAt || item.createdAt) }}</span></template><template v-if="graceLeft(item)"><span>·</span><span>sends in <span class="n">{{ graceLeft(item) }}</span> s</span></template><template v-else-if="item.deliveryState === 'failed'"><span>·</span><span class="dl-bad">not delivered</span></template><template v-else-if="item.deliveryState"><span>·</span><span>waiting for it to read</span></template></span></span>
            <span class="dec-side"><span class="dec-out" :class="outcomeOf(item).tone"><AppIcon :name="outcomeOf(item).icon" :size="13" />{{ outcomeOf(item).word }}</span><button v-if="graceLeft(item)" type="button" class="btn sm ghost" @click="undoRow(item)"><AppIcon name="rollback" :size="13" />Undo</button></span>
          </li>
        </ul>
      </template>
      <div v-if="sourceLines.length || more[filter === 'open' ? 'open' : 'answered']" class="source-warnings" role="status">
        <p v-for="line in sourceLines" :key="line.text" class="src-error"><AppIcon name="alert" :size="14" /><span>{{ line.text }}</span><template v-if="line.retry"><span class="sep">·</span><button type="button" class="linkish" :disabled="loading" @click="refresh">Try again</button></template></p>
        <p v-if="more[filter === 'open' ? 'open' : 'answered']" class="src-error"><AppIcon name="list" :size="14" /><span>More questions are available</span><span class="sep">·</span><button v-if="pages[filter === 'open' ? 'open' : 'answered'] < MAX_QUESTION_PAGES" type="button" class="linkish" :disabled="loading" @click="loadMore">Load 100 more</button><span v-else>the view shows up to 1,000 questions per state</span></p>
      </div>
    </section>
    <p v-if="opened && fresh.length" class="arrival-line" role="status">{{ fresh.length }} new item{{ fresh.length === 1 ? '' : 's' }} waiting for the next round.</p>
    <DecisionDeskMemo v-if="opened" :key="`${owner}:${memoRound}`" :items="roundItems" :round="round" :start="start" :arrivals-count="fresh.length" :pending-rule-ids="items.filter(row => row.kind === 'rule' && !row.decided).map(row => row.id.slice(2))" :allowed="allowed" :decide="decide" :stepup-method="stepupMethod" :can-revoke="canRevoke" :revoke="revoke" :edit-rule="editRule" :undo="undoLesson" :who="who" :ticket="ticketOf" :change="changeId || undefined" @recorded="recorded" @close="closed" />
    <DoctrineProposalDialog v-if="ruleEdit" :key="owner" :source="ruleEdit.source" :file="ruleEdit.file" :rule="ruleEdit.rule" :draft="ruleEdit.draft" @close="ruleEdit = undefined" @saved="editedRule" />
  </main>
</template>

<style scoped>
/* AEON-1339: the Desk page as the approved rev 11 artifact draws it. */
.scope-line { display: flex; align-items: center; flex-wrap: wrap; column-gap: 16px; min-height: 44px; margin: 0; font-size: 12px; color: var(--ink-3); }.scope-line a { color: var(--teal-ink); text-decoration: none; display: inline-flex; align-items: center; min-height: 44px; }.scope-line a:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }
.desk-page { width: 100%; max-width: 1060px; margin: 0 auto; padding: 34px max(16px, min(4vw, 40px)) 72px; color: var(--ink); }
.dp-head { display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: 14px 24px; margin-bottom: 18px; }
.dp-head h1 { margin: 0; font: 300 clamp(28px, 3.4vw, 36px)/1.1 var(--serif); letter-spacing: -.025em; outline: none; }
.dp-head .head-note { margin: 8px 0 0; font-size: 13px; color: var(--ink-3); }
.dp-head .btn { min-height: 36px; }
.dp-bar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 14px; margin-bottom: 14px; }
.dp-bar .seg button .n { font: 500 11.5px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.dp-bar .seg button[aria-checked="true"] .n { color: var(--teal-ink); }
.dp-bar .dec-filter { margin-left: auto; flex-wrap: wrap; border-radius: 18px; }
.trim-pages { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 12px; margin-bottom: 12px; font-size: 12px; color: var(--ink-3); }
/* The list card: a warm full tint and a soft gold ring while something waits, never an edge bar. */
.queue { --agent-state-color: var(--agent-waiting); overflow: clip; container: queue / inline-size; }
.queue.glass-card:not(.clear) { background: linear-gradient(165deg, color-mix(in srgb, var(--gold-2) 16%, var(--surface-raised-2)), color-mix(in srgb, var(--gold-2) 10%, var(--glass)) 60%); box-shadow: var(--shadow), 0 0 0 1px color-mix(in srgb, var(--gold) 38%, transparent); }
.q-group-label { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; min-height: 28px; margin: 10px 0 0; padding: 0 18px; font: 500 10.5px/1.3 var(--mono); letter-spacing: .16em; text-transform: uppercase; color: var(--ink-3); }
.q-group-label .n { letter-spacing: 0; }
.q-group-label .group-note { font: 450 12px/1.3 var(--font); letter-spacing: 0; text-transform: none; color: var(--ink-3); }
.q-group.held .q-group-label { color: var(--queue-wait-ink); }
.items { margin: 0; padding: 4px 8px 8px; list-style: none; display: flex; flex-direction: column; gap: 4px; }
.item { position: relative; display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; gap: 12px; align-items: start; padding: 12px 12px 12px 10px; border-radius: 12px; }
.queue.glass-card:not(.clear) .item + .item { box-shadow: 0 -1px 0 color-mix(in srgb, var(--gold) 18%, transparent); }
@media (hover: hover) { .item:hover { background: var(--row-hover); } }
.item.open { background: var(--row-selected); }
/* The whole row opens the item; its inline actions and links sit above that target. */
.row-open { position: absolute; inset: 0; z-index: 0; width: 100%; padding: 0; border: 0; border-radius: inherit; background: transparent; cursor: pointer; }
.row-open:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.item > :not(.row-open), .dec > :not(.row-open) { position: relative; z-index: 1; pointer-events: none; }
.item :is(.row-actions, form, .who[data-tip], .res-title, a, button:not(.row-open)), .dec :is(.dec-side) { pointer-events: auto; }
.mark { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 9px; color: var(--agent-state-color); background: color-mix(in srgb, var(--agent-state-color) 10%, var(--surface-raised)); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--agent-state-color) 25%, transparent); }
/* A question is teal while the agent carries on, amber when it holds something up. */
.mark.k-question, .mark.k-handover { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.item.blocks .mark { color: var(--queue-wait-ink); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.mark:is(.k-rule, .k-action, .k-tier, .k-key_trim, .k-account_matrix) { color: var(--ink-2); background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
.item.high .mark { color: var(--danger); background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.body { display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; min-width: 0; }
.body p { margin: 0; }
.line1 { display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 10px; min-height: 22px; }
.what { font-size: 14px; font-weight: 650; color: var(--ink); text-wrap: pretty; overflow-wrap: anywhere; }
.meta { display: inline-flex; align-items: baseline; gap: 8px; }
.expiry { font-size: 12px; color: var(--ink-3); white-space: nowrap; font-variant-numeric: tabular-nums; }
.expiry.soon { color: var(--gold-ink); font-weight: 600; }
.risk { display: inline-flex; align-items: center; gap: 4px; color: var(--ink-3); font-size: 12px; white-space: nowrap; }
.risk.high { color: var(--danger); font-weight: 600; }
.line2 { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 6px; font-size: 12.5px; color: var(--ink-2); }
.phrase { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 6px; min-width: 0; }
.who { display: inline-flex; align-items: center; gap: 5px; height: 24px; margin-left: -4px; padding: 0 6px 0 4px; border-radius: 7px; color: var(--ink); font-size: 12.5px; font-weight: 600; }
.asks { color: var(--ink-3); }
.to { color: var(--ink); font-weight: 550; }
.proj-quiet { display: inline-flex; align-items: center; gap: 5px; margin-right: 2px; color: var(--ink-2); white-space: nowrap; }
.proj-quiet svg { color: var(--ink-3); }
.res-key { display: inline-flex; align-items: center; padding: 3px 7px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 11.5px/1 var(--mono); font-variant-ligatures: none; white-space: nowrap; }
.res-title { min-width: 0; max-width: 42ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); }
.why { margin-top: 2px !important; font-size: 13px; color: var(--ink-2); line-height: 1.45; overflow: hidden; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; overflow-wrap: anywhere; }
.why.body-text { -webkit-line-clamp: 3; line-clamp: 3; color: var(--ink); }
.target { font: 12px var(--mono); color: var(--ink-2); overflow-wrap: anywhere; }
.status-line { display: inline-flex; align-items: flex-start; gap: 6px; min-width: 0; font-size: 12.5px; color: var(--ink-2); }
.status-line svg { flex: none; margin-top: 2px; }
.status-line.park { color: var(--queue-wait-ink); font-weight: 600; }
.status-line .mt { font-weight: 450; color: var(--ink-2); }
.rec-line { display: flex; align-items: baseline; flex-wrap: wrap; gap: 2px 8px; font-size: 13px; color: var(--ink); }
.rec-word { font: 500 10px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.row-note { font-size: 12.5px; color: var(--ink-2); }
.row-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; align-self: center; min-width: 0; max-width: 100%; }
.row-actions .btn.ghost:not(.answer) { color: var(--ink-2); }
.answer { color: var(--teal-ink); }
.btn.approve-soft { border-color: transparent; background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.btn.approve-soft:hover { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--teal); }
.btn.deny { color: var(--danger-on); background: var(--danger); border-color: transparent; }
.btn.deny:hover { filter: brightness(1.06); background: var(--danger); }
.decision { display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; margin-top: 8px; }
.decision label { font-size: 12px; font-weight: 600; color: var(--ink-2); }
.decision textarea { width: 100%; height: auto; min-height: 58px; resize: vertical; padding: 8px 10px; font-size: 13.5px; line-height: 1.4; }
.decision-actions { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 8px; }
.decision-actions .hint { margin-right: auto; display: inline-flex; align-items: center; gap: 4px; font-size: 11.5px; color: var(--ink-3); }
/* Decided rows: the outcome as an icon and a coloured word, the delivery after who and when. */
.dec-list { display: grid; gap: 2px; margin: 0; padding: 6px 8px 8px; list-style: none; }
.dec { position: relative; display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; gap: 2px 12px; align-items: start; padding: 10px; border-radius: 10px; color: var(--ink); }
@media (hover: hover) { .dec:hover { background: var(--row-hover); } }
.dec .mark { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.dec .mark.teal { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.dec .mark.ok { background: color-mix(in srgb, var(--ok) 12%, var(--surface-raised)); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--ok) 28%, transparent); color: var(--ok); }
.dec .mark.gold { background: var(--gold-wash); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--gold) 35%, transparent); color: var(--gold-ink); }
.dec .mark.bad { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.dec-text { display: grid; gap: 2px; min-width: 0; }
.dec-q { font-size: 13.5px; font-weight: 600; line-height: 1.35; overflow-wrap: anywhere; }
.dec-a { font-size: 13px; line-height: 1.4; color: var(--ink-2); overflow-wrap: anywhere; }
.dec-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 6px; font-size: 12px; color: var(--ink-3); }
.dec-meta .dl-bad { color: var(--danger); font-weight: 600; }
.dec-side { display: grid; justify-items: end; gap: 6px; }
.dec-out { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; font-weight: 650; white-space: nowrap; }
.dec-out.ok { color: var(--ok); } .dec-out.teal { color: var(--teal-ink); } .dec-out.gold { color: var(--gold-ink); } .dec-out.bad { color: var(--danger); } .dec-out.ink { color: var(--ink-2); }
.n { display: inline-block; min-width: 2ch; text-align: right; font-variant-numeric: tabular-nums; }
/* One calm line where a source failed, at the end of the list, with its action inline. */
.source-warnings { display: grid; gap: 0; padding: 2px 8px 10px; }
.src-error { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; margin: 0; padding: 10px 12px; font-size: 12.5px; color: var(--ink-2); }
.src-error svg { color: var(--ink-3); }
.src-error .sep { color: var(--ink-3); }
.linkish { display: inline; padding: 0; border: 0; border-radius: 4px; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; }
.linkish:hover { text-decoration: underline; text-underline-offset: 2px; }
.linkish:disabled { opacity: .55; cursor: default; }
.linkish:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.empty-line { display: flex; align-items: center; gap: 8px; margin: 0; padding: 26px 22px; font-size: 14px; color: var(--ink-2); }
.empty-line svg { color: var(--ok); }
.arrival-line { margin: 12px 4px 0; font-size: 12px; color: var(--teal-ink); }
@container queue (max-width: 760px) {
  .item { grid-template-columns: 30px minmax(0, 1fr); }
  .row-actions { grid-column: 2; justify-self: start; }
  .dec { grid-template-columns: 30px minmax(0, 1fr); }
  .dec-side { grid-column: 2; justify-items: start; }
}
@media (max-width: 720px) {
  .desk-page { padding-top: 24px; }
  .dp-head .btn { width: 100%; min-height: 44px; }
  .dp-bar .dec-filter { margin-left: 0; }
  .item { padding: 12px 10px; }
  .row-actions .btn { height: 40px; padding: 0 12px; }
  .decision-actions .hint { display: none; }
  .decision-actions .btn { height: 40px; }
  .res-title { max-width: 100%; }
  .q-group-label { padding: 0 14px; }
}
@media (pointer: coarse) { .seg button { height: 40px; min-width: 44px; } .dp-bar { gap: 14px; } .linkish { position: relative; } .linkish::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: max(100%, 44px); transform: translate(-50%, -50%); } }
</style>
