<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ATTENTION_KINDS, actOnAttention, attentionIdentity, refreshAttentionRelease, listAttention, listAttentionGroups, attentionGrouping, attentionFolds, orderAttentionGroups, collectAttentionGroups, finishAttentionGroups, mergeAttentionRows, previewAttentionBulk, runAttentionBulk, undoAttentionBulk, attentionMoveId, attentionResolution, type AttentionBulkPreview as BulkPreview, type AttentionBulkResult, type AttentionBulkAction, type AttentionGroup, type AttentionFilters, type AttentionItem, type AttentionPage } from '../lib/attention'
import { getProjectAutopilot, saveProjectAutopilot, getStatusAutopilot, saveStatusAutopilot, type ProjectOverride, type AutopilotSettings, type RuleKey } from '../lib/statusAutopilot'
import AttentionGroupMenu from '../components/work/AttentionGroupMenu.vue'
import AttentionBulkPreview from '../components/work/AttentionBulkPreview.vue'
import { createScope, scopeOwner } from '../lib/identityScope'
import { usePreference } from '../lib/preferences'
import { filtersFromQuery, type RowGroup, type TicketRow } from '../lib/ticketList'
import type { ColumnDef, ListPrefs } from '../lib/columns'
import { toast, dismiss, toastBottomClearance } from '../lib/toast'
import { absoluteTime, statusMeta, relativeTime } from '../lib/work'
import { useSession } from '../stores/session'
import { useProjects } from '../stores/projects'
import AppIcon, { type IconName } from '../components/AppIcon.vue'
import StatusIcon from '../components/work/StatusIcon.vue'
import KeyCap from '../components/KeyCap.vue'
import TicketTable from '../components/work/TicketTable.vue'
import ListToolbar from '../components/work/ListToolbar.vue'
import { vClipTip } from '../directives/clipTip'

