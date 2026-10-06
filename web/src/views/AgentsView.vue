<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { tierEvidenceRefresh, type AgentEventIdentity } from '../lib/tierEvidenceLive'
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { can, myPermissions } from '../lib/authz'
import type { AttachQueueRow, AttachReview } from '../lib/attachWatch'
import { pairingPermissions } from '../lib/agentPairing'
import { message, subscribeAgents, type Approval, type SessionControl } from '../lib/agents'
import { canDecideApproval as allowedToDecide, controlBlocked, decidedApprovals, type Resource } from '../lib/agentState'
import type { AgentState } from '../lib/agentSignals'
import { confirmAction } from '../lib/confirm'
import { toast } from '../lib/toast'
import { TICKET_PEEK } from '../lib/ticketPeek'
import { usePoller } from '../lib/usePolledData'
import { useAgents, type HeldRequest, type SessionView } from '../stores/agents'
import { useCapacity } from '../stores/capacity'
import { useProjects } from '../stores/projects'
import { useSession } from '../stores/session'
import AppIcon from '../components/AppIcon.vue'
import KeyCap from '../components/KeyCap.vue'
import ApprovalQueue from '../components/agents/ApprovalQueue.vue'
import SessionList from '../components/agents/SessionList.vue'
import ChangeTierPopover from '../components/agents/ChangeTierPopover.vue'
import TierToast from '../components/agents/TierToast.vue'
import { useServiceTiers } from '../stores/serviceTiers'
import { controlPermitted } from '../lib/managedControl'
import SessionPanel from '../components/agents/SessionPanel.vue'
import HeadCounts from '../components/agents/HeadCounts.vue'
import { FILTER_LABEL, matchesFilter, type HeadFilter } from '../components/agents/headCounts'
import AccountsComputers from '../components/agents/AccountsComputers.vue'
import { openModelPrefs } from '../lib/modelPrefsCommand'
import AgentsWorking from '../components/agents/AgentsWorking.vue'
import StartAgentDialog from '../components/agents/StartAgentDialog.vue'
import LeadsList from '../components/lead/LeadsList.vue'
import { openStartLead } from '../lib/leadOverlay'
import { LEAD_WORDS } from '../lib/lead'
import { useDeveloperSettings } from '../lib/developerSettings'
import RunQueue from '../components/agents/RunQueue.vue'
import QuotaWarnings from '../components/agents/QuotaWarnings.vue'
import AttachApproval from '../components/agents/AttachApproval.vue'
import AttachPending from '../components/agents/AttachPending.vue'
import WindDownPanel from '../components/agents/WindDownPanel.vue'
import FloatingPanel from '../components/work/FloatingPanel.vue'
import { useAgentPause } from '../stores/agentPause'
import { agentsFooter } from '../lib/footerProviders'
import { countedLive, jumpTarget } from '../components/agents/sessionTree'
import { useFooterSummary } from '../lib/footerSummary'
import { clockTime, liveSession } from '../lib/agentPause'

