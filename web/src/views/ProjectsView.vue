<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useProjects, type Project } from '../stores/projects'
import { useProjectGroups } from '../stores/projectGroups'
import { useSession } from '../stores/session'
import { useLiveAgents } from '../stores/liveAgents'
import { activeByProject, activeSessions, liveChanges, liveSummary, type LiveAgent } from '../lib/liveAgents'
import { onPreferenceFailure, usePreference } from '../lib/preferences'
import { plural } from '../lib/work'
import { toast, type ToastAction } from '../lib/toast'
import { opensRowMenu, type RowAction, type RowMenuAnchor } from '../lib/rowActions'
import { ARCHIVED, NO_GROUP, bucket, isShared, isUserGroup, nameProblem, showsHeaders } from '../lib/projectGroups'
import { PROJECT_COLUMNS, chosenProjectColumns, customisedProjectColumns, fittingProjectColumns, projectColumnOrder, type ProjectColumnId, type ProjectColumnPrefs } from '../lib/projectColumns'
import { byRank, keepOrder, nearestCell, place, prunedOrder, sameOrder } from '../lib/projectOrder'
import AppIcon, { type IconName } from '../components/AppIcon.vue'
import WelcomeBlock from '../components/WelcomeBlock.vue'
import MorningBriefingReminder from '../components/MorningBriefingReminder.vue'
import FloatingPanel from '../components/work/FloatingPanel.vue'
import ColumnPicker from '../components/work/ColumnPicker.vue'
import RowMenu from '../components/business/RowMenu.vue'
import GroupChips from '../components/projects/GroupChips.vue'
import HiddenGroups from '../components/projects/HiddenGroups.vue'
import MoveToGroup, { type MoveOption } from '../components/projects/MoveToGroup.vue'
import ProjectBulkBar from '../components/projects/ProjectBulkBar.vue'
import ProjectCards from '../components/projects/ProjectCards.vue'
import ProjectList, { type ProjectSection } from '../components/projects/ProjectList.vue'

// Each sort has its icon; the Display button shows the current one (AEON-174).
type SortKey = 'activity' | 'name' | 'open' | 'progress' | 'custom'
const SORTS: { value: SortKey; label: string; phrase: string; icon: IconName }[] = [
  { value: 'activity', label: 'Last activity', phrase: 'last activity', icon: 'clock' },
  { value: 'name', label: 'Name', phrase: 'name', icon: 'sort-name' },
  { value: 'open', label: 'Open tickets', phrase: 'open tickets', icon: 'ticket' },
  { value: 'progress', label: 'Progress', phrase: 'progress', icon: 'progress' },
  { value: 'custom', label: 'Custom', phrase: 'custom order', icon: 'grip' },
]
const COLUMN_LABELS: Record<string, string> = { key: 'Key', project: 'Project', ...Object.fromEntries(PROJECT_COLUMNS.map(c => [c.id, c.label])) }

const store = useProjects()
const groups = useProjectGroups()
const live = useLiveAgents()
const session = useSession()
const route = useRoute()
const router = useRouter()
// The person's view of this page: List or Cards and the list's columns. Their own
// order of projects (the Custom sort) has a key of its own, so saving one never
// overwrites the other.
type PagePrefs = { view?: 'list' | 'cards'; columns?: ProjectColumnPrefs }
const pagePref = usePreference<PagePrefs>('projects')
const ORDER_KEY = 'projects:order'
const orderPref = usePreference<{ ids?: string[] }>(ORDER_KEY)
const pageReady = ref(false)
void Promise.all([pagePref.ready, orderPref.ready]).then(() => { pageReady.value = true })
const view = computed<'list' | 'cards'>(() => pagePref.value.value?.view === 'cards' ? 'cards' : 'list')
const columnPrefs = computed(() => pagePref.value.value?.columns ?? null)
function savePage(patch: PagePrefs) {
  const next = { ...(pagePref.value.value ?? {}), ...patch }
  if ('columns' in patch && !patch.columns) delete next.columns
  pagePref.save(next, 0)
}

const term = ref('')
const search = ref<HTMLInputElement>()
const page = ref<HTMLElement>()
const listCard = ref<HTMLElement>()
const now = ref(Date.now())
let clock: ReturnType<typeof setInterval> | undefined
let stopLive: (() => void) | undefined

// ---------- Sorting ----------
const routeSort = computed<SortKey>(() => SORTS.some(s => s.value === route.query.sort) ? route.query.sort as SortKey : 'activity')
// While a card is dragged, and until the address catches up after a drop, the
// sort is Custom already: the Display button says so the moment a drag starts.
const sortOverride = ref<SortKey | null>(null)
const sort = computed<SortKey>(() => sortOverride.value ?? routeSort.value)
const sortMeta = computed(() => SORTS.find(s => s.value === sort.value)!)
// The saved custom order, or the live one while a card is being dragged.
const savedOrder = computed(() => orderPref.value.value?.ids ?? [])
const dragOrder = ref<string[] | null>(null)
const byCustom = computed(() => byRank(new Map((dragOrder.value ?? savedOrder.value).map((id, i) => [id, i]))))
const compare: Record<SortKey, (a: Project, b: Project) => number> = {
  activity: (a, b) => Date.parse(b.last_activity) - Date.parse(a.last_activity),
  name: (a, b) => a.title.localeCompare(b.title, 'en', { sensitivity: 'base' }),
  open: (a, b) => (b.open + b.in_progress) - (a.open + a.in_progress),
  progress: (a, b) => b.percent - a.percent || b.total - a.total,
  custom: (a, b) => byCustom.value(a, b),
}
const order = (a: Project, b: Project) => compare[sort.value](a, b) || compare.activity(a, b)
// Other sorts leave the custom order alone, so Custom brings it back later.
function chooseSort(value: SortKey) { sortOverride.value = null; void router.replace({ query: { ...route.query, sort: value === 'activity' ? undefined : value } }) }

// ---------- Groups ----------
const ready = computed(() => store.loaded && groups.ready && pageReady.value)
const defs = computed(() => groups.defs)
const matches = (project: Project) => {
  const needle = term.value.trim().toLowerCase()
  return !needle || `${project.routeKey} ${project.title} ${project.description}`.toLowerCase().includes(needle)
}
const everything = computed(() => bucket(store.projects, defs.value, p => groups.where(p)))
const totals = computed(() => new Map(everything.value.map(s => [s.group.id, s.items.length])))
const found = computed(() => bucket(store.projects.filter(matches), defs.value, p => groups.where(p), order))
const archivedCount = computed(() => totals.value.get(ARCHIVED) ?? 0)
const hasUserGroups = computed(() => defs.value.some(d => isUserGroup(d.id)))
const headers = computed(() => showsHeaders(defs.value, groups.hidden, archivedCount.value))
const filtering = computed(() => !!term.value.trim())
const sections = computed<ProjectSection[]>(() => found.value
  .filter(s => !groups.hidden.has(s.group.id))
  .filter(s => s.items.length || (isUserGroup(s.group.id) && !filtering.value))
  .map(s => ({ group: s.group, items: s.items, total: filtering.value ? s.items.length : totals.value.get(s.group.id) ?? 0, collapsed: headers.value && groups.collapsed.has(s.group.id) })))
const chips = computed(() => defs.value
  .filter(d => isUserGroup(d.id) || (d.id === NO_GROUP && hasUserGroups.value && ((totals.value.get(d.id) ?? 0) > 0 || groups.hidden.has(d.id))) || (d.id === ARCHIVED && archivedCount.value > 0))
  .map(group => ({ group, count: totals.value.get(group.id) ?? 0, hidden: groups.hidden.has(group.id) })))
const hiddenItems = computed(() => (filtering.value ? found.value : everything.value)
  .filter(s => groups.hidden.has(s.group.id) && s.items.length)
  .map(s => ({ id: s.group.id, name: s.group.name, count: s.items.length })))