const route = useRoute(), router = useRouter(), session = useSession(), projects = useProjects()
const scope = createScope(() => scopeOwner(session.identity)), reads = scope.lane(), previews = scope.lane(), stats = scope.lane()
type Row = AttentionItem & { resolved?: 'apply' | 'dismiss'; failure?: string; batchId?: string }
type Attempt = { action: AttentionBulkAction; query: AttentionFilters; preview: BulkPreview; exclude: string[]; key: string; snapshot: Row[] }
type Batch = { id: string; action: AttentionBulkAction; changed: number; undoKey: string; undoChanged: number }
type Group = AttentionGroup & { items: Row[]; next: string | null; loading: boolean; error: string; batch?: Batch; retry?: Attempt }
const flatRows = ref<Row[]>([]), groupRows = ref<Group[]>([])
const running = ref(new Set<string>())
const overlay = ref<{ group: Group; anchor: HTMLElement; mode: 'menu' | 'off' | AttentionBulkAction; query: AttentionFilters; preview?: BulkPreview; override?: ProjectOverride; settings?: AutopilotSettings; loading: boolean; error: string }>()
const ruleFor: Partial<Record<Row['kind'], RuleKey>> = { triage: 'new', cancel: 'backlog', blocked: 'blocked', missed: 'done' }
const groupName = (group: Group) => group.key || group.title || (locale === 'de' ? kinds.get(group.kind!)?.labelDe : kinds.get(group.kind!)?.label) || group.id
const rowBusy = (row: Row) => busy.value || groupRows.value.some(group => running.value.has(group.id) && group.items.includes(row))
const rows = computed(() => grouping.value === 'none' ? flatRows.value : groupRows.value.flatMap(group => group.items))
const counts = ref<AttentionPage['counts']>({}), total = ref(0)
const facets = ref<AttentionPage['facets']>({ projects: [], assignees: [] }), truncated = ref(false), groupsTruncated = ref(false)
const next = ref<string | null>(null), loading = ref(false), busy = ref(false), error = ref('')
const selected = ref(new Set<number>()), undoToast = ref<number>(), selectionBar = ref<HTMLElement>()
const toolbar = ref<InstanceType<typeof ListToolbar>>(), table = ref<InstanceType<typeof TicketTable>>()
const cursor = ref<string | null>(null), collapsed = ref(new Set<string>())
const foldPrefs = usePreference<{ project?: Record<string, boolean>; kind?: Record<string, boolean> }>('needs-attention:folds')
const columnPrefs = usePreference<ListPrefs>('needs-attention:columns')
const locale = document.documentElement.lang.toLowerCase().startsWith('de') ? 'de' : 'en'
const words = (en: string, de: string) => locale === 'de' ? de : en
let pendingGroups: { group: Group; query: AttentionFilters; after?: string }[] = [], activeGroupReads = 0
let generation = 0, selectionResize: ResizeObserver | undefined, rangeAnchor: number | undefined, groupsFromList = false
function measureSelection() {
  const bar = selectionBar.value
  const footer = bar ? parseFloat(getComputedStyle(bar).getPropertyValue('--footer-h')) || 0 : 0
  toastBottomClearance.value = bar ? window.innerHeight - bar.getBoundingClientRect().top - footer : 0
}
watch(selectionBar, bar => {
  selectionResize?.disconnect(); measureSelection()
  if (bar) { selectionResize = new ResizeObserver(measureSelection); selectionResize.observe(bar) }
}, { flush: 'post' })
const filters = computed<AttentionFilters>(() => ({ kind: String(route.query.kind ?? ''), project_id: String(route.query.project_id ?? ''), assignee: String(route.query.assignee ?? ''), q: String(route.query.q ?? '') }))
const grouping = computed(() => attentionGrouping(route.query.group))
const heading = computed(() => projects.byId(filters.value.project_id)?.title ?? words('All projects', 'Alle Projekte'))
const chosen = computed(() => rows.value.filter(row => selected.value.has(row.event_id) && !row.resolved && row.editable))
const applicableChosen = computed(() => chosen.value.filter(row => row.applicable))
const selectable = computed(() => rows.value.filter(row => row.editable && !row.resolved))
const kinds = new Map(ATTENTION_KINDS.map(kind => [kind.id, kind]))
const titleFor = (row: Row) => row.to === 'release' ? row.release_title || words('Choose a release', 'Release wählen') : statusMeta(row.to).label
const rowID = (row: AttentionItem) => `attention-${row.event_id}`
function ticketRow(row: Row): TicketRow {
  const group = groupRows.value.find(group => group.project_id === row.project_id), known = projects.byId(row.project_id)
  const key = group?.key || known?.routeKey || row.key.replace(/-\d+$/, '')
  return { id: rowID(row), key: row.key, title: row.failure || row.title, kind_id: '', kind_slug: 'work', kind_label: 'Work item', is_leaf: true, body: '', fields: {}, state: row.from, priority: null, assignee: null, parent: null, parent_id: null, children_count: 0, position: '', created_at: row.at, updated_at: row.revision, project_key: key, project: { id: row.project_id, key, title: group?.title || known?.title || key } }
}
const rowsById = computed(() => new Map(rows.value.map(row => [rowID(row), ticketRow(row)])))
const attentionById = computed(() => new Map(rows.value.map(row => [rowID(row), row])))
const attentionRow = (row: TicketRow) => attentionById.value.get(row.id)!
const selectedIds = computed(() => new Set(chosen.value.map(rowID)))
const groups = computed<RowGroup[]>(() => grouping.value === 'none' ? [{ key: 'all', label: '', rows: flatRows.value.map(ticketRow), total: total.value }] : groupRows.value.map(group => ({
  key: group.id, label: group.title || (locale === 'de' ? kinds.get(group.kind ?? group.id as Row['kind'])?.labelDe : kinds.get(group.kind ?? group.id as Row['kind'])?.label) || group.key || words('No project', 'Kein Projekt'),
  rows: group.items.map(ticketRow), total: group.total, loaded: group.items.length, hasMore: !!group.next || group.items.length < group.total,
  loadingMore: group.loading, moreError: group.error,
  ...(grouping.value === 'project' && group.project_id ? { project: { id: group.project_id, key: group.key || '', title: group.title || group.key || '' } } : { icon: kinds.get(group.kind ?? group.id as Row['kind'])?.icon }),
})))
const tablePrefs = computed<ListPrefs>(() => ({ visible: [], widths: columnPrefs.value.value?.widths }))
const extraColumns = computed<ColumnDef[]>(() => [
  // loadedFitWidth will not go under these widths. Kind and Since stay near
  // their minima so Apply and Undo remain fully visible at 1024px.
  { id: 'attention-kind', label: words('Kind', 'Art'), sort: null, width: 136, min: 120, max: 300 },
  { id: 'attention-suggestion', label: words('Suggestion', 'Vorschlag'), sort: null, width: 210, min: 170, max: 480 },
  { id: 'attention-since', label: words('Since', 'Seit'), sort: null, width: 70, min: 70, max: 200, end: true },
  { id: 'attention-actions', label: words('Actions', 'Aktionen'), sort: null, width: 172, min: 172, max: 360, end: true },
])
const listFilters = computed(() => ({ ...filtersFromQuery({}), q: filters.value.q, group: grouping.value === 'kind' ? 'type' as const : grouping.value }))
function setFilter(field: keyof AttentionFilters, value: string) { void router.replace({ path: '/tickets', query: { ...route.query, view: 'needs-attention', [field]: value || undefined } }) }
function setGrouping(value: 'project' | 'kind' | 'none') { void router.replace({ path: '/tickets', query: { ...route.query, view: 'needs-attention', group: value } }) }
function clearFilters() { void router.replace({ path: '/tickets', query: { view: 'needs-attention', ...(route.query.group ? { group: route.query.group } : {}) } }) }
function resetVisit() {
  closeOverlay(false); running.value.clear(); stats.cancel()
  generation++; reads.cancel(); pendingGroups = []; activeGroupReads = 0; groupsFromList = false; flatRows.value = []; groupRows.value = []; selected.value.clear(); counts.value = {}; facets.value = { projects: [], assignees: [] }; truncated.value = false; groupsTruncated.value = false; toastBottomClearance.value = 0; total.value = 0; next.value = null; error.value = ''; busy.value = false; loading.value = false; cursor.value = null; collapsed.value = new Set(); rangeAnchor = undefined
  if (undoToast.value) dismiss(undoToast.value)
}
function acceptPage(page: AttentionPage) { counts.value = page.counts; total.value = page.total; facets.value = page.facets; truncated.value = page.facets_truncated }
function appendRows(existing: Row[], page: AttentionPage) {
  const merged = mergeAttentionRows(existing, page)
  existing.splice(0, existing.length, ...merged)
}
function takeGroupPage(group: Group, page: AttentionPage) {
  appendRows(group.items, page)
  group.next = page.next_cursor
  if (!groupsFromList) return
  group.total = page.total
  group.editable = Math.max(group.editable, page.items.filter(row => row.editable).length)
  group.applicable = Math.max(group.applicable, page.items.filter(row => row.editable && row.applicable).length)
  if (grouping.value === 'project') {
    const kind = filters.value.kind as Row['kind'] | ''
    group.counts = kind ? { [kind]: page.counts[kind] ?? page.total } : { ...page.counts }
  } else if (group.kind) group.counts = { [group.kind]: page.total }
}
function groupFilters(group: Group): AttentionFilters {
  if (grouping.value !== 'project') return { ...filters.value, kind: group.kind || group.id }
  const project_id = group.project_id || group.id
  return { ...filters.value, ...(project_id && project_id !== 'none' ? { project_id } : {}) }
}
function drainGroups() {
  while (activeGroupReads < 4 && pendingGroups.length) {
    const request = pendingGroups.shift()!, group = request.group
    activeGroupReads++
    void scope.run(({ after, signal }) => after(listAttention(request.query, signal, request.after), page => {
      takeGroupPage(group, page)
    }), { failed: e => { group.error = e instanceof Error ? e.message : 'Needs attention could not be loaded.' }, settled: () => { group.loading = false; activeGroupReads--; drainGroups() } })
  }
}
function loadGroup(key: string, more = false) {
  const group = groupRows.value.find(group => group.id === key)
  if (!group || group.loading || (more && !group.next)) return
  group.loading = true; group.error = ''
  pendingGroups.push({ group, query: groupFilters(group), after: more ? group.next! : undefined }); drainGroups()
}
function load(more = false) {
  if (loading.value || (more && !next.value)) return
  loading.value = true; error.value = ''
  const query = { ...filters.value }, by = grouping.value
  void reads.run(({ after, signal }) => {
    if (by === 'none') return after(Promise.all([listAttention(query, signal, more ? next.value! : undefined), columnPrefs.ready]), ([page]) => {
      appendRows(flatRows.value, page); acceptPage(page); next.value = page.next_cursor
    })
    // Commit groups, counts and facets in the same turn as the scope check.
    // A bare await leaves a gap where the previous visit can be written back.
    const commitGrouped = (grouped: { groups: AttentionGroup[]; total: number; truncated: boolean }, page: AttentionPage, fromList: boolean) => {
      groupsFromList = fromList
      groupRows.value = orderAttentionGroups(grouped.groups, by).map(group => ({ ...group, items: [], next: null, loading: false, error: '' }))
      acceptPage(page); total.value = grouped.total; groupsTruncated.value = grouped.truncated
      collapsed.value = attentionFolds(groupRows.value, grouped.total, foldPrefs.value.value?.[by], !!query.q)
    }
    const loadOpenGroups = (): Promise<unknown> => {
      const open = groupRows.value.filter(group => !collapsed.value.has(group.id))
      const step = (index: number): Promise<unknown> => {
        if (index >= open.length) return Promise.resolve()
        const batch = open.slice(index, index + 4)
        // Bound parallel reads even when hundreds of groups were saved open.
        return after(Promise.all(batch.map(group => listAttention(groupFilters(group), signal))), pages => {
          pages.forEach((groupPage, pageIndex) => { const group = batch[pageIndex]!; takeGroupPage(group, groupPage) })
          return step(index + 4)
        })
      }
      return step(0)
    }
    return after(Promise.all([listAttentionGroups(query, by, signal), listAttention(query, signal), foldPrefs.ready, columnPrefs.ready]), ([summary, page]) => {
      if (summary) { commitGrouped(summary, page, false); return loadOpenGroups() }
      return after(collectAttentionGroups(by, cursor => cursor ? listAttention(query, signal, cursor) : Promise.resolve(page)), collected => {
        commitGrouped({ ...finishAttentionGroups(collected, page, by, query), total: page.total }, page, true)
        return loadOpenGroups()
      })
    })
  }, { failed: e => { error.value = e instanceof Error ? e.message : 'Needs attention could not be loaded.' }, settled: () => { loading.value = false } })
}
watch([() => scopeOwner(session.identity), filters, grouping], ([owner], previous) => { if (owner !== previous?.[0]) toolbar.value?.closeOverlays(); scope.reset(); resetVisit(); load() }, { immediate: true, flush: 'sync' })
function saveFolds() {
  if (grouping.value === 'none') return
  foldPrefs.save({ ...(foldPrefs.value.value ?? {}), [grouping.value]: Object.fromEntries(groupRows.value.map(group => [group.id, collapsed.value.has(group.id)])) })
}
function toggleGroup(key: string, fold = !collapsed.value.has(key)) {
  const next = new Set(collapsed.value); if (fold) next.add(key); else next.delete(key)
  collapsed.value = next; saveFolds()
  if (!fold && !groupRows.value.find(group => group.id === key)?.items.length) loadGroup(key)
}
function foldAll(fold: boolean) { for (const group of groupRows.value) toggleGroup(group.id, fold) }
function select(row: Row, mode: 'toggle' | 'range' = 'toggle') {
  if (rowBusy(row) || row.resolved || !row.editable) return
  if (mode === 'range' && rangeAnchor !== undefined) {
    const visible = groups.value.filter(group => !collapsed.value.has(group.key)).flatMap(group => group.rows).map(row => attentionById.value.get(row.id)!)
    const start = visible.findIndex(row => row.event_id === rangeAnchor), end = visible.indexOf(row)
    if (start >= 0 && end >= 0) for (const item of visible.slice(Math.min(start, end), Math.max(start, end) + 1)) if (item.editable && !item.resolved && !rowBusy(item) && selected.value.size < 100) selected.value.add(item.event_id)
  } else if (selected.value.has(row.event_id)) selected.value.delete(row.event_id)
  else if (selected.value.size < 100) selected.value.add(row.event_id)
  else toast('Choose up to 100 tickets at a time.')
  rangeAnchor = row.event_id
}
function selectAll(on = true) {
  if (busy.value) return
  if (!on) selected.value.clear()
  else { selected.value = new Set(selectable.value.filter(row => !rowBusy(row)).slice(0, 100).map(row => row.event_id)); if (selectable.value.length > 100) toast('The first 100 editable tickets are selected.') }
}
function selectGroup(key: string, on: boolean) {
  if (busy.value) return
  for (const row of groupRows.value.find(group => group.id === key)?.items ?? []) if (!on) selected.value.delete(row.event_id); else if (row.editable && !row.resolved && !rowBusy(row) && selected.value.size < 100) selected.value.add(row.event_id)
}
function applySelection() {
  const skipped = chosen.value.length - applicableChosen.value.length
  if (skipped) toast(`${skipped} selected suggestions cannot be applied; they stay selected.`)
  act('apply', applicableChosen.value)
}
function act(action: 'apply' | 'dismiss' | 'undo', targets: Row[], focusID?: number) {
  if (busy.value || !targets.length || targets.some(rowBusy)) return
  const snapshot = targets.map(attentionIdentity), visit = generation
  stats.cancel(); busy.value = true
  void scope.run(({ after, signal }) => after(actOnAttention(action, snapshot, signal), response => {
    if (visit !== generation) return
    const successes: Row[] = [], failures: string[] = []
    for (const target of snapshot) {
      const row = rows.value.find(row => row.event_id === target.event_id && row.node_id === target.node_id && row.revision === target.revision)
      if (!row) continue
      const result = response.items.find(result => result.event_id === target.event_id)
      if (!result?.ok || !result.revision || (action !== 'undo' && !result.resolution_event_id)) {
        row.failure = result?.error ?? 'The change could not be confirmed. Reload before trying again.'; failures.push(`${row.key}: ${row.failure}`); continue
      }
      // Advance only rows bound to the release revision this write verified.
      // Other captured identities still need a reload after external changes.
      if (result.release_id && result.release_revision !== undefined && result.previous_release_revision !== undefined) {
        refreshAttentionRelease(rows.value, result)
        row.release_id = result.release_id; row.release_revision = result.release_revision; row.release_project_revision = result.release_project_revision
      }
      row.revision = result.revision; row.failure = ''; row.resolution_event_id = result.resolution_event_id
      if (action === 'undo') { row.resolved = undefined; counts.value[row.kind] = (counts.value[row.kind] ?? 0) + 1; total.value++ }
      else { row.resolved = action; selected.value.delete(row.event_id); counts.value[row.kind] = Math.max(0, (counts.value[row.kind] ?? 0) - 1); total.value = Math.max(0, total.value - 1) }
      const group = groupRows.value.find(group => group.items.includes(row))
      if (group) {
        group.counts[row.kind] = Math.max(0, (group.counts[row.kind] ?? 0) + (action === 'undo' ? 1 : -1))
        group.editable = Math.max(0, group.editable + (action === 'undo' ? 1 : -1))
        if (row.applicable) group.applicable = Math.max(0, group.applicable + (action === 'undo' ? 1 : -1))
        if (action === 'undo' && row.batchId && group.batch && row.batchId === group.batch.id) { group.batch.changed = Math.max(0, group.batch.changed - 1); row.batchId = undefined }
      }
      successes.push(row)
    }
    if (successes.length && action !== 'undo') {
      const identities = successes.map(attentionIdentity)
      undoToast.value = toast(`${successes.length} ${action === 'apply' ? 'applied' : 'dismissed'}${failures.length ? `; ${failures.length} could not be changed` : ''}.`, { key: 'attention-result', timeout: 10000, action: { label: identities.length > 1 ? 'Undo all' : 'Undo', run: () => {
        if (generation !== visit) return
        const current = identities.flatMap(id => { const row = rows.value.find(row => row.event_id === id.event_id && row.revision === id.revision && row.resolution_event_id === id.resolution_event_id); return row ? [row] : [] })
        act('undo', current)
      } } })
    }
    if (failures.length) toast(failures.join('\n'), { tone: 'error', key: 'attention-errors' })
  }), { failed: e => { if (visit === generation) toast(e instanceof Error ? e.message : 'The change could not be confirmed.', { tone: 'error' }) }, settled: () => {
    if (visit !== generation) return
    busy.value = false
    if (focusID) void nextTick(() => {
      if (generation !== visit) return
      const current = rows.value.find(row => row.event_id === focusID)
      document.getElementById(`${current?.resolved ? 'undo' : 'apply'}-${focusID}`)?.focus({ preventScroll: true })
    })
  } })
}
function closeOverlay(restore = true) {
  const anchor = overlay.value?.anchor
  if (overlay.value && (overlay.value.mode === 'apply' || overlay.value.mode === 'dismiss') && overlay.value.loading) running.value.delete(overlay.value.group.id)
  previews.cancel(); overlay.value = undefined
  if (restore && anchor?.isConnected) anchor.focus({ preventScroll: true })
}
function openGroup(key: string, mode: 'menu' | AttentionBulkAction, anchor?: HTMLElement) {
  const group = groupRows.value.find(group => group.id === key)
  if (!group || busy.value || running.value.has(key) || (mode !== 'menu' && !group.editable)) return
  closeOverlay(false)
  const trigger = anchor || document.getElementById(`${mode === 'apply' ? 'group-apply' : 'group-more'}-${key}`)
  if (!trigger) return
  const opened = { group, anchor: trigger, mode, query: { ...groupFilters(group) }, loading: mode !== 'menu', error: '' }
  overlay.value = opened
  if (mode !== 'menu') {
    running.value.add(key)
    void previews.run(({ after, signal }) => after(previewAttentionBulk(mode, opened.query, signal), preview => { if (overlay.value?.group.id === key) overlay.value.preview = preview }), {
      failed: e => { toast(e instanceof Error ? e.message : 'Preview could not be loaded.', { tone: 'error' }); closeOverlay(true) },
      settled: () => { running.value.delete(key); if (overlay.value?.group.id === key) overlay.value.loading = false },
    })
  } else if (group.kind && group.can_manage && ruleFor[group.kind]) {
    overlay.value.loading = true
    void previews.run(({ after, signal }) => after(getStatusAutopilot(signal), settings => { if (overlay.value?.group.id === key) overlay.value.settings = settings }), {
      failed: e => { if (overlay.value) overlay.value.error = e instanceof Error ? e.message : 'Settings could not be loaded.' },
      settled: () => { if (overlay.value) overlay.value.loading = false },
    })
  }
}
function prepareOff() {
  const opened = overlay.value
  if (!opened?.group.project_id || !opened.group.can_manage) return
  opened.mode = 'off'; opened.loading = true; opened.error = ''
  void previews.run(({ after, signal }) => after(getProjectAutopilot(opened.group.project_id!, signal), override => { opened.override = override }), {
    failed: e => { opened.error = e instanceof Error ? e.message : 'Settings could not be loaded.' }, settled: () => { opened.loading = false },
  })
}
function refreshStats() {
  const query = { ...filters.value }, by = grouping.value
  void stats.run(({ after, signal }) => after(Promise.all([listAttention(query, signal), by === 'none' ? null : listAttentionGroups(query, by, signal)]), ([page, summary]) => {
    acceptPage(page)
    if (summary) for (const group of groupRows.value) {
      if (running.value.has(group.id)) continue
      const current = summary.groups.find(current => current.id === group.id)
      group.counts = { ...(current?.counts ?? {}) }; group.editable = current?.editable ?? 0; group.applicable = current?.applicable ?? 0
      group.total = (current?.total ?? 0) + group.items.filter(row => row.resolved).length
      if (current?.override_mode) group.override_mode = current.override_mode
    }
  }), { failed: () => toast(words('The changes were saved, but counts could not be refreshed. Reload this view.', 'Die Änderungen wurden gespeichert, aber die Zahlen konnten nicht aktualisiert werden. Die Ansicht neu laden.'), { tone: 'error' }) })
}
// Each identity is checked again in the same scope continuation as its update.
// Four history reads at a time, one bounded page per loaded row; never infer row
// success from an aggregate count or a sampled skip list.
function reconcile(group: Group, action: 'apply' | 'dismiss' | 'undo', snapshot: Row[], batchId: string, signal: AbortSignal, after: import('../lib/identityScope').After): Promise<unknown> {
  const actor = session.identity!.principal.id
  const step = (index: number): Promise<unknown> => {
    if (index >= snapshot.length) return Promise.resolve()
    const slice = snapshot.slice(index, index + 4)
    return after(Promise.allSettled(slice.map(row => attentionResolution(row, action, actor, signal))), answers => {
      answers.forEach((answer, n) => {
        const captured = slice[n]!, row = group.items.find(row => row.event_id === captured.event_id && row.node_id === captured.node_id && row.revision === captured.revision && row.resolution_event_id === captured.resolution_event_id)
        if (!row) return
        if (answer.status === 'rejected') { row.failure = words('The row could not be refreshed. Reload before trying again.', 'Die Zeile konnte nicht aktualisiert werden. Vor einem neuen Versuch neu laden.'); return }
        const result = answer.value
        if (!result?.revision) return
        row.revision = result.revision; row.resolution_event_id = result.resolution_event_id; row.failure = ''
        row.resolved = action === 'undo' ? undefined : action; row.batchId = action === 'undo' ? undefined : batchId
        // Bulk membership Undo verifies its own revisions on the server. A row
        // Undo uses the history identity and the same existing server check.
        if (row.to === 'release') { row.release_revision = undefined; row.release_project_revision = undefined }
        selected.value.delete(row.event_id)
      })
      return step(index + 4)
    })
  }
  return step(0)
}
function batchMessage(group: Group, action: 'apply' | 'dismiss' | 'undo', result: AttentionBulkResult) {
  const skipped = result.skipped.reduce((sum, skip) => sum + skip.count, 0)
  const verb = words(action === 'apply' ? 'applied' : action === 'dismiss' ? 'dismissed' : 'undone', action === 'apply' ? 'angewendet' : action === 'dismiss' ? 'verworfen' : 'rückgängig')
  return `${result.changed} ${verb} ${words('in', 'in')} ${groupName(group)}${skipped ? words(`; ${skipped} skipped`, `; ${skipped} übersprungen`) : ''}${result.failed.length ? words(`; ${result.failed.length} failed`, `; ${result.failed.length} fehlgeschlagen`) : ''}.${!result.completed ? words(' The run is incomplete. Retry to continue.', ' Der Lauf ist unvollständig. Erneut versuchen, um fortzusetzen.') : ''}`
}
function markBatchFailures(group: Group, result: AttentionBulkResult) {
  for (const failure of result.failed) { const row = group.items.find(row => row.event_id === failure.event_id); if (row) row.failure = failure.error }
  if (result.failed.length || result.skipped.length || !result.completed) toast([!result.completed ? words('The run is incomplete.', 'Der Lauf ist unvollständig.') : '', ...result.skipped.map(skip => `${skip.count}: ${skip.reason} (${skip.sample_keys.join(', ')})`), ...result.failed.map(failure => `${failure.key}: ${failure.error}`)].filter(Boolean).join('\n'), { tone: result.failed.length || !result.completed ? 'error' : 'info', key: `attention-details-${group.id}`, timeout: 10000 })
}
function startBulk(exclude: string[]) {
  const opened = overlay.value
  if (!opened?.preview || (opened.mode !== 'apply' && opened.mode !== 'dismiss')) return
  const attempt: Attempt = { action: opened.mode, query: { ...opened.query }, preview: opened.preview, exclude: [...exclude], key: crypto.randomUUID(), snapshot: opened.group.items.filter(row => !row.resolved && row.event_id <= opened.preview!.through_event_id).map(row => ({ ...row })) }
  closeOverlay(false); runGroup(opened.group, attempt)
}
function runGroup(group: Group, attempt: Attempt) {
  if (busy.value || running.value.has(group.id)) return
  const visit = generation
  stats.cancel(); group.retry = attempt; running.value.add(group.id)
  void scope.run(({ after, signal }) => after(runAttentionBulk(attempt.action, attempt.query, attempt.preview, attempt.exclude, attempt.key, signal), result => {
    group.batch = result.changed ? { id: result.batch_id, action: attempt.action, changed: result.changed, undoKey: crypto.randomUUID(), undoChanged: 0 } : undefined
    if (result.completed) group.retry = undefined
    markBatchFailures(group, result)
    undoToast.value = toast(batchMessage(group, attempt.action, result), { key: `attention-result-${group.id}`, timeout: 10000, tone: result.failed.length || !result.completed ? 'error' : 'info', ...(result.changed ? { action: { label: words('Undo all', 'Alle rückgängig'), run: () => { if (generation === visit) undoGroup(group, result.batch_id) } } } : {}) })
    return after(reconcile(group, attempt.action, attempt.snapshot.filter(row => !attempt.exclude.includes(attentionMoveId(row))), result.batch_id, signal, after), () => { refreshStats() })
  }), { failed: e => { toast(e instanceof Error ? e.message : 'The change could not be confirmed. Retry uses the same batch.', { tone: 'error' }) }, settled: () => {
    running.value.delete(group.id)
    void nextTick(() => { if (generation === visit) document.getElementById(`group-${group.batch ? 'undo' : 'apply'}-${group.id}`)?.focus({ preventScroll: true }) })
  } })
}
function undoGroup(group: Group, batchId: string, inRun = false): Promise<unknown> | undefined {
  if (group.batch?.id !== batchId || (!inRun && (busy.value || running.value.has(group.id)))) return
  const batch = group.batch, snapshot = group.items.filter(row => row.batchId === batchId && row.resolved).map(row => ({ ...row })), visit = generation
  stats.cancel(); if (!inRun) running.value.add(group.id)
  return scope.run(({ after, signal }) => after(undoAttentionBulk(batchId, batch.undoKey, signal), result => {
    markBatchFailures(group, result)
    batch.changed = Math.max(0, batch.changed - Math.max(0, result.changed - batch.undoChanged)); batch.undoChanged = result.changed
    if (result.completed) group.retry = undefined
    toast(batchMessage(group, 'undo', result), { tone: result.failed.length || !result.completed ? 'error' : 'info', key: `attention-result-${group.id}` })
    return after(reconcile(group, 'undo', snapshot, batchId, signal, after), () => { refreshStats() })
  }), { ...(inRun ? {} : { failed: (e: unknown) => { toast(e instanceof Error ? e.message : 'Undo could not be confirmed.', { tone: 'error' }) } }), settled: () => {
    if (!inRun) running.value.delete(group.id)
    void nextTick(() => { if (generation === visit) document.getElementById(`group-apply-${group.id}`)?.focus({ preventScroll: true }) })
  } })
}
function projectChange(mode: 'off' | 'inherit', also = false) {
  const opened = overlay.value
  if (!opened?.group.project_id || !opened.group.can_manage || opened.loading || (mode === 'off' && !opened.override)) return
  const group = opened.group, projectId = group.project_id!, query = { ...opened.query }, previous = opened.override, visit = generation
  const snapshot = group.items.filter(row => !row.resolved).map(row => ({ ...row }))
  closeOverlay(false); stats.cancel(); running.value.add(group.id)
  void scope.run(({ after, signal }) => after(Promise.all([previous || getProjectAutopilot(projectId, signal), also ? previewAttentionBulk('dismiss', query, signal) : null]), ([before, preview]) => after(saveProjectAutopilot(projectId, mode, before.revision, signal), saved => {
    group.override_mode = saved.mode
    // Settings Undo exists even if the optional dismiss fails afterwards.
    let batchId: string | undefined, undoing = false, restoreRevision = saved.revision, restored = false
    const undo = () => {
      if (visit !== generation || undoing || running.value.has(group.id)) return
      stats.cancel(); undoing = true; running.value.add(group.id)
      void scope.run(({ after: then, signal: undoSignal }) => then(restored ? Promise.resolve(null) : saveProjectAutopilot(projectId, before.mode, restoreRevision, undoSignal), returned => {
        if (returned) { group.override_mode = returned.mode; restoreRevision = returned.revision; restored = true }
        return then(batchId ? undoGroup(group, batchId, true) : Promise.resolve(null), () => { toast(words('The previous project setting was restored.', 'Die vorherige Projekteinstellung wurde wiederhergestellt.')); refreshStats() })
      }), { failed: e => { toast(e instanceof Error ? e.message : 'Undo could not be confirmed.', { tone: 'error' }) }, settled: () => { undoing = false; running.value.delete(group.id) } })
    }
    const notify = (suffix = '', tone: 'info' | 'error' = 'info') => { undoToast.value = toast(words(mode === 'off' ? `${groupName(group)} now sets its own: Off.` : `${groupName(group)} follows the workspace again.`, mode === 'off' ? `${groupName(group)} hat jetzt eine eigene Einstellung: Aus.` : `${groupName(group)} folgt wieder dem Workspace.`) + suffix, { tone, key: `attention-result-${group.id}`, timeout: 10000, action: { label: words('Undo', 'Rückgängig'), run: undo } }) }
    notify()
    if (!preview) return
    const attempt: Attempt = { action: 'dismiss', query, preview, exclude: [], key: crypto.randomUUID(), snapshot }
    group.retry = attempt; batchId = preview.preview_token
    group.batch = { id: batchId, action: 'dismiss', changed: 0, undoKey: crypto.randomUUID(), undoChanged: 0 }
    return after(runAttentionBulk('dismiss', query, preview, [], attempt.key, signal), result => {
      batchId = result.changed ? result.batch_id : undefined
      group.batch = result.changed ? { id: result.batch_id, action: 'dismiss', changed: result.changed, undoKey: crypto.randomUUID(), undoChanged: 0 } : undefined
      if (result.completed) group.retry = undefined
      markBatchFailures(group, result); notify(' ' + batchMessage(group, 'dismiss', result), result.failed.length || !result.completed ? 'error' : 'info')
      return after(reconcile(group, 'dismiss', snapshot, result.batch_id, signal, after), () => { refreshStats() })
    })
  })), { failed: e => { toast(e instanceof Error ? e.message : 'The project setting or dismiss could not be confirmed.', { tone: 'error' }) }, settled: () => { running.value.delete(group.id) } })
}
function toggleRule() {
  const opened = overlay.value, rule = opened?.group.kind && ruleFor[opened.group.kind]
  if (!opened?.settings || !opened.group.can_manage || !rule || opened.loading) return
  const before: AutopilotSettings = { ...opened.settings, rules: Object.fromEntries(Object.entries(opened.settings.rules).map(([key, rule]) => [key, { ...rule }])) as AutopilotSettings['rules'] }, group = opened.group, visit = generation
  const changed = { ...before, rules: { ...before.rules, [rule]: { ...before.rules[rule], enabled: !before.rules[rule].enabled } } }
  closeOverlay(false); stats.cancel(); running.value.add(group.id)
  void scope.run(({ after, signal }) => after(saveStatusAutopilot(changed, false, signal), saved => {
    let undone = false
    undoToast.value = toast(words('Status autopilot saved.', 'Status-Autopilot gespeichert.'), { key: `attention-result-${group.id}`, timeout: 10000, action: { label: words('Undo', 'Rückgängig'), run: () => {
      if (generation !== visit || undone || running.value.has(group.id)) return
      running.value.add(group.id)
      void scope.run(({ after: then, signal: undoSignal }) => then(saveStatusAutopilot({ ...before, revision: saved.revision }, false, undoSignal), () => { undone = true; toast(words('The previous rule setting was restored.', 'Die vorherige Regeleinstellung wurde wiederhergestellt.')) }), { failed: e => toast(e instanceof Error ? e.message : 'Undo could not be confirmed.', { tone: 'error' }), settled: () => { running.value.delete(group.id) } })
    } } })
  }), { failed: e => toast(e instanceof Error ? e.message : 'Settings could not be saved.', { tone: 'error' }), settled: () => { running.value.delete(group.id) } })
}
function openGroupDestination() {
  const opened = overlay.value
  if (!opened) return
  const group = opened.group, query = opened.query
  closeOverlay(false)
  void router.push(group.project_id ? { path: `/p/${encodeURIComponent(group.key || group.project_id)}/tickets`, query: { ...(query.q ? { q: query.q } : {}), ...(query.assignee ? { assignee: query.assignee } : {}) } } : '/settings/autopilot')
}
function openRow(row: TicketRow) { void router.push(`/projects/${encodeURIComponent(row.project_key!)}/tickets/${encodeURIComponent(row.key)}`) }
function copyKey(row: TicketRow) { void navigator.clipboard.writeText(row.key).catch(() => toast('The key could not be copied.', { tone: 'error' })) }
function newTab(row: TicketRow) { window.open(`/projects/${encodeURIComponent(row.project_key!)}/tickets/${encodeURIComponent(row.key)}`, '_blank', 'noopener') }
function navigation() { return groups.value.flatMap(group => [ ...(grouping.value === 'none' ? [] : [`group-${group.key}`]), ...(!collapsed.value.has(group.key) ? group.rows.map(row => row.id) : []) ]) }
function focusCursor(id: string) { cursor.value = id; table.value?.focusGrid(); void nextTick(() => table.value?.scrollToRow(id)) }
function keys(event: KeyboardEvent) {
  if (event.defaultPrevented || event.altKey) return
  const target = event.target instanceof HTMLElement ? event.target : null
  const typing = target && (target.isContentEditable || ['TEXTAREA', 'SELECT'].includes(target.tagName) || target instanceof HTMLInputElement && !['checkbox', 'radio', 'button', 'submit', 'reset'].includes(target.type))
  if (event.key === 'Escape' && typing) { target.blur(); event.preventDefault(); return }
  if (typing || overlay.value || target?.closest('[role="dialog"], [role="menu"]')) return
  if (event.ctrlKey || event.metaKey) {
    const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
    if (event.key.toLowerCase() === 'a' && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && !event.shiftKey && target?.closest('.tickets')) { event.preventDefault(); selectAll() }
    return
  }
  if (event.shiftKey) return
  const key = event.key.toLowerCase()
  if (key === '/') { event.preventDefault(); toolbar.value?.focusSearch(); return }
  if (key === 'escape') { selected.value.clear(); toolbar.value?.closeOverlays(); return }
  if (target?.closest('button, a, input') && [' ', 'enter'].includes(key)) return
  const activeID = target?.closest('tr[id]')?.id.replace(/^row-/, '') || cursor.value
  const entries = navigation(), index = activeID ? entries.indexOf(activeID) : -1
  if (['j', 'k', 'arrowdown', 'arrowup'].includes(key)) {
    event.preventDefault()
    const next = index < 0 ? 0 : Math.max(0, Math.min(entries.length - 1, index + (key === 'j' || key === 'arrowdown' ? 1 : -1)))
    if (entries[next]) focusCursor(entries[next]!)
    return
  }
  const group = activeID?.startsWith('group-') ? groupRows.value.find(group => `group-${group.id}` === activeID) : undefined
  const row = activeID ? attentionById.value.get(activeID) : undefined
  if (group && ['arrowleft', 'arrowright', ' ', 'enter'].includes(key)) {
    event.preventDefault()
    const fold = collapsed.value.has(group.id)
    if (key !== 'arrowleft' || !fold) if (key !== 'arrowright' || fold) document.getElementById(`row-group-${group.id}`)?.querySelector<HTMLButtonElement>('.group-toggle')?.click()
    table.value?.focusGrid(); return
  }
  if (row && [' ', 'x'].includes(key)) { event.preventDefault(); select(row); return }
  if (row && key === 'enter') { event.preventDefault(); openRow(rowsById.value.get(rowID(row))!); return }
  if (key === 'a' || key === 'd') {
    if (chosen.value.length) { event.preventDefault(); if (key === 'a') applySelection(); else act('dismiss', chosen.value) }
    else if (group) { event.preventDefault(); openGroup(group.id, key === 'a' ? 'apply' : 'dismiss') }
    else if (row && !row.resolved && row.editable && (key === 'd' || row.applicable)) { event.preventDefault(); act(key === 'a' ? 'apply' : 'dismiss', [row], row.event_id) }
  }
}
onMounted(() => { void projects.load(); window.addEventListener('keydown', keys); window.addEventListener('resize', measureSelection) })
onBeforeUnmount(() => { closeOverlay(false); generation++; scope.dispose(); selectionResize?.disconnect(); toastBottomClearance.value = 0; window.removeEventListener('resize', measureSelection); window.removeEventListener('keydown', keys); if (undoToast.value) dismiss(undoToast.value) })
</script>

