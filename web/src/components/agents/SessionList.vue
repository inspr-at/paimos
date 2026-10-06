<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { SessionControl } from '../../lib/agents'
import { reparentSession, undoRemoval } from '../../lib/agentRows'
import { movableWorker, moveTarget } from './sessionMove'
import { useAgents } from '../../stores/agents'
import { GROUPS, controlBlocked, elapsed, sessionForest, type SessionBranch, type SessionGroup } from '../../lib/agentState'
import { relativeTime } from '../../lib/work'
import type { Availability, SessionView } from '../../stores/agents'
import AppIcon from '../AppIcon.vue'
import TicketPeekLink from '../TicketPeekLink.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import ConnectHint from './ConnectHint.vue'
import SessionPauseActions from './SessionPauseActions.vue'
import { useAgentPause } from '../../stores/agentPause'
import { pausingSession } from '../../lib/agentPause'
import { isStale, useSessionRemoval } from './sessionRemoval'
import AgentStateLabel from './AgentStateLabel.vue'
import ListeningLabel from './ListeningLabel.vue'
import { useAgentAppearance } from '../../lib/agentAppearance'
const { appearance } = useAgentAppearance()
import AgentGlyph from './AgentGlyph.vue'
import { currentActivity, workerActivity } from './activity'
import HarnessBadge from './HarnessBadge.vue'
import ExecutionMark from './ExecutionMark.vue'
import SessionHost from './SessionHost.vue'
import { listHostLabels } from '../../lib/agents'
import { intendedResult, sessionContext, sessionExecution, sessionEtaEligible } from './sessionRow'
import EtaCell from '../work/EtaCell.vue'
import { etaFromSession } from '../../lib/eta'
import { brand } from '../../lib/brand'
import { toast } from '../../lib/toast'
import { useAgentRecovery } from '../../lib/agentRecovery'
import { quickRemoval, sessionMenu, type SessionMenu } from './sessionActions'
import { controlPermitted, type ControlGrant } from '../../lib/managedControl'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { DEFAULT_SORT, nextSort, orderForest, readSort, writeSort, type SessionSort, type SortKey } from './sessionOrder'
import TierCell from './TierCell.vue'
import FoldSection from './FoldSection.vue'
import { useSectionPrefs } from '../../stores/sectionPrefs'
import { below, shownBelow, treeRows, type TreeRow } from './sessionTree'
import { useServiceTiers } from '../../stores/serviceTiers'
import { TIER_NAME } from '../../lib/serviceTier'
const serviceTiers = useServiceTiers()

// Session families stay together across status groups. Each lead's history is
// opt-in for this mounted list only; refreshes never open it or persist it.
const props = defineProps<{
  history?: SessionView[]; historyState?: 'idle' | 'loading' | 'ready' | 'error'; historyMore?: boolean; groups: Record<SessionGroup, SessionView[]>; now: number; cursor: string; selected: string; state: Availability; error: string
  loaded: boolean; controls: Record<string, SessionControl>; canStart: boolean
}>()
const agentRecovery = useAgentRecovery()
const emit = defineEmits<{ open: [id: string]; control: [view: SessionView, kind: SessionControl['kind']]; focusRow: [id: string]; retry: []; start: []; history: []; older: [] }>()
const showStopped = ref(false)
// History is a separate, opt-in list of every ended or removed session. The
// main list keeps only sessions that ended in the last 24 hours (AEON-291).
const showRemoved = ref(false)
const removedCount = computed(() => props.history?.length ?? 0)
function toggleHistory() {
  showRemoved.value = !showRemoved.value
  if (showRemoved.value) emit('history')
}
const removal = useSessionRemoval()
const current = computed(() => GROUPS.flatMap(g => props.groups[g.id]))
const stale = computed(() => current.value.map(v => v.session).filter(s => isStale(s, props.now) && removal.canRemove(s)))
const lineageName = (id: string) => [...current.value, ...(props.history ?? [])].find(v => v.session.id === id)?.name || 'lead session'
const total = computed(() => GROUPS.reduce((sum, g) => sum + props.groups[g.id].length, 0))
type Branch = SessionBranch<SessionView>
// Families in the viewer's order (AEON-468): state, then start time, unless they chose a column.
const sort = ref<SessionSort | null>(null)
const forest = computed(() => orderForest(sessionForest(showRemoved.value ? props.history ?? [] : current.value, props.now), sort.value, props.now, showRemoved.value))
// Three calm buckets in urgency order: what needs a look, what runs, what ended.
// Each row still names its exact state; a family sits with its most urgent member.
type Bucket = 'attention' | 'live' | 'pausing' | 'paused' | 'stopped'
const BUCKETS: { id: Bucket; label: string }[] = [{ id: 'attention', label: 'Needs attention' }, { id: 'live', label: 'Live' }, { id: 'pausing', label: 'Pausing' }, { id: 'paused', label: 'Paused' }, { id: 'stopped', label: 'Ended' }]
const bucketOf = (group: SessionGroup): Bucket => group === 'stopped' ? 'stopped' : group === 'pausing' ? 'pausing' : group === 'paused' ? 'paused' : group === 'working' || group === 'idle' ? 'live' : 'attention'
const roots = (bucket: Bucket) => showRemoved.value
  ? (bucket === 'stopped' ? forest.value : [])
  : forest.value.filter(branch => bucketOf(branch.group) === bucket)
const expanded = ref<Record<string, boolean>>({})
const history = ref<Record<string, boolean>>({})
const containsSelected = (branch: Branch): boolean => branch.view.session.id === props.selected || branch.children.some(containsSelected)
// A direct link may reveal its selected row, but never its stopped siblings.
const candidates = (branch: Branch) => branch.children.filter(child => showRemoved.value || history.value[branch.view.session.id] || child.liveCount > 0 || child.view.status.state === 'paused' || containsSelected(child))
// Parents are open by default; a fold lasts for this visit (AEON-784).
const isExpanded = (branch: Branch): boolean => candidates(branch).length > 0 && (expanded.value[branch.view.session.id] ?? true)
// The fold button sits in its parent's row, so a click already moves the cursor there.
function toggle(branch: Branch) {
  const id = branch.view.session.id
  expanded.value[id] = !isExpanded(branch)
}
function toggleStopped(branch: Branch) {
  const id = branch.view.session.id
  history.value[id] = !history.value[id]
  expanded.value[id] = true
}
const stoppedChildren = (branch: Branch) => branch.children.reduce((sum, child) => sum + child.count - child.liveCount, 0)
const descendants = (branch: Branch): SessionView[] => branch.children.flatMap(child => [child.view, ...descendants(child)])
const activityLine = (branch: Branch) => branch.view.session.agent_activity_mode === 'off' ? '' :
  (branch.view.session.role === 'coordinator' && !branch.view.session.stopped_at ? workerActivity(descendants(branch), props.now) : '') || currentActivity(branch.view, props.now)