// Every project on screen, in screen order: the keyboard and range selection walk it.
const onScreen = computed(() => sections.value.filter(s => !s.collapsed).flatMap(s => s.items))
const active = computed(() => store.projects.filter(project => !project.archived))
const openTotal = computed(() => active.value.reduce((sum, project) => sum + project.open + project.in_progress, 0))
function groupName(id: string) { return groups.def(id)?.name ?? 'No group' }
function to(project: Project) { return `/p/${encodeURIComponent(project.routeKey)}` }
function label(project: Project) {
  const working = liveSummary(activeSessions(live.forProject(project.id)))
  return `${project.routeKey} ${project.title}, ${project.open + project.in_progress} open, ${project.in_progress} in progress, ${project.done} done${working ? `, ${working}` : ''}${selected.value.has(project.id) ? ', selected' : ''}`
}
const validateName = (name: string, except?: string) => nameProblem(name, defs.value, except)

// ---------- Columns ----------
type PickerColumn = ProjectColumnId | 'key' | 'project'
const PINNED_COLUMNS: PickerColumn[] = ['key', 'project']
const own = (ids: PickerColumn[]) => ids.filter((id): id is ProjectColumnId => !PINNED_COLUMNS.includes(id))
const listWidth = ref(0)
let sizer: ResizeObserver | undefined
// Phones stack each row (progress, the three counts and the time always show).
const phoneQuery = window.matchMedia('(max-width: 760px)')
const phone = ref(phoneQuery.matches)
const phoneChange = () => { phone.value = phoneQuery.matches }
phoneQuery.addEventListener('change', phoneChange)
const columns = computed<ProjectColumnId[]>(() => phone.value ? ['open', 'doing', 'done', 'progress', 'activity'] : fittingProjectColumns(listWidth.value, columnPrefs.value))
const pickerColumns = computed(() => ({
  order: [...PINNED_COLUMNS, ...projectColumnOrder(columnPrefs.value)] as PickerColumn[],
  visible: chosenProjectColumns(columnPrefs.value) as PickerColumn[],
  customised: customisedProjectColumns(columnPrefs.value),
}))
function saveColumns(order: PickerColumn[], visible: PickerColumn[]) { savePage({ columns: { order: own(order), visible: own(visible) } }) }
function resetColumns() { savePage({ columns: undefined }) }
const displayAnchor = ref<HTMLElement | null>(null)
function closeDisplay(restore: boolean) { const anchor = displayAnchor.value; displayAnchor.value = null; if (restore) anchor?.focus() }
function setView(next: 'list' | 'cards') { if (next !== view.value) savePage({ view: next }) }

// ---------- Selection ----------
const selected = ref(new Set<string>())
let selectAnchor: string | null = null
function toggleSelect(id: string) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id); else next.add(id)
  selected.value = next
  selectAnchor = id
}
function selectRange(id: string) {
  const ids = onScreen.value.map(p => p.id)
  const from = selectAnchor ? ids.indexOf(selectAnchor) : -1, to = ids.indexOf(id)
  if (from === -1 || to === -1) { toggleSelect(id); return }
  const next = new Set(selected.value)
  for (let i = Math.min(from, to); i <= Math.max(from, to); i++) next.add(ids[i]!)
  selected.value = next
}
function clearSelection() { selected.value = new Set(); selectAnchor = null }
function selectAll() { selected.value = new Set(onScreen.value.map(p => p.id)); selectAnchor = onScreen.value[0]?.id ?? null }
// Projects that leave the screen (hidden, folded, filtered away) leave the selection.
watch(onScreen, list => {
  if (!selected.value.size) return
  const ids = new Set(list.map(p => p.id))
  const kept = [...selected.value].filter(id => ids.has(id))
  if (kept.length !== selected.value.size) selected.value = new Set(kept)
})
const selectedProjects = computed(() => onScreen.value.filter(p => selected.value.has(p.id)))
const busy = ref(false)

// ---------- Moving ----------
function subjectOf(list: Project[]) { return list.length === 1 ? list[0]!.title : plural(list.length, 'project') }
async function moveTo(list: Project[], target: string, options: { quiet?: boolean; focus?: boolean } = {}) {
  // Dropping a project where it already is changes nothing and says nothing.
  if (!list.length || list.every(p => groups.where(p) === target)) return
  busy.value = true
  const subject = subjectOf(list)
  const wasArchived = list.every(p => p.archived)
  try {
    const outcome = await groups.move(list, target)
    clearSelection()
    const name = groupName(target)
    const message = target === ARCHIVED ? `Archived ${subject}` : wasArchived ? `Restored ${subject} to ${name}` : `Moved ${subject} to ${name}`
    const actions: ToastAction[] = [{ label: 'Undo', run: () => void outcome.undo().then(() => toast(target === ARCHIVED ? `${subject} is back` : 'Move undone'), e => toast(`That could not be undone: ${reason(e)}`, { tone: 'error' })) }]
    if (groups.hidden.has(target)) actions.push({ label: 'Show', run: () => groups.setVisible([target], true) })
    if (!options.quiet) toast(groups.hidden.has(target) ? `${message} (hidden)` : message, { actions, timeout: 8000 })
    if (target === ARCHIVED || wasArchived) void store.load(true)
    // From the keyboard, focus follows the first project to its new place (when it is shown).
    if (options.focus) { await nextTick(); if (!focusItem(list[0]!.id)) search.value?.focus() }
  } catch (e) {
    toast(`${subject} stays where it is: ${reason(e)}`, { tone: 'error' })
  } finally { busy.value = false }
}
function reason(e: unknown) {
  const text = e instanceof Error ? e.message : String(e)
  if (/changed/i.test(text)) return 'it was changed elsewhere; try again.'
  if (/forbidden|admin/i.test(text) && !/Only a workspace admin/.test(text)) return 'only a workspace admin can do that.'
  return text.replace(/\.?$/, '.')
}
const moveDialog = ref<{ ids: string[]; anchor: HTMLElement | null } | null>(null)
const moveProjects = computed(() => (moveDialog.value?.ids ?? []).map(id => store.byId(id)).filter((p): p is Project => !!p))
const moveOptions = computed<MoveOption[]>(() => {
  const list = moveProjects.value
  const current = list.length ? new Set(list.map(p => groups.where(p))) : new Set<string>()
  return defs.value.map(group => {
    const here = current.size === 1 && current.has(group.id)
    const outsiders = isShared(group.id) && !groups.admin && list.some(p => groups.sharedOf(p.id) !== group.id)
    return { group, count: totals.value.get(group.id) ?? 0, current: here, reason: !here && outsiders ? 'Only a workspace admin can add projects to a shared group.' : undefined }
  })
})
function openMove(ids: string[], anchor: HTMLElement | null) {
  rowMenu.value = null
  moveDialog.value = { ids, anchor: anchor ?? document.querySelector<HTMLElement>(`[data-project-id="${CSS.escape(ids[0] ?? '')}"] .item-link`) }
}
function closeMove(restore: boolean) {
  const dialog = moveDialog.value
  moveDialog.value = null
  if (restore && dialog?.ids[0]) focusItem(dialog.ids[0])
}
async function chooseMove(target: string) {
  const list = moveProjects.value
  moveDialog.value = null
  await moveTo(list, target, { focus: true })
}
async function createAndMove(name: string) {
  const problem = validateName(name)
  if (problem) { toast(problem, { tone: 'error' }); return }
  const list = moveProjects.value
  moveDialog.value = null
  const id = groups.create(name)
  await moveTo(list, id, { focus: true })
}

