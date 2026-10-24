<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import DecisionDeskMemo from '../components/agents/DecisionDeskMemo.vue'
import PersonStepRequest from '../components/agents/PersonStepRequest.vue'
import DoctrineProposalDialog from '../components/rules/DoctrineProposalDialog.vue'
import DeskSyncLine from '../components/agents/DeskSyncLine.vue'
import { deskClock, deskClockMoved, deskSyncAnnouncement, deskSyncCopy, deskSyncLine } from '../lib/deskSync'
import { getDoctrine, type DoctrineSource, type DoctrineFile, type DoctrineRule, type DoctrineInboxItem, type DoctrineProposal } from '../lib/doctrine'
import { arrivals, deskAutoRetry, deskEmptyState, deskFailedWords, deskItemID, deskLinkItem, deskReadAborted, deskRefreshNotice, deskSourceLines, deskUnansweredLine, draftFor, kindLabels, newRound, outcomeLabels, retainDecidedLessons, runRef, type DeskClose, type DeskDraft, type DeskItem, type DeskOutcome, type DeskProjection, type DeskRetryPhase, type DeskSource, type DeskTicket, type DeskWho } from '../lib/decisionDesk'
import AppIcon, { type IconName } from '../components/AppIcon.vue'
import KeyCap from '../components/KeyCap.vue'
import HarnessMark from '../components/agents/HarnessMark.vue'
import { agentName, harnessLabel, RISK_LABEL, riskFor } from '../lib/agentState'
import { captureToastOwner, toast } from '../lib/toast'
import { approvalItem, commitDesk, decisionPermission, emptySources, loadDesk, loadMoreQuestions, nativeTierAdapter, questionItem, readApproval, readDeskTickets, readQuestion, tierSessionsFromRefresh, MAX_QUESTION_PAGES, type DeskRead, type DeskSources } from '../lib/decisionDeskApi'
import { phoneCapability } from '../lib/deskPhoneApproval'
import { settledElsewhere } from '../lib/stepup'
import { can, onAccessRevalidate } from '../lib/authz'
import { definitionDeskWords } from '../lib/definitions'
import { displayLanguage } from '../lib/displayLanguage'
import { deskOfferDismissed, deskOfferEligible, deskPushBrowser, deviceSubscriptionHash, dismissDeskOffer, phoneSettings, setDeskSubscription, type PhoneSettings } from '../lib/phoneApprovals'
import { useIdentityScope } from '../lib/useIdentityScope'
import { useSession } from '../stores/session'
import { useAgents } from '../stores/agents'
import type { KeyTrimCursors } from '../lib/keyTrim'
import { useDecisionDesk } from '../stores/decisionDesk'
import { useDeskConnections } from '../stores/deskConnections'
import DeskInstanceItems from '../components/DeskInstanceItems.vue'
const elsewhere = useDeskConnections()
import { revokeApproval } from '../lib/agents'
import { getRun } from '../lib/agentRows'
import { undoKnowledge } from '../lib/knowledge'
import { doctrineInbox, inboxChanged } from '../lib/doctrineInbox'
import deskLight from '../assets/desk/desk-empty-light.webp'
import deskDark from '../assets/desk/desk-empty-dark.webp'

const session = useSession()
const noticeDe = computed(() => displayLanguage() === 'de')
const noticeText = (en: string, de: string) => noticeDe.value ? de : en
const noticeWords = computed(() => ({
  offer: noticeText('Get a browser notification when a decision waits', 'Browser-Benachrichtigung erhalten, wenn eine Entscheidung wartet'),
  // The offer is the longer sentence in both languages, so the shared slot keeps Turn on beside it (AEON-1617).
  failed: noticeText('Notifications could not be turned on.', 'Benachrichtigungen konnten nicht eingeschaltet werden.'),
}))
const noticeSettings = ref<PhoneSettings | null>(null), noticeHash = ref(''), noticeDismissed = ref(deskOfferDismissed())
const noticeBusy = ref(false), noticeOn = ref(false), noticeError = ref(false), noticeBrowser = ref(deskPushBrowser())
const noticeScope = useIdentityScope(() => session.identity?.principal.kind === 'person' && can('profile.read') && can('profile.write'))
const noticeReads = noticeScope.lane()
const showNotice = computed(() => !!noticeSettings.value && deskOfferEligible(noticeSettings.value, noticeHash.value, noticeBrowser.value, noticeDismissed.value))
// The offer is decided before anything below the title shows, so it never arrives above the tabs later, and a Desk
// without it keeps the subtitle's own height (AEON-1448: no band). A read slower than the bound leaves the offer out
// for this visit. Once shown, the offer keeps its slot for the visit; later states swap their words in place.
const NOTICE_WAIT_MS = 800
// A browser that already granted notifications never waits for the read: it is opted in or chose elsewhere (Tick).
const noticePossible = () => { const browser = deskPushBrowser(); return browser.supported && !browser.homeScreenRequired && browser.permission === 'default' && !deskOfferDismissed() }
const noticeDeciding = ref(noticePossible()), noticeHeld = ref(false)
// A failed Turn on keeps the offer, with the failure in its sentence, even once the browser reports a refusal.
const offerShown = computed(() => noticeHeld.value && !noticeOn.value && (noticeError.value || showNotice.value))
let noticeTimer: ReturnType<typeof setTimeout> | undefined
function decideNotice() {
  if (!noticeDeciding.value) return
  clearTimeout(noticeTimer)
  noticeHeld.value = showNotice.value; noticeDeciding.value = false
}
function loadNotice() {
  if (!noticePossible()) { decideNotice(); return }
  return noticeReads.run(({ after, signal }) => after(Promise.all([phoneSettings(signal), deviceSubscriptionHash()]), ([value, hash]) => {
    noticeSettings.value = value; noticeHash.value = hash; noticeBrowser.value = deskPushBrowser()
  }), { failed: () => { noticeSettings.value = null }, settled: decideNotice })
}
function turnNoticeOn() {
  if (noticeBusy.value || !offerShown.value || !noticeSettings.value || !noticeScope.owner.value) return
  // Keep the browser's user activation: never request permission during load.
  const permission = Notification.requestPermission(), publicKey = noticeSettings.value.vapid_public_key
  noticeReads.cancel(); noticeBusy.value = true; noticeError.value = false
  void noticeScope.run(({ after, signal }) => after(setDeskSubscription(true, publicKey, permission, signal), () => {
    noticeOn.value = true; noticeSettings.value = null
  }), { failed: () => { noticeError.value = true }, settled: () => { noticeBusy.value = false; noticeBrowser.value = deskPushBrowser() } })
}
function dismissNotice() { noticeDismissed.value = true; noticeError.value = false; dismissDeskOffer() }
function resetNotice() {
  noticeSettings.value = null; noticeHash.value = ''; noticeBusy.value = false; noticeOn.value = false; noticeError.value = false
  noticeDismissed.value = deskOfferDismissed()
}
watch(noticeScope.owner, () => { resetNotice(); void loadNotice() }, { flush: 'sync' })
const stopNoticeAccess = onAccessRevalidate({ reset: resetNotice, refresh: () => { void loadNotice() } })
onBeforeUnmount(() => { stopNoticeAccess(); clearTimeout(noticeTimer) })
onMounted(() => {
  if (noticeDeciding.value) noticeTimer = setTimeout(() => { noticeReads.cancel(); decideNotice() }, NOTICE_WAIT_MS)
  void loadNotice()
})
const definitionLanguage = computed(() => displayLanguage())
const titleOf = (item: DeskItem) => item.definitionChange ? definitionDeskWords(item.definitionChange.project_key,item.definitionChange.pending_count,definitionLanguage.value) : item.title
const agents = useAgents()
const desk = useDecisionDesk(), route = useRoute(), router = useRouter()
const makeAdapters = () => ({ approvalRun: async (id: string) => {
  const identity = session.identity
  const run = await getRun(id)
  if (session.identity !== identity || !session.authenticationCurrent()) throw new Error('The person changed while reading approval ownership.')
  return agents.admitRun(run)
}, tiers: nativeTierAdapter(async () => tierSessionsFromRefresh(await agents.refreshSessions(), agents.sessions), can, async (project, id) => {
  await agents.loadSessionDetail(project, id)
  return agents.sessionById(id)
}) })
let adapters = makeAdapters()
const owner = computed(() => `${session.identity?.tenant.id ?? ''}:${session.identity?.principal.id ?? ''}`)
const items = ref<DeskItem[]>([]), roundItems = ref<DeskItem[]>([]), round = ref<string[]>([]), start = ref(''), opened = ref(false)
// Same-view links can close and reopen in one render; each new round must
// discard the prior memo's index, drafts and pending context reads.
const memoRound = ref(0)
const state = ref<'loading' | 'ready'>('loading'), warnings = ref<string[]>([]), unanswered = ref<DeskSource[]>([]), more = ref({ open: false, answered: false }), loading = ref(false), pages = ref({ open: 1, answered: 1 }), filter = ref<'open' | 'decided'>('open'), outcomeFilter = ref<DeskOutcome | 'approvals' | ''>('')
const roundBaseline = ref<string[]>([]), phoneVerification = ref<boolean | undefined>(), capabilityError = ref('')
// Names the step-up method on Approve; the server makes the actual choice.
const stepupMethod = ref('Passkey or sign-in')
const trimCursors = ref<KeyTrimCursors>({}), nextKeyTrims = ref<KeyTrimCursors>({})
const trimState = computed(() => filter.value === 'open' ? 'pending' : 'decided')
let sources: DeskSources = emptySources(), generation = 0, alive = true
let capabilityGeneration = 0
let currentRead: DeskRead | undefined
let hydratedProjection: DeskProjection | undefined, hydration: Promise<void> | undefined
const lastGoodAt = ref<number | null>(null), hydrationFailures = ref(0), hydrationFailedSince = ref<number | null>(null)
// AEON-1700: the store's failed projection reads, kept until a hydration adopts a good read. A projection that answers
// clears the store's count before its rows are read; the Desk is up to date again only once they are.
const outageFailures = ref(0), outageSince = ref<number | null>(null)
watch(() => [desk.failures, desk.failedSince] as const, ([count, since]) => {
  outageFailures.value = Math.max(outageFailures.value, count)
  if (since !== null) outageSince.value = Math.min(outageSince.value ?? since, since)
}, { flush: 'sync', immediate: true })
// AEON-1631: good rows kept from this visit while a later read missed the canonical desk, and the automatic retries' phase.
const stale = ref(false), retryPhase = ref<DeskRetryPhase>('idle')
const linkError = ref(''), revoked = ref(new Set<string>()), settledSteps = ref(new Set<string>()), pendingSteps = ref(new Set<string>())
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
  if (scopeActive.value && place) return `Nothing waits for you in ${place}`
  if (scopeActive.value) return 'Nothing in this view is waiting.'
  if (!desk.allClear) return 'Nothing waits here'
  return 'Nothing is waiting in the available sources.'
})
const showAllTo = computed(() => {
  const query = { ...route.query }
  delete query.kind
  delete query.project
  return { path: '/decision-desk', query }
})
// A scoped Open count matches the list. The header keeps the whole-desk total.
const openSourceIDs = computed(() => desk.projection ? new Set(desk.projection.items.map(deskItemID)) : null)
// Retained person-step slots keep Undo still, but only the canonical open
// source counts them, including while a committed write response is pending.
const openItems = computed(() => items.value.filter(item => !item.decided && inScope(item)
  && (!item.personStep || !openSourceIDs.value || openSourceIDs.value.has(item.id))))