// Markus's desk for agents: one head line whose counts filter Sessions, what waits
// on him (only when something does), the accounts with today's plan, then every
// session grouped by state. A session opens in the docked panel.
const agents = useAgents()
const pause = useAgentPause()
const headerMenu = ref<{ type: 'add' | 'more'; anchor: HTMLElement } | null>(null)
function headerAction(type: 'add' | 'more', event: Event) { headerMenu.value = headerMenu.value?.type === type ? null : { type, anchor: event.currentTarget as HTMLElement } }
function menuAction(action: () => void) {
  const anchor = headerMenu.value?.anchor
  headerMenu.value = null
  // Dialogs remember the durable header trigger, rather than a removed menu item.
  anchor?.focus({ preventScroll: true })
  action()
}
function menuKeys(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role=menuitem]')].filter(item => !(item as HTMLButtonElement).disabled)
  const i = items.indexOf(document.activeElement as HTMLElement)
  items[event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (i + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus()
}
const capacity = useCapacity()
const projects = useProjects()
const session = useSession()
const route = useRoute()
const router = useRouter()
const cursor = ref('')
const runQueue = ref<InstanceType<typeof RunQueue>>()
const windDown = ref<InstanceType<typeof WindDownPanel>>()
const sessionList = ref<InstanceType<typeof SessionList>>()
const filter = ref<HeadFilter | null>(null)
// Decision Desk and notification links focus the existing request card.
watch([() => route.query.needs, () => agents.loaded], async ([id, loaded]) => {
  if (!loaded || typeof id !== 'string' || !/^[am]:[0-9a-f-]{36}$/i.test(id)) return
  await nextTick()
  cursor.value = id
  focusRow(id)
}, { immediate: true })
// Provider-change notices link to the affected run's session or queued row.
watch([() => route.query.run, () => agents.loaded, () => agents.sessions, () => agents.runs], async ([id, loaded]) => {
  if (!loaded || typeof id !== 'string' || !/^[0-9a-f-]{36}$/i.test(id)) return
  const linked = agents.sessions.find(s => s.run_id === id)
  if (linked) { void router.replace({ path: `/agents/${linked.id}`, query: { ...route.query, run: undefined } }); return }
  await nextTick()
  if (route.query.run !== id) return
  // A folded Queued section opens for this visit to show the linked run.
  if (runQueue.value?.reveal()) await nextTick()
  const queued = document.getElementById(`run-${id}`)
  if (queued) { queued.focus(); queued.scrollIntoView({ block: 'nearest' }); void router.replace({ query: { ...route.query, run: undefined } }) }
}, { immediate: true })
const live = ref(false)
const stale = computed(() => agents.refreshStale || (agents.sessionsUpdatedAt !== null && agents.now - agents.sessionsUpdatedAt > 45_000))
const updatedFull = computed(() => agents.sessionsUpdatedAt === null ? '' : new Date(agents.sessionsUpdatedAt).toLocaleString())
const freshnessTip = computed(() => [live.value ? 'Connected to live updates' : 'Refreshing every 20 seconds', updatedFull.value ? `last update ${updatedFull.value}` : ''].filter(Boolean).join(' · '))
const queue = ref<InstanceType<typeof ApprovalQueue>>()
const startDialog = ref<InstanceType<typeof StartAgentDialog>>()
const attachDialog = ref<InstanceType<typeof AttachApproval>>()
const attachPending = ref<InstanceType<typeof AttachPending>>()
const canStart = computed(() => session.identity?.principal.kind === 'person' && can('work_orders.write') && can('run.create'))
const pairingAccess = computed(() => pairingPermissions({
  permissions: [...myPermissions()],
  principalKind: session.identity?.principal.kind,
}))
const showConnect = computed(() => !!session.identity && session.identity.principal.kind !== 'agent')
const canAttach = computed(() => session.identity?.principal.kind === 'person' && pairingAccess.value.canLookup)
// AEON-741: leads start workers; the manual Start agent is an expert opt-in.
const { showExpertStart } = useDeveloperSettings()
const leadsList = ref<InstanceType<typeof LeadsList>>()
const manualStart = computed(() => canStart.value && showExpertStart.value)
const leadless = computed(() => leadsList.value?.withoutLead ?? [])
const canStartLead = computed(() => !!leadsList.value?.mayStart && leadless.value.length > 0)
const leadlessNote = computed(() => leadless.value.length === 1 ? `${projects.byId(leadless.value[0]!)?.routeKey ?? 'One project'} has none. One per project.` : `${leadless.value.length} projects have none. One per project.`)
const showNew = computed(() => manualStart.value || canStartLead.value || canAttach.value || showConnect.value)
watch(() => `${session.identity?.tenant.id}/${session.identity?.principal.id}`, () => { headerMenu.value = null; filter.value = null })

const ticketPeek = inject(TICKET_PEEK, null)
const ticketPeekOpen = computed(() => !!ticketPeek?.openKey.value)
const sessionId = computed(() => typeof route.params.sessionId === 'string' ? route.params.sessionId : '')
const selected = computed(() => [...agents.views, ...agents.historyViews].find(v => v.session.id === sessionId.value))
// A link to a session that ended more than a day ago finds it in History,
// reading older pages until it appears or the server has no more.
watch([sessionId, () => agents.loaded, () => agents.historyState], ([id, loaded, state]) => {
  if (!id || !loaded || selected.value) return
  if (state === 'idle') void agents.loadHistory()
  else if (state === 'ready' && agents.historyMore) void agents.loadOlderHistory()
}, { immediate: true })
const writable = computed(() => can('harness.control'))
const canResolve = computed(() => session.identity?.principal.kind === 'person' && can('inbox.manage'))
const canRevoke = computed(() => session.identity?.principal.kind === 'person' && can('approvals.revoke'))
const canDecide = computed(() => session.identity?.principal.kind === 'person' && (can('approvals.decide') || canResolve.value))
const canDecideApproval = (approval: Approval) => session.identity?.principal.kind === 'person' && allowedToDecide(approval, can)
const history = computed(() => decidedApprovals(agents.approvals, agents.now))
const showCapacity = computed(() => agents.loaded && capacity.state !== 'forbidden' && agents.accountsState !== 'forbidden')

// ---------- Accounts and computers: one computer-first panel (AEON-499) ----------
const showSetup = computed(() => agents.loaded && (showCapacity.value || (pairingAccess.value.canListComputers && capacity.computers.length > 0)))
const pageTitle = ref<HTMLElement>()
const attachCount = ref(0)
const attachRows = ref<AttachQueueRow[]>([])
const attachHistory = ref<AttachQueueRow[]>([])
const waiting = computed(() => agents.needsCount + capacity.signins.length + attachCount.value)
function reviewAttach(request: AttachReview) { attachDialog.value?.show(request, attachRows.value.map(row => row.review)) }

// ---------- Resources and people ----------
function resource(approval: Approval): Resource {
  if (approval.resource_kind === 'tenant') return { label: 'the whole workspace' }
  if (approval.resource_kind === 'run') {
    // A run is shown by the ticket its session works on.
    const run = approval.run_id ?? approval.resource_id
    const ticket = agents.views.find(v => v.session.run_id === run && v.ticket)?.ticket
    return ticket ? { label: ticket.title, key: ticket.key, title: ticket.title, href: ticket.href } : { label: 'its current run' }
  }
  const id = approval.resource_id ?? ''
  const project = projects.byId(id)
  // A project reads by its name alone; its key would only repeat it.
  if (project) return { label: project.title, title: project.title, href: `/p/${encodeURIComponent(project.routeKey)}` }
  const node = agents.nodes[id]
  if (!node) return { label: 'a ticket' }
  const owner = projects.byRouteKey(node.key.split('-')[0] ?? '')
  return { label: node.title, key: node.key, title: node.title, href: owner ? `/p/${encodeURIComponent(owner.routeKey)}/${encodeURIComponent(node.key)}` : undefined }
}
function openAgent(principalId: string) {
  const views = agents.byAgent(principalId)
  const target = views.find(v => v.status.group !== 'stopped') ?? views[0]
  if (target) openSession(target.session.id)
  else toast('This agent has no session listed right now.')
}

// ---------- Actions ----------
// A decision confirms on its own card, which moves focus on and announces it (AEON-505);
// the page keeps Needs you while a decided card still shows, then folds it away.
const settling = ref(false)
const announcement = ref('')
function announce(text: string) { announcement.value = ''; void nextTick(() => { announcement.value = text }) }
const reducedMotion = () => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false
let foldHadFocus = false
// The height folds to nothing and a negative margin takes the column gap with it, so
// what follows moves up smoothly and does not jump when the card is gone.
function foldNeeds(el: Element, done: () => void) {
  const card = el as HTMLElement
  foldHadFocus = card.contains(document.activeElement)
  // The app owns this fold; native anchoring must not correct its scroll offset.
  const scroller = card.closest<HTMLElement>('main'), anchor = scroller?.style.overflowAnchor ?? ''
  if (scroller) scroller.style.overflowAnchor = 'none'
  const remove = () => {
    done()
    requestAnimationFrame(() => { if (scroller) scroller.style.overflowAnchor = anchor })
  }
  if (reducedMotion() || !card.parentElement) { remove(); return }
  const px = (value: string) => parseFloat(value) || 0
  const style = getComputedStyle(card)
  const edges = px(style.borderTopWidth) + px(style.borderBottomWidth) + px(style.paddingTop) + px(style.paddingBottom)
  const gap = px(getComputedStyle(card.parentElement).rowGap)
  Object.assign(card.style, { height: `${card.offsetHeight}px`, minHeight: '0', overflow: 'clip' })
  void card.offsetHeight
  Object.assign(card.style, { transition: 'height .28s cubic-bezier(.4, 0, .2, 1), margin-bottom .28s cubic-bezier(.4, 0, .2, 1), opacity .2s ease', height: '0px', marginBottom: `${-(gap + edges)}px`, opacity: '0' })
  const finish = () => { clearTimeout(timer); card.removeEventListener('transitionend', ended); remove() }
  const ended = (event: TransitionEvent) => { if (event.target === card && event.propertyName === 'height') finish() }
  const timer = setTimeout(finish, 450)
  card.addEventListener('transitionend', ended)
}
// Focus that was inside the folded card goes to Decided, never to the page body.
function needsFolded() {
  if (!foldHadFocus) return
  foldHadFocus = false
  document.querySelector<HTMLElement>('.agents-page .decided .history-toggle')?.focus({ preventScroll: true })
}
async function resolveHeld(request: HeldRequest, decision: 'resolved' | 'dismissed', note: string) {
  await agents.resolve(request, decision, note)
  toast(`${decision === 'resolved' ? 'Resolved' : 'Dismissed'}: the request from ${agents.askerName(request.sender_principal_id).name} is answered.`)
  await nextTick()
  const next = agents.pending[0] ?? null
  const nextHeld = agents.held[0] ?? null
  cursor.value = next ? `a:${next.id}` : nextHeld ? `m:${nextHeld.id}` : ''
  if (cursor.value) focusRow(cursor.value)
}
// Per session, like the server: harness.control in that session's project.
const controlBlock = (view: SessionView, kind: SessionControl['kind']) => controlBlocked(view.session, kind, view.name, controlPermitted(view.session, { person: session.identity?.principal.kind === 'person', can }), agents.controls[view.session.id])
async function control(view: SessionView, kind: SessionControl['kind']) {
  if (kind === 'stop') {
    const ok = await confirmAction({
      title: `Stop ${view.name}?`, danger: true, confirmLabel: 'Stop session',
      body: `${view.harness} ends this session after its current step. It cannot be resumed; the next session starts fresh.`,
    })
    if (!ok) return
  }
  try {
    await agents.control(view, kind)
    toast(kind === 'stop' ? `Stop sent to ${view.name}.` : `Interrupt sent to ${view.name}.`)
  } catch (e) { toast(message(e), { tone: 'error' }) }
}
// A head count filters Sessions to its state (AEON-780); pressing it again, the
// chip's × or Esc shows all again. The filter is a look, never a setting.
// A folded Sessions section opens for this visit (AEON-784). The reveal is
// recorded even when Sessions shows open, so a preference read that lands
// after the filter cannot fold the rows away.
async function setFilter(next: HeadFilter) {
  filter.value = filter.value === next ? null : next
  if (!filter.value) return
  if (sessionList.value?.reveal()) await nextTick()
  await nextTick()
  // The cursor starts on the first match; Sessions scroll into view only when out of view.
  const first = document.querySelector<HTMLElement>('.agents-page .sessions .row[data-row]:not(.filter-context)')
  if (first?.dataset.row) cursor.value = first.dataset.row
  const list = document.querySelector<HTMLElement>('.agents-page .sessions')
  const box = list?.getBoundingClientRect()
  if (list && box && (box.top < 0 || box.top > window.innerHeight - 160)) list.scrollIntoView({ block: 'start', behavior: reducedMotion() ? 'auto' : 'smooth' })
}
function clearFilter() {
  const was = filter.value
  filter.value = null
  // The chip is gone; focus goes back to the count that set it.
  if (was) document.querySelector<HTMLElement>(`.agents-page .page-head [data-filter="${was}"]`)?.focus({ preventScroll: true })
}
// The footer's counts jump to the first session in that state. The row may be
// a folded descendant: pick it from the sessions, open its ancestors, then focus it.
async function jump(state: AgentState) {
  const target = jumpTarget(agents.views, state)
  if (!target) return
  // An active Sessions filter that hides the row moves to the footer's own state: the chip stays where it is and only its word changes.
  const view = agents.views.find(v => v.session.id === target.id)
  if (filter.value && view && !matchesFilter(view, filter.value)) {
    filter.value = matchesFilter(view, state as HeadFilter) ? state as HeadFilter : null
    announce(filter.value ? `Sessions now show ${FILTER_LABEL[filter.value].toLowerCase()} only` : 'Sessions now show all')
  }
  sessionList.value?.revealSession(target.id)
  await nextTick()
  cursor.value = `s:${target.id}`
  focusRow(cursor.value)
}
function review(approval: Approval) {
  if (window.innerWidth < 1100) void closePanel()
  cursor.value = `a:${approval.id}`
  void nextTick(() => focusRow(cursor.value))
}

// ---------- Footer summary (AEON-785): count · state · the one exception ----------
const footerWind = computed(() => {
  const by = pause.report?.deadline_at
  if (!by) return null
  const left = pause.reportIds.filter(id => { const s = agents.sessionById(id); return !!s && liveSession(s) }).length
  return { left, by: clockTime(by) }
})
// What this screen lists: live sessions, plus those paused on purpose.
useFooterSummary(() => {
  // A first read that failed shows its own error; the footer does not wait for it.
  if (!session.identity || (!agents.loaded && agents.sessionsError)) return null
  const live = agents.views.filter(countedLive)
  const states = (groups: string[]) => live.filter(v => groups.includes(v.status.group)).length
  const paused = agents.views.filter(v => v.status.state === 'paused').length
  const top = () => document.querySelector('main')?.scrollTo({ top: 0, behavior: reducedMotion() ? 'auto' : 'smooth' })
  return agentsFooter({
    loaded: agents.loaded,
    total: live.length + paused,
    working: states(['working']),
    idle: states(['idle']),
    throttled: states(['throttled']),
    awaiting: states(['awaiting']),
    pausing: states(['pausing']),
    problems: states(['problem', 'unresponsive']),
    asks: agents.needsCount,
    paused,
    wind: footerWind.value,
    act: {
      problem: () => void jump('problem'),
      ask: () => { const first = agents.pending[0] ? `a:${agents.pending[0].id}` : agents.held[0] ? `m:${agents.held[0].id}` : ''; if (first) { cursor.value = first; focusRow(first) } },
      wind: () => windDown.value?.focusWindDown(),
      paused: () => void jump('paused'),
      top,
    },
  })
}, () => session.identity ? { updatedAt: agents.sessionsUpdatedAt, paused: stale.value } : null)

// ---------- Panel ----------
// Like the ticket panel: opening from the list adds one history entry, switching
// sessions replaces it, and closing goes back to the list entry instead of adding one.
let openedFromList = false
function openSession(id: string) {
  cursor.value = `s:${id}`
  if (sessionId.value) { void router.replace({ path: `/agents/${id}`, query: route.query }); return }
  openedFromList = true
  void router.push({ path: `/agents/${id}`, query: route.query })
}
// Focus returns to the session's row once the panel is gone.
async function closePanel() {
  const id = sessionId.value
  if (id) cursor.value = `s:${id}`
  const back = openedFromList && typeof window.history.state?.back === 'string' && window.history.state.back.startsWith('/agents')
  openedFromList = false
  if (back) {
    const closed = new Promise<void>(resolve => { const stop = watch(sessionId, value => { if (!value) { stop(); resolve() } }); setTimeout(() => { stop(); resolve() }, 1000) })
    router.back()
    await closed
  }
  else await router.replace({ path: '/agents', query: route.query })
  await nextTick()
  if (id) focusRow(cursor.value)
}

// ---------- Keyboard: j/k move, a approve, d deny, Enter opens, Esc closes ----------
// A folded section's rows stay in the page but inert; the cursor skips them.
function rows() { return [...document.querySelectorAll<HTMLElement>('.agents-page [data-row]')].filter(el => !el.closest('[inert]')) }
function focusRow(id: string) {
  const el = document.querySelector<HTMLElement>(`.agents-page [data-row="${CSS.escape(id)}"]`)
  el?.focus({ preventScroll: true })
  el?.scrollIntoView({ block: 'nearest' })
}
function move(step: number) {
  const list = rows()
  if (!list.length) return
  const index = list.findIndex(el => el.dataset.row === cursor.value)
  const next = list[index === -1 ? (step > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, index + step))]
  cursor.value = next.dataset.row ?? ''
  focusRow(cursor.value)
  // With the panel open, the panel follows the cursor through sessions.
  if (sessionId.value && cursor.value.startsWith('s:')) void router.replace({ path: `/agents/${cursor.value.slice(2)}`, query: route.query })
}
const serviceTiers = useServiceTiers()
function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}
function keydown(event: KeyboardEvent) {
  if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return
  if (document.querySelector('dialog[open], .floating') || typing(event.target)) return
  if (event.target instanceof Node && document.querySelector('.ticket-peek-host')?.contains(event.target)) return
  const [kind, id] = [cursor.value.slice(0, 1), cursor.value.slice(2)]
  switch (event.key) {
    case 'j': case 'ArrowDown': event.preventDefault(); move(1); break
    case 'k': case 'ArrowUp': event.preventDefault(); move(-1); break
    case 'a': case 'd':
      if (kind === 'a') { event.preventDefault(); void queue.value?.begin(id, event.key === 'a' ? 'approve' : 'deny') }
      else if (kind === 'm') { event.preventDefault(); void queue.value?.begin(id, event.key === 'a' ? 'resolve' : 'dismiss') }
      break
    case 'Enter': case 'o':
      if ((event.target as HTMLElement).closest('a, button, summary')) return
      if (kind === 's') { event.preventDefault(); openSession(id) }
      else if (kind === 'm') { event.preventDefault(); const held = agents.held.find(m => m.id === id); if (held) openAgent(held.sender_principal_id) }
      break
    case 'p': case 'r': {
      const view = agents.views.find(v => v.session.id === id)
      if (kind === 's' && view && pause.eligible(view.session, event.key === 'p' ? 'pause' : 'resume')) { event.preventDefault(); pause.open(event.key === 'p' ? 'pause' : 'resume', [view.session]) }
      break
    }
    case 'Escape':
      if (ticketPeekOpen.value) return
      if (sessionId.value) { event.preventDefault(); void closePanel() }
      else if (filter.value) { event.preventDefault(); filter.value = null }
      break
  }
}

