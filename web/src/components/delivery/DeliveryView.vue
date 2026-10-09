<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Project › Delivery (AEON-994 draft 5, package 2): the page head — Numbers | Flow,
// the 7 · 30 · 90 · 180 · 365-day window and Simple | Expert, both the person's
// own (saved server-side) — and the Numbers views: Simple (the default; summary,
// three plain sections, small charts) and Expert (ten tiles, ten trends).
// Controls sit in the head and never move; content below grows downward. Flow
// (package 6) reads the recorded runs: ?mode=live|replay|compare and ?run=.
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import ProjectTabs from '../work/ProjectTabs.vue'
import ExpertTile from './ExpertTile.vue'
import FlowView from './FlowView.vue'
import LevelSwitch from './LevelSwitch.vue'
import SimpleNumbers from './SimpleNumbers.vue'
import TrendChart from './TrendChart.vue'
import WindowSwitch from './WindowSwitch.vue'
import { deliveryLanguage } from '../../lib/delivery'
import { bucketOf, clockTime, DEFAULT_PREFS, DELIVERY_PREFS_KEY, hasAnyData, numbersOf, readDeliveryMetrics, readPrefs, shortDate, type DeliveryMetrics, type DeliveryPrefs, type Level, type TileModel, type WindowDays } from '../../lib/deliveryNumbers'
import { deliveryText, fill } from '../../lib/deliveryNumbersText'
import { FLOW_MODES, type FlowMode } from '../../lib/deliveryFlowModes'
import { timeLabel } from '../../lib/deliveryFlow'
import { flowText, put } from '../../lib/deliveryFlowText'
import { useDeliveryFlow } from '../../lib/useDeliveryFlow'
import { onPreferenceFailure, preferenceSaves, usePreference } from '../../lib/preferences'
import { usePoller } from '../../lib/usePolledData'
import { vClipTip } from '../../directives/clipTip'
import { useProfile } from '../../stores/profile'

const props = defineProps<{ project: { id: string; routeKey: string; title: string } }>()
const route = useRoute()
const router = useRouter()
const profile = useProfile()
const lang = computed(() => deliveryLanguage(profile.profile?.locale))
const text = computed(() => deliveryText(lang.value))

// ---------- Views: ?view=numbers|flow ----------
const view = computed<'numbers' | 'flow'>(() => route.query.view === 'flow' ? 'flow' : 'numbers')
const views = computed(() => [
  { id: 'numbers', label: text.value.views.numbers, icon: 'bars' as const },
  { id: 'flow', label: text.value.views.flow, icon: 'flow' as const },
])
function setView(id: string) {
  if (id === view.value) return
  const { view: _view, ...query } = route.query
  void router.replace({ path: route.path, query: id === 'flow' ? { ...query, view: 'flow' } : query, hash: route.hash })
}

// ---------- The person's level and window ----------
// Held here once chosen, so a person without a saved preference (or a failed save) still
// sees their choice; the stored value wins only until they choose.
const pref = usePreference<Partial<DeliveryPrefs>>(DELIVERY_PREFS_KEY)
const prefsReady = ref(false)
void pref.ready.then(() => { prefsReady.value = true })
const chosen = ref<DeliveryPrefs | null>(null)
const prefs = computed<DeliveryPrefs>(() => chosen.value ?? (prefsReady.value ? readPrefs(pref.value.value) : DEFAULT_PREFS))
// The listener fires inside the failed write, before the set records it. The watch only
// clears the warning, so a retry does not flash the failure that is still in the set.
const prefFailed = ref(false)
onBeforeUnmount(onPreferenceFailure(key => { if (key === DELIVERY_PREFS_KEY) prefFailed.value = true }))
watch(() => [...preferenceSaves.failed], ids => {
  if (!ids.some(id => id.endsWith(`/${DELIVERY_PREFS_KEY}`))) prefFailed.value = false
})
function choose(next: Partial<DeliveryPrefs>) {
  const value = { ...prefs.value, ...next }
  chosen.value = value
  prefFailed.value = false
  pref.save(value, 0)
}
function retryPrefs() {
  prefFailed.value = false
  pref.save(chosen.value ?? prefs.value, 0)
}
const setWindow = (window: WindowDays) => choose({ window })
const setLevel = (level: Level) => choose({ level })

