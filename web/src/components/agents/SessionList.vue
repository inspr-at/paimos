<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
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
import SessionEstimate from './SessionEstimate.vue'
import { etaFromSession } from '../../lib/eta'
import { brand } from '../../lib/brand'
import { toast } from '../../lib/toast'
import { quickRemoval, sessionMenu, type SessionMenu } from './sessionActions'
import { controlPermitted, type ControlGrant } from '../../lib/managedControl'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { DEFAULT_SORT, nextSort, orderForest, readSort, writeSort, type SessionSort, type SortKey } from './sessionOrder'
import TierCell from './TierCell.vue'
import { useServiceTiers } from '../../stores/serviceTiers'
import { TIER_NAME } from '../../lib/serviceTier'
const serviceTiers = useServiceTiers()

// Session families stay together across status groups. Each lead's history is
// opt-in for this mounted list only; refreshes never open it or persist it.
const props = defineProps<{
  history?: SessionView[]; historyState?: 'idle' | 'loading' | 'ready' | 'error'; historyMore?: boolean; groups: Record<SessionGroup, SessionView[]>; now: number; cursor: string; selected: string; state: Availability; error: string
  loaded: boolean; controls: Record<string, SessionControl>; canStart: boolean
}>()
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
// Count sessions, including folded descendants, rather than family roots.
const bucketCount = (bucket: Bucket) => roots(bucket).reduce((sum, branch) => sum + (showRemoved.value || bucket === 'stopped' ? branch.count : branch.liveCount), 0)
const expanded = ref<Record<string, boolean>>({})
const history = ref<Record<string, boolean>>({})
const containsSelected = (branch: Branch): boolean => branch.view.session.id === props.selected || branch.children.some(containsSelected)
// A direct link may reveal its selected row, but never its stopped siblings.
const candidates = (branch: Branch) => branch.children.filter(child => showRemoved.value || history.value[branch.view.session.id] || child.liveCount > 0 || child.view.status.state === 'paused' || containsSelected(child))
const isExpanded = (branch: Branch): boolean => candidates(branch).length > 0 && (expanded.value[branch.view.session.id] ?? true)
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
const otherChildren = (branch: Branch) => branch.children.reduce((sum, child) => sum + child.liveCount - child.workingCount, 0)
const workerLabel = (branch: Branch) => `${branch.count - 1} ${branch.count === 2 ? 'worker' : 'workers'}`
const visible = (group: Bucket) => {
  const out: { view: SessionView; branch: Branch; depth: number; parent: string; open: boolean; guides: boolean[]; family: boolean; familyEnd: boolean; primary: string; context: string; exec: ReturnType<typeof sessionExecution> }[] = []
  function walk(branch: Branch, guides: boolean[], parent = '') {
    const open = isExpanded(branch)
    const primary = intendedResult(branch.view)
    out.push({ view: branch.view, branch, depth: guides.length, parent, open, guides, family: guides.length > 0 || open, familyEnd: false, primary, context: sessionContext(branch.view, primary), exec: sessionExecution(branch.view) })
    if (open) {
      const children = candidates(branch)
      children.forEach((child, index) => walk(child, [...guides, index < children.length - 1], branch.view.name))
    }
  }
  for (const branch of roots(group)) {
    if (group !== 'stopped' || showRemoved.value || showStopped.value || containsSelected(branch)) {
      walk(branch, [])
      out[out.length - 1]!.familyEnd = true
    }
  }
  return out
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
watch(viewer, id => {
  sort.value = readSort(id)
  expanded.value = {}; history.value = {}; showStopped.value = false; showRemoved.value = false
}, { immediate: true })
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
function rowClick(event: MouseEvent, id: string) {
  if ((event.target as HTMLElement).closest('a, button')) return
  emit('open', id)
}
defineExpose({ toggleHistory })
</script>

<template>
  <section class="sessions glass-card" aria-labelledby="sessions-title">
    <header class="card-head">
      <h2 id="sessions-title">{{ showRemoved ? 'History' : 'Sessions' }}</h2>
      <span v-if="loaded && state === 'ready'" class="head-tools">
        <button v-if="sort" type="button" class="btn sm ghost quiet-btn" data-tip="Order by state, then start time" @click="resetSort">Default order</button>
        <button
          v-if="!showRemoved && stale.length" type="button" class="btn sm ghost quiet-btn" :disabled="removal.busy.value"
          :data-tip="`${stale.length} without a heartbeat for 15 minutes`" @click="removal.clearStale(stale)"
        >Clear stale</button>
        <button
          type="button" class="btn sm ghost quiet-btn" :aria-pressed="showRemoved"
          :aria-label="showRemoved ? 'Back to sessions' : 'Show history: every ended or removed session'" @click="toggleHistory"
        ><template v-if="showRemoved"><AppIcon name="arrow-left" :size="13" />Sessions</template><template v-else><AppIcon name="clock" :size="13" />History</template></button>
      </span>
    </header>

    <div v-if="sortable" class="sort-bar" role="group" aria-label="Sort sessions">
      <label class="sort-pick">
        <span class="sort-label">Sort</span>
        <select class="sort-select" :value="effectiveSort.key" aria-describedby="sort-beat-help" @change="pickSort">
          <option v-for="column in COLUMNS" :key="column.key" :value="column.key">{{ column.label }}</option>
        </select>
      </label>
      <button
        type="button" class="icon-btn sort-dir" :aria-label="`Order ${dirWord}; switch to ${effectiveSort.dir === 'asc' ? 'descending' : 'ascending'}`"
        :data-tip="`Order ${dirWord}`" @click="sortBy(effectiveSort.key)"
      ><AppIcon :name="effectiveSort.dir === 'asc' ? 'arrow-up' : 'arrow-down'" :size="14" /></button>
      <p v-if="effectiveSort.key === 'heartbeat'" class="sort-note">{{ HEARTBEAT_HELP }}</p>
    </div>
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

    <div v-else class="table" :class="{ 'has-eta': hasEta }" role="table" aria-label="Agent sessions">
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
            <template v-if="showRemoved">Ended<span class="mono">{{ bucketCount(group.id) }}</span></template>
            <button v-else-if="group.id === 'stopped'" type="button" class="group-toggle" :aria-expanded="showStopped" @click="showStopped = !showStopped">
              <AppIcon name="chevron-right" :size="12" class="chev" :class="{ turned: showStopped }" />{{ group.label }}<span class="mono">{{ bucketCount(group.id) }}</span>
            </button>
            <template v-else>{{ group.label }}<span class="mono" data-tip="Live sessions in these families, including collapsed workers">{{ bucketCount(group.id) }}</span></template>
          </span>
        </div>
        <div
          v-for="{ view, branch, depth, parent, open, guides, family, familyEnd, primary, context, exec } in visible(group.id)" :key="view.session.id" class="row agent-state-surface" :data-state="view.status.state" role="row" :data-row="`s:${view.session.id}`" :data-parent="view.session.parent_harness_session_id || undefined" :data-depth="depth" :style="{ '--depth': depth, ...appearance(view.status.state) }" tabindex="-1" @focusin="emit('focusRow', `s:${view.session.id}`)"
          :draggable="permittedWorker(view) && !moving"
          :data-drop-target="dragged && targetFor(dragged, view) ? 'true' : undefined"
          @dragstart="startDrag($event, view)" @dragend="endDrag" @dragover="dragOver($event, view)" @dragleave="dropOver = ''" @drop="drop($event, view)"
          :class="[view.status.group, { 'drop-over': dropOver === view.session.id, worker: depth > 0, family, 'family-start': family && !depth, 'family-end': family && familyEnd, active: cursor === `s:${view.session.id}`, selected: selected === view.session.id }]" @click="rowClick($event, view.session.id)"
        >
          <span v-if="depth || open" class="tree-lines" aria-hidden="true">
            <span v-for="(continues, level) in guides" :key="level" class="tree-guide" :class="{ continues, elbow: level === depth - 1, last: level === depth - 1 && !continues }" :style="{ '--level': level }" />
            <span v-if="open" class="tree-stem" :style="{ '--level': depth }" />
          </span>
          <span role="cell" class="c-state" :class="{ 'vendor-limit': view.session.vendor_limited }">
            <AgentStateLabel :state="view.status.state" :label="view.status.label" :detail="view.session.archived_at ? 'Removed' : tierRequestLabel(view) || pendingLabel(view)" />
            <!-- No inbox (AEON-282) is the more specific cue; otherwise the listening cue (AEON-280). -->
            <span v-if="live(view) && view.session.management_mode === 'managed' && view.session.run_id && !view.session.advertised_capabilities.includes('inbox')" class="no-inbox" title="This session has no inbox delivery path. Launch a managed worker to receive follow-up messages.">No inbox</span>
            <ListeningLabel v-else class="state-listen" :session="view.session" :now="now" compact />
          </span>
          <span role="cell" class="c-agent">
            <span v-if="depth" class="sr-only">Worker of {{ parent }}. </span>
            <RouterLink class="agent-link" :to="`/agents/${view.session.id}`" :aria-label="`${view.harness} ${view.name}, ${view.status.label}${view.session.role === 'coordinator' ? ', lead' : ''}. ${primary}. ${context}`">
              <span class="bot">
                <AgentGlyph :view="view" :size="30" />
                <HarnessBadge :harness="view.session.harness" />
              </span>
              <span class="who">
                <span class="result" :title="primary">{{ primary }}</span>
                <span class="session-context">
                  <span class="session-name" :title="context">{{ context }}</span>
                  <span v-if="view.session.role === 'coordinator'" class="role" data-tip="Coordinates other sessions">Lead</span>
                  <time v-if="view.session.heartbeat_at" class="ctx-beat" :datetime="view.session.heartbeat_at">{{ relativeTime(view.session.heartbeat_at, { now }) }}</time>
                </span>
                <span v-if="activityLine(branch)" class="current-activity" :title="activityLine(branch)">{{ activityLine(branch) }}</span>
              </span>
            </RouterLink>
            <span v-if="view.session.stopped_at && view.session.handed_over_to_id" class="lineage">
              <RouterLink :to="`/agents/${view.session.handed_over_to_id}`" :title="`Handed over to ${lineageName(view.session.handed_over_to_id)}`">Handed over to {{ lineageName(view.session.handed_over_to_id) }}</RouterLink>
              <button v-if="stoppedChildren(branch)" type="button" class="worker-toggle history-toggle" :aria-expanded="!!history[view.session.id]" :aria-label="`Show stopped workers of ${view.name}`" @click="toggleStopped(branch)">{{ stoppedChildren(branch) }} stopped</button>
            </span>
            <span v-else-if="branch.children.length" class="worker-tools">
              <button type="button" class="worker-toggle" :disabled="!candidates(branch).length" :aria-expanded="open" :aria-label="`${open ? 'Collapse' : 'Expand'} ${workerLabel(branch)} of ${view.name}: ${workingChildren(branch)} working`" @click="toggle(branch)">
                <AppIcon name="chevron-right" :size="12" class="chev" :class="{ turned: open }" />{{ workingChildren(branch) }} working
              </button>
              <template v-if="otherChildren(branch)"><span aria-hidden="true"> · </span><span class="idle-count">{{ otherChildren(branch) }} other active</span></template>
              <span aria-hidden="true"> · </span>
              <button type="button" class="worker-toggle history-toggle" :disabled="!stoppedChildren(branch)" :aria-expanded="!!history[view.session.id]" :aria-label="`${history[view.session.id] ? 'Hide' : 'Show'} stopped workers of ${view.name}: ${stoppedChildren(branch)} stopped`" @click="toggleStopped(branch)">{{ stoppedChildren(branch) }} stopped</button>
            </span>
            <RouterLink v-if="view.session.adopted_from_id" class="lineage adopted" :to="`/agents/${view.session.adopted_from_id}`" :title="`Adopted from ${lineageName(view.session.adopted_from_id)}`">Adopted from {{ lineageName(view.session.adopted_from_id) }}</RouterLink>
          </span>
          <span role="cell" class="c-ticket">
            <TicketPeekLink v-if="view.ticket" class="ticket-chip" :ticket-key="view.ticket.key" :href="view.ticket.href" :tip="view.ticket.title">{{ view.ticket.key }}</TicketPeekLink>
            <span v-else class="faint">{{ view.projectKey || '—' }}</span>
            <SessionEstimate v-if="etaOf(view) || working(view)" class="row-eta" :eta="etaOf(view)" :now="now" :missing="working(view)" />
          </span>
          <span class="execution-host" role="presentation">
            <span role="cell" class="c-exec" :aria-label="[exec.model ? exec.providerLabel : '', exec.modelLine, exec.accountLine].filter(Boolean).join('. ')">
              <span class="exec-icon"><ExecutionMark :kind="exec.kind" :provider="exec.provider" /></span>
              <span class="exec-copy">
                <span v-if="exec.model" class="exec-model" :title="exec.modelLine">{{ exec.modelLine }}</span>
                <span class="exec-account" :title="exec.accountLine"><span v-if="view.harness" class="exec-harness">{{ exec.kind === 'ai' ? view.harness : exec.accountLine }}</span><span v-if="exec.account" class="exec-acct"><template v-if="view.harness"> · </template>{{ exec.account }}</span></span>
              </span>
              <TierCell class="phone-tier" :session="view.session" :name="view.name" phone />
            </span>
            <span role="cell" class="c-tier"><TierCell :session="view.session" :name="view.name" /></span>
            <span role="cell" class="c-host">
              <SessionHost :key="`${viewer}:${view.session.host}`" :host="view.session.host" :label="hostLabels.get(view.session.host)" :editable="grant.person" @renamed="renamedHost(view.session.host, $event)" />
            </span>
          </span>
          <span role="cell" class="right c-beat">
            <time v-if="view.session.heartbeat_at" :datetime="view.session.heartbeat_at">{{ relativeTime(view.session.heartbeat_at, { now }) }}</time>
            <span v-else class="faint">never</span>
            <ListeningLabel class="beat-listen" :session="view.session" :now="now" compact />
          </span>
          <span role="cell" class="right c-elapsed mono-cell">{{ elapsed(view.session, now) }}</span>
          <span role="cell" class="c-actions">
            <SessionPauseActions :session="view.session" compact />
            <button
              v-if="bin(view)" type="button" class="icon-btn sm flat bin" :aria-label="`Remove ${view.name}`" data-tip="Remove · undo right after"
              :disabled="removal.busy.value" @click="removal.removeOne(view.session, view.name, true)"
            ><AppIcon name="trash" :size="16" /></button>
            <button
              v-if="hasMenu(view)" type="button" class="icon-btn sm flat more" :aria-label="`Actions for ${view.name}`" aria-haspopup="menu"
              :aria-expanded="menu?.view.session.id === view.session.id" @click="openMenu(view, $event)"
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
        <button v-if="pause.eligible(menu.view.session, 'pause') && (menu.view.session.supported_pause_levels?.includes('pause') || menu.view.session.advertised_capabilities.includes('inbox') || menu.view.session.advertised_capabilities.includes('pause'))" type="button" role="menuitem" class="menu-item" @click="pause.open('pause', [menu.view.session], menu.anchor); menu = null"><AppIcon name="pause" /><span class="mi-text">Pause…</span></button>
        <button v-if="pause.eligible(menu.view.session, 'stop')" type="button" role="menuitem" class="menu-item danger" @click="pause.open('stop', [menu.view.session], menu.anchor); menu = null"><AppIcon name="halt" /><span class="mi-text">Stop now…</span></button>
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
        <template v-if="menuItems.remove">
          <hr class="menu-sep">
          <button type="button" role="menuitem" class="menu-item" @click="pickRemove">
            <AppIcon name="trash" :size="16" /><span class="mi-text"><span>{{ quickRemoval(menu.view) ? 'Remove' : 'Remove…' }}</span></span>
          </button>
        </template>
        <p v-if="menuItems.note" class="menu-note">{{ menuItems.note }}</p>
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
  </section>
</template>

<style scoped>
.c-tier { display: flex; align-items: center; justify-content: center; align-self: stretch; }
.phone-tier { display: none; }
.row.drop-over { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--ink-2); }
.move-lead .mi-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lineage { flex-basis: 100%; display: flex; align-items: center; gap: 8px; min-width: 0; padding: 2px 0 3px 38px; font-size: 11px; color: var(--ink-2); }
.lineage a, .lineage.adopted { color: inherit; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lineage a:hover, .lineage.adopted:hover { text-decoration: underline; }
.lineage .history-toggle { flex-shrink: 0; }
.sessions { overflow: clip; container: sessions / inline-size; }
.card-head { display: flex; align-items: baseline; gap: 10px; padding: 14px 18px 10px; }
.card-head h2 { font-size: 15px; font-weight: 650; }
.head-tools { display: inline-flex; align-items: center; gap: 2px; margin-left: auto; margin-right: -8px; }
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
/* Where headers (or their Running and Heartbeat columns) are hidden, this bar carries the same keys and direction. */
.sort-bar { display: none; align-items: center; flex-wrap: wrap; gap: 8px; padding: 6px 18px 10px; border-top: 1px solid var(--line); }
.sort-pick { display: inline-flex; align-items: center; gap: 8px; }
.sort-label { font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.sort-select { height: 34px; padding: 0 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--field-bg); font: inherit; font-size: 13px; color: var(--ink); }
.sort-select:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.sort-dir { width: 34px; height: 34px; }
.sort-note { flex-basis: 100%; font-size: 12px; color: var(--ink-3); }
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
/* The track is anchored to the lead glyph's centre. Each visible descendant
   carries its ancestors' tracks across row boundaries; the last child closes
   its track at the badge. The toggle remains in the lead's text column. */
.row > .tree-lines { position: absolute; inset: 0 0 0 calc(var(--state-width) + 17px); padding: 0; pointer-events: none; color: var(--ink-3); }
.tree-guide, .tree-stem { position: absolute; left: calc(var(--level) * var(--tree-step)); top: 0; bottom: 0; width: var(--tree-step); }
.tree-guide.continues::before, .tree-guide.elbow::before { content: ''; position: absolute; top: 0; bottom: 0; width: 1px; background: currentColor; }
.tree-guide.last::before { bottom: auto; height: calc(var(--tree-joint) - 4px); }
.tree-guide.elbow::after { content: ''; position: absolute; top: calc(var(--tree-joint) - 4px); left: 0; width: calc(var(--tree-step) - 15px); height: 5px; border: solid currentColor; border-width: 0 0 1px 1px; border-radius: 0 0 0 5px; }
.tree-stem { top: calc(var(--tree-joint) + 15px); bottom: 0; width: 1px; background: currentColor; }
.worker-tools { display: flex; align-items: center; flex-wrap: wrap; gap: 2px; flex-basis: 100%; padding: 2px 0 6px 38px; color: var(--ink-2); font-size: 11.5px; }
.worker-toggle { display: inline-flex; align-items: center; justify-content: center; gap: 4px; min-height: 28px; padding: 2px 6px; border: 0; border-radius: 6px; background: transparent; color: var(--ink); font: inherit; font-weight: 550; white-space: nowrap; }
.worker-toggle:hover:not(:disabled) { background: var(--row-hover); }
.worker-toggle:disabled { cursor: default; }
.worker-toggle:focus-visible { box-shadow: var(--focus-ring); }
.history-toggle { color: var(--ink-2); font-weight: 450; }
.history-toggle[aria-expanded="true"] { background: var(--row-hover); color: var(--ink); }
.idle-count { padding-inline: 4px; white-space: nowrap; }
.chev { transition: transform .2s ease; }
@media (prefers-reduced-motion: reduce) { .row, .chev { transition: none; } }
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
.agent-link { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; color: var(--ink); text-decoration: none; }
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
  .sort-bar { display: flex; }
  .table { --state-width: 156px; grid-template-columns: var(--state-width) minmax(100px, 1.35fr) minmax(68px, .42fr) minmax(108px, .75fr) 80px 132px 64px 108px; }
  .table.has-eta { grid-template-columns: var(--state-width) minmax(100px, 1.35fr) minmax(104px, .42fr) minmax(108px, .75fr) 80px 132px 64px 108px; }
  .c-elapsed { display: none; }
}
/* The menu floats outside the list container: size its targets by viewport. */
@media (max-width: 720px) { .menu-item { min-height: 44px; align-items: center; } .menu-item > svg { margin-top: 0; } }
/* Narrow lists stack identity, execution and state. Tier uses its inline glyph;
   the wider breakpoint retains Host and the pause/remove/overflow reservation. */