// ---------- Live ----------
let stop: (() => void) | undefined
const poller = usePoller(() => Promise.all([agents.loadAll(), capacity.load(), pause.refreshLeaving()]), 20_000, { invalidate: () => { agents.invalidatePolls(); capacity.invalidate() } })
let clock: ReturnType<typeof setInterval> | undefined
let debounce: ReturnType<typeof setTimeout> | undefined
const evidenceRefresh = tierEvidenceRefresh(() => selected.value?.session, session => {
  void serviceTiers.load(session).catch(error => { serviceTiers.errors[session.id] = error instanceof Error ? error.message : 'Tier evidence unavailable.' })
}, () => document.visibilityState !== 'hidden')
function evidenceVisible() { if (document.visibilityState !== 'hidden') evidenceRefresh.notify() }
function changed(event?: string, identity?: AgentEventIdentity) {
  evidenceRefresh.notify(event, identity)
  // Confirmation polling already tracks its own exact pending sessions.
  if (!event) serviceTiers.reconcile()
  if (document.visibilityState === 'hidden' || debounce) return
  // A fixed batch window cannot be starved by a stream of new worker events.
  debounce = setTimeout(() => {
    debounce = undefined
    void agents.loadAll()
    void pause.refreshLeaving()
  }, 400)
}
let stopTierWatch: (() => void) | undefined
onMounted(() => {
  stopTierWatch = serviceTiers.watchPage()
  document.addEventListener('visibilitychange', evidenceVisible)
  void agents.loadAll()
  void capacity.load()
  stop = subscribeAgents(changed, value => { live.value = value }, () => agents.deliveryChanged())
  poller.start()
  clock = setInterval(() => agents.tick(), 1000)
  window.addEventListener('keydown', keydown)
})
onBeforeUnmount(() => {
  evidenceRefresh.stop(); document.removeEventListener('visibilitychange', evidenceVisible)
  stopTierWatch?.()
  stop?.(); poller.stop(); clearInterval(clock); clearTimeout(debounce)
  window.removeEventListener('keydown', keydown)
})
watch(sessionId, id => { if (id) cursor.value = `s:${id}` }, { immediate: true })
</script>