// ---------- Flow: ?mode=live|replay|compare and ?run=, read while Flow is shown ----------
const flowMode = computed<FlowMode>(() => FLOW_MODES.includes(route.query.mode as FlowMode) ? route.query.mode as FlowMode : 'live')
const flowRun = computed(() => typeof route.query.run === 'string' && route.query.run ? route.query.run : null)
const flow = useDeliveryFlow({ projectId: () => props.project.id, mode: () => flowMode.value, run: () => flowRun.value, active: () => view.value === 'flow', lang: () => lang.value })
function setFlowMode(mode: FlowMode) {
  if (mode === flowMode.value) return
  const { mode: _mode, run: _run, ...query } = route.query
  void router.replace({ path: route.path, query: mode === 'live' ? query : { ...query, mode }, hash: route.hash })
}
function setFlowRun(run: string) {
  void router.replace({ path: route.path, query: { ...route.query, run }, hash: route.hash })
}

// ---------- Numbers: one bounded read, refreshed every minute while visible ----------
// A failed read shows no numbers at all: an old answer never stands in for a new one.
const data = shallowRef<DeliveryMetrics | null>(null)
const status = ref<'loading' | 'ready' | 'error'>('loading')
let reading: AbortController | null = null
let generation = 0
async function load() {
  const turn = ++generation, projectId = props.project.id
  reading?.abort()
  const controller = reading = new AbortController()
  try {
    const answer = await readDeliveryMetrics(projectId, controller.signal)
    if (turn !== generation || projectId !== props.project.id) return
    data.value = answer
    status.value = 'ready'
  } catch {
    if (turn !== generation || controller.signal.aborted) return
    data.value = null
    status.value = 'error'
  } finally { if (reading === controller) reading = null }
}
function retry() { status.value = 'loading'; void load() }
const poller = usePoller(load, 60_000, { invalidate: () => { generation++; reading?.abort() } })
onMounted(() => { void profile.load(); poller.start(true) })
onBeforeUnmount(() => { poller.stop(); reading?.abort() })
watch(() => props.project.id, () => { data.value = null; status.value = 'loading'; poller.restart() })

const state = computed<'loading' | 'error' | 'ready'>(() => status.value === 'ready' && !prefsReady.value ? 'loading' : status.value)
const numbers = computed(() => data.value ? numbersOf(data.value, prefs.value.window, lang.value) : null)
// Placeholders keep every tile and chart in its slot while loading or after a failed read.
const empty = computed(() => numbersOf({ project_id: props.project.id, generated_at: '', source: null, metrics: [] }, prefs.value.window, lang.value))
const tiles = computed(() => state.value === 'ready' && numbers.value ? numbers.value.tiles : empty.value.tiles)
const charts = computed(() => state.value === 'ready' && numbers.value ? numbers.value.charts : empty.value.charts)
const source = computed(() => data.value?.source ?? null)
const noData = computed(() => state.value === 'ready' && !!data.value && !hasAnyData(data.value))
const updated = computed(() => {
  if (view.value === 'flow') {
    // Flow names its own moment: Live says what "now" is; Replay and Compare show recorded runs.
    const shown = flow.data.value
    return flowMode.value === 'live' && shown?.now != null ? put(flowText(lang.value).nowLive, { t: timeLabel(shown, shown.now) }) : ''
  }
  if (state.value !== 'ready' || !data.value?.generated_at) return ''
  const at = fill(text.value.updated, { t: clockTime(data.value.generated_at, lang.value) })
  return source.value?.app_connected ? `${at} · ${text.value.live}` : at
})
const chips = computed(() => {
  const t = text.value, s = source.value
  if (!s) return []
  const day = (iso: string | null) => iso ? shortDate(iso.slice(0, 10), lang.value) : ''
  const back = s.backfill === 'done' ? { icon: 'history' as const, b: t.srcBack[0], rest: fill(t.srcBack[1], { d: day(s.backfill_since) }) }
    : s.backfill === 'running' ? { icon: 'history' as const, b: t.srcRun[0], rest: s.covered_since ? fill(t.srcRun[1], { d: day(s.covered_since) }) : t.srcRunNone[1] }
      : { icon: 'history' as const, b: t.srcNone[0], rest: t.srcNone[1] }
  return [
    { icon: 'pulse' as const, b: s.app_connected ? t.srcApp[0] : t.srcAppOff[0], rest: s.app_connected ? t.srcApp[1] : t.srcAppOff[1] },
    back,
    { icon: 'merge' as const, b: t.srcTool[0], rest: t.srcTool[1] },
  ]
})
const unit = computed(() => bucketOf(prefs.value.window))
const trendCaption = computed(() => {
  const t = text.value
  return `${{ day: t.perDay, week: t.perWeek, month: t.perMonth }[unit.value]} · ${fill(t.lastDays, { w: prefs.value.window })}`
})

