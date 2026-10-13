<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, watchEffect } from 'vue'
import type { SessionControl } from '../../lib/agents'
import { reparentSession, undoRemoval } from '../../lib/agentRows'
import { movableWorker, moveTarget } from './sessionMove'
import { useAgents } from '../../stores/agents'
import { GROUPS, controlBlocked, sessionForest, sessionWorking, stopReasonLabel, type SessionBranch, type SessionGroup } from '../../lib/agentState'
import { relativeTime } from '../../lib/work'
import type { Availability, SessionView } from '../../stores/agents'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import ConnectHint from './ConnectHint.vue'
import SessionPauseActions from './SessionPauseActions.vue'
import { useAgentPause } from '../../stores/agentPause'
import { pausingSession } from '../../lib/agentPause'
import { isStale, useSessionRemoval } from './sessionRemoval'
import { useAgentAppearance } from '../../lib/agentAppearance'
const { appearance } = useAgentAppearance()
import SessionAvatar from './SessionAvatar.vue'
import HarnessOpener from './HarnessOpener.vue'
import SessionsHeld from './SessionsHeld.vue'
import { currentActivity, currentStep, workerActivity } from './activity'
import { timeShort, timeText, timeTip } from './sessionTime'
import { inFrameOrder, lingers, movesAt, useSessionHold, type Heading, type Structure } from './sessionHold'
import { useReaderAnchor, useTouchScroll } from '../../lib/readerAnchor'
import { clockTime } from '../../lib/agentPause'
import { STATE_LABEL } from '../../lib/agentSignals'
import ExecutionMark from './ExecutionMark.vue'
import SessionHost from './SessionHost.vue'
import { listHostLabels } from '../../lib/agents'
import { intendedResult, modelNames, sessionExecution, sessionSearchText } from './sessionRow'
import { brand } from '../../lib/brand'
import { toast } from '../../lib/toast'
import { useAgentRecovery } from '../../lib/agentRecovery'
import { quickRemoval, sessionMenu, type SessionMenu } from './sessionActions'
import { controlPermitted, type ControlGrant } from '../../lib/managedControl'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { DEFAULT_SORT, nextSort, orderForest, readSort, writeSort, type SessionSort, type SortKey } from './sessionOrder'
import { LEAD_WORDS } from '../../lib/lead'
import { below, countedLive, shownBelow, treeRows, type TreeRow } from './sessionTree'
import { useServiceTiers } from '../../stores/serviceTiers'
import { TIER_NAME } from '../../lib/serviceTier'
import { FILTER_LABEL, matchesFilter, type HeadFilter } from './headCounts'
import KeyCap from '../KeyCap.vue'
import { shortcutCaps, shortcutHint, shortcutKeys } from '../../lib/shortcuts'
import { vClipTip } from '../../directives/clipTip'
import { leadActionAvailable, leadActionReason, leadBand, type ProjectLead } from '../../lib/lead'
import { openLeadPanel, openStartLead } from '../../lib/leadOverlay'
import { useAgentsLayout } from '../../lib/agentsLayout'
import { useRoute, useRouter } from 'vue-router'
import { usePreference } from '../../lib/preferences'
import { useProjects } from '../../stores/projects'
import type { AgentState } from '../../lib/agentSignals'
import LeadBot from '../lead/LeadBot.vue'
import LeadMenu from '../lead/LeadMenu.vue'
import SessionFacetMenu from './SessionFacetMenu.vue'
import type { AgentProjects } from './useAgentProjects'
import {
  DEFAULT_GROUP, emptyFacets, FACET_KEYS, FACET_TITLE, facetChoices, facetsActive, foldSummary, GROUP_BY, groupWord, isLeadRoot, keyedGroups, matchesFacets,
  NO_PROJECT, orderProjects, projectGroups, queueHeads, queuedWord, queueNote, type FacetKey, type Facets, type GroupBy, type LeadLine, type ProjectRef,
} from './sessionGroups'
const route = useRoute(), router = useRouter()
const serviceTiers = useServiceTiers()

// Session families stay together across status groups. Each lead's history is
// opt-in for this mounted list only; refreshes never open it or persist it.
const props = defineProps<{
  history?: SessionView[]; historyState?: 'idle' | 'loading' | 'ready' | 'error'; historyMore?: boolean; groups: Record<SessionGroup, SessionView[]>; now: number; cursor: string; selected: string; state: Availability; error: string
  loaded: boolean; controls: Record<string, SessionControl>; canStart: boolean; canLead?: boolean; filter?: HeadFilter | null
  /** The card head's search (AEON-1209): it filters this list only. */
  query?: string
  layoutDock?: boolean; pane?: boolean
  /** Each project's lead and queued work (AEON-1210): lead rows, the queue chip and the no-lead line. */
  projects?: AgentProjects | null
  /** Display › Group by and Filter › Project, Harness, Computer; the address carries both. */
  group?: GroupBy
  facets?: Facets
}>()
const agentRecovery = useAgentRecovery()
const emit = defineEmits<{ open: [id: string]; control: [view: SessionView, kind: SessionControl['kind']]; focusRow: [id: string]; retry: []; start: []; history: []; older: []; clearFilter: []; filter: [filter: HeadFilter]; search: [query: string]; group: [group: GroupBy]; facets: [facets: Facets] }>()
const showStopped = ref(false)
// History is a separate, opt-in list of every ended or removed session. The
// main list keeps only sessions that ended in the last 24 hours (AEON-291).
const showRemoved = ref(false)
const removedCount = computed(() => props.history?.length ?? 0)
function toggleHistory() {
  showRemoved.value = !showRemoved.value
  if (showRemoved.value) emit('history')
  // History lists ended sessions; a state filter only applies to the live list.
  if (showRemoved.value && props.filter) emit('clearFilter')
}
watch(() => props.filter, value => { if (value) showRemoved.value = false })
const removal = useSessionRemoval()
const current = computed(() => GROUPS.flatMap(g => props.groups[g.id]))
const stale = computed(() => current.value.map(v => v.session).filter(s => isStale(s, props.now) && removal.canRemove(s)))
const lineageName = (id: string) => [...current.value, ...(props.history ?? [])].find(v => v.session.id === id)?.name || `${LEAD_WORDS.l} session`
type Branch = SessionBranch<SessionView>
// Computer names as the person labelled them (read below, per viewer).
const hostLabels = ref(new Map<string, string>())
// Families in the viewer's order (AEON-468): state, then start time, unless they chose a column.
const sort = ref<SessionSort | null>(null)
const agents = useAgents()
// Models are named as Settings › Models names them, never by their raw ids (AEON-1211). Declared
// before the forest, which the selected path's immediate watcher reads during setup.
const names = computed(() => modelNames(agents.models))

// ---------- Calm enter and leave (AEON-1211, design AEON-1196 §3.2; the UI half of AEON-1195) ----------
// A state change is a state: the row stays and its badge, line 2 and time change. An ended
// session keeps the place (the group) it had while it ran for 10 minutes, then moves to Ended.
const lastGroup = new Map<string, SessionGroup>()
// Its rank among its siblings while it ran (0 working, 1 live), so an ended worker keeps that place too.
const lastRank = new Map<string, 0 | 1>()
const rememberGroups = (views: SessionView[]) => {
  for (const v of views) {
    if (v.status.group === 'stopped') continue
    lastGroup.set(v.session.id, v.status.group)
    if (countedLive(v)) lastRank.set(v.session.id, sessionWorking(v) ? 0 : 1)
  }
}
watch(current, rememberGroups, { immediate: true })
const lingering = (view: SessionView) => view.status.group === 'stopped' && lingers(view.session, props.now)
function placed(view: SessionView): SessionView {
  if (!lingering(view)) return view
  return { ...view, status: { ...view.status, group: lastGroup.get(view.session.id) ?? 'idle' } }
}
const liveViews = computed(() => current.value.map(placed))
// Rows show the newest copy of their session; the held copy only places them.
const liveById = computed(() => new Map(current.value.map(view => [view.session.id, view])))
const liveOf = (view: SessionView) => showRemoved.value ? view : liveById.value.get(view.session.id) ?? view
// A row keeps its place while the copy that places it sits outside Ended though its session ended:
// placed() above, or a copy the hold keeps. Membership, sibling order, the stopped count and
// line 2 read it from that copy, never from the clock, so a held list keeps the row past the
// 10-minute mark until Show now or the release.
const ended = (view: SessionView) => view.status.state === 'done' || view.status.state === 'stopped'
const keepsPlace = (view: SessionView) => view.status.group !== 'stopped' && ended(liveOf(view))
const keptRank = (view: SessionView) => keepsPlace(view) ? lastRank.get(view.session.id) ?? 1 : undefined
// A head count's filter (AEON-780) and the card head's search (AEON-1209) keep matching
// sessions and their ancestors, which stay as dimmed context, unfolded. The state filter
// applies to the live list; the search to History too.
const stateFilter = computed(() => showRemoved.value ? null : props.filter ?? null)
const facets = computed(() => props.facets ?? emptyFacets())
const group = computed<GroupBy>(() => props.group ?? DEFAULT_GROUP)
const terms = computed(() => (props.query ?? '').toLowerCase().split(/\s+/).filter(Boolean))
// Whether a session matches now, read from its newest copy. Models match by the names the rows
// show (AEON-1211). The list itself asks `filtering` below, which a hold answers first.
const liveMatch = computed<((view: SessionView) => boolean) | null>(() => {
  const state = stateFilter.value, words = terms.value, models = names.value, chosen = facets.value, faceted = facetsActive(chosen)
  if (!state && !words.length && !faceted) return null
  return view => {
    if (state && !matchesFilter(view, state)) return false
    const text = words.length ? sessionSearchText(view, models) : ''
    return words.every(word => text.includes(word)) && (!faceted || matchesFacets(view, chosen))
  }
})
// Structure waits while the person points at the list, has keyboard focus in a row or a menu
// open; a refresh then never moves, adds or removes a row under them (sessionHold.ts).
// Only a mouse or a pen points. A finger on the list is a scroll: the browser takes it over
// (pointercancel, then pointerleave), which once released the hold mid-swipe (AEON-1279).
// Touch instead holds while the finger is down and its scroll still runs, momentum included.
const pointing = ref(false)
const touching = useTouchScroll()
function pointerIn(event: PointerEvent) { if (event.pointerType !== 'touch') pointing.value = true }
const keyFocus = ref(false)
// Follows the list's menus (declared below): the row menu, Move to lead, Filter and Display.
const menuOpen = ref(false)
const structure: Structure<SessionView> = {
  id: view => view.session.id,
  key: view => `${view.status.group}|${view.session.project_id ?? ''}|${view.session.parent_harness_session_id ?? ''}|${group.value === 'harness' ? view.session.harness : group.value === 'computer' ? view.session.host : ''}`,
  match: view => liveMatch.value?.(view) ?? true,
  order: views => placementOrder(views),
}
// What the person holds: Show now offers it. A swipe's own hold ends by itself before a finger
// could reach Show now, so the note stays calm through it.
const personHolds = computed(() => pointing.value || keyFocus.value || menuOpen.value)
const hold = useSessionHold(() => liveViews.value, structure, () => !showRemoved.value && (personHolds.value || touching.value), undefined, {
  read: projectLayout,
  headings: layout => layout.headings,
  follow: (held, current, ids) => ({
    ...held,
    placements: new Map([...held.placements].map(([id, placement]) => [id, ids.has(`p:${id}`) ? current.placements.get(id) ?? placement : placement])),
    headings: held.headings.flatMap(heading => ids.has(heading.id) ? current.headings.filter(next => next.id === heading.id) : [heading]),
  }),
})
// Whether a row matches is part of its place: a row the hold keeps keeps the match it froze with,
// so a refresh that changes it, a new copy or a renamed model alike, waits like any other move.
const filtering = computed<((view: SessionView) => boolean) | null>(() => {
  const live = liveMatch.value
  if (!live || showRemoved.value) return live
  return view => hold.matched(view.session.id) ?? live(view)
})
// The sessions the list lays out: a row the hold keeps keeps the table, so the live list emptying
// under the pointer never swaps it for the connect hint.
const total = computed(() => hold.views.value.length)
// The card head's note says a delay like the page head does (AEON-785): no "Live" while reads lag.
const listStale = computed(() => agents.refreshStale || (agents.sessionsUpdatedAt !== null && props.now - agents.sessionsUpdatedAt > 45_000))
const ordered = (views: SessionView[]): Branch[] => {
  const branches = orderForest(sessionForest(views, props.now, keptRank), sort.value, props.now, false, names.value)
  return hold.rank.value ? inFrameOrder(branches, hold.rank.value, branch => branch.view.session.id) : branches
}
const forest = computed(() => showRemoved.value
  ? orderForest(sessionForest(props.history ?? [], props.now), sort.value, props.now, true, names.value)
  : ordered(hold.views.value))
