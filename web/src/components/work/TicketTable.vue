<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { vClipTip } from '../../directives/clipTip'
import { estimateDisplay } from '../../lib/estimates'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { ListItem } from '../../lib/api'
import { COLUMN_BY_ID, costUnitLabel, layoutWidths, releaseLabel, tagList, titleRoom, visibleColumns, widthOf, type ColumnId, type ListPrefs, type TagRef } from '../../lib/columns'
import { releaseCell, type NativeReleaseView } from '../../lib/releaseMembership'
import { ticketWorkers, withServerLead, type LiveAgent } from '../../lib/liveAgents'
import { useLiveAgents } from '../../stores/liveAgents'
import type { GroupBy, RowGroup, EpicRef } from '../../lib/ticketList'
import type { OutlineEntry, TreeMeta } from '../../lib/outline'
import { absoluteTime, highlight, kindLabel, plural, priorityLabel, relativeTime, statusMeta, type SortField, type SortKey } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import TicketTypeIcon from './TicketTypeIcon.vue'
import PersonAvatar from './PersonAvatar.vue'
import PriorityIcon from './PriorityIcon.vue'
import StatusIcon from './StatusIcon.vue'
import TicketWorkers from './TicketWorkers.vue'
import QuickCreateRow, { type QuickDraft } from './QuickCreateRow.vue'
import EtaCell from './EtaCell.vue'
import PlanningCell from './PlanningCell.vue'
import QueueAction from './QueueAction.vue'
import QueueStaleHint from './QueueStaleHint.vue'
import QueueIndicator from './QueueIndicator.vue'
import SuggestedReleaseCell from './SuggestedReleaseCell.vue'
import { useWorkQueue } from '../../stores/workQueue'
import { can } from '../../lib/authz'
import { queueable } from '../../lib/workQueue'
import { PLANNING_COLUMNS, planningPresent, planningTip, type PlanningColumn } from '../../lib/planning'
import { etaFromTicket, progressAccessibleName, progressReportedAt } from '../../lib/eta'

const NO_WORKERS: LiveAgent[] = []

const props = defineProps<{
  // Read-only delivery expansion reuses the ticket row without a second toolbar.
  embedded?: boolean
  rowHref?: (row: { key: string }) => string
  // How many rows the first page will likely show, so the skeleton holds that height.
  expectedRows?: number
  groups: RowGroup[]
  group: GroupBy
  rowsById: Map<string, ListItem>
  cursorId: string | null
  openId: string | null
  query: string
  sort: SortKey[]
  density: 'comfortable' | 'compact'
  loading: boolean
  loadingMore: boolean
  error: string
  moreError: string
  hasMore: boolean
  filtered: boolean
  hidingClosed: boolean
  collapsed: Set<string>
  total: number | null
  externalCount?: boolean
  projectKey: string
  scrollRoot: HTMLElement | null
  now: number
  showAssignee: boolean
  creating: boolean
  projectId: string
  knownStates: string[]
  create: (draft: QuickDraft) => Promise<boolean>
  // Outline mode: the tree's flat entries replace the list groups.
  outline?: OutlineEntry[] | null
  canDrag?: boolean
  // The person's saved columns, order and widths for this project (null: automatic).
  prefs?: ListPrefs | null
  costAllowed?: boolean
  // Multi-select for bulk changes: checkboxes lead each row.
  selectable?: boolean
  selected?: Set<string>
  // Phone selection with nothing chosen yet: round checks show before the first tap.
  picking?: boolean
  canAssignRelease?: boolean
  // Native journey membership for the visible tickets. Imported fields.release is not this.
  nativeReleases?: Map<string, NativeReleaseView>
  // Live updates (AEON-326): rows waiting for Show carry a label ("Closed",
  // "Deleted", ...) and stay dimmed; rows someone else just changed tint briefly.
  liveLabels?: Map<string, string>
  liveFlash?: Set<string>
  liveStale?: boolean
  // "3 updates · Show": in the Title header, so it never covers a row.
  livePill?: { text: string; overflow: boolean } | null
}>()
// Unfiltered loads hold the expected height (so nothing below jumps); 1..28 rows.
const skeletonRows = computed(() => Math.max(1, Math.min(28, props.expectedRows ?? 14)))
const emit = defineEmits<{
  open: [row: ListItem]
  cursor: [id: string]
  sort: [field: SortField, additive: boolean]
  status: [row: ListItem, anchor: HTMLElement]
  copy: [row: ListItem]
  assignee: [row: ListItem, anchor: HTMLElement]
  newTab: [row: ListItem]
  toggleGroup: [key: string]
  openEpic: [epic: EpicRef]
  retry: []
  more: []
  clearFilters: []
  showClosed: []
  gridFocus: []
  closeCreate: []
  toggleRow: [id: string]
  toggleNoEpic: []
  moreChildren: [parentId: string]
  move: [row: ListItem, epic: ListItem | null]
  // Which columns show now (the Display menu lists them), and resized widths to save.
  layout: [visible: ColumnId[], customised: boolean]
  widths: [widths: Partial<Record<ColumnId, number>>]
  // toggle: one row on or off; range: from the last row chosen to this one.
  select: [row: ListItem, mode: 'toggle' | 'range']
  selectAll: [on: boolean]
  release: [row: ListItem, anchor: HTMLElement]
  showUpdates: []
}>()

const CLS: Record<ColumnId, string> = { key: 'c-key', title: 'c-title', status: 'c-status', priority: 'c-prio', assignee: 'c-assignee', epic: 'c-epic', release: 'c-release', tags: 'c-tags', cost: 'c-cost', estimate: 'c-estimate', created: 'c-created', updated: 'c-updated', progress: 'c-progress', eta: 'c-eta', model: 'c-model', tokens: 'c-tokens', list_cost: 'c-list-cost', paid: 'c-paid', suggested: 'c-suggested' }
const isPlanning = (id: ColumnId): id is PlanningColumn => (PLANNING_COLUMNS as ColumnId[]).includes(id)
// Planning cells are not Tab stops. The focused row carries their descriptions.
function planningDescribedBy(row: ListItem): string {
  return columns.value.flatMap(column => column.id === 'suggested' ? [`suggested-${row.id}`] : isPlanning(column.id) && planningTip(row, column.id) ? [`plan-${row.id}-${column.id}`] : []).join(' ')
}
// Columns follow the table's own width (the docked panel narrows it; wide screens
// add columns) and the person's saved choice. Decided here rather than in CSS so
// every colspan matches the visible columns.
const width = ref(1200)
const phoneQuery = window.matchMedia('(max-width: 720px)')
const phone = ref(phoneQuery.matches)
// One live feed for the whole list (AEON-233). Workers are matched to loaded rows;
// a ticket with a worker earns Assignee even when nobody is the stored owner.
const live = useLiveAgents()
const queue = useWorkQueue()
const queued = (row: ListItem) => queueable(row) ? queue.entry(props.projectId, row.id) : null
const mayAssign = computed(() => !props.embedded && (can('nodes.write', props.projectId) || can('run.create', props.projectId)))
const workersById = computed(() => ticketWorkers(live.forProject(props.projectId), props.projectId))
function workersOf(row: ListItem) { return workersById.value.get(row.id) ?? NO_WORKERS }
// The server names the lead. Other live workers stay in the feed's order after it.
function assigneeWorkers(row: ListItem) { return withServerLead(workersOf(row), row.lead_worker) }
// Release, Tags and Estimate earn their automatic columns only when some loaded row has one.
const present = computed(() => {
  const rows = [...props.rowsById.values()]
  const listed = props.outline ? props.outline.flatMap(entry => entry.type === 'row' ? [entry.row] : []) : rows
  return {
    assigned: props.showAssignee || !!queue.snapshots[props.projectId]?.items.length,
    suggested: rows.some(row => ['delivered', 'accepted', 'done', 'in_progress', 'in-progress', 'qa'].includes(row.state) || !!queued(row)),
    workers: listed.some(row => workersOf(row).length > 0 || !!row.lead_worker?.name),
    estimate: rows.some(row => row.kind_slug === 'ticket' || row.kind_slug === 'task' || !!estimate(row) || (row.kind_slug === 'epic' && (row.estimate?.open_children ?? 0) > 0)),
    release: rows.some(row => !!releaseLabel(row.fields) || props.nativeReleases?.get(row.id)?.status === 'member'),
    tags: rows.some(row => tagList(row.fields).length > 0),
    progress: listed.some(row => progressOf(row) != null),
    eta: listed.some(row => { const eta = etaFromTicket(row.eta); return !!eta?.ready || !!eta?.live || !!row.eta?.has_working_session }),
    ...planningPresent(rows),
  }
})
const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone
function progressOf(row: ListItem): { pct: number; stale: boolean; label: string } | null {
  const eta = etaFromTicket(row.eta)
  if (!eta || typeof eta.progress !== 'number') return null
  const pct = Math.max(0, Math.min(100, Math.round(eta.progress)))
  const stale = !!eta.stale || !!props.liveStale
  const label = progressAccessibleName(pct, stale, stale ? progressReportedAt(row.eta) : null, props.now, timeZone)
  return { pct, stale, label: props.liveStale ? `${label}. Showing the last successful update.` : label }
}
const layout = computed(() => {
  if (props.embedded) {
    const ids: ColumnId[] = ['key', 'title', 'status', 'estimate', 'progress', 'eta', 'assignee']
    return { columns: ids.map(id => COLUMN_BY_ID.get(id)!), customised: false }
  }
  return visibleColumns(width.value, { phone: phone.value, present: present.value, prefs: props.prefs, costAllowed: props.costAllowed ?? false })
})
const columns = computed(() => layout.value.columns.map(def => ({ ...def, field: def.sort, cls: CLS[def.id] })))
const ids = computed(() => columns.value.map(column => column.id))
const has = (id: ColumnId) => ids.value.includes(id)
watch(ids, value => emit('layout', value, layout.value.customised), { immediate: true })
// Live widths while dragging a column edge; saved when the drag ends.
const dragWidths = ref<Partial<Record<ColumnId, number>>>({})
// Every column but Title has a col width; Title takes the rest (about 960px at most
// on wide tables, where the spare width widens the text columns instead). Dragging
// Title's edge gives it a width of its own. The columns beside it move within their
// min and max, so a fixed column never collapses.
const layoutWidth = computed(() => layout.value.customised ? Math.max(width.value, ids.value.reduce((sum, id) => sum + (id === 'title' ? COLUMN_BY_ID.get(id)!.min : dragWidths.value[id] ?? widthOf(id, props.prefs)), 0)) : width.value)
const widths = computed(() => layoutWidths(ids.value, layoutWidth.value, props.prefs, dragWidths.value))
function colWidth(id: ColumnId) { return id === 'title' ? null : widths.value[id] ?? null }
function nativeRelease(row: ListItem) {
  if (row.kind_slug === 'epic') return releaseCell({ status: 'none' })
  return releaseCell(props.nativeReleases?.get(row.id))
}
const titleWidth = computed(() => Math.max(0, Math.round(layoutWidth.value - Object.values(widths.value).reduce((sum, w) => sum + (w ?? 0), 0))))
const shownWidth = (id: ColumnId) => id === 'title' ? titleWidth.value : colWidth(id) ?? 0
function bounds(id: ColumnId) {
  if (id === 'title') return titleRoom(ids.value, layoutWidth.value, props.prefs, dragWidths.value)
  const def = COLUMN_BY_ID.get(id)!
  return { min: def.min, max: def.max }
}
function clampColumn(id: ColumnId, value: number) {
  const range = bounds(id)
  return Math.max(range.min, Math.min(range.max, Math.round(value)))
}
// Status, priority and epic line up in the quick-create row when they keep their default places.
const quickAligned = computed(() => ids.value[2] === 'status' && ids.value[3] === 'priority')
const card = ref<HTMLElement>()
let sizer: ResizeObserver | undefined
const phoneChange = () => { phone.value = phoneQuery.matches }
onMounted(() => {
  phoneChange(); phoneQuery.addEventListener('change', phoneChange)
  if (card.value) {
    // The first fit happens before paint, so the columns never jump into place.
    const box = getComputedStyle(card.value)
    width.value = card.value.clientWidth - parseFloat(box.paddingLeft) - parseFloat(box.paddingRight)
    sizer = new ResizeObserver(([entry]) => { width.value = entry.contentRect.width }); sizer.observe(card.value)
  }
})
onBeforeUnmount(() => { phoneQuery.removeEventListener('change', phoneChange); sizer?.disconnect() })
// ---------- Column widths: drag a header edge; double-click fits, or resets Title ----------
let resizing: { id: ColumnId; startX: number; startWidth: number } | null = null
// Two presses within 350ms on the same edge fit the column (pointerdown's
// preventDefault keeps the browser from reporting dblclick reliably). Title
// returns to automatic instead of fitting its content.
let lastPress = { id: '' as ColumnId | '', at: 0 }
function resizeStart(event: PointerEvent, id: ColumnId) {
  if (event.button !== 0) return
  event.preventDefault(); event.stopPropagation()
  if (lastPress.id === id && event.timeStamp - lastPress.at < 350) {
    lastPress = { id: '', at: 0 }
    resizing = null
    if (id === 'title') resetTitle()
    else autofit(id)
    return
  }
  lastPress = { id, at: event.timeStamp }
  resizing = { id, startX: event.clientX, startWidth: shownWidth(id) }
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
}
function resizeMove(event: PointerEvent) {
  if (!resizing) return
  dragWidths.value = { ...dragWidths.value, [resizing.id]: clampColumn(resizing.id, resizing.startWidth + event.clientX - resizing.startX) }
}
function resizeEnd() {
  if (!resizing) return
  const id = resizing.id
  const start = resizing.startWidth
  resizing = null
  const value = dragWidths.value[id]
  // A click on Title's edge, without a drag, leaves it automatic.
  if (value === undefined || (id === 'title' && Math.abs(value - start) < 1)) {
    if (id === 'title' && typeof props.prefs?.widths?.title !== 'number') {
      const { title: _title, ...rest } = dragWidths.value
      dragWidths.value = rest
    }
    return
  }
  emit('widths', { ...(props.prefs?.widths ?? {}), [id]: value })
}
function resizeKey(event: KeyboardEvent, id: ColumnId) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const next = clampColumn(id, shownWidth(id) + (event.key === 'ArrowRight' ? 16 : -16))
  dragWidths.value = { ...dragWidths.value, [id]: next }
  emit('widths', { ...(props.prefs?.widths ?? {}), [id]: next })
}
// Title goes back to taking the spare width. Other columns keep the widths the person set.
function resetTitle() {
  const { title: _title, ...rest } = dragWidths.value
  dragWidths.value = rest
  if (typeof props.prefs?.widths?.title !== 'number') return
  const next = { ...(props.prefs?.widths ?? {}) }
  delete next.title
  emit('widths', next)
}
// Fit: the widest content in the column (header included), within the column's bounds.
function autofit(id: ColumnId) {
  const def = COLUMN_BY_ID.get(id)!
  let widest = 0
  for (const cell of grid.value?.querySelectorAll<HTMLElement>(`.${CLS[id]} .cell, th.${CLS[id]} .th-sort, th.${CLS[id]} .th-label`) ?? []) {
    // Planning descriptions are hidden siblings whose nowrap scrollWidth is
    // the whole hover. Fit only the visible model or figure's own children.
    const content = cell.querySelector<HTMLElement>('.plan-figure, .plan-model') ?? cell
    const children = ([...content.children] as HTMLElement[]).filter(child => !child.classList.contains('sr-only'))
    const gap = parseFloat(getComputedStyle(content).columnGap) || 0
    const inner = children.reduce((sum, child) => sum + Math.max(child.scrollWidth, child.getBoundingClientRect().width), 0) + gap * Math.max(0, children.length - 1)
    widest = Math.max(widest, inner)
  }
  const next = Math.max(def.min, Math.min(def.max, Math.ceil(widest + 26)))
  dragWidths.value = { ...dragWidths.value, [id]: next }
  emit('widths', { ...(props.prefs?.widths ?? {}), [id]: next })
}
watch(() => props.prefs?.widths, () => { if (!resizing) dragWidths.value = {} })
function estimate(row: ListItem) { return estimateDisplay(row).text }
// An epic with open children and no hours still has coverage to explain.
function emptyEstimateTip(row: ListItem) {
  if (row.kind_slug !== 'epic' || (row.estimate?.open_children ?? 0) < 1 || estimate(row)) return 'No estimate yet'
  return estimateDisplay(row).tip
}
const grid = ref<HTMLTableElement>()
const quick = ref<InstanceType<typeof QuickCreateRow>>()
const inlineQuick = ref<InstanceType<typeof QuickCreateRow>[]>([])

