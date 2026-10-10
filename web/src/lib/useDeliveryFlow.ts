// SPDX-License-Identifier: AGPL-3.0-only
// Delivery › Flow loading (AEON-994 draft 5, package 6). Live reads the runs of the last
// hours and follows the server-sent hints (delivery.step, delivery.item, delivery.incident)
// by reading again; Replay and Compare read one whole run. Every answer belongs to the
// project, mode and run it was asked for: a stale answer is dropped, a failed read shows
// an error rather than an old answer and stays there until Retry or a bounded refresh.
// The run list is kept, so a failure cannot change the derived run and start another read.
// Without any recorded run every mode shows one empty state; Flow never shows sample data (AEON-1135).
import { computed, onBeforeUnmount, ref, shallowRef, watch, type Ref } from 'vue'
import type { DeliveryLanguage } from './delivery'
import { pick, type FlowData } from './deliveryFlow'
import {
  arionTarget, compareData, FLOW_HINTS, flowStreamURL, liveData, parseHint, readFlow, readFlowRun, recordedRuns, replayData, runTitle,
  type ApiFlow, type ApiItem, type Recorded,
} from './deliveryFlowData'
import type { FlowMode } from './deliveryFlowModes'
import { flowText, put } from './deliveryFlowText'

export interface FlowChoice { id: string; label: string }
/** Why nothing shows: no recorded run at all, nothing in flight, no replayable run, no release to race. */
export type FlowEmpty = 'none' | 'live' | 'runs' | 'release' | null
const LIVE_HOURS = 4, LIST_DAYS = 30, LIVE_POLL_MS = 60_000, HINT_DELAY_MS = 400
const RETRY_MS = [2_000, 5_000, 15_000, 30_000] as const

interface SourceLike { readyState: number; onerror: ((ev: Event) => unknown) | null; addEventListener(type: string, listener: (event: MessageEvent) => void): void; close(): void }
export interface FlowLoadOptions {
  projectId: () => string; mode: () => FlowMode; run: () => string | null; active: () => boolean; lang: () => DeliveryLanguage
  /** Injected in tests: the stream source and the clock. */
  open?: (url: string) => SourceLike | null; now?: () => number
}