// A new order, filter or search is the person's own choice: it applies at once.
watch([sort, stateFilter, group, () => JSON.stringify(facets.value), () => terms.value.join(' ')], () => { if (hold.rank.value) hold.showNow() })
const lingersBelow = (branch: Branch): boolean => keepsPlace(branch.view) || branch.children.some(lingersBelow)
function onFocusIn(event: FocusEvent) {
  const target = event.target as HTMLElement
  keyFocus.value = !!target.closest('[data-row]') && target.matches(':focus-visible')
}
function onFocusOut(event: FocusEvent) {
  const next = event.relatedTarget
  keyFocus.value = next instanceof HTMLElement && !!table.value?.contains(next) && !!next.closest('[data-row]') && keyFocus.value
}
const kept = computed(() => {
  const ids = new Set<string>()
  const active = filtering.value
  if (!active) return ids
  const walk = (branch: Branch): boolean => {
    const below = branch.children.map(walk).some(Boolean)
    const keep = below || active(branch.view)
    if (keep) ids.add(branch.view.session.id)
    return keep
  }
  forest.value.forEach(walk)
  return ids
})
const keeps = (branch: Branch) => !filtering.value || kept.value.has(branch.view.session.id)
// Placement, including the held copy, decides when an ended family joins Ended.
const settled = (branch: Branch) => branch.group === 'stopped'
const liveRoots = computed(() => forest.value.filter(branch => !settled(branch) && keeps(branch)))
const endedRoots = computed(() => forest.value.filter(branch => settled(branch) && keeps(branch)))
const expanded = ref<Record<string, boolean>>({})
const history = ref<Record<string, boolean>>({})
const containsSelected = (branch: Branch): boolean => branch.view.session.id === props.selected || branch.children.some(containsSelected)
// A direct link may reveal its selected row, but never its stopped siblings.
// An ended child keeps its place for 10 minutes (AEON-1211) before it joins the stopped ones.
const candidates = (branch: Branch) => branch.children.filter(child => showRemoved.value || history.value[branch.view.session.id] || child.liveCount > 0 || child.view.status.state === 'paused' || lingersBelow(child) || containsSelected(child))
// ---------- Groups (AEON-1210, design AEON-1196 §3.3) ----------
// By project (the default): projects in their fixed order, the lead row heading each group and
// its workers nested below; a project without a lead, with queued work or sessions, is headed by one slim line. "No project"
// and "Ended" come at the end. Display may group by lead, harness, state or computer, or not at all.
const projectStore = useProjects()
const customOrder = usePreference<{ ids?: string[] }>('projects:order')
const orderedProjects = computed<ProjectRef[]>(() => orderProjects(projectStore.projects.filter(p => !p.archived).map(p => ({ id: p.id, key: p.routeKey, title: p.title })), customOrder.value.value?.ids ?? []))
const leadOf = (projectId: string | null | undefined) => projectId ? props.projects?.lead(projectId) ?? null : null
const queuedOf = (projectId: string | null | undefined) => projectId ? props.projects?.queued(projectId) ?? null : null
// A count from a queue read cut short reads “4+”; a queue that can't be read says so below the list (AEON-1210 G2).
const qWord = (count: number | null) => count === null ? '?' : queuedWord(count, !!props.projects?.queuePartial)
const qTell = (count: number | null) => count === null ? 'Queued count unknown' : `${props.projects?.queuePartial ? 'At least ' : ''}${count} queued`
const sourceQueueNote = computed(() => props.projects ? queueNote(props.projects) : '')
// Lead lines speak for projects, not sessions: they show while nothing but a project filter narrows the list.
const lines = computed(() => !showRemoved.value && !!props.projects && !stateFilter.value && !terms.value.length && !facets.value.harness.length && !facets.value.computer.length)
type Header = 'none' | 'static' | 'toggle' | 'fold'
interface Section { id: string; header: Header; label: string; note?: string; count: number; roots: Branch[]; project?: ProjectRef; line?: LeadLine | null }
interface ProjectPlacement { project: ProjectRef; lead: ProjectLead | null; queued: number | null; header: Header; line: LeadLine | null; separate: boolean }
interface ProjectLayout { projects: ProjectRef[]; placements: Map<string, ProjectPlacement>; headings: Heading[] }
const liveTally = (branch: Branch): number => Number(countedLive(liveOf(branch.view))) + branch.children.reduce((sum, child) => sum + liveTally(child), 0)
const liveCount = (roots: Branch[]) => roots.reduce((sum, branch) => sum + liveTally(branch), 0)
const flatten = (roots: Branch[]): Branch[] => roots.flatMap(branch => [branch, ...flatten(branch.children)])
// Capture the inputs that place project headings with the sessions, including projects with
// no session. Counts and lead state are evidence; line kind, membership and order are placement.
function projectLayout(views: readonly SessionView[], match: (view: SessionView) => boolean): ProjectLayout {
  const keep = (branch: Branch): boolean => match(branch.view) || branch.children.some(keep)
  const roots = orderForest(sessionForest([...views], props.now, keptRank), sort.value, props.now, false, names.value).filter(branch => !settled(branch) && keep(branch))
  const grouped = projectGroups({ roots, projects: orderedProjects.value, lead: leadOf, queued: queuedOf, lines: lines.value, onlyProjects: facets.value.project }).groups
  const projects = [...orderedProjects.value, ...grouped.map(g => g.project).filter(p => !orderedProjects.value.some(known => known.id === p.id))]
  const placements = new Map<string, ProjectPlacement>(projects.map(project => {
    const g = grouped.find(g => g.project.id === project.id), current = leadOf(project.id)
    const lead = current ? { ...current } : null
    const line = g?.line ? { ...g.line, lead: { ...g.line.lead } } : null
    return [project.id, { project, lead, queued: queuedOf(project.id), header: g?.leads || line ? 'none' as const : 'static' as const, line, separate: !!line && (line.kind === 'pending' || !!line.queued) }]
  }))
  let headings: Heading[] = []
  if (group.value === 'project') headings = grouped.map(g => ({ id: `p:${g.project.id}`, key: `${placements.get(g.project.id)!.header}|${g.line?.kind ?? ''}|${g.line?.kind === 'pending' || g.leads ? Number(!!placements.get(g.project.id)!.queued) : ''}` }))
  else if (group.value === 'lead' || group.value === 'none') {
    if (group.value === 'lead') {
      const leads = roots.filter(branch => isLeadRoot(branch, leadOf(branch.view.session.project_id)) || branch.children.length > 0)
      headings = leads.map(branch => ({ id: `l:${branch.view.session.id}`, key: 'fold' }))
      if (roots.some(branch => !leads.includes(branch))) headings.push({ id: 'nolead', key: 'fold' })
    }
    headings.push(...grouped.filter(g => placements.get(g.project.id)!.separate).map(g => ({ id: `p:${g.project.id}`, key: g.line!.kind })))
    for (const id of queueHeads(roots, leadOf)) {
      const root = roots.find(branch => branch.view.session.id === id)!
      const placement = placements.get(root.view.session.project_id)
      if (!placement?.line && placement?.queued) headings.push({ id: `q:${id}`, key: 'queue' })
    }
  }
  return { projects, placements, headings }
}
const placementLeadOf = (id: string | null | undefined) => id ? hold.layout.value?.placements.get(id)?.lead ?? null : null
const placementQueuedOf = (id: string | null | undefined) => id ? hold.layout.value?.placements.get(id)?.queued ?? null : null
const hasQueue = (id: string | null | undefined) => !!placementQueuedOf(id)
const placementProjects = computed(() => hold.layout.value?.projects ?? orderedProjects.value)
// Rehydrate the held line's evidence from the newest projection without changing its kind.
const lineOf = (placement: ProjectPlacement): LeadLine | null => {
  const line = placement.line
  if (!line) return null
  const lead = leadOf(placement.project.id) ?? line.lead
  return line.kind === 'none' ? { kind: 'none', lead, queued: queuedOf(placement.project.id) } : { kind: 'pending', lead }
}
// Grouped by lead or not at all, a project's lead line shows where its current lead has no running session here;
// a no-lead line only for queued work, since the project's sessions are listed elsewhere.
function lineSections(): Section[] {
  if (!lines.value) return []
  return [...(hold.layout.value?.placements.values() ?? [])].flatMap(p => p.separate ? [{ id: `p:${p.project.id}`, header: 'none' as const, label: p.project.key, count: 0, roots: [], project: p.project, line: lineOf(p) }] : [])
}
// Freeze the order the chosen grouping actually shows. A project's lead heads its group,
// even when another root has a more urgent state; starting a hold must not reorder them.
function placementOrder(views: readonly SessionView[]): string[] {
  const branches = orderForest(sessionForest([...views], props.now, keptRank), sort.value, props.now, false, names.value)
  const live = branches.filter(branch => !settled(branch)), done = branches.filter(settled)
  let roots: Branch[]
  if (group.value === 'project') {
    const grouped = projectGroups({ roots: live, projects: orderedProjects.value, lead: leadOf, queued: queuedOf, lines: false })
    roots = [...grouped.groups.flatMap(g => g.roots), ...grouped.loose, ...done]
  } else if (group.value === 'lead') {
    const leads = live.filter(branch => isLeadRoot(branch, leadOf(branch.view.session.project_id)) || branch.children.length > 0)
    roots = [...leads, ...live.filter(branch => !leads.includes(branch)), ...done]
  } else if (group.value === 'none') roots = [...live, ...done]
  else {
    const items = flatten(live).map(branch => branch.view)
    const keyOf = (view: SessionView) => group.value === 'harness' ? view.session.harness : group.value === 'state' ? view.status.group : view.session.host || HOSTLESS
    const label = (key: string) => group.value === 'harness' ? items.find(view => view.session.harness === key)?.harness || key : group.value === 'state' ? groupLabel(key) : key === HOSTLESS ? key : hostLabels.value.get(key) ?? key
    roots = [...keyedGroups(items, keyOf, label, group.value === 'state' ? GROUPS.map(item => item.id) : [])
      .flatMap(g => orderForest(sessionForest(g.items, props.now, keptRank), sort.value, props.now, false, names.value)), ...done]
  }
  return flatten(roots).map(branch => branch.view.session.id)
}
const HOSTLESS = 'No computer'
const groupLabel = (key: string) => GROUPS.find(g => g.id === key)?.label ?? key
const sections = computed<Section[]>(() => {
  if (showRemoved.value) return [{ id: 'history', header: 'static', label: 'Ended', count: forest.value.filter(keeps).reduce((sum, branch) => sum + branch.count, 0), roots: forest.value.filter(keeps) }]
  const out: Section[] = []
  const live = liveRoots.value
  if (group.value === 'project') {
    const { groups, loose } = projectGroups({ roots: live, projects: placementProjects.value, lead: placementLeadOf, queued: placementQueuedOf, lines: lines.value, onlyProjects: facets.value.project })
    for (const g of groups) {
      const placement = hold.layout.value?.placements.get(g.project.id)
      out.push({ id: `p:${g.project.id}`, header: placement?.header ?? (g.leads || g.line ? 'none' : 'static'), label: g.project.key, note: g.project.title, count: liveCount(g.roots), roots: hold.rank.value ? inFrameOrder(g.roots, hold.rank.value, branch => branch.view.session.id) : g.roots, project: g.project, line: placement ? lineOf(placement) : g.line })
    }
    if (loose.length) out.push({ id: 'loose', header: 'static', label: 'No project', note: 'personal and workspace sessions', count: liveCount(loose), roots: loose })
  } else if (group.value === 'lead') {
    const leads = live.filter(branch => isLeadRoot(branch, placementLeadOf(branch.view.session.project_id)) || branch.children.length > 0)
    for (const branch of leads) out.push({ id: `l:${branch.view.session.id}`, header: 'fold', label: branch.view.name, note: branch.view.projectKey, count: liveCount([branch]), roots: [branch] })
    const rest = live.filter(branch => !leads.includes(branch))
    if (rest.length) out.push({ id: 'nolead', header: 'fold', label: 'No lead', count: liveCount(rest), roots: rest })
    out.push(...lineSections())
  } else if (group.value === 'none') {
    if (live.length) out.push({ id: 'all', header: 'none', label: '', count: liveCount(live), roots: live })
    out.push(...lineSections())
  } else {
    // By harness, state or computer each live session joins the group of its own value; a worker
    // whose lead sits in another group starts its own line there.
    const views = flatten(live).filter(branch => keeps(branch) && (branch.view.status.group !== 'stopped' || (live.includes(branch) && !settled(branch)))).map(branch => branch.view)
    const keyOf = (view: SessionView) => group.value === 'harness' ? view.session.harness : group.value === 'state' ? view.status.group : view.session.host || HOSTLESS
    const label = (key: string) => group.value === 'harness' ? views.find(view => view.session.harness === key)?.harness || key : group.value === 'state' ? groupLabel(key) : key === HOSTLESS ? key : hostLabels.value.get(key) ?? key
    for (const g of keyedGroups(views, keyOf, label, group.value === 'state' ? GROUPS.map(item => item.id) : [])) {
      out.push({ id: `k:${g.key}`, header: 'fold', label: g.label, count: g.items.length, roots: ordered(g.items) })
    }
  }
  const done = endedRoots.value
  if (done.length) out.push({ id: 'ended', header: 'toggle', label: 'Ended', note: 'in the last 24 h · older in History', count: done.reduce((sum, branch) => sum + branch.count, 0), roots: done })
  return out
})
const hasLines = computed(() => sections.value.some(section => !!section.line))
// A lead without a session here speaks through its project's lead state (AEON-741's words).
// An intent without a running session never looks like a running process (audit AEON-1218 G2).
const PENDING_STATE: Record<ProjectLead['state'], AgentState> = { none: 'idle', starting: 'awaiting', working: 'working', waiting_for_room: 'throttled', paused: 'paused', cannot_start: 'problem' }
const PENDING_WORD: Record<ProjectLead['state'], string> = { none: 'None', starting: 'Starting', working: 'Working', waiting_for_room: 'Waiting', paused: 'Paused', cannot_start: 'Can’t start' }
const pendingState = (lead: ProjectLead) => lead.state === 'working' && !(lead.session_id && lead.process_active) ? 'awaiting' : PENDING_STATE[lead.state]
const pendingBusy = (lead: ProjectLead) => !!lead.session_id && lead.process_active
const pendingWord = (lead: ProjectLead) => PENDING_WORD[lead.state]
const pendingBand = (lead: ProjectLead, project: ProjectRef) => leadBand(lead, project.key, queuedOf(project.id))
function openPending(event: MouseEvent, project: ProjectRef) {
  if ((event.target as HTMLElement).closest('.c-actions, .qchip')) return
  openLeadPanel(project.id, event.currentTarget as HTMLElement)
}
function openProjectLead(projectId: string, anchor: HTMLElement, section: 'next-up' | null = null) {
  const session = props.projects?.lead(projectId)?.session_id
  if (session) { void router.push({ path: `/agents/${encodeURIComponent(session)}`, query: { ...route.query, tab: 'lead' }, hash: section ? '#next-up' : '' }); return }
  openLeadPanel(projectId, anchor, section)
}
// The queue chip opens the project's lead at Next up (the Lead tab arrives with package P4).
function openQueue(projectId: string | null | undefined, event: MouseEvent) {
  event.stopPropagation()
  if (projectId) openProjectLead(projectId, event.currentTarget as HTMLElement, 'next-up')
}
// Keyed groups fold for this visit; the Ended fold opens on its own toggle, a filter or a link.
const groupOpen = ref<Record<string, boolean>>({})
const sectionOpen = (section: Section) => section.header === 'fold' ? groupOpen.value[section.id] ?? true
  : section.header === 'toggle' ? showStopped.value || !!filtering.value || section.roots.some(containsSelected) : true