// ---------- Definition popover: hover or focus shows it, a click pins it, Esc or scrolling closes it ----------
const tip = ref<{ tile: TileModel; x: number; y: number; pinned: boolean; placed: boolean } | null>(null)
const tipEl = ref<HTMLElement>()
let tipAnchor: HTMLElement | null = null
function openTip(tile: TileModel, anchor: HTMLElement, pin: boolean) {
  if (!pin && tip.value?.pinned) return
  if (pin && tip.value?.pinned && tipAnchor === anchor) { closeTip(); return }
  tipAnchor = anchor
  const rect = anchor.getBoundingClientRect()
  tip.value = { tile, x: rect.left, y: rect.bottom + 8, pinned: pin, placed: false }
  requestAnimationFrame(place)
}
function place() {
  const el = tipEl.value, current = tip.value
  if (!el || !current || !tipAnchor) return
  const rect = tipAnchor.getBoundingClientRect(), width = el.offsetWidth, height = el.offsetHeight
  let y = rect.bottom + 8
  if (y + height > window.innerHeight - 12) y = Math.max(12, rect.top - height - 8)
  tip.value = { ...current, x: Math.max(12, Math.min(window.innerWidth - width - 12, rect.left + rect.width / 2 - width / 2)), y, placed: true }
}
function leaveTip() { if (!tip.value?.pinned) closeTip() }
function closeTip() { tip.value = null; tipAnchor = null }
function onKey(event: KeyboardEvent) { if (event.key === 'Escape' && tip.value) { const anchor = tipAnchor; closeTip(); anchor?.focus({ preventScroll: true }) } }
function onPointer(event: PointerEvent) { if (tip.value?.pinned && !tipEl.value?.contains(event.target as Node) && !tipAnchor?.contains(event.target as Node)) closeTip() }
onMounted(() => {
  document.addEventListener('keydown', onKey)
  document.addEventListener('pointerdown', onPointer, true)
  window.addEventListener('scroll', closeTip, { passive: true, capture: true })
})
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey)
  document.removeEventListener('pointerdown', onPointer, true)
  window.removeEventListener('scroll', closeTip, { capture: true })
})
// The tile behind an open popover belongs to this window and this answer.
watch([() => prefs.value.window, () => prefs.value.level, view, lang], closeTip)

// Source chips wrap onto several rows on a phone. A failed read must keep that box,
// or the shorter banner collapses it and every Learn button below jumps (AEON-541).
const statusEl = ref<HTMLElement>()
const statusHold = ref(0)
watch(state, (next, prev) => {
  if (prev === 'ready' && next !== 'ready') statusHold.value = statusEl.value?.getBoundingClientRect().height ?? 0
  else if (next === 'ready') statusHold.value = 0
}, { flush: 'pre' })
</script>

