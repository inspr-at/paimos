// SPDX-License-Identifier: AGPL-3.0-only
import type { DeliveryCommit } from './deliveryChanges'
import { computed, reactive, ref, shallowRef } from 'vue'
import { READ_CONCURRENCY, RELEASE_PAGE_SIZE, WORK_PAGE_SIZE, WORK_RENDER_LIMIT, readPlanning, type ItemPage, type MatchCounts, type PlanningAnswer, type PlanningContext, type PlanningOverview, type PlanningRead, type PlanningRelease, type PlanningSource, type ReleasePage } from './deliveryPlanning.ts'

interface WorkState { items: ItemPage['items']; cursor: string; matches?: MatchCounts; count: number; incomplete: boolean; loaded: boolean; loading: boolean; error: string }
interface Job { source: PlanningSource; cursor: string; generation: number; token: number; context: PlanningContext; abort: AbortController; limit: number; publish?: () => void }
const compare = (a: string, b: string) => a < b ? -1 : a > b ? 1 : 0
const isWork = (source: PlanningSource) => source.startsWith('release:') || source.startsWith('backlog:')
const ownerOf = (source: PlanningSource) => source.startsWith('backlog:') ? 'backlog' : source.slice(8)
const message = (e: unknown) => e instanceof Error ? e.message : 'Release read failed'
const emptyWork = (): WorkState => ({ items: [], cursor: '', count: 0, incomplete: false, loaded: false, loading: false, error: '' })