// ---------- Folded groups, remembered per person (agents:layout.folded_projects) ----------
// A lead row folds its project's group: the fold is the person's preference, on every device.
// A link or a footer jump to a session inside a folded group opens it for this visit only.
const layout = useAgentsLayout()
const layoutReady = ref(false)
const prefRoots = computed(() => {
  const ids = new Set<string>()
  if (showRemoved.value || !['project', 'lead', 'none'].includes(group.value)) return ids
  for (const section of sections.value) if (section.id !== 'ended') for (const root of section.roots) {
    if (root.view.session.project_id && isLeadRoot(root, placementLeadOf(root.view.session.project_id))) ids.add(root.view.session.id)
  }
  return ids
})
// One queue chip per project: on its current lead's row, or on its lead line when no running lead session is here.
const chipRoots = computed(() => queueHeads(liveRoots.value, placementLeadOf))
// Parents are open by default; a deeper fold lasts for this visit (AEON-784).
// A head-count filter forces kept parents open through treeRows' match.
const isExpanded = (branch: Branch): boolean => {
  if (!candidates(branch).length) return false
  const id = branch.view.session.id
  if (prefRoots.value.has(id)) return expanded.value[id] === true || !layout.folded.value.has(branch.view.session.project_id)
  return expanded.value[id] ?? true
}
const knownProjects = () => projectStore.loaded ? new Set(projectStore.projects.map(p => p.id)) : undefined
// The fold button sits in its parent's row, so a click already moves the cursor there.
function toggle(branch: Branch) {
  const id = branch.view.session.id, open = isExpanded(branch)
  if (!prefRoots.value.has(id)) { expanded.value[id] = !open; return }
  delete expanded.value[id]
  const project = branch.view.session.project_id
  layout.setFolded(open ? [project] : [], open ? [] : [project], knownProjects())
}
// Collapse all / Expand all: one muted icon beside the title that writes the same preference.
const foldTargets = computed(() => sections.value.flatMap(section => section.roots).filter(root => prefRoots.value.has(root.view.session.id) && candidates(root).length > 0))
const anyOpen = computed(() => foldTargets.value.some(isExpanded))
const foldNote = ref('')
function foldAll() {
  if (!foldTargets.value.length || filtering.value) return
  const open = anyOpen.value, ids = [...new Set(foldTargets.value.map(root => root.view.session.project_id))]
  for (const root of foldTargets.value) delete expanded.value[root.view.session.id]
  layout.setFolded(open ? ids : [], open ? [] : ids, knownProjects())
  foldNote.value = open ? 'All groups collapsed' : 'All groups expanded'
}
function toggleStopped(branch: Branch) {
  const id = branch.view.session.id
  history.value[id] = !history.value[id]
  expanded.value[id] = true
}
// Ended workers a parent hides until asked: neither live on screen nor keeping their place.
const stoppedChildren = (branch: Branch): number => branch.children.reduce((sum, child) => sum + Number(!countedLive(child.view) && !keepsPlace(child.view)) + stoppedChildren(child), 0)
// What a row says about the sessions under it reads their newest copies, wherever they are placed.
const descendants = (branch: Branch): SessionView[] => branch.children.flatMap(child => [liveOf(child.view), ...descendants(child)])
const activityLine = (branch: Branch, view: SessionView) => view.session.agent_activity_mode === 'off' ? '' :
  (view.session.role === 'coordinator' && !view.session.stopped_at ? workerActivity(descendants(branch), props.now) : '') || currentActivity(view, props.now)
