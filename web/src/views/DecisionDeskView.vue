<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import DecisionDeskMemo from '../components/agents/DecisionDeskMemo.vue'
import DoctrineProposalDialog from '../components/rules/DoctrineProposalDialog.vue'
import { getDoctrine, type DoctrineSource, type DoctrineFile, type DoctrineRule, type DoctrineInboxItem, type DoctrineProposal } from '../lib/doctrine'
import { arrivals, deskItemID, deskLinkItem, kindLabels, newRound, outcomeLabels, type DeskDraft, type DeskItem, type DeskOutcome } from '../lib/decisionDesk'
import { approvalItem, commitDesk, decisionPermission, emptySources, loadDesk, loadMoreQuestions, nativeTierAdapter, questionItem, readApproval, readQuestion, MAX_QUESTION_PAGES, type DeskRead, type DeskSources } from '../lib/decisionDeskApi'
import { phoneCapability } from '../lib/deskPhoneApproval'
import { settledElsewhere } from '../lib/stepup'
import { can, onAccessChange } from '../lib/authz'
import { useSession } from '../stores/session'
import { useAgents } from '../stores/agents'
import type { KeyTrimCursors } from '../lib/keyTrim'
import { useDecisionDesk } from '../stores/decisionDesk'
import { revokeApproval } from '../lib/agents'
import { doctrineInbox, inboxChanged } from '../lib/doctrineInbox'

