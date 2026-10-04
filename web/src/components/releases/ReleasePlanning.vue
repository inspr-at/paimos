<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { DELIVERY_ACTIONS, type DeliveryCommit } from '../../lib/deliveryChanges'
import { usePlanningMoves } from '../../lib/usePlanningMoves'
import { useSession } from '../../stores/session'
import { can } from '../../lib/authz'
import DeliveryMoveSheet from './DeliveryMoveSheet.vue'
import { api } from '../../lib/api'
import { planningQuery, releaseName, type MatchCounts, type PlanningSource } from '../../lib/deliveryPlanning'
import { readPreference, writePreference } from '../../lib/preferences'
import { releaseScope } from '../../lib/releaseScope'
import type { ListFilters } from '../../lib/ticketList'
import { useReleasePlanning } from '../../lib/useReleasePlanning'
import PlanningReleaseRow from './PlanningReleaseRow.vue'
import PlanningWork from './PlanningWork.vue'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ applyVersion?: number; projectId: string; projectKey: string; person: string; filters: ListFilters; hideStates?: string[]; scrollRoot: HTMLElement | null; now: number; density: 'compact' | 'comfortable' }>()
const emit = defineEmits<{ open: [key: string]; copy: [key: string]; newTab: [key: string]; scope: [id: string]; summary: [value: { total: number | null; loading: boolean; incomplete: boolean }]; menu: [releaseId: string, anchor: HTMLElement]; abandoned: [anchor: HTMLElement] }>()
const root = ref<HTMLElement>()
let hovered: Element | null = null
function activeElements() {
  return [hovered, document.activeElement].filter((el): el is Element => !!el && !!root.value?.contains(el) && !!el.closest('[data-planning-block]'))
}
function canInsert(source: PlanningSource) {
  const active = activeElements()
  if (moves?.drag.value || moves?.draft.value) return false
  if (!active.length) return true
  if (source === 'overview') return false
  const point = [...(root.value?.querySelectorAll('[data-insertion]') ?? [])].find(el => el.getAttribute('data-insertion') === source)
  if (!point) return false
  return !active.some(el => !!(point.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING))
}
const planning = useReleasePlanning({ canInsert })
const { overview, expanded, work, loading, stale, error, pageError, pending: pendingPages, inFlight, queued, budgetBlocked, rendered, paging, continuations } = planning
const pendingExpansion = ref(false)
const pending = computed(() => pendingPages.value + Number(pendingExpansion.value))
const mode = ref<'checking' | 'journey' | 'releases' | 'error'>('checking')
const modeError = ref(''), preferenceError = ref('')
const owner = computed(() => JSON.stringify([props.projectId, props.person]))
const parsedQuery = computed(() => {
  try { return { query: planningQuery(props.projectId, props.filters, props.hideStates), error: '' } }
  catch (e) { return { query: '', error: e instanceof Error ? e.message : 'Invalid filters' } }
})
const query = computed(() => parsedQuery.value.query)
const queryError = computed(() => parsedQuery.value.error)
const identity = computed(() => JSON.stringify([owner.value, releaseScope(props.filters.ships_in), query.value, mode.value]))
const session = useSession()
const moves = usePlanningMoves({ root, identity: () => identity.value, releases: () => [...overview.value?.active ?? [], ...overview.value?.released.items ?? []], items: () => Object.values(work).flatMap(s => s.items), scroll: () => props.scrollRoot, agent: () => session.identity?.principal.kind === 'agent', allowed: () => can('releases.write', props.projectId), stale: () => stale.value, actions: inject(DELIVERY_ACTIONS, undefined) })
const { draft: moveDraft, initial: moveInitial, drag: moving, feedback: moveFeedback, point: movePoint, line: moveLine, highlight: moveHighlight, refused: moveRefused } = moves
let statusAbort: AbortController | null = null, touched = false, savedIds: string[] = [], readyOwner = '', disposed = false
const prefKey = () => `releases-expanded:${props.projectId}`
async function loadMode(preserve = false) {
  statusAbort?.abort(); statusAbort = new AbortController()
  const signal = statusAbort.signal, captured = owner.value
  if (!preserve) { mode.value = 'checking'; preferenceError.value = ''; touched = false; savedIds = []; readyOwner = ''; pendingExpansion.value = false; planning.reset(null) }
  modeError.value = ''
  // Expansion preference is separate from Outline and scoped by the authenticated
  // person on the server. Capture ownership before either asynchronous read.
  const key = prefKey()
  void readPreference(key).then(value => {
    if (disposed || captured !== owner.value || signal.aborted || touched) return
    savedIds = Array.isArray(value?.ids) ? value.ids.filter((id): id is string => typeof id === 'string' && id.length <= 64).slice(0, 500) : []
    readyOwner = captured; restoreExpansion()
  })
  try {
    const response = await api(`/projects/${encodeURIComponent(props.projectId)}/delivery`, { signal })
    if (!response.ok) throw new Error(`Delivery status could not be loaded (${response.status})`)
    const status = await response.json()
    if (disposed || captured !== owner.value || signal.aborted) return
    if (status.project_id !== props.projectId || !['journey', 'releases'].includes(status.mode)) throw new Error('Invalid delivery status')
    const unchanged = mode.value === status.mode
    mode.value = status.mode
    if (preserve && unchanged && status.mode === 'releases') planning.reset({ project: props.projectId, person: props.person, scope: releaseScope(props.filters.ships_in), query: query.value })
  } catch (e) {
    if (disposed || captured !== owner.value || signal.aborted) return
    modeError.value = e instanceof Error ? e.message : 'Delivery status could not be loaded'; mode.value = 'error'
  }
}
watch(owner, () => { void loadMode() }, { immediate: true })
watch(() => props.applyVersion, () => { void loadMode(true) })
function committed(change: DeliveryCommit) { planning.committed(change) }
watch(identity, () => {
  if (mode.value !== 'releases' || !query.value) { planning.reset(null); return }
  planning.reset({ project: props.projectId, person: props.person, scope: releaseScope(props.filters.ships_in), query: query.value })
}, { immediate: true })
function restoreExpansion(force = false) {
  if (touched || readyOwner !== owner.value || stale.value || !overview.value) return
  // Opening even an empty/loading block is structural insertion. Defer it
  // before touching expanded when it would move a hovered or focused row.
  const loaded = [...overview.value.active, ...overview.value.released.items].map(row => row.release_id)
  const sources: PlanningSource[] = savedIds.filter(id => !expanded.has(id)).flatMap(id => id === 'backlog'
    ? ['backlog:ranked', 'backlog:tail'] : loaded.includes(id) ? [`release:${id}` as PlanningSource] : [])
  if (!force && sources.some(source => !canInsert(source))) { pendingExpansion.value = true; return }
  pendingExpansion.value = false
  // Retain saved IDs for later release pages, without a read for unloaded rows.
  for (const id of savedIds) expanded.add(id)
  for (const row of [...overview.value.active, ...overview.value.released.items]) if (expanded.has(row.release_id)) planning.expand(row.release_id)
  if (expanded.has('backlog')) planning.expand('backlog')
  readyOwner = ''
}
watch(overview, () => restoreExpansion())
let preferenceWrite: Promise<void> = Promise.resolve()
function saveExpansion() {
  touched = true; pendingExpansion.value = false
  const captured = owner.value, key = prefKey(), ids = [...expanded].slice(0, 500)
  preferenceWrite = preferenceWrite.catch(() => undefined).then(async () => {
    if (disposed || captured !== owner.value) return
    const ok = await writePreference(key, { ids })
    if (!disposed && captured === owner.value) preferenceError.value = ok ? '' : 'Expanded releases could not be remembered'
  })
  return preferenceWrite
}
function returnFocus(id: string) {
  const block = [...(root.value?.querySelectorAll<HTMLElement>('[data-planning-block]') ?? [])].find(el => el.dataset.planningBlock === id)
  if (block?.contains(document.activeElement)) block.querySelector<HTMLButtonElement>('.chevron')?.focus({ preventScroll: true })
}
function toggle(id: string) {
  if (expanded.has(id)) { returnFocus(id); planning.collapse(id) } else planning.expand(id)
  void saveExpansion()
}
function expandAll() { planning.expandAll(); void saveExpansion() }
function collapseAll() { for (const id of expanded) returnFocus(id); planning.collapseAll(); void saveExpansion() }
function scope(id: string) { planning.expand(id); void saveExpansion(); emit('scope', id === 'backlog' ? 'none' : id) }
function flush(force = false) { restoreExpansion(force); planning.flush(force) }
function pointer(event: PointerEvent) { hovered = event.target instanceof Element ? event.target : null; flush() }
function leave() { hovered = null; flush() }
function focusOut() { void nextTick(() => flush()) }
const nextSource = ref<PlanningSource | ''>('')
watch(continuations, sources => { if (!sources.includes(nextSource.value as PlanningSource)) nextSource.value = sources[0] ?? '' }, { immediate: true })
function sourceLabel(source: PlanningSource) {
  if (source.startsWith('backlog:')) return source === 'backlog:tail' ? 'Backlog · new' : 'Backlog · ranked'
  const row = [...overview.value?.active ?? [], ...overview.value?.released.items ?? []].find(row => `release:${row.release_id}` === source)
  return row ? releaseName(row) : 'Release work'
}
function continueWork() { flush(true); if (nextSource.value) planning.moreWork(nextSource.value) }
const backlogVisible = computed(() => !props.filters.q.trim() || Object.values(overview.value?.backlog_matches ?? {}).some(c => c.matched_count > 0))
const scoped = (id: string) => props.filters.ships_in.length === 1 && props.filters.ships_in[0] === (id === 'backlog' ? 'none' : id)
function workProps(source: PlanningSource, matches?: MatchCounts) {
  const state = work[source]
  return { source, items: state?.items ?? [], matches: state?.matches ?? matches, loaded: state?.loaded ?? false, loading: state?.loading ?? false, error: state?.error ?? '', more: !!state?.cursor,
    incomplete: state?.incomplete ?? false, projectId: props.projectId, projectKey: props.projectKey, now: props.now, query: props.filters.q, scrollRoot: props.scrollRoot, density: props.density }
}
watch([overview, loading, stale], () => emit('summary', { total: stale.value ? null : overview.value?.matches?.shown_count ?? null, loading: loading.value || mode.value === 'checking', incomplete: overview.value?.counts_incomplete ?? false }), { immediate: true })
onBeforeUnmount(() => { disposed = true; statusAbort?.abort(); planning.dispose() })
defineExpose({ committed, reload: () => mode.value === 'error' ? loadMode() : planning.reset({ project: props.projectId, person: props.person, scope: releaseScope(props.filters.ships_in), query: query.value }) })
</script>