// ---------- New group ----------
const createAnchor = ref<HTMLElement | null>(null)
const draftName = ref('')
const draftProblem = ref('')
function openCreate(anchor: HTMLElement) { createAnchor.value = createAnchor.value ? null : anchor; draftName.value = ''; draftProblem.value = '' }
function closeCreate(restore: boolean) { const anchor = createAnchor.value; createAnchor.value = null; if (restore) anchor?.focus() }
async function submitCreate() {
  const problem = validateName(draftName.value)
  if (problem) { draftProblem.value = problem; return }
  const id = groups.create(draftName.value)
  createAnchor.value = null
  if (groups.hidden.has(id)) groups.setVisible([id], true)
  await nextTick()
  focusGroup(id)
}

// ---------- Group menu ----------
const groupMenu = ref<{ id: string; anchor: HTMLElement } | null>(null)
const renaming = ref<string | null>(null)
const groupActions = computed<RowAction[]>(() => {
  const id = groupMenu.value?.id
  const group = id ? groups.def(id) : undefined
  if (!group) return []
  const order = defs.value.map(d => d.id), at = order.indexOf(group.id)
  const out: RowAction[] = []
  const adminOnly = group.kind === 'shared' && !groups.admin ? 'Only a workspace admin can change a shared group.' : undefined
  if (isUserGroup(group.id)) out.push({ id: 'rename', label: 'Rename', icon: 'edit', group: 1, reason: adminOnly })
  if (group.kind === 'personal' && groups.admin && groups.sharedAvailable) out.push({ id: 'share', label: 'Share with the workspace', icon: 'users', group: 1 })
  if (group.kind === 'shared' && groups.admin) out.push({ id: 'unshare', label: 'Stop sharing', icon: 'users', group: 1 })
  out.push({ id: 'up', label: 'Move up', icon: 'arrow-up', group: 2, reason: at === 0 ? 'It is already first.' : undefined })
  out.push({ id: 'down', label: 'Move down', icon: 'arrow-down', group: 2, reason: at === order.length - 1 ? 'It is already last.' : undefined })
  out.push({ id: 'hide', label: 'Hide', icon: 'eye-off', group: 3 })
  if (isUserGroup(group.id)) out.push({ id: 'delete', label: 'Delete group', icon: 'trash', group: 4, danger: true, reason: adminOnly })
  return out
})
function openGroupMenu(id: string, anchor: HTMLElement) { groupMenu.value = groupMenu.value?.id === id ? null : { id, anchor } }
function closeGroupMenu(restore: boolean) { const menu = groupMenu.value; groupMenu.value = null; if (restore) menu?.anchor.focus() }
function membersOf(id: string) { return everything.value.find(s => s.group.id === id)?.items.map(p => p.id) ?? [] }
async function groupAction(action: string) {
  const id = groupMenu.value?.id
  groupMenu.value = null
  if (!id) return
  const name = groupName(id)
  switch (action) {
    case 'rename': renaming.value = id; break
    case 'up': case 'down': stepGroup(id, action === 'up' ? -1 : 1); break
    case 'hide': groups.setVisible([id], false); toast(`${name} is hidden`, { action: { label: 'Show', run: () => groups.setVisible([id], true) } }); break
    case 'share':
      try {
        const undo = await groups.share(id, membersOf(id))
        toast(`${name} is shared with the workspace`, { timeout: 8000, action: { label: 'Undo', run: () => void undo().catch(e => toast(`Sharing could not be undone: ${reason(e)}`, { tone: 'error' })) } })
      } catch (e) { toast(`${name} was not shared: ${reason(e)}`, { tone: 'error' }) }
      break
    case 'unshare':
      try {
        const undo = await groups.unshare(id, membersOf(id))
        toast(`${name} is yours alone again`, { timeout: 8000, action: { label: 'Undo', run: () => void undo().catch(e => toast(`That could not be undone: ${reason(e)}`, { tone: 'error' })) } })
      } catch (e) { toast(`${name} is still shared: ${reason(e)}`, { tone: 'error' }) }
      break
    case 'delete': {
      const count = membersOf(id).length
      try {
        const undo = await groups.remove(id)
        toast(count ? `Deleted ${name} · ${plural(count, 'project')} moved to No group` : `Deleted ${name}`, { timeout: 8000, action: { label: 'Undo', run: () => void undo().then(() => toast(`${name} is back`), e => toast(`The group could not be brought back: ${reason(e)}`, { tone: 'error' })) } })
      } catch (e) { toast(`${name} was not deleted: ${reason(e)}`, { tone: 'error' }) }
      break
    }
  }
}
async function renameGroup(id: string, name: string | null) {
  renaming.value = null
  if (name !== null) {
    try { await groups.rename(id, name) } catch (e) { toast(`The group keeps its name: ${reason(e)}`, { tone: 'error' }) }
  }
  await nextTick()
  focusGroup(id)
}
function stepGroup(id: string, delta: -1 | 1) {
  groups.step(id, delta)
  void nextTick(() => focusGroup(id))
}
function toggleFold(id: string) { groups.toggleFold(id) }
function focusGroup(id: string) { page.value?.querySelector<HTMLElement>(`[data-group-drop="${CSS.escape(id)}"] .fold`)?.focus() }

// ---------- Row menu ----------
const rowMenu = ref<{ project: Project; anchor: RowMenuAnchor } | null>(null)
const rowActions = computed<RowAction[]>(() => {
  const project = rowMenu.value?.project
  if (!project) return []
  const picked = selected.value.has(project.id)
  // Moving earlier or later arranges the custom order (Alt and the arrow keys).
  const members = sectionOf(project.id)?.items ?? [], at = members.findIndex(p => p.id === project.id), cards = view.value === 'cards'
  const arranging: RowAction[] = members.length > 1 ? [
    { id: 'earlier', label: 'Move earlier', icon: cards ? 'arrow-left' : 'arrow-up', group: 3, reason: at === 0 ? 'It is already first.' : undefined },
    { id: 'later', label: 'Move later', icon: cards ? 'arrow' : 'arrow-down', group: 3, reason: at === members.length - 1 ? 'It is already last.' : undefined },
  ] : []
  return [
    { id: 'open', label: 'Open', icon: 'arrow', group: 1, keys: 'Enter' },
    { id: 'move', label: 'Move to group…', icon: 'folder', group: 2, keys: 'm' },
    { id: 'select', label: picked ? 'Deselect' : 'Select', icon: 'check', group: 2, keys: 'x' },
    ...arranging,
    { id: 'copy', label: 'Copy link', icon: 'link', group: 4 },
    project.archived ? { id: 'restore', label: 'Restore from the archive', icon: 'rollback', group: 5 } : { id: 'archive', label: 'Archive', icon: 'archive', group: 5 },
  ]
})
function openRowMenu(project: Project, anchor: RowMenuAnchor) { rowMenu.value = rowMenu.value?.project.id === project.id && anchor instanceof HTMLElement ? null : { project, anchor } }
function closeRowMenu(restore: boolean) { const menu = rowMenu.value; rowMenu.value = null; if (restore && menu) focusItem(menu.project.id) }
async function rowAction(action: string) {
  const menu = rowMenu.value
  if (!menu) return
  const project = menu.project
  rowMenu.value = null
  switch (action) {
    case 'open': void router.push(to(project)); break
    case 'move': openMove(selected.value.has(project.id) ? [...selected.value] : [project.id], menu.anchor instanceof HTMLElement ? menu.anchor : null); break
    case 'select': toggleSelect(project.id); focusItem(project.id); break
    case 'earlier': case 'later': arrange(project.id, action === 'earlier' ? -1 : 1); break
    case 'copy':
      try { await navigator.clipboard.writeText(new URL(to(project), location.origin).href); toast(`Copied the link to ${project.title}`) }
      catch { toast('The link could not be copied.', { tone: 'error' }) }
      break
    case 'archive': await moveTo([project], ARCHIVED); break
    case 'restore': await moveTo([project], groups.where({ ...project, archived: false })); break
  }
}
async function archiveSelection(restore: boolean) {
  const list = selectedProjects.value.filter(p => p.archived === restore)
  if (restore) {
    for (const target of new Set(list.map(p => groups.where({ ...p, archived: false })))) await moveTo(list.filter(p => groups.where({ ...p, archived: false }) === target), target)
  } else await moveTo(list, ARCHIVED)
}