<template>
  <section class="attention-page" aria-labelledby="attention-title">
    <header class="attention-head">
      <h1 id="attention-title">{{ heading }}</h1>
      <p>{{ words('Every project you can see. Needs attention collects what the autopilot flagged for a person.', 'Alle sichtbaren Projekte. Braucht Aufmerksamkeit sammelt, was der Autopilot für einen Menschen markiert hat.') }}</p>
      <div class="stat-line" role="group" aria-label="Filter by kind">
        <button v-for="kind in ATTENTION_KINDS" :key="kind.id" type="button" class="stat" :aria-pressed="filters.kind === kind.id" @click="setFilter('kind', filters.kind === kind.id ? '' : kind.id)">
          <StatusIcon v-if="kind.id === 'cancel'" state="cancelled" :size="12" /><AppIcon v-else :name="kind.icon as IconName" :size="12" /><b>{{ counts[kind.id] ?? 0 }}</b><span>{{ locale === 'de' ? kind.labelDe : kind.label.toLowerCase() }}</span>
        </button>
      </div>
    </header>
    <div class="navigation-band">
      <nav class="section-tabs" aria-label="Sections"><RouterLink to="/releases"><AppIcon name="box" :size="15" />Releases</RouterLink><RouterLink class="active" to="/tickets?view=needs-attention" aria-current="page"><AppIcon name="ticket" :size="15" />Tickets</RouterLink><RouterLink to="/knowledge"><AppIcon name="book" :size="15" />{{ words('Knowledge', 'Wissen') }}</RouterLink></nav>
      <nav class="view-tabs" aria-label="Saved views"><RouterLink class="active" :to="{ path: '/tickets', query: { ...route.query, view: 'needs-attention' } }" aria-current="page"><AppIcon name="flag" :size="13" />{{ words('Needs attention', 'Braucht Aufmerksamkeit') }}<AppIcon name="users" :size="12" /></RouterLink></nav>
    </div>
    <ListToolbar ref="toolbar" :key="scopeOwner(session.identity)" :filters="listFilters" :options="() => []" :label="(_dimension, value) => value" :total="loading ? null : total" :loading="loading" density="comfortable" :stuck="false" view="list"
      :attention="{ owner: scopeOwner(session.identity), filters, group: grouping, facets, truncated, locale }"
      @search="value => setFilter('q', value)" @attention-filter="setFilter" @attention-group="setGrouping" @clear-all="clearFilters" @expand-groups="foldAll(false)" @collapse-groups="foldAll(true)" />
    <TicketTable ref="table" :key="`${scopeOwner(session.identity)}:${grouping}`" :selection-disabled="busy" :sort-disabled="true" label="Tickets needing attention" :retry-label="words('Retry', 'Erneut versuchen')" :empty-text="words('Nothing here needs attention.', 'Hier braucht nichts Aufmerksamkeit.')" :expected-rows="6" :groups="groups" :group="listFilters.group" :rows-by-id="rowsById" :cursor-id="cursor" :open-id="null" :query="filters.q" :sort="[]" density="comfortable"
      :loading="loading" :loading-more="loading && rows.length > 0" :error="error" :more-error="rows.length ? error : ''" :has-more="grouping === 'none' && !!next" :filtered="Object.values(filters).some(Boolean)" :hiding-closed="false" :collapsed="collapsed" :total="total" :scroll-root="null" :now="Date.now()" :show-assignee="false" :creating="false" :extra-columns="extraColumns" :prefs="tablePrefs" :locale="locale"
      :selectable="groupRows.some(group => group.editable > 0) || rows.some(row => row.editable)" :selected="selectedIds" :row-selectable="row => !!attentionById.get(row.id)?.editable && !attentionById.get(row.id)?.resolved && !rowBusy(attentionById.get(row.id)!)"
      @select="(row, mode) => select(attentionById.get(row.id)!, mode)" @select-all="selectAll" @select-group="selectGroup" @toggle-group="toggleGroup" @more-in-group="key => loadGroup(key, !!groupRows.find(group => group.id === key)?.next)" @more="load(true)" @retry="load()" @clear-filters="clearFilters" @cursor="id => cursor = id" @open="openRow" @new-tab="newTab" @copy="copyKey" @widths="widths => columnPrefs.save({ widths })">
      <template #cell-title="{ row }"><AppIcon name="ticket" :size="14" /><span v-clip-tip="`${attentionRow(row).title} · ${attentionRow(row).failure || attentionRow(row).reason}`" class="attention-title" :class="{ 'row-error': attentionRow(row).failure }" :role="attentionRow(row).failure ? 'alert' : undefined">{{ attentionRow(row).failure || attentionRow(row).title }}</span></template>
      <template #cell-attention-kind="{ row }"><span class="attention-kind"><StatusIcon v-if="attentionRow(row).kind === 'cancel'" state="cancelled" :size="13" /><AppIcon v-else :name="kinds.get(attentionRow(row).kind)!.icon as IconName" :size="13" />{{ locale === 'de' ? kinds.get(attentionRow(row).kind)!.oneDe : kinds.get(attentionRow(row).kind)!.one }}</span></template>
      <template #cell-attention-suggestion="{ row }"><span class="attention-suggestion" :data-tip="attentionRow(row).reason"><StatusIcon :state="attentionRow(row).from" :size="12" />{{ statusMeta(attentionRow(row).from).label }}<AppIcon name="arrow" :size="11" /><span class="sr-only">{{ words('to', 'zu') }}</span><AppIcon v-if="attentionRow(row).to === 'release'" name="box" :size="12" /><StatusIcon v-else :state="attentionRow(row).to" :size="12" /><span v-clip-tip="titleFor(attentionRow(row))" class="suggestion-to">{{ titleFor(attentionRow(row)) }}</span><AppIcon v-if="!attentionRow(row).resolved && !attentionRow(row).applicable" name="info" :size="13" :data-tip="attentionRow(row).unavailable_reason" /></span></template>
      <template #cell-attention-since="{ row }"><time class="attention-since" :datetime="attentionRow(row).at" :data-tip="absoluteTime(attentionRow(row).at)">{{ relativeTime(attentionRow(row).at) }}</time></template>
      <template #cell-attention-actions="{ row: ticket }"><template v-for="row in [attentionRow(ticket)]" :key="row.event_id">
            <span class="action-stack" :data-resolved="row.resolved" @click.stop>
              <span class="attention-row-actions" :class="{ hidden: row.resolved }" :aria-hidden="!!row.resolved">
                <button :id="`apply-${row.event_id}`" type="button" class="btn sm" :disabled="rowBusy(row) || !row.editable || !row.applicable" :tabindex="row.resolved ? -1 : 0" :aria-label="`Apply to ${row.key}: ${statusMeta(row.from).label} to ${titleFor(row)}`" :data-tip="row.unavailable_reason || row.reason" @click="act('apply', [row], row.event_id)">{{ words('Apply', 'Anwenden') }}</button>
                <button type="button" class="btn sm ghost" :disabled="rowBusy(row) || !row.editable" :tabindex="row.resolved ? -1 : 0" :aria-label="`Dismiss for ${row.key}`" :data-tip="row.editable ? 'Keep it as it is; this suggestion does not return' : 'Editing this ticket needs permission'" @click="act('dismiss', [row], row.event_id)">{{ words('Dismiss', 'Verwerfen') }}</button>
              </span>
              <span class="done-mark" :class="{ hidden: !row.resolved }" :aria-hidden="!row.resolved">{{ row.resolved === 'dismiss' ? words('Dismissed', 'Verworfen') : words('Applied', 'Angewendet') }} · <button :id="`undo-${row.event_id}`" type="button" class="link-btn" :tabindex="row.resolved ? 0 : -1" :disabled="rowBusy(row)" :aria-label="`Undo for ${row.key}`" @click="act('undo', [row], row.event_id)">{{ words('Undo', 'Rückgängig') }}</button></span>
            </span>
      </template></template>
      <template #group-end="{ group: tableGroup }"><template v-for="group in groupRows.filter(item => item.id === tableGroup.key)" :key="group.id">
        <span class="group-meta" :data-cursor="cursor === `group-${group.id}` ? 'true' : undefined">
          <template v-for="kind in ATTENTION_KINDS" :key="kind.id"><span v-if="group.counts[kind.id]" class="kind-count" :data-tip="locale === 'de' ? kind.labelDe : kind.label"><StatusIcon v-if="kind.id === 'cancel'" state="cancelled" :size="12" /><AppIcon v-else :name="kind.icon as IconName" :size="12" />{{ group.counts[kind.id] }}</span></template>
          <span v-if="group.override_mode === 'off'" class="group-note">{{ words('Autopilot off', 'Autopilot aus') }}</span>
        </span>
        <span class="group-actions" @click.stop>
          <span class="group-slot">
            <button :id="`group-apply-${group.id}`" type="button" class="btn sm group-apply" :class="{ hidden: running.has(group.id) || group.retry || (group.batch?.changed ?? 0) > 0 || !group.editable }" :tabindex="running.has(group.id) || group.retry || (group.batch?.changed ?? 0) > 0 || !group.editable ? -1 : 0" :aria-hidden="running.has(group.id) || !!group.retry || (group.batch?.changed ?? 0) > 0 || !group.editable" :disabled="busy || !group.applicable" :aria-label="words(`Apply all in ${groupName(group)}`, `Alles anwenden in ${groupName(group)}`)" aria-haspopup="dialog" :aria-expanded="overlay?.group.id === group.id && overlay.mode === 'apply'" @click="openGroup(group.id, 'apply', $event.currentTarget as HTMLElement)">{{ words('Apply all', 'Alle anwenden') }} <b>{{ group.applicable }}</b></button>
            <span v-if="running.has(group.id)" class="group-state" role="status">{{ overlay?.group.id === group.id && overlay.loading ? words('Loading preview…', 'Vorschau wird geladen…') : words('Applying…', 'Wird angewendet…') }}</span>
            <span v-else-if="group.retry" class="group-state"><button type="button" class="link-btn" @click="runGroup(group, group.retry!)">{{ words('Retry', 'Erneut versuchen') }}</button><template v-if="group.batch?.changed"> · <button :id="`group-undo-${group.id}`" type="button" class="link-btn" :disabled="busy" @click="undoGroup(group, group.batch!.id)">{{ words('Undo', 'Rückgängig') }}</button></template></span>
            <span v-else-if="group.batch?.changed" class="group-state">{{ group.batch.changed }} {{ words(group.batch.action === 'apply' ? 'applied' : 'dismissed', group.batch.action === 'apply' ? 'angewendet' : 'verworfen') }} · <button :id="`group-undo-${group.id}`" type="button" class="link-btn" :disabled="busy" @click="undoGroup(group, group.batch!.id)">{{ words('Undo', 'Rückgängig') }}</button></span>
            <span v-else-if="!group.editable" class="group-state group-note" :data-tip="words('Editing these tickets needs permission', 'Diese Tickets zu ändern braucht eine Berechtigung')">{{ words(group.total > group.items.filter(row => row.resolved).length ? 'View only' : 'All resolved', group.total > group.items.filter(row => row.resolved).length ? 'Nur ansehen' : 'Alles erledigt') }}</span>
          </span>
          <button :id="`group-more-${group.id}`" type="button" class="icon-btn sm" :disabled="busy || running.has(group.id)" :aria-label="words(`More for ${groupName(group)}`, `Mehr zu ${groupName(group)}`)" aria-haspopup="menu" :aria-expanded="overlay?.group.id === group.id && overlay.mode === 'menu'" @click="openGroup(group.id, 'menu', $event.currentTarget as HTMLElement)"><AppIcon name="more" :size="16" /></button>
        </span>
      </template></template>
    </TicketTable>
    <AttentionBulkPreview v-if="overlay?.preview && (overlay.mode === 'apply' || overlay.mode === 'dismiss')" :anchor="overlay.anchor" :name="groupName(overlay.group)" :action="overlay.mode" :preview="overlay.preview" :locale="locale" @close="closeOverlay" @run="startBulk" />
    <AttentionGroupMenu v-if="overlay && (overlay.mode === 'menu' || overlay.mode === 'off')" :key="`${overlay.group.id}:${overlay.mode}`" :anchor="overlay.anchor" :group="overlay.group" :name="groupName(overlay.group)" :mode="overlay.mode" :locale="locale" :loading="overlay.loading" :error="overlay.error" :rule-enabled="overlay.group.kind && ruleFor[overlay.group.kind] ? overlay.settings?.rules[ruleFor[overlay.group.kind]!].enabled : undefined" @close="closeOverlay" @dismiss="openGroup(overlay!.group.id, 'dismiss', overlay!.anchor)" @off="prepareOff" @turn-off="also => projectChange('off', also)" @follow="projectChange('inherit')" @rule="toggleRule" @open="openGroupDestination" />
    <div class="list-footer">
      <p class="list-count">{{ rows.length }} {{ words('of', 'von') }} {{ Math.max(total + rows.filter(row => row.resolved).length, rows.length) }} {{ words('shown', 'gezeigt') }}</p>
      <p v-if="groupsTruncated" role="status">{{ words('Only the first 500 groups are shown. Narrow the list with filters.', 'Nur die ersten 500 Gruppen werden gezeigt. Die Liste mit Filtern eingrenzen.') }}</p>
      <button v-if="grouping === 'none' && next" type="button" class="btn sm" :disabled="loading" @click="load(true)">{{ loading ? words('Loading…', 'Wird geladen…') : words('Load 50 more', '50 weitere laden') }}</button>
    </div>
    <div v-if="chosen.length" class="selection-dock"><div ref="selectionBar" class="selection-bar" role="toolbar" aria-label="Selected tickets"><span><b>{{ chosen.length }}</b> {{ words('selected', 'ausgewählt') }}</span><span class="selection-hint">{{ chosen.length - applicableChosen.length ? `${chosen.length - applicableChosen.length} cannot be applied; they stay selected` : words('Apply uses each row’s own suggestion', 'Anwenden nutzt den Vorschlag jeder Zeile') }}</span><button type="button" class="btn sm primary" :aria-label="`Apply ${chosen.length}`" :disabled="busy || !applicableChosen.length" @click="applySelection">{{ words('Apply', 'Anwenden') }} {{ chosen.length }}<KeyCap k="A" /></button><button type="button" class="btn sm" :aria-label="`Dismiss ${chosen.length}`" :disabled="busy" @click="act('dismiss', chosen)">{{ words('Dismiss', 'Verwerfen') }} {{ chosen.length }}<KeyCap k="D" /></button><button type="button" class="btn sm ghost" aria-label="Clear the selection" :disabled="busy" @click="selected.clear()"><AppIcon name="close" :size="13" />{{ words('Clear', 'Löschen') }}<KeyCap k="esc" /></button></div></div>
  </section>