<template>
  <section ref="root" class="release-planning" aria-label="Release planning" @pointerover="pointer" @pointerleave="leave" @focusout="focusOut" @pointerdown="moves.down" @click.capture="moves.click" @contextmenu="moves.context">
    <div class="planning-controls" role="toolbar" aria-label="Release expansion and continuation">
      <div class="expansion-controls">
        <button type="button" class="btn sm ghost" :disabled="!overview || stale" @click="expandAll"><AppIcon name="expand-all" :size="14" />Expand all</button>
        <button type="button" class="btn sm ghost" :disabled="!overview || stale" @click="collapseAll"><AppIcon name="collapse-all" :size="14" />Collapse all</button>
      </div>
      <div class="release-continuation">
        <button type="button" class="btn sm ghost" :disabled="!overview?.active_next_cursor || stale || paging.has('active')" aria-label="Load more upcoming releases" @click="planning.more('active')">More upcoming</button>
        <button type="button" class="btn sm ghost" :disabled="!overview?.released.next_cursor || stale || paging.has('released')" aria-label="Load more released releases" @click="planning.more('released')">Load more</button>
      </div>
      <div class="work-continuation">
        <select v-model="nextSource" aria-label="Work to continue loading" :disabled="!continuations.length || stale"><option v-if="!continuations.length" value="">No work waiting</option><option v-for="source in continuations" :key="source" :value="source">{{ sourceLabel(source) }}</option></select>
        <button type="button" class="btn sm ghost" :disabled="!nextSource || stale || rendered >= 2000" @click="continueWork">Load work</button>
        <button type="button" class="btn sm ghost" :disabled="!pending" @click="flush(true)">Show loaded</button>
      </div>
    </div>
    <div class="move-feedback" role="status" aria-live="polite">{{ moveFeedback }}</div>
    <div class="planning-feedback" role="status" aria-live="polite">
      <span v-if="queryError || modeError || error || pageError || preferenceError" class="error">{{ queryError || modeError || error || pageError || preferenceError }}{{ stale ? ' · showing previous data' : '' }}</span>
      <span v-else-if="pending">{{ pending }} {{ pendingExpansion ? 'updates' : 'pages' }} ready · Show loaded to insert them now{{ stale ? ' · previous query still shown' : '' }}</span>
      <span v-else-if="stale">Showing the previous query while the current read loads.</span>
      <span v-else-if="mode === 'checking' || loading">Loading releases…</span>
      <span v-else-if="budgetBlocked">{{ rendered }} work rows loaded · collapse a release to load more</span>
      <span v-else-if="queued || inFlight">{{ inFlight }} reads running · {{ queued }} pages waiting</span>
      <span v-else-if="overview?.counts_incomplete">Counts are a lower bound; more matches may exist.</span>
      <span v-else-if="overview">{{ overview.active.length }} upcoming · {{ overview.released.items.length }} released loaded</span>
      <button type="button" class="retry" :disabled="!(modeError || error || pageError)" @click="modeError ? loadMode() : planning.reset({ project: projectId, person, scope: releaseScope(filters.ships_in), query })">Retry</button>
    </div>
    <slot v-if="mode === 'journey'" name="journey"><p class="journey-placeholder">This project is still using its journey.</p></slot>
    <template v-if="overview">
      <div class="release-columns" aria-hidden="true"><span>Release</span><span>Status</span><span>Open</span><span>Progress</span><span>Outlook</span><span>Agents</span><span /></div>
      <h2 class="release-section">Upcoming</h2>
      <div v-for="release in overview.active" :key="release.release_id" :data-planning-block="release.release_id" class="release-block" :class="{ 'move-target': moveHighlight === release.release_id, 'move-refused': moveHighlight === release.release_id && moveRefused }">
        <PlanningReleaseRow :release="release" :expanded="expanded.has(release.release_id)" :scoped="scoped(release.release_id)" :disabled="stale" @toggle="toggle(release.release_id)" @scope="scope(release.release_id)" @menu="anchor => emit('menu', release.release_id, anchor)">
          <template #handle><button type="button" class="release-drag-handle" data-move-handle :aria-label="`Reorder ${releaseName(release)}`" :aria-disabled="release.visibility === 'published' || release.state === 'released'" :data-tip="release.visibility === 'published' ? 'Published releases keep their order.' : 'Reorder upcoming release'" @click.stop="moves.open({ kind: 'release', record: { ...release } })"><AppIcon name="grip" :size="14" /></button></template>
        </PlanningReleaseRow>
        <div v-if="expanded.has(release.release_id)" :id="`release-work-${release.release_id}`"><PlanningWork v-bind="workProps(`release:${release.release_id}`, release.matches)" @open="key => emit('open', key)" @copy="key => emit('copy', key)" @new-tab="key => emit('newTab', key)" @move="moves.openItem" /></div>
        <span v-else :data-insertion="`release:${release.release_id}`" class="insertion" />
      </div>
      <p v-if="!overview.active.length" class="empty">{{ filters.q ? 'No upcoming releases match' : 'No upcoming releases' }}</p>
      <span data-insertion="active" class="insertion" />
      <div v-if="backlogVisible" data-planning-block="backlog" class="release-block" :class="{ 'move-target': moveHighlight === 'backlog', 'move-refused': moveHighlight === 'backlog' && moveRefused }">
        <div class="backlog-row" :class="{ scoped: scoped('backlog') }">
          <button type="button" class="chevron" :disabled="stale" :aria-expanded="expanded.has('backlog')" aria-controls="release-work-backlog" :aria-label="`${expanded.has('backlog') ? 'Collapse' : 'Expand'} Backlog`" @click="toggle('backlog')"><AppIcon name="chevron-right" :size="14" /></button>
          <button type="button" class="backlog-name" :disabled="stale" @click="scope('backlog')"><AppIcon name="inbox" :size="14" />Backlog</button>
          <span class="backlog-count mono">{{ overview.backlog.ranked }} ranked · {{ overview.backlog.tail }} new</span>
        </div>
        <div v-if="expanded.has('backlog')" id="release-work-backlog">
          <PlanningWork v-bind="workProps('backlog:ranked', overview.backlog_matches?.ranked)" @open="key => emit('open', key)" @copy="key => emit('copy', key)" @new-tab="key => emit('newTab', key)" @move="moves.openItem" />
          <h3 class="tail-heading">New · oldest first</h3>
          <PlanningWork v-bind="workProps('backlog:tail', overview.backlog_matches?.tail)" @open="key => emit('open', key)" @copy="key => emit('copy', key)" @new-tab="key => emit('newTab', key)" @move="moves.openItem" />
        </div>
      </div>
      <div class="abandoned-row"><AppIcon name="archive" :size="14" /><span>Abandoned</span><span class="mono">{{ overview.abandoned }}{{ overview.counts_incomplete ? '+' : '' }}</span><span class="quiet">Names stay reserved</span></div>
      <h2 class="release-section">Released <span>Internal work reads Done</span></h2>
      <div v-for="release in overview.released.items" :key="release.release_id" :data-planning-block="release.release_id" class="release-block" :class="{ 'move-target': moveHighlight === release.release_id, 'move-refused': moveHighlight === release.release_id && moveRefused }">
        <PlanningReleaseRow :release="release" :expanded="expanded.has(release.release_id)" :scoped="scoped(release.release_id)" :disabled="stale" @toggle="toggle(release.release_id)" @scope="scope(release.release_id)" @menu="anchor => emit('menu', release.release_id, anchor)">
          <template #handle><button type="button" class="release-drag-handle" data-move-handle :aria-label="`Reorder ${releaseName(release)}`" :aria-disabled="release.visibility === 'published' || release.state === 'released'" :data-tip="release.visibility === 'published' ? 'Published releases keep their order.' : 'Reorder upcoming release'" @click.stop="moves.open({ kind: 'release', record: { ...release } })"><AppIcon name="grip" :size="14" /></button></template>
        </PlanningReleaseRow>
        <div v-if="expanded.has(release.release_id)" :id="`release-work-${release.release_id}`"><PlanningWork v-bind="workProps(`release:${release.release_id}`, release.matches)" @open="key => emit('open', key)" @copy="key => emit('copy', key)" @new-tab="key => emit('newTab', key)" @move="moves.openItem" /></div>
        <span v-else :data-insertion="`release:${release.release_id}`" class="insertion" />
      </div>
      <span data-insertion="released" class="insertion" />
      <p class="empty">{{ overview.released.next_cursor ? 'More released releases are available with Load more above.' : overview.counts_incomplete ? 'Loaded release pages exhausted; counts remain incomplete.' : filters.q ? 'End of the loaded matching release pages' : 'All released releases loaded' }}</p>
    </template>
    <Teleport to="body">
      <div v-if="moving" class="move-ghost" aria-hidden="true" :style="{ left: `${Math.min(movePoint.x + 12, Math.max(8, (root?.clientWidth ?? 320) - 220))}px`, top: `${Math.max(144, movePoint.y - 16)}px` }"><span>{{ moving.record.title }}</span><p>{{ moveFeedback }}</p></div>
      <div v-if="moveLine" class="move-line" :style="{ left: `${moveLine.x}px`, top: `${moveLine.y}px`, width: `${moveLine.width}px` }" />
      <DeliveryMoveSheet v-if="moveDraft" :subject="moveDraft" :initial="moveInitial" :person="person" :releases="overview?.active ?? []" :visible-items="Object.values(work).flatMap(s => s.items)" :advice="moves.advice" @close="moveDraft = null" @confirm="moves.confirm" />
    </Teleport>
  </section>
