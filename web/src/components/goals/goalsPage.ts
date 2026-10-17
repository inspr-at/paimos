// SPDX-License-Identifier: AGPL-3.0-only
// The Goals view's state (AEON-1457): what the toolbar and the list share. The list
// is read from the server with its filters (search, state, Hide done) and sort; the
// state chips count items over every visible goal and add up, so Hide done and the
// search change the list, never the counts. Each read is bound to the person and
// the query it was asked for: an answer for another person, an older query or an
// older turn never reaches the screen. Progress series and creation times are read
// per goal as rows need them, a few at a time.
import { computed, onScopeDispose, reactive, ref, shallowRef, watch, type Ref } from 'vue'
import { useSession } from '../../stores/session'
import { scopeOwner } from '../../lib/identityScope'
import { usePreference } from '../../lib/preferences'
import { usePoller } from '../../lib/usePolledData'
import { goalCreatedAt, goalMatches, goalSeries, itemMatches, listGoals, SORT_DIR, type Goal, type GoalCounts, type GoalList, type GoalSeries, type GoalSort, type StateFilter } from '../../lib/goals'
import { isGoalRange, type GoalRange } from '../../lib/goalRate'

const LIST_POLL_MS = 60_000
const SEARCH_SETTLE_MS = 250
// A series is read again once the goal's progress changed, or after this long.
const SERIES_FRESH_MS = 5 * 60_000
const SERIES_PARALLEL = 3

type GoalsPrefs = { range?: string; hideDone?: boolean }
export interface SeriesEntry { state: 'reading' | 'ready' | 'failed' | 'too_large'; series?: GoalSeries; createdAt: number | null; at: number; sign: string }