</template>

<style scoped>
.attention-page { max-width: 1800px; margin: 0 auto; width: 100%; padding: 18px 28px 100px; }
.attention-head { display: flex; align-items: baseline; flex-wrap: wrap; gap: 6px 18px; padding-bottom: 14px; }
.attention-head h1 { font-size: 26px; overflow-wrap: anywhere; }
.attention-head > p { color: var(--ink-2); font-size: 13px; }
.stat-line { flex-basis: 100%; display: flex; flex-wrap: wrap; gap: 4px 6px; }
.stat { display: inline-flex; align-items: center; gap: 6px; min-height: 30px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font-size: 12px; white-space: nowrap; }
.stat b { width: 4ch; text-align: right; font-family: var(--mono); font-variant-numeric: tabular-nums; }
.stat[aria-pressed="true"] { color: var(--teal-ink); background: var(--row-selected); }
.navigation-band { display: flex; flex-wrap: wrap; gap: 12px 18px; padding: 8px 0; border-block: 1px solid var(--line); }
.section-tabs, .view-tabs { display: flex; align-items: center; gap: 4px; }
.section-tabs a, .view-tabs a { display: flex; align-items: center; justify-content: center; gap: 6px; min-height: 36px; padding: 0 12px; border-radius: 8px; font-size: 13px; color: var(--ink-2); text-decoration: none; }
a.active { background: var(--row-selected); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--line); }
.attention-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--ink); }
.row-error { color: var(--danger); }
.attention-kind, .attention-suggestion { display: inline-flex; align-items: center; gap: 6px; min-width: 0; color: var(--ink-2); font-size: 12.5px; }
.attention-kind { white-space: nowrap; }
.attention-suggestion .suggestion-to { overflow: hidden; text-overflow: ellipsis; font-weight: 600; color: var(--ink); }
.attention-kind svg { color: var(--teal-ink); }
.attention-since { font: 500 12px/1 var(--mono); color: var(--ink-2); }
.action-stack { display: grid; justify-items: end; }
.action-stack > * { grid-area: 1 / 1; }
.attention-row-actions, .done-mark { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.done-mark { font-size: 12px; color: var(--ink-2); }
.hidden { visibility: hidden; pointer-events: none; }
.link-btn { border: 0; background: transparent; color: var(--teal-ink); font-size: inherit; padding: 4px 0; }
.group-actions { display: inline-flex; align-items: center; gap: 6px; flex: none; }
.group-slot { display: grid; min-width: max-content; align-items: center; min-height: 32px; }
.group-slot > * { grid-area: 1 / 1; }
.group-slot::before { content: "9999 angewendet · Rückgängig"; visibility: hidden; grid-area: 1 / 1; font-size: 12px; }
.group-state { justify-self: end; font-size: 12px; white-space: nowrap; }
.group-apply { justify-self: end; }
.group-apply b { min-width: 4ch; text-align: right; font-family: var(--mono); }
.group-meta { min-height: 22px; display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.kind-count { display: inline-flex; align-items: center; gap: 4px; height: 22px; padding: 0 7px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font: 600 11px/1 var(--mono); }
.group-note { color: var(--ink-3); font-size: 12px; }
.attention-page :deep(.group-row:has([data-cursor="true"]) th) { background: var(--row-selected); }
.attention-page :deep(.ticket-row:has([data-resolved]) td:not([data-column-id="attention-actions"]):not(.c-check)) { opacity: .5; }
.attention-page :deep(.title-cell) { flex-wrap: nowrap; }
.attention-page :deep(.ticket-row > .c-title > .row-actions) { display: none; }
.attention-page :deep(td[data-column-id="attention-actions"] .cell) { overflow: visible; }
.list-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding-top: 10px; color: var(--ink-2); font-size: 12px; }
.list-count { font: 400 12px/1.5 var(--mono); }
.selection-dock { position: fixed; left: 0; right: 0; bottom: calc(var(--footer-h, 0px) + 18px + env(safe-area-inset-bottom)); z-index: 30; display: flex; justify-content: center; pointer-events: none; }.selection-bar { pointer-events: auto; display: flex; flex-wrap: wrap; gap: 8px 10px; align-items: center; max-width: calc(100vw - 32px); padding: 8px 8px 8px 16px; border-radius: 16px; background: var(--surface-raised); box-shadow: var(--shadow-pop); font-size: 13px; }.selection-bar b { font-family: var(--mono); }.selection-hint { color: var(--ink-3); font-size: 12px; }
.selection-bar .btn.primary { box-shadow: none; }
.selection-bar .btn.primary:focus-visible { outline: 2px solid var(--teal-ink); outline-offset: 2px; }
@media (max-width: 720px) {
 .attention-page { padding: 14px 12px 140px; }
 .attention-head h1 { font-size: 24px; }
 .stat-line { flex-wrap: nowrap; overflow-x: auto; padding-block: 2px; }
 .stat { min-height: 44px; flex: none; }
 .navigation-band { gap: 4px; }
 .section-tabs a, .view-tabs a { min-height: 44px; padding-inline: 8px; }
 .attention-title { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; white-space: normal; line-height: 1.35; height: 2.7em; }
 .attention-row-actions .btn, .link-btn, .group-actions .btn, .group-actions .icon-btn { min-height: 44px; }
 .attention-page :deep(.group-end) { display: grid; grid-template-columns: minmax(0, 1fr); padding-left: 0; width: 100%; }
 .group-meta { min-height: 22px; }
 .group-actions { margin-left: auto; justify-self: end; }
 .group-actions .icon-btn { min-width: 44px; }
 .selection-dock { bottom: var(--footer-h, 0px); }
 .selection-bar { width: 100%; max-width: 100%; border-radius: 0; padding: 10px 12px calc(10px + env(safe-area-inset-bottom)); }
 .selection-hint { display: none; }
}
@media (pointer: coarse) { .stat, .section-tabs a, .view-tabs a, .attention-row-actions .btn, .link-btn, .group-actions .btn, .group-actions .icon-btn { min-height: 44px; }
 .group-actions .icon-btn { min-width: 44px; } }
</style>