// One entry list for both views: the list's groups flattened, or the Outline's tree.
type Entry = OutlineEntry | { type: 'list-group'; key: string; group: RowGroup } | { type: 'row'; key: string; row: ListItem; tree?: TreeMeta; repeat?: boolean }
// Each list group keeps its own tbody so its sticky header scrolls away with it.
// A ticket under several labels shows in each label's group; only its first row
// carries the id that keyboard focus and scrolling use.
const sections = computed<{ key: string; entries: Entry[] }[]>(() => {
  if (props.outline) return [{ key: 'outline', entries: props.outline }]
  const seen = new Set<string>()
  return props.groups.map(group => {
    const entries: Entry[] = []
    if (props.group !== 'none') entries.push({ type: 'list-group', key: `group-${group.key}`, group })
    if (!props.collapsed.has(group.key)) for (const row of group.rows) {
      const repeat = seen.has(row.id)
      seen.add(row.id)
      entries.push({ type: 'row', key: repeat ? `${group.key}:${row.id}` : row.id, row, repeat })
    }
    return { key: group.key, entries }
  })
})
const entries = computed(() => sections.value.flatMap(section => section.entries))
const INDENT = 18
// Column i of a row at `depth`: ancestors' continuing lines, then the row's own elbow.
function guideClass(i: number, depth: number, guides: boolean[], last: boolean) {
  if (i < depth - 1) return guides[i] ? 'line' : 'none'
  return last ? 'elbow last' : 'elbow'
}
function childCount(entry: { row: ListItem; tree?: TreeMeta }) {
  if (entry.tree) return entry.tree.hasChildren && !entry.tree.stats && entry.row.kind_slug !== 'epic' ? entry.row.children_count : 0
  return entry.row.kind_slug === 'epic' ? entry.row.children_count : 0
}

// Drag a ticket onto an epic (or onto "No epic") to move it there.
const dragId = ref<string | null>(null)
const dropTarget = ref<string | null>(null)
let dragged: ListItem | null = null
function draggable(entry: { row: ListItem; tree?: TreeMeta }) { return !!props.canDrag && !!entry.tree && entry.row.kind_slug === 'ticket' && !entry.tree.dimmed }
function dragStart(event: DragEvent, row: ListItem) {
  if (!props.canDrag || row.kind_slug !== 'ticket') return
  dragged = row; dragId.value = row.id
  event.dataTransfer?.setData('text/plain', row.key)
  if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
}
function dragEnd() { dragged = null; dragId.value = null; dropTarget.value = null }
function validTarget(epic: ListItem | null) {
  if (!dragged) return false
  return epic ? epic.id !== dragged.parent_id && epic.kind_slug === 'epic' : dragged.parent?.kind_slug === 'epic' || dragged.parent_id !== props.projectId
}
function dragOver(event: DragEvent, epic: ListItem | null) {
  if (!validTarget(epic)) return
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
  dropTarget.value = epic ? epic.id : 'no-epic'
}
function dragLeave(event: DragEvent, key: string) {
  const to = event.relatedTarget as Node | null
  if (to && (event.currentTarget as HTMLElement).contains(to)) return
  if (dropTarget.value === key) dropTarget.value = null
}
function drop(event: DragEvent, epic: ListItem | null) {
  if (!validTarget(epic) || !dragged) return
  event.preventDefault()
  const row = dragged
  dragEnd()
  emit('move', row, epic)
}
const sentinel = ref<HTMLElement>()
let observer: IntersectionObserver | undefined

