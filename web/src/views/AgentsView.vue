<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { tierEvidenceRefresh, type AgentEventIdentity } from '../lib/tierEvidenceLive'
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { can, myPermissions, permissionsKnown } from '../lib/authz'
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
import RoutinesLine from '../components/routines/RoutinesLine.vue'
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
import { useAgentProjects } from '../components/agents/useAgentProjects'
import { DEFAULT_GROUP, emptyFacets, facetParam, FACET_KEYS, parseFacet, parseGroup, sameFacets, type Facets, type GroupBy } from '../components/agents/sessionGroups'
import { openLeadPanel, openStartLead } from '../lib/leadOverlay'
import { LEAD_WORDS } from '../lib/lead'
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
import { onAction, shortcutCaps, shortcutKeys, shortcutTyping, type ShortcutAction } from '../lib/shortcuts'
import { usePreference, preferenceSaves } from '../lib/preferences'
import type { PanelLayout } from '../lib/leadPanel'
import { isHolderTerminalTarget } from '../lib/nativeTerminal/ports'

// Markus's desk for agents (AEON-1209, design AEON-1196): the head line whose counts filter
// Sessions; the capacity row (the dial and the accounts chip) that scrolls with the page;
// what waits on him (only when something does); then the Sessions card, whose head carries
// the list's own tools and sticks inside the card. Sessions group by project, each lead
// heading its group (AEON-1210). A session opens in the docked panel.
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
// "Search sessions" filters this list only (the app header searches everything); ?q= carries it.
const query = ref(typeof route.query.q === 'string' ? route.query.q.slice(0, 200) : '')
watch(() => route.query.q, value => { const next = typeof value === 'string' ? value.slice(0, 200) : ''; if (next !== query.value) query.value = next })
function setQuery(value: string) {
  query.value = value
  if ((route.query.q ?? '') !== value) void router.replace({ query: { ...route.query, q: value || undefined } })
}
// Display › Group by and Filter › Project, Harness, Computer (AEON-1210): ?group=, ?project=,
// ?harness= and ?computer= carry them, read with bounds before any work.
const readFacets = (): Facets => ({ project: parseFacet(route.query.project), harness: parseFacet(route.query.harness), computer: parseFacet(route.query.computer) })
const facets = ref<Facets>(readFacets())
const group = ref<GroupBy>(parseGroup(route.query.group))
// The page's own address writes can overlap (quick Group by clicks); an older one landing late must not
// put back an older choice, so the address is read again only when no write of ours is on its way.
// Each write carries every list tool as it is now, so whichever write lands last is complete.
let writing = 0
async function writeQuery() {
  writing++
  const tools = { project: facetParam(facets.value.project), harness: facetParam(facets.value.harness), computer: facetParam(facets.value.computer), group: group.value === DEFAULT_GROUP ? undefined : group.value }
  try { await router.replace({ query: { ...route.query, ...tools } }) } finally { writing-- }
}
watch(() => [route.query.project, route.query.harness, route.query.computer, route.query.group], () => {
  if (writing) return
  const next = readFacets()
  if (!sameFacets(next, facets.value)) facets.value = next
  const grouped = parseGroup(route.query.group)
  if (grouped !== group.value) group.value = grouped
})
function setFacets(next: Facets) {
  const bounded = Object.fromEntries(FACET_KEYS.map(key => [key, parseFacet(facetParam(next[key]))])) as Facets
  facets.value = bounded
  void writeQuery()
}
function setGroup(next: GroupBy) {
  group.value = next
  void writeQuery()
}
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
// Each project's lead and queued work: the Sessions list heads its groups with them (AEON-1210).
const { view: projectLeads, withoutLead: leadless, mayStart: mayStartLead, launchAvailable: leadLaunchAvailable, launchReason } = useAgentProjects()
const manualStart = computed(() => canStart.value && showExpertStart.value)
const canStartLead = computed(() => mayStartLead.value && leadless.value.length > 0)
const leadlessNote = computed(() => !leadLaunchAvailable.value ? launchReason.value : leadless.value.length === 1 ? `${projects.byId(leadless.value[0]!)?.routeKey ?? 'One project'} has none. One per project.` : `${leadless.value.length} projects have none. One per project.`)
const showNew = computed(() => manualStart.value || canStartLead.value || canAttach.value || showConnect.value)
watch(() => `${session.identity?.tenant.id}/${session.identity?.principal.id}`, () => {
  headerMenu.value = null; filter.value = null
  if (query.value) setQuery('')
  if (FACET_KEYS.some(key => facets.value[key].length)) setFacets(emptyFacets())
  if (group.value !== DEFAULT_GROUP) setGroup(DEFAULT_GROUP)
})