// The first read shows no count yet (AEON-1406); the empty slot keeps the tab width.
// Once a read failed, its panel holds through the retries instead of returning to the reading frame (AEON-1631).
const reading = computed(() => state.value === 'loading' && !items.value.length && !desk.error && retryPhase.value === 'idle')
const openCountLabel = computed(() => {
  if (reading.value) return ''
  if (!scopeActive.value) return desk.count ?? '?'
  if (state.value !== 'ready' || desk.count === null) return '?'
  return openItems.value.length + elsewhere.count
})
const openCountTip = computed(() => scopeActive.value ? [`${openItems.value.length} here`, ...elsewhere.breakdown].join(' · ') : desk.countTip)
const groupOpenCount = (rows: DeskItem[]) => rows.filter(item => openItems.value.includes(item)).length
const decidedCountLabel = computed(() => reading.value ? '' : items.value.filter(item => item.decided && (!scopeActive.value || inScope(item))).length)
const visible = computed(() => items.value.filter(item => inScope(item) && (item.decided === (filter.value === 'decided') || filter.value === 'open' && item.personStep && settledSteps.value.has(item.id)) && (filter.value === 'open' || !outcomeFilter.value
  || (outcomeFilter.value === 'approvals' ? item.kind === 'approval' : (item.kind === 'question' || item.kind === 'handover') && item.outcome === outcomeFilter.value))))
// Link-out rows are decided on their settings page, so they never join a memo round.
const reviewable = computed(() => visible.value.filter(item => !item.linkOut && !item.personStep))
const person = computed(() => session.identity?.principal.kind === 'person')
// AEON-1339: the list in rev 11's two groups; what holds something up comes first.
const heldRows = computed(() => visible.value.filter(item => item.held || item.kind === 'approval'))
const readyRows = computed(() => visible.value.filter(item => !(item.held || item.kind === 'approval')))
const openRound = computed(() => items.value.filter(item => inScope(item) && !item.decided && !item.linkOut && !item.personStep))
// The ticket each open item is about, resolved as the memo resolves it (AEON-1339): bounded id and key lookups per read.
const tickets = ref(new Map<string, DeskTicket>())
// A run approval is about the ticket its run works on: the run's work order, read once per run ('' when no read
// named it), at most 30 runs per read, five at a time (AEON-1339). The memo says "reading" only while that runs.
const runTickets = ref(new Map<string, string>()), runsReading = ref(new Set<string>())
let ticketTurn = 0
async function readTickets(rows: readonly DeskItem[]) {
  const turn = ++ticketTurn, identity = owner.value, current = () => alive && turn === ticketTurn && identity === owner.value
  const runs = [...new Set(rows.filter(row => row.runId && !row.ticketId && !row.workOrderId && !runTickets.value.has(row.runId)).map(row => row.runId!))]
  const read = runs.slice(0, 30), named = new Map<string, string>(runs.slice(30).map(id => [id, '']))
  runsReading.value = new Set(read)
  try {
    for (let at = 0; at < read.length; at += 5) await Promise.all(read.slice(at, at + 5).map(async id => {
      try { const run = await adapters.approvalRun(id); named.set(id, run?.id === id ? run.work_order_id || '' : '') } catch { named.set(id, '') }
    }))
    if (!current()) return
    if (named.size) runTickets.value = new Map([...runTickets.value, ...named])
    const found = await readDeskTickets(rows.map(row => ({ ticketId: row.ticketId ?? workOrderOf(row), ticketKey: row.ticketKey })), tickets.value)
    if (!current() || !found.size) return
    tickets.value = new Map([...tickets.value, ...found])
  } catch { /* The rows keep their project; a ticket chip waits for the next read. */ }
  finally { if (turn === ticketTurn) runsReading.value = new Set() }
}
function workOrderOf(item: DeskItem) { return item.workOrderId || (item.runId ? runTickets.value.get(item.runId) : undefined) || undefined }
function ticketOf(item: DeskItem): DeskTicket | undefined {
  const id = item.ticketId ?? workOrderOf(item)
  return (id ? tickets.value.get(id) : undefined) ?? (item.ticketKey ? tickets.value.get(`key:${item.ticketKey}`) ?? { key: item.ticketKey, title: '' } : undefined)
}
const runPending = (item: DeskItem) => !!item.runId && runsReading.value.has(item.runId)
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
// The whole row opens the item. Text that carries a tooltip (the agent, the ticket title) takes the
// pointer for its tip, so its click is passed on here; controls and the row's own target act themselves.
function rowClick(event: MouseEvent, item: DeskItem) {
  if (!(event.target instanceof Element) || event.target.closest('button, a, form, input, textarea, select, label')) return
  if (item.linkOut) void router.push(item.linkOut)
  else begin(item.id)
}
// An action request is titled by its kind; its message names the row.
const rowName = (item: DeskItem) => item.definitionChange ? titleOf(item) : item.kind === 'action' && item.context ? `${kindLabels.action}: ${item.context.slice(0, 200)}` : `${kindLabels[item.kind]}: ${titleOf(item)}`
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
const sourceLines = computed(() => deskSourceLines(warnings.value, linkError.value !== ''))
const failedLines = computed(() => deskSourceLines(warnings.value).filter(line => line.retry))
// A first projection read that failed leaves nothing to list: the desk itself did not answer (AEON-1631).
const silent = computed<DeskSource[]>(() => !desk.projection && desk.error && !unanswered.value.includes('desk') ? [...unanswered.value, 'desk'] : unanswered.value)
// AEON-1406: the empty Open tab. A minor source that did not answer gives B, never A. AEON-1631: the desk, its
// questions or its approvals not answering, or a badge count beside an empty list, is never all clear.
const emptyState = computed(() => filter.value === 'open' ? deskEmptyState({ reading: reading.value, open: visible.value.length, scoped: scopeActive.value, unanswered: silent.value, failed: failedLines.value.length > 0,
  waiting: desk.hereCount, checking: (loading.value || desk.loading) && retryPhase.value === 'idle' }) : null)