function sortOf(field: SortField | null) {
  if (!field) return null
  const index = props.sort.findIndex(key => key.field === field)
  return index === -1 ? null : { ...props.sort[index], index }
}
function ariaSort(field: SortField | null) {
  const current = sortOf(field)
  return current ? (current.desc ? 'descending' : 'ascending') : field ? 'none' : undefined
}
function href(row: { key: string }) { return props.rowHref?.(row) ?? `/p/${encodeURIComponent(props.projectKey)}/${encodeURIComponent(row.key)}` }
function parentOf(row: ListItem) {
  // Direct parents only: an epic, or the ticket a task belongs to. The project itself is implied.
  if (!row.parent || row.parent.kind_slug === 'project') return null
  return row.parent
}
// The epic a row belongs to: its own, or for a task its ticket's. The server names
// it; older servers leave it to the parent (or the parent's row, when loaded).
function epicOf(row: ListItem): { id: string; key: string; title: string } | null {
  if (row.epic !== undefined) return row.epic
  const parent = parentOf(row)
  if (!parent) return null
  if (parent.kind_slug === 'epic') return parent
  const up = props.rowsById.get(parent.id)
  if (up?.epic) return up.epic
  return up?.parent?.kind_slug === 'epic' ? up.parent : null
}
// Chip beside the title: the direct parent, unless the Epic column (or epic
// grouping) already says it. A task's ticket always shows here.
function epicChip(row: ListItem) {
  const parent = parentOf(row)
  if (!parent) return null
  return parent.kind_slug === 'epic' && (props.group === 'epic' || has('epic')) ? null : parent
}
const TAG_SHOWN = 3
function tagTip(tags: TagRef[]) { return tags.map(tag => tag.name).join(', ') }
function statusClick(event: MouseEvent, row: ListItem) {
  // While choosing on a phone, the card is a selection target. Status waits.
  if (props.selectable && phone.value && selecting.value) { emit('select', row, 'toggle'); return }
  emit('status', row, event.currentTarget as HTMLElement)
}
function rowClick(event: MouseEvent, row: ListItem) {
  if (swallowClick) { swallowClick = false; return }
  if (props.selectable && phone.value && selecting.value && !(event.target as HTMLElement).closest('.phone-check')) {
    emit('select', row, 'toggle')
    return
  }
  if ((event.target as HTMLElement).closest('button, input, .ticket-workers')) return
  if (event.metaKey || event.ctrlKey) { emit('newTab', row); return }
  // Shift-click selects the range from the last chosen row; while a phone is
  // choosing, a tap adds or removes the row instead of opening it.
  if (props.selectable && event.shiftKey) { event.preventDefault(); emit('select', row, 'range'); return }
  if (props.selectable && phone.value && selecting.value) { emit('select', row, 'toggle'); return }
  emit('cursor', row.id)
  emit('open', row)
}
const selecting = computed(() => !!props.picking || !!props.selected?.size)
const loadedRows = computed(() => entries.value.filter((entry): entry is Extract<Entry, { type: 'row' }> => entry.type === 'row' && !('repeat' in entry && entry.repeat)))
const allChecked = computed(() => !!props.selected?.size && loadedRows.value.length > 0 && loadedRows.value.every(entry => props.selected!.has(entry.row.id)))
function checkClick(event: MouseEvent, row: ListItem) {
  event.stopPropagation()
  if (event.shiftKey) { event.preventDefault(); emit('select', row, 'range') }
}
// A long press on a phone starts a selection. The hold and the context menu
// both fire for one press; the click that follows must not undo it.
const HOLD_MS = 480
let pressTimer = 0
let pressOrigin: { x: number; y: number } | null = null
let pointerDown = false
let swallowClick = false
function pressBegin(event: PointerEvent, row: ListItem) {
  if (!props.selectable || !phone.value || selecting.value || event.button !== 0) return
  if ((event.target as HTMLElement).closest('button, input')) return
  pointerDown = true
  swallowClick = false
  pressOrigin = { x: event.clientX, y: event.clientY }
  window.clearTimeout(pressTimer)
  pressTimer = window.setTimeout(() => {
    pressTimer = 0
    swallowClick = true
    emit('select', row, 'toggle')
  }, HOLD_MS)
}
function pressMove(event: PointerEvent) {
  if (!pressTimer || !pressOrigin) return
  if (Math.hypot(event.clientX - pressOrigin.x, event.clientY - pressOrigin.y) > 10) pressFinish(false)
}
function pressFinish(fromPointerUp: boolean) {
  pointerDown = false
  pressOrigin = null
  if (!pressTimer) return
  window.clearTimeout(pressTimer)
  pressTimer = 0
  if (fromPointerUp) swallowClick = false
}
function longPress(event: Event, row: ListItem) {
  if (!props.selectable || !phone.value) return
  event.preventDefault()
  if (swallowClick) return
  if (pressTimer) { window.clearTimeout(pressTimer); pressTimer = 0 }
  // A later click belongs to this press only while the finger is still down.
  // A dispatched context menu (no pointer) must leave the next tap alone.
  swallowClick = pointerDown
  emit('select', row, 'toggle')
}
function linkClick(event: MouseEvent) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.button === 1) {
    if (props.embedded) event.stopPropagation() // Keep the native link from also opening through the row handler.
    return // let the browser open a tab
  }
  event.preventDefault()
}
function groupEpicRow(group: RowGroup) { return group.epic ? props.rowsById.get(group.epic.id) : undefined }
function focusGrid() { grid.value?.focus({ preventScroll: true }) }
function scrollToRow(id: string) {
  document.getElementById(`row-${id}`)?.scrollIntoView({ block: 'nearest' })
}

function observe() {
  observer?.disconnect()
  if (!sentinel.value) return
  observer = new IntersectionObserver(entries => {
    if (entries.some(entry => entry.isIntersecting) && props.hasMore && !props.loadingMore && !props.moreError) emit('more')
  }, { root: props.scrollRoot, rootMargin: '0px 0px 800px 0px' })
  observer.observe(sentinel.value)
}
let stopLive: (() => void) | undefined
onMounted(() => { stopLive = live.watch(); observe() })
watch(() => [props.scrollRoot, props.hasMore, props.loadingMore], observe)
onBeforeUnmount(() => { stopLive?.(); observer?.disconnect(); window.clearTimeout(pressTimer) })
defineExpose({
  focusGrid, scrollToRow, el: grid,
  focusCreate: () => (inlineQuick.value[0] ?? quick.value)?.focus(),
  createDirty: () => !!quick.value?.isDirty() || inlineQuick.value.some(row => row?.isDirty()),
})
</script>