// shown: the project groups the person hides take their goals with them.
export function useGoalsPage(options: { term: Ref<string>; active: () => boolean; shown?: (goal: Goal) => boolean }) {
  const session = useSession()
  const owner = computed(() => scopeOwner(session.identity))
  const prefs = usePreference<GoalsPrefs>('goals')

  // ---------- Toolbar state ----------
  const filter = ref<StateFilter>('all')
  const sort = ref<GoalSort>('eta')
  const dir = ref<'asc' | 'desc'>('asc')
  // Hide done is on until the person turns it off; the choice is theirs on every device.
  const hideDone = computed(() => prefs.value.value?.hideDone ?? true)
  function setHideDone(value: boolean) { void prefs.save({ ...(prefs.value.value ?? {}), hideDone: value }, 0) }
  // One range for every chart, the one used last (the ViewSwitch rule).
  const range = computed<GoalRange>(() => isGoalRange(prefs.value.value?.range) ? prefs.value.value!.range as GoalRange : '24h')
  function setRange(value: GoalRange) { if (value !== range.value) void prefs.save({ ...(prefs.value.value ?? {}), range: value }, 0) }
  function setSort(key: GoalSort, direction?: 'asc' | 'desc') {
    if (direction) { sort.value = key; dir.value = direction; return }
    if (sort.value === key) dir.value = dir.value === 'asc' ? 'desc' : 'asc'
    else { sort.value = key; dir.value = SORT_DIR[key] }
  }

  // ---------- The list ----------
  const list = shallowRef<GoalList | null>(null)
  const error = ref('')
  const reading = ref(false)
  const loadingMore = ref(false)
  let turn = 0, shownQuery = ''
  let controller: AbortController | undefined
  const query = () => ({ q: options.term.value, state: filter.value === 'all' ? null : filter.value, hideDone: hideDone.value, sort: sort.value, dir: dir.value })
  function read(): Promise<void> {
    if (!owner.value || !options.active()) return Promise.resolve()
    controller?.abort()
    const abort = new AbortController(); controller = abort
    const mine = ++turn, who = owner.value, asked = JSON.stringify(query())
    reading.value = true
    // The answer counts only for the person, query and turn it was asked for.
    const current = () => mine === turn && who === owner.value
    return listGoals(query(), abort.signal).then(answer => {
      if (!current()) return
      reading.value = false
      // The same question never goes back to an older snapshot than the one on screen.
      const shown = list.value
      if (shown && asked === shownQuery && (answer.read_through < shown.read_through || (answer.read_through === shown.read_through && answer.generation < shown.generation))) return
      list.value = answer; shownQuery = asked; error.value = ''
    }, cause => {
      if (!current() || abort.signal.aborted) return
      error.value = cause instanceof Error ? cause.message : 'Goals could not be read.'
      reading.value = false
    })
  }
  // The next page joins the list; a newer read of the first page replaces it instead.
  function loadMore(): Promise<void> {
    const page = list.value
    if (!page?.next_cursor || loadingMore.value) return Promise.resolve()
    const mine = turn, who = owner.value
    loadingMore.value = true
    return listGoals({ ...query(), cursor: page.next_cursor }).then(answer => {
      if (mine !== turn || who !== owner.value || list.value !== page) return
      const known = new Set(page.items.map(goal => goal.id))
      list.value = { ...answer, items: [...page.items, ...answer.items.filter(goal => !known.has(goal.id))] }
    }, cause => {
      if (mine === turn && who === owner.value) error.value = cause instanceof Error ? cause.message : 'More goals could not be read.'
    }).finally(() => { if (mine === turn) loadingMore.value = false })
  }
  let settle: ReturnType<typeof setTimeout> | undefined
  watch(options.term, () => { clearTimeout(settle); settle = setTimeout(() => { void read() }, SEARCH_SETTLE_MS) })
  watch([filter, sort, dir, hideDone], () => { clearTimeout(settle); void read() })
  const poller = usePoller(read, LIST_POLL_MS, { enabled: () => options.active() && !!owner.value })
  watch(() => options.active() && !!owner.value, on => { if (on) poller.start(true); else { poller.stop(); controller?.abort() } }, { immediate: true })

  // ---------- Opening goals and charts ----------
  const open = reactive(new Set<string>())
  const charts = reactive(new Set<string>())
  // "N more queued · show" and "N done · show", per goal.
  const folds = reactive(new Map<string, { queued?: boolean; done?: boolean }>())
  const goals = computed<Goal[]>(() => (list.value?.items ?? []).filter(goal => options.shown?.(goal) ?? true))
  const hiddenByGroups = computed(() => (list.value?.items.length ?? 0) > 0 && goals.value.length === 0)
  const term = computed(() => options.term.value.trim())
  // A search that finds a goal's items, not the goal, opens it to show them.
  const searchOpens = (goal: Goal) => !!term.value && !goalMatches(goal, term.value) && (goal.items ?? []).some(item => itemMatches(item, term.value))
  const isOpen = (goal: Goal) => open.has(goal.id) || searchOpens(goal)
  const allOpen = computed(() => goals.value.length > 0 && goals.value.every(isOpen))
  function toggle(id: string, on?: boolean) {
    const next = on ?? !open.has(id)
    if (next) open.add(id); else open.delete(id)
  }
  function toggleAll(visible: readonly Goal[] = goals.value) {
    const close = visible.length > 0 && visible.every(isOpen)
    for (const goal of visible) toggle(goal.id, !close)
  }
  function toggleFold(id: string, which: 'queued' | 'done') {
    const now = folds.get(id) ?? {}
    folds.set(id, { ...now, [which]: !now[which] })
  }
  function toggleChart(id: string) { if (charts.has(id)) charts.delete(id); else charts.add(id) }

  // ---------- Series ----------
  const series = reactive(new Map<string, SeriesEntry>())
  const queue: { goal: Goal; key: string; sign: string }[] = []
  let running = 0
  const signOf = (goal: Goal) => `${goal.progress?.pct ?? ''}:${goal.states ? Object.values(goal.states).join('.') : ''}:${goal.done ? 1 : 0}`
  const seriesKey = (id: string, value: GoalRange) => `${id}:${value}`
  function seriesFor(goal: Goal, value: GoalRange = range.value): SeriesEntry | undefined { return series.get(seriesKey(goal.id, value)) }
  // Rows ask for their series; one is read again when the goal moved or it is old.
  function want(goal: Goal, value: GoalRange = range.value) {
    if (goal.expansion !== 'full') return
    const key = seriesKey(goal.id, value), sign = signOf(goal), have = series.get(key)
    if (have && (have.state === 'reading' || (have.sign === sign && Date.now() - have.at < SERIES_FRESH_MS))) return
    if (queue.some(entry => entry.key === key)) return
    series.set(key, { state: 'reading', createdAt: have?.createdAt ?? null, series: have?.series, at: Date.now(), sign })
    queue.push({ goal, key, sign })
    pump()
  }
  function pump() {
    while (running < SERIES_PARALLEL && queue.length) {
      const { goal, key, sign } = queue.shift()!
      const value = key.slice(goal.id.length + 1) as GoalRange, who = owner.value, epoch = seriesEpoch
      running++
      void Promise.all([goalSeries(goal.id, value), goalCreatedAt(goal.id)]).then(([answer, createdAt]) => {
        if (who !== owner.value || epoch !== seriesEpoch) return
        series.set(key, { state: answer.expansion === 'too_large' ? 'too_large' : 'ready', series: answer, createdAt, at: Date.now(), sign })
      }, () => {
        if (who !== owner.value || epoch !== seriesEpoch) return
        series.set(key, { state: 'failed', createdAt: null, at: Date.now(), sign })
      }).finally(() => { running--; if (epoch === seriesEpoch) pump() })
    }
  }
  let seriesEpoch = 0

  // ---------- Counts ----------
  const totals = computed<GoalCounts>(() => list.value?.totals ?? { moving: 0, queued: 0, paused: 0, done: 0 })
  const counts = computed<Record<StateFilter, number>>(() => ({ all: totals.value.moving + totals.value.queued + totals.value.paused + totals.value.done, ...totals.value }))
  const loaded = computed(() => list.value !== null)

  // Another person or workspace starts from nothing: no list, no open goals, no series.
  watch(owner, () => {
    turn++; controller?.abort(); seriesEpoch++; queue.length = 0
    list.value = null; error.value = ''; reading.value = false; loadingMore.value = false
    open.clear(); charts.clear(); folds.clear(); series.clear(); filter.value = 'all'
    if (options.active()) void read()
  }, { flush: 'sync' })
  function stop() { poller.stop(); controller?.abort(); clearTimeout(settle); seriesEpoch++; queue.length = 0 }
  // Leaving the page stops the reads; its watchers go with the page's scope.
  onScopeDispose(stop)

  return {
    filter, sort, dir, hideDone, setHideDone, range, setRange, setSort, list, goals, hiddenByGroups, error, reading, loaded, loadingMore, loadMore, read,
    open, charts, folds, isOpen, allOpen, toggle, toggleAll, toggleFold, toggleChart, searchOpens, want, seriesFor, counts, totals, stop,
  }
}
export type GoalsPage = ReturnType<typeof useGoalsPage>