<template>
  <section class="agents-page" :class="{ 'panel-open': !!sessionId && !ticketPeekOpen }" aria-labelledby="agents-title">
    <!-- One line (AEON-780): title, the counts as filters, then the controls. -->
    <header class="page-head">
      <h1 id="agents-title" ref="pageTitle" tabindex="-1">Agents</h1>
      <HeadCounts :views="agents.views" :now="agents.now" :loaded="agents.loaded" :filter="filter" @filter="setFilter">
        <!-- No "Live" in the head; only a delay is worth saying, at the line end (AEON-785 moves freshness to the footer). -->
        <p v-if="stale" class="freshness" role="status" :data-tip="freshnessTip"><span class="live-mark" aria-hidden="true" />Update delayed</p>
      </HeadCounts>
      <div class="head-side">
        <button v-if="showNew" type="button" class="btn primary add-agent" aria-label="New: start a lead, attach a session or connect a machine" aria-haspopup="menu" :aria-expanded="headerMenu?.type === 'add'" @click="headerAction('add', $event)"><AppIcon name="plus" :size="17" /></button>
        <button type="button" class="icon-btn flat more-agent" aria-label="More agent actions" aria-haspopup="menu" :aria-expanded="headerMenu?.type === 'more'" @click="headerAction('more', $event)"><AppIcon name="more" /></button>
      </div>
      <AttachApproval ref="attachDialog" hide-trigger :now="agents.now" @changed="(result, declined) => attachPending?.settle(result, declined)" />
      <FloatingPanel v-if="headerMenu" :anchor="headerMenu.anchor" align="end" :width="300" :label="headerMenu.type === 'add' ? 'Add agent or computer' : 'More agent actions'" @close="restore => { if (restore) headerMenu?.anchor.focus(); headerMenu = null }">
        <div role="menu" class="header-menu" @keydown="menuKeys">
          <template v-if="headerMenu.type === 'add'">
            <button v-if="canStartLead" type="button" role="menuitem" class="menu-item" @click="menuAction(() => openStartLead(leadless, headerMenu?.anchor ?? null, true))"><AppIcon name="play" /><span>Start {{ LEAD_WORDS.l }}…<small>{{ leadlessNote }}</small></span></button>
            <button v-if="canAttach" type="button" role="menuitem" class="menu-item" @click="menuAction(() => attachDialog?.open())"><AppIcon name="link" /><span>Attach a running session…<small>Bring in a session started in a terminal.</small></span></button>
            <RouterLink v-if="showConnect" role="menuitem" class="menu-item" to="/agents/register-agent" @click="headerMenu = null"><AppIcon name="monitor" /><span>Connect your machine…<small>Install agentd so it can run agents.</small></span></RouterLink>
            <template v-if="manualStart">
              <div class="menu-sep" role="separator" /><p class="eyebrow menu-eyebrow">Expert</p>
              <button type="button" role="menuitem" class="menu-item" @click="menuAction(() => startDialog?.open())"><AppIcon name="agent" /><span>Start agent manually…<small>Pick ticket, host, harness, account, model and thinking yourself. Same checks apply.</small></span></button>
            </template>
          </template>
          <!-- Pause all, Resume all and Wind down live with the wind-down; History in the Sessions head (AEON-780). -->
          <template v-else>
            <button role="menuitem" type="button" class="menu-item" aria-label="Model preferences" @click="menuAction(() => openModelPrefs())"><AppIcon name="gear" /><span>Model preferences<small>Which models do which work.</small></span></button>
            <RouterLink role="menuitem" class="menu-item" to="/agents/usage" @click="headerMenu = null"><AppIcon name="pulse" /><span>Usage<small>Tokens and cost per agent, account and day.</small></span></RouterLink>
            <RouterLink role="menuitem" class="menu-item" to="/decision-desk" aria-label="Decision Desk" @click="headerMenu = null"><AppIcon name="inbox" /><span>Decision Desk<small>Everything agents ask of people.</small></span></RouterLink>
            <RouterLink v-if="can('keys.manage')" role="menuitem" class="menu-item" to="/settings/access/agents" @click="headerMenu = null"><AppIcon name="key" /><span>Agent keys<small>Scoped API keys for agents.</small></span></RouterLink>
            <div class="menu-sep" role="separator" />
            <RouterLink role="menuitem" class="menu-item" to="/settings/personal#agents" @click="headerMenu = null"><AppIcon name="gear" /><span>Agent settings<small>Default pause level and indicators.</small></span></RouterLink>
          </template>
        </div>
      </FloatingPanel>
    </header>

    <!-- The first load swaps the loading layout for the real one in one step, so the
         page does not jump as each read lands. -->
    <div :key="agents.loaded ? 'ready' : 'loading'" class="layout">
      <div class="main-col">
        <AttachPending ref="attachPending" :now="agents.now" @count="count => attachCount = count" @rows="rows => attachRows = rows" @history="rows => attachHistory = rows" @updated="requests => attachDialog?.sync(requests)" v-slot="{ rows }">
        <Transition :css="false" @leave="foldNeeds" @after-leave="needsFolded">
          <ApprovalQueue
            v-if="agents.loaded && (waiting || settling || rows.length)"
            ref="queue" :attaches="rows" :attach-history="attachHistory" :pending="agents.pending" :held="agents.held" :signins="capacity.signins" :history="history" :now="agents.now" :loaded="agents.loaded"
            :cursor="cursor" :can-decide="canDecide" :can-decide-approval="canDecideApproval" :can-resolve="canResolve" :can-revoke="canRevoke" :asker="agents.askerName" :resource="resource" :decide="agents.decide" :revoke="agents.revoke" :resolve="resolveHeld"
            @review-attach="request => { attachRows = rows; reviewAttach(request) }" @dismiss-attach="id => attachPending?.dismiss(id)" @focus-row="id => cursor = id" @open-agent="openAgent" @settling="active => settling = active" @announce="announce"
          />
        </Transition>
        </AttachPending>
        <AgentsWorking v-if="showSetup && session.identity?.principal.kind === 'person'" />
        <LeadsList v-if="agents.loaded" ref="leadsList" />
        <AccountsComputers v-if="showSetup" :permissions="pairingAccess" :show-accounts="showCapacity" />
        <p v-if="agents.approvalsHardError" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />Permission requests could not be loaded: {{ agents.approvalsError }} <button type="button" class="btn sm" @click="agents.refreshApprovals()">Try again</button></p>
        <WindDownPanel v-if="agents.loaded" ref="windDown" />
        <SessionList
          v-if="agents.loaded" ref="sessionList"
          :groups="agents.grouped" :history="agents.historyViews" :history-state="agents.historyState" :history-more="agents.historyMore" :now="agents.now" :cursor="cursor" :selected="sessionId" :state="agents.sessionsUpdatedAt !== null ? 'ready' : agents.sessionsState" :error="agents.sessionsError"
          :loaded="agents.loaded" :controls="agents.controls" :can-start="manualStart" :can-lead="canStart" :filter="filter"
          @open="openSession" @control="control" @focus-row="id => cursor = id" @retry="agents.loadAll()" @start="startDialog?.open()" @history="agents.loadHistory(true)" @older="agents.loadOlderHistory()" @clear-filter="clearFilter"
        />
        <p v-if="agents.sessionsUpdatedAt !== null && agents.sessionsState === 'error'" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />Sessions could not be refreshed: {{ agents.sessionsError }} <button type="button" class="btn sm" @click="agents.loadAll()">Try again</button></p>
        <ApprovalQueue
          v-if="agents.loaded && !waiting && !settling && !attachRows.length && (history.length || attachHistory.length)" history-only :attach-history="attachHistory"
          :pending="[]" :held="[]" :history="history" :now="agents.now" :loaded="agents.loaded" cursor="" :can-decide="false" :can-decide-approval="() => false" :can-resolve="false"
          :can-revoke="canRevoke" :asker="agents.askerName" :resource="resource" :decide="agents.decide" :revoke="agents.revoke" :resolve="resolveHeld"
        />
        <QuotaWarnings v-if="agents.loaded && showCapacity" :sessions="agents.views" />
        <RunQueue v-if="agents.loaded" ref="runQueue" @emptied="pageTitle?.focus()" />
        <p v-if="agents.loaded && (agents.views.length || agents.pending.length)" class="hint" aria-hidden="true">
          <kbd class="keycap">j</kbd><kbd class="keycap">k</kbd> move · <KeyCap k="left" /><KeyCap k="right" /> fold · <kbd class="keycap"><AppIcon name="enter" /></kbd> open · <kbd class="keycap">p</kbd> pause · <kbd class="keycap">r</kbd> resume · <kbd class="keycap">a</kbd> approve · <kbd class="keycap">d</kbd> deny
        </p>
      </div>
    </div>

    <SessionPanel
      v-if="sessionId && agents.loaded && !ticketPeekOpen" :view="selected" :loading="!selected && (agents.historyState === 'loading' || (agents.historyState === 'ready' && agents.historyMore))" :now="agents.now" :can-write="writable" :control-block="controlBlock"
      @close="closePanel" @control="control" @review="review"
    />
    <ChangeTierPopover v-if="serviceTiers.dialog" :key="serviceTiers.dialog.instance" />
    <TierToast />
    <StartAgentDialog ref="startDialog" />
    <p class="sr-only" aria-live="polite" aria-atomic="true">{{ announcement }}</p>
  </section>