const ticketPeek = inject(TICKET_PEEK, null)
const ticketPeekOpen = computed(() => !!ticketPeek?.openKey.value)
const sessionId = computed(() => typeof route.params.sessionId === 'string' ? route.params.sessionId : '')
const selected = computed(() => [...agents.views, ...agents.historyViews].find(v => v.session.id === sessionId.value))
// AEON-1196 r7: the handle is the list's width, never a global panel width.
const LIST_MIN = 280, DETAILS_MIN = 380, FORM_AT = 560, DIVIDER = 16, FOCUS_WIDTH = 320, OVERVIEW_DETAILS = 480
const layoutPref = usePreference<{ handle?: number; [key: string]: unknown }>('agents:layout')
const person = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
const work = ref<HTMLElement>(), divider = ref<HTMLElement>(), listPane = ref<HTMLElement>()
const workWidth = ref(0), paneHeight = ref(0)
const canSplit = computed(() => workWidth.value >= FORM_AT + DIVIDER + OVERVIEW_DETAILS)
const panelLayout = ref<PanelLayout>('side'), previousPanelLayout = ref<PanelLayout>('side')
const panelMedia = window.matchMedia('(max-width: 720px), (max-height: 480px)')
const smallPanel = ref(panelMedia.matches)
const syncPanelClass = () => { smallPanel.value = panelMedia.matches }
const largePanelPreference = usePreference<{ layout?: PanelLayout }>('agents:panel.layout.large')
const smallPanelPreference = usePreference<{ layout?: PanelLayout }>('agents:panel.layout.phone')
const panelPreference = computed(() => smallPanel.value ? smallPanelPreference : largePanelPreference)
const localPanelLayout = ref<PanelLayout | null>(null)
let panelLayoutWrite = 0
watch([person, smallPanel], async () => {
  const write = ++panelLayoutWrite, who = person.value
  localPanelLayout.value = null; panelLayout.value = 'side'; previousPanelLayout.value = 'side'
  await panelPreference.value.ready
  if (write !== panelLayoutWrite || who !== person.value) return
  const saved = panelPreference.value.value.value?.layout
  if (saved === 'side' || saved === 'wide' || saved === 'full') panelLayout.value = saved
}, { immediate: true })
async function setPanelLayout(next: PanelLayout, persist = true) {
  if (next === 'full') previousPanelLayout.value = panelLayout.value === 'full' ? previousPanelLayout.value : panelLayout.value
  else if (panelLayout.value === 'full' && next === 'side') next = previousPanelLayout.value
  const write = ++panelLayoutWrite, who = person.value, device = smallPanel.value, pref = panelPreference.value
  panelLayout.value = next; localPanelLayout.value = persist ? next : null
  drag = null; dragWidth.value = null
  await nextTick()
  if (next !== 'full') { ensureWorkTop(); placeRow() }
  if (!persist || !await pref.ensureLoaded()) return
  if (write !== panelLayoutWrite || who !== person.value || device !== smallPanel.value) return
  pref.save({ ...pref.value.value, layout: next }, 0)
  localPanelLayout.value = null
}
const panelOpen = computed(() => !!sessionId.value && agents.loaded && !ticketPeekOpen.value)
const split = computed(() => panelOpen.value && canSplit.value && panelLayout.value === 'side')
const panelOnly = computed(() => panelOpen.value && panelLayout.value === 'wide')
const maxHandle = computed(() => workWidth.value - DIVIDER - DETAILS_MIN)
const clampHandle = (width: number) => Math.round(Math.max(LIST_MIN, Math.min(maxHandle.value, width)))
const savedHandle = computed(() => {
  const width = layoutPref.value.value?.handle
  return typeof width === 'number' && Number.isFinite(width) ? width : workWidth.value - DIVIDER - OVERVIEW_DETAILS
})
const dragWidth = ref<number | null>(null)
const pendingHandle = ref<number | null>(null)
const handle = computed(() => dragWidth.value ?? clampHandle(pendingHandle.value ?? savedHandle.value))
const preset = computed(() => !split.value ? 'list' : handle.value < FORM_AT ? 'focus' : 'overview')
const layouts = { focus: ['Focus', 'sessions narrow, details wide'], overview: ['Overview', 'sessions wide, details narrow'], list: ['List only', 'no details'] } as const
const nextPreset = computed(() => ({ list: 'focus', focus: 'overview', overview: 'list' } as const)[preset.value])
const layoutKeys = computed(() => shortcutKeys('agents.layout.preset'))
const layoutTip = computed(() => [layouts[preset.value][0], `Next: ${layouts[nextPreset.value][0]}`, ...layoutKeys.value.flatMap(shortcutCaps).flat()].join(' · '))
const layoutEnabled = computed(() => canSplit.value && agents.loaded && !!(sessionId.value || agents.views.length || agents.historyViews.length) && !ticketPeekOpen.value)
const willClose = computed(() => dragWidth.value !== null && workWidth.value - DIVIDER - handle.value < DETAILS_MIN)
const layoutSaveFailed = computed(() => preferenceSaves.failed.has(`${person.value}/agents:layout`))
let lastSession = '', layoutResize: ResizeObserver | undefined, layoutFrame: number | undefined
let layoutWrite = 0
let rowAnchor: { id: string; top: number } | null = null
let drag: { pointer: number; x: number; width: number; person: string } | null = null
function rememberHandle(width: number) {
  const owner = person.value, write = ++layoutWrite, next = clampHandle(width)
  pendingHandle.value = next
  // Folded groups share this object. Keep the handle local through failed reads;
  // only a successful read (including an empty preference) permits merging.
  void layoutPref.ensureLoaded().then(loaded => {
    if (!loaded || owner !== person.value || write !== layoutWrite) return
    layoutPref.save({ ...(layoutPref.value.value ?? {}), handle: next })
    pendingHandle.value = null
  })
}
function ensureWorkTop() {
  const main = work.value?.closest('main')
  if (main && work.value) main.scrollTo({ top: main.scrollTop + work.value.getBoundingClientRect().top - main.getBoundingClientRect().top, behavior: 'instant' })
}
function placeRow() {
  if (!split.value) return
  const row = listPane.value?.querySelector<HTMLElement>(`[data-row="s:${CSS.escape(sessionId.value)}"]`)
  const body = listPane.value?.querySelector<HTMLElement>('.table')
  if (!row || !body) return
  const box = body.getBoundingClientRect(), rowBox = row.getBoundingClientRect()
  const top = rowAnchor?.id === sessionId.value && rowAnchor.top >= box.top && rowAnchor.top + rowBox.height <= box.bottom ? rowAnchor.top : Math.max(box.top, Math.min(box.bottom - rowBox.height, rowBox.top))
  body.scrollTop += rowBox.top - top
}
function finishLayout(event: TransitionEvent) {
  if (event.target === listPane.value && event.propertyName === 'flex-basis') placeRow()
}
async function cycleLayout() {
  if (!layoutEnabled.value) return false
  if (panelLayout.value !== 'side') void setPanelLayout('side')
  const owner = person.value
  const next = nextPreset.value
  if (next === 'list') { await closePanel(); return }
  rememberHandle(next === 'focus' ? FOCUS_WIDTH : workWidth.value - DIVIDER - OVERVIEW_DETAILS)
  if (!sessionId.value) {
    const visible = rows().filter(row => row.dataset.row?.startsWith('s:'))
    const row = visible.find(row => row.dataset.row === `s:${lastSession}`) ?? visible.find(row => row.dataset.row === cursor.value) ?? visible[0]
    if (row?.dataset.row) openSession(row.dataset.row.slice(2))
  }
  await nextTick()
  if (owner !== person.value || !split.value) return
  ensureWorkTop(); placeRow()
  announce(`Layout: ${layouts[next][0]}`)
}
function startDrag(event: PointerEvent) {
  if (event.button !== 0 || !split.value) return
  event.preventDefault()
  divider.value?.focus({ preventScroll: true })
  divider.value?.setPointerCapture(event.pointerId)
  drag = { pointer: event.pointerId, x: event.clientX, width: handle.value, person: person.value }
  dragWidth.value = handle.value
}
function moveDivider(event: PointerEvent) {
  if (!drag || drag.pointer !== event.pointerId || drag.person !== person.value || !split.value) return
  dragWidth.value = Math.round(Math.max(LIST_MIN, Math.min(workWidth.value - DIVIDER - 120, drag.width + event.clientX - drag.x)))
}
function endDrag(event: PointerEvent) {
  if (!drag || drag.pointer !== event.pointerId) return
  const current = drag, width = handle.value
  drag = null; dragWidth.value = null
  if (current.person !== person.value || event.type === 'pointercancel' || !split.value) return
  if (workWidth.value - DIVIDER - width < DETAILS_MIN) void closePanel()
  else { rememberHandle(width); announce(`List width ${clampHandle(width)} pixels`) }
}
function moveHandle(width: number) { rememberHandle(width); announce(`List width ${clampHandle(width)} pixels`) }
watch(person, (_next, previous) => {
  layoutWrite++; pendingHandle.value = null
  drag = null; dragWidth.value = null; rowAnchor = null; lastSession = ''; openedFromList = false; cursor.value = ''
  if (previous && sessionId.value) void router.replace({ path: '/agents', query: {} })
})
watch([sessionId, canSplit, () => agents.loaded], async ([id, fits, loaded], previous) => {
  if (!id || !fits || !loaded) return
  const owner = person.value
  await nextTick()
  if (id !== sessionId.value || owner !== person.value || !split.value) return
  if (!previous?.[0] || !previous[1] || !previous[2]) ensureWorkTop()
  placeRow()
}, { flush: 'post' })
// A link to a session that ended more than a day ago finds it in History,
// reading older pages until it appears or the server has no more.
watch([sessionId, () => agents.loaded, () => agents.historyState], ([id, loaded, state]) => {
  if (!id || !loaded || selected.value) return
  if (state === 'idle') void agents.loadHistory()
  else if (state === 'ready' && agents.historyMore) void agents.loadOlderHistory()
}, { immediate: true })
const writable = computed(() => can('harness.control'))
const showCapacity = computed(() => can('account.read'))

