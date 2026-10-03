<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import DecisionDeskMemo from '../components/agents/DecisionDeskMemo.vue'
import { arrivals, kindLabels, newRound, outcomeLabels, type DeskDraft, type DeskItem, type DeskOutcome } from '../lib/decisionDesk'
import { commitDesk, decisionPermission, emptySources, loadDesk, loadMoreQuestions, nativeTierAdapter, MAX_QUESTION_PAGES, type DeskRead, type DeskSources } from '../lib/decisionDeskApi'
import { phoneVerificationAvailable } from '../lib/deskPhoneApproval'
import { can, onAccessChange } from '../lib/authz'
import { useSession } from '../stores/session'
import { useAgents } from '../stores/agents'

const session = useSession()
const agents = useAgents()
const makeAdapters = () => ({ tiers: nativeTierAdapter(async () => { await agents.refreshSessions(); return agents.sessions }, can) })
let adapters = makeAdapters()
const owner = computed(() => `${session.identity?.tenant.id ?? ''}:${session.identity?.principal.id ?? ''}`)
const items = ref<DeskItem[]>([]), roundItems = ref<DeskItem[]>([]), round = ref<string[]>([]), start = ref(''), opened = ref(false)
const state = ref<'loading' | 'ready'>('loading'), warnings = ref<string[]>([]), more = ref({ open: false, answered: false }), loading = ref(false), pages = ref({ open: 1, answered: 1 }), filter = ref<'open' | 'decided'>('open'), outcomeFilter = ref<DeskOutcome | ''>('')
const roundBaseline = ref<string[]>([]), phoneVerification = ref<boolean | undefined>(), capabilityError = ref('')
let sources: DeskSources = emptySources(), generation = 0, alive = true
let currentRead: DeskRead | undefined
const fresh = computed(() => arrivals(items.value, roundBaseline.value))
const visible = computed(() => items.value.filter(item => item.decided === (filter.value === 'decided') && (!outcomeFilter.value || item.outcome === outcomeFilter.value)))
const person = computed(() => session.identity?.principal.kind === 'person')
function allowed(item: DeskItem) { return person.value && decisionPermission(item, sources, can, adapters) }
async function refresh() {
  const turn = ++generation, identity = owner.value
  loading.value = true
  try {
    const result = await loadDesk(pages.value, adapters)
    if (!alive || turn !== generation || identity !== owner.value) return
    applyRead(result)
  } catch { if (turn === generation) warnings.value = ['The desk could not be read. Try refreshing.'] }
  finally { if (alive && turn === generation) { state.value = 'ready'; loading.value = false } }
}
function applyRead(result: DeskRead) {
  currentRead = result
  sources = result.sources; items.value = result.items; warnings.value = result.warnings; more.value = result.hasMore
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
function begin(id?: string) {
  roundBaseline.value = newRound(items.value)
  roundItems.value = [...visible.value]; round.value = newRound(roundItems.value)
  if (!round.value.length) return
  start.value = id && round.value.includes(id) ? id : round.value[0]!
  opened.value = true
}
async function decide(item: DeskItem, draft: DeskDraft, requestId: string) {
  const identity = owner.value
  if (!allowed(item) || item.unavailable) throw new Error('This action is no longer available to this person.')
  if (item.expiresAt && Date.parse(item.expiresAt) <= Date.now()) throw new Error('This request expired.')
  if (item.kind === 'approval' && phoneVerification.value === undefined) throw new Error(capabilityError.value || 'Approval verification availability is still being checked.')
  const result = await commitDesk(item, draft, sources, requestId, phoneVerification.value === true, adapters)
  if (!alive || identity !== owner.value) throw new Error('The signed-in person changed. Reopen the desk.')
  return result
}
function recorded(item: DeskItem) {
  items.value = items.value.map(row => row.id === item.id ? item : row)
  if (currentRead) currentRead = { ...currentRead, items: items.value }
  roundItems.value = roundItems.value.map(row => row.id === item.id ? item : row)
}
watch(owner, () => {
  generation++; opened.value = false; items.value = []; roundItems.value = []; round.value = []; roundBaseline.value = []
  sources = emptySources(); adapters = makeAdapters(); filter.value = 'open'; outcomeFilter.value = ''; pages.value = { open: 1, answered: 1 }
  currentRead = undefined
  phoneVerification.value = undefined; capabilityError.value = ''
  const identity = owner.value
  void phoneVerificationAvailable().then(available => { if (alive && identity === owner.value) phoneVerification.value = available })
    .catch(() => { if (alive && identity === owner.value) capabilityError.value = 'Approval verification availability could not be confirmed. Refresh the page before deciding.' })
  void refresh()
}, { immediate: true })
// A refresh signal arrives before the permission response. Only an actual
// reset invalidates the frozen source; can() reacts to the completed refresh.
const stopAccess = onAccessChange(change => { if (change === 'reset' && opened.value) roundItems.value = roundItems.value.map(item => allowed(item) ? item : { ...item, unavailable: 'Access changed. Reopen the desk to confirm this source.' }) })
let poll: ReturnType<typeof setInterval> | undefined
const focus = () => { if (!loading.value) void refresh() }
onMounted(() => { window.addEventListener('focus', focus); poll = setInterval(() => { if (!document.hidden && !loading.value) void refresh() }, 30_000) })
onBeforeUnmount(() => { alive = false; generation++; stopAccess(); clearInterval(poll); window.removeEventListener('focus', focus) })
</script>

<template>
  <main class="decision-desk" aria-labelledby="desk-title">
    <header class="desk-head"><div><p class="eyebrow">{{ session.identity?.tenant.name }}</p><h1 id="desk-title">Decision Desk</h1><p class="intro">One topic at a time. The work can wait while the answer takes shape.</p></div><div class="desk-links"><RouterLink to="/agents">Agents</RouterLink><button type="button" :disabled="loading" @click="refresh">Refresh</button></div></header>
    <div class="desk-summary"><div class="view-tabs" role="group" aria-label="Desk view"><button type="button" :aria-pressed="filter === 'open'" @click="filter = 'open'">Open <span>{{ items.filter(item => !item.decided).length }}</span></button><button type="button" :aria-pressed="filter === 'decided'" @click="filter = 'decided'">Decided <span>{{ items.filter(item => item.decided).length }}</span></button></div><label v-if="filter === 'decided'" class="outcome-filter">Stamp <select v-model="outcomeFilter" aria-label="Filter decided by stamp"><option value="">All</option><option v-for="(label, outcome) in outcomeLabels" :key="outcome" :value="outcome">{{ label }}</option></select></label><button type="button" class="review-button" :disabled="!visible.length || loading" @click="begin()">One at a time</button></div>
    <p v-if="state === 'loading'" role="status">Reading the desk…</p>
    <div v-if="warnings.length" class="source-warnings" role="status"><p v-for="warning in warnings" :key="warning">{{ warning }}</p></div>
    <p v-if="state === 'ready' && !visible.length">{{ warnings.length ? 'The available sources have no matching items.' : filter === 'decided' ? 'No recorded decisions in this view.' : 'Nothing is waiting in the available sources.' }}</p>
    <ol class="desk-list" aria-label="Desk items"><li v-for="item in visible" :key="item.id"><button type="button" class="desk-row" :data-testid="`desk-row-${item.id}`" @click="begin(item.id)"><span class="row-kind">{{ kindLabels[item.kind] }}</span><span class="row-topic"><strong>{{ item.title }}</strong><small>{{ item.projectName }} · {{ item.decided ? item.answer : item.meanwhile }}</small></span><span class="row-state">{{ item.decided ? outcomeLabels[item.outcome] : item.held ? 'Holding work' : 'Waiting' }}</span></button></li></ol>
    <p v-if="more[filter === 'open' ? 'open' : 'answered']" class="incomplete">More questions are available. <button v-if="pages[filter === 'open' ? 'open' : 'answered'] < MAX_QUESTION_PAGES" type="button" :disabled="loading" @click="loadMore">Load 100 more</button><span v-else>The view is limited to 1,000 questions per state.</span></p>
    <p class="chore-line">Sign-ins and account setup remain in <RouterLink to="/agents">Agents</RouterLink>.</p>
    <p v-if="opened && fresh.length" class="arrival-line">{{ fresh.length }} new item{{ fresh.length === 1 ? '' : 's' }} waiting for the next round.</p>
    <DecisionDeskMemo v-if="opened" :key="owner" :items="roundItems" :round="round" :start="start" :arrivals-count="fresh.length" :allowed="allowed" :decide="decide" @recorded="recorded" @close="opened = false" />
  </main>
</template>

<style scoped>
.decision-desk { width: min(1120px, 100%); margin: 0 auto; padding: 30px 32px; color: var(--ink); }.desk-head { display: flex; justify-content: space-between; gap: 24px; margin-bottom: 28px; }.eyebrow { font-size: 11px; color: var(--ink-3); margin: 0 0 7px; }h1 { font-size: 30px; font-weight: 550; margin: 0; letter-spacing: -.02em; }.intro { font-size: 13px; color: var(--ink-3); margin-top: 10px; }.desk-links { display: flex; align-items: flex-start; gap: 20px; padding-top: 10px; }.desk-links a, .chore-line a { color: var(--teal-ink); text-decoration: none; font-size: 12px; }button { cursor: pointer; }button:disabled { cursor: default; opacity: .5; }.desk-links button { border: 0; color: var(--ink-2); background: transparent; font-size: 12px; padding: 0; }.desk-summary { display: flex; align-items: center; gap: 18px; border-bottom: 1px solid var(--line-2); padding-bottom: 14px; }.view-tabs { display: flex; gap: 20px; }.view-tabs button { border: 0; background: transparent; color: var(--ink-3); font-size: 13px; padding: 8px 0; }.view-tabs button[aria-pressed=true] { font-weight: 700; color: var(--ink); }.view-tabs span { font-size: 11px; margin-left: 6px; font-variant-numeric: tabular-nums; }.review-button { margin-left: auto; border: 1px solid var(--line-2); border-radius: 8px; background: var(--glass); padding: 10px 16px; color: var(--teal-ink); font-size: 12px; font-weight: 600; }.outcome-filter { font-size: 12px; display: flex; gap: 8px; align-items: center; }.outcome-filter select { color: var(--ink); background: var(--surface); border: 1px solid var(--line); height: 32px; max-width: 130px; }.desk-list { margin: 0; padding: 0; list-style: none; }.desk-row { width: 100%; display: grid; grid-template-columns: 120px minmax(0,1fr) 100px; align-items: center; gap: 22px; padding: 20px 0; border: 0; border-bottom: 1px solid var(--line); background: transparent; text-align: left; color: var(--ink); }.desk-row:hover { background: var(--row-hover); }.row-kind { font-size: 11px; color: var(--ink-3); }.row-topic { min-width: 0; }.row-topic strong { display: block; font-size: 14px; font-weight: 550; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.row-topic small { display: block; font-size: 12px; color: var(--ink-3); margin-top: 7px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.row-state { font-size: 11px; text-align: right; color: var(--ink-3); }.chore-line, .incomplete { margin-top: 24px; font-size: 12px; color: var(--ink-3); }.incomplete button { border: 0; background: transparent; color: var(--teal); font-size: inherit; }.source-warnings { font-size: 12px; color: var(--warn-ink); padding: 10px 0; }.source-warnings p { margin: 5px 0; }.arrival-line { font-size: 12px; color: var(--teal-ink); }button:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }
@media (max-width: 720px) { .decision-desk { padding: 22px 18px; }.desk-head { gap: 12px; }.desk-links { gap: 12px; }h1 { font-size: 25px; }.intro { max-width: 24ch; }.desk-summary { flex-wrap: wrap; gap: 12px; }.desk-row { grid-template-columns: minmax(0,1fr) 78px; gap: 10px; padding: 16px 0; }.row-kind { grid-column: 1 / -1; }.row-state { grid-column: 2; grid-row: 2; }.row-topic { grid-row: 2; }.review-button { padding: 10px; }.outcome-filter { order: 3; } }
</style>