<template>
  <div ref="card" class="table-card" :class="[density, { embedded, selectable, selecting, customised: layout.customised, overflowing: !phone && layoutWidth > width + 1 }]">
    <table :style="!phone && layout.customised ? { minWidth: `${layoutWidth}px` } : undefined" ref="grid" class="tickets" :class="{ outline: !!outline }" :role="outline ? 'treegrid' : 'grid'" :aria-label="outline ? 'Ticket outline' : 'Tickets'" :aria-busy="loading" tabindex="0" :aria-activedescendant="cursorId ? `row-${cursorId}` : undefined" @focus="emit('gridFocus')">
      <colgroup>
        <col v-for="column in columns" :key="column.id" :class="column.cls" :style="colWidth(column.id) ? { width: `${colWidth(column.id)}px` } : undefined" />
      </colgroup>
      <thead v-if="!embedded">
        <tr>
          <th v-for="column in columns" :key="column.label" scope="col" :class="[column.cls, { end: column.end }]" :aria-sort="ariaSort(column.field)">
            <input
              v-if="column.id === 'key' && selectable" type="checkbox" class="row-check head-check" :class="{ shown: selecting }" :checked="allChecked"
              :indeterminate="selecting && !allChecked" :aria-label="allChecked ? 'Clear the selection' : 'Select all loaded tickets'" aria-keyshortcuts="Control+A Meta+A"
              @change="emit('selectAll', ($event.target as HTMLInputElement).checked)"
            />
            <button v-if="column.field" type="button" class="th-sort" :class="{ on: sortOf(column.field) }" :data-tip="'Sort by ' + column.label.toLowerCase() + '\nShift-click adds a secondary sort'" @click="event => emit('sort', column.field!, event.shiftKey)">
              <span>{{ column.label }}</span>
              <span class="sort-mark" aria-hidden="true">
                <template v-if="sortOf(column.field)">
                  <AppIcon :name="sortOf(column.field)!.desc ? 'arrow-down' : 'arrow-up'" :size="11" />
                  <span v-if="sort.length > 1" class="sort-index">{{ sortOf(column.field)!.index + 1 }}</span>
                </template>
                <AppIcon v-else-if="!sort.length && column.field === 'updated_at'" name="arrow-down" :size="11" class="default-sort" />
              </span>
            </button>
            <span v-else class="th-label">{{ column.label }}</span>
            <button
              v-if="column.id === 'title' && livePill" type="button" class="live-pill" :aria-label="livePill.text" aria-keyshortcuts="u"
              :data-tip="livePill.overflow ? 'Load the list again' : 'Show the updates · u'" @click.stop="emit('showUpdates')"
            >
              <AppIcon :name="livePill.overflow ? 'refresh' : 'arrow-up'" :size="12" />
              <span class="live-count">{{ livePill.text.split(' · ')[0] }}</span><span class="live-dot" aria-hidden="true">·</span><b>{{ livePill.text.split(' · ')[1] }}</b>
            </button>
            <span
              v-if="!phone" class="col-resize" role="separator" aria-orientation="vertical" tabindex="0"
              :aria-label="`Resize ${column.label} column`" :aria-valuenow="shownWidth(column.id)" :aria-valuemin="bounds(column.id).min" :aria-valuemax="bounds(column.id).max"
              :data-tip="column.id === 'title' ? 'Drag to resize · double-click to reset' : 'Drag to resize · double-click to fit'" @pointerdown="resizeStart($event, column.id)" @pointermove="resizeMove" @pointerup="resizeEnd" @pointercancel="resizeEnd"
              @keydown="resizeKey($event, column.id)" @click.stop
            />
          </th>
        </tr>
      </thead>

      <tbody v-if="creating" class="create-body">
        <QuickCreateRow ref="quick" :project-id="projectId" :known-states="knownStates" :trailing="columns.length - 4" :span="quickAligned ? undefined : columns.length - 2" :create="create" @close="emit('closeCreate')" />
      </tbody>
      <tbody v-if="loading && !entries.length" class="skeleton-body" aria-hidden="true">
        <tr v-for="index in skeletonRows" :key="index" class="ticket-row ghost">
          <td class="c-key"><div class="cell"><span class="skeleton sk-key" /></div></td>
          <td class="c-title"><div class="cell"><span class="skeleton sk-title" :style="{ width: `${38 + ((index * 37) % 45)}%` }" /></div></td>
          <td v-for="column in columns.slice(2)" :key="column.id" :class="column.cls"><div class="cell"><span v-if="column.id === 'status'" class="sk-dot" /><span class="skeleton" :class="column.end ? 'sk-time' : 'sk-word'" /></div></td>
        </tr>
      </tbody>

      <tbody v-for="section in sections" v-else :key="section.key" :class="{ dim: loading }">
        <template v-for="entry in section.entries" :key="entry.key">
          <!-- List grouping header (status or epic) -->
          <tr v-if="entry.type === 'list-group'" class="group-row" :class="{ collapsed: collapsed.has(entry.group.key) }">
            <th :colspan="columns.length" scope="rowgroup">
              <div class="group-head">
                <button type="button" class="group-toggle" :aria-expanded="!collapsed.has(entry.group.key)" :aria-label="`${collapsed.has(entry.group.key) ? 'Expand' : 'Collapse'} ${entry.group.epic ? entry.group.epic.key : entry.group.label}`" @click="emit('toggleGroup', entry.group.key)">
                  <AppIcon name="chevron" :size="14" />
                </button>
                <template v-if="group === 'status'">
                  <StatusIcon :state="entry.group.state ?? ''" />
                  <span class="group-label">{{ entry.group.label }}</span>
                  <span class="group-count mono">{{ entry.group.total }}</span>
                </template>
                <template v-else-if="entry.group.epic">
                  <AppIcon name="epic" :size="14" class="epic-glyph" />
                  <button v-if="groupEpicRow(entry.group)" :id="`row-${entry.group.epic.id}`" type="button" class="group-epic" :class="{ cursor: cursorId === entry.group.epic.id, open: openId === entry.group.epic.id }" @click="emit('cursor', entry.group.epic.id); emit('open', groupEpicRow(entry.group)!)">
                    <span class="key">{{ entry.group.epic.key }}</span>
                    <span class="group-label">{{ entry.group.epic.title }}</span>
                  </button>
                  <button v-else type="button" class="group-epic" @click="emit('openEpic', entry.group.epic)">
                    <span class="key">{{ entry.group.epic.key }}</span>
                    <span class="group-label">{{ entry.group.epic.title }}</span>
                  </button>
                  <StatusIcon v-if="groupEpicRow(entry.group)" :state="groupEpicRow(entry.group)!.state" :size="12" class="group-epic-status" />
                  <span class="group-count mono">{{ entry.group.total }}</span>
                </template>
                <template v-else-if="entry.group.person">
                  <PersonAvatar :id="entry.group.person.id" :name="entry.group.person.name" :size="18" />
                  <span class="group-label">{{ entry.group.label }}</span>
                  <span class="group-count mono">{{ entry.group.total }}</span>
                </template>
                <template v-else-if="entry.group.priority && entry.group.priority !== 'none'">
                  <PriorityIcon :priority="entry.group.priority" />
                  <span class="group-label">{{ entry.group.label }}</span>
                  <span class="group-count mono">{{ entry.group.total }}</span>
                </template>
                <template v-else-if="entry.group.kind">
                  <AppIcon :name="entry.group.kind === 'epic' ? 'epic' : entry.group.kind === 'task' ? 'task' : 'ticket'" :size="14" class="kind-glyph" :class="entry.group.kind" />
                  <span class="group-label">{{ entry.group.label }}</span>
                  <span class="group-count mono">{{ entry.group.total }}</span>
                </template>
                <template v-else-if="entry.group.tag">
                  <i class="tag-dot group-dot" :data-color="entry.group.tag.color || undefined" aria-hidden="true" />
                  <span class="group-label">{{ entry.group.label }}</span>
                  <span class="group-count mono">{{ entry.group.total }}</span>
                </template>
                <template v-else>
                  <span class="group-label muted">{{ entry.group.label }}</span>
                  <span class="group-count mono">{{ entry.group.total }}</span>
                </template>
              </div>
            </th>
          </tr>

          <!-- Outline "No epic" group: also a drop target to take a ticket out of its epic -->
          <tr
            v-else-if="entry.type === 'group'" class="group-row outline-group" :class="{ collapsed: entry.collapsed, 'drop-target': dropTarget === 'no-epic' }"
            @dragover="dragOver($event, null)" @dragleave="dragLeave($event, 'no-epic')" @drop="drop($event, null)"
          >
            <th :colspan="columns.length" scope="rowgroup">
              <div class="group-head">
                <button type="button" class="group-toggle" :aria-expanded="!entry.collapsed" :aria-label="`${entry.collapsed ? 'Expand' : 'Collapse'} ${entry.label}`" @click="emit('toggleNoEpic')">
                  <AppIcon name="chevron" :size="14" />
                </button>
                <span class="group-label muted">{{ entry.label }}</span>
                <span class="group-count mono">{{ entry.count }}</span>
                <span v-if="dropTarget === 'no-epic'" class="drop-pill"><AppIcon name="arrow" :size="11" />Take out of its epic</span>
              </div>
            </th>
          </tr>

          <!-- Children still loading -->
          <tr v-else-if="entry.type === 'skeleton'" class="ticket-row ghost tree-row" aria-hidden="true">
            <td class="c-key"><div class="cell"><span class="skeleton sk-key" /></div></td>
            <td class="c-title">
              <div class="cell title-cell">
                <span class="tree" :style="{ width: `${(entry.depth + 1) * INDENT}px` }">
                  <span v-for="i in entry.depth" :key="i" class="guide" :class="guideClass(i - 1, entry.depth, entry.guides, entry.last)" :style="{ left: `${(i - 1) * INDENT}px` }" />
                </span>
                <span class="skeleton sk-title" style="width: 42%" />
              </div>
            </td>
            <td v-for="column in columns.slice(2)" :key="column.id" :class="column.cls"><div class="cell"><span v-if="column.id === 'status'" class="sk-dot" /><span v-if="column.id === 'status' || column.id === 'priority' || column.end" class="skeleton" :class="column.end ? 'sk-time' : 'sk-word'" /></div></td>
          </tr>

          <!-- More children or top-level work to load -->
          <tr v-else-if="entry.type === 'more'" class="more-row">
            <td class="c-key" />
            <td :colspan="columns.length - 1" class="c-title">
              <div class="cell title-cell">
                <span class="tree" :style="{ width: `${(entry.depth + 1) * INDENT}px` }">
                  <span v-for="i in entry.depth" :key="i" class="guide" :class="guideClass(i - 1, entry.depth, entry.guides, true)" :style="{ left: `${(i - 1) * INDENT}px` }" />
                </span>
                <button type="button" class="more-btn" :disabled="entry.loading" @click="emit('moreChildren', entry.parentId)">{{ entry.loading ? 'Loading…' : 'Show more' }}</button>
              </div>
            </td>
          </tr>

          <!-- Inline create under an epic -->
          <QuickCreateRow
            v-else-if="entry.type === 'create'" ref="inlineQuick" :project-id="projectId" :known-states="knownStates" :trailing="columns.length - 4" :span="quickAligned ? undefined : columns.length - 2" :create="create"
            :initial-epic="entry.epic" :indent="(entry.depth + 1) * INDENT" @close="emit('closeCreate')"
          />

          <!-- A ticket, task or epic -->
          <tr
            v-else :id="'repeat' in entry && entry.repeat ? undefined : `row-${entry.row.id}`" class="ticket-row"
            :class="{
              selected: !!selected?.has(entry.row.id),
              cursor: cursorId === entry.row.id, open: openId === entry.row.id, epic: entry.row.kind_slug === 'epic',
              'tree-row': !!entry.tree, dimmed: entry.tree?.dimmed, top: entry.tree && entry.tree.depth === 0 && entry.row.kind_slug === 'epic',
              'drop-target': dropTarget === entry.row.id, dragging: dragId === entry.row.id,
              stale: !!liveLabels?.has(entry.row.id), 'live-flash': !!liveFlash?.has(entry.row.id),
            }"
            :style="entry.tree ? { '--depth': entry.tree.depth } : undefined"
            :data-planning-item="embedded ? entry.row.id : undefined"
            :aria-selected="cursorId === entry.row.id" :aria-level="entry.tree ? entry.tree.depth + 1 : undefined"
            :aria-describedby="planningDescribedBy(entry.row) || undefined"
            :aria-expanded="entry.tree?.hasChildren ? entry.tree.expanded : undefined"
            :draggable="draggable(entry) ? 'true' : undefined"
            @click="rowClick($event, entry.row)" @contextmenu="longPress($event, entry.row)"
            @pointerdown="pressBegin($event, entry.row)" @pointermove="pressMove" @pointerup="pressFinish(true)" @pointercancel="pressFinish(false)"
            @dragstart="dragStart($event, entry.row)" @dragend="dragEnd"
            @dragover="entry.row.kind_slug === 'epic' && entry.tree ? dragOver($event, entry.row) : undefined"
            @dragleave="dragLeave($event, entry.row.id)" @drop="entry.row.kind_slug === 'epic' && entry.tree ? drop($event, entry.row) : undefined"
          >
            <td v-if="phone && selecting" class="c-check">
              <button
                type="button" class="phone-check" role="checkbox" :aria-checked="!!selected?.has(entry.row.id)" :aria-label="`Select ${entry.row.key}`"
                @click.stop="emit('select', entry.row, 'toggle')"
              >
                <span class="mark" aria-hidden="true"><AppIcon v-if="selected?.has(entry.row.id)" name="check" :size="13" /></span>
              </button>
            </td>
            <td class="c-key">
              <slot v-if="embedded" name="planning-handle" :row="entry.row" />
              <div class="cell">
                <input
                  v-if="selectable" type="checkbox" class="row-check" :checked="!!selected?.has(entry.row.id)" :aria-label="`Select ${entry.row.key}`" tabindex="-1"
                  @click="checkClick($event, entry.row)" @change="emit('select', entry.row, 'toggle')"
                />
                <button type="button" class="key key-btn" :aria-label="`Copy ${entry.row.key}`" :data-tip="`Copy ${entry.row.key}`" @click.stop="emit('copy', entry.row)">{{ entry.row.key }}</button>
              </div>
            </td>
            <td class="c-title">
              <div class="cell title-cell">
                <span v-if="entry.tree" class="tree" :style="{ width: `${(entry.tree.depth + 1) * INDENT}px` }">
                  <span v-for="i in entry.tree.depth" :key="i" class="guide" :class="guideClass(i - 1, entry.tree.depth, entry.tree.guides, entry.tree.last)" :style="{ left: `${(i - 1) * INDENT}px` }" />
                  <button
                    v-if="entry.tree.hasChildren" type="button" class="twisty" :style="{ '--twisty-left': `${entry.tree.depth * INDENT}px` }" :aria-expanded="entry.tree.expanded"
                    :aria-label="`${entry.tree.expanded ? 'Collapse' : 'Expand'} ${entry.row.key}`" tabindex="-1" @click.stop="emit('toggleRow', entry.row.id)"
                  ><AppIcon name="chevron-right" :size="13" /></button>
                  <span v-else class="twisty-spacer" />
                </span>
                <TicketTypeIcon :kind="entry.row.kind_slug" :recurrence="entry.row.recurrence" />
                <a class="title-link" :href="href(entry.row)" :tabindex="embedded ? 0 : -1" @click="linkClick"><span v-clip-tip="entry.row.title" class="title-text"><template v-for="(part, i) in highlight(entry.row.title, query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span></a>
                <span v-if="liveLabels?.has(entry.row.id)" class="live-label">{{ liveLabels.get(entry.row.id) }}</span>
                <span v-if="childCount(entry)" class="child-count mono" :data-tip="plural(childCount(entry), 'child item')">{{ childCount(entry) }}</span>
                <span v-if="!entry.tree && epicChip(entry.row)" class="parent-chip" :class="{ epic: epicChip(entry.row)!.kind_slug === 'epic' }" :data-tip="`${kindLabel(epicChip(entry.row)!.kind_slug)} ${epicChip(entry.row)!.key}\n${epicChip(entry.row)!.title}`">
                  <AppIcon v-if="epicChip(entry.row)!.kind_slug === 'epic'" name="epic" :size="10" />
                  <AppIcon v-else name="ticket" :size="10" />
                  <span v-if="epicChip(entry.row)!.kind_slug === 'epic'" class="parent-title">{{ epicChip(entry.row)!.title }}</span>
                  <span v-else class="parent-title mono">{{ epicChip(entry.row)!.key }}</span>
                </span>
                <TicketWorkers v-if="!has('assignee') && assigneeWorkers(entry.row).length" class="title-workers" variant="cue" :workers="assigneeWorkers(entry.row)" :ticket-key="entry.row.key" :stale="liveStale" />
                <span v-if="dropTarget === entry.row.id" class="drop-pill"><AppIcon name="arrow" :size="11" />Move into {{ entry.row.key }}</span>
                <span v-else-if="entry.tree?.stats && entry.tree.stats.scope" class="epic-progress" :data-tip="`${entry.tree.stats.done} of ${entry.tree.stats.scope} done${entry.tree.stats.total - entry.tree.stats.scope ? ` · ${entry.tree.stats.total - entry.tree.stats.scope} cancelled` : ''}`">
                  <span class="bar"><i :style="{ width: `${Math.round(entry.tree.stats.done / entry.tree.stats.scope * 100)}%` }" /></span>
                  <span class="mono">{{ entry.tree.stats.done }}/{{ entry.tree.stats.scope }}</span>
                </span>
              </div>
              <slot v-if="embedded" name="planning-menu" :row="entry.row" />
              <span v-if="!embedded" class="row-actions">
                <QueueAction :row="entry.row" :project-id="projectId" />
                <button type="button" class="icon-btn sm flat" :aria-label="`Open ${entry.row.key} in a new tab`" data-tip="Open in new tab" @click.stop="emit('newTab', entry.row)"><AppIcon name="external" :size="13" /></button>
              </span>
            </td>
            <template v-for="column in columns.slice(2)" :key="column.id">
              <td v-if="column.id === 'status'" class="c-status" :data-column-label="column.label">
                <div class="cell"><button type="button" class="status-btn" :disabled="embedded" :aria-label="`Status: ${statusMeta(entry.row.state).label}${queued(entry.row) ? `, queued #${queued(entry.row)!.position}` : ''}${embedded ? '' : `. Change status of ${entry.row.key}`}`" aria-haspopup="menu" @click.stop="statusClick($event, entry.row)">
                  <StatusIcon :state="entry.row.state" />
                  <span>{{ statusMeta(entry.row.state).label }}</span><span v-if="queued(entry.row)" class="q-pos">· #{{ queued(entry.row)!.position }}</span>
                </button><QueueStaleHint :row="entry.row" /></div>
              </td>
              <td v-else-if="column.id === 'priority'" class="c-prio" :data-column-label="column.label" :class="{ narrow: (colWidth('priority') ?? 112) < 100 }">
                <div class="cell" :data-tip="entry.row.priority && entry.row.priority !== 'none' ? priorityLabel(entry.row.priority) : 'No priority'">
                  <template v-if="entry.row.priority && entry.row.priority !== 'none'"><PriorityIcon :priority="entry.row.priority" /><span class="prio-label">{{ priorityLabel(entry.row.priority) }}</span></template>
                  <span v-else class="empty" aria-label="No priority">—</span>
                </div>
              </td>
              <td v-else-if="column.id === 'assignee'" class="c-assignee" :data-column-label="column.label">
                <div class="cell"><button type="button" class="assignee-btn" :disabled="!mayAssign" :aria-label="`Assignee: ${queued(entry.row) ? `queued #${queued(entry.row)!.position}` : entry.row.assignee?.name ?? (embedded ? 'Not reported' : 'Unassigned')}${embedded ? '' : `. Change assignee of ${entry.row.key}`}`" aria-haspopup="menu" @click.stop="emit('assignee', entry.row, $event.currentTarget as HTMLElement)">
                  <span v-if="entry.row.assignee" class="owner" :class="{ 'with-workers': assigneeWorkers(entry.row).length || queued(entry.row) }" :data-tip="entry.row.assignee.name"><PersonAvatar :id="entry.row.assignee.id" :name="entry.row.assignee.name" :size="20" /><span v-if="!queued(entry.row)" class="person-name">{{ entry.row.assignee.name }}</span></span>
                  <QueueIndicator v-if="queued(entry.row)" :entry="queued(entry.row)!" :manual="queue.snapshots[projectId]?.manual_order" />
                  <span v-else-if="assigneeWorkers(entry.row).length && !entry.row.assignee"><AppIcon name="chevron" :size="12" /></span>
                  <span v-else-if="!entry.row.assignee" class="empty" :aria-label="embedded ? 'Assignee not reported' : 'Unassigned'">—</span>
                </button><TicketWorkers v-if="!queued(entry.row) && assigneeWorkers(entry.row).length" :workers="assigneeWorkers(entry.row)" :ticket-key="entry.row.key" :stale="liveStale" /></div>
              </td>
              <td v-else-if="column.id === 'suggested'" class="c-suggested" :data-column-label="column.label"><div class="cell"><SuggestedReleaseCell :row="entry.row" :project-id="projectId" :now="now" /></div></td>
              <td v-else-if="column.id === 'epic'" class="c-epic" :data-column-label="column.label">
                <div class="cell">
                  <span v-if="epicOf(entry.row)" class="epic-cell" :data-tip="`Epic ${epicOf(entry.row)!.key}\n${epicOf(entry.row)!.title}`">
                    <AppIcon name="epic" :size="12" class="epic-glyph" />
                    <span class="epic-name">{{ epicOf(entry.row)!.title }}</span>
                  </span>
                  <span v-else class="empty" aria-label="No epic">—</span>
                </div>
              </td>
              <td v-else-if="column.id === 'release'" class="c-release" :data-column-label="column.label">
                <div class="cell">
                  <button
                    v-if="canAssignRelease && entry.row.kind_slug !== 'epic'" type="button" class="release-chip mono" :class="{ bare: nativeRelease(entry.row).kind !== 'member' }"
                    :aria-label="nativeRelease(entry.row).kind === 'member' ? `${nativeRelease(entry.row).label}. Change release of ${entry.row.key}` : nativeRelease(entry.row).kind === 'none' ? `No release. Add ${entry.row.key} to a release` : `Release unknown. Add ${entry.row.key} to a release`"
                    @click.stop="emit('release', entry.row, $event.currentTarget as HTMLElement)"
                  >{{ nativeRelease(entry.row).text }}</button>
                  <span v-else-if="nativeRelease(entry.row).kind === 'member'" class="release-chip mono">{{ nativeRelease(entry.row).text }}</span>
                  <span v-else class="empty" :aria-label="nativeRelease(entry.row).label">{{ nativeRelease(entry.row).text }}</span>
                </div>
              </td>
              <td v-else-if="column.id === 'tags'" class="c-tags" :data-column-label="column.label">
                <div v-if="tagList(entry.row.fields).length" class="cell tag-cell" :data-tip="tagTip(tagList(entry.row.fields))">
                  <span v-for="tag in tagList(entry.row.fields).slice(0, TAG_SHOWN)" :key="tag.name" class="tag-chip"><i class="tag-dot" :data-color="tag.color || undefined" aria-hidden="true" />{{ tag.name }}</span>
                  <span v-if="tagList(entry.row.fields).length > TAG_SHOWN" class="tag-more mono">+{{ tagList(entry.row.fields).length - TAG_SHOWN }}</span>
                </div>
                <div v-else class="cell"><span class="empty" aria-label="No tags">—</span></div>
              </td>
              <td v-else-if="column.id === 'cost'" class="c-cost" :data-column-label="column.label">
                <div class="cell">
                  <span v-if="costUnitLabel(entry.row.fields)" class="cost-cell" :data-tip="`Cost unit ${costUnitLabel(entry.row.fields)}`"><AppIcon name="coin" :size="12" class="cost-glyph" /><span class="cost-name">{{ costUnitLabel(entry.row.fields) }}</span></span>
                  <span v-else class="empty" aria-label="No cost unit">—</span>
                </div>
              </td>
              <td v-else-if="column.id === 'estimate'" class="c-estimate" :data-column-label="column.label"><div class="cell"><span v-if="estimate(entry.row)" class="mono" :class="{ 'estimate-draft': estimateDisplay(entry.row).draft }" :data-tip="estimateDisplay(entry.row).tip">{{ estimate(entry.row) }}<span v-if="estimateDisplay(entry.row).draft" class="estimate-mark"> est.</span></span><span v-else class="empty" :aria-label="emptyEstimateTip(entry.row) || 'No estimate'" :data-tip="emptyEstimateTip(entry.row) || undefined">—</span></div></td>
              <td v-else-if="column.id === 'created'" class="c-created" :data-column-label="column.label"><div class="cell"><time :datetime="entry.row.created_at" :data-tip="absoluteTime(entry.row.created_at)">{{ relativeTime(entry.row.created_at, { now }) }}</time></div></td>
              <td v-else-if="column.id === 'updated'" class="c-updated" :data-column-label="column.label"><div class="cell"><time :datetime="entry.row.updated_at" :data-tip="absoluteTime(entry.row.updated_at)">{{ relativeTime(entry.row.updated_at, { now }) }}</time></div></td>
              <td v-else-if="column.id === 'progress'" class="c-progress" :data-column-label="column.label">
                <div class="cell">
                  <span v-if="progressOf(entry.row)" class="progress-read" :class="{ stale: progressOf(entry.row)!.stale }" role="img" :aria-label="progressOf(entry.row)!.label" :data-tip="progressOf(entry.row)!.label">
                    <span class="bar" aria-hidden="true"><i :style="{ width: `${progressOf(entry.row)!.pct}%` }" /></span>
                    <span class="pct" aria-hidden="true">{{ progressOf(entry.row)!.pct }}%</span>
                  </span>
                  <span v-else class="empty" aria-label="No progress">—</span>
                </div>
              </td>
              <td v-else-if="column.id === 'eta'" class="c-eta" :data-column-label="column.label"><div class="cell"><EtaCell :eta="etaFromTicket(entry.row.eta)" :now="now" :missing="!!entry.row.eta?.has_working_session" :connection-stale="liveStale" /></div></td>
              <td v-else-if="isPlanning(column.id)" :class="column.cls" :data-column-label="column.label"><div class="cell"><PlanningCell :column="column.id" :row="entry.row" :row-id="entry.row.id" /></div></td>
            </template>
          </tr>
        </template>
      </tbody>
    </table>

    <div v-if="!embedded && error" class="state" role="alert">
      <span class="state-icon danger"><AppIcon name="alert" :size="18" /></span>
      <h2>Tickets could not be loaded</h2>
      <p>{{ error }}</p>
      <button type="button" class="btn" @click="emit('retry')"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <div v-else-if="!embedded && !loading && !entries.length" class="state">
      <span class="state-icon"><AppIcon :name="filtered ? 'filter' : 'inbox'" :size="18" /></span>
      <template v-if="filtered">
        <h2>No tickets match these filters</h2>
        <div class="state-actions">
          <button type="button" class="btn" @click="emit('clearFilters')">Clear filters</button>
        </div>
      </template>
      <template v-else-if="hidingClosed">
        <h2>Nothing open here</h2>
        <p>Every ticket in this project is closed.</p>
        <button type="button" class="btn" @click="emit('showClosed')">Show closed tickets</button>
      </template>
      <template v-else>
        <h2>No tickets yet</h2>
        <p>Tickets, epics and tasks of this project appear here.</p>
      </template>
    </div>

    <div v-if="!embedded" ref="sentinel" class="sentinel" aria-hidden="true" />
    <div v-if="!embedded && loadingMore" class="list-foot" role="status"><span class="spinner" aria-hidden="true" />Loading more tickets…</div>
    <div v-else-if="!embedded && moreError" class="list-foot error" role="alert">More tickets could not be loaded. <button type="button" class="btn sm" @click="emit('more')">Retry</button></div>
    <div v-else-if="!embedded && !externalCount && !loading && !error && total && !hasMore && entries.length" class="list-foot end phone-only">{{ plural(total, 'ticket') }}</div>
  </div>