export function useDeliveryFlow(options: FlowLoadOptions) {
  const open = options.open ?? ((url: string) => typeof EventSource === 'undefined' ? null : new EventSource(url) as unknown as SourceLike)
  const clock = options.now ?? (() => Date.now())
  const phase = ref<'loading' | 'ready' | 'error'>('loading')
  const list = shallowRef<ApiFlow | null>(null)
  const live = shallowRef<Recorded | null>(null)
  const single = shallowRef<Recorded | null>(null)
  /** project|mode|run|none that `phase === ready` belongs to. Empty after a failure. */
  const loadedView = ref('')
  let generation = 0
  let reading: AbortController | null = null

  /** The project has no recorded run in the list window. */
  const none = computed(() => !!list.value && list.value.items.length === 0)
  const releases = computed(() => (list.value?.items ?? []).filter(item => item.kind === 'release' && list.value!.steps.some(s => s.item_id === item.id && s.step_key === 'a')))
  const replayable = computed(() => (list.value?.items ?? []).filter(item => list.value!.steps.some(s => s.item_id === item.id)))
  const byStart = (a: ApiItem, b: ApiItem) => Date.parse(b.started_at ?? '') - Date.parse(a.started_at ?? '') || 0
  /** The run a mode shows: the asked one if it is offered, else the latest. */
  const runId = computed(() => {
    const mode = options.mode(), asked = options.run()
    if (mode === 'live') return null
    if (none.value) return null
    const pool = (mode === 'compare' ? releases.value : replayable.value).slice().sort(byStart)
    // By default the latest release, else the latest run.
    return pool.find(item => item.id === asked)?.id ?? pool.find(item => item.kind === 'release')?.id ?? pool[0]?.id ?? null
  })
  // The run read last may have ended after the run list was read.
  const ended = (id: string) => single.value?.runs[0]?.id === id && single.value.runs[0].facts?.ended != null
  const choices = computed<FlowChoice[]>(() => {
    const mode = options.mode(), lang = options.lang(), text = flowText(lang)
    if (mode === 'live') return []
    const pool = (mode === 'compare' ? releases.value : replayable.value).slice().sort(byStart)
    return pool.map(item => mode === 'compare'
      ? { id: item.id, label: put(text.vsTarget, { a: pick(runTitle(item), lang) }) }
      : { id: item.id, label: `${pick(runTitle(item), lang)} (${item.ended_at || ended(item.id) ? text.final : text.soFarRun})` })
  })

  /** The answer on screen. A list-derived run change is not itself a loaded answer. */
  function viewToken() {
    const mode = options.mode()
    const run = mode === 'live' ? '' : (runId.value ?? '')
    return `${options.projectId()}|${mode}|${run}|${none.value ? 'none' : 'recorded'}`
  }
  const status = computed<'loading' | 'ready' | 'error'>(() => {
    if (phase.value === 'error') return 'error'
    if (phase.value === 'loading' || loadedView.value !== viewToken()) return 'loading'
    return 'ready'
  })
  /** What the page shows, built from the answers in the person's language. */
  const data = computed<FlowData | null>(() => {
    const mode = options.mode(), lang = options.lang()
    if (status.value !== 'ready') return null
    if (none.value) return null
    if (mode === 'live') return live.value ? liveData(live.value) : null
    const rec = single.value, run = rec?.runs[0]
    if (!rec || !run || run.id !== runId.value) return null
    if (mode === 'replay') return replayData(run, rec.origin, rec.now)
    return compareData(run, arionTarget(lang, run.target?.minutes))
  })
  const empty = computed<FlowEmpty>(() => {
    if (status.value !== 'ready' || data.value) return null
    if (none.value) return 'none'
    const mode = options.mode()
    return mode === 'live' ? 'live' : mode === 'compare' ? 'release' : 'runs'
  })
  /** Changes when the view should start over (another mode or run); new data alone keeps the view. */
  const key = computed(() => `${options.projectId()}|${options.mode()}|${runId.value ?? ''}|${none.value ? 'none' : 'recorded'}`)
  const truncated = computed(() => !none.value && !!((options.mode() === 'live' ? live.value?.truncated : single.value?.truncated) || list.value?.truncated))

  // ---------- Reading ----------
  async function load(scope: 'all' | 'view' = 'all') {
    const turn = ++generation, projectId = options.projectId(), mode = options.mode()
    reading?.abort()
    const controller = reading = new AbortController(), signal = controller.signal
    const now = clock()
    // Another project, mode or run is not the answer on screen. The same view refreshes in place.
    if (loadedView.value !== viewToken() || phase.value !== 'ready') phase.value = 'loading'
    try {
      if (scope === 'all' || !list.value) {
        const answer = await readFlow(projectId, { from: new Date(now - LIST_DAYS * 86_400_000) }, signal)
        if (turn !== generation) return
        list.value = answer
      }
      if (!none.value) {
        if (mode === 'live') {
          const answer = await readFlow(projectId, { from: new Date(now - LIVE_HOURS * 3_600_000) }, signal)
          if (turn !== generation) return
          live.value = recordedRuns(answer, { extendOpen: true })
        } else {
          const id = runId.value
          if (id) {
            const answer = await readFlowRun(projectId, id, signal)
            if (turn !== generation || runId.value !== id) return
            single.value = recordedRuns({ now: answer.now, items: [answer.item], steps: answer.steps, incidents: answer.incidents, truncated: answer.truncated }, { extendOpen: false })
          } else single.value = null
        }
      }
      if (turn !== generation || options.projectId() !== projectId) return
      loadedView.value = viewToken()
      phase.value = 'ready'
    } catch {
      if (turn !== generation || signal.aborted) return
      // The list stays. Dropping it changes the derived run, and that used to start another read.
      live.value = null; single.value = null
      loadedView.value = ''
      phase.value = 'error'
    } finally { if (reading === controller) reading = null }
  }
  function retry() { phase.value = 'loading'; void load('all') }

  // ---------- Live hints: read again shortly after a burst ----------
  let source: SourceLike | null = null
  let hintTimer: ReturnType<typeof setTimeout> | undefined
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let pollTimer: ReturnType<typeof setInterval> | undefined
  let retries = 0
  let pending: 'view' | 'all' | null = null
  function hinted(event: MessageEvent) {
    const hint = parseHint(String(event.data))
    if (!hint) return
    retries = 0
    // A hint about another run changes the run list; a hint about the shown run changes the view.
    const shown = options.mode() === 'live' || hint.item_id === runId.value
    pending = none.value || !shown || pending === 'all' ? 'all' : 'view'
    clearTimeout(hintTimer)
    hintTimer = setTimeout(() => { const scope = pending ?? 'view'; pending = null; void load(scope) }, HINT_DELAY_MS)
  }
  function connect() {
    disconnect()
    let opened: SourceLike | null = null
    try { opened = open(flowStreamURL(options.projectId())) } catch { opened = null }
    if (!opened) return
    source = opened
    for (const type of FLOW_HINTS) opened.addEventListener(type, hinted)
    opened.addEventListener('stream.ready', () => { retries = 0 })
    opened.onerror = () => {
      if (source !== opened || opened.readyState !== 2) return
      // The browser gave up (403, 429, a closed stream): wait, read again, reconnect.
      opened.close(); source = null
      const wait = RETRY_MS[Math.min(retries++, RETRY_MS.length - 1)]
      retryTimer = setTimeout(() => { if (options.active()) { void load('view'); connect() } }, wait)
    }
  }
  function disconnect() {
    clearTimeout(retryTimer); retryTimer = undefined
    if (source) { source.close(); source = null }
  }
  function startPoll() {
    clearInterval(pollTimer)
    // Without a recorded run the list is read again, so the first run shows without a reload.
    pollTimer = setInterval(() => { if (options.active() && options.mode() === 'live') void load(none.value ? 'all' : 'view') }, LIVE_POLL_MS)
  }

  watch(() => [options.active(), options.projectId()] as const, ([active], before) => {
    if (!active) { disconnect(); clearInterval(pollTimer); clearTimeout(hintTimer); generation++; reading?.abort(); return }
    if (before && before[1] !== options.projectId()) { list.value = null; live.value = null; single.value = null; loadedView.value = ''; phase.value = 'loading' }
    void load('all'); connect(); startPoll()
  }, { immediate: true })
  watch(() => [options.mode(), options.run()] as const, (next, before) => {
    if (!options.active() || !before) return
    // Explicit selection only. A list-derived run (the latest, or a list cleared after a failure) is not a new read.
    if (next[0] !== before[0]) void load('all')
    else if (next[1] !== before[1]) void load('view')
  })
  onBeforeUnmount(() => { disconnect(); clearInterval(pollTimer); clearTimeout(hintTimer); generation++; reading?.abort() })

  return { status: status as Readonly<Ref<'loading' | 'ready' | 'error'>>, data, empty, choices, runId, key, truncated, retry }
}