// ---------- Keyboard ----------
function items() { return [...(page.value?.querySelectorAll<HTMLElement>('[data-project-id] .item-link') ?? [])] }
function focusedId(): string | null { return (document.activeElement as HTMLElement | null)?.closest<HTMLElement>('[data-project-id]')?.dataset.projectId ?? null }
function focusItem(id: string): boolean {
  const link = page.value?.querySelector<HTMLElement>(`[data-project-id="${CSS.escape(id)}"] .item-link`)
  link?.focus(); link?.scrollIntoView({ block: 'nearest' })
  return !!link
}
function step(delta: number) {
  const list = items()
  if (!list.length) return
  const index = list.indexOf(document.activeElement as HTMLElement)
  const next = index === -1 ? (delta > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, index + delta))
  list[next]!.focus(); list[next]!.scrollIntoView({ block: 'nearest' })
}
// Cards: up and down go to the card above or below, nearest the same column.
function stepVertical(delta: -1 | 1) {
  const list = items()
  const current = document.activeElement as HTMLElement
  const index = list.indexOf(current)
  if (index === -1) { step(delta); return }
  const box = current.getBoundingClientRect(), centre = box.left + box.width / 2
  const rows = list.map(el => ({ el, box: el.getBoundingClientRect() })).filter(({ box: b }) => delta > 0 ? b.top >= box.bottom - 2 : b.bottom <= box.top + 2)
  if (!rows.length) return
  const edge = delta > 0 ? Math.min(...rows.map(r => r.box.top)) : Math.max(...rows.map(r => r.box.top))
  const row = rows.filter(r => Math.abs(r.box.top - edge) < 4)
  const best = row.reduce((a, b) => Math.abs(a.box.left + a.box.width / 2 - centre) <= Math.abs(b.box.left + b.box.width / 2 - centre) ? a : b)
  best.el.focus(); best.el.scrollIntoView({ block: 'nearest' })
}
function extend(delta: number) {
  const id = focusedId()
  if (!id) { step(delta); return }
  const ids = onScreen.value.map(p => p.id)
  const at = ids.indexOf(id), to = Math.max(0, Math.min(ids.length - 1, at + delta))
  const next = new Set(selected.value)
  next.add(ids[at]!); next.add(ids[to]!)
  if (!selectAnchor) selectAnchor = ids[at]!
  selected.value = next
  focusItem(ids[to]!)
}
function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}
function keydown(event: KeyboardEvent) {
  if (document.querySelector('dialog[open], .floating')) return
  if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 'a' && !typing(event.target) && onScreen.value.length) {
    event.preventDefault(); selectAll(); return
  }
  // Alt and an arrow key move the focused project in the custom order.
  if (event.altKey && !event.metaKey && !event.ctrlKey && !event.shiftKey && event.key.startsWith('Arrow') && !typing(event.target)) {
    const id = focusedId()
    if (!id) return
    event.preventDefault()
    const back = event.key === 'ArrowLeft' || event.key === 'ArrowUp', vertical = event.key === 'ArrowUp' || event.key === 'ArrowDown'
    arrange(id, (back ? -1 : 1) * (vertical ? perRow(id) : 1))
    return
  }
  if (event.metaKey || event.ctrlKey || event.altKey) return
  if (typing(event.target)) {
    if (event.key === 'ArrowDown' && event.target === search.value) { event.preventDefault(); step(1) }
    if (event.key === 'Escape' && event.target === search.value && term.value) { event.preventDefault(); term.value = '' }
    return
  }
  const id = focusedId()
  if (opensRowMenu(event) && id) {
    const project = store.byId(id), button = page.value?.querySelector<HTMLElement>(`[data-project-id="${CSS.escape(id)}"] .row-more, [data-project-id="${CSS.escape(id)}"] .card-more`)
    if (project && button) { event.preventDefault(); openRowMenu(project, button) }
    return
  }
  const cards = view.value === 'cards'
  switch (event.key) {
    case 'j': event.preventDefault(); step(1); break
    case 'k': event.preventDefault(); step(-1); break
    case 'J': event.preventDefault(); extend(1); break
    case 'K': event.preventDefault(); extend(-1); break
    case 'ArrowDown': event.preventDefault(); if (event.shiftKey) extend(cards ? 1 : 1); else if (cards) stepVertical(1); else step(1); break
    case 'ArrowUp': event.preventDefault(); if (event.shiftKey) extend(-1); else if (cards) stepVertical(-1); else step(-1); break
    case 'ArrowRight': if (cards) { event.preventDefault(); if (event.shiftKey) extend(1); else step(1) } break
    case 'ArrowLeft': if (cards) { event.preventDefault(); if (event.shiftKey) extend(-1); else step(-1) } break
    case '/': event.preventDefault(); search.value?.focus(); break
    case 'x': if (id) { event.preventDefault(); toggleSelect(id) } break
    case 'm': {
      const ids = selected.value.size ? [...selected.value] : id ? [id] : []
      if (ids.length) { event.preventDefault(); openMove(ids, id ? document.querySelector<HTMLElement>(`[data-project-id="${CSS.escape(id)}"] .item-link`) : null) }
      break
    }
    case 'Escape': if (selected.value.size) { event.preventDefault(); clearSelection() } break
    case 'Enter': if (document.activeElement === document.getElementById('main')) { const first = items()[0]; if (first) { event.preventDefault(); void router.push(first.getAttribute('href')!) } } break
  }
}
// Shift and Command (or Ctrl) clicks select instead of opening.
function clickCapture(event: MouseEvent) {
  // The click that ends a card drag opens nothing.
  if (clickBlocked) { event.preventDefault(); event.stopPropagation(); return }
  const link = (event.target as HTMLElement).closest<HTMLElement>('[data-project-id] .item-link')
  if (!link || !(event.shiftKey || event.metaKey || event.ctrlKey) || event.button !== 0) return
  const id = link.closest<HTMLElement>('[data-project-id]')!.dataset.projectId!
  event.preventDefault(); event.stopPropagation()
  if (event.shiftKey && selectAnchor) selectRange(id); else toggleSelect(id)
}
function contextMenu(event: MouseEvent) {
  const item = (event.target as HTMLElement).closest<HTMLElement>('[data-project-id]')
  const project = item ? store.byId(item.dataset.projectId!) : undefined
  if (!project) return
  event.preventDefault()
  openRowMenu(project, { x: event.clientX, y: event.clientY })
}