// Reading, all clear and a silent source share one frame, so switching between them moves nothing.
const clearFrame = computed(() => !scopeActive.value && (emptyState.value === 'reading' || desk.allClear && (emptyState.value === 'clear' || emptyState.value === 'incomplete')))
const unansweredLine = computed(() => deskUnansweredLine(unanswered.value))
// AEON-1631 (Tick): a desk that did not answer takes the list's first-row slot with the count the badge knows.
const failedPanel = computed(() => (!desk.projection && !!desk.error) || emptyState.value === 'failed')
const failedWords = computed(() => deskFailedWords(scopeActive.value ? null : desk.hereCount, noticeDe.value))
// In that frame the quiet line names the failed sources once; truncation and link lines stay listed below.
// The failed panel already says the list did not load, so it lists nothing more.
const listedLines = computed(() => failedPanel.value ? [] : clearFrame.value ? sourceLines.value.filter(line => !failedLines.value.some(failed => failed.text === line.text)) : sourceLines.value)
// A project scope says its name and Show all in the empty line itself (design C), so the scope line steps aside.
const scopeInCard = computed(() => filter.value === 'open' && !visible.value.length && !!scopeProject.value && !scopeKind.value && !failedPanel.value)
// A failed read is retried on its own, 3, 10 and 30 s apart, one at a time; the 30 s poll goes on after that.
const failing = computed(() => failedPanel.value || stale.value)
// The pending retry's due time, from its own timer, for the sync line's countdown (AEON-1700).
const retryDue = ref<number | null>(null)
const autoRetry = deskAutoRetry(() => refresh(), () => alive && failing.value, (phase, dueAt) => { retryPhase.value = phase; retryDue.value = phase === 'retrying' ? dueAt ?? null : null })
watch(failing, now => { if (now) autoRetry.start(); else autoRetry.reset() })
// A person's Try again restarts the schedule; a read already under way is joined, never doubled.
async function tryAgain() {
  autoRetry.stop(); retryDue.value = null
  await refresh()
  if (!alive) return
  if (failing.value) autoRetry.start()
  else autoRetry.reset()
}
function allowed(item: DeskItem) { return person.value && decisionPermission(item, sources, can, adapters) }
const refreshFailures = computed(() => Math.max(desk.failures, outageFailures.value, hydrationFailures.value))
const refreshHint = computed(() => {
  const since = [desk.failedSince, outageSince.value, hydrationFailedSince.value].filter((at): at is number => at !== null)
  return deskRefreshNotice(refreshFailures.value, since.length ? Math.min(...since) : null, lastGoodAt.value, now.value)
})
// AEON-1700 (Tick UI spec v1): one sync line for the whole Desk: the last good read, a read or a live change under
// way, failures, the network, and the next read from the real timers (this view's poll, the header's, a retry).
const VIEW_POLL_MS = 30_000
const viewPoll = Symbol('desk-view-poll')
const pageShown = ref(document.visibilityState !== 'hidden'), networkUp = ref(navigator.onLine !== false)
function readPage() { pageShown.value = document.visibilityState !== 'hidden'; networkUp.value = navigator.onLine !== false }
// "Checking now…" holds for 400 ms, so a fast read never flickers; a hydration that follows its projection
// read straight away continues the same activity, and a live change keeps its words until its read is done.
const CHECKING_MIN_MS = 400
const activity = ref<{ since: number; change: boolean } | null>(null)
let activityTimer: ReturnType<typeof setTimeout> | undefined
watch(() => [desk.loading || loading.value || desk.changeArrived, desk.changeArrived] as const, ([busy, change]) => {
  if (busy) {
    clearTimeout(activityTimer); activityTimer = undefined
    activity.value = { since: activity.value?.since ?? Date.now(), change: !!activity.value?.change || change }
    return
  }
  if (!activity.value || activityTimer) return
  activityTimer = setTimeout(() => { activityTimer = undefined; activity.value = null }, Math.max(0, activity.value.since + CHECKING_MIN_MS - Date.now()))
}, { immediate: true })
const sync = computed(() => {
  const line = deskSyncLine({
    now: now.value, checkedAt: lastGoodAt.value, busy: !!activity.value, change: !!activity.value?.change,
    failures: refreshFailures.value, notice: refreshHint.value, stale: stale.value, retry: retryPhase.value,
    online: networkUp.value, visible: pageShown.value, live: desk.live, pollAt: desk.nextPollAt, retryAt: retryDue.value,
  }, displayLanguage())
  return { ...line, tip: [line.tip, elsewhere.syncTooltip(now.value)].filter(Boolean).join('\n') }
})
// The empty Open card holds the line under its message; everywhere else it sits in the tab row (§1).
const syncInCard = computed(() => clearFrame.value)
// §5: one polite region hears only failures turning quiet, escalation, recovery and new items; never a tick or a check.
const announcement = ref('')
let announcing: string[] = []
function announce(text: string) {
  announcing.push(text)
  if (announcing.length > 1) return
  announcement.value = ''
  void nextTick(() => { announcement.value = announcing.join(' '); announcing = [] })
}
watch(() => sync.value.health, (to, from) => {
  if (to === 'ok' && lastGoodAt.value === null) return
  const words = deskSyncAnnouncement(from, to, lastGoodAt.value === null ? '' : deskClock(lastGoodAt.value), displayLanguage())
  if (words) announce(words)
})
// Open items this visit has listed; a later good read names the ones it adds. The first read announces nothing.
let seenOpen: Set<string> | null = null
function noteArrivals() {
  const open = items.value.filter(item => !item.decided).map(item => item.id)
  const added = seenOpen ? open.filter(id => !seenOpen!.has(id)).length : 0
  seenOpen = new Set([...(seenOpen ?? []), ...open])
  if (added) announce(deskSyncCopy(displayLanguage()).announce.arrived(added))
}
async function refresh() {
  if (!session.authenticationCurrent()) return
  const read = await desk.refresh()
  if (read && alive && desk.projection) await hydrate(desk.projection)
}
// Header polls and live events share the projection read. Hydration also has
// one flight; an intervening projection gets exactly one trailing hydration.
// A read that began before this view recorded a decision cannot know it: applied
// after it, the read put a decided lesson back as open and its toast refused Undo
// (AEON-1622). Such a read is dropped; the refresh recorded() starts reads again.
let decisions = 0
async function hydrate(projection: DeskProjection): Promise<void> {
  const waitingTurn = generation, identity = owner.value
  if (hydration) {
    await hydration
    if (alive && waitingTurn === generation && identity === owner.value && desk.projection) return hydrate(desk.projection)
    return
  }
  if (projection === hydratedProjection || !session.authenticationCurrent()) return
  const turn = ++generation, seen = decisions
  const current = () => alive && turn === generation && seen === decisions && identity === owner.value && desk.projection === projection && session.authenticationCurrent()
  now.value = Date.now()
  loading.value = true
  const read = (async () => {
    try {
      const result = await loadDesk(pages.value, adapters, trimCursors.value, projection)
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
      if (!current()) return
      // AEON-1631: a read whose canonical desk did not answer never replaces good open rows from this visit.
      // They stay actionable (the server decides every write), with a quiet note naming their time. A missing
      // question or approval source still lists every canonical row, so that read applies with its own line.
      if (result.unanswered?.includes('desk') && currentRead && !currentRead.unanswered?.includes('desk') && items.value.some(item => !item.decided)) {
        stale.value = true
        return
      }
      applyRead(result); stale.value = false
      hydratedProjection = projection
      lastGoodAt.value = Date.now(); hydrationFailures.value = 0; hydrationFailedSince.value = null
      // This adopted read ends the outage; a projection read that failed after it still counts.
      outageFailures.value = desk.failures; outageSince.value = desk.failedSince
      noteArrivals()
      followLink()
      void readTickets(result.items.filter(item => !item.decided).slice(0, 300))
    } catch (cause) {
      if (current() && !deskReadAborted(cause)) {
        hydrationFailures.value++; hydrationFailedSince.value ??= Date.now()
        if (!currentRead) { warnings.value = ['The desk could not be read. Try refreshing.']; unanswered.value = ['desk'] }
      }
    } finally { if (alive && turn === generation) { state.value = 'ready'; loading.value = false } }
  })()
  hydration = read
  try { await read } finally { if (hydration === read) hydration = undefined }
}
watch(() => desk.projection, projection => { if (projection) void hydrate(projection) })
function applyRead(result: DeskRead) {
  const present = new Set(result.items.map(item => item.id))
  // A server refresh can omit an already committed request while its write
  // response is still in flight. Retain its old slot, then its resolved slot,
  // until this visit ends; appending it would move every following control.
  for (const [index, item] of items.value.entries()) {
    if (present.has(item.id) || !item.personStep || !(settledSteps.value.has(item.id) && item.decided || pendingSteps.value.has(item.id))) continue
    const successor = items.value.slice(index + 1).find(row => present.has(row.id))
    const at = successor ? result.items.findIndex(row => row.id === successor.id) : Math.min(index, result.items.length)
    result.items.splice(at, 0, item); present.add(item.id)
  }
  retainDecidedLessons(items.value, result.items, result.sources.lessons)
  currentRead = result
  sources = result.sources; items.value = result.items; warnings.value = result.warnings; unanswered.value = result.unanswered ?? []; more.value = result.hasMore
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
  followed = requested; linkError.value = ''; if (!item.personStep) begin(item.id)
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
// The toast's Undo outlives the view: a round that ends on a lesson returns to Agents or the preview
// (AEON-1396). The person and tenant the toast was shown to, the revision this view last read and the
// server's own conflict check guard it, never whether the view is still mounted.
async function undoLesson(item: DeskItem, identity = owner.value): Promise<DeskItem> {
  const id = item.id, revision = item.revision, eventId = item.undoEvent
  const latest = items.value.find(row => row.id === id)
  if (!eventId || undoingLessons.has(eventId) || identity !== owner.value || !session.authenticationCurrent() || latest?.revision !== revision || latest.undoEvent !== eventId || !allowed(latest)) throw new Error('This lesson decision is no longer available to undo.')
  undoingLessons.add(eventId)
  try {
    await undoKnowledge(eventId)
    const current = items.value.find(row => row.id === id)
    if (identity !== owner.value || !session.authenticationCurrent() || current?.revision !== revision || current.undoEvent !== eventId) throw new Error('The source changed while undoing. Reopen the Desk to confirm the result.')
    return { ...current, decided: false, optionId: undefined, answer: undefined, reason: undefined, delivery: undefined, undoEvent: undefined }
  } finally { undoingLessons.delete(eventId) }
}
// A lesson has no server delivery. Its Undo keeps the ten-second grace of an answer, counted from the
// decision, so a round that ends on a lesson ends like every other kind (AEON-1385).
const LESSON_GRACE_MS = 10_000
let pendingLessonToast: { item: DeskItem; at: number } | undefined
function showLessonToast(item: DeskItem, timeout = 8000) {
  const current = captureToastOwner(), identity = owner.value
  toast(item.delivery ?? 'Lesson decision recorded.', { timeout, action: { label: 'Undo', run: () => {
    if (!current()) return
    void undoLesson(item, identity).then(restored => {
      if (!current()) return
      // recorded() also refreshes the header's Desk; away from the Desk only a toast says it worked.
      recorded(restored)
      if (!alive) toast('Undone. The lesson waits on the Desk again.')
    }).catch(cause => { if (current()) toast(cause instanceof Error ? cause.message : 'Undo did not work.', { tone: 'error' }) })
  } } })
}
// The memo already offers Undo in its pinned native action slot. Its toast
// appears on closing, where it cannot cover the phone's decision controls.
watch(opened, open => {
  if (!open && pendingLessonToast) { const { item } = pendingLessonToast; pendingLessonToast = undefined; showLessonToast(item) }
})
function stepBusy(id: string, value: boolean) {
  const next = new Set(pendingSteps.value); if (value) next.add(id); else next.delete(id); pendingSteps.value = next
}
function stepSettled(item: DeskItem) {
  const next = new Set(settledSteps.value)
  if (item.decided) next.add(item.id); else next.delete(item.id)
  settledSteps.value = next; recorded(item)
}
function recorded(item: DeskItem) {
  decisions++
  if (item.kind === 'lesson') {
    if (!item.decided && pendingLessonToast?.item.id === item.id) pendingLessonToast = undefined
    if (item.decided && item.undoEvent && item.undoEvent !== items.value.find(row => row.id === item.id)?.undoEvent) {
      if (opened.value) pendingLessonToast = { item, at: Date.now() }
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
  settledSteps.value = new Set(); pendingSteps.value = new Set()
  capabilityGeneration++
  ruleEdit.value = undefined
  generation++; opened.value = false; items.value = []; roundItems.value = []; round.value = []; roundBaseline.value = []
  followed = ''; linkError.value = ''; revoked.value = new Set(); tickets.value = new Map(); runTickets.value = new Map(); runsReading.value = new Set(); ticketTurn++; rowForm.value = undefined; changeId.value = ''
  sources = emptySources(); adapters = makeAdapters(); filter.value = route.query.view === 'decided' ? 'decided' : 'open'; outcomeFilter.value = ''; pages.value = { open: 1, answered: 1 }
  projectNames.value = new Map()
  trimCursors.value = {}; nextKeyTrims.value = {}
  currentRead = undefined
  hydratedProjection = undefined; hydration = undefined
  lastGoodAt.value = null; hydrationFailures.value = 0; hydrationFailedSince.value = null; outageFailures.value = 0; outageSince.value = null
  phoneVerification.value = undefined; capabilityError.value = ''; stepupMethod.value = 'Passkey or sign-in'
  warnings.value = []; unanswered.value = []; more.value = { open: false, answered: false }; loading.value = false
  stale.value = false; autoRetry.reset(); seenOpen = null
  state.value = 'loading'
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
// On a row too narrow for its label the arrow shows alone (AEON-1448); on a fine pointer its name then also comes as a tip.
const tabRow = ref<HTMLElement>(), oneByOne = ref<HTMLButtonElement>(), arrowTip = ref(false)
let finePointer: MediaQueryList | undefined
function readArrowTip() { const label = oneByOne.value?.querySelector('.label'); arrowTip.value = !!label && getComputedStyle(label).display === 'none' && !!finePointer?.matches }
watch(oneByOne, readArrowTip, { flush: 'post' })
let rowSize: ResizeObserver | undefined
onMounted(() => {
  finePointer = matchMedia('(pointer: fine)'); finePointer.addEventListener('change', readArrowTip)
  readArrowTip()
  rowSize = new ResizeObserver(readArrowTip); if (tabRow.value) rowSize.observe(tabRow.value)
})
onBeforeUnmount(() => { rowSize?.disconnect(); finePointer?.removeEventListener('change', readArrowTip) })
// How the round ended: after the last decision the desk returns to where it was opened (AEON-1339).
function closed(result?: DeskClose) {
  opened.value = false; ruleEdit.value = undefined; changeId.value = ''
  if (result?.skipped) toast('Skipped. It stays on the desk.')
  if (!result?.afterLast) return
  // The toast keeps Undo for the rest of the grace, read on a fresh clock for every kind.
  const last = result.last; now.value = Date.now()
  if (last?.kind === 'lesson') {
    const pending = pendingLessonToast?.item.id === last.id ? pendingLessonToast : undefined
    pendingLessonToast = undefined
    const left = pending ? Math.min(10, Math.max(0, Math.round((pending.at + LESSON_GRACE_MS - now.value) / 1000))) : 0
    if (pending && left > 1) showLessonToast(pending.item, (left - 1) * 1000)
  } else if (last && graceLeft(last) > 1) {
    const name = who(last)?.name ?? 'the agent', id = last.id
    toast(`Decided. It goes to ${name} in a few seconds.`, { timeout: (graceLeft(last) - 1) * 1000, action: { label: 'Undo', run: () => undoAfterClose(id) } })
  }
  const from = route.query.from
  if ((from === 'preview' || from === 'agents') && typeof window.history.state?.back === 'string') {
    const target = from === 'preview' ? '[data-place="desk"] .place-main' : '#agents-desk-title'
    const stop = router.afterEach(() => { stop(); void nextTick(() => requestAnimationFrame(() => document.querySelector<HTMLElement>(target)?.focus({ preventScroll: true }))) })
    router.back()
    return
  }
  // A round action that is fading out (nothing left to answer) is no place for focus (AEON-1448).
  void nextTick(() => { if (document.activeElement === document.body || !document.activeElement) ((openRound.value.length ? oneByOne.value : undefined) ?? document.getElementById('desk-title'))?.focus({ preventScroll: true }) })
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
onMounted(() => {
  window.addEventListener('focus', focus)
  // A frozen capture clock leaves Date.now() unchanged, so this poll must not start "Checking now…". Focus still reads.
  let viewPollAt = Date.now()
  poll = setInterval(() => {
    const at = Date.now()
    desk.notePoll(viewPoll, at + VIEW_POLL_MS)
    if (!deskClockMoved(viewPollAt, at)) return
    viewPollAt = at
    if (!document.hidden && !loading.value) void refresh()
  }, VIEW_POLL_MS)
  desk.notePoll(viewPoll, Date.now() + VIEW_POLL_MS)
  clockTimer = setInterval(() => { now.value = Date.now() }, 1000)
  document.addEventListener('visibilitychange', readPage); window.addEventListener('online', readPage); window.addEventListener('offline', readPage)
})
onBeforeUnmount(() => {
  alive = false; generation++; ticketTurn++; autoRetry.stop(); stopAccess(); clearInterval(poll); clearInterval(clockTimer); clearTimeout(activityTimer); window.removeEventListener('focus', focus)
  desk.notePoll(viewPoll, null)
  document.removeEventListener('visibilitychange', readPage); window.removeEventListener('online', readPage); window.removeEventListener('offline', readPage)
})
</script>

<template>
  <main class="desk-page decision-desk" :class="{ deciding: noticeDeciding }" :aria-busy="noticeDeciding || undefined" aria-labelledby="desk-title">
    <header class="dp-head">
      <div class="desk-heading"><h1 id="desk-title" tabindex="-1">Decision Desk</h1>
        <div class="head-note desk-notice" data-testid="desk-notice">
          <!-- Once shown, the offer keeps its wrapping geometry through dismissal, success and later reads. -->
          <div v-if="noticeHeld" class="notice-offer" :class="{ reserved: !offerShown }" :aria-hidden="!offerShown || undefined" :inert="!offerShown || undefined">
            <!-- The offer is the longer sentence and sizes the shared slot, so a shorter failure moves nothing. -->
            <span class="notice-sentence">
              <span class="notice-size" aria-hidden="true">{{ noticeWords.offer }}</span>
              <span class="notice-size" aria-hidden="true">{{ noticeWords.failed }}</span>
              <span aria-live="polite" data-testid="desk-notice-sentence">{{ noticeError ? noticeWords.failed : noticeWords.offer }}</span>
            </span>
            <div class="notice-actions">
              <button type="button" class="linkish notice-on" :disabled="noticeBusy" @click="turnNoticeOn">{{ noticeText('Turn on', 'Einschalten') }}</button>
              <button type="button" class="linkish notice-later" :disabled="noticeBusy" @click="dismissNotice">{{ noticeText('Not now', 'Nicht jetzt') }}</button>
            </div>
          </div>
          <p v-if="noticeOn" role="status">{{ noticeText('Notifications on for this browser', 'Benachrichtigungen für diesen Browser an') }}</p>
          <p v-else-if="!offerShown">{{ noticeText('Agents keep working unless something is held up.', 'Agenten arbeiten weiter, solange nichts aufgehalten wird.') }}</p>
        </div>
      </div>
    </header>
    <!-- AEON-1448 (Tick): "Answer one by one" sits at the tab row's end, only while the (scoped) open round has items.
         It is never taller than the tabs, so arriving or leaving fades it in place and moves nothing else. -->
    <div ref="tabRow" class="dp-tabrow">
      <div class="dp-bar">
        <div class="seg view-tabs" role="radiogroup" aria-label="Show">
          <button type="button" role="radio" :aria-checked="filter === 'open'" :data-tip="openCountTip" data-testid="desk-tab-open" @click="filter = 'open'">Open<span class="n">{{ openCountLabel }}</span></button>
          <button type="button" role="radio" :aria-checked="filter === 'decided'" data-testid="desk-tab-decided" @click="filter = 'decided'">Decided<span class="n">{{ decidedCountLabel }}</span></button>
        </div>
        <!-- AEON-1700: the sync line takes the row's free end on a wide row and its own line under the tabs on a narrow one.
             On the empty Open card it moves into the card instead; there is only ever one. -->
        <div v-if="!syncInCard" class="sync-slot" :class="{ 'over-filter': filter !== 'decided' }" data-testid="desk-sync-slot"><DeskSyncLine :line="sync" :pulse="desk.liveEvents" :busy="loading || desk.loading" @retry="tryAgain" /></div>
        <!-- Wide rows retain the inline filter's box; narrow rows only take a filter line on Decided (AEON-1616). -->
        <div class="seg dec-filter" :class="{ reserved: filter !== 'decided' }" role="radiogroup" aria-label="Stamp" :aria-hidden="filter !== 'decided' || undefined" :inert="filter !== 'decided' || undefined">
          <button v-for="option in ([['', 'All'], ['once', 'Once'], ['always', 'Always'], ['requirement', 'Requirement'], ['doctrine', 'Doctrine'], ['approvals', 'Approvals']] as const)" :key="option[0]" type="button" role="radio" :aria-checked="outcomeFilter === option[0]" @click="outcomeFilter = option[0]">{{ option[1] }}</button>
        </div>
        <Transition name="one-by-one">
          <button v-if="openRound.length" ref="oneByOne" type="button" class="btn one-by-one" aria-label="Answer one by one" :data-tip="arrowTip ? 'Answer one by one' : undefined" data-testid="desk-one-by-one" @click="beginOpen"><AppIcon name="arrow" :size="14" /><span class="label">Answer one by one</span></button>
        </Transition>
      </div>
    </div>
    <p v-if="scopeActive && !scopeInCard" class="scope-line" data-testid="desk-scope">{{ scopeCopy }} <RouterLink :to="showAllTo">Show all</RouterLink></p>
    <nav v-if="nextKeyTrims[trimState] || trimCursors[trimState]" class="trim-pages" aria-label="Key trim pages"><button type="button" class="btn sm ghost" :disabled="loading || !trimCursors[trimState]" @click="pageKeyTrims(true)">First key trims</button><button type="button" class="btn sm ghost" :disabled="loading || !nextKeyTrims[trimState]" @click="pageKeyTrims()">Next key trims</button><span>Up to 100 key trims per page</span></nav>
    <section class="queue glass-card list-only" :class="{ clear: filter !== 'open' || !visible.length }" :aria-label="filter === 'open' ? 'Open' : 'Decided'" :aria-busy="loading || desk.loading" data-testid="desk-list">
      <!-- AEON-1406 (Tick's approved design): D reading, A all clear and B a silent source share this frame;
           B's line has its slot in A and D, so none of them moves the card. -->
      <!-- AEON-1631 (Tick): a desk that did not answer is never the all-clear art. The note under the sentence swaps in
           a slot sized to the longer text, and Try again stays where it is through every retry. -->
      <div v-if="failedPanel" class="desk-failed" :data-retry="retryPhase" data-testid="desk-failed">
        <AppIcon name="cloud-off" :size="18" />
        <div class="failed-text" role="status">
          <p class="failed-line">{{ failedWords.sentence }}</p>
          <p class="failed-note"><span :class="{ off: retryPhase === 'exhausted' }" :aria-hidden="retryPhase === 'exhausted' || undefined">{{ failedWords.retrying }}</span><span :class="{ off: retryPhase !== 'exhausted' }" :aria-hidden="retryPhase !== 'exhausted' || undefined">{{ failedWords.exhausted }}</span></p>
        </div>
        <button type="button" class="btn sm failed-retry" :disabled="loading || desk.loading" @click="tryAgain">{{ failedWords.retry }}</button>
      </div>
      <div v-else-if="clearFrame" class="all-clear" :class="{ reading: emptyState === 'reading' }" :data-state="emptyState" data-testid="desk-clear">
        <span class="art" aria-hidden="true"><template v-if="emptyState !== 'reading'"><img class="light" :src="deskLight" alt="" width="840" height="560" decoding="async" loading="lazy"><img class="dark" :src="deskDark" alt="" width="840" height="560" decoding="async" loading="lazy"></template></span>
        <p class="line" :data-testid="emptyState === 'reading' ? undefined : 'desk-empty'"><template v-if="emptyState !== 'reading'"><AppIcon name="check" :size="16" />{{ desk.allClear ? 'Nothing waits for you' : 'Nothing waits here · other instances are shown below' }}</template></p>
        <DeskSyncLine v-if="syncInCard" class="card-sync" :line="sync" :pulse="desk.liveEvents" :busy="loading || desk.loading" @retry="tryAgain" />
        <p class="quiet" role="status" data-testid="desk-unanswered"><template v-if="emptyState === 'incomplete'"><AppIcon name="clock" :size="14" /><span>{{ unansweredLine }}</span><span class="sep" aria-hidden="true">·</span><button type="button" class="linkish" :disabled="loading" @click="refresh">Try again</button></template></p>
        <p v-if="emptyState === 'reading'" class="reading-note" role="status">Reading the desk…</p>
      </div>
      <p v-else-if="reading" class="empty-line" role="status"><AppIcon name="clock" :size="16" /><span>Reading the desk…</span></p>
      <template v-else-if="filter === 'open'">
        <p v-if="!visible.length" class="empty-line" data-testid="desk-empty"><AppIcon name="check" :size="16" /><span>{{ emptyCopy }}<template v-if="scopeInCard"> · <RouterLink class="linkish" :to="showAllTo">Show all</RouterLink></template></span></p>
        <div v-for="group in [{ key: 'held', label: 'Waiting on you', note: 'something is held up until you answer', rows: heldRows }, { key: 'ready', label: 'When you’re ready', note: 'the agents carry on meanwhile', rows: readyRows }].filter(group => group.rows.length)" :key="group.key" class="q-group" :class="group.key">
          <p class="q-group-label">{{ group.label }}<span class="n">{{ groupOpenCount(group.rows) }}</span><span class="group-note">{{ group.note }}</span></p>
          <ul class="items desk-list" :aria-label="group.label">
            <template v-for="item in group.rows" :key="item.id">
              <li v-if="item.personStep"><PersonStepRequest :item="item" @settled="stepSettled" @busy="value => stepBusy(item.id, value)" /></li>
              <li v-else class="item desk-row" :class="{ blocks: item.held && (item.kind === 'question' || item.kind === 'handover'), high: !!item.approval && riskFor(item.approval) === 'high', open: rowForm?.id === item.id }" :data-testid="`desk-row-${item.id}`" @click="rowClick($event, item)">
              <component :is="item.linkOut ? RouterLink : 'button'" v-bind="item.linkOut ? { to: item.linkOut } : { type: 'button' }" class="row-open" :aria-label="rowName(item)" @click="item.linkOut || begin(item.id)" />
              <span class="mark" :class="`k-${item.kind}`" aria-hidden="true"><AppIcon :name="item.definitionChange ? 'history' : KIND_ICON[item.kind]" :size="16" /></span>
              <div class="body">
                <p class="line1"><strong class="what"><span v-if="item.kind === 'lesson'">Lesson to adopt · </span><template v-if="item.definitionChange"><span class="definition-project-key">{{ item.definitionChange.project_key }}</span>{{ titleOf(item).slice(item.definitionChange.project_key.length) }}</template><template v-else>{{ titleOf(item) }}</template></strong><span class="meta"><time v-if="item.expiresAt" class="expiry" :class="{ soon: soon(item.expiresAt) }" :datetime="item.expiresAt">{{ expiry(item.expiresAt) }}</time><time v-else-if="item.createdAt" class="expiry" :datetime="item.createdAt">{{ ago(item.createdAt) }}</time><span v-if="item.approval" class="risk" :class="{ high: riskFor(item.approval) === 'high' }">{{ RISK_LABEL[riskFor(item.approval)] }}</span></span></p>
                <p class="line2"><span v-if="who(item)" class="who" :data-tip="who(item)?.model || undefined"><HarnessMark v-if="who(item)?.harness" :harness="who(item)!.harness!" :size="14" /><span>{{ who(item)!.name }}</span></span><span class="phrase"><span v-if="item.kind === 'rule'" class="asks">{{ who(item) ? 'proposes' : 'An agent proposes' }} a rule change for</span><template v-else-if="item.kind === 'action'"><span class="asks">{{ who(item) ? 'to' : 'From an agent to' }}</span><span class="to">you</span><span class="asks">on</span></template><span v-else-if="who(item)" class="asks">on</span><span class="proj-quiet" data-testid="desk-row-project"><AppIcon name="folder" :size="13" />{{ item.projectName }}</span><template v-if="ticketOf(item)"><span class="res-key">{{ ticketOf(item)!.key }}</span><span v-if="ticketOf(item)!.title" class="res-title" :data-tip="ticketOf(item)!.title">{{ ticketOf(item)!.title }}</span></template><span v-if="item.runId" class="run-ref" :data-tip="`Run ${item.runId}`" data-testid="desk-row-run">{{ runRef(item.runId) }}</span></span></p>
                <p v-if="item.approval?.resource_kind === 'tenant'" class="target">The whole workspace</p>
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
                <RouterLink v-if="item.linkOut" class="btn sm approve-soft" :to="item.linkOut">{{ item.definitionChange ? definitionLanguage === 'de' ? 'Öffnen' : 'Open' : 'Open in Settings' }}</RouterLink>
                <template v-else-if="item.kind === 'approval' && allowed(item) && !item.unavailable"><button type="button" class="btn sm ghost" @click="openRowForm(item, 'deny')"><AppIcon name="close" :size="13" />Deny</button><button type="button" class="btn sm approve-soft" @click="openRowForm(item, 'approve')"><AppIcon name="check" :size="13" />Approve</button></template>
                <template v-else-if="item.kind === 'action' && allowed(item) && !item.unavailable"><button type="button" class="btn sm ghost answer" @click="begin(item.id)"><AppIcon name="send" :size="13" />Answer…</button><button type="button" class="btn sm ghost" @click="openRowForm(item, 'dismiss')"><AppIcon name="close" :size="13" />Dismiss</button><button type="button" class="btn sm approve-soft" @click="openRowForm(item, 'resolve')"><AppIcon name="check" :size="13" />Resolve</button></template>
                <button v-else type="button" class="btn sm approve-soft" @click="begin(item.id)">{{ item.kind === 'question' || item.kind === 'handover' ? 'Answer…' : 'Review…' }}</button>
              </div>
            </li>
            </template>
          </ul>
        </div>
      </template>
      <template v-else>
        <p v-if="!visible.length" class="empty-line" data-testid="desk-empty"><AppIcon name="check" :size="16" /><span>{{ outcomeFilter ? 'Nothing decided with this stamp yet' : 'No decisions recorded yet' }}</span></p>
        <ul v-else class="dec-list desk-list" aria-label="Decided">
          <template v-for="item in visible" :key="item.id">
              <li v-if="item.personStep"><PersonStepRequest :item="item" @settled="stepSettled" @busy="value => stepBusy(item.id, value)" /></li>
              <li v-else class="dec desk-row" :data-testid="`desk-row-${item.id}`">
            <component :is="item.linkOut ? RouterLink : 'button'" v-bind="item.linkOut ? { to: item.linkOut } : { type: 'button' }" class="row-open" :aria-label="`${kindLabels[item.kind]}: ${titleOf(item)}`" @click="item.linkOut || begin(item.id)" />
            <span class="mark" :class="outcomeOf(item).tone" aria-hidden="true"><AppIcon :name="outcomeOf(item).icon" :size="15" /></span>
            <span class="dec-text"><span class="dec-q">{{ item.title }}</span><span v-if="item.answer" class="dec-a">{{ item.answer }}</span><span class="dec-meta"><template v-if="who(item)"><span>{{ who(item)!.name }}</span><span>·</span></template><span class="proj-quiet"><AppIcon name="folder" :size="13" />{{ item.projectName }}</span><template v-if="ticketOf(item)"><span>·</span><span>{{ ticketOf(item)!.key }}</span></template><template v-if="item.runId"><span>·</span><span class="run-ref" :data-tip="`Run ${item.runId}`" data-testid="desk-row-run">{{ runRef(item.runId) }}</span></template><template v-if="item.decidedAt || item.createdAt"><span>·</span><span>decided {{ ago(item.decidedAt || item.createdAt) }}</span></template><template v-if="graceLeft(item)"><span>·</span><span>sends in <span class="n">{{ graceLeft(item) }}</span> s</span></template><template v-else-if="item.deliveryState === 'failed'"><span>·</span><span class="dl-bad">not delivered</span></template><template v-else-if="item.deliveryState"><span>·</span><span>waiting for it to read</span></template></span></span>
            <span class="dec-side"><span class="dec-out" :class="outcomeOf(item).tone"><AppIcon :name="outcomeOf(item).icon" :size="13" />{{ outcomeOf(item).word }}</span><button v-if="graceLeft(item)" type="button" class="btn sm ghost" @click="undoRow(item)"><AppIcon name="rollback" :size="13" />Undo</button></span>
          </li>
            </template>
        </ul>
      </template>
      <div v-if="listedLines.length || more[filter === 'open' ? 'open' : 'answered']" class="source-warnings" role="status">
        <p v-for="line in listedLines" :key="line.text" class="src-error"><AppIcon name="alert" :size="14" /><span>{{ line.text }}</span><template v-if="line.retry"><span class="sep">·</span><button type="button" class="linkish" :disabled="loading" @click="refresh">Try again</button></template></p>
        <p v-if="more[filter === 'open' ? 'open' : 'answered']" class="src-error"><AppIcon name="list" :size="14" /><span>More questions are available</span><span class="sep">·</span><button v-if="pages[filter === 'open' ? 'open' : 'answered'] < MAX_QUESTION_PAGES" type="button" class="linkish" :disabled="loading" @click="loadMore">Load 100 more</button><span v-else>the view shows up to 1,000 questions per state</span></p>
      </div>
    </section>
    <p class="sr-only" aria-live="polite" data-testid="desk-sync-announce">{{ announcement }}</p>
    <p v-if="opened && fresh.length" class="arrival-line" role="status">{{ fresh.length }} new item{{ fresh.length === 1 ? '' : 's' }} waiting for the next round.</p>
    <DecisionDeskMemo v-if="opened" :key="`${owner}:${memoRound}`" :items="roundItems" :round="round" :start="start" :arrivals-count="fresh.length" :pending-rule-ids="items.filter(row => row.kind === 'rule' && !row.decided).map(row => row.id.slice(2))" :allowed="allowed" :decide="decide" :stepup-method="stepupMethod" :can-revoke="canRevoke" :revoke="revoke" :edit-rule="editRule" :undo="undoLesson" :who="who" :ticket="ticketOf" :run-pending="runPending" :change="changeId || undefined" @recorded="recorded" @close="closed" />
    <DoctrineProposalDialog v-if="ruleEdit" :key="owner" :source="ruleEdit.source" :file="ruleEdit.file" :rule="ruleEdit.rule" :draft="ruleEdit.draft" @close="ruleEdit = undefined" @saved="editedRule" />
    <div v-if="filter === 'open'" class="desk-instances">
      <DeskInstanceItems v-for="read in elsewhere.reads" :key="read.tag" :read="read" :variant="read.state === 'not connected' ? 'goals' : 'desk'" />
      <p v-if="elsewhere.discovery === 'unknown'">Other instances could not be read. <button type="button" class="linkish" @click="elsewhere.refresh(true)">Try again</button></p>
    </div>
    <div v-else class="desk-instance-history">
      <a v-for="read in elsewhere.reads.filter(row => ['ok', 'reading', 'unreachable'].includes(row.state))" :key="read.tag" :href="`${read.origin}/decision-desk?view=decided`" target="_blank" rel="noopener noreferrer">Decisions made on {{ read.tag }} are listed there<AppIcon name="external" :size="13" /></a>
    </div>
  </main>
</template>

<style scoped>
.definition-project-key { white-space: nowrap; overflow-wrap: normal; }
.desk-instance-history { display: grid; }
.desk-instance-history a { display: inline-flex; align-items: center; gap: 6px; min-height: 44px; color: var(--ink-3); font-size: 13px; text-decoration: none; }
/* AEON-1339: the Desk page as the approved rev 11 artifact draws it. */
.scope-line { display: flex; align-items: center; flex-wrap: wrap; column-gap: 16px; min-height: 44px; margin: 0; font-size: 12px; color: var(--ink-3); }.scope-line a { color: var(--teal-ink); text-decoration: none; display: inline-flex; align-items: center; min-height: 44px; }.scope-line a:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }
.desk-page { width: 100%; max-width: 1060px; margin: 0 auto; padding: 34px max(16px, min(4vw, 40px)) 72px; color: var(--ink); }
/* This root <main> also takes App's scoped `main` rule. Its stable gutter would reserve a second, empty scrollbar
   strip inside #main's own on classic-scrollbar desktops. That narrows the tab row and moves the page off centre. */
.desk-page { scrollbar-gutter: auto; }
.dp-head { display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: 14px 24px; margin-bottom: 18px; }
.dp-head h1 { margin: 0; font: 300 clamp(28px, 3.4vw, 36px)/1.1 var(--serif); letter-spacing: -.025em; outline: none; }
.dp-head .head-note { margin: 8px 0 0; font-size: 13px; color: var(--ink-3); }
.desk-heading { width: 100%; min-width: 0; }
.desk-notice { display: grid; align-items: center; }
.desk-notice > * { grid-area: 1 / 1; margin: 0; }
.notice-offer { display: flex; align-items: center; flex-wrap: wrap; gap: 0 16px; }
.notice-sentence { display: grid; min-width: 0; }
.notice-sentence > span { grid-area: 1 / 1; overflow-wrap: anywhere; }
.notice-sentence .notice-size { visibility: hidden; }
.notice-actions { display: flex; flex-wrap: wrap; gap: 0 16px; }
/* Quiet actions: the way on in link ink, the way out muted, both in the sentence's own weight. */
.notice-actions .linkish { min-width: 44px; min-height: 44px; padding: 0; font-weight: 400; }
.notice-actions .notice-on { color: var(--primary-ink); }
.notice-actions .notice-later { color: var(--ink-3); }
.desk-heading .reserved { visibility: hidden; pointer-events: none; }
/* Until the offer is decided only the title shows; the rest appears once, in its final place. */
.deciding .desk-notice, .deciding .dp-tabrow, .deciding .scope-line, .deciding .trim-pages, .deciding .queue, .deciding .arrival-line { visibility: hidden; }
/* AEON-1448 (Tick): one tab row at every width: the tabs at its start, "Answer one by one" at its end. On a narrow row
   the Decided stamp filter takes a line of its own; the button keeps the first, so it never wraps or adds height. */
.dp-tabrow { --desk-list-gap: 14px; container: desk-tabs / inline-size; }
.dp-bar { --desk-row-gap: 10px; display: grid; grid-template-columns: max-content minmax(0, max-content) minmax(0, 1fr) max-content; align-items: center; gap: var(--desk-row-gap) 14px; margin-bottom: var(--desk-list-gap); }
.dp-bar .view-tabs { grid-area: 1 / 1; }
.dp-bar .view-tabs button { white-space: nowrap; }
/* AEON-1700 (Tick UI spec v1 §1): on a wide row the sync line takes the row's free end, right-aligned before the
   round action. Its slot is the row's own height and adds none; centred while it fits, a line that needs more room
   grows upward into the header's empty right side, never down onto the list (the AEON-1477 slot). */
.sync-slot { grid-area: 1 / 3; align-self: stretch; position: relative; min-width: 0; }
/* Open keeps the stamp filter's box but hides it (AEON-1616), so the line may use that space too; its right edge stays. */
.sync-slot.over-filter { grid-column: 2 / 4; }
.sync-slot > .desk-sync { position: absolute; right: 0; bottom: 0; min-height: 100%; max-width: 100%; justify-content: flex-end; text-align: right; }
/* The empty Open card holds it centred, 8 px under "Nothing waits for you" (the card's own gap is 10 px). */
.card-sync { justify-content: center; margin-top: -2px; text-align: center; }
/* 12.5 px is the tabs' type (a plain .btn is 13 px), so the label fits beside them on a 390 px phone. */
.one-by-one { grid-area: 1 / -2; justify-self: end; gap: 6px; padding: 0 12px; font-size: 12.5px; }
/* Coarse pointers: 44 px tall, and 44 × 44 as the arrow alone. */
@media (pointer: coarse) { .one-by-one { height: 44px; min-width: 44px; } }
@media (prefers-reduced-motion: no-preference) { .one-by-one-enter-active, .one-by-one-leave-active { transition: opacity 120ms ease; } }
.one-by-one-enter-from, .one-by-one-leave-to { opacity: 0; }
.dp-bar .seg button .n { font: 500 11.5px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.dp-bar .seg button[aria-checked="true"] .n { color: var(--teal-ink); }
/* The stamp filter stays one line beside the tabs; on a phone it scrolls sideways instead of wrapping (AEON-1339). */
.dp-bar .dec-filter { grid-area: 1 / 2; justify-self: start; flex-wrap: nowrap; min-width: 0; max-width: 100%; overflow-x: auto; overscroll-behavior-x: contain; scrollbar-width: none; }
.dp-bar .dec-filter.reserved { visibility: hidden; }
.dp-bar .dec-filter::-webkit-scrollbar { display: none; }
.dp-bar .dec-filter button { flex: none; white-space: nowrap; }
/* A 960 px window with a classic gutter wraps; 1024 keeps one row, where the filter and the hint fit.
   Below it only Decided needs a second line. Open goes straight from the tabs to its list or empty state. */
@container desk-tabs (width < 910px) {
  .dp-bar { grid-template-columns: max-content minmax(0, 1fr); }
  .dp-bar .dec-filter { grid-area: 2 / 1 / auto / -1; }
  .dp-bar .dec-filter.reserved { display: none; }
  /* AEON-1700 §1: one line under the tab row (and under Decided's stamp filter), left-aligned: 8 px below the row
     above it, 14 px (--desk-list-gap) above the list. Auto-placed into the first free full-width row. */
  .sync-slot, .sync-slot.over-filter { grid-area: auto / 1 / auto / -1; align-self: start; position: static; margin-top: calc(8px - var(--desk-row-gap)); }
  .sync-slot > .desk-sync { position: static; min-height: 20px; justify-content: flex-start; text-align: left; }
}
/* Below about 420 px the view tabs and the round action give up a little padding so the label still clears the tabs.
   The stamp filter is unchanged: on a phone it keeps scrolling (AEON-1339). */
@container desk-tabs (max-width: 419.98px) {
  .dp-bar .view-tabs button { padding: 0 6px; gap: 4px; }
  .one-by-one { padding: 0 6px; gap: 4px; }
}
/* The row decides, not the viewport. Beside the tabs the label needs about 340 px of row with the widest sans the
   spec meets (CI's DejaVu Sans Bold); below a 352 px row the arrow shows alone and keeps its name. A 390 px phone
   (358 px row) shows the label; the same window with a classic 15 px scrollbar (343 px row) gets the arrow. */
@container desk-tabs (max-width: 351.98px) {
  .one-by-one { width: 32px; padding: 0; }
  .one-by-one .label { display: none; }
}
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
.item :is(.row-actions, form, .who[data-tip], .res-title, .run-ref, a, button:not(.row-open)), .dec :is(.dec-side, .run-ref) { pointer-events: auto; }
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
/* A run approval's run, quiet beside its ticket; the full id is in the tip (AEON-1339). */
.run-ref { font: 500 11.5px/1.4 var(--mono); font-variant-ligatures: none; color: var(--ink-3); white-space: nowrap; }
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
.empty-line svg { flex: none; color: var(--ok); }
/* AEON-1631 (Tick): a desk that did not answer, in the list's first-row slot: a muted icon, the sentence in ink-2 and
   Try again on its first line at every width, so a sentence that changes with the count never moves it. Never red. */
.desk-failed { display: grid; grid-template-columns: 18px minmax(0, 1fr) auto; gap: 12px; align-items: start; padding: 22px 22px 24px; }
.desk-failed > svg { margin-top: 2px; color: var(--ink-3); }
.failed-text { display: grid; gap: 2px; min-width: 0; }
.failed-line { margin: 0; font-size: 14px; line-height: 22px; color: var(--ink-2); text-wrap: pretty; overflow-wrap: anywhere; }
.failed-note { display: grid; margin: 0; font-size: 12.5px; line-height: 20px; color: var(--ink-3); }
.failed-note > span { grid-area: 1 / 1; text-wrap: pretty; }
.failed-note > .off { visibility: hidden; }
.failed-retry { margin-top: -3px; }
@media (pointer: coarse) { .failed-retry { height: 44px; margin-top: -11px; } }
@media (max-width: 560px) { .desk-failed { gap: 10px; padding: 18px 16px 20px; } }
/* AEON-1406: the calm empty desk. The picture sits above one line; a silent source adds a quiet line in its reserved slot. */
.all-clear { position: relative; display: grid; justify-items: center; gap: 10px; padding: 28px 22px 30px; text-align: center; }
.all-clear .art { display: block; width: min(100%, 340px); aspect-ratio: 3 / 2; }
.all-clear .art img { display: block; width: 100%; height: 100%; object-fit: contain; }
/* One picture per theme; the other is never shown (and, lazy and undisplayed, never fetched). */
.all-clear .art .dark { display: none; }
:root[data-theme="dark"] .all-clear .art .light { display: none; }
:root[data-theme="dark"] .all-clear .art .dark { display: block; }
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) .all-clear .art .light { display: none; }
  :root:not([data-theme="light"]) .all-clear .art .dark { display: block; }
}
.all-clear .line { display: inline-flex; align-items: center; gap: 8px; min-height: 22px; margin: 0; font-size: 15px; line-height: 22px; font-weight: 600; color: var(--ink); }
.all-clear .line svg { flex: none; color: var(--ok); }
/* Inline flow, so a narrow card wraps the sentence and keeps Try again with it. */
.all-clear .quiet { justify-self: stretch; min-height: 20px; margin: 0; font-size: 12.5px; line-height: 20px; color: var(--ink-3); text-wrap: balance; }
/* Base svg is a block; inline here, so the icon flows with its sentence. */
.all-clear .quiet svg { display: inline-block; vertical-align: -2px; margin-right: 6px; }
.all-clear .quiet .sep { margin: 0 6px; opacity: .6; }
.all-clear .linkish, .empty-line .linkish { font-weight: inherit; text-decoration: underline; text-underline-offset: 2px; }
.all-clear.reading > :not(.reading-note) { visibility: hidden; }
.reading-note { position: absolute; inset: 0; display: grid; place-items: center; margin: 0; font-size: 13px; color: var(--ink-3); }
.arrival-line { margin: 12px 4px 0; font-size: 12px; color: var(--teal-ink); }
/* The quiet line takes two lines in a narrow card, so its slot does too. */
@container queue (max-width: 640px) { .all-clear .quiet { min-height: 40px; } }
@container queue (max-width: 760px) {
  .item { grid-template-columns: 30px minmax(0, 1fr); }
  .row-actions { grid-column: 2; justify-self: start; }
  .dec { grid-template-columns: 30px minmax(0, 1fr); }
  .dec-side { grid-column: 2; justify-items: start; }
}
@media (max-width: 720px) {
  .desk-page { padding-top: 24px; }
  .item { padding: 12px 10px; }
  .row-actions .btn { height: 40px; padding: 0 12px; }
  .decision-actions .hint { display: none; }
  .decision-actions .btn { height: 40px; }
  .res-title { max-width: 100%; }
  .q-group-label { padding: 0 14px; }
}
@media (max-width: 560px) { .all-clear { padding: 20px 16px 24px; } .all-clear .art { width: min(100%, 250px); } }
@media (pointer: coarse) { .seg button { height: 40px; min-width: 44px; } .dp-bar { --desk-row-gap: 14px; } .linkish { position: relative; } .linkish::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: max(100%, 44px); transform: translate(-50%, -50%); } }
</style>