</template>

<style scoped>
.agents-page { width: 100%; margin: 0; padding: 22px var(--gutter) 24px; }
/* One 48 px line: title, counts, then the controls at the right (AEON-780). */
.page-head { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 22px; min-height: 48px; margin-bottom: 14px; }
.page-head h1 { margin: 0; font-size: 30px; white-space: nowrap; }
.page-head .head-counts { flex: 0 1 auto; }
.head-side { display: flex; align-items: center; flex-wrap: nowrap; gap: 6px; margin-left: auto; flex: none; }
/* The page's one primary button, without the glow (base.css glows are retired here). */
.add-agent { width: 36px; height: 36px; min-height: 0; padding: 0; border-radius: 50%; }
.add-agent:not(:focus-visible) { box-shadow: 0 0 0 1px rgba(14, 111, 108, .5), inset 0 1px 0 rgba(255, 255, 255, .35); }
/* Only a delay is said in the head; a dot and a word, details on hover. */
.freshness { display: inline-flex; align-items: center; gap: 7px; height: 30px; padding: 0 8px; color: var(--gold-ink); font-size: 12px; white-space: nowrap; }
.live-mark { width: 7px; height: 7px; border-radius: 50%; background: var(--gold); flex: none; }
.header-menu .menu-sep { height: 1px; margin: 4px 8px; background: var(--line); }
.header-menu .menu-eyebrow { margin: 0; padding: 8px 10px 2px; }
.header-menu .menu-item { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 48px; padding: 8px 10px; border: 0; border-radius: 6px; background: transparent; color: var(--ink); font-size: 13px; text-align: left; text-decoration: none; }
.header-menu .menu-item:hover { background: var(--row-hover); }
.header-menu .menu-item span { display: grid; gap: 3px; }
.header-menu small { font-size: 11px; color: var(--ink-3); }
.layout { display: grid; grid-template-columns: minmax(0, 1fr); gap: 20px; align-items: start; container: agents-layout / inline-size; }
/* Reports and live groups grow below the controls without changing the viewport offset. */
.agents-page { overflow-anchor: none; }
/* A column, not a grid: a folding card's negative margin can take the gap with it (AEON-505). */
.main-col { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
.inline-error { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; padding: 10px 14px; border-radius: 12px; background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); font-size: 13px; color: var(--danger); }
.hint { display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 5px; padding: 4px 0; font-size: 12px; color: var(--ink-3); }
.hint .keycap + .keycap { margin-left: 2px; }
/* Wide screens dock the session panel: the page reflows beside it. */
@media (min-width: 1100px) {
  .agents-page.panel-open { margin: 0; padding-right: calc(var(--panel-w) + 22px); }
}
@media (max-width: 720px) {
  .agents-page { padding: 16px 12px 20px; }
  .hint { display: none; }
  /* Phones: title and controls on line 1 (44 px), the counts on line 2. */
  .page-head { gap: 2px 8px; }
  .page-head h1 { font-size: 26px; }
  .page-head .head-counts { order: 3; flex: 1 1 100%; }
  .add-agent, .more-agent { width: 44px; height: 44px; }
  .header-menu .menu-item { min-height: 52px; }
  .freshness { height: 44px; }
}
</style>