// ---------- Accounts and computers: one computer-first panel (AEON-499) ----------
// The capacity sources read independently of Sessions. Mount their reserved row
// once access is known so a pending accounts read can show its loading skeleton.
const showSetup = computed(() => permissionsKnown())
const pageTitle = ref<HTMLElement>()
const attachRows = ref<AttachQueueRow[]>([])
const attachHistory = ref<AttachQueueRow[]>([])
function reviewAttach(request: AttachReview) { attachDialog.value?.show(request, attachRows.value.map(row => row.review)) }

// Per session, like the server: harness.control in that session's project.
const controlBlock = (view: SessionView, kind: SessionControl['kind']) => controlBlocked(view.session, kind, view.name, controlPermitted(view.session, { person: session.identity?.principal.kind === 'person', can }), agents.controls[view.session.id])
async function control(view: SessionView, kind: SessionControl['kind']) {
  const who = `${session.identity?.tenant.id}:${session.identity?.principal.id}`, id = view.session.id
  if (kind === 'stop') {
    const ok = await confirmAction({
      title: `Stop ${view.name}?`, danger: true, confirmLabel: 'Stop session',
      body: `${view.harness} ends this session after its current step. It cannot be resumed; the next session starts fresh.`,
    })
    if (!ok) return
  }
  const current = agents.views.find(row => row.session.id === id)
  if (who !== `${session.identity?.tenant.id}:${session.identity?.principal.id}` || !current || controlBlock(current, kind)) return
  try {
    await agents.control(current, kind)
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
  const row = listPane.value?.querySelector<HTMLElement>(`[data-row="s:${CSS.escape(id)}"]`)
  rowAnchor = row ? { id, top: row.getBoundingClientRect().top } : null
  lastSession = id
  cursor.value = `s:${id}`
  if (sessionId.value) { void router.replace({ path: `/agents/${id}`, query: route.query }); return }
  openedFromList = true
  void router.push({ path: `/agents/${id}`, query: route.query })
}
// Focus returns to the session's row once the panel is gone.
async function closePanel() {
  const id = sessionId.value
  const owner = person.value
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
  // Returning focus must leave the preset dock under the pointer. Keyboard
  // navigation scrolls rows into view; closing details preserves page scroll.
  if (id && owner === person.value && !sessionId.value) focusRow(cursor.value, false)
}

// ---------- Keyboard: j/k move, Enter opens, Esc closes ----------
// A folded section's rows stay in the page but inert; the cursor skips them.
function rows() { return [...document.querySelectorAll<HTMLElement>('.agents-page [data-row]')].filter(el => !el.closest('[inert]')) }
// A key acts only on a row the list shows, or on the session open in the panel (AEON-1209).
const onScreen = (id: string) => (!!sessionId.value && id === `s:${sessionId.value}`) || rows().some(el => el.dataset.row === id)
// Search and filters take rows away; a cursor left on a hidden row moves to the first match, or to none.
watch([query, filter], () => {
  if (!cursor.value || onScreen(cursor.value)) return
  cursor.value = document.querySelector<HTMLElement>('.agents-page .sessions .row[data-row]:not(.filter-context)')?.dataset.row ?? ''
}, { flush: 'post' })
function focusRow(id: string, scroll = true) {
  const el = document.querySelector<HTMLElement>(`.agents-page [data-row="${CSS.escape(id)}"]`)
  el?.focus({ preventScroll: true })
  if (scroll) el?.scrollIntoView({ block: 'nearest' })
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
const pageActionScope = {
  enabled: () => !!session.identity,
  when: (event: KeyboardEvent) => !isHolderTerminalTarget(event.target) && !isHolderTerminalTarget(document.activeElement) && !(event.target instanceof Node && document.querySelector('.ticket-peek-host')?.contains(event.target)),
}
const actionScope = {
  ...pageActionScope,
  when: (event: KeyboardEvent) => {
    if (!pageActionScope.when(event)) return false
    // Local controls get first refusal through the bubble dispatcher; these
    // role guards also keep navigation inside controls such as the mode selector.
    return !(event.target instanceof HTMLElement && event.target.closest('[role="radiogroup"], [role="tablist"], [role="slider"], [role="spinbutton"], [role="listbox"], [role="separator"]'))
  },
}
function pauseSelected(action: 'pause' | 'resume') {
  const id = cursor.value.slice(2)
  const view = agents.views.find(view => view.session.id === id)
  if (!cursor.value.startsWith('s:') || !view || !onScreen(cursor.value) || !pause.eligible(view.session, action)) return false
  pause.open(action, [view.session])
}
const shortcutDisposers = [
  onAction('agents.search.focus', () => { if (!sessionList.value?.focusSearch()) return false }, pageActionScope),
  onAction('agents.filter.add', () => { if (!sessionList.value?.openFilter()) return false }, pageActionScope),
  onAction('agents.layout.preset', () => { void cycleLayout() }, { ...pageActionScope, enabled: () => layoutEnabled.value }),
  ...(['left', 'right', 'min', 'max'] as const).map(direction => onAction(`agents.divider.${direction}`, () => {
    moveHandle(direction === 'min' ? LIST_MIN : direction === 'max' ? maxHandle.value : handle.value + (direction === 'left' ? -16 : 16))
  }, { enabled: () => split.value, when: event => event.target === divider.value })),
  onAction('agents.row.next', () => { move(1) }, actionScope),
  onAction('agents.row.prev', () => { move(-1) }, actionScope),
  onAction('agents.row.open', event => {
    if (event.target instanceof HTMLElement && event.target.closest('a, button, summary')) return false
    if (!onScreen(cursor.value)) return false
    const [kind, id] = [cursor.value.slice(0, 1), cursor.value.slice(2)]
    if (kind === 's') openSession(id)
    // A lead without a session on the list opens its lead panel (AEON-1210).
    else if (kind === 'l') openLeadPanel(id, document.querySelector<HTMLElement>(`.agents-page [data-row="l:${CSS.escape(id)}"]`))
    else return false
  }, actionScope),
  onAction('agents.session.pause', () => pauseSelected('pause'), actionScope),
  onAction('agents.session.resume', () => pauseSelected('resume'), actionScope),
  onAction('agents.details.close', event => {
    if (ticketPeekOpen.value) return false
    if (shortcutTyping(event.target)) { (event.target as HTMLElement).blur(); (event.target as HTMLElement).closest<HTMLElement>('.session-panel')?.focus({ preventScroll: true }); return }
    if (sessionId.value && panelLayout.value === 'full') void setPanelLayout('side')
    else if (sessionId.value) void closePanel()
    else if (filter.value) filter.value = null
    else return false
  }, pageActionScope),
]
const hintGroups: { actions: ShortcutAction[]; label: string }[] = [
  { actions: ['agents.row.next', 'agents.row.prev'], label: 'move' },
  { actions: ['agents.group.fold', 'agents.group.unfold'], label: 'fold' },
  { actions: ['agents.row.open'], label: 'open' },
  { actions: ['agents.search.focus'], label: 'search' },
  { actions: ['agents.layout.preset'], label: 'layout' },
  { actions: ['agents.session.pause'], label: 'pause' },
  { actions: ['agents.session.resume'], label: 'resume' },
]
const activeHints = computed(() => hintGroups.map(group => ({ ...group, keys: group.actions.flatMap(id => shortcutKeys(id).slice(0, 1).flatMap(binding => shortcutCaps(binding).flat())) })).filter(group => group.keys.length))

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
  panelMedia.addEventListener('change', syncPanelClass)
  const main = work.value?.closest('main')
  const measureWidth = () => { workWidth.value = Math.floor(work.value?.getBoundingClientRect().width ?? 0) }
  const measureHeight = () => { paneHeight.value = Math.max(0, (main?.clientHeight ?? 0) - 8) }
  measureWidth(); measureHeight()
  layoutResize = new ResizeObserver(entries => {
    // Width only depends on the surrounding page. Apply its breakpoint before
    // paint; deferring it lets the old split move controls during interaction.
    measureWidth()
    // The main viewport's height is independent of the split. Its resize can
    // settle immediately too, without briefly opening a zero-height pane.
    if (entries.some(entry => entry.target === main)) measureHeight()
    // Applying a breakpoint changes the work area's height. Defer that read
    // so the height write cannot feed the same observer delivery.
    if (layoutFrame !== undefined) return
    layoutFrame = requestAnimationFrame(() => {
      layoutFrame = undefined
      measureHeight()
      if (!canSplit.value) { drag = null; dragWidth.value = null }
    })
  })
  if (work.value) layoutResize.observe(work.value)
  if (main) layoutResize.observe(main)
  stopTierWatch = serviceTiers.watchPage()
  document.addEventListener('visibilitychange', evidenceVisible)
  void agents.loadAll()
  void capacity.load()
  stop = subscribeAgents(changed, value => { live.value = value }, () => agents.deliveryChanged())
  poller.start()
  clock = setInterval(() => agents.tick(), 1000)
})
onBeforeUnmount(() => {
  panelLayoutWrite++; panelMedia.removeEventListener('change', syncPanelClass)
  layoutResize?.disconnect(); drag = null; dragWidth.value = null
  if (layoutFrame !== undefined) cancelAnimationFrame(layoutFrame)
  evidenceRefresh.stop(); document.removeEventListener('visibilitychange', evidenceVisible)
  stopTierWatch?.()
  stop?.(); poller.stop(); clearInterval(clock); clearTimeout(debounce)
  shortcutDisposers.forEach(dispose => dispose())
})
watch(sessionId, id => { if (id) { cursor.value = `s:${id}`; lastSession = id } }, { immediate: true })
</script>

