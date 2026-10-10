<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { tierEvidenceRefresh, type AgentEventIdentity } from '../lib/tierEvidenceLive'
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { can, myPermissions } from '../lib/authz'
import type { AttachQueueRow, AttachReview } from '../lib/attachWatch'
import { pairingPermissions } from '../lib/agentPairing'
import { message, subscribeAgents, type Approval, type SessionControl } from '../lib/agents'
import { controlBlocked } from '../lib/agentState'
import type { AgentState } from '../lib/agentSignals'
import { confirmAction } from '../lib/confirm'
import { toast } from '../lib/toast'
import { TICKET_PEEK } from '../lib/ticketPeek'
import { usePoller } from '../lib/usePolledData'
import { useAgents, type SessionView } from '../stores/agents'
import { useProjects } from '../stores/projects'
import { useDecisionDesk } from '../stores/decisionDesk'
import DecisionDeskPanel from '../components/agents/DecisionDeskPanel.vue'
import { useCapacity } from '../stores/capacity'
import { useSession } from '../stores/session'
import AppIcon from '../components/AppIcon.vue'
import KeyCap from '../components/KeyCap.vue'
import AgentChores from '../components/agents/AgentChores.vue'
import { deskLinkItem } from '../lib/decisionDesk'
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
import { leadLaunchReason, LEAD_WORDS } from '../lib/lead'
import { useDeveloperSettings } from '../lib/developerSettings'
import RunQueue from '../components/agents/RunQueue.vue'
import AttachApproval from '../components/agents/AttachApproval.vue'
import AttachPending from '../components/agents/AttachPending.vue'
import WindDownControl from '../components/agents/WindDownControl.vue'
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
const projects = useProjects(), desk = useDecisionDesk()
const announcement = ref('')
function announce(text: string) { announcement.value = ''; void nextTick(() => { announcement.value = text }) }
const reducedMotion = () => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false
const session = useSession()
const route = useRoute()
const router = useRouter()
const cursor = ref('')
const runQueue = ref<InstanceType<typeof RunQueue>>()
const windDown = ref<InstanceType<typeof WindDownControl>>()
const sessionList = ref<InstanceType<typeof SessionList>>()
const filter = ref<HeadFilter | null>(null)
// Existing briefing/notification links retain the original source identity.
watch(() => route.query.needs, value => {
  const id = deskLinkItem(value)
  if (id) void router.replace({ path: '/decision-desk', query: { item: id } })
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
const leadLaunchAvailable = computed(() => !!leadsList.value?.launchAvailable)
const leadlessNote = computed(() => !leadLaunchAvailable.value ? leadsList.value?.launchReason ?? leadLaunchReason(null) : leadless.value.length === 1 ? `${projects.byId(leadless.value[0]!)?.routeKey ?? 'One project'} has none. One per project.` : `${leadless.value.length} projects have none. One per project.`)
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
const showCapacity = computed(() => agents.loaded && capacity.state !== 'forbidden' && agents.accountsState !== 'forbidden')

// ---------- Accounts and computers: one computer-first panel (AEON-499) ----------
const showSetup = computed(() => agents.loaded && (showCapacity.value || (pairingAccess.value.canListComputers && capacity.computers.length > 0)))
const pageTitle = ref<HTMLElement>()
const attachRows = ref<AttachQueueRow[]>([])
const attachHistory = ref<AttachQueueRow[]>([])
function reviewAttach(request: AttachReview) { attachDialog.value?.show(request, attachRows.value.map(row => row.review)) }

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
  void router.push({ path: '/decision-desk', query: { item: `a:${approval.id}` } })
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
    asks: desk.count ?? 0,
    paused,
    wind: footerWind.value,
    act: {
      problem: () => void jump('problem'),
      ask: () => void router.push('/decision-desk'),
      wind: () => windDown.value?.openStatus(),
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

// ---------- Keyboard: j/k move, Enter opens, Esc closes ----------
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
    case 'Enter': case 'o':
      if ((event.target as HTMLElement).closest('a, button, summary')) return
      if (kind === 's') { event.preventDefault(); openSession(id) }
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
  if (!event || event.startsWith('inbox.')) agents.threadChanged()
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
        <!-- Wind down is a head control (AEON-783): ghost button, then a teal chip while it runs. -->
        <WindDownControl v-if="agents.loaded" ref="windDown" />
        <button v-if="showNew" type="button" class="btn primary add-agent" aria-label="New: start a lead, attach a session or connect a machine" aria-haspopup="menu" :aria-expanded="headerMenu?.type === 'add'" @click="headerAction('add', $event)"><AppIcon name="plus" :size="17" /></button>
        <button type="button" class="icon-btn flat more-agent" aria-label="More agent actions" aria-haspopup="menu" :aria-expanded="headerMenu?.type === 'more'" @click="headerAction('more', $event)"><AppIcon name="more" /></button>
      </div>
      <AttachApproval ref="attachDialog" hide-trigger :now="agents.now" @changed="(result, declined) => attachPending?.settle(result, declined)" />
      <FloatingPanel v-if="headerMenu" :anchor="headerMenu.anchor" align="end" :width="300" :label="headerMenu.type === 'add' ? 'Add agent or computer' : 'More agent actions'" @close="restore => { if (restore) headerMenu?.anchor.focus(); headerMenu = null }">
        <div role="menu" class="header-menu" @keydown="menuKeys">
          <template v-if="headerMenu.type === 'add'">
            <button v-if="canStartLead" type="button" role="menuitem" class="menu-item" :aria-disabled="!leadLaunchAvailable" @click="leadLaunchAvailable && menuAction(() => openStartLead(leadless, headerMenu?.anchor ?? null, true))"><AppIcon name="play" /><span>Start {{ LEAD_WORDS.l }}…<small>{{ leadlessNote }}</small></span></button>
            <button v-if="canAttach" type="button" role="menuitem" class="menu-item" @click="menuAction(() => attachDialog?.open())"><AppIcon name="link" /><span>Attach a running session…<small>Bring in a session started in a terminal.</small></span></button>
            <RouterLink v-if="showConnect" role="menuitem" class="menu-item" to="/agents/register-agent" @click="headerMenu = null"><AppIcon name="monitor" /><span>Connect your machine…<small>Install agentd so it can run agents.</small></span></RouterLink>
            <template v-if="manualStart">
              <div class="menu-sep" role="separator" /><p class="eyebrow menu-eyebrow">Expert</p>
              <button type="button" role="menuitem" class="menu-item" @click="menuAction(() => startDialog?.open())"><AppIcon name="agent" /><span>Start agent manually…<small>Pick ticket, host, harness, account, model and thinking yourself. Same checks apply.</small></span></button>
            </template>
          </template>
          <!-- Pause all, Resume all and Wind down live in the Wind down popover; History in the Sessions head (AEON-780). -->
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
    <div class="layout">
      <div class="main-col">
        <DecisionDeskPanel v-if="session.identity?.principal.kind === 'person'" />
        <AttachPending ref="attachPending" :now="agents.now" @rows="rows => attachRows = rows" @history="rows => attachHistory = rows" @updated="requests => attachDialog?.sync(requests)" v-slot="{ rows }">
          <AgentChores v-if="capacity.signins.length || rows.length || attachHistory.length" :signins="capacity.signins" :attaches="rows" :history="attachHistory" @review="request => { attachRows = rows; reviewAttach(request) }" @dismiss="id => attachPending?.dismiss(id)" />
        </AttachPending>
        <AgentsWorking v-if="showSetup && session.identity?.principal.kind === 'person'" />
        <LeadsList v-if="agents.loaded" ref="leadsList" />
        <AccountsComputers v-if="showSetup" :permissions="pairingAccess" :show-accounts="showCapacity" />
        <p v-if="agents.approvalsHardError" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />Permission requests could not be loaded: {{ agents.approvalsError }} <button type="button" class="btn sm" @click="agents.refreshApprovals()">Try again</button></p>
        <SessionList
          v-if="agents.loaded" ref="sessionList"
          :groups="agents.grouped" :history="agents.historyViews" :history-state="agents.historyState" :history-more="agents.historyMore" :now="agents.now" :cursor="cursor" :selected="sessionId" :state="agents.sessionsUpdatedAt !== null ? 'ready' : agents.sessionsState" :error="agents.sessionsError"
          :loaded="agents.loaded" :controls="agents.controls" :can-start="manualStart" :can-lead="canStart" :filter="filter"
          @open="openSession" @control="control" @focus-row="id => cursor = id" @retry="agents.loadAll()" @start="startDialog?.open()" @history="agents.loadHistory(true)" @older="agents.loadOlderHistory()" @clear-filter="clearFilter"
        />
        <p v-if="agents.sessionsUpdatedAt !== null && agents.sessionsState === 'error'" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />Sessions could not be refreshed: {{ agents.sessionsError }} <button type="button" class="btn sm" @click="agents.loadAll()">Try again</button></p>
        <RunQueue v-if="agents.loaded" ref="runQueue" @emptied="pageTitle?.focus()" />
        <p v-if="agents.loaded && (agents.views.length || agents.pending.length)" class="hint" aria-hidden="true">
          <kbd class="keycap">j</kbd><kbd class="keycap">k</kbd> move · <KeyCap k="left" /><KeyCap k="right" /> fold · <kbd class="keycap"><AppIcon name="enter" /></kbd> open · <kbd class="keycap">p</kbd> pause · <kbd class="keycap">r</kbd> resume
        </p>
      </div>
    </div>

    <SessionPanel
      v-if="sessionId && agents.loaded && !ticketPeekOpen" :view="selected" :loading="!selected && (agents.historyState === 'loading' || (agents.historyState === 'ready' && agents.historyMore))" :now="agents.now" :can-write="writable" :control-block="controlBlock"
      @close="closePanel" @control="control" @review="review"
    />
    <p class="sr-only" role="status" aria-live="polite">{{ announcement }}</p>
    <ChangeTierPopover v-if="serviceTiers.dialog" :key="serviceTiers.dialog.instance" />
    <TierToast />
    <StartAgentDialog ref="startDialog" />
  </section>
</template>

<style scoped>
.agents-page { width: 100%; margin: 0; padding: 22px var(--gutter) 24px; }
/* One 48 px line: title, counts, then the controls at the right (AEON-780). */
.page-head { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 22px; min-height: 48px; margin-bottom: 14px; container: agents-head / inline-size; }
.page-head h1 { margin: 0; font-size: 30px; white-space: nowrap; }
.page-head .head-counts { flex: 0 1 auto; }
.head-side { display: flex; align-items: center; flex-wrap: nowrap; gap: 6px; margin-left: auto; flex: none; }
/* The page's one primary button, without the glow (base.css glows are retired here). */
.add-agent { width: 36px; height: 36px; min-height: 0; padding: 0; border-radius: 50%; }
.add-agent:not(:focus-visible) { box-shadow: 0 0 0 1px color-mix(in srgb, var(--primary) 50%, transparent), inset 0 1px 0 var(--glass-edge); }
/* Only a delay is said in the head; a dot and a word, details on hover. */
.freshness { display: inline-flex; align-items: center; gap: 7px; height: 30px; padding: 0 8px; color: var(--warn-ink); font-size: 12px; white-space: nowrap; }
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