const workingChildren = (branch: Branch) => branch.children.reduce((sum, child) => sum + child.workingCount, 0)
const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`
type Row = TreeRow<SessionView> & { view: SessionView; primary: string; context: string; exec: ReturnType<typeof sessionExecution>; family: boolean; familyEnd: boolean; lead: boolean; sub: number; under: ReturnType<typeof below> }
// Sessions as a tree of any depth (AEON-784): the list decides which children a
// parent offers and whether it is open; sessionTree lays out the rows.
function visible(group: Bucket): Row[] {
  const shown = roots(group).filter(branch => group !== 'stopped' || showRemoved.value || showStopped.value || containsSelected(branch))
  const rows = treeRows(shown, { children: candidates, isOpen: isExpanded })
  return rows.map((row, index) => {
    const view = row.branch.view
    const primary = intendedResult(view)
    return {
      ...row, view, primary, context: sessionContext(view, primary), exec: sessionExecution(view),
      family: row.depth > 0 || row.open, familyEnd: !rows[index + 1]?.depth,
      lead: view.session.role === 'coordinator' || row.branch.children.length > 0,
      sub: row.foldable ? shownBelow(row.branch, candidates) : 0,
      under: below(row.branch, v => v.name),
    }
  })
}
const bucketRows = computed(() => Object.fromEntries(BUCKETS.map(b => [b.id, visible(b.id)])) as Record<Bucket, Row[]>)
const rowById = computed(() => new Map(BUCKETS.flatMap(b => bucketRows.value[b.id]).map(row => [row.view.session.id, row])))
const foldLabel = (row: Row) => `${row.open ? 'Fold' : 'Unfold'} ${row.view.name}: ${plural(row.sub, 'sub-agent')}, ${workingChildren(row.branch)} working`
const rollLabel = (row: Row) => row.under.problem ? `${row.under.problem} ${row.under.problem === 1 ? 'problem' : 'problems'} below` : `${row.under.ask} ${row.under.ask === 1 ? 'asks' : 'ask'} you below`

// Tree keys (AEON-784): on a row or its fold button, ← folds an open parent or
// goes to the parent; → unfolds a folded parent or goes to its first child.
// Other controls in the row (the tier cell) keep their own arrow keys.
const table = ref<HTMLElement>()
function focusSession(id: string) {
  void nextTick(() => {
    const el = table.value?.querySelector<HTMLElement>(`[data-row="s:${CSS.escape(id)}"]`)
    el?.focus({ preventScroll: true })
    el?.scrollIntoView({ block: 'nearest' })
  })
}
function treeKey(event: KeyboardEvent) {
  if ((event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return
  const target = event.target as HTMLElement
  const el = target.closest<HTMLElement>('[data-row]')
  if (!el || (target !== el && !target.classList.contains('tree-fold'))) return
  const row = rowById.value.get(el.dataset.row?.slice(2) ?? '')
  if (!row) return
  event.preventDefault()
  if (event.key === 'ArrowLeft') {
    if (row.open) toggle(row.branch)
    else if (row.parent) focusSession(row.parent.view.session.id)
  } else if (row.foldable && !row.open) toggle(row.branch)
  else if (row.open) {
    const first = candidates(row.branch)[0]
    if (first) focusSession(first.view.session.id)
  }
}
// Open the route's ancestors on navigation or when that session first arrives;
// subsequent ticks must not undo a person's explicit collapse.
const selectedPath = computed(() => {
  function find(branch: Branch): string[] | null {
    if (branch.view.session.id === props.selected) return [props.selected]
    for (const child of branch.children) {
      const path = find(child)
      if (path) return [branch.view.session.id, ...path]
    }
    return null
  }
  for (const branch of forest.value) {
    const path = find(branch)
    if (path) return path.join('/')
  }
  return ''
})
watch(selectedPath, path => {
  if (!path) return
  for (const id of path.split('/').slice(0, -1)) expanded.value[id] = true
  if (roots('stopped').some(containsSelected)) showStopped.value = true
}, { immediate: true })

// Control rights are per session: harness.control in that session's project.
const identity = useSession()
const grant = computed<ControlGrant>(() => ({ person: identity.identity?.principal.kind === 'person', can }))
// The chosen order is remembered per viewer in this browser; the page works without storage.
const viewer = computed(() => identity.identity ? `${identity.identity.tenant.id}.${identity.identity.principal.id}` : '')
watch(viewer, id => { sort.value = readSort(id) }, { immediate: true })
const hostLabels = ref(new Map<string, string>())
let hostRead = 0
watch(viewer, async () => {
  const read = ++hostRead
  hostLabels.value = new Map()
  if (!grant.value.person) return
  try {
    const labels = await listHostLabels()
    if (read === hostRead) hostLabels.value = new Map([...labels.map(item => [item.host, item.label] as const), ...hostLabels.value])
  } catch { /* The registered host remains the default; the editor retries its read. */ }
}, { immediate: true })
function renamedHost(host: string, label: string) {
  hostLabels.value = new Map([...hostLabels.value, [host, label]])
}
const COLUMNS: { key: SortKey; label: string; cls?: string }[] = [
  { key: 'state', label: 'State' }, { key: 'result', label: 'Name' }, { key: 'ticket', label: 'Ticket' },
  { key: 'execution', label: 'Execution', cls: 'c-exec' }, { key: 'host', label: 'Host', cls: 'c-host' }, { key: 'heartbeat', label: 'Heartbeat', cls: 'right c-beat' }, { key: 'running', label: 'Running', cls: 'right c-elapsed' },
]
const effectiveSort = computed(() => sort.value ?? DEFAULT_SORT)
const ariaSort = (key: SortKey) => effectiveSort.value.key === key ? (effectiveSort.value.dir === 'asc' ? 'ascending' : 'descending') : undefined
const HEARTBEAT_HELP = 'Beats under 3 minutes old count as equal; start time and id then decide their order.'
const sortTip = (column: { key: SortKey; label: string }) => column.key === 'heartbeat' ? `Sort by heartbeat. ${HEARTBEAT_HELP}` : !sort.value && column.key === 'state' ? 'Default order: state, then start time' : `Sort by ${column.label.toLowerCase()}`
// Headers are hidden on narrow lists, so a compact control carries the same keys and direction.
const sortable = computed(() => props.loaded && props.state === 'ready' && (showRemoved.value ? removedCount.value > 0 : total.value > 0))
const dirWord = computed(() => effectiveSort.value.dir === 'asc' ? 'ascending' : 'descending')
function pickSort(event: Event) {
  const key = (event.target as HTMLSelectElement).value as SortKey
  if (key !== effectiveSort.value.key) sortBy(key)
}
function sortBy(key: SortKey) {
  sort.value = nextSort(sort.value, key)
  writeSort(viewer.value, sort.value)
}
function resetSort() {
  sort.value = null
  writeSort(viewer.value, null)
}
const controlBlock = (view: SessionView, kind: SessionControl['kind']) => controlBlocked(view.session, kind, view.name, controlPermitted(view.session, grant.value), props.controls[view.session.id])
// A direct link to a session that already left the list shows it in History.
// Removing the selected session here never flips the list.
let seenCurrent = ''
watch([() => props.selected, () => props.history?.length, () => current.value.length], ([id]) => {
  if (!id) return
  if (current.value.some(v => v.session.id === id)) { seenCurrent = id; return }
  if (seenCurrent !== id && props.history?.some(v => v.session.id === id)) showRemoved.value = true
}, { immediate: true })

// A row offers only what works for its session (AEON-291). Ended and silent
// sessions get a bin right in the row: one click, then an undo toast. The
// overflow holds the rest; it is hidden when the bin already says it all.
const live = (view: SessionView) => view.session.phase !== 'stopped' && !view.session.archived_at
const menuOf = (view: SessionView): SessionMenu => sessionMenu(view, { grant: grant.value, canRemove: removal.canRemove(view.session), pending: props.controls[view.session.id], product: brand.value.short_name, now: props.now })
const bin = (view: SessionView) => removal.canRemove(view.session) && quickRemoval(view)
const hasMenu = (view: SessionView) => { const m = menuOf(view); return m.control.length > 0 || m.other.length > 0 || !!m.note || (m.remove && !bin(view)) }
// The bound ticket's estimate sits under its key while the session runs; an ended
// session no longer speaks for the ticket.
const etaOf = (view: SessionView) => sessionEtaEligible(view) ? etaFromSession(view.session) : null
const working = (view: SessionView) => sessionEtaEligible(view) && view.session.phase === 'working'
const hasEta = computed(() => current.value.some(view => !!etaOf(view) || working(view)))
const menu = ref<{ view: SessionView; anchor: HTMLElement } | null>(null)
const menuItems = computed(() => menu.value ? menuOf(menu.value.view) : null)
function openMenu(view: SessionView, event: MouseEvent) { menu.value = menu.value?.view.session.id === view.session.id ? null : { view, anchor: event.currentTarget as HTMLElement } }
function pickTier() {
  const selected = menu.value
  if (!selected) return
  menu.value = null
  serviceTiers.open(selected.view.session, selected.view.name, selected.anchor)
}
const tierRequestLabel = (view: SessionView) => view.session.service_tier_request ? `asks for ${TIER_NAME[view.session.service_tier_request]}` : ''
const agents = useAgents()
const pause = useAgentPause()
const moving = ref(false)
const dragged = ref<SessionView | null>(null)
const dropOver = ref('')
const moveMenu = ref<{ view: SessionView; anchor: HTMLElement } | null>(null)
const permittedWorker = (view: SessionView) => grant.value.person && can('harness.write', view.session.project_id) && movableWorker(view.session)
const targetFor = (worker: SessionView, lead: SessionView) => permittedWorker(worker) && moveTarget(worker.session, lead.session, current.value.map(v => v.session))
const leadsFor = (worker: SessionView) => current.value.filter(lead => targetFor(worker, lead))
function pickMove() { moveMenu.value = menu.value; menu.value = null }
function closeMove(restoreFocus: boolean) {
  if (restoreFocus) moveMenu.value?.anchor.focus()
  moveMenu.value = null
}
function menuKeys(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="menuitem"]')].filter(item => !(item as HTMLButtonElement).disabled)
  if (!items.length) return
  event.preventDefault(); event.stopPropagation()
  const current = items.indexOf(document.activeElement as HTMLElement)
  const index = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (current + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length
  items[index]?.focus()
}
async function move(worker: SessionView, lead: SessionView) {
  if (moving.value || !targetFor(worker, lead)) return
  moving.value = true
  moveMenu.value = null
  try {
    const result = await reparentSession(worker.session, lead.session)
    agents.recordSession(result.session)
    toast(`Moved ${worker.name} to ${lead.name}`, result.undoable ? { timeout: 8000, action: { label: 'Undo', run: () => void undoMove(result.event_id, worker.name) } } : {})
  } catch (error) { toast(error instanceof Error ? error.message : 'Move failed. Refresh and retry.', { tone: 'error' }) }
  finally { moving.value = false }
}
async function undoMove(event: number, label: string) {
  try {
    const restored = await undoRemoval(event)
    agents.recordSession(restored)
    toast(`Move of ${label} undone`)
  }
  catch (error) { toast(error instanceof Error ? error.message : 'Could not undo this move.', { tone: 'error' }) }
}
function startDrag(event: DragEvent, view: SessionView) {
  if (!permittedWorker(view) || moving.value || (event.target as HTMLElement).closest('button')) { event.preventDefault(); return }
  dragged.value = view
  if (event.dataTransfer) { event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData('text/plain', view.session.id) }
}
function dragOver(event: DragEvent, lead: SessionView) {
  if (!dragged.value || !targetFor(dragged.value, lead)) return
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
  dropOver.value = lead.session.id
}
function endDrag() { dragged.value = null; dropOver.value = '' }
function drop(event: DragEvent, lead: SessionView) {
  const worker = dragged.value
  endDrag()
  if (!worker || !targetFor(worker, lead)) return
  event.preventDefault()
  void move(worker, lead)
}
function pick(kind: SessionControl['kind']) {
  const view = menu.value?.view
  menu.value = null
  if (view && !controlBlock(view, kind)) emit('control', view, kind)
}
function pickRecovery() {
  const view = menu.value?.view
  menu.value = null
  if (view) void agentRecovery.request(view.session, view.name)
}
function pickRemove() {
  const view = menu.value?.view
  menu.value = null
  if (view) void removal.removeOne(view.session, view.name, quickRemoval(view))
}
function pickOpen() {
  const view = menu.value?.view
  menu.value = null
  if (view) emit('open', view.session.id)
}
async function pickCopy() {
  const view = menu.value?.view
  menu.value = null
  if (!view) return
  try { await navigator.clipboard.writeText(view.session.id); toast('Session id copied') } catch { toast('Copy failed. The id is in the session details.', { tone: 'error' }) }
}
function pendingLabel(view: SessionView) {
  const c = props.controls[view.session.id]
  if (!c || c.state === 'completed') return ''
  return c.kind === 'stop' ? (c.state === 'claimed' ? 'Stopping…' : 'Stop sent') : (c.state === 'claimed' ? 'Interrupting…' : 'Interrupt sent')
}
// The section folds per person (AEON-781/784). Folded, its head still says the
// state: how many run where, and what needs a look.
const sections = useSectionPrefs()
const sectionOpen = computed(() => sections.open.sessions)
const liveViews = computed(() => current.value.filter(view => live(view)))
const foldedSummary = computed(() => {
  if (!props.loaded || props.state !== 'ready') return props.state === 'error' ? 'Could not be loaded' : ''
  if (showRemoved.value) return removedCount.value ? plural(removedCount.value, 'ended session') : 'No ended sessions yet'
  const n = liveViews.value.length
  if (!n) return 'None running'
  const hosts = new Set(liveViews.value.map(view => view.session.host).filter(Boolean)).size
  const problems = liveViews.value.filter(view => view.status.group === 'problem' || view.status.group === 'unresponsive').length
  const asks = liveViews.value.filter(view => view.status.group === 'needs').length
  return [`${n} live${hosts ? ` on ${plural(hosts, 'computer')}` : ''}`, problems ? plural(problems, 'problem') : '', asks ? `${asks} ${asks === 1 ? 'asks' : 'ask'} you` : ''].filter(Boolean).join(' · ')
})
const sortMenu = ref<HTMLElement | null>(null)
function rowClick(event: MouseEvent, id: string) {
  if ((event.target as HTMLElement).closest('a, button')) return
  emit('open', id)
}
defineExpose({ toggleHistory })
</script>

<template>
  <FoldSection class="sessions" label="Sessions" :open="sectionOpen" :tip="sectionOpen ? 'Fold sessions' : 'Unfold sessions'" @toggle="sections.toggle('sessions')">
    <template #title><span id="sessions-title">{{ showRemoved ? 'History' : 'Sessions' }}</span><span v-if="loaded && state === 'ready'" class="fs-count">{{ showRemoved ? removedCount : total }}</span></template>
    <template #head>
      <span v-if="!sectionOpen && foldedSummary" class="fs-sum">{{ foldedSummary }}</span>
      <span v-if="sectionOpen && loaded && state === 'ready'" class="head-tools">
        <button v-if="sort" type="button" class="btn sm ghost quiet-btn" data-tip="Order by state, then start time" @click="resetSort">Default order</button>
        <button
          v-if="sortable" type="button" class="btn sm ghost quiet-btn sort-tool" aria-haspopup="dialog" :aria-expanded="!!sortMenu"
          :aria-label="`Sort sessions: ${COLUMNS.find(c => c.key === effectiveSort.key)?.label ?? ''}, ${dirWord}`" data-tip="Sort"
          @click="sortMenu = sortMenu ? null : ($event.currentTarget as HTMLElement)"
        ><AppIcon name="sort" :size="14" /></button>
        <button
          v-if="!showRemoved && stale.length" type="button" class="btn sm ghost quiet-btn" :disabled="removal.busy.value"
          :aria-label="`Clear stale: ${stale.length} without a heartbeat for 15 minutes`"
          :data-tip="`${stale.length} without a heartbeat for 15 minutes`" @click="removal.clearStale(stale)"
        ><AppIcon name="trash" :size="13" /><span class="tool-label">Clear stale</span></button>
        <button
          type="button" class="btn sm ghost quiet-btn" :aria-pressed="showRemoved"
          :aria-label="showRemoved ? 'Back to sessions' : 'Show history: every ended or removed session'" @click="toggleHistory"
        ><template v-if="showRemoved"><AppIcon name="arrow-left" :size="13" /><span class="tool-label">Sessions</span></template><template v-else><AppIcon name="history" :size="13" /><span class="tool-label">History</span></template></button>
      </span>
    </template>
    <span v-if="sortable" id="sort-beat-help" class="sr-only">{{ HEARTBEAT_HELP }}</span>

    <div v-if="state === 'forbidden'" class="state">
      <AppIcon name="agent" :size="20" />
      <h3>Sessions are visible to workspace members with agent access</h3>
      <p>Ask a workspace admin to give your account access to agent sessions.</p>
    </div>
    <div v-else-if="state === 'error'" class="state" role="alert">
      <AppIcon name="alert" :size="20" />
      <h3>Sessions could not be loaded</h3>
      <p>{{ error }}</p>
      <button type="button" class="btn" @click="emit('retry')"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <div v-else-if="!loaded" class="skeleton-rows" role="status" aria-label="Loading sessions">
      <div v-for="i in 5" :key="i" class="sk-row"><span class="skeleton dot" /><span class="skeleton" :style="{ width: `${18 + (i * 7) % 16}%` }" /><span class="skeleton key" /><span class="skeleton" style="width: 12%" /></div>
    </div>
    <p v-else-if="showRemoved && !removedCount" class="state" :role="historyState === 'error' ? 'alert' : undefined">{{ historyState === 'loading' ? 'Loading history…' : historyState === 'error' ? 'History could not be loaded.' : 'No ended sessions yet.' }}</p>
    <ConnectHint v-else-if="!total && !showRemoved" :can-start="canStart" @start="emit('start')" />

    <div v-else ref="table" class="table" :class="{ 'has-eta': hasEta }" role="treegrid" :aria-label="showRemoved ? 'Ended sessions' : 'Agent sessions'" @keydown="treeKey">
      <div class="thead" role="row">
        <template v-for="column in COLUMNS" :key="column.key">
        <span role="columnheader" :class="column.cls" :aria-sort="ariaSort(column.key)">
          <button type="button" class="th-sort" :class="{ on: sort?.key === column.key }" :data-tip="sortTip(column)" :aria-describedby="column.key === 'heartbeat' ? 'sort-beat-help' : undefined" @click="sortBy(column.key)">
            <span>{{ column.label }}</span>
            <span v-if="ariaSort(column.key)" class="sort-mark" aria-hidden="true"><AppIcon :name="effectiveSort.dir === 'asc' ? 'arrow-up' : 'arrow-down'" :size="11" :class="{ 'default-sort': !sort }" /></span>
          </button>
        </span>
        <span v-if="column.key === 'execution'" role="columnheader" class="c-tier"><span tabindex="0" data-tip="Service tier: vendor serving speed and price. One chevron per offered tier. Click to change; Left/Right arrow: one step.">Tier</span></span>
        </template>
        <span role="columnheader"><span class="sr-only">Actions</span></span>
      </div>
      <template v-for="group in BUCKETS" :key="group.id">
        <div v-if="roots(group.id).length" class="group-row" :class="group.id" role="row">
          <span role="rowheader" class="group-label">
            <template v-if="showRemoved">Ended<span class="mono">{{ roots(group.id).length }}</span></template>
            <button v-else-if="group.id === 'stopped'" type="button" class="group-toggle" :aria-expanded="showStopped" @click="showStopped = !showStopped">
              <AppIcon name="chevron-right" :size="12" class="chev" :class="{ turned: showStopped }" />{{ group.label }}<span class="mono">{{ roots(group.id).length }}</span>
            </button>
            <template v-else>{{ group.label }}<span class="mono">{{ roots(group.id).length }}</span></template>
          </span>
        </div>
        <div
          v-for="row in bucketRows[group.id]" :key="row.view.session.id" class="row agent-state-surface" :data-state="row.view.status.state" role="row"
          :aria-level="row.depth + 1" :aria-posinset="row.posinset" :aria-setsize="row.setsize" :aria-expanded="row.foldable ? row.open : undefined" :aria-selected="selected === row.view.session.id"
          :data-row="`s:${row.view.session.id}`" :data-parent="row.view.session.parent_harness_session_id || undefined" :data-depth="row.depth" :style="{ '--depth': row.depth, ...appearance(row.view.status.state) }" tabindex="-1" @focusin="emit('focusRow', `s:${row.view.session.id}`)"
          :draggable="permittedWorker(row.view) && !moving"
          :data-drop-target="dragged && targetFor(dragged, row.view) ? 'true' : undefined"
          @dragstart="startDrag($event, row.view)" @dragend="endDrag" @dragover="dragOver($event, row.view)" @dragleave="dropOver = ''" @drop="drop($event, row.view)"
          :class="[row.view.status.group, { 'drop-over': dropOver === row.view.session.id, worker: row.depth > 0, family: row.family, 'family-start': row.family && !row.depth, 'family-end': row.family && row.familyEnd, active: cursor === `s:${row.view.session.id}`, selected: selected === row.view.session.id, 'context-only': row.contextOnly }]" @click="rowClick($event, row.view.session.id)"
        >
          <span v-if="row.depth || row.open" class="tree-lines" aria-hidden="true">
            <span v-for="(continues, level) in row.guides" :key="level" class="tree-guide" :class="{ continues, elbow: level === row.depth - 1, last: level === row.depth - 1 && !continues }" :style="{ '--level': level }" />
            <span v-if="row.open" class="tree-stem" :style="{ '--level': row.depth }" />
          </span>
          <span role="gridcell" class="c-state" :class="{ 'vendor-limit': row.view.session.vendor_limited }">
            <AgentStateLabel :state="row.view.status.state" :label="row.view.status.label" :detail="row.view.session.archived_at ? 'Removed' : tierRequestLabel(row.view) || pendingLabel(row.view)" />
            <!-- No inbox (AEON-282) is the more specific cue; otherwise the listening cue (AEON-280). -->
            <span v-if="live(row.view) && row.view.session.management_mode === 'managed' && row.view.session.run_id && !row.view.session.advertised_capabilities.includes('inbox')" class="no-inbox" title="This session has no inbox delivery path. Launch a managed worker to receive follow-up messages.">No inbox</span>
            <ListeningLabel v-else class="state-listen" :session="row.view.session" :now="now" compact />
          </span>
          <span role="gridcell" class="c-agent">
            <span v-if="row.depth" class="sr-only">Worker of {{ row.parent?.view.name }}. </span>
            <button
              v-if="row.foldable" type="button" class="tree-fold" tabindex="-1" :aria-expanded="row.open" :aria-label="foldLabel(row)"
              :data-tip="row.open ? 'Fold · ←' : 'Unfold · →'" @click="toggle(row.branch)"
            ><AppIcon name="chevron-right" :size="14" class="chev" :class="{ turned: row.open }" /></button>
            <span v-else class="tree-fold-space" aria-hidden="true" />
            <RouterLink class="agent-link" :to="`/agents/${row.view.session.id}`" :aria-label="`${row.view.harness} ${row.view.name}, ${row.view.status.label}${row.lead ? ', lead' : ''}${row.under.problem ? `, ${rollLabel(row)}` : ''}. ${row.primary}. ${row.context}`">
              <span class="bot">
                <AgentGlyph :view="row.view" :size="30" />
                <HarnessBadge :harness="row.view.session.harness" />
                <i v-if="row.under.problem" class="roll-dot" data-tip="A problem below" />
              </span>
              <span class="who">
                <span class="result" :title="row.primary">{{ row.primary }}</span>
                <span class="session-context">
                  <span class="session-name" :title="row.context">{{ row.context }}</span>
                  <span v-if="row.lead" class="role" data-tip="Coordinates other sessions">Lead</span>
                  <time v-if="row.view.session.heartbeat_at" class="ctx-beat" :datetime="row.view.session.heartbeat_at">{{ relativeTime(row.view.session.heartbeat_at, { now }) }}</time>
                </span>
                <span v-if="activityLine(row.branch)" class="current-activity" :title="activityLine(row.branch)">{{ activityLine(row.branch) }}</span>
              </span>
            </RouterLink>
            <span v-if="row.view.session.stopped_at && row.view.session.handed_over_to_id" class="lineage">
              <RouterLink :to="`/agents/${row.view.session.handed_over_to_id}`" :title="`Handed over to ${lineageName(row.view.session.handed_over_to_id)}`">Handed over to {{ lineageName(row.view.session.handed_over_to_id) }}</RouterLink>
              <button v-if="stoppedChildren(row.branch)" type="button" class="worker-toggle history-toggle" :aria-expanded="!!history[row.view.session.id]" :aria-label="`Show stopped workers of ${row.view.name}`" @click="toggleStopped(row.branch)">{{ stoppedChildren(row.branch) }} stopped</button>
            </span>
            <span v-else-if="(row.foldable && !row.open) || stoppedChildren(row.branch)" class="worker-tools">
              <template v-if="row.foldable && !row.open">
                <span class="kid-count">{{ plural(row.sub, 'sub-agent') }}</span>
                <span v-if="row.under.problem || row.under.ask" class="roll" :class="row.under.problem ? 'problem' : 'ask'" :title="(row.under.problem ? row.under.names.problem : row.under.names.ask).join(', ')">{{ rollLabel(row) }}</span>
              </template>
              <button
                v-if="stoppedChildren(row.branch)" type="button" class="worker-toggle history-toggle" :aria-expanded="!!history[row.view.session.id]"
                :aria-label="`${history[row.view.session.id] ? 'Hide' : 'Show'} stopped workers of ${row.view.name}: ${stoppedChildren(row.branch)} stopped`" @click="toggleStopped(row.branch)"
              >{{ stoppedChildren(row.branch) }} stopped</button>
            </span>
            <RouterLink v-if="row.view.session.adopted_from_id" class="lineage adopted" :to="`/agents/${row.view.session.adopted_from_id}`" :title="`Adopted from ${lineageName(row.view.session.adopted_from_id)}`">Adopted from {{ lineageName(row.view.session.adopted_from_id) }}</RouterLink>
          </span>
          <span role="gridcell" class="c-ticket">
            <TicketPeekLink v-if="row.view.ticket" class="ticket-chip" :ticket-key="row.view.ticket.key" :href="row.view.ticket.href" :tip="row.view.ticket.title">{{ row.view.ticket.key }}</TicketPeekLink>
            <span v-else class="faint">{{ row.view.projectKey || '—' }}</span>
            <EtaCell v-if="etaOf(row.view) || working(row.view)" class="row-eta" align="start" :eta="etaOf(row.view)" :now="now" :missing="working(row.view)" />
          </span>
          <span class="execution-host" role="presentation">
            <span role="gridcell" class="c-exec" :aria-label="[row.exec.model ? row.exec.providerLabel : '', row.exec.modelLine, row.exec.accountLine].filter(Boolean).join('. ')">
              <span class="exec-icon"><ExecutionMark :kind="row.exec.kind" :provider="row.exec.provider" /></span>
              <span class="exec-copy">
                <span v-if="row.exec.model" class="exec-model" :title="row.exec.modelLine">{{ row.exec.modelLine }}</span>
                <span class="exec-account" :title="row.exec.accountLine"><span v-if="row.view.harness" class="exec-harness">{{ row.exec.kind === 'ai' ? row.view.harness : row.exec.accountLine }}</span><span v-if="row.exec.account" class="exec-acct"><template v-if="row.view.harness"> · </template>{{ row.exec.account }}</span></span>
              </span>
              <TierCell class="phone-tier" :session="row.view.session" :name="row.view.name" phone />
            </span>
            <span role="gridcell" class="c-tier"><TierCell :session="row.view.session" :name="row.view.name" /></span>
            <span role="gridcell" class="c-host">
              <SessionHost :key="`${viewer}:${row.view.session.host}`" :host="row.view.session.host" :label="hostLabels.get(row.view.session.host)" :editable="grant.person" @renamed="renamedHost(row.view.session.host, $event)" />
            </span>
          </span>
          <span role="gridcell" class="right c-beat">
            <time v-if="row.view.session.heartbeat_at" :datetime="row.view.session.heartbeat_at">{{ relativeTime(row.view.session.heartbeat_at, { now }) }}</time>
            <span v-else class="faint">never</span>
            <ListeningLabel class="beat-listen" :session="row.view.session" :now="now" compact />
          </span>
          <span role="gridcell" class="right c-elapsed mono-cell">{{ elapsed(row.view.session, now) }}</span>
          <span role="gridcell" class="c-actions">
            <SessionPauseActions :session="row.view.session" compact />
            <button
              v-if="bin(row.view)" type="button" class="icon-btn sm flat bin" :aria-label="`Remove ${row.view.name}`" data-tip="Remove · undo right after"
              :disabled="removal.busy.value" @click="removal.removeOne(row.view.session, row.view.name, true)"
            ><AppIcon name="trash" :size="16" /></button>
            <button
              v-if="hasMenu(row.view)" type="button" class="icon-btn sm flat more" :aria-label="`Actions for ${row.view.name}`" aria-haspopup="menu"
              :aria-expanded="menu?.view.session.id === row.view.session.id" @click="openMenu(row.view, $event)"
            ><AppIcon name="more" :size="16" /></button>
          </span>
        </div>
      </template>
    </div>
    <div v-if="showRemoved && loaded && state === 'ready' && (historyMore || (historyState === 'loading' && removedCount))" class="older">
      <button type="button" class="btn sm ghost quiet-btn" :disabled="historyState === 'loading'" @click="emit('older')">{{ historyState === 'loading' ? 'Loading…' : 'Show older' }}</button>
    </div>
    <FloatingPanel v-if="menu && menuItems" :anchor="menu.anchor" align="end" :width="248" :label="`Actions for ${menu.view.name}`" @close="menu = null">
      <div role="menu" :aria-label="`Actions for ${menu.view.name}`" @keydown="menuKeys">
        <button v-if="permittedWorker(menu.view) && leadsFor(menu.view).length" type="button" role="menuitem" class="menu-item" data-autofocus @click="pickMove">
          <AppIcon name="arrow" :size="16" /><span class="mi-text">Move to lead…</span>
        </button>
        <button v-if="agentRecovery.action(menu.view.session)" type="button" role="menuitem" class="menu-item" :disabled="agentRecovery.busy[menu.view.session.id]" @click="pickRecovery"><AppIcon name="refresh" :size="16" /><span class="mi-text">{{ agentRecovery.action(menu.view.session) === 'restart' ? 'Restart' : 'Reconnect' }}</span></button>
        <button v-if="pause.eligible(menu.view.session, 'pause') && (menu.view.session.supported_pause_levels?.includes('pause') || menu.view.session.advertised_capabilities.includes('inbox') || menu.view.session.advertised_capabilities.includes('pause'))" type="button" role="menuitem" class="menu-item" @click="pause.open('pause', [menu.view.session], menu.anchor); menu = null"><AppIcon name="pause" /><span class="mi-text">Pause…</span></button>

        <button v-if="pause.eligible(menu.view.session, 'resume')" type="button" role="menuitem" class="menu-item" @click="pause.open('resume', [menu.view.session], menu.anchor); menu = null"><AppIcon name="play" /><span class="mi-text">Resume</span></button>
        <template v-if="menuItems.control.length">
          <button v-if="menuItems.control.includes('interrupt') && !pausingSession(menu.view.session)" type="button" role="menuitem" class="menu-item" data-autofocus @click="pick('interrupt')">
            <AppIcon name="interrupt" :size="16" /><span class="mi-text"><span>Interrupt this step</span><small>No handover; stays for your next message.</small></span>
          </button>
          <button v-if="menuItems.control.includes('settings')" type="button" role="menuitem" class="menu-item" @click="pickOpen">
            <AppIcon name="edit" :size="16" /><span class="mi-text"><span>Name, model, effort</span></span>
          </button>
          <hr class="menu-sep">
        </template>
        <button v-if="!serviceTiers.unavailable(menu.view.session) || serviceTiers.canAsk(menu.view.session)" type="button" role="menuitem" class="menu-item" :disabled="!!serviceTiers.state(menu.view.session).pending || serviceTiers.busy[menu.view.session.id]" @click="pickTier"><AppIcon name="gauge" :size="16" /><span class="mi-text">{{ serviceTiers.canAsk(menu.view.session) ? 'Ask for a tier…' : 'Change tier…' }}</span></button>
        <RouterLink v-if="menuItems.other.includes('ticket') && menu.view.ticket" role="menuitem" class="menu-item" :to="menu.view.ticket.href" :data-autofocus="menuItems.control.length ? undefined : ''" @click="menu = null">
          <AppIcon name="link" :size="16" /><span class="mi-text"><span>Open {{ menu.view.ticket.key }}</span></span>
        </RouterLink>
        <button type="button" role="menuitem" class="menu-item" :data-autofocus="menuItems.control.length || menu.view.ticket ? undefined : ''" @click="pickCopy">
          <AppIcon name="copy" :size="16" /><span class="mi-text"><span>Copy session id</span></span>
        </button>
        <hr v-if="pause.eligible(menu.view.session, 'stop') || menuItems.remove" class="menu-sep">
        <button v-if="pause.eligible(menu.view.session, 'stop')" type="button" role="menuitem" class="menu-item danger" @click="pause.open('stop', [menu.view.session], menu.anchor); menu = null"><AppIcon name="halt" /><span class="mi-text">Stop now…</span></button>
        <template v-if="menuItems.remove">
          <button type="button" role="menuitem" class="menu-item" @click="pickRemove">
            <AppIcon name="trash" :size="16" /><span class="mi-text"><span>{{ quickRemoval(menu.view) ? 'Remove' : 'Remove…' }}</span></span>
          </button>
        </template>
        <p v-if="menu.view.session.agent_recovery" class="menu-note">{{ menu.view.session.agent_recovery.detail }}</p>
        <p v-else-if="menuItems.note" class="menu-note">{{ menuItems.note }}</p>
      </div>
    </FloatingPanel>
    <FloatingPanel v-if="sortMenu" :anchor="sortMenu" align="end" :width="280" label="Sort sessions" cycle @close="sortMenu = null">
      <div class="sort-pop" role="group" aria-label="Sort sessions">
        <label class="sort-pick">
          <span class="sort-label">Sort</span>
          <select class="sort-select" :value="effectiveSort.key" aria-describedby="sort-beat-help" data-autofocus @change="pickSort">
            <option v-for="column in COLUMNS" :key="column.key" :value="column.key">{{ column.label }}</option>
          </select>
        </label>
        <button
          type="button" class="icon-btn sort-dir" :aria-label="`Order ${dirWord}; switch to ${effectiveSort.dir === 'asc' ? 'descending' : 'ascending'}`"
          :data-tip="`Order ${dirWord}`" @click="sortBy(effectiveSort.key)"
        ><AppIcon :name="effectiveSort.dir === 'asc' ? 'arrow-up' : 'arrow-down'" :size="14" /></button>
        <p v-if="effectiveSort.key === 'heartbeat'" class="sort-note">{{ HEARTBEAT_HELP }}</p>
      </div>
    </FloatingPanel>
    <FloatingPanel v-if="moveMenu" :anchor="moveMenu.anchor" align="end" :width="248" :label="`Move ${moveMenu.view.name} to lead`" @close="closeMove">
      <div role="menu" :aria-label="`Move ${moveMenu.view.name} to lead`" @keydown="menuKeys">
        <p class="menu-note">Move to lead</p>
        <button v-for="lead in leadsFor(moveMenu.view)" :key="lead.session.id" type="button" role="menuitem" class="menu-item move-lead" :title="lead.name" @click="move(moveMenu.view, lead)">
          <AppIcon name="arrow" :size="16" /><span class="mi-text">{{ lead.name }}</span>
        </button>
      </div>
    </FloatingPanel>
  </FoldSection>
</template>

<style scoped>
.c-tier { display: flex; align-items: center; justify-content: center; align-self: stretch; }
.phone-tier { display: none; }
.row.drop-over { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--ink-2); }
.move-lead .mi-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lineage { flex-basis: 100%; display: flex; align-items: center; gap: 8px; min-width: 0; padding: 2px 0 3px 70px; font-size: 11px; color: var(--ink-2); }
.lineage a, .lineage.adopted { color: inherit; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lineage a:hover, .lineage.adopted:hover { text-decoration: underline; }
.lineage .history-toggle { flex-shrink: 0; }
.sessions { overflow: clip; container: sessions / inline-size; }
/* The section head (FoldSection): title and count, the folded state, then quiet
   tools at the line end, so a tool that appears moves nothing before it. */
.fs-count { font: 500 12px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.fs-sum { min-width: 0; font-size: 13px; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.head-tools { display: inline-flex; align-items: center; gap: 2px; margin-left: auto; margin-right: -4px; }
.tool-label { white-space: nowrap; }
.sort-tool { display: none; }
.quiet-btn { color: var(--ink-2); font-weight: 550; }
.quiet-btn:hover { color: var(--ink); }
.quiet-btn[aria-pressed="true"] { background: transparent; box-shadow: none; color: var(--ink-2); }
.quiet-btn[aria-pressed="true"]:hover { background: var(--row-selected); color: var(--ink); }
.quiet-btn .count { margin-left: 2px; font: 500 11.5px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.table { --state-width: 164px; --tree-step: 28px; display: grid; grid-template-columns: var(--state-width) minmax(140px, 1.45fr) minmax(72px, .48fr) minmax(128px, .82fr) 80px 132px 80px 80px 108px; padding: 0 0 8px; }
.thead, .row, .group-row { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: center; column-gap: 0; }
.thead { height: 32px; padding: 0 12px; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; white-space: nowrap; }
.thead > span, .row > span, .execution-host > span { padding: 0 8px; min-width: 0; }
.execution-host { display: contents; }
.right { text-align: right; justify-content: flex-end; }
/* Each header orders the families by its column; the mark shows key and direction.
   Labels never clip: right-aligned headers grow leftwards into the free end of the
   column beside them. :where keeps the container queries able to hide the cell. */
.th-sort { display: inline-flex; flex: none; align-items: center; gap: 6px; height: 26px; margin: 0 -6px; padding: 0 6px; border: 0; border-radius: 6px; background: transparent; font: inherit; letter-spacing: inherit; text-transform: inherit; color: inherit; white-space: nowrap; }
:where(.thead > .right) { display: flex; }
.thead > .right .th-sort { flex-direction: row-reverse; }
.th-sort:hover { color: var(--ink); background: var(--row-hover); }
.th-sort.on { color: var(--teal-ink); }
.th-sort:focus-visible { box-shadow: var(--focus-ring); }
.sort-mark { display: inline-grid; place-items: center; flex: none; width: 11px; height: 11px; }
.default-sort { opacity: .55; }
/* Where headers (or their Running and Heartbeat columns) are hidden, the head's sort tool carries the same keys and direction. */
.sort-pop { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 8px; }
.sort-pick { display: inline-flex; align-items: center; gap: 8px; }
.sort-label { font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.sort-select { height: 34px; padding: 0 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--field-bg); font: inherit; font-size: 13px; color: var(--ink); }
.sort-select:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.sort-dir { width: 34px; height: 34px; }
.sort-note { flex-basis: 100%; font-size: 12px; color: var(--ink-3); }
@media (max-width: 720px) { .sort-select { height: 44px; font-size: 16px; } .sort-dir { width: 44px; height: 44px; } }
.group-row { margin: 10px 6px 2px; padding: 0 12px; }
.group-label { grid-column: 1 / -1; display: inline-flex; align-items: center; gap: 8px; height: 26px; font: 500 10.5px/1 var(--mono); letter-spacing: .16em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.group-row.attention .group-label { color: var(--gold-ink); }
.group-label .mono { letter-spacing: 0; color: var(--ink-3); }
.group-toggle { display: inline-flex; align-items: center; gap: 8px; height: 26px; margin-left: -6px; padding: 0 8px 0 6px; border: 0; border-radius: 8px; background: transparent; font: inherit; letter-spacing: inherit; text-transform: inherit; color: inherit; }
.group-toggle:hover { background: var(--row-hover); color: var(--ink); }
.group-toggle:focus-visible { box-shadow: var(--focus-ring); }
.chev.turned { transform: rotate(90deg); }
/* Two label lines keep the glyph centre near the tree joint. Phones add padding. */
.row { --tree-joint: 22px; position: relative; min-height: 48px; margin: 0 6px; padding: 0 4px; border-radius: 10px; outline: none; cursor: pointer; font-size: 13px; }
.row.family { border-radius: 0; }
.row.family-start { border-radius: 10px 10px 0 0; }
.row.family-end { border-radius: 0 0 10px 10px; }
@media (hover: hover) { .row:hover { background: var(--row-hover); } }
.row.active { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.row.selected { background: var(--row-selected); }
.row { transition: background-color .3s ease, color .3s ease; }
.row.stopped { color: var(--ink-2); }
.row.stopped .agent-name { font-weight: 450; color: var(--ink-2); }
.row.worker .c-agent { padding-left: calc(8px + var(--depth) * var(--tree-step)); }
/* The track hangs from each parent's fold button (AEON-784). Each visible
   descendant carries its ancestors' tracks across row boundaries; the elbow
   ends at the child's own fold slot, and the last child closes its track. */
.row > .tree-lines { position: absolute; inset: 0 0 0 calc(var(--state-width) + 14px); padding: 0; pointer-events: none; color: var(--ink-3); }
.tree-guide, .tree-stem { position: absolute; left: calc(var(--level) * var(--tree-step)); top: 0; bottom: 0; width: var(--tree-step); }
.tree-guide.continues::before, .tree-guide.elbow::before { content: ''; position: absolute; top: 0; bottom: 0; width: 1px; background: currentColor; }
.tree-guide.last::before { bottom: auto; height: calc(var(--tree-joint) - 4px); }
.tree-guide.elbow::after { content: ''; position: absolute; top: calc(var(--tree-joint) - 4px); left: 0; width: calc(var(--tree-step) - 15px); height: 5px; border: solid currentColor; border-width: 0 0 1px 1px; border-radius: 0 0 0 5px; }
.tree-stem { top: calc(var(--tree-joint) + 9px); bottom: 0; width: 1px; background: currentColor; }
/* Every row keeps the fold slot, so names stay on one edge when a parent appears. */
.tree-fold, .tree-fold-space { flex: none; width: 24px; height: 24px; }
.tree-fold { display: grid; place-items: center; padding: 0; border: 0; border-radius: 6px; background: transparent; color: var(--ink-3); cursor: pointer; }
@media (hover: hover) { .tree-fold:hover { background: var(--row-hover); color: var(--teal-ink); } }
.tree-fold:focus-visible { outline: none; box-shadow: var(--focus-ring); }
/* Every ancestor of a problem carries a small dot on its mark. */
.roll-dot { position: absolute; top: -1px; right: -1px; width: 8px; height: 8px; border-radius: 50%; background: var(--danger); box-shadow: 0 0 0 1.5px var(--surface-raised); }
/* A folded parent says what is under it; a problem or ask below in its tone. */
.kid-count { padding-inline: 4px 2px; color: var(--ink-2); font-weight: 550; white-space: nowrap; }
.roll { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; font-weight: 600; white-space: nowrap; }
.roll.problem { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.roll.ask { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); }
.worker-tools { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 6px; flex-basis: 100%; min-height: 28px; padding: 0 0 6px 70px; color: var(--ink-2); font-size: 11.5px; }
.worker-toggle { display: inline-flex; align-items: center; justify-content: center; gap: 4px; min-height: 28px; padding: 2px 6px; border: 0; border-radius: 6px; background: transparent; color: var(--ink); font: inherit; font-weight: 550; white-space: nowrap; }
.worker-toggle:hover:not(:disabled) { background: var(--row-hover); }
.worker-toggle:disabled { cursor: default; }
.worker-toggle:focus-visible { box-shadow: var(--focus-ring); }
.history-toggle { color: var(--ink-2); font-weight: 450; }
.history-toggle[aria-expanded="true"] { background: var(--row-hover); color: var(--ink); }
.chev { transition: transform .2s ease; }
@media (prefers-reduced-motion: reduce) { .row, .chev { transition: none; } }
/* A filter's context rows (ancestors of a match) stay present and quiet. */
.row.context-only > :not(.c-agent), .row.context-only .who { opacity: .55; }
.c-state { display: inline-flex; align-items: center; gap: 9px; min-width: 0; }
.c-state:has(.no-inbox) { flex-direction: column; align-items: flex-start; justify-content: center; gap: 3px; }
.no-inbox { color: var(--ink-3); font-size: 11px; white-space: nowrap; }
.c-state :deep(.state-word) { white-space: nowrap; }
.c-state.vendor-limit { max-width: 148px; }
.c-state.vendor-limit :deep(.agent-state-label) { min-width: 0; max-width: 100%; align-items: flex-start; }
.c-state.vendor-limit :deep(.state-word) { min-width: 0; white-space: normal; overflow-wrap: anywhere; }

.state-label { font-size: 12.5px; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.row.needs .state-label { color: var(--gold-ink); font-weight: 600; }
.row > .c-agent { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; min-width: 0; padding-block: 6px; }
.agent-link { display: inline-flex; flex: 1 1 0; align-items: center; gap: 8px; min-width: 0; max-width: 100%; color: var(--ink); text-decoration: none; }
.agent-link:focus-visible { box-shadow: var(--focus-ring); border-radius: 6px; }
.bot { position: relative; display: inline-grid; width: 30px; height: 30px; flex: none; }
.who { display: grid; min-width: 0; line-height: 1.25; }
.result { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.session-context { display: flex; align-items: center; gap: 6px; min-width: 0; color: var(--ink-3); font-size: 12px; font-weight: 450; }
.current-activity { color: var(--ink-3); font-size: 11.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; }
.session-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* The heartbeat has its own column on wide rows; phones say it inline here. */
.ctx-beat { display: none; flex: none; white-space: nowrap; }
.row:hover .result { color: var(--teal-ink); }
.role { flex: none; height: 16px; padding: 0 5px; border-radius: 999px; background: var(--gold-wash); color: var(--gold-ink); font: 600 9px/16px var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.c-exec { display: inline-flex; align-items: center; gap: 8px; min-width: 0; }
/* Marks differ in width (Claude narrow, xAI wide): a fixed slot keeps every row's text on one left edge. */
.exec-icon { display: grid; place-items: center; flex: none; width: 28px; height: 28px; }
.exec-icon :deep(svg) { max-width: 28px; height: auto; max-height: 14px; }
.exec-copy { display: grid; min-width: 0; line-height: 1.25; }
.exec-model, .exec-account { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.exec-model { font-size: 12.5px; color: var(--ink); }
.exec-account { font-size: 11.5px; color: var(--ink-3); }
/* The estimate follows the key on its line and wraps below it only when the column is narrow. */
.row > .c-ticket { display: flex; flex-wrap: wrap; align-items: center; align-content: center; gap: 3px 8px; padding-block: 6px; }
.row-eta { font-size: 12px; }
.ticket-chip { display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 11.5px/1 var(--mono); text-decoration: none; font-variant-ligatures: none; white-space: nowrap; }
.ticket-chip:hover { filter: brightness(1.04); text-decoration: underline; }
.ticket-chip:focus-visible { box-shadow: var(--focus-ring); }
.row .c-beat { font-size: 12.5px; color: var(--ink-2); white-space: nowrap; }
/* Listening sits under the state word on wide rows and under the heartbeat on phones. */
.row .c-state:has(.state-listen) { display: grid; justify-items: start; gap: 3px; }
.state-listen { padding-left: 1px; }
.row .beat-listen { display: none; }
.row .c-elapsed { font-size: 12px; color: var(--ink-2); white-space: nowrap; font-variant-numeric: tabular-nums; }
.faint { color: var(--ink-3); }
.mono-cell { font-family: var(--mono); font-variant-ligatures: none; }
/* Controls appear on the row the pointer or keyboard is on; the layout never
   shifts. Reserve room for Resume, Remove and More, including the row inset. */
.row > .c-actions { display: inline-flex; align-items: center; justify-content: flex-end; gap: 2px; padding: 0 4px 0 0; }
/* The bin is always visible on an ended or silent row: it is that row's action. */
@media (hover: hover) { .row:not(:hover):not(:focus-within):not(.active):not(.selected) :deep(.pause-shortcut) { opacity: 0; } }
.act, .more { opacity: 0; transition: opacity .15s ease; }
.row:hover :is(.act, .more), .row.active :is(.act, .more), .row.selected :is(.act, .more), .row:focus-within :is(.act, .more), .more[aria-expanded="true"] { opacity: 1; }
.bin { color: var(--ink-3); }
.bin:hover:not(:disabled) { color: var(--ink); }
@media (hover: none) { .act { display: none; } .more { opacity: 1; } .c-actions .icon-btn:is(.more, .bin) { width: 36px; height: 36px; } }
@media (prefers-reduced-motion: reduce) { .act, .more { transition: none; } }
.menu-note { margin: 4px 6px 2px; padding: 8px 4px 2px; border-top: 1px solid var(--line); font-size: 11.5px; line-height: 1.4; color: var(--ink-3); }
.menu-sep { height: 1px; margin: 4px 6px; border: 0; background: var(--line); }
.menu-item { display: flex; align-items: flex-start; gap: 10px; box-sizing: border-box; text-decoration: none; width: 100%; padding: 8px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item > svg { margin-top: 2px; color: var(--ink-2); flex-shrink: 0; }
.menu-item:hover:not([aria-disabled="true"]) { background: var(--row-hover); }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.menu-item.danger:not([aria-disabled="true"]), .menu-item.danger:not([aria-disabled="true"]) > svg { color: var(--danger); }
.menu-item[aria-disabled="true"] { color: var(--ink-3); cursor: not-allowed; }
.mi-text { display: grid; gap: 2px; min-width: 0; }
.mi-text small { font-size: 11.5px; color: var(--ink-3); line-height: 1.35; }
.older { display: flex; justify-content: center; padding: 0 0 12px; }
@media (max-width: 720px) { .older .btn { min-height: 44px; } }
.state { display: grid; justify-items: center; gap: 8px; padding: 48px 24px 56px; text-align: center; color: var(--ink-2); border-top: 1px solid var(--line); }
.state > svg { color: var(--teal); margin-bottom: 4px; }
.state h3 { color: var(--ink); font-size: 16px; }
.state p { max-width: 52ch; font-size: 13.5px; }
.state .btn { margin-top: 8px; }
.skeleton-rows { display: grid; gap: 4px; padding: 8px 18px 16px; border-top: 1px solid var(--line); }
.sk-row { display: flex; align-items: center; gap: 18px; height: 40px; }
.sk-row .dot { width: 10px; height: 10px; border-radius: 50%; }
.sk-row .key { width: 70px; height: 20px; border-radius: 6px; }
/* Estimates need a ticket track wide enough for "overdue 5 min". */
/* Retain the pre-Host ticket allocation: the new column must not wrap ETA/%
   or increase existing row heights. The other flexible tracks absorb Host. */
.table.has-eta { grid-template-columns: var(--state-width) minmax(140px, 1.45fr) minmax(max(112px, calc((100% - var(--state-width) - 236px) * .48 / 2.75)), .48fr) minmax(128px, .82fr) 80px 132px 80px 80px 108px; }
@container sessions (max-width: 1000px) {
  .sort-tool { display: inline-flex; }
  .table { --state-width: 156px; grid-template-columns: var(--state-width) minmax(100px, 1.35fr) minmax(68px, .42fr) minmax(108px, .75fr) 80px 132px 64px 108px; }
  .table.has-eta { grid-template-columns: var(--state-width) minmax(100px, 1.35fr) minmax(104px, .42fr) minmax(108px, .75fr) 80px 132px 64px 108px; }
  .c-elapsed { display: none; }
}
/* The menu floats outside the list container: size its targets by viewport. */
@media (max-width: 720px) { .menu-item { min-height: 44px; align-items: center; } .menu-item > svg { margin-top: 0; } }
/* Narrow lists stack identity, execution and state. Tier uses its inline glyph;
   the wider breakpoint retains Host and the pause/remove/overflow reservation. */
@container sessions (max-width: 880px) {
  /* One text column beside the fold and avatar columns: title (two lines at
     most) with the heartbeat inline under it, one execution line, one state
     line, then what a folded lead holds. 16 px per tree level (AEON-784). */
  .table { --tree-step: 16px; --title-line: 18px; display: block; }
  .thead, .c-tier { display: none; }
  .phone-tier { display: inline-flex; }
  /* Default needs the same reachable control as paid tiers. Reserve its box
     through confirmation, price feedback and Undo so neighbours stay put. */
  .phone-tier :deep(.tier-mark) { width: 72px; }
  .phone-tier :deep(.price) { max-width: 44px; overflow: hidden; text-overflow: ellipsis; }
  .group-row { display: block; margin: 12px 8px 2px; padding: 0 8px; }
  .row { --tree-joint: 25px; display: grid; grid-template-columns: 44px 30px auto minmax(0, 1fr) 44px; grid-template-rows: auto auto auto auto; grid-template-areas: ". . . . actions" ". . . . actions" ". . . . actions" ". . . . actions"; column-gap: 8px; row-gap: 0; align-items: start; min-height: 0; margin: 0 6px; padding: 10px 0 10px calc(10px + var(--depth) * var(--tree-step)); }
  .row > span, .execution-host > span { padding: 0; }
  .row > .c-agent { grid-column: 1 / 5; grid-row: 1 / 5; display: grid; grid-template-columns: subgrid; grid-template-rows: subgrid; align-items: start; padding-block: 0; }
  .row.worker .c-agent { padding-left: 0; }
  /* The 44 px fold button is centred on the glyph's centre line. */
  .tree-fold, .tree-fold-space { grid-column: 1; grid-row: 1; width: 44px; height: 44px; margin-top: calc(var(--tree-joint) - 10px - 22px); }
  .agent-link { grid-column: 2 / -1; grid-row: 1; display: grid; grid-template-columns: subgrid; align-items: start; }
  .who { grid-column: 2 / -1; line-height: 1.3; }
  .result { font-size: 14px; line-height: var(--title-line); white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; }
  .session-context { margin-top: 2px; }
  .ctx-beat { display: inline; }
  .ctx-beat::before { content: '·'; margin-right: 6px; }
  /* Lines run beside the fold buttons: 16 px per level, the elbow ends at the child's fold slot. */
  .row > .tree-lines { left: 18px; }
  .tree-guide.elbow::after { width: calc(var(--tree-step) - 8px); }
  .tree-stem { top: calc(var(--tree-joint) + 22px); }
  .row:has(.lineage) { grid-template-rows: auto auto auto auto auto; }
  .row:has(.lineage) > .c-agent { grid-row: 1 / 6; }
  .lineage { grid-column: 2 / -1; grid-row: 4; padding: 4px 0 0; min-height: 24px; }
  .worker-tools ~ .lineage { grid-row: 5; }
  .worker-tools { grid-column: 2 / -1; grid-row: 4; flex-wrap: nowrap; white-space: nowrap; min-height: 44px; margin: 0 0 0 -4px; padding: 0; }
  /* The 44 px line closes a lead's row by itself. */
  .row:has(> .c-agent > .worker-tools) { padding-bottom: 0; }
  .worker-toggle { min-height: 44px; padding-inline: 6px; }
  /* Reserve every available action on the title line. A silent session can
     offer Pause, Remove and Actions together; Host owns the line below. */
  .row:has(> .c-actions > :is(.bin, .pause-controls:not(:empty))):has(> .c-actions > .more) { grid-template-columns: 44px 30px auto minmax(0, 1fr) max-content; }
  .row > .c-actions:has(> :is(.bin, .pause-controls:not(:empty))):has(> .more) { flex-direction: row; }
  /* Execution and Host share the whole line below the actions; a short host
     returns its spare width to the model/command. */
  /* The lines below the title start under the glyph: the fold column keeps the tree. */
  .execution-host { display: grid; grid-column: 2 / -1; grid-row: 2; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 8px; margin-top: 6px; }
  /* Keep the train's Default tier control without consuming main's command
     text slot beside Host. Its reserved line also keeps tier feedback still. */
  .c-exec { display: grid; grid-template-columns: 18px minmax(0, 1fr); min-width: 0; gap: 6px; }
  .c-exec > .phone-tier { grid-column: 1 / -1; grid-row: 2; }
  .exec-icon { width: 18px; height: 18px; place-items: center; }
  .exec-icon :deep(svg) { max-width: 18px; max-height: 13px; }
  .execution-host > .c-host { display: flex; justify-self: end; padding: 0; }
  .c-host :deep(.host-badge) { max-width: 104px; }
  /* One line: harness · model · effort. The account stays in the tooltip and the detail panel. */
  .exec-copy { flex: 1; display: flex; align-items: baseline; min-width: 0; font-size: 12px; color: var(--ink-2); }
  .exec-account { display: contents; }
  .exec-acct { display: none; }
  .exec-harness { order: -1; flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--ink-2); }
  .exec-model { flex: 0 1 auto; min-width: 0; font-size: 12px; }
  .exec-harness ~ .exec-model::before, .exec-copy:has(.exec-harness) .exec-model::before { content: '·'; margin: 0 .4em; color: var(--ink-3); }
  .c-state { grid-column: 2 / 4; grid-row: 3; min-width: 0; margin-top: 4px; min-height: 22px; }
  .c-state :deep(.agent-state-label) { min-width: 0; max-width: 100%; align-items: flex-start; }
  .c-state :deep(.state-word) { min-width: 0; white-space: normal; overflow-wrap: anywhere; }
  .c-ticket { grid-column: 4; grid-row: 3; justify-self: start; min-width: 0; overflow: hidden; margin-top: 4px; min-height: 22px; }
  .row > .c-ticket { padding-block: 0; }
  .c-beat { display: none; }
  /* AEON-280 x AEON-304: no beat cell on phones, so the listening cue joins the state line as
     its icon; the words stay in its accessible name and tooltip. */
  .row .c-state:has(.state-listen) { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .row .state-listen :deep(.listening-text) { display: none; }
  .c-elapsed { display: none; }
  .row > .c-actions { grid-area: actions; grid-row: 1 / span 3; align-self: center; flex-direction: column; justify-content: center; gap: 0; padding: 0; }
  .act { display: none; }
  .c-actions .icon-btn:is(.more, .bin) { width: 44px; height: 44px; opacity: 1; }
}
/* AEON-304: the overflow sits top-right, its icon centred on the title's first line. */
@container sessions (max-width: 880px) {
  .row > .c-actions { align-self: start; margin-top: calc(var(--title-line) / 2 - 22px); }
  /* Phone tools: 44 px icon buttons; their names stay for screen readers. */
  .head-tools .btn { min-width: 44px; min-height: 44px; justify-content: center; }
  .head-tools .tool-label { display: none; }
}
/* A very narrow list cannot fit both Terminal and its command beside Host.
   Grow the execution copy downward to leave room for command text. */
@container sessions (max-width: 320px) {
  .c-exec:has([data-run-kind="terminal"]) .exec-copy { display: grid; }
  .c-exec:has([data-run-kind="terminal"]) .exec-model::before { content: none; }
}
</style>