</template>

<style scoped>
.table-card {
  --row-h: 36px;
  position: relative; border-radius: var(--radius); border: 1px solid var(--glass-edge); overflow: clip;
  background: linear-gradient(165deg, var(--surface-raised-2), var(--glass) 60%); box-shadow: var(--shadow);
  container: tickets / inline-size;
}
.table-card.embedded { border: 0; border-radius: 0; box-shadow: none; background: transparent; }
.embedded .status-btn:disabled, .embedded .assignee-btn:disabled { opacity: 1; cursor: default; }
.embedded .ticket-row .c-key { padding-left: 28px; }
.embedded .c-status .status-btn { padding: 0; }
.table-card.compact { --row-h: 30px; }
/* Touch tablets need a full 44px content line plus the row's hairline.
   Both densities keep hit targets inside their row; fine pointers stay dense. */
@media (min-width: 721px) and (pointer: coarse) {
  .table-card, .table-card.compact { --row-h: 45px; }
}
.tickets { width: 100%; border-collapse: separate; border-spacing: 0; table-layout: fixed; font-size: 13.5px; }
.tickets:focus-visible { box-shadow: none; }
/* Keyboard cursor: the app focus ring around the whole row. A shadow on the
   tr itself is clipped by the card, so the ring is drawn just inside the row. */