<template>
  <section class="agents-page" :class="{ split, 'panel-only': panelOnly, dragging: dragWidth !== null, 'will-close': willClose }" :style="{ '--list-w': `${handle}px`, '--pane-h': `${paneHeight}px` }" aria-labelledby="agents-title">
    <!-- One line (AEON-780): title, the counts as filters, then the controls. -->
    <header class="page-head">
      <h1 id="agents-title" ref="pageTitle" tabindex="-1">Agents</h1>
      <HeadCounts :views="agents.views" :now="agents.now" :loaded="agents.loaded" :filter="filter" @filter="setFilter">
        <!-- No "Live" in the head; only a delay is worth saying, at the line end (AEON-785 moves freshness to the footer). -->
        <p v-if="stale" class="freshness" role="status" :data-tip="freshnessTip"><span class="live-mark" aria-hidden="true" />Update delayed</p>
      </HeadCounts>
      <div class="head-side">
        <!-- Wind down · More · New, New last (AEON-1209). Wind down is a ghost button, then a teal chip while it runs (AEON-783). -->
        <WindDownControl v-if="agents.loaded" ref="windDown" />
        <button type="button" class="icon-btn flat more-agent" aria-label="More agent actions" aria-haspopup="menu" :aria-expanded="headerMenu?.type === 'more'" @click="headerAction('more', $event)"><AppIcon name="more" /></button>
        <button v-if="showNew" type="button" class="btn primary add-agent" aria-label="New: start a lead, attach a session or connect a machine" aria-haspopup="menu" :aria-expanded="headerMenu?.type === 'add'" @click="headerAction('add', $event)"><AppIcon name="plus" :size="17" /></button>
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
          <!-- Pause all, Resume all and Wind down live in the Wind down popover; History in the Sessions head (AEON-780).
               The Decision Desk is a place in the app header, not an item here (AEON-1209, AEON-1139). -->
          <template v-else>
            <button role="menuitem" type="button" class="menu-item" aria-label="Model preferences" @click="menuAction(() => openModelPrefs())"><AppIcon name="gear" /><span>Model preferences<small>Which models do which work.</small></span></button>
            <RouterLink role="menuitem" class="menu-item" to="/agents/usage" @click="headerMenu = null"><AppIcon name="pulse" /><span>Usage<small>Tokens and cost per agent, account and day.</small></span></RouterLink>
            <RouterLink role="menuitem" class="menu-item" to="/agents/routines" @click="headerMenu = null"><AppIcon name="repeat" /><span>Routines<small>Recurring work agents do by themselves.</small></span></RouterLink>
            <RouterLink v-if="can('keys.manage')" role="menuitem" class="menu-item" to="/settings/access/agents" @click="headerMenu = null"><AppIcon name="key" /><span>Agent keys<small>Scoped API keys for agents.</small></span></RouterLink>
            <div class="menu-sep" role="separator" />
            <RouterLink role="menuitem" class="menu-item" to="/settings/personal#agents" @click="headerMenu = null"><AppIcon name="gear" /><span>Agent settings<small>Default pause level and indicators.</small></span></RouterLink>
          </template>
        </div>
      </FloatingPanel>
    </header>

    <!-- Capacity keeps its own loading slots; Sessions admits its first bundle
         after its metadata and permissions have settled. -->
    <div class="layout">
      <div class="main-col">
        <!-- The capacity row: what may run. The folded dial, then the accounts chip; it scrolls with the page. -->
        <div v-if="showSetup" class="cap-row" role="group" aria-label="Capacity: what may run">
          <AgentsWorking v-if="session.identity?.principal.kind === 'person'" body-target="#dial-body-host" />
          <AccountsComputers :permissions="pairingAccess" :show-accounts="showCapacity" />
        </div>
        <div id="dial-body-host" class="dial-host" />
        <!-- AEON-1192: one Routines line while a routine waits for a person or failed. -->
        <RoutinesLine v-if="agents.loaded && session.identity?.principal.kind === 'person'" />
        <AttachPending ref="attachPending" :now="agents.now" @rows="rows => attachRows = rows" @history="rows => attachHistory = rows" @updated="requests => attachDialog?.sync(requests)" v-slot="{ rows }">
          <AgentChores v-if="capacity.signins.length || rows.length || attachHistory.length" :signins="capacity.signins" :attaches="rows" :history="attachHistory" @review="request => { attachRows = rows; reviewAttach(request) }" @dismiss="id => attachPending?.dismiss(id)" />
        </AttachPending>
        <p v-if="agents.approvalsHardError" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />Permission requests could not be loaded: {{ agents.approvalsError }} <button type="button" class="btn sm" @click="agents.refreshApprovals()">Try again</button></p>
      </div>
    </div>
    <div ref="work" class="agents-work" :data-layout="preset">
      <!-- One sticky dock. Both heads reserve its box; it never changes parents. -->
      <div v-show="canSplit && !panelOpen" class="layout-dock">
        <button type="button" class="icon-btn raised layout-button" data-layout-cycle :data-layout="preset" :aria-disabled="!layoutEnabled" :aria-label="`Layout: ${layouts[preset][0]}, ${layouts[preset][1]}. Next: ${layouts[nextPreset][0]}`" :aria-keyshortcuts="layoutKeys.join(' ') || undefined" :data-tip="layoutTip" @click="cycleLayout"><AppIcon :name="`layout-${preset}`" :size="17" /></button>
      </div>
      <div class="agents-split">
        <div v-show="!panelOnly" ref="listPane" class="agents-list-pane" @transitionend="finishLayout">
        <SessionList
          v-if="agents.loaded" ref="sessionList"
          :groups="agents.grouped" :history="agents.historyViews" :history-state="agents.historyState" :history-more="agents.historyMore" :now="agents.now" :cursor="cursor" :selected="sessionId" :state="agents.sessionsUpdatedAt !== null ? 'ready' : agents.sessionsState" :error="agents.sessionsError"
          :loaded="agents.loaded" :controls="agents.controls" :can-start="manualStart" :can-lead="canStart" :filter="filter" :query="query" :layout-dock="canSplit && !split" :pane="split"
          :projects="projectLeads" :group="group" :facets="facets"
          @open="openSession" @control="control" @focus-row="id => cursor = id" @retry="agents.loadAll()" @start="startDialog?.open()" @history="agents.loadHistory(true)" @older="agents.loadOlderHistory()" @clear-filter="clearFilter"
          @filter="setFilter" @search="setQuery" @group="setGroup" @facets="setFacets"
        />
        <p v-if="agents.sessionsUpdatedAt !== null && agents.sessionsState === 'error'" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />Sessions could not be refreshed: {{ agents.sessionsError }} <button type="button" class="btn sm" @click="agents.loadAll()">Try again</button></p>
        <RunQueue v-if="agents.loaded" ref="runQueue" @emptied="pageTitle?.focus()" />
        <p v-if="agents.loaded && (agents.views.length || agents.pending.length) && !split" class="hint" aria-hidden="true">
          <template v-for="(hint, index) in activeHints" :key="hint.label"><span v-if="index"> · </span><KeyCap v-for="key in hint.keys" :key="key" :k="key" /> {{ hint.label }}</template>
        </p>
        </div>
        <div ref="divider" class="agents-divider" :role="split ? 'separator' : undefined" :aria-hidden="split ? undefined : true" :aria-orientation="split ? 'vertical' : undefined" :tabindex="split ? 0 : undefined" :aria-label="split ? 'Resize the session list' : undefined" :aria-valuemin="split ? LIST_MIN : undefined" :aria-valuemax="split ? maxHandle : undefined" :aria-valuenow="split ? handle : undefined" :aria-valuetext="split ? `List width ${handle} pixels` : undefined" :data-tip="split ? 'Drag to resize the session list' : undefined" @pointerdown="startDrag" @pointermove="moveDivider" @pointerup="endDrag" @pointercancel="endDrag"><span class="grip" /></div>
        <SessionPanel
          v-if="sessionId && agents.loaded && !ticketPeekOpen" :view="selected" :loading="!selected && (agents.historyState === 'loading' || (agents.historyState === 'ready' && agents.historyMore))" :now="agents.now" :can-write="writable" :control-block="controlBlock" :docked="split || panelOnly" :sheet="!canSplit" :layout="panelLayout" :sidebar-available="canSplit"
          @close="closePanel" @control="control" @review="review" @layout="setPanelLayout" @auto-layout="setPanelLayout($event, false)"
        />
      </div>
      <p v-if="panelOpen && (localPanelLayout || preferenceSaves.failed.has(`${person}/agents:panel.layout.${smallPanel ? 'phone' : 'large'}`))" class="layout-error" role="status">The panel layout lasts for this visit until it can be saved.</p>
      <p v-if="layoutSaveFailed" class="layout-error" role="status">The layout could not be saved. It lasts for this visit.</p>
    </div>
    <p class="sr-only" role="status" aria-live="polite">{{ announcement }}</p>
    <ChangeTierPopover v-if="serviceTiers.dialog" :key="serviceTiers.dialog.instance" />
    <TierToast />
    <StartAgentDialog ref="startDialog" />
  </section>
