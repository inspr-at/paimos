// SPDX-License-Identifier: AGPL-3.0-only
// The Goals view's state (AEON-1457): what the toolbar and the list share. The list
// is read from the server with its filters (search, state, Hide done) and sort; the
// state chips count items over every visible goal and add up, so Hide done and the
// search change the list, never the counts. Each read is bound to the person and
// the query it was asked for: an answer for another person, an older query or an
// older turn never reaches the screen. Progress series and creation times are read
// per goal as rows need them, a few at a time. The event stream (AEON-1494) says
// when goals, items or the workspace status note changed: the list is read again
// at once, and its state is the footer's freshness stamp (Live or Reconnecting).
// AEON-1395: Group › Goals or Epics, for callers that offer it (groups). The person's
// choice is kept with their Goals preferences; a link (?group=epics) wins until they
// choose and changes nothing. Their first read waits for those preferences, so a
// remembered group never shows the other list first. A switch replaces the rows;
// the frame (status note, header) stays, and a read for the other group never lands.
// AEON-1614: Hide for me. A person hides a goal from their own view (the server keeps
// it); Display › Show has Done goals (H), Hidden goals ({n}) and Waiting on you, all
// saved with the Goals preferences; the toolbar's Waiting on you toggle is the same
// setting. Hidden goals count in the state chips as hidden done goals do. A hide or
// Undo is bound to the person and goal it was asked for.
import { computed, onScopeDispose, reactive, ref, shallowRef, watch, type Ref } from 'vue'
import { useSession } from '../../stores/session'
import { scopeOwner } from '../../lib/identityScope'
import { usePreference } from '../../lib/preferences'
import { usePoller } from '../../lib/usePolledData'
import { toast } from '../../lib/toast'
import { goalCreatedAt, goalMatches, goalSeries, hideCopy, hideGoal, isGoalGroup, itemMatches, listGoals, unhideGoal, SORT_DIR, type Goal, type GoalCounts, type GoalGroup, type GoalList, type GoalSeries, type GoalSort, type StateFilter } from '../../lib/goals'
import { isGoalRange, type GoalRange } from '../../lib/goalRate'
import { EPIC_STREAM_EVENTS, GOAL_STREAM_EVENTS, openGoalsLive, type GoalsLiveState } from '../../lib/goalsLive'

const LIST_POLL_MS = 60_000
const SEARCH_SETTLE_MS = 250
// A series is read again once the goal's progress changed, or after this long.
const SERIES_FRESH_MS = 5 * 60_000
const SERIES_PARALLEL = 3
// Changes arrive in bursts (one write appends several events): at most one read per
// window, and never more than one in flight, so a busy stream cannot starve the list.
const LIVE_SETTLE_MS = 400

type GoalsPrefs = { range?: string; hideDone?: boolean; group?: string; showHidden?: boolean; showWaiting?: boolean }
export interface SeriesEntry { state: 'reading' | 'ready' | 'failed' | 'too_large'; series?: GoalSeries; createdAt: number | null; at: number; sign: string }