@container sessions (max-width: 880px) {
  /* One text column beside the avatar column: title (two lines at most) with
     the heartbeat inline under it, one execution line, one state line, then a
     lead's worker toggles. The avatar column stays free for the tree lines. */
  .table { --tree-step: 28px; --title-line: 18px; display: block; }
  .thead, .c-tier { display: none; }
  .phone-tier { display: inline-flex; }
  /* Default needs the same reachable control as paid tiers. Reserve its box
     through confirmation, price feedback and Undo so neighbours stay put. */
  .phone-tier :deep(.tier-mark) { width: 72px; }
  .phone-tier :deep(.price) { max-width: 44px; overflow: hidden; text-overflow: ellipsis; }
  .group-row { display: block; margin: 12px 8px 2px; padding: 0 8px; }
  .row { --tree-joint: 25px; display: grid; grid-template-columns: 30px auto minmax(0, 1fr) 44px; grid-template-rows: auto auto auto auto; grid-template-areas: ". . . actions" ". . . actions" ". . . actions" ". . . actions"; column-gap: 8px; row-gap: 0; align-items: start; min-height: 0; margin: 0 6px; padding: 10px 0 10px calc(10px + var(--depth) * var(--tree-step)); }
  .row > span, .execution-host > span { padding: 0; }
  .row > .c-agent { grid-column: 1 / 4; grid-row: 1 / 5; display: grid; grid-template-columns: subgrid; grid-template-rows: subgrid; align-items: start; padding-block: 0; }
  .row.worker .c-agent { padding-left: 0; }
  .agent-link { grid-column: 1 / -1; grid-row: 1; display: grid; grid-template-columns: subgrid; align-items: start; }
  .who { grid-column: 2 / -1; line-height: 1.3; }
  .result { font-size: 14px; line-height: var(--title-line); white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; }
  .session-context { margin-top: 2px; }
  .ctx-beat { display: inline; }
  .ctx-beat::before { content: '·'; margin-right: 6px; }
  .row > .tree-lines { left: 25px; }
  .row:has(.lineage) { grid-template-rows: auto auto auto auto auto; }
  .row:has(.lineage) > .c-agent { grid-row: 1 / 6; }
  .lineage { grid-column: 2 / -1; grid-row: 4; padding: 4px 0 0; min-height: 24px; }
  .worker-tools ~ .lineage { grid-row: 5; }
  .worker-tools { grid-column: 2 / -1; grid-row: 4; flex-wrap: nowrap; white-space: nowrap; margin: 0 0 0 -6px; padding: 0; }
  /* The 44 px toggle line closes a lead's row by itself. */
  .row:has(> .c-agent > .worker-tools) { padding-bottom: 0; }
  .worker-toggle { min-height: 44px; padding-inline: 6px; }
  .worker-tools .idle-count, .worker-tools > span[aria-hidden]:has(+ .idle-count) { display: none; }
  /* Reserve every available action on the title line. A silent session can
     offer Pause, Remove and Actions together; Host owns the line below. */
  .row:has(> .c-actions > :is(.bin, .pause-controls:not(:empty))):has(> .c-actions > .more) { grid-template-columns: 30px auto minmax(0, 1fr) max-content; }
  .row > .c-actions:has(> :is(.bin, .pause-controls:not(:empty))):has(> .more) { flex-direction: row; }
  /* Execution and Host share the whole line below the actions; a short host
     returns its spare width to the model/command. */
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
  .c-state { grid-column: 2; grid-row: 3; min-width: 0; margin-top: 4px; min-height: 22px; }
  .c-state :deep(.agent-state-label) { min-width: 0; max-width: 100%; align-items: flex-start; }
  .c-state :deep(.state-word) { min-width: 0; white-space: normal; overflow-wrap: anywhere; }
  .c-ticket { grid-column: 3; grid-row: 3; justify-self: start; min-width: 0; overflow: hidden; margin-top: 4px; min-height: 22px; }
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
  /* With Default order showing, the tools take their own line rather than clip. */
  .card-head { flex-wrap: wrap; align-items: center; padding: 6px 10px 2px 18px; }
  .head-tools { flex-wrap: wrap; justify-content: flex-end; }
  .head-tools .btn { min-height: 44px; }
  .sort-bar { padding: 8px 18px; }
  .sort-select { height: 44px; font-size: 16px; }
  .sort-dir { width: 44px; height: 44px; }
}
/* A very narrow list cannot fit both Terminal and its command beside Host.
   Grow the execution copy downward to leave room for command text. */
@container sessions (max-width: 320px) {
  .c-exec:has([data-run-kind="terminal"]) .exec-copy { display: grid; }
  .c-exec:has([data-run-kind="terminal"]) .exec-model::before { content: none; }
}
</style>