// ---------- Dragging ----------
const dragIds = ref(new Set<string>())
const dragGroup = ref<string | null>(null)
const dropOn = ref<string | null>(null)
const caretBefore = ref<string | null>(null)
function resetDrag() { dragIds.value = new Set(); dragGroup.value = null; dropOn.value = null; caretBefore.value = null }
function dragStart(event: DragEvent) {
  const target = event.target as HTMLElement
  const item = target.closest?.<HTMLElement>('[data-project-id]')
  if (item) {
    const id = item.dataset.projectId!
    dragIds.value = new Set(selected.value.has(id) ? selected.value : [id])
    event.dataTransfer?.setData('text/plain', [...dragIds.value].join(','))
    if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
    return
  }
  const handle = target.closest?.<HTMLElement>('[data-group-handle]')
  if (handle) {
    dragGroup.value = handle.dataset.groupHandle!
    event.dataTransfer?.setData('text/plain', `group:${dragGroup.value}`)
    if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
  }
}
function dragOver(event: DragEvent) {
  const zone = (event.target as HTMLElement).closest?.<HTMLElement>('[data-group-drop]')
  if (!zone) { dropOn.value = null; caretBefore.value = null; return }
  const id = zone.dataset.groupDrop!
  if (dragIds.value.size) { event.preventDefault(); dropOn.value = id; return }
  if (!dragGroup.value) return
  // Before the group under the pointer, or after it when over its lower part.
  event.preventDefault()
  const section = zone.closest<HTMLElement>('.group-section, .card-section') ?? zone
  const box = section.getBoundingClientRect()
  const ids = sections.value.map(s => s.group.id)
  const after = event.clientY > box.top + Math.min(box.height / 2, 60)
  const before = after ? ids[ids.indexOf(id) + 1] ?? '$end' : id
  const previous = before === '$end' ? ids[ids.length - 1] : ids[ids.indexOf(before) - 1]
  caretBefore.value = before === dragGroup.value || previous === dragGroup.value ? null : before
}
function dragLeave(event: DragEvent) {
  if (!event.relatedTarget || !page.value?.contains(event.relatedTarget as Node)) { dropOn.value = null; caretBefore.value = null }
}
async function drop(event: DragEvent) {
  event.preventDefault()
  const ids = [...dragIds.value], group = dragGroup.value, target = dropOn.value, before = caretBefore.value
  resetDrag()
  if (ids.length && target) {
    const list = ids.map(id => store.byId(id)).filter((p): p is Project => !!p)
    await moveTo(list, target)
  } else if (group && before && before !== group) {
    groups.reorder(group, before === '$end' ? null : before)
  }
}

// ---------- Arranging: the Custom sort ----------
// Cards are arranged by dragging them (mouse or pen) or with Alt and the arrow
// keys; the list follows the same order and moves with Alt and the arrows too.
// Each group keeps its own order: a card is arranged among its group's cards.
const announcement = ref('')
function announce(text: string) { announcement.value = ''; void nextTick(() => { announcement.value = text }) }
// Agents starting or stopping work (AEON-184) are said in the same polite region.
// What was already going on when the page opened is not news; changes settle for
// a moment first, so a burst is said once and a start undone at once is not said.
const LIVE_NEWS_SETTLE_MS = 2500
let liveSaid: Map<string, LiveAgent[]> | null = null
let liveNewsTimer: ReturnType<typeof setTimeout> | undefined
watch(() => live.state === 'ready' ? live.byProject : null, after => {
  if (!after) return
  const current = activeByProject(after)
  if (!liveSaid) { liveSaid = current; return }
  clearTimeout(liveNewsTimer)
  liveNewsTimer = setTimeout(() => {
    const now = activeByProject(live.byProject)
    const news = liveChanges(liveSaid, now, id => store.byId(id)?.title)
    liveSaid = now
    if (news) announce(news)
  }, LIVE_NEWS_SETTLE_MS)
})
function sectionOf(id: string) { return sections.value.find(s => s.items.some(p => p.id === id)) }
// Every project in the order the screen shows them now, the base a new custom order starts from.
function screenOrder() { return [...store.projects].sort(order).map(p => p.id) }
function cardEl(id: string) { return page.value?.querySelector<HTMLElement>(`[data-project-id="${CSS.escape(id)}"]`) ?? null }
// A card's box without the transform it may be gliding under.
function layoutBox(el: HTMLElement) {
  const box = el.getBoundingClientRect(), transform = getComputedStyle(el).transform
  const shift = transform && transform !== 'none' ? new DOMMatrixReadOnly(transform) : null
  const dx = shift?.m41 ?? 0, dy = shift?.m42 ?? 0
  return { left: box.left - dx, top: box.top - dy, right: box.right - dx, bottom: box.bottom - dy }
}
function where(position: number, count: number, group: string) {
  return `position ${position} of ${count}${headers.value ? ` in ${groupName(group)}` : ''}`
}
// What is saved is pruned to the projects shown to this person and bounded in size.
// Moves in quick succession send one write when they settle.
function saveOrder(ids: string[]) {
  const known = new Set(store.projects.map(p => p.id)), archived = new Set(store.projects.filter(p => p.archived).map(p => p.id))
  orderPref.save({ ids: keepOrder(ids, known, archived) }, 300)
}
const stopFailures = onPreferenceFailure(key => {
  if (key !== ORDER_KEY) return
  toast('Your project order could not be saved. It stays here until you reload.', { tone: 'error', key: 'order-failed', action: { label: 'Try again', run: () => saveOrder(savedOrder.value) } })
})
// Whenever the projects shown change (one deleted, access changed, one archived),
// the saved order is pruned to them once things settle, and written only if that
// changes it.
let pruneTimer: ReturnType<typeof setTimeout> | undefined
const visibleProjects = computed(() => ready.value && store.loaded && !store.error && store.projects.length ? store.projects.map(p => `${p.id}${p.archived ? ':a' : ''}`).join(',') : '')
const stopPruning = watch(visibleProjects, list => {
  clearTimeout(pruneTimer)
  if (!list) return
  pruneTimer = setTimeout(() => {
    if (!visibleProjects.value || !savedOrder.value.length) return
    const next = prunedOrder(savedOrder.value, new Set(store.projects.map(p => p.id)), new Set(store.projects.filter(p => p.archived).map(p => p.id)))
    if (next) orderPref.save({ ids: next }, 0)
  }, 600)
}, { immediate: true })
// Saves an arrangement; the first one made under another sort switches to Custom, undoably.
async function commitOrder(next: string[]) {
  const was = routeSort.value, before = savedOrder.value
  saveOrder(next)
  dragOrder.value = null
  if (was === 'custom') { sortOverride.value = null; return }
  sortOverride.value = 'custom'
  toast('Now sorted by custom order', { key: 'custom-order', timeout: 8000, action: { label: 'Undo', run: () => { saveOrder(before); chooseSort(was) } } })
  await router.replace({ query: { ...route.query, sort: 'custom' } })
  if (sortOverride.value === 'custom') sortOverride.value = null
}
function arrange(id: string, delta: number) {
  const section = sectionOf(id), project = store.byId(id)
  if (!section || !project || section.collapsed || drag) return
  const members = section.items.map(p => p.id), at = members.indexOf(id)
  const to = Math.max(0, Math.min(members.length - 1, at + delta))
  if (to === at) { announce(`${project.title} is already ${at === 0 ? 'first' : 'last'}`); return }
  const next = place(screenOrder(), members, [id], to)
  void commitOrder(next)
  announce(`${project.title} moved to ${where(to + 1, members.length, section.group.id)}`)
  void nextTick(() => focusItem(id))
}
// Alt with up or down moves a card by a row of the grid; in the list by one.
function perRow(id: string) {
  if (view.value !== 'cards') return 1
  const tops = (sectionOf(id)?.items ?? []).map(p => cardEl(p.id)).filter((el): el is HTMLElement => !!el).map(el => layoutBox(el).top)
  return Math.max(1, tops.filter(top => Math.abs(top - tops[0]!) < 4).length)
}