</template>
<style scoped>
.move-feedback { height: 44px; display: flex; align-items: center; font-size: 12px; line-height: 18px; color: var(--ink-2); overflow: auto; }
.move-target > :first-child { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--line-2); }
.move-refused > :first-child { background: var(--danger-bg); }
.release-drag-handle { display: grid; place-items: center; width: 28px; height: 44px; padding: 0; background: transparent; border: 0; color: var(--ink-3); cursor: grab; touch-action: none; }
.release-drag-handle[aria-disabled="true"] { cursor: not-allowed; }
@media(hover:hover) { .release-drag-handle { opacity: 0; } .release-block:hover .release-drag-handle, .release-block:focus-within .release-drag-handle { opacity: 1; } }
@media(pointer:coarse) { .release-drag-handle { width: 44px; touch-action: pan-y; } }
.move-ghost { position: fixed; transform: translateY(-100%); z-index: 1000; pointer-events: none; width: min(220px,calc(100vw - 32px)); padding: 10px; border: 1px solid var(--line); border-radius: 8px; color: var(--ink); background: var(--surface); box-shadow: var(--shadow-pop); font-size: 12px; }
.move-ghost span { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.move-ghost p { margin: 6px 0 0; font-size: 11px; line-height: 1.4; }
.move-line { position: fixed; height: 2px; background: var(--ink-2); z-index: 999; pointer-events: none; }
.release-planning { container: releases / inline-size; min-width: 0; }
.planning-controls { display: grid; grid-template-columns: auto 1fr; align-items: center; gap: 8px; padding: 8px 0; }
.expansion-controls, .release-continuation, .work-continuation { display: flex; align-items: center; gap: 6px; min-width: 0; }
.release-continuation { justify-content: flex-end; }
.work-continuation { grid-column: 1 / -1; }
.work-continuation select { min-width: 0; flex: 1; max-width: 32rem; height: 36px; color: var(--ink-2); background: transparent; border: 1px solid var(--line); border-radius: 6px; padding: 0 8px; }
.planning-feedback { display: flex; align-items: center; gap: 8px; height: 44px; font-size: 12px; color: var(--ink-3); }
.planning-feedback > span { min-width: 0; flex: 1; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.error { color: var(--danger); }
.retry { padding: 0 8px; height: 36px; border: 0; background: transparent; color: var(--teal-ink); flex-shrink: 0; }
.release-columns { display: grid; grid-template-columns: minmax(0, 1fr) 7rem 4.5rem 8rem 8rem 10rem 44px; gap: 8px; padding: 10px 8px 10px 44px; border-bottom: 1px solid var(--line); font: 10.5px/1 var(--mono); color: var(--ink-3); text-transform: uppercase; }
.release-section { display: flex; justify-content: space-between; gap: 8px; margin: 0; padding: 12px; font: 600 11px/1.4 var(--mono); text-transform: uppercase; letter-spacing: .1em; color: var(--ink-3); border-bottom: 1px solid var(--line); }
.release-section span { font-weight: 400; font-size: 10px; }
.backlog-row { display: flex; align-items: center; min-height: 64px; gap: 8px; padding: 0 12px; border-bottom: 1px solid var(--line); }
.backlog-row.scoped { background: var(--row-selected); }
.chevron { display: grid; place-items: center; min-width: 32px; height: 44px; border: 0; background: transparent; color: var(--ink-2); border-radius: 6px; }
.chevron[aria-expanded="true"] svg { transform: rotate(90deg); }
.backlog-name { display: flex; align-items: center; gap: 8px; height: 44px; padding: 0 4px; border: 0; background: transparent; color: var(--ink); font-weight: 650; }
.backlog-count { color: var(--ink-3); font-size: 11px; }
button:hover:not(:disabled) { background: var(--row-hover); }
button:focus-visible, select:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.abandoned-row { display: flex; align-items: center; min-height: 48px; gap: 10px; padding: 0 16px; border-bottom: 1px solid var(--line); font-size: 13px; }
.quiet { color: var(--ink-3); font-size: 12px; }
.tail-heading { margin: 0; padding: 10px 44px; border-bottom: 1px solid var(--line); font: 10.5px/1.4 var(--mono); text-transform: uppercase; color: var(--ink-3); }
.insertion { display: block; height: 0; }
.empty, .journey-placeholder { padding: 12px; font-size: 13px; color: var(--ink-3); }
@container releases (max-width: 1100px) { .release-columns { grid-template-columns: minmax(0, 1fr) 6rem 4rem 6.5rem 6rem 7rem 44px; gap: 6px; } }
@container releases (max-width: 760px) { .release-columns { display: none; } }
@container releases (max-width: 450px) {
  .planning-controls { grid-template-columns: minmax(0, 1fr); }
  .release-continuation { justify-content: flex-start; }
  .work-continuation { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; }
  .work-continuation select { width: 100%; }
  .abandoned-row { flex-wrap: wrap; }
}
@media (pointer: coarse) { .planning-controls .btn, .retry, .work-continuation select { min-height: 44px; } .chevron { min-width: 44px; } }
</style>