</template>

<style scoped>
.agents-page { --page-x: var(--gutter); width: 100%; min-width: 0; margin: 0; padding: 22px var(--page-x) 24px; }
/* One control height for the head, the capacity row and the Sessions card head (AEON-1209): 36 px, 44 on touch. */
.agents-page { --ctl-h: 36px; }
@media (pointer: coarse), (max-width: 720px) { .agents-page { --ctl-h: 44px; } }
/* One 48 px line: title, counts, then the controls at the right (AEON-780). */
.page-head { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 22px; min-height: 48px; margin-bottom: 14px; container: agents-head / inline-size; }
.page-head h1 { margin: 0; font-size: 30px; white-space: nowrap; }
.page-head .head-counts { flex: 0 1 auto; }
.head-side { display: flex; align-items: center; flex-wrap: nowrap; gap: 6px; margin-left: auto; flex: none; }
/* The page's one primary button, without the glow (base.css glows are retired here). */
.add-agent { width: var(--ctl-h); height: var(--ctl-h); min-height: 0; padding: 0; border-radius: 50%; }
.more-agent { width: var(--ctl-h); height: var(--ctl-h); }
.head-side :deep(.wd-btn) { height: var(--ctl-h); padding: 0 14px; font-size: 13px; }
.head-side :deep(.wd-chip) { height: var(--ctl-h); }
.page-head :deep(.head-counts .count) { height: var(--ctl-h); }
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
.main-col { display: flex; flex-direction: column; gap: var(--section-gap); min-width: 0; }
/* The capacity row (AEON-1209): what may run, left-aligned, scrolling with the page. Its width decides
   how the dial's units open (a container query in the dial). A tight row's plates float over the
   neighbours and may reach into the page's side padding, but never widen the page: the row clips at
   the page's edge (its padding and negative margin cancel out in layout). */