.tickets:focus .ticket-row.cursor { position: relative; z-index: 1; }
.tickets:focus .ticket-row.cursor td:first-child::after {
  content: ''; position: absolute; z-index: 4; left: 3px; top: 2px; bottom: 2px; width: calc(100% - 6px);
  border-radius: 6px; pointer-events: none; box-shadow: var(--focus-ring);
}
/* Column widths live on the colgroup (saved per person); Title takes the rest. */
thead th {
  position: sticky; top: var(--toolbar-h, 0px); z-index: 2; height: 34px; padding: 0 12px; text-align: left; font-weight: 500;
  background: var(--surface-raised-2); border-bottom: 1px solid var(--line-2);
  -webkit-backdrop-filter: blur(14px) saturate(1.15); backdrop-filter: blur(14px) saturate(1.15);
}
thead th:first-child { padding-left: 18px; }
.th-sort, .th-label { display: inline-flex; align-items: center; gap: 6px; height: 26px; margin: 0 -6px; padding: 0 6px; border: 0; border-radius: 6px; background: transparent; font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.th-sort:hover { color: var(--ink); background: var(--row-hover); }
.th-sort:active { background: var(--row-selected); }
.th-sort.on { color: var(--teal-ink); }
.th-sort:focus-visible { box-shadow: var(--focus-ring); }
.sort-mark { display: inline-flex; align-items: center; gap: 1px; min-width: 11px; }
.sort-index { font-size: 9px; letter-spacing: 0; }
.default-sort { opacity: .55; }
thead th.end { text-align: right; }
thead th.end .th-sort { flex-direction: row-reverse; }
/* The resize handle sits on the header's right edge; it shows on hover and focus. */
.col-resize { position: absolute; top: 6px; bottom: 6px; right: 0; z-index: 3; width: 9px; cursor: col-resize; touch-action: none; outline: none; }
.col-resize::after { content: ''; position: absolute; top: 0; bottom: 0; left: 8px; width: 1px; background: var(--line-2); opacity: 0; transition: opacity .12s ease; }
thead th:hover .col-resize::after { opacity: 1; }
.col-resize:hover::after, .col-resize:active::after, .col-resize:focus-visible::after { opacity: 1; width: 2px; left: 7px; background: var(--teal); }

/* Every cell centres one flex line in the row, so text, icons and chips share a baseline. */
.ticket-row { height: var(--row-h); cursor: default; scroll-margin-top: calc(var(--toolbar-h, 0px) + 40px); scroll-margin-bottom: 24px; }
.ticket-row { --recurrence-row-tint: transparent; }
@media (hover: hover) { .ticket-row:hover { --recurrence-row-tint: var(--row-hover); } }
.ticket-row.selected, .ticket-row.cursor, .ticket-row.open, .ticket-row.drop-target { --recurrence-row-tint: var(--row-selected); }
.ticket-row td { height: var(--row-h); padding: 0 12px; border-bottom: 1px solid var(--line); vertical-align: middle; }
.ticket-row td:first-child { padding-left: 18px; }
.cell { display: flex; align-items: center; gap: 8px; min-width: 0; height: calc(var(--row-h) - 1px); line-height: 18px; white-space: nowrap; }
.c-updated .cell, .c-created .cell, .c-estimate .cell, .c-progress .cell, .c-eta .cell, .c-tokens .cell, .c-list-cost .cell, .c-paid .cell { justify-content: flex-end; }
@media (hover: hover) { .ticket-row:hover td { background: var(--row-hover); } }
.ticket-row.cursor td, .ticket-row.open td { background: var(--row-selected); }
/* The ticket shown in the panel also carries a hairline ring in the row's own shape (no edge accents, rule 11). */
.ticket-row.open { outline: 1px solid var(--chip-teal-line); outline-offset: -1px; }
tbody.dim { opacity: .55; }
tbody:last-of-type .ticket-row:last-child td { border-bottom: 0; }

/* ---------- Selection: a checkbox leads each row; it shows on hover and while a selection exists ---------- */
.selectable thead th:first-child, .selectable .ticket-row td:first-child { padding-left: 10px; }
.selectable .c-key .cell { gap: 10px; }
.row-check { flex-shrink: 0; width: 16px; height: 16px; margin: 0; accent-color: var(--teal); opacity: 0; transition: opacity .1s ease; cursor: pointer; }
.head-check { margin-right: 10px; vertical-align: middle; }
.ticket-row:hover .row-check, .ticket-row.selected .row-check, .ticket-row.cursor .row-check, .selecting .row-check, thead th:hover .head-check, .head-check.shown, .row-check:focus-visible { opacity: 1; }
.row-check:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
@media (hover: none) { .row-check { opacity: 1; } .table-card:not(.selecting) .row-check { display: none; } .selectable:not(.selecting) .ticket-row td:first-child { padding-left: 18px; } }
@media (max-width: 720px) { .table-card:not(.selecting) .row-check { display: none; } .row-check { opacity: 1; } }
.ticket-row.selected td { background: var(--row-selected); }
.ticket-row.selected .key { color: var(--teal-ink); }
.ticket-row.selected { outline: 1px solid var(--chip-teal-line); outline-offset: -1px; }

/* ---------- Outline ---------- */
.tree { position: relative; align-self: stretch; flex-shrink: 0; margin: -1px 0 -1px -4px; }
.guide { position: absolute; top: 0; bottom: -1px; width: 18px; pointer-events: none; }
.guide.line::before, .guide.elbow::before { content: ''; position: absolute; left: 8.5px; top: 0; bottom: 0; width: 1px; background: var(--line-2); }
.guide.elbow.last::before { bottom: 50%; }
.guide.elbow::after { content: ''; position: absolute; left: 8.5px; top: 50%; width: 9px; height: 1px; background: var(--line-2); }
.twisty { position: absolute; top: 50%; left: var(--twisty-left); display: grid; place-items: center; width: 18px; height: 22px; margin-top: -11px; padding: 0; border: 0; border-radius: 5px; background: transparent; color: var(--ink-3); }
.twisty svg { transition: transform .15s ease; }
.twisty[aria-expanded="true"] svg { transform: rotate(90deg); }
@media (hover: hover) { .twisty:hover { color: var(--ink); background: var(--row-selected); } }
.twisty-spacer { display: none; }
.ticket-row.tree-row.epic .title-link { font-weight: 650; }
.ticket-row.top td { border-top: 1px solid var(--line); }
tbody .ticket-row.top:first-child td { border-top: 0; }
/* Waiting live updates: a quiet teal pill in the Title header (AEON-326). */
.live-pill {
  display: inline-flex; align-items: center; gap: 5px; max-width: calc(100% - 64px); height: 22px; margin-left: 12px; padding: 0 10px 0 8px; border: 0; border-radius: 999px;
  vertical-align: middle; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink);
  font: 500 12px/1 var(--font); letter-spacing: 0; text-transform: none; white-space: nowrap; overflow: hidden;
}
.live-pill b { font-weight: 650; }
.live-pill .live-count { overflow: hidden; text-overflow: ellipsis; }
.live-pill .live-dot { opacity: .6; }
.live-pill:hover { background: var(--row-selected); }
.live-pill:focus-visible { box-shadow: var(--focus-ring); }
@media (prefers-reduced-motion: no-preference) {
  .live-pill { animation: live-pill-in .2s ease-out; }
  @keyframes live-pill-in { from { opacity: 0; transform: translateY(-3px); } to { opacity: 1; transform: none; } }
}
/* A row waiting for Show: dimmed, with its label at full strength (AEON-326). */
.ticket-row.stale td:not(.c-title):not(.c-key) > .cell, .ticket-row.stale .c-key .key, .ticket-row.stale .c-title .cell > :not(.live-label) { opacity: .45; }
.live-label { flex-shrink: 0; height: 20px; padding: 0 8px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font-size: 11.5px; font-weight: 600; line-height: 20px; white-space: nowrap; }
/* Someone else changed the row: a brief full tint that fades. */
@media (prefers-reduced-motion: no-preference) {
  .ticket-row.live-flash td { animation: row-live 2s ease-out; }
  @keyframes row-live { from { background-color: var(--row-selected); } to { background-color: transparent; } }
}
@media (prefers-reduced-motion: reduce) { .ticket-row.live-flash td { background-color: var(--row-hover); } }
.ticket-row.dimmed .key, .ticket-row.dimmed .title-link, .ticket-row.dimmed .kind-glyph, .ticket-row.dimmed .c-status .cell, .ticket-row.dimmed .c-prio .cell, .ticket-row.dimmed .c-assignee .cell, .ticket-row.dimmed .ticket-workers, .ticket-row.dimmed time { opacity: .5; }
.epic-progress { display: inline-flex; align-items: center; gap: 8px; flex-shrink: 0; margin-left: auto; padding-left: 12px; }
.epic-progress .bar { width: 64px; height: 5px; }
.epic-progress .mono { min-width: 38px; font-size: 11px; color: var(--ink-2); text-align: right; }
.ticket-row[draggable="true"] { cursor: grab; }
.ticket-row.dragging td { opacity: .45; }
/* A drop target is outlined all round. */
.ticket-row.drop-target td, .outline-group.drop-target th { background: var(--row-selected); }
.ticket-row.drop-target { outline: 2px solid var(--teal); outline-offset: -2px; }
.outline-group.drop-target th { box-shadow: inset 0 0 0 2px var(--teal); }
.drop-pill { display: inline-flex; align-items: center; gap: 5px; flex-shrink: 0; height: 22px; margin-left: auto; padding: 0 10px; border-radius: 999px; background: linear-gradient(180deg, #1a8683, #0e6f6c); color: #fff; font-size: 11.5px; font-weight: 600; box-shadow: 0 6px 14px -8px rgba(14, 111, 108, .8); }
.outline-group th { top: calc(var(--toolbar-h, 0px) + 35px); }
.more-row td { height: 34px; padding: 0 12px; border-bottom: 1px solid var(--line); }
.more-btn { height: 26px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; }
.more-btn:hover { background: var(--row-hover); }
.more-btn:focus-visible { box-shadow: var(--focus-ring); }

.key-btn { display: inline-flex; align-items: center; height: 22px; margin: 0 -5px; padding: 0 5px; border: 0; border-radius: 6px; background: transparent; cursor: copy; }
.key-btn:hover { color: var(--teal-ink); background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
.assignee-btn { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; height: 26px; margin-left: -8px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); white-space: nowrap; }
.assignee-btn:hover:not(:disabled) { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
.assignee-btn:disabled { opacity: 1; }
.q-pos { color: var(--ink-3); font: 500 11.5px/18px var(--mono); }
.key { font: 500 11.5px/18px var(--mono); color: var(--ink-2); letter-spacing: .01em; font-variant-ligatures: none; }
.ticket-row.open .key, .ticket-row.cursor .key { color: var(--teal-ink); }
.kind-glyph { color: var(--ink-3); }
.kind-glyph.epic { color: var(--gold); }
.title-link { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--ink); text-decoration: none; }
.title-text { display: block; overflow: hidden; text-overflow: ellipsis; }
.ticket-row.epic .title-link { font-weight: 650; }
.title-link:focus-visible { box-shadow: none; }
.child-count { flex-shrink: 0; height: 17px; padding: 0 6px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 10.5px; line-height: 17px; color: var(--ink-2); }
/* Parent chips stay quiet: tint only, 12px, capped; the title keeps the space first. */
.parent-chip { display: inline-flex; align-items: center; gap: 5px; flex: 0 3 auto; min-width: 0; max-width: 180px; height: 20px; padding: 0 8px; border-radius: 999px; background: var(--code-bg); color: var(--ink-3); font-size: 12px; line-height: 20px; }
.parent-chip.epic { min-width: 64px; }
.parent-chip:not(.epic) { flex-shrink: 0; }
.parent-chip.epic svg { color: var(--gold); opacity: .85; }
.parent-chip .mono { font-size: 11px; }
.parent-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* Row actions float over the end of the title cell instead of reserving space in every
   row; the title fades out beneath them, whatever the row tint underneath. */
td.c-title { position: relative; overflow: hidden; }
/* Let the arrow's invisible reach cross the cell edge on touch layouts.
   The title link and phone title text retain their own clipping. */
@media (max-width: 720px), (pointer: coarse) { td.c-title { overflow: visible; } }
.row-actions { position: absolute; top: 50%; right: 8px; display: inline-flex; gap: 2px; transform: translateY(-50%); visibility: hidden; }
/* The parent chip steps out entirely while row actions show, so it is never clipped. */
@media (hover: hover) { .ticket-row:hover .parent-chip { opacity: 0; } }
.ticket-row.cursor .parent-chip, td.c-title:focus-within .parent-chip { opacity: 0; }
/* Release work keeps its context visible; its rows have no inline action tray. */
.embedded .ticket-row.cursor .parent-chip, .embedded .ticket-row:hover .parent-chip, .embedded td.c-title:focus-within .parent-chip { opacity: 1; }
/* Hover actions float at the end of the title. Reserve their width on opening,
   so hovering or focusing the recurring marker never shrinks the title link (two 24px
   buttons, the 2px gap, and the 8px inset). With the cell's 12px padding that
   leaves the cue 12px clear of Copy. Phones hide the actions. */
@media (min-width: 721px) {
  .title-cell { padding-right: 58px; }
  .ticket-row:hover .title-cell,
  .ticket-row.cursor .title-cell,
  td.c-title:focus-within .title-cell {
    -webkit-mask-image: none !important;
    mask-image: none !important;
  }
}
.ticket-row:hover .row-actions, .ticket-row.cursor .row-actions, .row-actions:focus-within { visibility: visible; }
.row-actions :deep(.icon-btn) { width: 24px; height: 24px; color: var(--ink-3); }
.row-actions :deep(.icon-btn):hover { color: var(--teal-ink); }
.compact .row-actions :deep(.icon-btn) { width: 22px; height: 22px; }
@media (min-width: 721px) and (pointer: coarse) {
  /* Keep the compact visuals, but reserve each full 44px reach plus the
     existing 2px separation before interaction, as on fine pointers above.
     Padding contains the outer edges as well. Touch can set :hover during
     a hold; reserving space only then shrinks the name under the pointer. */
  .row-actions { gap: 22px; padding: 0 10px; }
  .compact .row-actions { gap: 24px; padding: 0 11px; }
  /* Reserve the touch action reach before hover/focus so the recurring
     marker never changes the title width when those actions appear. */
  .title-cell { padding-right: 98px; }
}

.status-btn { display: inline-flex; align-items: center; gap: 8px; max-width: 100%; height: 26px; margin-left: -8px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--ink); font-size: 13px; line-height: 18px; white-space: nowrap; }
.status-btn span { overflow: hidden; text-overflow: ellipsis; }
.status-btn .q-pos { flex: none; margin-left: -3px; }
.status-btn:hover, .status-btn[aria-expanded="true"] { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
.status-btn:active { background: var(--row-selected); }
.status-btn:focus-visible { box-shadow: var(--focus-ring); }
.compact .status-btn { height: 22px; }
.c-prio .cell, .c-assignee .cell { color: var(--ink-2); font-size: 13px; }
.prio-label, .person-name { overflow: hidden; text-overflow: ellipsis; }
.person-name { color: var(--ink); }
.owner { display: inline-flex; align-items: center; gap: 8px; min-width: 0; flex: 1 1 auto; }
.owner .person-name { min-width: 0; }
.c-assignee .ticket-workers { flex: 1 1 auto; min-width: 0; }
/* A person and a live worker share the cell: the person keeps the avatar (name on hover), the worker keeps its name. */
.owner.with-workers { flex: 0 0 auto; }
.owner.with-workers .person-name { display: none; }
.c-assignee .cell:has(.owner) .ticket-workers { flex: 0 1 auto; }
/* The title ellipsizes. The cue keeps its full name and does not shrink. */
.title-workers { flex: 0 0 auto; min-width: max-content; max-width: none; }
.empty { color: var(--ink-3); }
.c-updated time, .c-created time { color: var(--ink-2); font-size: 12.5px; font-variant-numeric: tabular-nums; }
.c-estimate .mono.estimate-draft { color: var(--ink-3); }
.estimate-mark { font-size: 10px; }
.c-estimate .mono { font-size: 12px; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.progress-read { display: inline-flex; align-items: center; justify-content: flex-end; gap: 6px; min-width: 0; max-width: 100%; color: var(--ink-2); }
.progress-read .bar { width: 36px; height: 4px; flex: none; box-shadow: none; }
.progress-read .bar > i { box-shadow: none; }
.progress-read .pct { flex: none; font: 500 11px/1 var(--mono); font-variant-numeric: tabular-nums; font-variant-ligatures: none; color: var(--ink-2); }
.progress-read.stale, .progress-read.stale .pct { color: var(--ink-3); }
.progress-read.stale .bar > i { opacity: .45; }
/* The percent lives in Progress once that column is on; ETA keeps the time. */
.table-card:has(col.c-progress) .c-eta :deep(.pct) { display: none; }
th.c-progress, th.c-eta, td.c-progress, td.c-eta, th.c-tokens, td.c-tokens, th.c-list-cost, td.c-list-cost, th.c-paid, td.c-paid { padding-left: 8px; padding-right: 8px; }
th.c-progress .th-sort, th.c-eta .th-sort, th.c-tokens .th-sort, th.c-list-cost .th-sort, th.c-paid .th-sort { letter-spacing: .08em; }
.epic-cell { display: inline-flex; align-items: center; gap: 6px; min-width: 0; color: var(--ink-2); font-size: 12.5px; }
.epic-name { overflow: hidden; text-overflow: ellipsis; }
.cost-cell { display: inline-flex; align-items: center; gap: 6px; min-width: 0; color: var(--ink-2); font-size: 12.5px; }
.cost-glyph { flex-shrink: 0; color: var(--ink-3); }
.cost-name { overflow: hidden; text-overflow: ellipsis; }
.group-dot { width: 8px; height: 8px; margin: 0 3px; }
.release-chip { overflow: hidden; text-overflow: ellipsis; max-width: 100%; padding: 2px 7px; border: 0; border-radius: 6px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font: inherit; font-size: 11.5px; font-variant-ligatures: none; cursor: pointer; }
button.release-chip { display: inline-flex; align-items: center; height: 22px; }
button.release-chip.bare { background: transparent; box-shadow: none; color: var(--ink-3); }
button.release-chip:focus-visible { box-shadow: var(--focus-ring); }
.tag-cell { gap: 4px; overflow: hidden; }
.tag-chip { display: inline-flex; flex-shrink: 1; align-items: center; gap: 5px; min-width: 0; max-width: 100%; height: 20px; padding: 0 7px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font-size: 11.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
/* The classic tag colours as a small dot; the name carries the meaning. */
.tag-dot { flex-shrink: 0; width: 6px; height: 6px; border-radius: 50%; background: var(--ink-3); }
.tag-dot[data-color="blue"] { background: #4f86c6; }
.tag-dot[data-color="red"] { background: #d0625b; }
.tag-dot[data-color="green"] { background: #4f9e6f; }
.tag-dot[data-color="yellow"], .tag-dot[data-color="orange"] { background: var(--gold); }
.tag-dot[data-color="purple"] { background: #8a6cc2; }
.tag-dot[data-color="teal"], .tag-dot[data-color="cyan"] { background: var(--teal); }
.tag-dot[data-color="pink"] { background: #c7679a; }
.tag-more { flex-shrink: 0; color: var(--ink-3); font-size: 11px; }
.c-prio.narrow .prio-label { display: none; }

.group-row th { position: sticky; top: calc(var(--toolbar-h, 0px) + 35px); z-index: 1; height: 36px; padding: 0 12px 0 8px; text-align: left; font-weight: 400; background: var(--surface-raised-2); border-bottom: 1px solid var(--line); -webkit-backdrop-filter: blur(12px); backdrop-filter: blur(12px); }
.group-head { display: flex; align-items: center; gap: 8px; min-width: 0; }
.group-toggle { display: grid; place-items: center; width: 24px; height: 24px; padding: 0; border: 0; border-radius: 6px; background: transparent; color: var(--ink-3); }
.group-toggle:hover { background: var(--row-hover); color: var(--ink); }
.group-toggle:focus-visible, .group-epic:focus-visible { box-shadow: var(--focus-ring); }
.group-row.collapsed .group-toggle svg { transform: rotate(-90deg); }
.group-label { font-size: 13px; font-weight: 650; color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.group-label.muted { color: var(--ink-2); }
.group-count { font-size: 11px; color: var(--ink-3); }
.epic-glyph { color: var(--gold); }
.group-epic { display: inline-flex; align-items: center; gap: 10px; min-width: 0; height: 26px; padding: 0 8px; margin-left: -4px; border: 0; border-radius: 6px; background: transparent; }
.group-epic:hover { background: var(--row-hover); }
.group-epic.cursor, .group-epic.open { background: var(--row-selected); }
.group-epic.open { box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.group-epic .key { color: var(--ink-2); }

.ghost td { border-bottom-color: var(--line); }
.sk-key { width: 70px; }
.sk-title { height: 10px; }
.sk-dot { flex-shrink: 0; width: 12px; height: 12px; border-radius: 50%; box-shadow: inset 0 0 0 1.6px var(--skeleton); }
.sk-word { width: 58px; }
.sk-time { width: 44px; }

.create-body :deep(.create-row) { scroll-margin-top: calc(var(--toolbar-h, 0px) + 40px); }
@media (max-width: 720px) { .create-body { display: block; } }
.state { display: grid; justify-items: center; gap: 8px; padding: 56px 24px 64px; text-align: center; }
.state-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 6px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.state-icon.danger { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.state h2 { font-size: 17px; color: var(--ink); }
.state p { max-width: 420px; font-size: 13.5px; color: var(--ink-2); }
.state .btn, .state-actions { margin-top: 10px; }
.state-actions { display: flex; gap: 8px; }
.state-actions .btn { margin: 0; }
.sentinel { height: 1px; }
.list-foot { display: flex; align-items: center; justify-content: center; gap: 10px; min-height: 44px; font-size: 12.5px; color: var(--ink-2); border-top: 1px solid var(--line); }
.list-foot.end { font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.list-foot.error { color: var(--danger); }
/* The toolbar carries the count on wider screens; phones end the list with it. */
@media (min-width: 721px) { .list-foot.end.phone-only { display: none; } }
.spinner { width: 14px; height: 14px; border-radius: 50%; border: 1.8px solid var(--line-2); border-top-color: var(--teal); }
@media (prefers-reduced-motion: no-preference) { .spinner { animation: spin .8s linear infinite; } @keyframes spin { to { transform: rotate(360deg); } } }

/* The table reflows to its own width (the docked panel narrows it): Title shrinks first,
   then Assignee and Updated step aside. */

/* Epic progress gives the title room first: the bar goes, the count stays. */
@container tickets (max-width: 900px) { .epic-progress .bar { display: none; } .epic-progress { padding-left: 8px; } }
/* Below ~820px Priority keeps only its icon (label in the tooltip); Status keeps its label. */
@container tickets (max-width: 820px) { th.c-prio .th-sort { letter-spacing: .06em; } .prio-label { display: none; } }
/* A narrow table (the docked panel beside it) drops the epic chip rather than cutting it to a stub. */
@media (min-width: 721px) { @container tickets (max-width: 1040px) { .title-cell:has(.title-workers) .parent-chip.epic { display: none; } .parent-chip.epic { min-width: 96px; } } }

.c-check { display: none; }
.phone-check { display: none; }
@media (max-width: 720px) {
  .table-card { border-radius: 14px; overflow: visible; }
  .tickets, .tickets tbody { display: block; }
  .tickets thead { display: none; }
  .ticket-row { -webkit-touch-callout: none; }
  .ticket-row {
    display: grid; grid-template-columns: auto auto minmax(0, 1fr) auto; grid-template-areas: "key status prio updated" "title title title title";
    align-items: center; gap: 5px 10px; height: auto; padding: 10px 14px 11px; border-bottom: 1px solid var(--line);
  }
  /* The compact status visual is 2px shorter; leave its 44px reach inside the card. */
  .table-card.compact .ticket-row { padding-top: 11px; }
  /* Native boxes stay for the wide layout. A phone uses the round mark instead. */
  .row-check { display: none; }
  .table-card.selecting .ticket-row {
    grid-template-columns: 44px auto auto minmax(0, 1fr) auto;
    grid-template-areas: "check key status prio updated" "check title title title title";
    padding-left: 2px; column-gap: 6px;
  }
  .c-check { display: flex; grid-area: check; align-self: center; justify-content: center; }
  .phone-check {
    display: grid; place-items: center; width: 44px; height: 44px; margin: 0; padding: 0; border: 0; border-radius: 50%;
    background: transparent; color: #fffefa;
  }
  .phone-check .mark {
    display: grid; place-items: center; width: 22px; height: 22px; border-radius: 50%;
    box-shadow: inset 0 0 0 1.5px var(--ink-2); background: var(--surface);
  }
  .phone-check[aria-checked="true"] .mark { background: linear-gradient(180deg, #1a8683, #0e6f6c); box-shadow: none; }
  .phone-check:focus-visible { box-shadow: var(--focus-ring); }
  .ticket-row.selected { background: var(--row-selected); }
  .ticket-row td { display: block !important; height: auto; padding: 0; border: 0; background: none !important; box-shadow: none !important; }
  .ticket-row td:first-child { padding-left: 0; }
  .ticket-row .cell { height: auto; }
  .ticket-row.cursor, .ticket-row.open { background: var(--row-selected); }
  .tickets colgroup { display: none; }
  .c-key { grid-area: key; } .c-status { grid-area: status; } .c-prio { grid-area: prio; } .c-updated { grid-area: updated; }
  /* An hour estimate sits at the end of the title line. An empty cell stays off the card. */
  .c-estimate { grid-area: estimate; justify-self: end; align-self: center; min-width: 0; }
  .c-estimate .cell { height: auto; }
  .ticket-row:not(:has(.c-estimate .mono)) .c-estimate { display: none !important; }
  .table-card:not(.customised) .ticket-row:has(.c-estimate .mono) { grid-template-areas: "key status prio updated" "title title title estimate"; }
  .table-card.selecting:not(.customised) .ticket-row:has(.c-estimate .mono) { grid-template-areas: "check key status prio updated" "check title title title estimate"; }
  .c-progress { grid-area: progress; justify-self: end; min-width: 0; }
  .ticket-row:not(:has(.progress-read)) .c-progress { display: none !important; }
  .table-card:not(.customised) .ticket-row:has(.progress-read) { grid-template-areas: "key status prio updated" "title title title progress"; }
  .table-card.selecting:not(.customised) .ticket-row:has(.progress-read) {
    grid-template-columns: 44px auto auto minmax(0, 1fr) auto;
    grid-template-areas: "check key status prio updated" "check title title title progress";
  }
  /* With both, the title spans two lines beside progress over the estimate. */
  .table-card:not(.customised) .ticket-row:has(.c-estimate .mono):has(.progress-read) { grid-template-areas: "key status prio updated" "title title title progress" "title title title estimate"; }
  .table-card.selecting:not(.customised) .ticket-row:has(.c-estimate .mono):has(.progress-read) { grid-template-areas: "check key status prio updated" "check title title title progress" "check title title title estimate"; }
  /* A ready time shows where Updated sits; it is the fresher answer to "when". */
  .ticket-row:not(:has(.eta-cell)) .c-eta, .ticket-row:has(.eta-cell) .c-updated { display: none !important; }
  .c-eta { grid-area: updated; justify-self: end; min-width: 0; }
  .c-title { grid-area: title; }
  .ticket-row .c-assignee { display: none !important; }
  .embedded .ticket-row { grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: 'key status' 'title title' 'estimate assignee' 'progress eta' !important; padding-left: 28px; }
  .embedded .ticket-row .c-key { padding-left: 0; }
  .embedded .ticket-row .c-assignee { display: block !important; grid-area: assignee; justify-self: end; }
  .embedded .ticket-row .c-progress { display: block !important; justify-self: start; }
  .embedded .ticket-row .c-estimate { display: block !important; justify-self: start; }
  .embedded .ticket-row .c-eta { display: block !important; grid-area: eta; }
  .title-cell { align-items: flex-start; flex-wrap: wrap; gap: 4px 8px; white-space: normal; }
  .title-cell .kind-glyph, .title-cell .ticket-type-icon { margin-top: 2px; }
  /* The link keeps its 44 px reach; the two-line clamp sits on the text inside it,
     so a third line never shows through the reach below (AEON-140). */
  /* The whole card opens the ticket, so the title needs no padded reach of its own. */
  .title-link { flex: 1 1 calc(100% - 30px); white-space: normal; font-size: 14.5px; line-height: 1.35; }
  .title-text { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .parent-chip { max-width: calc(100% - 22px); margin-left: 22px; }
  .child-count { display: none; }
  .row-actions { display: none; }
  .ticket-row .title-cell { -webkit-mask-image: none !important; mask-image: none !important; }
  .ticket-row.tree-row {
    padding-left: calc(14px + var(--depth, 0) * 10px);
    background-image: repeating-linear-gradient(90deg, var(--line-2) 0 1px, transparent 1px 10px);
    background-size: calc(var(--depth, 0) * 10px) 100%; background-position: 10px 0; background-repeat: no-repeat;
  }
  .ticket-row.tree-row.cursor, .ticket-row.tree-row.open { background-color: var(--row-selected); }
  .tree { width: 0 !important; margin: 0; align-self: flex-start; }
  .guide { display: none; }
  /* The chevron area sits left of the title with a generous tap target. */
  .tree-row .title-cell { position: relative; padding-left: 0; }
  /* Centre the shared hit area on the arrow; desktop indentation is already
     represented by the card's padding, so it must not offset the phone arrow. */
  .twisty { position: relative; inset: auto; width: 36px; height: 36px; margin: -8px 0 -8px -10px; }
  .twisty-spacer { display: block; flex-shrink: 0; width: 26px; }
  .tree-row .tree { display: contents; }
  .tree-row .title-link { flex-basis: calc(100% - 60px); }
  .epic-progress { flex-basis: 100%; margin-left: 22px; padding-left: 0; }
  .epic-progress .bar { display: block; width: 96px; }
  .ticket-row.top td { border-top: 0; }
  .more-row { display: block; padding: 6px 14px; }
  .more-row td { display: block; height: auto; border: 0; padding: 0; }
  .status-btn { height: 24px; margin-left: 0; padding: 0 8px 0 6px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 12px; }
  .prio-label { display: none; }
  /* A phone card shows priority only when there is one. */
  .c-prio .empty { display: none; }
  .ghost { display: grid; }
  .group-row, .group-row th { display: block; }
  .group-row th { top: var(--toolbar-h, 0px); padding: 0 10px; }
  .group-head { height: 40px; }
  .table-card.selecting .ticket-row.tree-row { padding-left: calc(2px + var(--depth, 0) * 10px); }
  /* Saved columns read as labelled metadata below Key and Title, in the chosen
     order. Automatic cards retain their compact layout above. No optional
     value is hidden or placed in an implicit off-screen grid column. */
  .table-card.customised .tickets .ticket-row {
    grid-template-columns: minmax(0, 1fr); grid-template-areas: none;
  }
  .table-card.customised.selecting .tickets .ticket-row { grid-template-columns: 44px minmax(0, 1fr); }
  .table-card.customised .ticket-row td { grid-area: auto; min-width: 0; justify-self: stretch; }
  .table-card.customised.selecting .ticket-row td { grid-column: 2; }
  .table-card.customised.selecting .ticket-row .c-check { grid-column: 1; grid-row: 1 / span 2; }
  .table-card.customised .ticket-row td[data-column-label] {
    display: grid !important; grid-template-columns: minmax(7em, 30%) minmax(0, 1fr); align-items: center; gap: 8px;
  }
  .table-card.customised td[data-column-label]::before { content: attr(data-column-label); color: var(--ink-3); font-size: 11.5px; }
  .table-card.customised td[data-column-label] .cell { min-width: 0; flex-wrap: wrap; justify-content: flex-start; white-space: normal; }
  .table-card.customised .c-prio .empty, .table-card.customised .prio-label { display: inline; }
  .table-card.customised .cost-name, .table-card.customised .epic-name, .table-card.customised .person-name { white-space: normal; overflow-wrap: anywhere; }
  @media (pointer: coarse) {
    .table-card.customised .status-btn, .table-card.customised .assignee-btn, .table-card.customised button.release-chip { min-height: 44px; }
  }
}
@media (min-width: 721px) {
  .table-card.overflowing { overflow-x: auto; overscroll-behavior-x: contain; }
  .table-card.overflowing thead th { top: 0; }
}
</style>

<style scoped>
.embedded .c-key { position: relative; }
.embedded .c-title { padding-right: 44px; }
@media (pointer: coarse) { .embedded .ticket-row .c-key { padding-left: 48px; } }
</style>