// A single bounded queue owns first pages, continuations and held insertions.
// A generation captures the complete project/person/scope/query identity; a
// per-source token also invalidates collapse/reopen races inside that generation.
export function useReleasePlanning(options: { read?: PlanningRead; canInsert?: (source: PlanningSource) => boolean } = {}) {
  const read = options.read ?? readPlanning
  const overview = shallowRef<PlanningOverview | null>(null)
  const expanded = reactive(new Set<string>())
  const work = reactive<Record<string, WorkState>>({})
  const loading = ref(false), stale = ref(false), error = ref(''), pageError = ref('')
  const pending = ref(0), inFlight = ref(0), queued = ref(0), budgetBlocked = ref(false)
  const paging = reactive(new Set<PlanningSource>())
  const queue: Job[] = [], running = new Set<Job>(), held = new Set<Job>()
  const ownRows = new Map<string, ItemPage['items'][number]>()
  const tokens = new Map<PlanningSource, number>(), seen = new Map<PlanningSource, Set<string>>()
  let context: PlanningContext | null = null, generation = 0, disposed = false
  const rendered = computed(() => Object.values(work).reduce((n, state) => n + state.items.length, 0))
  const reservation = () => [...running, ...held].reduce((n, job) => n + (isWork(job.source) && current(job) ? job.limit : 0), 0)
  const current = (job: Job) => !disposed && job.generation === generation && job.token === (tokens.get(job.source) ?? 0) && !job.abort.signal.aborted
  const sync = () => { pending.value = held.size; inFlight.value = running.size; queued.value = queue.length }
  const stateOf = (source: PlanningSource) => work[source] ?? (work[source] = emptyWork())
  const rows = () => [...(overview.value?.active ?? []), ...(overview.value?.released.items ?? [])]
  const countsFor = (source: PlanningSource) => source.startsWith('backlog:') ? overview.value?.backlog_matches?.[source.slice(8)] : rows().find(r => r.release_id === ownerOf(source))?.matches
  function enqueue(source: PlanningSource, cursor = '') {
    if (!context || disposed || (source !== 'overview' && stale.value)) return
    if ([...queue, ...running, ...held].some(job => current(job) && job.source === source)) return
    if (isWork(source)) {
      if (!expanded.has(ownerOf(source))) return
      const state = stateOf(source), counts = countsFor(source)
      if (!cursor && state.loaded) return
      if (!cursor && counts?.shown_count === 0 && !counts.incomplete) {
        Object.assign(state, { loaded: true, matches: counts, count: 0 }); return
      }
      state.loading = true; state.error = ''
    } else if (source !== 'overview') paging.add(source)
    queue.push({ source, cursor, generation, token: tokens.get(source) ?? 0, context: { ...context }, abort: new AbortController(), limit: 0 })
    pump()
  }
  function openWork(id: string) {
    if (id !== 'backlog' && !rows().some(row => row.release_id === id)) return
    if (id === 'backlog') { enqueue('backlog:ranked'); enqueue('backlog:tail') }
    else enqueue(`release:${id}`)
  }
  function searchExpand(releases: PlanningRelease[]) {
    if (!context || !new URLSearchParams(context.query).get('q')) return
    for (const row of releases) if ((row.matches?.matched_count ?? 0) > 0) expanded.add(row.release_id)
    if (Object.values(overview.value?.backlog_matches ?? {}).some(c => c.matched_count > 0)) expanded.add('backlog')
  }
  function publish(job: Job, answer: PlanningAnswer) {
    if (!current(job)) return
    if (job.source === 'overview') {
      const next = answer as PlanningOverview
      overview.value = next
      for (const key of Object.keys(work)) delete work[key]
      stale.value = false; loading.value = false
      searchExpand([...next.active, ...next.released.items])
      for (const id of expanded) if (id === 'backlog' || rows().some(r => r.release_id === id)) openWork(id)
    } else if (isWork(job.source)) {
      const page = answer as ItemPage, state = stateOf(job.source)
      const ids = new Set(state.items.map(i => i.item_id))
      state.items.push(...page.items.filter(i => !ids.has(i.item_id)))
      Object.assign(state, { cursor: page.next_cursor ?? '', matches: page.matches, count: page.count, incomplete: page.incomplete, loaded: true, loading: false })
    } else {
      const page = answer as ReleasePage, snapshot = overview.value!
      const previous = job.source === 'active' ? snapshot.active : snapshot.released.items
      const ids = new Set(previous.map(r => r.release_id)), additions = page.items.filter(r => !ids.has(r.release_id))
      overview.value = job.source === 'active' ? { ...snapshot, active: [...previous, ...additions], active_next_cursor: page.next_cursor }
        : { ...snapshot, released: { ...page, items: [...previous, ...additions] } }
      paging.delete(job.source)
      // Ordinary continuation starts collapsed. Search hits and saved expansion
      // remain reachable without fetching all historical release members.
      searchExpand(additions)
      for (const row of additions) if (expanded.has(row.release_id)) openWork(row.release_id)
    }
    let cursors = seen.get(job.source)
    if (!cursors) { cursors = new Set(); seen.set(job.source, cursors) }
    cursors.add(job.cursor)
  }
  function pump() {
    budgetBlocked.value = false
    for (let index = 0; index < queue.length && running.size < READ_CONCURRENCY;) {
      const job = queue[index]!
      if (!current(job)) { queue.splice(index, 1); continue }
      const remaining = WORK_RENDER_LIMIT - rendered.value - reservation()
      if (isWork(job.source) && remaining <= 0) { budgetBlocked.value = true; index++; continue }
      job.limit = isWork(job.source) ? Math.min(WORK_PAGE_SIZE, remaining) : RELEASE_PAGE_SIZE
      queue.splice(index, 1); running.add(job)
      void run(job)
    }
    sync()
  }
  async function run(job: Job) {
    try {
      const answer = await read(job.context, job.source, job.cursor, job.limit, job.abort.signal)
      if (!current(job)) return
      const next = job.source === 'overview' ? undefined : (answer as ItemPage).next_cursor
      if (next && (next === job.cursor || seen.get(job.source)?.has(next))) throw new Error('The server returned a repeated page cursor')
      if (isWork(job.source) && (answer as ItemPage).items.length > job.limit) throw new Error('The server exceeded the work page limit')
      if (options.canInsert?.(job.source) === false) { job.publish = () => publish(job, answer); held.add(job) }
      else publish(job, answer)
    } catch (e) {
      if (!current(job)) return
      if (job.source === 'overview') { error.value = message(e); loading.value = false }
      else if (isWork(job.source)) { const state = stateOf(job.source); state.error = message(e); state.loading = false }
      else { pageError.value = message(e); paging.delete(job.source) }
    } finally { running.delete(job); pump() }
  }
  function flush(force = false) {
    for (const job of [...held]) {
      if (!current(job) || force || options.canInsert?.(job.source) !== false) {
        held.delete(job)
        if (current(job)) job.publish?.()
      }
    }
    pump()
  }
  function invalidate() {
    generation++
    for (const job of running) job.abort.abort()
    for (const job of held) job.abort.abort()
    held.clear(); queue.length = 0; tokens.clear(); seen.clear(); paging.clear()
    for (const state of Object.values(work)) state.loading = false
    sync()
  }
  function reset(next: PlanningContext | null) {
    const sameOwner = context?.project === next?.project && context?.person === next?.person
    invalidate(); ownRows.clear(); context = next; error.value = ''; pageError.value = ''; budgetBlocked.value = false
    if (!sameOwner) { overview.value = null; expanded.clear(); for (const key of Object.keys(work)) delete work[key] }
    stale.value = !!overview.value; loading.value = !!next
    if (next) enqueue('overview')
  }
  function collapse(id: string) {
    expanded.delete(id)
    const sources: PlanningSource[] = id === 'backlog' ? ['backlog:ranked', 'backlog:tail'] : [`release:${id}`]
    for (const source of sources) {
      tokens.set(source, (tokens.get(source) ?? 0) + 1)
      for (const job of running) if (job.source === source) job.abort.abort()
      for (const job of [...held]) if (job.source === source) { job.abort.abort(); held.delete(job) }
      for (let i = queue.length - 1; i >= 0; i--) if (queue[i]!.source === source) queue.splice(i, 1)
      seen.delete(source)
      delete work[source]
    }
    pump()
  }
  function expand(id: string) { expanded.add(id); openWork(id) }
  function expandAll() { for (const row of rows()) expanded.add(row.release_id); expanded.add('backlog'); for (const id of expanded) openWork(id) }
  function collapseAll() { for (const id of [...expanded]) collapse(id) }
  function more(source: 'active' | 'released') {
    const cursor = source === 'active' ? overview.value?.active_next_cursor : overview.value?.released.next_cursor
    if (cursor) { pageError.value = ''; enqueue(source, cursor) }
  }
  const continuations = computed(() => Object.entries(work).filter(([, state]) => state.error || state.cursor || !state.loaded).map(([source]) => source as PlanningSource))
  function moreWork(source: PlanningSource) { const state = stateOf(source); enqueue(source, state.loaded ? state.cursor : '') }
  function committed(change: DeliveryCommit) {
    if (!overview.value || !context) return
    // Lifecycle owners apply their authoritative result separately; a blanket
    // refresh here would also expose still-held foreign structural changes.
    if (change.kind === 'lifecycle') {
      const result = change.result
      if (!result || result.project_id !== context.project) return
      invalidate(); loading.value = false
      const before = rows().find(row => row.release_id === result.release_id)
      const updated = { ...before, ...result }
      const active = overview.value.active.filter(row => row.release_id !== result.release_id)
      const released = overview.value.released.items.filter(row => row.release_id !== result.release_id)
      if (['planned','building','frozen'].includes(updated.state)) active.push(updated)
      else if (updated.state === 'released') released.unshift(updated)
      active.sort((a, b) => compare(a.rank, b.rank) || compare(a.release_id, b.release_id))
      // The transition row is authoritative. Rollover also changes other
      // containers; keep their held snapshot and disclose that a refresh is
      // needed instead of inventing their new counts or reading foreign edits.
      const rollover = updated.state === 'abandoned' || updated.state === 'released' || !before?.version && !!updated.version
      if (rollover) { delete work[`release:${updated.release_id}`]; overview.value.counts_incomplete = true }
      overview.value = { ...overview.value, active, released: { ...overview.value.released, items: released }, abandoned: overview.value.abandoned + Number(updated.state === 'abandoned' && before?.state !== 'abandoned') }
      return
    }
    // Cancel pre-commit reads without replacing the visible snapshot: a late
    // continuation must never overwrite or reinsert the old placement.
    invalidate()
    loading.value = false
    if (change.kind === 'rank') {
      const result = change.result
      const at = overview.value.active.findIndex(row => row.release_id === result.release_id)
      if (at < 0 || result.project_id !== context.project) return
      const active = overview.value.active.map(row => row.release_id === result.release_id ? { ...row, ...result } : row)
      active.sort((a, b) => compare(a.rank, b.rank) || compare(a.release_id, b.release_id))
      overview.value = { ...overview.value, active }; return
    }
    for (const placement of change.result.items) {
      if (placement.project_id !== context.project) continue
      const known = Object.values(work).flatMap(state => state.items).find(it => it.item_id === placement.item_id) ?? ownRows.get(placement.item_id)
      if (!known) continue
      if (ownRows.size >= 101 && !ownRows.has(placement.item_id)) ownRows.delete(ownRows.keys().next().value!)
      // Go omits empty placement fields. Their absence clears the old value,
      // including when Undo returns a row to the unranked Backlog tail.
      const updated = { ...known, ...placement, release_id: placement.release_id, rank: placement.rank }
      ownRows.set(placement.item_id, updated)
      const source: PlanningSource = known.release_id ? `release:${known.release_id}` : known.rank ? 'backlog:ranked' : 'backlog:tail'
      const destination: PlanningSource = placement.release_id ? `release:${placement.release_id}` : placement.rank ? 'backlog:ranked' : 'backlog:tail'
      for (const state of Object.values(work)) state.items = state.items.filter(it => it.item_id !== placement.item_id)
      const target = work[destination]
      if (target?.loaded && rendered.value < WORK_RENDER_LIMIT) {
        target.items.push(updated)
        target.items.sort((a, b) => compare(destination === 'backlog:tail' ? a.created_at : a.rank ?? '', destination === 'backlog:tail' ? b.created_at : b.rank ?? '') || compare(a.item_id, b.item_id))
      }
      if (source !== destination) {
        for (const [key, delta] of [[source, -1], [destination, 1]] as const) {
          const state = work[key]
          if (state) { state.count = Math.max(0, state.count + delta); if (state.matches) state.matches = { ...state.matches, matched_count: Math.max(0, state.matches.matched_count + delta), shown_count: Math.max(0, state.matches.shown_count + delta) } }
          const row = rows().find(r => `release:${r.release_id}` === key)
          if (row) {
            if (row.matches) row.matches = { ...row.matches, matched_count: Math.max(0, row.matches.matched_count + delta), shown_count: Math.max(0, row.matches.shown_count + delta) }
            const completed = ['done', 'accepted', 'delivered'].includes(known.state), canceled = ['cancelled', 'canceled'].includes(known.state)
            if (['ticket','task'].includes(known.kind) && !canceled) row.rollup = { units: Math.max(0, row.rollup.units + delta), completed: Math.max(0, row.rollup.completed + (completed ? delta : 0)), open_hours: Math.max(0, row.rollup.open_hours + (completed ? 0 : (known.estimated_hours ?? 0) * delta)) }
          } else if (key.startsWith('backlog:')) {
            const part = key.slice(8) as 'ranked' | 'tail'
            overview.value.backlog[part] = Math.max(0, overview.value.backlog[part] + delta)
            const matches = overview.value.backlog_matches?.[part]
            if (matches) overview.value.backlog_matches![part] = { ...matches, matched_count: Math.max(0, matches.matched_count + delta), shown_count: Math.max(0, matches.shown_count + delta) }
          }
        }
      }
    }
    for (const row of rows()) {
      const revision = change.result.release_revisions?.[row.release_id]
      if (revision) row.revision = revision
    }
    overview.value = { ...overview.value }
  }
  function dispose() { disposed = true; invalidate(); ownRows.clear(); context = null }
  return { overview, expanded, work, loading, stale, error, pageError, pending, inFlight, queued, budgetBlocked, paging, rendered, continuations,
    committed, reset, expand, collapse, expandAll, collapseAll, more, moreWork, flush, dispose }
}