const workingChildren = (branch: Branch) => descendants(branch).filter(sessionWorking).length
// Line 2 says what the session works on, or what it asks, or how it ended; never the state word,
// which lives in the avatar (AEON-1211). A pending control or tier ask leads; an ended row that
// keeps its place says when it ended and when it moves to Ended. `view` is the newest copy; the branch places it.
function lineTwo(branch: Branch, view: SessionView) {
  const lead = view.session.archived_at ? 'Removed' : tierRequestLabel(view) || pendingLabel(view)
  const handedOver = view.session.stopped_at && view.session.handed_over_to_id ? `Handed over to ${lineageName(view.session.handed_over_to_id)}` : ''
  if (keepsPlace(branch.view)) return [lead, handedOver, endedLine(view)].filter(Boolean).join(' · ')
  const step = handedOver || activityLine(branch, view) || currentStep(view, descendants(branch).filter(countedLive).length, props.now)
  // A word more precise than the badge's ("Stop requested", "Lost contact") opens the line.
  const precise = view.status.label !== STATE_LABEL[view.status.state] && view.status.label !== step ? view.status.label : ''
  return [lead, precise, step].filter(Boolean).join(' · ')
}
// A session that ended in the last 10 minutes and keeps its place says so, never its last activity or an
// estimate (AEON-1240): "Finished 22:20 · moves to Ended at 22:30". Only Done finished; any other end
// names its cause as the panel head does ("Cancelled 22:20 · …", "Budget exhausted", "Lost contact"),
// or "Stopped" when the reason is withheld, never "Ended 22:20 · moves to Ended".
function endedLine(view: SessionView) {
  const at = Date.parse(view.session.stopped_at ?? '')
  if (!Number.isFinite(at)) return ''
  const cause = stopReasonLabel(view.session.stop_reason) || view.status.label
  const how = view.status.state === 'done' ? 'Finished' : /^ended$/i.test(cause) ? 'Stopped' : cause
  return `${how} ${clockTime(at)} · moves to Ended at ${clockTime(movesAt(view.session))}`
}
// A filter shows every kept child; otherwise the list hides stopped ones until asked.
const offered = (branch: Branch) => filtering.value ? branch.children.filter(keeps) : candidates(branch)
const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`
type Row = TreeRow<SessionView> & { view: SessionView; primary: string; exec: ReturnType<typeof sessionExecution>; family: boolean; familyEnd: boolean; lead: boolean; leadRoot: boolean; queueChip: boolean; queued: number | null; line: string; sub: number; under: ReturnType<typeof below> }
// Sessions as a tree of any depth (AEON-784): the list decides which children a
// parent offers and whether it is open; sessionTree lays out the rows.
function rowsOf(section: Section): Row[] {
  if (!sectionOpen(section)) return []
  const shown = section.id === 'ended' ? section.roots.filter(branch => showStopped.value || !!filtering.value || containsSelected(branch)) : section.roots
  const rows = treeRows(shown, { children: candidates, isOpen: isExpanded, match: filtering.value })
  return rows.map((row, index) => {
    const view = liveOf(row.branch.view)
    const primary = intendedResult(view)
    const leadRoot = !row.depth && prefRoots.value.has(view.session.id)
    const sub = row.foldable ? shownBelow(row.branch, offered) : 0
    const under = below(row.branch, v => v.name, v => !!props.projects?.asks(v.session.id), liveOf)
    // Line 2 says what it does; a parent always has it, so a fold never changes the row's height.
    // Folded, it sums up what it holds (design AEON-1196: “· 5 workers · 1 problem · 1 question”).
    const doing = lineTwo(row.branch, view)
    const line = row.foldable && !row.open ? [doing, foldSummary({ workers: sub, problems: under.problem, questions: under.ask })].filter(Boolean).join(' · ') : doing || (row.foldable ? plural(sub, 'worker') : '')
    return {
      ...row, view, primary, line, exec: sessionExecution(view, names.value),
      family: row.depth > 0 || row.open, familyEnd: !rows[index + 1]?.depth,
      lead: view.session.role === 'coordinator' || row.branch.children.length > 0,
      leadRoot, queueChip: leadRoot && !section.line && chipRoots.value.has(view.session.id) && hasQueue(view.session.project_id), queued: queuedOf(view.session.project_id), sub, under,
    }
  })
}
const sectionRows = computed(() => new Map(sections.value.map(section => [section.id, rowsOf(section)])))
const rowById = computed(() => new Map([...sectionRows.value.values()].flat().map(row => [row.view.session.id, row])))
const foldLabel = (row: Row) => `${row.open ? 'Fold' : 'Unfold'} ${row.view.name}’s workers: ${plural(row.sub, 'worker')}, ${workingChildren(row.branch)} working`
const rollLabel = (row: Row) => row.under.problem ? `${row.under.problem} ${row.under.problem === 1 ? 'problem' : 'problems'} below` : `${row.under.ask} ${row.under.ask === 1 ? 'asks' : 'ask'} you below`

// Tree keys (AEON-784): on a row or its fold button, ← folds an open parent or
// goes to the parent; → unfolds a folded parent or goes to its first child.
// Other controls in the row keep their own arrow keys.
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
    if (row.open && !filtering.value) toggle(row.branch)
    else if (row.parent) focusSession(row.parent.view.session.id)
  } else if (row.foldable && !row.open && !filtering.value) toggle(row.branch)
  else if (row.open) {
    const first = offered(row.branch)[0]
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
  openHolding(containsSelected)
}, { immediate: true })
// The Ended fold or a folded group of Display's grouping opens for this visit to show a session.
function openHolding(holds: (branch: Branch) => boolean) {
  if (forest.value.some(branch => settled(branch) && holds(branch))) showStopped.value = true
  for (const section of sections.value) if (section.header === 'fold' && flatten(section.roots).some(holds)) groupOpen.value[section.id] = true
}

// Control rights are per session: harness.control in that session's project.
const identity = useSession()
const grant = computed<ControlGrant>(() => ({ person: identity.identity?.principal.kind === 'person', can }))
// The chosen order is remembered per viewer in this browser; the page works without storage.
const viewer = computed(() => identity.identity ? `${identity.identity.tenant.id}.${identity.identity.principal.id}` : '')
watch(viewer, id => {
  sort.value = readSort(id)
  expanded.value = {}; history.value = {}; groupOpen.value = {}; showStopped.value = false; showRemoved.value = false; foldNote.value = ''
  // Folded groups arrive with the person's layout; the rows wait for it (at most 1.5 s), so no group folds under the pointer.
  layoutReady.value = false
  let timer: ReturnType<typeof setTimeout> | undefined
  void Promise.race([layout.ready, new Promise<void>(resolve => { timer = setTimeout(resolve, 1500) })]).then(() => { clearTimeout(timer); if (viewer.value === id) layoutReady.value = true })
  // A different person never sees the last one's held rows.
  hold.reset(); lastGroup.clear(); lastRank.clear(); rememberGroups(current.value)
}, { immediate: true })
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
// The columns are the approved ones (AEON-1211, design AEON-1196): Session · Time · Execution ·
// Computer · Beat. The state lives in each row's avatar and the ticket key opens the name, so
// neither has a column; Display › Sort still orders by both. The tier is in the details.
const SORTS: { key: SortKey; label: string; cls?: string }[] = [
  { key: 'state', label: 'State' }, { key: 'result', label: 'Session' }, { key: 'running', label: 'Time', cls: 'right c-time' }, { key: 'ticket', label: 'Ticket' },
  { key: 'execution', label: 'Execution', cls: 'c-exec' }, { key: 'host', label: 'Computer', cls: 'c-host' }, { key: 'heartbeat', label: 'Beat', cls: 'right c-beat' },
]
const COLUMNS = SORTS.filter(column => column.key !== 'state' && column.key !== 'ticket')
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
// sessions get a bin in table rows; compact rows keep removal in their one
// reserved overflow slot, leaving names room.
const menuOf = (view: SessionView): SessionMenu => sessionMenu(view, { grant: grant.value, canRemove: removal.canRemove(view.session), pending: props.controls[view.session.id], product: brand.value.short_name, now: props.now })
const bin = (view: SessionView) => removal.canRemove(view.session) && quickRemoval(view)
// A project's lead also offers its lead panel and Next up (AEON-1210).
const leadProject = (view: SessionView) => view.session.project_id && !view.session.stopped_at && (view.session.role === 'coordinator' || leadOf(view.session.project_id)?.session_id === view.session.id) ? view.session.project_id : null
// Tier lives in the panel; the row menu also opens its picker.
const hasMenu = (view: SessionView) => { const m = menuOf(view); return !!leadProject(view) || m.control.length > 0 || m.other.length > 0 || !!m.note || (m.remove && !bin(view)) || !serviceTiers.unavailable(view.session) || serviceTiers.canAsk(view.session) }
const menu = ref<{ view: SessionView; anchor: HTMLElement; compact: boolean } | null>(null)
const menuItems = computed(() => menu.value ? menuOf(menu.value.view) : null)
function openMenu(view: SessionView, event: MouseEvent) { menu.value = menu.value?.view.session.id === view.session.id ? null : { view, anchor: event.currentTarget as HTMLElement, compact: !!table.value && getComputedStyle(table.value).display === 'block' } }
function pickTier() {
  const selected = menu.value
  if (!selected) return
  menu.value = null
  serviceTiers.open(selected.view.session, selected.view.name, selected.anchor)
}
const tierRequestLabel = (view: SessionView) => view.session.service_tier_request ? `asks for ${TIER_NAME[view.session.service_tier_request]}` : ''
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
  hold.follow([worker.session.id])
  try {
    const result = await reparentSession(worker.session, lead.session)
    agents.recordSession(result.session)
    toast(`Moved ${worker.name} to ${lead.name}`, result.undoable ? { timeout: 8000, action: { label: 'Undo', run: () => void undoMove(result.event_id, worker.name, worker.session.id) } } : {})
  } catch (error) { toast(error instanceof Error ? error.message : 'Move failed. Refresh and retry.', { tone: 'error' }) }
  finally { moving.value = false }
}
async function undoMove(event: number, label: string, id: string) {
  try {
    const restored = await undoRemoval(event)
    hold.follow([id])
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
function pickLead(section: 'next-up' | null) {
  const selected = menu.value, project = selected ? leadProject(selected.view) : null
  menu.value = null
  if (selected && project) openProjectLead(project, selected.anchor, section)
}
function pickRecovery() {
  const view = menu.value?.view
  menu.value = null
  if (view) void agentRecovery.request(view.session, view.name)
}
function pickRemove() {
  const view = menu.value?.view
  menu.value = null
  if (view) { hold.follow([view.session.id]); void removal.removeOne(view.session, view.name, quickRemoval(view)) }
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
// The Sessions card is always open (AEON-1209): its head carries the list's own tools.
// A page action that leads into the list (a state count, History) has nothing to unfold.
const reveal = () => false
const sortMenu = ref<HTMLElement | null>(null)
function rowClick(event: MouseEvent, id: string) {
  if ((event.target as HTMLElement).closest('a, button')) return
  emit('open', id)
}
// The page menu's History shows History; an open section toggles it as before.
function menuHistory() {
  if (!reveal() || !showRemoved.value) toggleHistory()
}
// A footer jump may name a session this list has folded away. Open its
// ancestors (and the ended bucket, when it lives there) before the page focuses the row.
function revealSession(id: string) {
  showRemoved.value = false
  const open = (branch: Branch, ancestors: string[]): boolean => {
    if (branch.view.session.id === id) {
      for (const ancestor of ancestors) expanded.value[ancestor] = true
      return true
    }
    return branch.children.some(child => open(child, [...ancestors, branch.view.session.id]))
  }
  forest.value.some(branch => open(branch, []))
  const holds = (branch: Branch): boolean => branch.view.session.id === id || branch.children.some(holds)
  openHolding(holds)
  reveal()
}
// ---------- The card head (AEON-1209, design AEON-1196): search, Filter and Display ----------
// It sticks inside the card under the app header; the pills and the column heads stick
// with it. Sticking adds a hairline shadow only, so its box never changes.
const search = ref<HTMLInputElement>()
const searchKeys = computed(() => shortcutKeys('agents.search.focus'))
const searchCaps = computed(() => searchKeys.value.slice(0, 1).flatMap(shortcutCaps).flat())
const filterKeys = computed(() => shortcutKeys('agents.filter.add'))
const draft = ref(props.query ?? '')
let searchTimer: ReturnType<typeof setTimeout> | undefined
watch(() => props.query, value => { if ((value ?? '') !== draft.value.trim()) draft.value = value ?? '' })
watch(draft, value => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => { if (value.trim() !== (props.query ?? '')) emit('search', value.trim().slice(0, 200)) }, 220)
})
function clearSearch() {
  clearTimeout(searchTimer)
  draft.value = ''
  if (props.query) emit('search', '')
  search.value?.focus()
}
// Esc clears the search, then leaves the field on the next press.
function searchKey(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.isComposing) return
  event.preventDefault(); event.stopPropagation()
  if (draft.value) clearSearch()
  else search.value?.blur()
}
function focusSearch() {
  if (!search.value) return false
  search.value.focus(); search.value.select()
  return true
}
// Filter (AEON-1210): the ticket list's "Filter by" menu with State (the head count's filter),
// Project, Harness and Computer; each opens its own choices at the same place.
type FilterView = 'by' | 'state' | FacetKey
const filterButton = ref<HTMLButtonElement>()
const filterMenu = ref<{ anchor: HTMLElement; view: FilterView } | null>(null)
function openFilter() {
  if (!filterButton.value) return false
  filterMenu.value = filterMenu.value ? null : { anchor: filterButton.value, view: 'by' }
  return true
}
function chooseFilter(view: FilterView) {
  const anchor = filterMenu.value?.anchor ?? filterButton.value
  if (anchor) filterMenu.value = { anchor, view }
}
function openFilterAt(view: FilterView, event: MouseEvent) {
  filterMenu.value = filterMenu.value?.view === view ? null : { anchor: event.currentTarget as HTMLElement, view }
}
function closeFilter(restore: boolean) {
  const anchor = filterMenu.value?.anchor
  filterMenu.value = null
  if (restore) (anchor?.isConnected ? anchor : filterButton.value)?.focus()
}
watchEffect(() => { menuOpen.value = !!(menu.value || moveMenu.value || filterMenu.value || sortMenu.value) })
const STATES = Object.keys(FILTER_LABEL) as HeadFilter[]
const stateCount = (state: HeadFilter) => current.value.filter(view => matchesFilter(view, state)).length
function pickState(state: HeadFilter) {
  filterMenu.value = null
  emit('filter', state)
  filterButton.value?.focus({ preventScroll: true })
}
function filterMenuKeys(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="menuitemradio"]:not([aria-disabled="true"]), [role="menuitem"]')]
  if (!items.length) return
  event.preventDefault(); event.stopPropagation()
  const index = items.indexOf(document.activeElement as HTMLElement)
  items[event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus()
}
const harnessName = (id: string) => current.value.find(view => view.session.harness === id)?.harness || id
const facetLabel = (key: FacetKey, value: string) => key === 'project' ? (value === NO_PROJECT ? 'No project' : value) : key === 'harness' ? harnessName(value) : hostLabels.value.get(value) ?? value
const facetText = (key: FacetKey) => facets.value[key].map(value => facetLabel(key, value)).join(', ')
const facetChoicesFor = (key: FacetKey) => facetChoices(showRemoved.value ? props.history ?? [] : current.value, key, value => facetLabel(key, value), facets.value[key])
const activeFacets = computed(() => FACET_KEYS.filter(key => facets.value[key].length > 0))
const FILTER_BY: { view: FilterView; title: string; icon: 'pulse' | 'folder' | 'agent' | 'monitor' }[] = [
  { view: 'state', title: 'State', icon: 'pulse' }, { view: 'project', title: FACET_TITLE.project, icon: 'folder' },
  { view: 'harness', title: FACET_TITLE.harness, icon: 'agent' }, { view: 'computer', title: FACET_TITLE.computer, icon: 'monitor' },
]
const facetView = computed<FacetKey | null>(() => { const view = filterMenu.value?.view; return view === 'project' || view === 'harness' || view === 'computer' ? view : null })
const onCount = (view: FilterView) => view === 'state' ? Number(!!stateFilter.value) : view === 'by' ? 0 : facets.value[view].length
function toggleFacet(key: FacetKey, value: string) {
  const list = facets.value[key]
  emit('facets', { ...facets.value, [key]: list.includes(value) ? list.filter(item => item !== value) : [...list, value] })
}
function clearFacet(key: FacetKey) {
  if (filterMenu.value?.view === key) filterMenu.value = null
  emit('facets', { ...facets.value, [key]: [] })
  filterButton.value?.focus({ preventScroll: true })
}
function clearAll() {
  if (stateFilter.value) emit('clearFilter')
  if (draft.value || props.query) { clearTimeout(searchTimer); draft.value = ''; emit('search', '') }
  if (facetsActive(facets.value)) emit('facets', emptyFacets())
  filterMenu.value = null
  search.value?.focus({ preventScroll: true })
}
// Display › Group by (AEON-1210): the address carries it, like the list's other tools.
function pickGroup(value: GroupBy) { if (value !== group.value) emit('group', value) }
const displayWord = computed(() => groupWord(group.value))
const foldAllShown = computed(() => !showRemoved.value && ['project', 'lead', 'none'].includes(group.value) && foldTargets.value.length > 0)
// What the head counts: the sessions on show, without the dimmed context rows.
const shownCount = computed(() => {
  const views = showRemoved.value ? props.history ?? [] : hold.views.value
  return filtering.value ? views.filter(filtering.value).length : views.length
})
const root = ref<HTMLElement>()
const stick = ref<HTMLElement>()
const stuck = ref(false)
let scroller: HTMLElement | null = null, frame = 0, resize: ResizeObserver | undefined
function measure() {
  frame = 0
  if (!root.value || !stick.value) return
  const top = scroller?.getBoundingClientRect().top ?? 0
  stuck.value = root.value.getBoundingClientRect().top < top - 0.5
}
const onScroll = () => { if (!frame) frame = requestAnimationFrame(measure) }
// Every update keeps the first rows on screen where they are (AEON-1279): WebKit has no scroll
// anchoring, and this page turns Chrome's off, so rows arriving or leaving above would move them.
// The list scrolls with the page, or in its own pane; the sticky heads cover the top of the view.
useReaderAnchor({
  scroller: () => props.pane ? table.value : scroller,
  rows: () => table.value?.querySelectorAll<HTMLElement>('[role="row"]:not(.thead)'),
  top: () => Math.max(...[stick.value, table.value?.querySelector<HTMLElement>(':scope > .thead')].map(head => head?.getBoundingClientRect().bottom ?? -Infinity)),
})
onMounted(() => {
  scroller = root.value?.closest('main') ?? null
  scroller?.addEventListener('scroll', onScroll, { passive: true })
  // The column heads stick right under the head, whatever its height (a pill row comes and goes).
  resize = new ResizeObserver(([entry]) => { root.value?.style.setProperty('--stick-h', `${Math.round(entry.borderBoxSize?.[0]?.blockSize ?? entry.contentRect.height)}px`) })
  if (stick.value) resize.observe(stick.value)
  measure()
})
onBeforeUnmount(() => { clearTimeout(searchTimer); cancelAnimationFrame(frame); scroller?.removeEventListener('scroll', onScroll); resize?.disconnect() })
defineExpose({ toggleHistory, menuHistory, reveal, revealSession, focusSearch, openFilter })
function sessionLinkClick(event: MouseEvent, id: string) {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  emit('open', id)
}
</script>

<template>
  <section ref="root" class="sessions glass-card" :class="{ 'has-layout-dock': layoutDock, pane }" aria-labelledby="sessions-title" @pointerenter="pointerIn" @pointerleave="pointing = false">
    <!-- The card head (AEON-1209, design AEON-1196): the list's own tools. It sticks inside the card,
         at the card's width, under the app header; the pills and the column heads stick with it. -->
    <div ref="stick" class="th-stick" :class="{ stuck }" data-stick>
      <div class="tree-head">
        <h2 class="sess-title"><span class="title-room"><span id="sessions-title">{{ showRemoved ? 'History' : 'Sessions' }}</span><span class="title-ghost" aria-hidden="true">Sessions</span></span><span v-if="loaded && state === 'ready'" class="fs-count" aria-hidden="true">{{ shownCount }}</span></h2>
        <!-- Collapse all / Expand all (AEON-1210): a muted icon in a fixed slot; the icon swaps in place and writes the folded groups. -->
        <span class="fold-all-slot">
          <button
            v-if="foldAllShown" type="button" class="fold-all" :aria-label="anyOpen ? 'Collapse all' : 'Expand all'" :aria-disabled="!!filtering || undefined"
            :data-tip="filtering ? 'Folding waits while a filter is on' : anyOpen ? 'Collapse all' : 'Expand all'" @click="foldAll"
          ><AppIcon :name="anyOpen ? 'collapse-all' : 'expand-all'" :size="15" /></button>
        </span>
        <div class="list-tools" role="group" aria-label="Filter this list">
          <label class="search-field sess-search">
            <AppIcon name="search" :size="14" />
            <input
              id="sessions-q" ref="search" v-model="draft" class="field" type="search" placeholder="Search sessions" maxlength="200" autocomplete="off" spellcheck="false"
              aria-label="Search sessions in this list" :aria-keyshortcuts="searchKeys.join(' ') || undefined" :data-tip="['Filters this list', shortcutHint('agents.search.focus')].filter(Boolean).join(' · ')" @keydown="searchKey"
            />
            <span v-if="!draft && searchCaps.length" class="sk-cap" aria-hidden="true"><KeyCap v-for="key in searchCaps" :key="key" :k="key" /></span>
            <button v-else type="button" class="clear-q" aria-label="Clear the search" @click="clearSearch"><AppIcon name="close" :size="12" /></button>
          </label>
          <button
            ref="filterButton" type="button" class="btn facet-btn tool-btn" :class="{ on: !!stateFilter || activeFacets.length > 0 }" aria-haspopup="menu" :aria-expanded="filterMenu?.anchor === filterButton"
            aria-label="Filter" :aria-keyshortcuts="filterKeys.join(' ') || undefined" :data-tip="filterMenu ? undefined : ['Add a filter', shortcutHint('agents.filter.add')].filter(Boolean).join(' · ')" @click="openFilter"
          ><AppIcon name="filter" :size="13" /><span class="bl">Filter</span></button>
          <button
            type="button" class="btn facet-btn tool-btn sort-tool" :class="{ on: !!sort || !!displayWord }" aria-haspopup="dialog" :aria-expanded="!!sortMenu" :aria-disabled="!sortable || undefined"
            :aria-label="`Display: ${displayWord ? `group ${displayWord}; ` : ''}sort sessions by ${SORTS.find(c => c.key === effectiveSort.key)?.label ?? ''}, ${dirWord}`" :data-tip="sortMenu ? undefined : sortable ? 'Group and sort' : 'Nothing to sort yet'"
            @click="sortMenu = sortMenu || !sortable ? null : ($event.currentTarget as HTMLElement)"
          ><AppIcon name="sort" :size="13" /><span class="bl">Display</span><span v-if="displayWord" class="bl tool-value"> · {{ displayWord }}</span></button>
        </div>
        <span class="grow" />
        <!-- Rows that wait to enter, leave or move while you point here (AEON-1211); otherwise how fresh the list is. -->
        <SessionsHeld v-if="loaded && state === 'ready'" :pending="personHolds ? hold.pending.value : 0":updated-at="agents.sessionsUpdatedAt" :now="now" :stale="listStale" @show-now="hold.showNow()" />
        <span v-if="loaded && state === 'ready'" class="head-tools">
          <button
            v-if="stale.length" type="button" class="btn sm ghost quiet-btn" :class="{ 'clear-stale-off': showRemoved }" :disabled="showRemoved || removal.busy.value" :aria-hidden="showRemoved || undefined"
            :aria-label="`Clear stale: ${stale.length} without a heartbeat for 15 minutes`"
            :data-tip="`${stale.length} without a heartbeat for 15 minutes`" @click="hold.follow(stale.map(s => s.id)); removal.clearStale(stale)"
          ><AppIcon name="trash" :size="13" /><span class="tool-label">Clear stale</span></button>
          <button
            type="button" class="btn sm ghost quiet-btn" :aria-pressed="showRemoved"
            :aria-label="showRemoved ? 'Back to sessions' : 'Show history: every ended or removed session'" @click="toggleHistory"
          ><AppIcon :name="showRemoved ? 'arrow-left' : 'history'" :size="13" /><!-- Both words keep their room, so the toggle never changes width (AEON-541). --><span class="tool-label btn-label"><span :aria-hidden="showRemoved || undefined">History</span><span :aria-hidden="!showRemoved || undefined">Sessions</span></span></button>
        </span>
      </div>
      <!-- Applied filters sit under the head and stick with it; the state filter is the head count's (AEON-780),
           then Project, Harness and Computer (AEON-1210). A pill reopens its choices; × removes it. -->
      <div v-if="stateFilter || terms.length || activeFacets.length" class="pills-row" role="group" aria-label="Applied filters">
        <span v-if="stateFilter" class="facet-control filter-chip">
          <button type="button" class="btn facet-btn on chip-main" aria-haspopup="menu" :aria-expanded="filterMenu?.view === 'state'" :aria-label="`State: ${FILTER_LABEL[stateFilter]}. Change the filter`" @click="openFilterAt('state', $event)">State<span class="facet-value chip-value">{{ FILTER_LABEL[stateFilter] }}</span></button>
          <button type="button" class="facet-x chip-x" aria-label="Show all sessions" data-tip="Show all sessions · Esc" @click="emit('clearFilter')"><AppIcon name="close" :size="11" /></button>
        </span>
        <span v-for="key in activeFacets" :key="key" class="facet-control filter-chip" :data-facet="key">
          <button type="button" class="btn facet-btn on chip-main" aria-haspopup="dialog" :aria-expanded="filterMenu?.view === key" :aria-label="`${FACET_TITLE[key]}: ${facetText(key)}. Change the filter`" @click="openFilterAt(key, $event)">{{ FACET_TITLE[key] }}<span v-clip-tip class="facet-value chip-value">{{ facetText(key) }}</span></button>
          <button type="button" class="facet-x chip-x" :aria-label="`Remove the ${FACET_TITLE[key]} filter`" :data-tip="`Remove the ${FACET_TITLE[key]} filter`" @click="clearFacet(key)"><AppIcon name="close" :size="11" /></button>
        </span>
        <button type="button" class="btn ghost clear-all" @click="clearAll">Clear all</button>
      </div>
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
    <div v-else-if="!loaded || !layoutReady" class="skeleton-rows" role="status" aria-label="Loading sessions">
      <div v-for="i in 5" :key="i" class="sk-row"><span class="skeleton dot" /><span class="skeleton" :style="{ width: `${18 + (i * 7) % 16}%` }" /><span class="skeleton key" /><span class="skeleton" style="width: 12%" /></div>
    </div>
    <p v-else-if="showRemoved && !removedCount" class="state" :role="historyState === 'error' ? 'alert' : undefined">{{ historyState === 'loading' ? 'Loading history…' : historyState === 'error' ? 'History could not be loaded.' : 'No ended sessions yet.' }}</p>
    <template v-else>
    <!-- No session yet: the hint, and still each project's lead line (design AEON-1196, the empty state). -->
    <ConnectHint v-if="!total && !showRemoved" :can-start="canStart" :can-lead="canLead" @start="emit('start')" />
    <div v-if="total || showRemoved || hasLines" ref="table" class="table" :class="{ 'lines-only': !total && !showRemoved }" role="treegrid" :aria-label="showRemoved ? 'Ended sessions' : 'Agent sessions'" @keydown="treeKey" @focusin="onFocusIn" @focusout="onFocusOut">
      <div v-if="total || showRemoved" class="thead" role="row">
        <template v-for="column in COLUMNS" :key="column.key">
        <span role="columnheader" :class="column.cls" :aria-sort="ariaSort(column.key)">
          <button type="button" class="th-sort" :class="{ on: sort?.key === column.key }" :data-tip="sortTip(column)" :aria-describedby="column.key === 'heartbeat' ? 'sort-beat-help' : undefined" @click="sortBy(column.key)">
            <span>{{ column.label }}</span>
            <span v-if="ariaSort(column.key)" class="sort-mark" aria-hidden="true"><AppIcon :name="effectiveSort.dir === 'asc' ? 'arrow-up' : 'arrow-down'" :size="11" :class="{ 'default-sort': !sort }" /></span>
          </button>
        </span>
        </template>
        <span role="columnheader"><span class="sr-only">Actions</span></span>
      </div>
      <div v-if="filtering && !kept.size" class="group-row filter-empty" role="row"><span role="cell"><template v-if="terms.length">No session matches “{{ query }}”{{ stateFilter ? ` among ${FILTER_LABEL[stateFilter].toLowerCase()} sessions` : '' }}.</template><template v-else-if="stateFilter">No session is {{ FILTER_LABEL[stateFilter].toLowerCase() }} right now.</template><template v-else>No session matches these filters.</template></span></div>
      <!-- Each project is one group, its lead row heading it; groups part with a hairline (AEON-1210, design AEON-1196). -->
      <div v-for="section in sections" :key="section.id" class="grp" :class="{ ended: section.id === 'ended' }" role="rowgroup" :aria-label="section.project ? `${section.project.key}${section.project.title ? ` · ${section.project.title}` : ''}` : section.label">
        <div v-if="section.header === 'toggle'" class="group-row ended-head" role="row">
          <span role="rowheader" class="group-label">
            <!-- Open when asked (or while a filter shows every match); a link may show its one ended row without opening it. -->
            <button type="button" class="group-toggle" :aria-expanded="showStopped || !!filtering" @click="showStopped = !showStopped">
              <AppIcon name="chevron-right" :size="12" class="chev" :class="{ turned: showStopped || !!filtering }" />{{ section.label }}<span class="mono">{{ section.count }}</span>
            </button>
            <span v-if="section.note" class="eh-note">{{ section.note }}</span>
          </span>
        </div>
        <div v-else-if="section.header === 'fold'" class="group-row keyed" role="row">
          <span role="rowheader" class="group-label">
            <button type="button" class="group-toggle" :aria-expanded="sectionOpen(section)" @click="groupOpen[section.id] = !sectionOpen(section)">
              <AppIcon name="chevron-right" :size="12" class="chev" :class="{ turned: sectionOpen(section) }" /><span class="group-name">{{ section.label }}</span><span class="mono">{{ section.count }}</span>
            </button>
            <span v-if="section.note" class="eh-note">{{ section.note }}</span>
          </span>
        </div>
        <div v-else-if="section.header === 'static'" class="group-row" role="row">
          <span role="rowheader" class="group-label"><span class="group-name">{{ section.label }}</span><span class="mono">{{ section.count }}</span><span v-if="section.note" class="eh-note">{{ section.note }}</span></span>
        </div>
        <!-- A project without a lead: one slim line (AEON-1196 §3.3), for its queued work and heading its sessions alike. -->
        <div v-if="section.line?.kind === 'none' && section.project" class="row nolead" role="row" :data-project="section.project.key">
          <span role="gridcell" class="nl-in">
            <span class="nl-ic" aria-hidden="true"><AppIcon name="folder" :size="15" /></span><b class="mono nl-key">{{ section.project.key }}</b><span class="nl-t">{{ section.line.queued ? `${qWord(section.line.queued)} queued · ` : '' }}no {{ LEAD_WORDS.l }}</span>
            <button v-if="leadActionAvailable(section.line.lead, 'start')" type="button" class="btn sm nl-btn" :aria-label="`Start a ${LEAD_WORDS.l} for ${section.project.key}`" @click="openStartLead([section.project.id], $event.currentTarget as HTMLElement, true)"><AppIcon name="play" :size="13" />Start a {{ LEAD_WORDS.l }}</button>
            <span v-else class="nl-note">{{ leadActionReason(section.line.lead, 'start') }}</span>
          </span>
        </div>
        <!-- A lead without a running session on this list (starting, waiting for room, paused, can't start) still heads its group. -->
        <div
          v-else-if="section.line?.kind === 'pending' && section.project" class="row lead-row pending" role="row" :data-project="section.project.key" :data-row="`l:${section.project.id}`"
          aria-level="1" :data-state="pendingState(section.line.lead)" :class="{ active: cursor === `l:${section.project.id}` }" tabindex="-1"
          @focusin="emit('focusRow', `l:${section.project.id}`)" @click="openPending($event, section.project)"
        >
          <span role="gridcell" class="c-agent">
            <span class="tree-fold-space" aria-hidden="true" />
            <button type="button" class="agent-link lead-open" :aria-label="`${pendingBand(section.line.lead, section.project).title}, ${pendingWord(section.line.lead)}, lead. ${pendingBand(section.line.lead, section.project).status}. Open the ${LEAD_WORDS.l} panel`" @click.stop="openPending($event, section.project)">
              <span class="bot pending-bot"><LeadBot :busy="pendingBusy(section.line.lead)" small /></span>
              <span class="who">
                <span class="l1"><span class="result">{{ pendingBand(section.line.lead, section.project).title }}</span><span class="role">Lead</span></span>
                <span class="l2"><span class="c-state">{{ pendingWord(section.line.lead) }}</span><span class="l2t" :title="pendingBand(section.line.lead, section.project).status">{{ pendingBand(section.line.lead, section.project).status }}</span></span>
              </span>
            </button>
            <button v-if="hasQueue(section.project.id)" type="button" class="qchip q-l1" :aria-label="`${qTell(queuedOf(section.project.id))} in ${section.project.key}: open Next up`" :data-tip="`${qWord(queuedOf(section.project.id))} queued · open Next up`" @click.stop="openQueue(section.project.id, $event)"><AppIcon name="queue" :size="12" />{{ qWord(queuedOf(section.project.id)) }}<span class="qw"> queued</span></button>
            <button v-if="hasQueue(section.project.id)" type="button" class="qchip q-l2" :aria-label="`${qTell(queuedOf(section.project.id))} in ${section.project.key}: open Next up`" @click.stop="openQueue(section.project.id, $event)"><AppIcon name="queue" :size="12" />{{ qWord(queuedOf(section.project.id)) }}<span class="qw"> queued</span></button>
          </span>
          <span role="gridcell" class="c-time" />
          <span class="execution-host" role="presentation"><span role="gridcell" class="c-exec" /><span role="gridcell" class="c-host" /></span>
          <span role="gridcell" class="right c-beat" />
          <span role="gridcell" class="c-actions" @click.stop><LeadMenu :project-id="section.project.id" :project-key="section.project.key" :lead="section.line.lead" @removed="hold.followHeadings([`p:${$event}`])" /></span>
        </div>
        <div
          v-for="row in sectionRows.get(section.id)" :key="row.view.session.id" class="row agent-state-surface" :data-state="row.view.status.state" role="row"
          :aria-level="row.depth + 1" :aria-posinset="row.posinset" :aria-setsize="row.setsize" :aria-expanded="row.foldable ? row.open : undefined" :aria-selected="selected === row.view.session.id"
          :data-row="`s:${row.view.session.id}`" :data-parent="row.view.session.parent_harness_session_id || undefined" :data-depth="row.depth" :style="{ '--depth': row.depth, ...appearance(row.view.status.state) }" tabindex="-1" @focusin="emit('focusRow', `s:${row.view.session.id}`)"
          :draggable="permittedWorker(row.view) && !moving"
          :data-drop-target="dragged && targetFor(dragged, row.view) ? 'true' : undefined"
          @dragstart="startDrag($event, row.view)" @dragend="endDrag" @dragover="dragOver($event, row.view)" @dragleave="dropOver = ''" @drop="drop($event, row.view)"
          :class="[row.view.status.group, { 'filter-context': row.contextOnly, 'context-only': row.contextOnly, 'drop-over': dropOver === row.view.session.id, worker: row.depth > 0, family: row.family, 'family-start': row.family && !row.depth, 'family-end': row.family && row.familyEnd, 'lead-row': row.leadRoot, active: cursor === `s:${row.view.session.id}`, selected: selected === row.view.session.id }]" :data-project="row.leadRoot ? row.view.projectKey || undefined : undefined" @click="rowClick($event, row.view.session.id)"
        >
          <span v-if="row.depth || row.open" class="tree-lines" aria-hidden="true">
            <span v-for="(continues, level) in row.guides" :key="level" class="tree-guide" :class="{ continues, elbow: level === row.depth - 1, last: level === row.depth - 1 && !continues }" :style="{ '--level': level }" />
            <span v-if="row.open" class="tree-stem" :style="{ '--level': row.depth }" />
          </span>
          <span role="gridcell" class="c-agent">
            <span v-if="row.depth" class="sr-only">Worker of {{ row.parent?.view.name }}. </span>
            <button
              v-if="row.foldable" type="button" class="tree-fold" data-fold="family" tabindex="-1" :disabled="!!filtering" :aria-expanded="row.open" :aria-label="foldLabel(row)"
              :data-tip="row.open ? 'Fold · Left arrow' : 'Unfold · Right arrow'" @click="toggle(row.branch)"
            ><AppIcon name="chevron-right" :size="14" class="chev" :class="{ turned: row.open }" /></button>
            <span v-else class="tree-fold-space" aria-hidden="true" />
            <RouterLink v-slot="{ href }" :to="`/agents/${row.view.session.id}`" custom>
            <a class="agent-link" :href="href" @click="sessionLinkClick($event, row.view.session.id)" :aria-label="`${row.view.harness} ${row.view.name}, ${row.view.status.label}${row.lead ? ', lead' : ''}${row.under.problem ? `, ${rollLabel(row)}` : ''}${row.primary !== row.view.name ? `. ${row.primary}` : ''}`">
              <!-- The avatar carries the state badge only (AEON-1211); its word is the tooltip and part of the name. -->
              <SessionAvatar :view="row.view" :size="30" :lead="row.lead" :tip="timeTip(row.view, now)" />
              <!-- Two lines whatever the state, so a state change never changes the row's height. Line 1 is the
                   name only: the ticket key opens it ("PHAROS-12 · …"), a lead adds Lead (AEON-1211). Compact
                   rows open it with the harness mark and end it with the time. -->
              <span class="who">
                <span class="l1">
                  <HarnessOpener class="opener" :harness="row.view.session.harness" :harness-label="row.view.harness" :exec="row.exec" />
                  <span v-if="row.view.ticket" class="key-inline">{{ row.view.ticket.key }} ·</span>
                  <span class="result" :title="row.primary !== row.view.name ? `${row.primary} · ${row.view.name}` : row.primary">{{ row.primary }}</span>
                  <span v-if="row.lead" class="role" data-tip="Coordinates other sessions">Lead</span>
                  <time class="t-inline" :datetime="row.view.session.stopped_at ?? row.view.session.created_at">{{ timeShort(row.view, now) }}</time>
                </span>
                <!-- Whether it listens (AEON-280) or has an inbox (AEON-282) is said in words in the details. -->
                <span class="l2" :class="{ asks: row.view.status.group === 'needs' }">
                  <span class="l2t" :title="row.line">{{ row.line }}</span>
                </span>
              </span>
            </a>
            </RouterLink>
            <!-- The queue chip opens the lead at Next up: on the title line in the wide table, on the state line in compact rows. -->
            <button v-if="row.queueChip" type="button" class="qchip q-l1" :aria-label="`${qTell(row.queued)} in ${row.view.projectKey}: open Next up`" :data-tip="`${qWord(row.queued)} queued · open Next up`" @click="openQueue(row.view.session.project_id, $event)"><AppIcon name="queue" :size="12" />{{ qWord(row.queued) }}<span class="qw"> queued</span></button>
            <span v-if="row.view.session.stopped_at && row.view.session.handed_over_to_id && !keepsPlace(row.branch.view)" class="lineage">
              <RouterLink :to="`/agents/${row.view.session.handed_over_to_id}`" :title="`Handed over to ${lineageName(row.view.session.handed_over_to_id)}`">Handed over to {{ lineageName(row.view.session.handed_over_to_id) }}</RouterLink>
              <button v-if="stoppedChildren(row.branch)" type="button" class="worker-toggle history-toggle" :aria-expanded="!!history[row.view.session.id]" :aria-label="`Show stopped workers of ${row.view.name}`" @click="toggleStopped(row.branch)">{{ stoppedChildren(row.branch) }} stopped</button>
            </span>
            <!-- Its stopped workers wait behind their count; the line stays whether the parent is open or folded. -->
            <span v-else-if="stoppedChildren(row.branch)" class="worker-tools">
              <button
                v-if="stoppedChildren(row.branch)" type="button" class="worker-toggle history-toggle" :aria-expanded="!!history[row.view.session.id]"
                :aria-label="`${history[row.view.session.id] ? 'Hide' : 'Show'} stopped workers of ${row.view.name}: ${stoppedChildren(row.branch)} stopped`" @click="toggleStopped(row.branch)"
              >{{ stoppedChildren(row.branch) }} stopped</button>
            </span>
            <button v-if="row.queueChip" type="button" class="qchip q-l2" :aria-label="`${qTell(row.queued)} in ${row.view.projectKey}: open Next up`" @click="openQueue(row.view.session.project_id, $event)"><AppIcon name="queue" :size="12" />{{ qWord(row.queued) }}<span class="qw"> queued</span></button>
            <RouterLink v-if="row.view.session.adopted_from_id" class="lineage adopted" :to="`/agents/${row.view.session.adopted_from_id}`" :title="`Adopted from ${lineageName(row.view.session.adopted_from_id)}`">Adopted from {{ lineageName(row.view.session.adopted_from_id) }}</RouterLink>
          </span>
          <span role="gridcell" class="right c-time mono-cell" :data-tip="timeTip(row.view, now)"><time :datetime="row.view.session.stopped_at ?? row.view.session.created_at">{{ timeText(row.view, now) }}</time></span>
          <span class="execution-host" role="presentation">
            <!-- The registry's name and the level, the account under it: "Opus 5.5 · xhigh" over "Claude Max · markus" (AEON-1211). -->
            <span role="gridcell" class="c-exec" :aria-label="[row.exec.model ? row.exec.providerLabel : '', row.exec.modelLine, row.exec.accountLine, row.exec.kind === 'ai' && row.exec.account ? row.view.harness : ''].filter(Boolean).join('. ')">
              <span class="exec-icon"><ExecutionMark :kind="row.exec.kind" :provider="row.exec.provider" /></span>
              <span class="exec-copy">
                <span v-if="row.exec.modelLine" class="exec-model" :title="row.exec.modelLine">{{ row.exec.modelLine }}</span>
                <span class="exec-account" :title="row.exec.accountLine">{{ row.exec.accountLine }}</span>
              </span>
            </span>
            <span role="gridcell" class="c-host">
              <SessionHost :key="`${viewer}:${row.view.session.host}`" :host="row.view.session.host" :label="hostLabels.get(row.view.session.host)" :editable="grant.person" @renamed="renamedHost(row.view.session.host, $event)" />
            </span>
          </span>
          <span role="gridcell" class="right c-beat">
            <time v-if="row.view.session.heartbeat_at" :datetime="row.view.session.heartbeat_at">{{ relativeTime(row.view.session.heartbeat_at, { now }) }}</time>
            <span v-else class="faint">never</span>
          </span>
          <span role="gridcell" class="c-actions">
            <SessionPauseActions :session="row.view.session" compact />
            <button
              v-if="bin(row.view)" type="button" class="icon-btn sm flat bin" :aria-label="`Remove ${row.view.name}`" data-tip="Remove · undo right after"
              :disabled="removal.busy.value" @click="hold.follow([row.view.session.id]); removal.removeOne(row.view.session, row.view.name, true)"
            ><AppIcon name="trash" :size="16" /></button>
            <button
              v-if="hasMenu(row.view)" type="button" class="icon-btn sm flat more" :aria-label="`Actions for ${row.view.name}`" aria-haspopup="menu"
              :aria-expanded="menu?.view.session.id === row.view.session.id" @click="openMenu(row.view, $event)"
            ><AppIcon name="more" :size="16" /></button>
          </span>
        </div>
      </div>
    </div>
    </template>
    <p v-if="projects?.truncated && !showRemoved && loaded" class="truncated">The first 50 projects show their {{ LEAD_WORDS.pl }}. Open a project to see its {{ LEAD_WORDS.l }}.</p>
    <!-- A read behind the lead rows that failed or stopped short says so below the list, never a silent gap (AEON-1210 G2). -->
    <p v-if="sourceQueueNote && !showRemoved && loaded" class="truncated" data-source-note="queue" role="status">{{ sourceQueueNote }}</p>
    <p v-if="projects?.asksPartial && !showRemoved && loaded" class="truncated" data-source-note="questions" role="status"><RouterLink to="/decision-desk">Not every open question could be read, so a lead may ask more than it shows here; see the Decision Desk</RouterLink></p>
    <div v-if="showRemoved && loaded && state === 'ready' && (historyMore || (historyState === 'loading' && removedCount))" class="older">
      <button type="button" class="btn sm ghost quiet-btn" :disabled="historyState === 'loading'" @click="emit('older')">{{ historyState === 'loading' ? 'Loading…' : 'Show older' }}</button>
    </div>
    <FloatingPanel v-if="menu && menuItems" :anchor="menu.anchor" align="end" :width="248" :label="`Actions for ${menu.view.name}`" @close="menu = null">
      <div role="menu" :aria-label="`Actions for ${menu.view.name}`" @keydown="menuKeys">
        <template v-if="leadProject(menu.view)">
          <button type="button" role="menuitem" class="menu-item" data-autofocus @click="pickLead(null)"><AppIcon name="agent" :size="16" /><span class="mi-text"><span>Open the {{ LEAD_WORDS.l }} panel</span><small>Right now, workers, Next up and the checks</small></span></button>
          <button v-if="queuedOf(leadProject(menu.view))" type="button" role="menuitem" class="menu-item" @click="pickLead('next-up')"><AppIcon name="queue" :size="16" /><span class="mi-text"><span>Next up · {{ qWord(queuedOf(leadProject(menu.view))) }}</span><small>The queue, in the {{ LEAD_WORDS.l }} panel</small></span></button>
          <hr class="menu-sep">
        </template>
        <button v-if="permittedWorker(menu.view) && leadsFor(menu.view).length" type="button" role="menuitem" class="menu-item" data-autofocus @click="pickMove">
          <AppIcon name="arrow" :size="16" /><span class="mi-text">Move to {{ LEAD_WORDS.l }}…</span>
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
            <AppIcon name="trash" :size="16" /><span class="mi-text"><span>{{ quickRemoval(menu.view) ? (menu.compact ? 'Remove stale session' : 'Remove') : 'Remove…' }}</span></span>
          </button>
        </template>
        <p v-if="menu.view.session.agent_recovery" class="menu-note">{{ menu.view.session.agent_recovery.detail }}</p>
        <p v-else-if="menuItems.note" class="menu-note">{{ menuItems.note }}</p>
      </div>
    </FloatingPanel>
    <!-- Filter (AEON-1210): the ticket list's "Filter by" menu; each choice opens at the same place. -->
    <FloatingPanel v-if="filterMenu?.view === 'by'" :anchor="filterMenu.anchor" :width="236" label="Add a filter" @close="closeFilter">
      <p class="eyebrow menu-eyebrow">Filter by</p>
      <div role="menu" aria-label="Filter by" @keydown="filterMenuKeys">
        <button
          v-for="(item, index) in FILTER_BY" :key="item.view" type="button" role="menuitem" class="menu-item by-item" :data-autofocus="index === 0 ? '' : undefined" :data-by="item.view"
          @click="chooseFilter(item.view)"
        ><AppIcon :name="item.icon" :size="14" class="by-icon" /><span class="mi-text">{{ item.title }}</span><span v-if="onCount(item.view)" class="on-count mono">{{ onCount(item.view) }}</span><AppIcon name="chevron-right" :size="13" class="by-go" /></button>
      </div>
    </FloatingPanel>
    <SessionFacetMenu
      v-else-if="facetView && filterMenu" :key="facetView" :anchor="filterMenu.anchor" :facet="facetView" :choices="facetChoicesFor(facetView)" :selected="facets[facetView]"
      @toggle="value => facetView && toggleFacet(facetView, value)" @clear="facetView && clearFacet(facetView)" @close="closeFilter"
    />
    <FloatingPanel v-if="filterMenu?.view === 'state'" :anchor="filterMenu.anchor" :width="248" label="Filter sessions" @close="closeFilter">
      <p class="eyebrow menu-eyebrow">Filter by state</p>
      <div role="menu" aria-label="Filter sessions by state" @keydown="filterMenuKeys">
        <button
          v-for="(key, index) in STATES" :key="key" type="button" role="menuitemradio" class="menu-item state-item" :aria-checked="stateFilter === key"
          :aria-disabled="!stateCount(key) && stateFilter !== key ? 'true' : undefined" :data-autofocus="index === 0 ? '' : undefined" :data-filter="key" @click="(stateCount(key) || stateFilter === key) && pickState(key)"
        ><AppIcon :name="stateFilter === key ? 'check' : 'filter'" :size="14" :class="{ off: stateFilter !== key }" /><span class="mi-text">{{ FILTER_LABEL[key] }}</span><span class="mi-count">{{ stateCount(key) }}</span></button>
      </div>
    </FloatingPanel>
    <FloatingPanel v-if="sortMenu" :anchor="sortMenu" :width="280" label="Display" cycle @close="sortMenu = null">
      <!-- Group by (AEON-1210, the DisplayPanel's grid): project by default, or lead, harness, state, computer or none. -->
      <p class="eyebrow menu-eyebrow">Group by</p>
      <div class="group-grid" role="radiogroup" aria-label="Group by">
        <button
          v-for="option in GROUP_BY" :key="option.value" type="button" role="radio" class="group-option" :aria-checked="group === option.value"
          :data-autofocus="group === option.value ? '' : undefined" @click="pickGroup(option.value)"
        >{{ option.label }}</button>
      </div>
      <p class="eyebrow menu-eyebrow sort-eyebrow">Sort</p>
      <div class="sort-pop" role="group" aria-label="Sort sessions">
        <label class="sort-pick">
          <span class="sort-label">Sort</span>
          <select class="sort-select" :value="effectiveSort.key" aria-describedby="sort-beat-help" @change="pickSort">
            <option v-for="column in SORTS" :key="column.key" :value="column.key">{{ column.label }}</option>
          </select>
        </label>
        <button
          type="button" class="icon-btn sort-dir" :aria-label="`Order ${dirWord}; switch to ${effectiveSort.dir === 'asc' ? 'descending' : 'ascending'}`"
          :data-tip="`Order ${dirWord}`" @click="sortBy(effectiveSort.key)"
        ><AppIcon :name="effectiveSort.dir === 'asc' ? 'arrow-up' : 'arrow-down'" :size="14" /></button>
        <p v-if="effectiveSort.key === 'heartbeat'" class="sort-note">{{ HEARTBEAT_HELP }}</p>
        <button v-if="sort" type="button" class="btn sm ghost quiet-btn" data-tip="Order by state, then start time" @click="resetSort">Default order</button>
        <!-- Below every control, so choosing State moves nothing. -->
        <p v-if="group === 'state'" class="sort-note group-note"><AppIcon name="alert" :size="13" />By state, a row moves to another group when its state changes.</p>
      </div>
    </FloatingPanel>
    <FloatingPanel v-if="moveMenu" :anchor="moveMenu.anchor" align="end" :width="248" :label="`Move ${moveMenu.view.name} to ${LEAD_WORDS.l}`" @close="closeMove">
      <div role="menu" :aria-label="`Move ${moveMenu.view.name} to ${LEAD_WORDS.l}`" @keydown="menuKeys">
        <p class="menu-note">Move to {{ LEAD_WORDS.l }}</p>
        <button v-for="lead in leadsFor(moveMenu.view)" :key="lead.session.id" type="button" role="menuitem" class="menu-item move-lead" :title="lead.name" @click="move(moveMenu.view, lead)">
          <AppIcon name="arrow" :size="16" /><span class="mi-text">{{ lead.name }}</span>
        </button>
      </div>
    </FloatingPanel>
  </section>
</template>

<style scoped>
.row.drop-over { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--ink-2); }
.move-lead .mi-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lineage { flex-basis: 100%; display: flex; align-items: center; gap: 8px; min-width: 0; padding: 2px 0 3px 70px; font-size: 11px; color: var(--ink-2); }
.lineage a, .lineage.adopted { color: inherit; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lineage a:hover, .lineage.adopted:hover { text-decoration: underline; }
.lineage .history-toggle { flex-shrink: 0; }
.sessions { overflow: clip; container: sessions / inline-size; padding: 0; }
.sessions.has-layout-dock .tree-head { padding-right: 58px; }
/* In a split the unbordered list pane is the query container: 560 means the handle's
   560 px, without subtracting this card's two hairlines from the threshold. */
.sessions.pane { container: none; display: flex; flex-direction: column; height: var(--pane-h); }
.sessions.pane > * { flex-shrink: 0; }
.sessions.pane > .table { flex: 1 1 auto; min-height: 0; overflow: auto; overscroll-behavior: contain; align-content: start; }
.sessions.pane .th-stick { position: static; }
.sessions.pane .thead { top: 0; }
/* The card head (AEON-1209, design AEON-1196): sticky inside the card, at the card's width, right
   under the app header. Sticking adds a hairline shadow only; its box and insides never change. */
.th-stick { position: sticky; top: 0; z-index: 20; padding: 6px 8px 0; border-radius: var(--radius) var(--radius) 0 0; background: color-mix(in srgb, var(--surface-raised) 94%, transparent); -webkit-backdrop-filter: blur(12px); backdrop-filter: blur(12px); transition: box-shadow .15s ease; }
.th-stick.stuck { box-shadow: 0 8px 14px -12px var(--shadow-color), 0 1px 0 var(--line); }
@media (prefers-reduced-motion: reduce) { .th-stick { transition: none; } }
.tree-head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 10px; min-height: 52px; padding: 4px 10px 6px; }
.sess-title { display: inline-flex; align-items: baseline; gap: 8px; margin: 0; color: var(--ink); font: 650 15px/1.3 var(--font); letter-spacing: -.005em; white-space: nowrap; }
/* The title keeps the room of its longer word, so History never moves the tools after it (AEON-541). */
.title-room { display: inline-grid; }
.title-room > * { grid-area: 1 / 1; }
.title-ghost { visibility: hidden; }
.fs-count { display: inline-block; min-inline-size: 3ch; font: 500 12px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.list-tools { display: inline-flex; align-items: center; flex-wrap: nowrap; gap: 8px; margin-left: 10px; min-width: 0; }
/* Collapse all / Expand all (AEON-1210): a muted ghost icon in a fixed 28 px slot (44 px to touch). */
.fold-all-slot { position: relative; display: grid; place-items: center; flex: none; width: 28px; height: 28px; margin-left: -4px; }
.fold-all { position: relative; display: grid; place-items: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: 8px; background: transparent; color: var(--ink-3); }
@media (hover: hover) { .fold-all:hover { background: var(--row-hover); color: var(--ink-2); } }
.fold-all:focus-visible { box-shadow: var(--focus-ring); }
.fold-all[aria-disabled="true"] { opacity: .45; cursor: default; }
@media (pointer: coarse) { .fold-all::after { content: ''; position: absolute; inset: -8px; } }
.tool-value { font-weight: 500; }
.sess-search { flex: none; width: 216px; }
.sess-search .field { height: var(--ctl-h, 36px); padding-right: 34px; border-radius: 999px; font-size: 13px; }
.sess-search .field::-webkit-search-cancel-button { display: none; }
.sk-cap { position: absolute; right: 9px; pointer-events: none; }
@media (hover: none) { .sk-cap { display: none; } }
.clear-q { position: absolute; right: 7px; display: grid; place-items: center; width: 22px; height: 22px; padding: 0; border: 0; border-radius: 50%; background: var(--chip-bg); color: var(--ink-2); }
.clear-q:hover { color: var(--ink); background: var(--row-selected); }
.clear-q:focus-visible { box-shadow: var(--focus-ring); }
.tool-btn { flex: none; gap: 6px; height: var(--ctl-h, 36px); padding: 0 14px 0 12px; color: var(--ink-2); font-size: 13px; }
.tool-btn:hover, .tool-btn[aria-expanded="true"] { color: var(--ink); }
.tool-btn.on { color: var(--teal-ink); }
.tool-btn[aria-disabled="true"] { opacity: .55; cursor: default; }
.grow { flex: 1 1 0; min-width: 0; }
.head-tools { display: inline-flex; align-items: center; gap: 2px; margin-right: -4px; }
/* One control height across the head (AEON-1209). */
.head-tools .btn { height: var(--ctl-h, 36px); padding: 0 12px; font-size: 13px; }
/* History keeps the stale action's slot, including when the tools wrap onto another line. */
.head-tools .clear-stale-off { visibility: hidden; pointer-events: none; }
.tool-label { white-space: nowrap; }
/* Applied filters, under the head: the state filter is the head count's (AEON-780); × or Esc clears it. */
.pills-row { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 0 10px 8px; }
.facet-control { display: inline-flex; align-items: stretch; flex: none; }
.chip-main { gap: 0; height: var(--ctl-h, 36px); padding: 0 8px 0 14px; border-top-right-radius: 0; border-bottom-right-radius: 0; font-size: 13px; }
.chip-value { color: var(--ink); font-weight: 500; }
.chip-value::before { content: '·'; margin: 0 6px; color: var(--ink-3); }
.chip-x { display: grid; place-items: center; width: 32px; height: var(--ctl-h, 36px); padding: 0; border: 1px solid var(--chip-teal-line); border-left: 0; border-radius: 0 999px 999px 0; background: var(--chip-teal-bg); color: var(--teal-ink); }
@media (hover: hover) { .chip-x:hover { background: var(--row-selected); } }
.chip-x:focus-visible { box-shadow: var(--focus-ring); }
.clear-all { height: var(--ctl-h, 36px); padding: 0 12px; color: var(--ink-2); font-size: 13px; }
/* The filter menu and the Display popover. */
.menu-eyebrow { margin: 0; padding: 6px 10px 4px; }
.state-item { align-items: center; }
.state-item .off { opacity: 0; }
.by-item { align-items: center; }
.by-item > .by-icon { margin-top: 0; color: var(--ink-3); }
.by-item .mi-text { flex: 1; }
.by-item > .by-go { margin-top: 0; color: var(--ink-3); }
.by-item .mi-count { margin-left: 0; color: var(--teal-ink); }
/* Display › Group by: the DisplayPanel's grid; the sort follows under a hairline. */
.group-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 4px; margin: 0 8px; padding: 3px; border-radius: 12px; background: var(--seg-bg); }
.group-option { height: 28px; padding: 0 4px; border: 0; border-radius: 9px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
@media (hover: hover) { .group-option:hover { color: var(--ink); } }
.group-option[aria-checked="true"] { background: var(--seg-on); color: var(--ink); box-shadow: var(--shadow-btn); }
.group-option:focus-visible { box-shadow: var(--focus-ring); }
@media (pointer: coarse), (max-width: 720px) { .group-option { height: 44px; } }
.sort-eyebrow { margin: 10px 8px 0; padding: 10px 2px 4px; border-top: 1px solid var(--line); }
.group-note { display: flex; align-items: flex-start; gap: 6px; }
.group-note > svg { flex: none; margin-top: 2px; color: var(--warn-ink); }
.mi-count { margin-left: auto; font: 500 11.5px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.filter-empty { color: var(--ink-3); font-size: 13px; }
.quiet-btn { color: var(--ink-2); font-weight: 550; }
.quiet-btn:hover { color: var(--ink); }
.quiet-btn[aria-pressed="true"] { background: transparent; box-shadow: none; color: var(--ink-2); }
.quiet-btn[aria-pressed="true"]:hover { background: var(--row-selected); color: var(--ink); }
.quiet-btn .count { margin-left: 2px; font: 500 11.5px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
/* Session · Time · Execution · Computer · Beat · actions (AEON-1211, design AEON-1196): the name takes the room. */
.table { --tree-step: 28px; display: grid; grid-template-columns: minmax(200px, 1fr) 92px minmax(150px, 220px) 132px 80px 108px; padding: 0 0 8px; }
.thead, .row, .group-row { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: center; column-gap: 0; }
.thead { position: sticky; top: var(--stick-h, 0px); z-index: 19; height: 32px; padding: 0 12px; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); background: color-mix(in srgb, var(--surface-raised) 94%, transparent); -webkit-backdrop-filter: blur(12px); backdrop-filter: blur(12px); font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; white-space: nowrap; }
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
/* Each project is one group (AEON-1210, design AEON-1196); groups part with a hairline, never a bar. */
.grp { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; padding: 4px 0; }
.grp + .grp { border-top: 1px solid var(--line); }
.group-row { min-height: 40px; margin: 0 6px; padding: 0 12px; }
.group-label { grid-column: 1 / -1; display: flex; align-items: center; gap: 10px; min-width: 0; min-height: 32px; font: 600 12.5px/1.3 var(--font); color: var(--ink-2); }
.group-label .mono { margin-left: 6px; font: 500 11.5px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.group-name { min-width: 0; overflow-wrap: anywhere; }
.group-row.keyed .group-label { font-size: 13px; color: var(--ink); }
.eh-note { min-width: 0; color: var(--ink-3); font-size: 12px; font-weight: 450; }
.group-toggle { display: inline-flex; align-items: center; gap: 6px; min-height: 32px; margin-left: -6px; padding: 0 8px 0 4px; border: 0; border-radius: 8px; background: transparent; font: inherit; color: inherit; }
@media (pointer: coarse), (max-width: 720px) { .group-toggle { min-height: 44px; } }
.group-toggle:hover { background: var(--row-hover); color: var(--ink); }
.group-toggle:focus-visible { box-shadow: var(--focus-ring); }
.chev.turned { transform: rotate(90deg); }
/* Two label lines and the 10px row padding put the glyph centre near this joint.
   Phones set their own. The stem still starts just below the fold. */
/* Two text lines beside a 30 px avatar: its centre (and the fold's) is the tree's joint. */
.row { --tree-joint: 30px; position: relative; min-height: 60px; margin: 0 6px; padding: 0 4px; border-radius: 10px; outline: none; cursor: pointer; font-size: 13px; }
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
   ends on the child's fold centre, and the last child closes its track.
   The name column is the first (AEON-1211). The row's 4px padding, the name
   cell's 8px padding and half the 24px fold put the fold's centre at 24px,
   which is where the 1px stem sits (its centre stays within half a pixel).
   One tree step reaches the next fold's centre. */
.row > .tree-lines { position: absolute; inset: 0 0 0 24px; padding: 0; pointer-events: none; color: var(--ink-3); }
.tree-guide, .tree-stem { position: absolute; left: calc(var(--level) * var(--tree-step)); top: 0; bottom: 0; width: var(--tree-step); }
.tree-guide.continues::before, .tree-guide.elbow::before { content: ''; position: absolute; top: 0; bottom: 0; width: 1px; background: currentColor; }
.tree-guide.last::before { bottom: auto; height: calc(var(--tree-joint) - 4px); }
.tree-guide.elbow::after { content: ''; position: absolute; top: calc(var(--tree-joint) - 4px); left: 0; width: var(--tree-step); height: 5px; border: solid currentColor; border-width: 0 0 1px 1px; border-radius: 0 0 0 5px; }
.tree-stem { top: calc(var(--tree-joint) + 13px); bottom: 0; width: 1px; background: currentColor; }
/* Every row keeps the fold slot, so names stay on one edge when a parent appears. */
.tree-fold, .tree-fold-space { flex: none; width: 24px; height: 24px; margin-top: 8px; }
.tree-fold { display: grid; place-items: center; padding: 0; border: 0; border-radius: 6px; background: transparent; color: var(--ink-3); cursor: pointer; }
@media (hover: hover) { .tree-fold:hover { background: var(--row-hover); color: var(--teal-ink); } }
.tree-fold:focus-visible { outline: none; box-shadow: var(--focus-ring); }
/* A parent's stopped workers wait behind their count. */
.worker-tools > * { flex: none; }
.worker-tools { display: flex; align-items: center; flex-wrap: nowrap; gap: 4px 6px; flex-basis: 100%; min-width: 0; min-height: 28px; padding: 0 0 6px 70px; color: var(--ink-2); font-size: 11.5px; }
.worker-toggle { display: inline-flex; align-items: center; justify-content: center; gap: 4px; min-height: 28px; padding: 2px 6px; border: 0; border-radius: 6px; background: transparent; color: var(--ink); font: inherit; font-weight: 550; white-space: nowrap; }
.worker-toggle:hover:not(:disabled) { background: var(--row-hover); }
.worker-toggle:disabled { cursor: default; }
.worker-toggle:focus-visible { box-shadow: var(--focus-ring); }
.history-toggle { color: var(--ink-2); font-weight: 450; }
.history-toggle[aria-expanded="true"] { background: var(--row-hover); color: var(--ink); }
.chev { transition: transform .2s ease; }
@media (prefers-reduced-motion: reduce) { .row, .chev { transition: none; } }
/* A filter's context rows (ancestors of a match) stay present and quiet. */
.row.context-only > :not(.c-agent), .row.context-only .who,
.row.filter-context > :not(.c-agent), .row.filter-context .who { opacity: .55; }
.row > .c-agent { align-self: start; display: inline-flex; align-items: flex-start; flex-wrap: wrap; gap: 4px 8px; min-width: 0; padding-block: 10px; }
.agent-link { display: inline-flex; flex: 1 1 0; align-items: center; gap: 8px; min-width: 0; max-width: 100%; color: var(--ink); text-decoration: none; }
.agent-link:focus-visible { box-shadow: var(--focus-ring); border-radius: 6px; }
/* Line 1 names the session (its result, then its own name, which gives way first); line 2 says what it
   works on, asks or how it ended. Two fixed lines: a state change never changes the row's height. */
.who { display: grid; gap: 2px; min-width: 0; flex: 1 1 auto; }
.l1 { display: flex; align-items: center; gap: 7px; min-width: 0; height: 20px; font-size: 13.5px; }
.l2 { display: flex; align-items: center; gap: 6px; min-width: 0; height: 18px; color: var(--ink-3); font-size: 12.5px; }
.l2.asks { color: var(--ink-2); font-weight: 550; }
.l2t { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.result { flex: 0 1 auto; min-width: 0; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* The ticket key opens the name on every form ("PHAROS-12 · …"). */
.key-inline { flex: none; margin-right: -2px; font-weight: 600; white-space: nowrap; }
/* Compact rows only: the opener mark and the time at the end of line 1. */
.opener, .t-inline { display: none; }
.t-inline { flex: none; margin-left: auto; padding-left: 6px; font: 500 11.5px/1 var(--mono); color: var(--ink-3); white-space: nowrap; font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
.row:hover :is(.result, .key-inline) { color: var(--teal-ink); }
/* The lead heads its group (design AEON-1196): its title is the row's strongest line. */
.lead-row .result { font-weight: 700; }
/* The queue chip opens the lead at Next up: on the title line in the wide table, on the state line in compact rows. */
.qchip { position: relative; display: inline-flex; flex: none; align-items: center; gap: 4px; height: 22px; padding: 0 8px; border: 0; border-radius: 999px; background: var(--surface-sunken); color: var(--ink-2); font-size: 11.5px; font-weight: 600; white-space: nowrap; cursor: pointer; }
.qchip:hover { background: var(--row-selected); color: var(--teal-ink); }
.qchip:focus-visible { box-shadow: var(--focus-ring); }
.q-l1 { align-self: center; }
.q-l2 { display: none; }
@media (pointer: coarse) { .qchip::after { content: ''; position: absolute; inset: -11px -4px; } }
/* A lead without a session here: the project's lead state, its panel on click. */
.lead-open { padding: 0; border: 0; background: transparent; font: inherit; color: inherit; text-align: left; cursor: pointer; }
.pending-bot { width: 30px; height: 30px; flex: none; display: grid; place-items: center; border-radius: 9px; background: var(--surface-sunken); }
.row.pending .l2 { color: var(--ink-2); }
/* A project without a lead: one slim line. */
.row.nolead { display: flex; grid-column: 1 / -1; align-items: center; min-height: 44px; margin: 4px 6px; padding: 4px 12px; cursor: default; background: repeating-linear-gradient(135deg, transparent 0 8px, color-mix(in srgb, var(--line) 40%, transparent) 8px 9px); box-shadow: inset 0 0 0 1px var(--line); }
@media (hover: hover) { .row.nolead:hover { background: repeating-linear-gradient(135deg, transparent 0 8px, color-mix(in srgb, var(--line) 40%, transparent) 8px 9px); } }
.nl-in { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 10px; width: 100%; min-width: 0; padding: 0; font-size: 13px; color: var(--ink-2); }
.nl-key { color: var(--ink); font-size: 12.5px; font-variant-ligatures: none; }
.nl-ic { display: grid; place-items: center; width: 28px; color: var(--ink-3); }
.nl-btn, .nl-note { margin-left: auto; }
.nl-note { color: var(--ink-3); font-size: 12px; }
@media (pointer: coarse), (max-width: 720px) { .nl-btn { min-height: 44px; } }
.truncated { margin: 0; padding: 0 18px 12px; font-size: 12px; color: var(--ink-3); }
.truncated a { color: inherit; text-decoration: underline; text-underline-offset: 2px; }
@media (pointer: coarse), (max-width: 720px) { .truncated a { display: inline-flex; align-items: center; min-height: 44px; } }
.role { flex: none; height: 16px; padding: 0 5px; border-radius: 999px; background: var(--secondary-tint-3); color: var(--secondary-ink); font: 600 9px/16px var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.c-exec { display: inline-flex; align-items: center; gap: 8px; min-width: 0; }
/* Marks differ in width (Claude narrow, xAI wide): a fixed slot keeps every row's text on one left edge. */
.exec-icon { display: grid; place-items: center; flex: none; width: 28px; height: 28px; }
.exec-icon :deep(svg) { max-width: 28px; height: auto; max-height: 14px; }
.exec-copy { display: grid; min-width: 0; line-height: 1.25; }
.exec-model, .exec-account { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.exec-model { font-size: 12.5px; color: var(--ink); }
.exec-account { font-size: 11.5px; color: var(--ink-3); }
.row .c-beat { font-size: 12.5px; color: var(--ink-2); white-space: nowrap; }
/* Time: running time while live, how long ago once ended, since when while paused (AEON-1211). */
.row .c-time { font-size: 12px; color: var(--ink-2); white-space: nowrap; font-variant-numeric: tabular-nums; }
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
@container sessions (max-width: 1000px) {
  .table { grid-template-columns: minmax(160px, 1fr) 84px minmax(128px, 180px) 132px 64px 108px; }
}
/* The menu floats outside the list container: size its targets by viewport. */
@media (max-width: 720px) { .menu-item { min-height: 44px; align-items: center; } .menu-item > svg { margin-top: 0; } }
/* The list's own width decides its form (design AEON-1196): the table down to 560 px, compact
   rows below. A table under 880 px (beside the docked panel) keeps Time, the model and the beat;
   the computer and the account line move to the details. */
@container sessions (max-width: 880px) {
  .table { grid-template-columns: minmax(108px, 1fr) 84px minmax(96px, 150px) 64px 108px; }
  .thead > .c-host, .execution-host > .c-host, .exec-account { display: none; }
}
/* The table already summarizes a lead's workers on line 2. Keep a separate
   summary track only where it carries an action or a problem/ask below. */
@container sessions (min-width: 560px) {
  .kid-count { display: none; }
  .worker-tools:not(:has(.worker-toggle, .roll)) { display: none; }
}
/* Compact rows (AEON-1211): a list under 560 px (the phone, and later the side list) shows each
   session in two lines. Line 1 opens with the 12 px harness mark, names the session and ends with
   its time; line 2 says what it works on. Execution, Computer and Beat move to
   the details and the row menu. 16 px per tree level (AEON-784). */
@container sessions (max-width: 559px) {
  /* A 44 px fold column on touch and phones; 28 px beside a fine pointer. */
  .table { --tree-step: 16px; --fold: 28px; --row-action: 28px; display: block; }
  .grp { display: block; }
  .q-l1 { display: none; }
  .q-l2 { display: inline-flex; }
  .row.pending .execution-host { display: none; }
  .thead, .row > :is(.c-time, .execution-host, .c-beat) { display: none; }
  /* Flex, not an inline line box: the Ended toggle is a button and its baseline
     made that header ~20px taller than the text headers, so the row jumped
     when a phone session moved from Pausing to Ended (AEON-790). */
  .group-row { display: flex; align-items: center; height: 26px; margin: 12px 8px 2px; padding: 0 8px; }
  /* Two 20 + 18 px lines (gap 2) beside the 30 px avatar put its centre, the fold's and the tree's
     joint 29 px below the row's top. */
  .row { --tree-joint: 29px; display: grid; grid-template-columns: var(--fold) 30px minmax(0, 1fr) var(--row-action); column-gap: 8px; align-items: start; min-height: 0; margin: 0 6px; padding: 10px 0 10px calc(10px + var(--depth) * var(--tree-step)); }
  .row > span { padding: 0; }
  .row > .c-agent { grid-column: 1 / 4; display: grid; grid-template-columns: subgrid; align-items: start; row-gap: 0; padding-block: 0; }
  .row.worker .c-agent { padding-left: 0; }
  /* The fold button is centred on the avatar's centre line. */
  .tree-fold, .tree-fold-space { grid-column: 1; grid-row: 1; width: var(--fold); height: var(--fold); margin-top: calc(var(--tree-joint) - 10px - var(--fold) / 2); }
  .agent-link { grid-column: 2 / -1; grid-row: 1; display: grid; grid-template-columns: subgrid; align-items: center; }
  .who { gap: 2px; }
  .l1 { gap: 6px; font-size: 14px; }
  .opener { display: inline-grid; }
  .t-inline { display: inline; }
  /* One name per compact row; the avatar says Lead (design AEON-1196). A list without parents needs no fold column. */
  .role { display: none; }
  .table:not(:has(.tree-fold)) { --fold: 0px; }
  /* Lines run beside the fold buttons: 16 px per level. The content inset (10px)
     and half the fold put its centre on the stem. The elbow is one step long,
     so it ends on the child fold's centre. */
  .row > .tree-lines { left: calc(10px + var(--fold) / 2); }
  .tree-guide.elbow::after { width: var(--tree-step); }
  .tree-stem { top: calc(var(--tree-joint) + var(--fold) / 2); }
  .lineage { grid-column: 2 / -1; padding: 4px 0 0; min-height: 24px; }
  .worker-tools { grid-column: 2 / -1; grid-row: 2; flex-wrap: nowrap; white-space: nowrap; min-height: 44px; margin: 0 0 0 -4px; padding: 0; }
  .worker-toggle { min-height: 44px; padding-inline: 6px; }
  .c-agent > .q-l2 { grid-column: 3; grid-row: 2; justify-self: start; margin-top: 2px; }
  /* One slot stays reserved even while its menu trigger is hidden. Remove,
     Pause, Resume and Change tier stay available in that menu. */
  .row > .c-actions { grid-column: 4; grid-row: 1; flex-direction: row; gap: 0; margin-top: calc(var(--tree-joint) - 10px - var(--row-action) / 2); padding: 0; }
  .act, .c-actions > .pause-controls, .c-actions > .bin { display: none; }
  .row > .c-actions .more { width: var(--row-action); height: var(--row-action); opacity: 0; }
  .row:is(:hover, :focus-within) > .c-actions .more, .row > .c-actions .more[aria-expanded="true"] { opacity: 1; }
}
@media (pointer: coarse), (max-width: 720px) {
  .table { --fold: 44px; --row-action: 44px; }
  .row > .c-actions .more { opacity: 1; }
  /* The Ended toggle is a 44 px target on touch, in a header of its own height. */
  .group-row { height: 44px; margin-block: 4px 0; }
  .group-toggle { height: 44px; }
}
/* Phone tools: 44 px icon buttons; their names stay for screen readers. The list's tools take
   their own line under the title, the search first and widest. */
@container sessions (max-width: 640px) {
  .eh-note { display: none; }
  .th-stick { padding: 4px 4px 0; }
  .tree-head { padding: 4px 6px 6px; }
  .list-tools { order: 3; flex: 1 1 100%; margin-left: 0; }
  .sess-search { flex: 1 1 auto; width: auto; }
  .tool-btn { width: 44px; padding: 0; }
  .tool-btn .bl { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
  .chip-x { width: 44px; }
  .head-tools .btn { min-width: 44px; min-height: 44px; justify-content: center; }
  .head-tools .tool-label { display: none; }
}
/* Touch: 44 px everywhere in the head. */
@media (pointer: coarse) {
  .clear-q::after { content: ''; position: absolute; inset: -11px; }
  .head-tools .btn, .clear-all { min-height: 44px; }
  .chip-x { width: 44px; }
}
</style>