// shown: the project groups the person hides take their goals with them.
// groups: the caller offers Group › Goals · Epics; link is the group a link asks for (?group=).
export function useGoalsPage(options: { term: Ref<string>; active: () => boolean; shown?: (goal: Goal) => boolean; groups?: { link?: () => unknown } }) {
  const session = useSession()
  const owner = computed(() => scopeOwner(session.identity))
  const prefs = usePreference<GoalsPrefs>('goals')
  // With groups, the person's preferences for this owner were read (or failed to) before the first read.
  const settledFor = ref('')
  watch(owner, who => {
    settledFor.value = ''
    if (who) void prefs.ready.then(() => { if (owner.value === who) settledFor.value = who })
  }, { immediate: true })
  // The person's Goals preferences are known: the Waiting on you band is decided before it paints.
  const prefsSettled = computed(() => !!owner.value && settledFor.value === owner.value)
  const settled = computed(() => !options.groups || prefsSettled.value)

  // ---------- Group: Goals or Epics (AEON-1395) ----------
  const linkedGroup = computed<GoalGroup | null>(() => { const value = options.groups?.link?.(); return isGoalGroup(value) ? value : null })
  const chosenGroup = ref<GoalGroup | null>(null)
  watch(linkedGroup, value => { if (value) chosenGroup.value = null })
  const storedGroup = computed<GoalGroup>(() => isGoalGroup(prefs.value.value?.group) ? prefs.value.value!.group as GoalGroup : 'goals')
  const group = computed<GoalGroup>(() => options.groups ? chosenGroup.value ?? linkedGroup.value ?? storedGroup.value : 'goals')
  function setGroup(value: GoalGroup) {
    chosenGroup.value = value
    if (value !== storedGroup.value) void prefs.save({ ...(prefs.value.value ?? {}), group: value }, 0)
  }

  // ---------- Toolbar state ----------
  const filter = ref<StateFilter>('all')
  const sort = ref<GoalSort>('eta')
  const dir = ref<'asc' | 'desc'>('asc')
  // Hide done is on until the person turns it off; the choice is theirs on every device.
  const hideDone = computed(() => prefs.value.value?.hideDone ?? true)
  function setHideDone(value: boolean) { void prefs.save({ ...(prefs.value.value ?? {}), hideDone: value }, 0) }
  // Display › Show: goals the person hid stay out until they show them; Waiting on you is on until turned off.
  const showHidden = computed(() => prefs.value.value?.showHidden ?? false)
  function setShowHidden(value: boolean) { void prefs.save({ ...(prefs.value.value ?? {}), showHidden: value }, 0) }
  const showWaiting = computed(() => prefs.value.value?.showWaiting ?? true)
  function setShowWaiting(value: boolean) { void prefs.save({ ...(prefs.value.value ?? {}), showWaiting: value }, 0) }
  // The toolbar's Waiting on you toggle and Display › Show flip this one setting (v1.2), so they never disagree.
  function toggleWaiting() { setShowWaiting(!showWaiting.value) }
  // Only a person hides goals for themselves; agents and keys cannot.
  const canHide = computed(() => session.identity?.principal.kind === 'person')
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
  // A group switch keeps the frame and clears the rows until the new list arrives.
  const replacing = ref(false)
  const loadingMore = ref(false)
  // When this screen last received the list: the Reconnecting stamp's "last update".
  const updatedAt = ref<number | null>(null)
  let turn = 0, shownQuery = ''
  // A list read retires pagination at once. The page still in flight must not
  // leave "Show more" disabled, and must not append onto the list that replaced it.
  let pageToken = 0
  let controller: AbortController | undefined
  // Epics are never hidden (v1), so only the Goals list asks for hidden goals.
  const query = () => ({ group: group.value, q: options.term.value, state: filter.value === 'all' ? null : filter.value, hideDone: hideDone.value, showHidden: group.value === 'goals' && showHidden.value, sort: sort.value, dir: dir.value })
  function retirePagination() { pageToken++; loadingMore.value = false }
  function read(): Promise<void> {
    if (!owner.value || !options.active() || !settled.value) { replacing.value = false; return Promise.resolve() }
    controller?.abort()
    const abort = new AbortController(); controller = abort
    retirePagination()
    const mine = ++turn, who = owner.value, asked = JSON.stringify(query())
    reading.value = true
    // The answer counts only for the person, query and turn it was asked for.
    const current = () => mine === turn && who === owner.value
    return listGoals(query(), abort.signal, group.value).then(answer => {
      if (!current()) return
      reading.value = false
      // The same question never goes back to an older snapshot than the one on screen.
      const shown = list.value
      if (shown && asked === shownQuery && (answer.read_through < shown.read_through || (answer.read_through === shown.read_through && answer.generation < shown.generation))) { replacing.value = false; return }
      list.value = answer; shownQuery = asked; error.value = ''; updatedAt.value = Date.now(); replacing.value = false
    }, cause => {
      if (!current() || abort.signal.aborted) return
      error.value = cause instanceof Error ? cause.message : 'Goals could not be read.'
      reading.value = false
      replacing.value = false
    })
  }
  // The next page joins the list; a newer read of the first page replaces it instead.
  function loadMore(): Promise<void> {
    const page = list.value
    if (!page?.next_cursor || loadingMore.value || !owner.value) return Promise.resolve()
    const mine = turn, who = owner.value, token = pageToken
    loadingMore.value = true
    return listGoals({ ...query(), cursor: page.next_cursor }, undefined, group.value).then(answer => {
      if (token !== pageToken || mine !== turn || who !== owner.value || list.value !== page) return
      const known = new Set(page.items.map(goal => goal.id))
      list.value = { ...answer, items: [...page.items, ...answer.items.filter(goal => !known.has(goal.id))] }
    }, cause => {
      if (token === pageToken && mine === turn && who === owner.value) error.value = cause instanceof Error ? cause.message : 'More goals could not be read.'
    }).finally(() => { if (token === pageToken) loadingMore.value = false })
  }
  let settle: ReturnType<typeof setTimeout> | undefined
  watch(options.term, () => { clearTimeout(settle); settle = setTimeout(() => { void read() }, SEARCH_SETTLE_MS) })
  watch([filter, sort, dir, hideDone, showHidden], () => { clearTimeout(settle); void read() })
  // Another group is another list: the rows are replaced, the frame (status note, header) stays.
  watch(group, () => {
    turn++; controller?.abort(); retirePagination(); clearTimeout(settle)
    error.value = ''
    const shown = list.value
    if (shown) { replacing.value = true; list.value = { ...shown, items: [], next_cursor: undefined } }
    void read()
  })
  const poller = usePoller(read, LIST_POLL_MS, { enabled: () => options.active() && !!owner.value && settled.value })
  watch(() => options.active() && !!owner.value && settled.value, on => { if (on) poller.start(true); else { poller.stop(); controller?.abort() } }, { immediate: true })

  // ---------- Live ----------
  const live = ref<GoalsLiveState>('off')
  let stream: { close(): void } | undefined
  let liveSettle: ReturnType<typeof setTimeout> | undefined
  let liveReading = false, liveAgain = false
  async function liveRead() {
    if (liveReading) { liveAgain = true; return }
    liveReading = true
    try { await read() } finally { liveReading = false }
    if (liveAgain && stream) { liveAgain = false; void liveRead() }
  }
  function stopLive() { clearTimeout(liveSettle); liveSettle = undefined; liveAgain = false; stream?.close(); stream = undefined; live.value = 'off' }
  function startLive() {
    stopLive()
    stream = openGoalsLive({
      events: group.value === 'epics' ? EPIC_STREAM_EVENTS : GOAL_STREAM_EVENTS,
      state: value => { live.value = value },
      changed: () => { if (liveSettle === undefined) liveSettle = setTimeout(() => { liveSettle = undefined; void liveRead() }, LIVE_SETTLE_MS) },
    })
  }
  // The stream belongs to the person on screen: another person starts a new one.
  watch([() => options.active(), owner, group], ([on, who]) => { if (on && who) startLive(); else stopLive() }, { immediate: true })
  const statusNote = computed(() => list.value?.status_note ?? null)
  const etaLastSetAt = computed(() => { const at = list.value?.eta_last_set_at ? Date.parse(list.value.eta_last_set_at) : NaN; return Number.isNaN(at) ? null : at })

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
  const inflight = new Set<string>()
  let running = 0
  // Flights belong to one epoch. An owner change drops their slots at once;
  // a late answer never writes, and never keeps the new person's queue from draining.
  let seriesEpoch = 0
  const signOf = (goal: Goal) => `${goal.progress?.pct ?? ''}:${goal.states ? Object.values(goal.states).join('.') : ''}:${goal.done ? 1 : 0}`
  const seriesKey = (id: string, value: GoalRange) => `${id}:${value}`
  function seriesFor(goal: Goal, value: GoalRange = range.value): SeriesEntry | undefined { return series.get(seriesKey(goal.id, value)) }
  function retireSeries() { seriesEpoch++; running = 0; queue.length = 0; inflight.clear() }
  // Rows ask for their series; one is read again when the goal moved or it is old.
  // A "reading" entry with no flight and no queue slot was stranded by an older epoch, so it is asked again.
  function want(goal: Goal, value: GoalRange = range.value) {
    // Epics have no recorded progress series.
    if (goal.expansion !== 'full' || goal.kind === 'epic') return
    const key = seriesKey(goal.id, value), sign = signOf(goal), have = series.get(key)
    if (inflight.has(key) || queue.some(entry => entry.key === key)) return
    if (have && have.state !== 'reading' && have.sign === sign && Date.now() - have.at < SERIES_FRESH_MS) return
    series.set(key, { state: 'reading', createdAt: have?.createdAt ?? null, series: have?.series, at: Date.now(), sign })
    queue.push({ goal, key, sign })
    pump()
  }
  function pump() {
    const epoch = seriesEpoch
    while (running < SERIES_PARALLEL && queue.length) {
      const { goal, key, sign } = queue.shift()!
      const value = key.slice(goal.id.length + 1) as GoalRange, who = owner.value
      running++
      inflight.add(key)
      void Promise.all([goalSeries(goal.id, value), goalCreatedAt(goal.id)]).then(([answer, createdAt]) => {
        if (who !== owner.value || epoch !== seriesEpoch) return
        series.set(key, { state: answer.expansion === 'too_large' ? 'too_large' : 'ready', series: answer, createdAt, at: Date.now(), sign })
      }, () => {
        if (who !== owner.value || epoch !== seriesEpoch) return
        series.set(key, { state: 'failed', createdAt: null, at: Date.now(), sign })
      }).finally(() => {
        if (epoch !== seriesEpoch) return
        inflight.delete(key)
        running--
        pump()
      })
    }
  }

  // ---------- Hide for me (AEON-1614) ----------
  // {n} in Display › Show: the person's hidden goals in these projects, whatever the filters.
  const hiddenCount = computed(() => list.value?.hidden_count ?? 0)
  const hiding = reactive(new Set<string>())
  const failure = (cause: unknown, fallback: string) => cause instanceof Error && cause.message ? cause.message : fallback
  // Shows the server's answer at once: with hidden goals off the row leaves (its place is
  // kept for Undo), with them on it stays and turns muted. A fresh read follows.
  function placeHidden(id: string, hidden: boolean): number {
    const shown = list.value
    if (!shown) return -1
    const index = shown.items.findIndex(goal => goal.id === id)
    const count = Math.max(0, (shown.hidden_count ?? 0) + (hidden ? 1 : -1))
    if (index !== -1 && (!hidden || showHidden.value)) {
      list.value = { ...shown, hidden_count: count, items: shown.items.map(goal => goal.id === id ? { ...goal, hidden } : goal) }
    } else if (index !== -1) {
      list.value = { ...shown, hidden_count: count, items: shown.items.filter(goal => goal.id !== id) }
    } else {
      list.value = { ...shown, hidden_count: count }
    }
    return index
  }
  async function setHidden(goal: Goal, hidden: boolean, at = -1) {
    const who = owner.value, id = goal.id
    if (!who || !canHide.value || hiding.has(id)) return
    hiding.add(id)
    try {
      await (hidden ? hideGoal(id) : unhideGoal(id))
    } catch (cause) {
      if (who === owner.value) toast(failure(cause, hidden ? 'The goal could not be hidden.' : 'The goal could not be shown again.'), { tone: 'error' })
      return
    } finally { hiding.delete(id) }
    // An answer for another person never reaches this screen.
    if (who !== owner.value) return
    if (hidden) {
      const index = placeHidden(id, true)
      toast(hideCopy.toast, { key: `goal-hidden:${id}`, action: { label: hideCopy.undo, run: () => { if (who === owner.value) void setHidden(goal, false, index) } } })
    } else {
      const shown = list.value
      placeHidden(id, false)
      // Undo puts the goal back where it was, before the fresh read confirms the order.
      if (shown && at !== -1 && !shown.items.some(item => item.id === id) && list.value) {
        const items = [...list.value.items]
        items.splice(Math.min(at, items.length), 0, { ...goal, hidden: false })
        list.value = { ...list.value, items }
      }
    }
    void read()
  }
  const hide = (goal: Goal) => setHidden(goal, true)
  const unhide = (goal: Goal) => setHidden(goal, false)

  // ---------- Counts ----------
  const totals = computed<GoalCounts>(() => list.value?.totals ?? { moving: 0, queued: 0, paused: 0, done: 0 })
  const totalsPartial = computed(() => list.value?.totals_partial === true)
  const counts = computed<Record<StateFilter, number>>(() => ({ all: totals.value.moving + totals.value.queued + totals.value.paused + totals.value.done, ...totals.value }))
  const loaded = computed(() => list.value !== null)

  // Another person or workspace starts from nothing: no list, no open goals, no series.
  watch(owner, () => {
    turn++; controller?.abort(); retireSeries(); retirePagination()
    list.value = null; error.value = ''; reading.value = false; replacing.value = false; updatedAt.value = null
    open.clear(); charts.clear(); folds.clear(); series.clear(); filter.value = 'all'; chosenGroup.value = null
    if (options.active()) void read()
  }, { flush: 'sync' })
  function stop() { poller.stop(); controller?.abort(); clearTimeout(settle); stopLive(); retireSeries(); retirePagination() }
  // Leaving the page stops the reads; its watchers go with the page's scope.
  onScopeDispose(stop)

  return {
    group, setGroup,
    filter, sort, dir, hideDone, setHideDone, showHidden, setShowHidden, showWaiting, setShowWaiting, toggleWaiting, prefsSettled,
    canHide, hiddenCount, hiding, hide, unhide, range, setRange, setSort, list, goals, hiddenByGroups, error, reading, replacing, loaded, loadingMore, loadMore, read,
    live, updatedAt, statusNote, etaLastSetAt,
    open, charts, folds, isOpen, allOpen, toggle, toggleAll, toggleFold, toggleChart, searchOpens, want, seriesFor, counts, totals, totalsPartial, stop,
  }
}
export type GoalsPage = ReturnType<typeof useGoalsPage>