.cap-row { display: flex; align-items: center; flex-wrap: wrap; justify-content: flex-start; gap: 8px; min-height: var(--ctl-h); margin-inline: calc(-1 * var(--page-x)); padding-inline: var(--page-x); container: cap / inline-size; overflow-x: clip; }
/* The unfolded dial's body lands here, under the capacity row; empty, it takes no room. */
.dial-host:empty { display: none; }
.inline-error { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; padding: 10px 14px; border-radius: 12px; background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); font-size: 13px; color: var(--danger); }
.hint { display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 5px; padding: 4px 0; font-size: 12px; color: var(--ink-3); }
.hint .keycap + .keycap { margin-left: 2px; }
/* Geometry and dock match the approved round-7 HTML; the main scroller starts below the header. */
.agents-work { position: relative; margin-top: 14px; }
.layout-dock { position: sticky; top: 0; z-index: 25; height: 0; }
.layout-button { position: absolute; top: 15px; right: 11px; width: var(--ctl-h); height: var(--ctl-h); color: var(--ink-2); }
.layout-button:not([data-layout="list"]) { color: var(--teal-ink); }
.layout-button[aria-disabled="true"] { opacity: .5; }
.agents-split { display: flex; gap: 0; align-items: flex-start; min-width: 0; }
/* The saved list basis is a pixel width from the last wide layout. The viewport
   can shrink before the split class updates, and the basis then eases for 240ms.
   Cap the pane at the row and let the divider yield so that frame cannot widen main. */