<template>
  <section class="dl" :aria-labelledby="`dl-title-${project.id}`">
    <svg class="dl-defs" width="0" height="0" aria-hidden="true" focusable="false">
      <defs>
        <linearGradient id="dl-fade" x1="0" y1="0" x2="0" y2="1"><stop offset="0" class="fade-0" /><stop offset=".45" class="fade-1" /></linearGradient>
        <pattern id="dl-hatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><line class="hatch-line" x1="0" y1="0" x2="0" y2="6" /></pattern>
      </defs>
    </svg>
    <div class="dl-head">
      <div class="dl-left">
        <div class="dl-titleline">
          <h2 :id="`dl-title-${project.id}`">{{ text.title }}</h2>
          <ProjectTabs class="dl-views" :items="views" :selected="view" :label="text.viewsLabel" @select="setView" />
        </div>
        <p class="dl-sub">{{ view === 'flow' ? text.flowSub : text.sub }}</p>
      </div>
      <div class="dl-right">
        <div class="dl-ctrls">
          <WindowSwitch :class="{ 'slot-hidden': view === 'flow' }" :model-value="prefs.window" :label="text.windowLabel" :unit="text.days" @update:model-value="setWindow" />
          <LevelSwitch :model-value="prefs.level" :label="text.levelLabel" :names="text.level" @update:model-value="setLevel" />
        </div>
        <span class="dl-updated" data-testid="delivery-updated">{{ updated || ' ' }}</span>
      </div>
      <!-- Save feedback sits on the status row. It is out of flow, so Learn does not move when it appears or clears.
           A refused save and a failed read can stand together: each owns one half of the row for good (save left,
           read right), so neither alert nor its retry changes place or size when the other comes or goes (AEON-541). -->
      <p v-if="prefFailed" class="banner err half pref-warn" role="alert" data-testid="delivery-pref-error">
        <AppIcon name="alert" :size="16" />
        <span v-clip-tip class="grow">{{ text.prefErr }}</span>
        <button type="button" class="btn sm" :data-tip="text.prefRetry" @click="retryPrefs"><AppIcon name="refresh" :size="14" /><span class="lbl">{{ text.prefRetry }}</span></button>
      </p>
    </div>

    <template v-if="view === 'numbers'">
      <div ref="statusEl" class="dl-status" :class="{ veiled: prefFailed && state !== 'error' }" :style="statusHold > 0 ? { minHeight: `${statusHold}px` } : undefined">
        <div v-if="state === 'loading'" class="sources" aria-hidden="true">
          <span class="sk chip-sk" style="width: 210px" /><span class="sk chip-sk" style="width: 250px" /><span class="sk chip-sk" style="width: 230px" />
        </div>
        <div v-else-if="state === 'error'" class="banner err half" role="alert" data-testid="delivery-load-error">
          <AppIcon name="alert" :size="16" />
          <span v-clip-tip class="grow"><b>{{ text.errT }}</b> {{ text.errB }}</span>
          <button type="button" class="btn sm" :data-tip="text.retry" @click="retry"><AppIcon name="refresh" :size="14" /><span class="lbl">{{ text.retry }}</span></button>
        </div>
        <div v-else-if="!source" class="banner" role="status">
          <AppIcon name="info" :size="16" />
          <span v-clip-tip class="grow"><b>{{ text.norepoT }}</b> {{ text.norepoB }}</span>
        </div>
        <div v-else-if="noData" class="banner" role="status">
          <AppIcon name="info" :size="16" />
          <span v-clip-tip class="grow"><b>{{ text.nodataT }}</b> {{ fill(text.nodataB, { repo: source.repository }) }}</span>
        </div>
        <div v-else class="sources">
          <span v-for="chip in chips" :key="chip.b" class="src"><AppIcon :name="chip.icon" :size="14" /><span><b>{{ chip.b }}</b>{{ chip.rest }}</span></span>
        </div>
      </div>

      <SimpleNumbers v-if="prefs.level === 'simple'" :data="data" :window="prefs.window" :state="state" :no-data="noData || (state === 'ready' && !source)" :lang="lang" />
      <div v-else class="expert">
        <p class="tiles-cap">{{ fill(text.cap, { w: prefs.window }) }}</p>
        <div class="tiles" role="list" data-testid="delivery-tiles">
          <ExpertTile v-for="tile in tiles" :key="tile.def.key" :tile="tile" :state="state" :text="text" :info-open="tip?.tile.def.key === tile.def.key"
            @info="(anchor, pin) => openTip(tile, anchor, pin)" @info-leave="leaveTip" />
        </div>
        <div class="trends-head">
          <h3>{{ text.trends }}</h3>
          <div class="legend">
            <span><svg class="lg-sw" width="18" height="8" aria-hidden="true"><line x1="0" y1="4" x2="18" y2="4" class="g-line" /></svg>{{ text.lgP50[unit] }}</span>
            <span><svg class="lg-sw" width="14" height="10" aria-hidden="true"><rect width="14" height="10" rx="2" class="g-band" /></svg>{{ text.lgBand }}</span>
            <span><svg class="lg-sw" width="18" height="8" aria-hidden="true"><line x1="0" y1="4" x2="18" y2="4" class="g-target" /></svg>{{ text.lgTarget }}</span>
            <span><svg class="lg-sw" width="14" height="10" aria-hidden="true"><rect width="14" height="10" rx="2" class="g-partial" /><circle cx="7" cy="5" r="2.4" class="g-dot hollow" /></svg>{{ text.lgPartial }}</span>
            <span><svg class="lg-sw" width="14" height="10" aria-hidden="true"><rect x=".5" y=".5" width="13" height="9" rx="2" class="g-nodata outline" /></svg>{{ text.lgNone }}</span>
          </div>
          <span class="tr-cap">{{ trendCaption }}</span>
        </div>
        <div class="charts" data-testid="delivery-charts">
          <TrendChart v-for="chart in charts" :key="chart.key" :model="chart" :state="state" :text="text" />
        </div>
        <div class="defs">
          <h3>{{ text.defsTitle }}</h3>
          <p v-for="[term, meaning] in text.defs" :key="term"><b>{{ term }}.</b> {{ fill(meaning, { w: prefs.window }) }}</p>
        </div>
      </div>
    </template>
    <template v-else>
      <!-- Without any recorded run, Flow shows the approved example, and says so. -->
      <div v-if="flow.example.value" class="flow-empty banner" :class="{ veiled: prefFailed }" role="status">
        <AppIcon name="flow" :size="16" />
        <span class="grow"><b>{{ text.flowNone }}</b> {{ flowText(lang).example }}</span>
      </div>
      <FlowView :data="flow.data.value" :data-key="flow.key.value" :mode="flowMode" :status="flow.status.value" :empty="flow.empty.value"
        :choices="flow.choices.value" :choice="flow.runId.value" :truncated="flow.truncated.value" :level="prefs.level" :lang="lang"
        @update:mode="setFlowMode" @choose="setFlowRun" @retry="flow.retry" />
    </template>

    <Teleport to="body">
      <div v-if="tip" ref="tipEl" class="dl-tip" role="tooltip" :style="{ left: `${tip.x}px`, top: `${tip.y}px`, visibility: tip.placed ? undefined : 'hidden' }" @mouseleave="leaveTip">
        <b>{{ tip.tile.label }}</b>
        <p>{{ tip.tile.definition }}</p>
        <p v-if="tip.tile.reason"><span class="meta">{{ text.why }}</span><br>{{ tip.tile.reason }}</p>
        <p class="meta">{{ text.source }}: {{ tip.tile.source }}<br>{{ text.window }}: {{ fill(text.windowT, { w: prefs.window }) }}<br>{{ tip.tile.target }} · {{ text.arion }}</p>
      </div>
    </Teleport>
  </section>