interface CardDrag {
  pointer: number; id: string; x: number; y: number; lastX: number; lastY: number; card: HTMLElement; started: boolean
  ids: string[]; moving: string[]; group: string; members: string[]; base: string[]; ghost?: HTMLElement; origin?: DOMRect
}
let drag: CardDrag | null = null
let settling = false
let clickBlocked = false
let scrollFrame = 0
const arranging = ref(false)
const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)')
function pointerDown(event: PointerEvent) {
  if (view.value !== 'cards' || !ready.value || event.pointerType === 'touch' || event.button !== 0 || event.shiftKey || event.metaKey || event.ctrlKey || event.altKey || drag || settling) return
  const target = event.target as HTMLElement
  const card = target.closest<HTMLElement>('.cards-view .card[data-project-id]')
  if (!card || target.closest('button, input')) return
  drag = { pointer: event.pointerId, id: card.dataset.projectId!, x: event.clientX, y: event.clientY, lastX: event.clientX, lastY: event.clientY, card, started: false, ids: [], moving: [], group: '', members: [], base: [] }
  window.addEventListener('pointermove', pointerMove)
  window.addEventListener('pointerup', pointerUp)
  window.addEventListener('pointercancel', abandonDrag)
  window.addEventListener('blur', abandonDrag)
}
function pointerMove(event: PointerEvent) {
  if (!drag || event.pointerId !== drag.pointer) return
  drag.lastX = event.clientX; drag.lastY = event.clientY
  if (!drag.started && (Math.hypot(event.clientX - drag.x, event.clientY - drag.y) < 6 || !beginDrag())) return
  event.preventDefault()
  follow()
}
function beginDrag() {
  const d = drag!, section = sectionOf(d.id)
  if (!section) { release(); drag = null; return false }
  const grabbedSelected = selected.value.has(d.id)
  d.started = true
  d.group = section.group.id
  d.members = section.items.map(p => p.id)
  d.ids = grabbedSelected ? d.members.filter(id => selected.value.has(id)) : [d.id]
  d.moving = grabbedSelected ? [...selected.value] : [d.id]
  d.base = screenOrder()
  // The card lifts off the page: a copy follows the pointer, its place stays open.
  const origin = d.card.getBoundingClientRect()
  const ghost = d.card.cloneNode(true) as HTMLElement
  ghost.removeAttribute('data-project-id')
  ghost.classList.remove('selected', 'menu')
  ghost.classList.add('card-ghost')
  ghost.setAttribute('aria-hidden', 'true')
  ghost.inert = true
  Object.assign(ghost.style, { left: `${origin.left}px`, top: `${origin.top}px`, width: `${origin.width}px`, height: `${origin.height}px` })
  if (d.ids.length > 1) { const count = document.createElement('span'); count.className = 'card-ghost-count'; count.textContent = String(d.ids.length); ghost.append(count) }
  document.body.append(ghost)
  requestAnimationFrame(() => ghost.classList.add('lifted'))
  d.ghost = ghost; d.origin = origin
  window.getSelection()?.removeAllRanges()
  document.documentElement.classList.add('arranging-cards')
  window.addEventListener('keydown', dragKey, true)
  rowMenu.value = null
  arranging.value = true
  dragIds.value = new Set(d.ids)
  dragOrder.value = d.base
  sortOverride.value = 'custom'
  autoScroll()
  return true
}
// The card under the pointer gives way: the dragged one takes the nearest place
// in its group. Over another group (its cards, header or chip) it moves there.
function follow() {
  const d = drag!
  d.ghost!.style.translate = `${d.lastX - d.x}px ${d.lastY - d.y}px`
  const zone = document.elementFromPoint(d.lastX, d.lastY)?.closest<HTMLElement>('[data-group-drop]')?.dataset.groupDrop
  if (zone && zone !== d.group) {
    dropOn.value = zone
    if (!sameOrder(dragOrder.value ?? [], d.base)) dragOrder.value = d.base
    return
  }
  dropOn.value = null
  const members = new Set(d.members)
  const cells = [...(page.value?.querySelectorAll<HTMLElement>('.cards-view [data-project-id]') ?? [])].filter(el => members.has(el.dataset.projectId!)).map(layoutBox)
  const cell = nearestCell(cells, d.lastX, d.lastY)
  if (cell < 0) return
  const next = place(d.base, d.members, d.ids, cell)
  if (!sameOrder(next, dragOrder.value ?? [])) dragOrder.value = next
}
// Near the top or bottom edge the page scrolls, faster the closer the pointer.
function autoScroll() {
  cancelAnimationFrame(scrollFrame)
  const tick = () => {
    if (!drag?.started) return
    const main = document.getElementById('main')
    if (main) {
      const box = main.getBoundingClientRect(), edge = 64, y = drag.lastY
      const push = y < box.top + edge ? y - box.top - edge : y > box.bottom - edge ? y - box.bottom + edge : 0
      if (push) { const before = main.scrollTop; main.scrollTop += Math.max(-22, Math.min(22, push / 3)); if (main.scrollTop !== before) follow() }
    }
    scrollFrame = requestAnimationFrame(tick)
  }
  scrollFrame = requestAnimationFrame(tick)
}
function release() {
  window.removeEventListener('pointermove', pointerMove)
  window.removeEventListener('pointerup', pointerUp)
  window.removeEventListener('pointercancel', abandonDrag)
  window.removeEventListener('keydown', dragKey, true)
  window.removeEventListener('blur', abandonDrag)
  cancelAnimationFrame(scrollFrame)
  document.documentElement.classList.remove('arranging-cards')
}
function dragKey(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  event.preventDefault(); event.stopPropagation()
  cancelDrag(true)
}
function pointerUp(event: PointerEvent) {
  if (!drag || event.pointerId !== drag.pointer) return
  if (!drag.started) { release(); drag = null; return }
  clickBlocked = true
  setTimeout(() => { clickBlocked = false })
  void finishDrag(true)
}
// The pointer was taken away (pointercancel) or the window lost focus: no release
// will follow for this press, so nothing waits for one.
function abandonDrag() { cancelDrag(false) }
// Escape: the button is still down, so its release opens nothing either. Only that
// pointer's next release is swallowed, and a new press forgets it.
function cancelDrag(held: boolean) {
  if (!drag) return
  const d = drag
  if (!d.started) { release(); drag = null; return }
  if (held) swallowRelease(d.pointer)
  void finishDrag(false)
}
let swallow: ((event: PointerEvent) => void) | null = null
function swallowRelease(pointer: number) {
  dropSwallow()
  swallow = event => {
    if (event.pointerId !== pointer) return
    dropSwallow()
    clickBlocked = true
    setTimeout(() => { clickBlocked = false })
  }
  window.addEventListener('pointerup', swallow, true)
  window.addEventListener('pointerdown', dropSwallow, true)
}
function dropSwallow() {
  if (swallow) window.removeEventListener('pointerup', swallow, true)
  window.removeEventListener('pointerdown', dropSwallow, true)
  swallow = null
}
async function finishDrag(dropping: boolean) {
  const d = drag!
  drag = null
  release()
  const target = dropping ? dropOn.value : null, next = dragOrder.value
  dropOn.value = null
  if (target) {
    // Dropped on another group: the projects move there, the sort stays as it was.
    dragOrder.value = null; sortOverride.value = null
    settleGhost(d, false)
    const list = d.moving.map(id => store.byId(id)).filter((p): p is Project => !!p)
    if (routeSort.value === 'custom') {
      // Under Custom they land at the end of that group as it is shown.
      const shown = screenOrder(), there = shown.filter(id => { const p = store.byId(id); return !!p && groups.where(p) === target })
      saveOrder(place(shown, [...there, ...d.moving], d.moving, there.length))
    }
    await moveTo(list, target)
    return
  }
  if (dropping && next && !sameOrder(next, d.base)) {
    void commitOrder(next)
    const project = store.byId(d.id), members = sectionOf(d.id)?.items.map(p => p.id) ?? []
    if (project) announce(`${project.title} moved to ${where(members.indexOf(d.id) + 1, members.length, d.group)}`)
  } else { dragOrder.value = null; sortOverride.value = null }
  await nextTick()
  settleGhost(d, true)
}
// The copy glides into the open place (or fades away over another group), then the card shows again.
function settleGhost(d: CardDrag, home: boolean) {
  const ghost = d.ghost!, slot = home ? cardEl(d.id) : null
  let done = false
  settling = true
  const finish = () => { if (done) return; done = true; ghost.remove(); dragIds.value = new Set(); arranging.value = false; settling = false }
  if (reducedMotion.matches || (home && !slot)) { finish(); return }
  ghost.classList.remove('lifted')
  ghost.classList.add(home ? 'settling' : 'dropped')
  if (slot) { const to = layoutBox(slot); ghost.style.translate = `${to.left - d.origin!.left}px ${to.top - d.origin!.top}px` }
  ghost.addEventListener('transitionend', event => { if (event.propertyName === 'translate' || event.propertyName === 'opacity') finish() })
  setTimeout(finish, 280)
}