const session = useSession()
const agents = useAgents()
const desk = useDecisionDesk(), route = useRoute()
const makeAdapters = () => ({ tiers: nativeTierAdapter(async () => { await agents.refreshSessions(); return agents.sessions }, can, async (project, id) => {
  await agents.loadSessionDetail(project, id)
  return agents.sessionById(id)
}) })
let adapters = makeAdapters()
const owner = computed(() => `${session.identity?.tenant.id ?? ''}:${session.identity?.principal.id ?? ''}`)
const items = ref<DeskItem[]>([]), roundItems = ref<DeskItem[]>([]), round = ref<string[]>([]), start = ref(''), opened = ref(false)
// Same-view links can close and reopen in one render; each new round must
// discard the prior memo's index, drafts and pending context reads.
const memoRound = ref(0)
const state = ref<'loading' | 'ready'>('loading'), warnings = ref<string[]>([]), more = ref({ open: false, answered: false }), loading = ref(false), pages = ref({ open: 1, answered: 1 }), filter = ref<'open' | 'decided'>('open'), outcomeFilter = ref<DeskOutcome | ''>('')
const roundBaseline = ref<string[]>([]), phoneVerification = ref<boolean | undefined>(), capabilityError = ref('')
// Names the step-up method on Approve; the server makes the actual choice.
const stepupMethod = ref('Passkey or sign-in')
const trimCursors = ref<KeyTrimCursors>({}), nextKeyTrims = ref<KeyTrimCursors>({})
const trimState = computed(() => filter.value === 'open' ? 'pending' : 'decided')
let sources: DeskSources = emptySources(), generation = 0, alive = true
let currentRead: DeskRead | undefined
const linkError = ref(''), revoked = ref(new Set<string>())
const ruleEdit = ref<{ item: DeskItem; draft: DoctrineInboxItem; source: DoctrineSource; file: DoctrineFile; rule: DoctrineRule; owner: string }>()
let followed = ''
const fresh = computed(() => arrivals(items.value, roundBaseline.value))
const visible = computed(() => items.value.filter(item => item.decided === (filter.value === 'decided') && (!outcomeFilter.value || item.outcome === outcomeFilter.value)))
// Link-out rows are decided on their settings page, so they never join a memo round.
const reviewable = computed(() => visible.value.filter(item => !item.linkOut))
const person = computed(() => session.identity?.principal.kind === 'person')
function allowed(item: DeskItem) { return person.value && decisionPermission(item, sources, can, adapters) }
async function refresh() {
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
    function includeLinked(item: DeskItem) {
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
  } catch { if (turn === generation) warnings.value = ['The desk could not be read. Try refreshing.'] }
  finally { if (alive && turn === generation) { state.value = 'ready'; loading.value = false } }
}
function applyRead(result: DeskRead) {
  currentRead = result
  sources = result.sources; items.value = result.items; warnings.value = result.warnings; more.value = result.hasMore
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
function recorded(item: DeskItem) {
  if (item.kind === 'rule' && item.decided && !items.value.find(row => row.id === item.id)?.decided) inboxChanged(doctrineInbox.pending - 1, item.id.slice(2))
  items.value = items.value.map(row => row.id === item.id ? item : row)
  if (currentRead) currentRead = { ...currentRead, items: items.value }
  roundItems.value = roundItems.value.map(row => row.id === item.id ? item : row)
  void desk.refresh()
}
watch(owner, () => {
  ruleEdit.value = undefined
  generation++; opened.value = false; items.value = []; roundItems.value = []; round.value = []; roundBaseline.value = []
  followed = ''; linkError.value = ''; revoked.value = new Set()
  sources = emptySources(); adapters = makeAdapters(); filter.value = route.query.view === 'decided' ? 'decided' : 'open'; outcomeFilter.value = ''; pages.value = { open: 1, answered: 1 }
  trimCursors.value = {}; nextKeyTrims.value = {}
  currentRead = undefined
  phoneVerification.value = undefined; capabilityError.value = ''; stepupMethod.value = 'Passkey or sign-in'
  const identity = owner.value
  void phoneCapability().then(capability => { if (alive && identity === owner.value) { phoneVerification.value = capability.available; stepupMethod.value = capability.passkeys ? 'Passkey' : 'Sign in again' } })
    .catch(() => { if (alive && identity === owner.value) capabilityError.value = 'Approval verification availability could not be confirmed. Refresh the page before deciding.' })
  void refresh()
}, { immediate: true, flush: 'sync' })
// A refresh signal arrives before the permission response. Only an actual
// reset invalidates the frozen source; can() reacts to the completed refresh.
const stopAccess = onAccessChange(change => { if (change === 'reset') { ruleEdit.value = undefined; if (opened.value) roundItems.value = roundItems.value.map(item => allowed(item) ? item : { ...item, unavailable: 'Access changed. Reopen the desk to confirm this source.' }) } })
let poll: ReturnType<typeof setInterval> | undefined
const focus = () => { if (!loading.value) void refresh() }
onMounted(() => { window.addEventListener('focus', focus); poll = setInterval(() => { if (!document.hidden && !loading.value) void refresh() }, 30_000) })
onBeforeUnmount(() => { alive = false; generation++; stopAccess(); clearInterval(poll); window.removeEventListener('focus', focus) })
</script>

<template>
  <main class="decision-desk" aria-labelledby="desk-title">
    <header class="desk-head"><div><p class="eyebrow">{{ session.identity?.tenant.name }}</p><h1 id="desk-title">Decision Desk</h1><p class="intro">One topic at a time. The work can wait while the answer takes shape.</p></div><div class="desk-links"><RouterLink to="/agents">Agents</RouterLink><button type="button" :disabled="loading" @click="refresh">Refresh</button></div></header>
    <div class="desk-summary"><div class="view-tabs" role="group" aria-label="Desk view"><button type="button" :aria-pressed="filter === 'open'" @click="filter = 'open'">Open <span>{{ desk.count ?? '?' }}</span></button><button type="button" :aria-pressed="filter === 'decided'" @click="filter = 'decided'">Decided <span>{{ items.filter(item => item.decided).length }}</span></button></div><label class="outcome-filter" :class="{ hidden: filter !== 'decided' }">Stamp <select v-model="outcomeFilter" :disabled="filter !== 'decided'" aria-label="Filter decided by stamp"><option value="">All</option><option v-for="(label, outcome) in outcomeLabels" :key="outcome" :value="outcome">{{ label }}</option></select></label><button type="button" class="review-button" :disabled="!reviewable.length || loading" @click="begin()">One at a time</button></div>
    <nav v-if="nextKeyTrims[trimState] || trimCursors[trimState]" class="trim-pages" aria-label="Key trim pages"><button type="button" :disabled="loading || !trimCursors[trimState]" @click="pageKeyTrims(true)">First key trims</button><button type="button" :disabled="loading || !nextKeyTrims[trimState]" @click="pageKeyTrims()">Next key trims</button><span>Up to 100 key trims per page</span></nav>
    <p v-if="state === 'loading'" role="status">Reading the desk…</p>
    <p v-if="state === 'ready' && !visible.length">{{ warnings.length ? 'The available sources have no matching items.' : filter === 'decided' ? 'No recorded decisions in this view.' : 'Nothing is waiting in the available sources.' }}</p>
    <ol class="desk-list" aria-label="Desk items"><li v-for="item in visible" :key="item.id"><component :is="item.linkOut ? RouterLink : 'button'" v-bind="item.linkOut ? { to: item.linkOut } : { type: 'button' }" class="desk-row" :data-testid="`desk-row-${item.id}`" @click="item.linkOut || begin(item.id)"><span class="row-kind">{{ kindLabels[item.kind] }}</span><span class="row-topic"><strong :data-tip="item.title">{{ item.title }}</strong><small :data-tip="item.decided ? item.answer : item.meanwhile">{{ item.projectName }} · {{ item.decided ? item.answer : item.meanwhile }}</small></span><span class="row-state">{{ item.decided ? outcomeLabels[item.outcome] : item.linkOut ? 'In Settings' : item.held ? 'Holding work' : 'Waiting' }}</span></component></li></ol>
    <p v-if="more[filter === 'open' ? 'open' : 'answered']" class="incomplete">More questions are available. <button v-if="pages[filter === 'open' ? 'open' : 'answered'] < MAX_QUESTION_PAGES" type="button" :disabled="loading" @click="loadMore">Load 100 more</button><span v-else>The view is limited to 1,000 questions per state.</span></p>
    <div v-if="warnings.length || linkError" class="source-warnings" role="status"><button type="button" :disabled="loading" @click="refresh">Retry</button><p v-if="linkError">{{ linkError }}</p><p v-for="warning in warnings" :key="warning">{{ warning }}</p></div>
    <p class="readiness">Handover-question import is awaiting a verified source contract. Answers to existing questions keep their verified successor route.</p>
    <p class="chore-line">Sign-ins and account setup remain in <RouterLink to="/agents">Agents</RouterLink>.</p>
    <p v-if="opened && fresh.length" class="arrival-line">{{ fresh.length }} new item{{ fresh.length === 1 ? '' : 's' }} waiting for the next round.</p>
    <DecisionDeskMemo v-if="opened" :key="`${owner}:${memoRound}`" :items="roundItems" :round="round" :start="start" :arrivals-count="fresh.length" :pending-rule-ids="items.filter(row => row.kind === 'rule' && !row.decided).map(row => row.id.slice(2))" :allowed="allowed" :decide="decide" :stepup-method="stepupMethod" :can-revoke="canRevoke" :revoke="revoke" :edit-rule="editRule" @recorded="recorded" @close="opened = false; ruleEdit = undefined" />
    <DoctrineProposalDialog v-if="ruleEdit" :key="owner" :source="ruleEdit.source" :file="ruleEdit.file" :rule="ruleEdit.rule" :draft="ruleEdit.draft" @close="ruleEdit = undefined" @saved="editedRule" />
  </main>
</template>

<style scoped>
.trim-pages { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; padding: 12px 0; font-size: 12px; color: var(--ink-3); }.trim-pages button { height: 32px; padding: 0 8px; border: 1px solid var(--line); border-radius: 4px; background: transparent; color: var(--ink); }
.decision-desk { width: min(1120px, 100%); margin: 0 auto; padding: 30px 32px; color: var(--ink); }.desk-head { display: flex; justify-content: space-between; gap: 24px; margin-bottom: 28px; }.eyebrow { font-size: 11px; color: var(--ink-3); margin: 0 0 7px; }h1 { font-size: 30px; font-weight: 550; margin: 0; letter-spacing: -.02em; }.intro { font-size: 13px; color: var(--ink-3); margin-top: 10px; }.desk-links { display: flex; align-items: flex-start; gap: 20px; padding-top: 10px; }.desk-links a, .chore-line a { color: var(--teal-ink); text-decoration: none; font-size: 12px; }button { cursor: pointer; }button:disabled { cursor: default; opacity: .5; }.desk-links button { min-inline-size: 7ch; border: 0; color: var(--ink-2); background: transparent; font-size: 12px; padding: 0; }.desk-summary { display: flex; align-items: center; gap: 18px; border-bottom: 1px solid var(--line-2); padding-bottom: 14px; }.view-tabs { display: flex; gap: 20px; }.view-tabs button { border: 0; background: transparent; color: var(--ink-3); font-size: 13px; font-weight: 600; padding: 8px 0; }.view-tabs button[aria-pressed=true] { color: var(--ink); }.view-tabs span { display: inline-block; min-inline-size: 3ch; font-size: 11px; margin-left: 6px; font-variant-numeric: tabular-nums; }.review-button { margin-left: auto; border: 1px solid var(--line-2); border-radius: 8px; background: var(--glass); padding: 10px 16px; color: var(--teal-ink); font-size: 12px; font-weight: 600; }.outcome-filter.hidden { visibility: hidden; }.outcome-filter { font-size: 12px; display: flex; gap: 8px; align-items: center; }.outcome-filter select { color: var(--ink); background: var(--surface); border: 1px solid var(--line); height: 32px; max-width: 130px; }.desk-list { margin: 0; padding: 0; list-style: none; }.desk-row { width: 100%; display: grid; grid-template-columns: 120px minmax(0,1fr) 100px; align-items: center; gap: 22px; padding: 20px 0; border: 0; border-bottom: 1px solid var(--line); background: transparent; text-align: left; color: var(--ink); }.desk-row:hover { background: var(--row-hover); }a.desk-row { text-decoration: none; }a.desk-row:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }.row-kind { font-size: 11px; color: var(--ink-3); }.row-topic { min-width: 0; }.row-topic strong { display: block; font-size: 14px; font-weight: 550; overflow: hidden; text-overflow: ellipsis; white-space: normal; overflow-wrap: anywhere; }.row-topic small { display: block; font-size: 12px; color: var(--ink-3); margin-top: 7px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.row-state { font-size: 11px; text-align: right; color: var(--ink-3); }.readiness, .chore-line, .incomplete { margin-top: 24px; font-size: 12px; color: var(--ink-3); }.incomplete button { border: 0; background: transparent; color: var(--primary-ink); font-size: inherit; }.source-warnings { font-size: 12px; color: var(--warn-ink); padding: 10px 0; }.source-warnings button { border: 1px solid var(--line); background: var(--glass); color: var(--ink); padding: 8px 14px; border-radius: 6px; }.source-warnings p { margin: 5px 0; }.arrival-line { font-size: 12px; color: var(--teal-ink); }button:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }
@media (max-width: 720px) { .decision-desk { padding: 22px 18px; }.desk-head { gap: 12px; }.desk-links { gap: 12px; }h1 { font-size: 25px; }.intro { max-width: 24ch; }.desk-summary { flex-wrap: wrap; gap: 12px; }.desk-row { grid-template-columns: minmax(0,1fr) 78px; gap: 10px; padding: 16px 0; }.row-kind { grid-column: 1 / -1; }.row-state { grid-column: 2; grid-row: 2; }.row-topic { grid-row: 2; }.review-button { padding: 10px; }.outcome-filter { order: 3; } }
</style>