</template>

<style scoped>
.dl { container: delivery / inline-size; margin-top: 20px; padding-bottom: 32px; }
.dl-defs { position: absolute; width: 0; height: 0; overflow: hidden; }
.fade-0 { stop-color: var(--teal); stop-opacity: 0; }
.fade-1 { stop-color: var(--teal); stop-opacity: .13; }
.hatch-line { stroke: var(--line-2); stroke-width: 1; }
/* Top-anchored head: the controls keep their place whatever the text beside them says. */
.dl-head { position: relative; display: flex; align-items: flex-start; justify-content: space-between; gap: 12px 24px; flex-wrap: wrap; }
.dl-left { flex: 1 1 360px; min-width: 0; }
.dl-titleline { display: flex; align-items: center; gap: 12px; }
.dl-titleline h2 { margin: 0; font: 650 19px/1.25 var(--font); letter-spacing: -.01em; color: var(--ink); }
.dl-sub { max-width: 72ch; margin: 4px 0 0; font-size: 13.5px; color: var(--ink-2); }
.dl-right { display: flex; flex-direction: column; align-items: flex-end; gap: 6px; }
.dl-ctrls { display: flex; align-items: center; gap: 10px; }
.slot-hidden { visibility: hidden; }
.dl-updated { font: 500 11.5px/1.4 var(--mono); color: var(--ink-3); white-space: nowrap; }
.dl-status { min-height: 40px; }
.dl-status.veiled, .flow-empty.veiled { visibility: hidden; }
/* The warning replaces the status row in place: same anchor, no extra flow, so tiles stay put. */
.banner.pref-warn { position: absolute; z-index: 2; top: calc(100% + 12px); right: calc(50% + 6px); left: 0; margin: 0; min-height: 28px; padding-block: 0; flex-wrap: nowrap; }
.banner.pref-warn .grow { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* The read alert owns the right half, the save alert the left half: always, so a retry never moves or resizes (AEON-541). */
.dl-status .banner.half { margin-left: calc(50% + 6px); }
.sources { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-height: 28px; margin-top: 12px; }
.src { display: inline-flex; align-items: center; gap: 6px; min-height: 26px; padding: 0 10px; border-radius: 999px; background: var(--surface-sunken); color: var(--ink-2); font-size: 12px; }
.src svg { flex: none; color: var(--teal-ink); }
.src b { font-weight: 600; color: var(--ink); }
.chip-sk { height: 26px; border-radius: 999px; }
.banner { display: flex; align-items: center; gap: 12px; margin-top: 12px; padding: 10px 12px 10px 14px; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); font-size: 13px; color: var(--ink-2); }
.banner svg { flex: none; color: var(--ink-2); }
.banner b { font-weight: 600; color: var(--ink); }
.banner .grow { flex: 1; min-width: 0; }
.banner.err { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.banner.err svg { color: var(--danger); }
.banner .btn { flex: none; gap: 6px; }
/* While a read is in flight or has failed, the row keeps the height measured from the chips (AEON-541). */
.dl-status .banner { min-height: 28px; padding-block: 0; flex-wrap: nowrap; }
.dl-status .banner .grow { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.flow-empty { margin-top: 16px; }
.tiles-cap { margin: 18px 0 0; font: 500 10.5px/1.5 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.tiles { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 12px; margin-top: 8px; }
.trends-head { display: flex; align-items: center; gap: 10px 20px; flex-wrap: wrap; margin-top: 26px; }
.trends-head h3 { margin: 0; font: 650 15px/1.3 var(--font); color: var(--ink); }
.legend { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 14px; font-size: 12px; color: var(--ink-2); }
.legend > span { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.tr-cap { margin-left: auto; font-size: 12px; color: var(--ink-2); }
.charts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-top: 12px; }
.defs { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 28px; margin-top: 22px; padding-top: 14px; border-top: 1px solid var(--line); }
.defs h3 { grid-column: 1 / -1; margin: 0; font: 650 13.5px/1.3 var(--font); color: var(--ink); }
.defs p { margin: 0; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.defs b { font-weight: 600; color: var(--ink); }
.lg-sw { display: inline-block; flex: none; }
.g-line { fill: none; stroke: var(--teal); stroke-width: 2; stroke-linecap: round; }
.g-band { fill: color-mix(in srgb, var(--teal) 13%, transparent); }
.g-target { stroke: var(--ink-2); stroke-width: 1.5; stroke-dasharray: 5 4; fill: none; }
.g-partial { fill: color-mix(in srgb, var(--gold) 9%, transparent); }
.g-dot.hollow { fill: var(--surface-raised); stroke: var(--teal); stroke-width: 1.6; }
.g-nodata { fill: url(#dl-hatch); }
.g-nodata.outline { stroke: var(--line-2); stroke-width: 1; }
.sk { display: block; border-radius: 6px; background: var(--skeleton); }
@media (prefers-reduced-motion: no-preference) {
  .sk { background: linear-gradient(90deg, var(--skeleton) 0%, var(--skeleton-hi) 50%, var(--skeleton) 100%) 0 0 / 200% 100%; animation: dl-sk 1.4s ease-in-out infinite; }
}
@keyframes dl-sk { to { background-position: -200% 0; } }
@container delivery (max-width: 900px) {
  /* Half a row has no room for the label: the retry keeps its icon and its name (read aloud, shown as a tip). */
  .half .btn { width: 28px; padding: 0; }
  .half .btn .lbl { position: absolute; width: 1px; height: 1px; margin: -1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; border: 0; }
}
@container delivery (max-width: 1000px) {
  .tiles { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .charts { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@container delivery (max-width: 640px) {
  .dl-titleline { flex-wrap: wrap; gap: 8px; }
  .dl-right { width: 100%; align-items: stretch; }
  .dl-ctrls { flex-wrap: wrap; gap: 8px; }
  .dl-views :deep(button) { min-height: 44px; padding: 0 14px; }
  .src { height: auto; padding: 4px 10px; }
  .tiles { gap: 8px; }
  .charts { grid-template-columns: minmax(0, 1fr); }
  .tr-cap { flex-basis: 100%; margin-left: 0; }
  .defs { grid-template-columns: minmax(0, 1fr); }
  .banner { flex-wrap: wrap; }
  .banner.pref-warn { flex-wrap: nowrap; min-height: 44px; }
  .banner .btn { min-height: 44px; }
  .dl-status .sources, .dl-status .banner { min-height: 44px; }
  .dl-status .banner { flex-wrap: nowrap; }
  /* Each alert fills its half of one 44 px row: the sentence takes up to three lines, the retry is a 44 px icon. */
  .banner.pref-warn, .dl-status .banner.half { gap: 6px; padding: 0 4px 0 10px; }
  .banner.pref-warn .grow, .dl-status .banner.half .grow { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 3; white-space: normal; font-size: 12px; line-height: 1.2; }
  .half .btn { width: 44px; }
}
@container delivery (max-width: 460px) { .tiles { grid-template-columns: minmax(0, 1fr); } }
</style>

<style>
/* The definition popover lives on body: fixed, so it never moves the page (AEON-541). */
.dl-tip { position: fixed; z-index: 80; width: max-content; max-width: min(340px, calc(100vw - 24px)); padding: 10px 12px; border-radius: 12px; background: var(--tip-bg); color: var(--tip-ink); font-size: 12.5px; line-height: 1.45; box-shadow: var(--shadow-pop); }
.dl-tip b { display: block; margin-bottom: 3px; font-weight: 650; }
.dl-tip p { margin: 0; color: inherit; opacity: .92; }
.dl-tip p + p { margin-top: 6px; }
.dl-tip .meta { font: 500 11px/1.5 var(--mono); opacity: .75; }
</style>