// ---------- Life cycle ----------
async function clearSearch() { term.value = ''; await nextTick(); search.value?.focus() }
onMounted(() => {
  void store.load()
  void groups.load()
  window.addEventListener('keydown', keydown)
  // A native capture listener, not @click.capture: a Vue invoker on an ancestor
  // stamps the event first, and handlers attached in the same instant would skip it.
  page.value?.addEventListener('click', clickCapture, true)
  clock = setInterval(() => { now.value = Date.now() }, 60_000)
  stopLive = live.watch()
})
watch(listCard, element => {
  sizer?.disconnect()
  if (!element) return
  listWidth.value = element.clientWidth
  sizer = new ResizeObserver(([entry]) => { listWidth.value = entry!.contentRect.width })
  sizer.observe(element)
}, { flush: 'post' })
onBeforeUnmount(() => { if (drag) { drag.ghost?.remove(); release(); drag = null } dropSwallow(); stopFailures(); stopPruning(); clearTimeout(pruneTimer); window.removeEventListener('keydown', keydown); page.value?.removeEventListener('click', clickCapture, true); clearInterval(clock); stopLive?.(); clearTimeout(liveNewsTimer); sizer?.disconnect(); phoneQuery.removeEventListener('change', phoneChange) })
const who = computed(() => session.identity?.tenant.name ?? 'Workspace')
// One line under the sorts says how to arrange your own order. Touch screens
// scroll when a card is dragged, so there the card's menu arranges it.
const touchOnly = window.matchMedia('(hover: none) and (pointer: coarse)').matches
const sortHint = computed(() => touchOnly
  ? (view.value === 'cards' || sort.value === 'custom' ? 'Move earlier and Move later in a project’s actions menu arrange your own order.' : '')
  : view.value === 'cards' ? 'Drag a card, or press Alt and an arrow key, to arrange your own order.'
    : sort.value === 'custom' ? 'Your own order: drag cards in Cards view, or press Alt and an arrow key.' : '')
const archivedSelection = computed(() => selectedProjects.value.length > 0 && selectedProjects.value.every(p => p.archived))
</script>

<template>
  <section
    ref="page" class="projects-page" :class="[`view-${view}`, { selecting: selected.size }]" aria-labelledby="projects-title"
    @dragstart="dragStart" @dragover="dragOver" @dragleave="dragLeave" @drop="drop" @dragend="resetDrag" @contextmenu="contextMenu" @pointerdown="pointerDown"
  >
    <p class="sr-only" aria-live="polite" aria-atomic="true">{{ announcement }}</p>
    <span id="arrange-hint" class="sr-only">Alt and the arrow keys move the project in your own order.</span>
    <WelcomeBlock />
    <header class="page-head">
      <p class="eyebrow">{{ who }}</p>
      <h1 id="projects-title">Projects</h1>
      <p v-if="store.loaded" class="summary">{{ plural(active.length, 'project') }} · {{ plural(openTotal, 'open ticket') }}</p>
      <p v-else class="summary"><span class="skeleton summary-skeleton" /></p>
    </header>

    <MorningBriefingReminder />
    <div ref="listCard" class="projects-card glass-card">
      <div class="projects-toolbar">
        <label class="search-field project-search">
          <AppIcon name="search" :size="14" />
          <input ref="search" v-model="term" class="field" type="search" placeholder="Filter projects" aria-label="Filter projects" autocomplete="off" spellcheck="false" />
          <kbd v-if="!term" class="keycap slash" aria-hidden="true">/</kbd>
        </label>
        <GroupChips v-if="ready" class="chips" :chips="chips" :drop-on="dropOn" :creating="!!createAnchor" @toggle="groups.toggleVisible" @create="openCreate" />
        <span v-else class="chips chips-skeleton"><span class="skeleton" /></span>
        <span class="tools">
          <span class="seg view-seg" role="radiogroup" aria-label="View">
            <button type="button" role="radio" :aria-checked="view === 'list'" aria-label="List view" data-tip="List · every project in rows" @click="setView('list')"><AppIcon name="list" :size="14" /><span class="view-label">List</span></button>
            <button type="button" role="radio" :aria-checked="view === 'cards'" aria-label="Cards view" data-tip="Cards · progress at a glance" @click="setView('cards')"><AppIcon name="cards" :size="14" /><span class="view-label">Cards</span></button>
          </span>
          <button
            type="button" class="btn sm display-btn" :class="{ on: sort !== 'activity' }" aria-haspopup="dialog" :aria-expanded="!!displayAnchor" :aria-label="`Display, sorted by ${sortMeta.phrase}`"
            :data-tip="`Sorted by ${sortMeta.phrase} · ${view === 'list' ? 'sort and columns' : 'sort'}`" :data-sort="sort" @click="displayAnchor = displayAnchor ? null : ($event.currentTarget as HTMLElement)"
          ><AppIcon :name="sortMeta.icon" :size="14" class="sort-icon" /><span class="display-label">Display</span><AppIcon name="chevron" :size="12" class="chev" /></button>
        </span>
        <FloatingPanel v-if="displayAnchor" :anchor="displayAnchor" :width="290" :tallest="640" align="end" label="Display options" @close="closeDisplay">
          <div class="display-panel">
            <p class="eyebrow">Sort by</p>
            <div class="sort-grid" role="radiogroup" aria-label="Sort projects by">
              <button
                v-for="option in SORTS" :key="option.value" type="button" role="radio" class="sort-option" :class="{ wide: option.value === 'custom' }" :aria-checked="sort === option.value"
                :data-sort="option.value" :data-autofocus="sort === option.value ? '' : undefined" @click="chooseSort(option.value)"
              ><AppIcon :name="option.icon" :size="14" /><span>{{ option.label }}</span></button>
            </div>
            <p v-if="sortHint" class="fine sort-hint">{{ sortHint }}</p>
            <p v-if="view === 'list' && phone" class="section fine">On a phone each project is a small card; columns apply to wider screens.</p>
            <ColumnPicker
              v-else-if="view === 'list'" class="section" :order="pickerColumns.order" :visible="pickerColumns.visible" :customised="pickerColumns.customised"
              :labels="COLUMN_LABELS" :pinned="PINNED_COLUMNS" reset-label="Default" reset-tip="Open, Doing, Done, Progress and Last activity" :note="'Key and Project always lead. Columns that do not fit step aside.'"
              @change="saveColumns" @reset="resetColumns"
            />
          </div>
        </FloatingPanel>
      </div>

      <div v-if="store.error && !store.loaded" class="state" role="alert">
        <AppIcon name="alert" :size="20" />
        <h2>Projects could not be loaded</h2>
        <p>{{ store.error }}</p>
        <button class="btn" type="button" @click="store.load(true)"><AppIcon name="refresh" :size="14" />Try again</button>
      </div>
      <div v-else-if="ready && !sections.length" class="state">
        <AppIcon name="folder" :size="20" />
        <h2>{{ term ? `No project matches “${term}”` : hiddenItems.length ? 'All projects are in hidden groups' : 'No projects yet' }}</h2>
        <p>{{ term ? (hiddenItems.length ? 'Hidden groups hold matches; show them below.' : 'Check the spelling or search by project key.') : hiddenItems.length ? 'Show a group from the chips above, or below.' : 'Projects you create or import appear here.' }}</p>
        <button v-if="term" class="btn" type="button" @click="clearSearch">Clear filter</button>
      </div>
      <ProjectList
        v-else-if="view === 'list'" :sections="sections" :headers="headers" :columns="columns" :term="term" :now="now" :loading="!ready"
        :selected="selected" :dragging="dragIds" :drop-on="dropOn" :caret-before="caretBefore" :row-menu="rowMenu?.project.id ?? null" :group-menu="groupMenu?.id ?? null"
        :renaming="renaming" :validate-name="validateName" :to="to" :label="label"
        @toggle="toggleFold" @step="stepGroup" @group-menu="openGroupMenu" @rename="renameGroup" @row-menu="openRowMenu"
      />
      <HiddenGroups v-if="view === 'list' && ready" :items="hiddenItems" :filtering="filtering" class="hidden-in-card" @show="ids => groups.setVisible(ids, true)" />
    </div>

    <template v-if="view === 'cards' && !(store.error && !store.loaded) && !(ready && !sections.length)">
      <ProjectCards
        class="cards-area" :sections="sections" :headers="headers" :term="term" :now="now" :loading="!ready" :arranging="arranging"
        :selected="selected" :dragging="dragIds" :drop-on="dropOn" :caret-before="caretBefore" :row-menu="rowMenu?.project.id ?? null" :group-menu="groupMenu?.id ?? null"
        :renaming="renaming" :validate-name="validateName" :to="to" :label="label"
        @toggle="toggleFold" @step="stepGroup" @group-menu="openGroupMenu" @rename="renameGroup" @row-menu="openRowMenu"
      />
    </template>
    <HiddenGroups v-if="view === 'cards' && ready" :items="hiddenItems" :filtering="filtering" @show="ids => groups.setVisible(ids, true)" />

    <FloatingPanel v-if="createAnchor" :anchor="createAnchor" :width="300" label="New group" @close="closeCreate">
      <form class="create-panel" @submit.prevent="submitCreate">
        <p class="eyebrow">New group</p>
        <input v-model="draftName" class="field" type="text" maxlength="60" placeholder="Name, e.g. Clients" aria-label="Group name" autocomplete="off" spellcheck="false" data-autofocus :aria-invalid="!!draftProblem" @input="draftProblem = ''" />
        <p v-if="draftProblem" class="problem" role="alert">{{ draftProblem }}</p>
        <p v-else class="fine">Only you see it until an admin shares it.</p>
        <button type="submit" class="btn primary sm create-btn">Create group</button>
      </form>
    </FloatingPanel>
    <RowMenu v-if="groupMenu && groupActions.length" :anchor="groupMenu.anchor" :items="groupActions" :label="`Group ${groupName(groupMenu.id)}`" @select="groupAction" @close="closeGroupMenu" />
    <RowMenu v-if="rowMenu" :anchor="rowMenu.anchor" :items="rowActions" :label="`Project ${rowMenu.project.title}`" @select="rowAction" @close="closeRowMenu" />
    <MoveToGroup v-if="moveDialog" :anchor="moveDialog.anchor" :subject="subjectOf(moveProjects)" :options="moveOptions" @choose="chooseMove" @create="createAndMove" @close="closeMove" />
    <ProjectBulkBar
      v-if="selected.size" :count="selected.size" :total="onScreen.length" :busy="busy" :archived-only="archivedSelection"
      @move="anchor => openMove([...selected], anchor)" @archive="archiveSelection(false)" @restore="archiveSelection(true)" @clear="clearSelection" @select-all="selectAll"
    />
  </section>