.agents-list-pane { flex: 0 0 100%; min-width: 0; max-width: 100%; container: sessions / inline-size; transition: flex-basis 240ms ease; }
.split .agents-list-pane { flex-basis: var(--list-w); position: sticky; top: 0; }
.split, .panel-only { padding-bottom: 8px; }
/* The pane toolbar is already below the app header. Page-anchor clearance
   would scroll its focused controls away from the pane's top. */
:global(main:has(.agents-page.split)), :global(main:has(.agents-page.panel-only)) { scroll-padding-top: 0; }
.panel-only .agents-divider { display: none; }
.agents-divider { position: relative; flex: 0 0 0; align-self: stretch; transition: flex-basis 240ms ease; }
.split .agents-divider { flex-basis: 16px; flex-shrink: 1; min-width: 0; cursor: col-resize; touch-action: none; }
.split .agents-divider::before { content: ''; position: absolute; top: 8px; bottom: 8px; left: 7.5px; width: 1px; background: var(--line); }
.grip { display: none; }
.split .grip { position: sticky; top: 45vh; display: block; width: 6px; height: 36px; margin: 40vh auto 0; border-radius: 999px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); }
.agents-divider:is(:hover, :focus-visible) .grip, .dragging .grip { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.agents-divider:focus-visible { outline: none; }
.agents-divider:focus-visible::before { background: var(--teal); }
.dragging .agents-list-pane, .dragging .agents-divider { transition: none; }
.dragging, .dragging * { cursor: col-resize !important; user-select: none; }
.will-close :deep(.session-panel.docked) { opacity: .5; }
.layout-error { position: absolute; right: 0; top: 58px; z-index: 26; padding: 6px 10px; background: var(--surface-raised); color: var(--danger); font-size: 12px; }
@media (pointer: coarse) { .split .agents-divider::after { content: ''; position: absolute; inset: 0 -14px; } }
@media (prefers-reduced-motion: reduce) { .agents-list-pane, .agents-divider { transition: none; } }
@media (max-width: 720px) {
  .agents-page { --page-x: 12px; padding: 16px var(--page-x) 20px; }
  .hint { display: none; }
  /* Phones: title and controls on line 1 (44 px), the counts on line 2. */
  .page-head { gap: 2px 8px; }
  .page-head h1 { font-size: 26px; }
  .page-head .head-counts { order: 3; flex: 1 1 100%; }
  .add-agent, .more-agent { width: 44px; height: 44px; }
  .head-side :deep(.wd-btn) { width: 44px; padding: 0; }
  .header-menu .menu-item { min-height: 52px; }
  .freshness { height: 44px; }
}
</style>