</template>

<style scoped>
.projects-page { width: 100%; margin: 0; padding: 22px var(--gutter) 40px; }
.projects-page.selecting { padding-bottom: 96px; }
.page-head { margin-bottom: 20px; }
.page-head h1 { margin-top: 6px; }
.summary { margin-top: 6px; font-size: 13.5px; color: var(--ink-2); min-height: 20px; }
.summary-skeleton { display: inline-block; width: 220px; }
.projects-card { overflow: clip; }
/* Search, chips and tools on one line when there is room; below 1200px the chips
   take their own line, so the toolbar is as tall before the groups load as after. */
.projects-toolbar { display: grid; grid-template-columns: 280px minmax(0, 1fr) auto; grid-template-areas: "search chips tools"; align-items: center; gap: 10px 14px; padding: 14px 16px; }
.view-list .projects-toolbar, .projects-card:has(.state) .projects-toolbar { border-bottom: 1px solid var(--line); }
.project-search { grid-area: search; min-width: 0; }
.project-search .field { padding-right: 32px; }
.project-search .field::-webkit-search-cancel-button { display: none; }
.slash { position: absolute; right: 9px; pointer-events: none; }
@media (hover: none) { .slash { display: none; } }
.chips { grid-area: chips; min-width: 0; }
.chips-skeleton { display: flex; align-items: center; height: 30px; }
.chips-skeleton .skeleton { width: 140px; }
.tools { grid-area: tools; display: flex; align-items: center; justify-content: flex-end; gap: 8px; }
@media (max-width: 1199px) { .projects-toolbar { grid-template-columns: minmax(0, 280px) 1fr auto; grid-template-areas: "search . tools" "chips chips chips"; } }
.view-seg button { height: 26px; padding: 0 11px; }
.display-btn { gap: 6px; color: var(--ink-2); }
.display-btn .sort-icon { flex-shrink: 0; }
.display-btn.on .sort-icon { color: var(--teal-ink); }
.chev { color: var(--ink-3); }
.display-panel { display: grid; gap: 8px; padding: 6px 8px 8px; }
.sort-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 4px; padding: 3px; border-radius: 12px; background: var(--seg-bg); }
.sort-option { display: inline-flex; align-items: center; gap: 7px; height: 30px; padding: 0 10px; border: 0; border-radius: 9px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 500; white-space: nowrap; }
.sort-option svg { flex-shrink: 0; color: var(--ink-3); }
.sort-option.wide { grid-column: 1 / -1; }
.sort-option:hover { color: var(--ink); }
.sort-option:hover svg { color: var(--ink-2); }
.sort-option[aria-checked="true"] { background: var(--seg-on); color: var(--ink); font-weight: 600; box-shadow: var(--shadow-btn); }
.sort-option[aria-checked="true"] svg { color: var(--teal-ink); }
.sort-hint { padding: 0 2px; line-height: 1.45; }
.sort-option:focus-visible { box-shadow: var(--focus-ring); }
.section { margin-top: 6px; padding-top: 10px; border-top: 1px solid var(--line); }
.fine { font-size: 12px; color: var(--ink-3); }
.cards-area { margin-top: 22px; }
.hidden-in-card { border-top: 1px solid var(--line); }
.create-panel { display: grid; gap: 8px; padding: 6px 8px 8px; }
.create-panel .field { height: 34px; }
.problem { font-size: 12px; color: var(--danger); }
.create-btn { justify-self: end; }
.state { display: grid; justify-items: center; gap: 8px; padding: 64px 24px; text-align: center; color: var(--ink-2); }
.state > svg { color: var(--teal); margin-bottom: 4px; }
.state h2 { color: var(--ink); font-size: 17px; }
.state p { font-size: 13.5px; }
.state .btn { margin-top: 10px; }
@media (max-width: 1100px) { .view-label { display: none; } .view-seg button { padding: 0 8px; } }
@media (max-width: 900px) { .display-label { display: none; } .display-btn { padding: 0 9px; } }
@media (max-width: 760px) {
  .projects-page { padding: 14px 12px 28px; }
  .page-head { margin-bottom: 14px; padding: 0 4px; }
  .projects-toolbar { grid-template-columns: minmax(0, 1fr); grid-template-areas: "search" "chips" "tools"; gap: 10px; padding: 12px; }
  .project-search .field { height: 44px; font-size: 16px; }
  .chips-skeleton { height: 44px; }
  .tools { justify-content: space-between; }
  .view-seg button { height: 44px; min-width: 48px; }
  .display-btn { height: 44px; min-width: 44px; }
  .cards-area { margin-top: 16px; }
}
</style>
