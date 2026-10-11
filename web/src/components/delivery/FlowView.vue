<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Delivery › Flow, A Lanes (AEON-994 draft 5, packages 5 and 6). The mode switch
// Live · Replay · Compare and the legend, one headline, then one card with a control
// bar (48 px), a hint line (20 px), the overview map (64 px), the lanes (330 px) and the
// moment panel (196 px), all fixed, so zooming, panning, playing and moving the time
// never move a control. Below: the release record of the release on screen (AEON-1022),
// then Live lists the runs in flight, Replay and Compare where the time went. Live
// follows "now" at 65 %; dragging the playhead to the past shows "Viewing HH:MM" and
// "Back to now". Replay auto-plays once per session (never with
// reduced motion): 60 s at 1x, 30 s at 2x. Compare races two lane sets on one axis.
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import FlowLanes from './FlowLanes.vue'
import FlowOverview from './FlowOverview.vue'
import InFlightTable from './InFlightTable.vue'
import MomentPanel from './MomentPanel.vue'
import ReleaseRecord from './ReleaseRecord.vue'
import TimeWent from './TimeWent.vue'
import type { DeliveryLanguage } from '../../lib/delivery'
import {
  atNow, backToFollow, createTimeline, fitAll, following, laneModel, minutesText, pick, refreshTimeline, resetTimeline, timeLabel, zoomPreset, zoomTo, ZOOMS, LIVE_AT,
  type FlowData, type FlowLevel, type LaneItem,
} from '../../lib/deliveryFlow'
import {
  autoplayOnce, clockOf, createPlayer, flightRows, FLOW_MODES, headOf, momentOf, recordOf, reducedMotionQuery, takeAutoplay, wentOf,
  type FlowMode, type Frames,
} from '../../lib/deliveryFlowModes'
import { flowText, hintParts, TIMES } from '../../lib/deliveryFlowText'
import type { FlowChoice, FlowEmpty } from '../../lib/useDeliveryFlow'

const props = defineProps<{
  data: FlowData | null; dataKey: string; mode: FlowMode; status: 'loading' | 'ready' | 'error'; empty: FlowEmpty
  choices: FlowChoice[]; choice: string | null; truncated?: boolean
  level: FlowLevel; lang: DeliveryLanguage
  /** Injected in tests: whether this replay may still auto-play, and the animation frames. */
  autoplay?: ReturnType<typeof autoplayOnce>; frames?: Frames
}>()
const emit = defineEmits<{ 'update:mode': [mode: FlowMode]; choose: [id: string]; retry: [] }>()
const text = computed(() => flowText(props.lang))
const sets = computed(() => props.data ? laneModel(props.data) : [])
const timeline = createTimeline()
const player = createPlayer(timeline, props.frames)
const card = ref<HTMLElement>()
// The selection belongs to the step on screen: new data re-binds it by the step's id, a new view clears it.
const selected = shallowRef<LaneItem | null>(null)

// ---------- Reduced motion: read live when it matters; the change event only refreshes the bar ----------
const motion = reducedMotionQuery()
const reducedNow = () => !!motion?.matches
const reduced = ref(reducedNow())
const onMotion = () => { reduced.value = reducedNow(); if (reduced.value) player.stop() }
onMounted(() => motion?.addEventListener?.('change', onMotion))

// ---------- A new view starts over; new data for the same view keeps the person's window and time ----------
let shownKey = ''
let autoTimer: ReturnType<typeof setTimeout> | undefined
function reset() {
  const data = props.data
  if (!data) return
  player.stop()
  clearTimeout(autoTimer)
  resetTimeline(timeline, data, { narrow: (card.value?.clientWidth ?? 1000) < 640 })
  if (props.mode === 'compare') { fitAll(timeline); timeline.T = timeline.p0 }
  selected.value = null
  if (props.mode === 'replay' && !reducedNow()) {
    const key = props.dataKey
    autoTimer = setTimeout(() => {
      if (props.mode === 'replay' && props.dataKey === key && !player.state.playing && !reducedNow() && (props.autoplay ?? takeAutoplay)()) player.play()
    }, 400)
  }
}
function rebind() {
  const sel = selected.value
  if (!sel) return
  const id = sel.step.facts?.id, items = sets.value.flatMap(set => set.items)
  selected.value = items.find(i => id ? i.step.facts?.id === id : i.run.id === sel.run.id && i.step.lane === sel.step.lane && Math.abs(i.step.start - sel.step.start) < 1e-6) ?? null
}
function show() {
  const data = props.data
  if (!data) { player.stop(); clearTimeout(autoTimer); return }
  if (props.dataKey !== shownKey) { shownKey = props.dataKey; reset(); return }
  refreshTimeline(timeline, data)
  rebind()
}
onMounted(show)
watch(() => [props.data, props.dataKey] as const, show, { flush: 'post' })
onBeforeUnmount(() => { player.stop(); clearTimeout(autoTimer); motion?.removeEventListener?.('change', onMotion) })

// ---------- Mode switch: Live · Replay · Compare ----------
const modes = computed(() => FLOW_MODES.map(id => ({ id, label: text.value.modes[id] })))
function modeKey(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  const index = FLOW_MODES.indexOf(props.mode)
  const next = { ArrowLeft: index - 1, ArrowUp: index - 1, ArrowRight: index + 1, ArrowDown: index + 1, Home: 0, End: FLOW_MODES.length - 1 }[event.key]
  if (next === undefined) return
  event.preventDefault()
  const id = FLOW_MODES[(next + FLOW_MODES.length) % FLOW_MODES.length]!
  emit('update:mode', id)
  const group = event.currentTarget as HTMLElement
  requestAnimationFrame(() => group.querySelector<HTMLButtonElement>(`[data-mode="${id}"]`)?.focus())
}

// ---------- The bar ----------
const live = computed(() => props.data?.now != null)
/** Replay and Compare keep the run picker up while the next read is in flight or has failed. */
const retainBar = computed(() => !props.data && props.mode !== 'live' && props.choices.length > 0)
const at = (m: number) => props.data ? timeLabel(props.data, m) : ''
const history = computed(() => live.value && !atNow(timeline))
const chip = computed(() => history.value ? text.value.viewing.replace('{t}', at(timeline.T)) : text.value.live.replace('{t}', at(props.data?.now ?? 0)))
const clock = computed(() => {
  const t = text.value, data = props.data
  if (!data) return ''
  if (!live.value) return clockOf(props.mode, { data, text: t, T: timeline.T })
  const base = t.at.replace('{t}', at(timeline.T))
  if (atNow(timeline)) return `${base} (${t.now})`
  return timeline.T > data.now! ? `${base} · ${t.expected}` : base
})
const followOn = computed(() => following(timeline))
const followLabels = computed(() => live.value ? [text.value.followingNow, text.value.backNow] : [text.value.following, text.value.follow])
const speeds = [1, 2] as const
function toggle() {
  if (player.state.playing) { player.stop(); return }
  if (reducedNow()) { reduced.value = true; return }
  player.play()
}
function speedKey(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return
  event.preventDefault()
  player.state.speed = player.state.speed === 1 ? 2 : 1
  const group = event.currentTarget as HTMLElement
  requestAnimationFrame(() => group.querySelector<HTMLButtonElement>(`[data-speed="${player.state.speed}"]`)?.focus())
}
function choose(event: Event) {
  const id = (event.target as HTMLSelectElement).value
  if (id && id !== props.choice) emit('choose', id)
}

// ---------- Zoom presets: Fit all · 15 · 30 · 1 h ----------
const presets = computed(() => [{ id: 'fit' as const, label: text.value.fit }, ...ZOOMS.map(z => ({ id: z, label: text.value.zooms[z]! }))])
const preset = computed(() => zoomPreset(timeline))
function zoom(id: 'fit' | typeof ZOOMS[number]) {
  if (id === 'fit') { fitAll(timeline); return }
  const keepNow = live.value && timeline.follow && atNow(timeline)
  zoomTo(timeline, id, keepNow ? props.data!.now! : timeline.T, keepNow ? LIVE_AT : undefined)
}
function presetKey(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  const ids = presets.value.map(p => p.id), index = Math.max(0, ids.indexOf(preset.value ?? 'fit'))
  const next = { ArrowLeft: index - 1, ArrowUp: index - 1, ArrowRight: index + 1, ArrowDown: index + 1, Home: 0, End: ids.length - 1 }[event.key]
  if (next === undefined) return
  event.preventDefault()
  const id = ids[Math.max(0, Math.min(ids.length - 1, next))]!
  zoom(id)
  const group = event.currentTarget as HTMLElement
  requestAnimationFrame(() => group.querySelector<HTMLButtonElement>(`[data-zoom="${id}"]`)?.focus())
}

// ---------- The hint line: the selected step, else how to use the lanes ----------
const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || '')
const hint = computed(() => hintParts(text.value.hint))
// Phones have no wheel and no arrow keys: the first three parts of the same hint, whole.
const touchHint = computed(() => text.value.hint.split(' · ').slice(0, 3).join(' · '))
const readout = computed(() => {
  const item = selected.value, data = props.data
  if (!item || !data) return null
  const s = item.step, t = text.value, run = item.run
  const kind = s.incident ? t.kinds.incident : t.kinds[s.kind]
  const actor = t.actors[props.level][s.lane]
  const fut = live.value && !run.isTarget && s.start >= data.now! ? ` · ${t.expected}` : ''
  return { tag: run.tag, rest: ` · ${pick(props.level === 'simple' ? s.simple : s.expert, props.lang)} · ${actor} · ${at(s.start)}–${at(s.end)} (${minutesText(s.end - s.start)}) · ${kind}${fut}` }
})
function select(item: LaneItem | null) { selected.value = item }

// ---------- The moment, the headline and what sits below ----------
const focusLane = ref(0)
const stageFocused = ref(false)
const focusKey = computed(() => {
  if (!stageFocused.value) return null
  let i = 0
  for (const [si, set] of sets.value.entries()) for (const lane of set.lanes) { if (i++ === focusLane.value) return `${si}:${lane}` }
  return null
})
const viewerZone = Intl.DateTimeFormat().resolvedOptions().timeZone || undefined
const ctx = computed(() => props.data ? { data: props.data, level: props.level, lang: props.lang, text: text.value, timeZone: viewerZone } : null)
const moment = computed(() => ctx.value ? momentOf({ ...ctx.value, sets: sets.value, T: timeline.T, selected: selected.value }) : null)
const head = computed(() => ctx.value ? headOf(props.mode, { ...ctx.value, reduced: reduced.value }) : null)
const rows = computed(() => ctx.value && props.mode === 'live' ? flightRows(ctx.value) : [])
const went = computed(() => ctx.value && props.mode !== 'live' ? sets.value.map(set => wentOf(set.main, ctx.value!)) : [])
// The release on screen is the first set's run (the headline's): its record sits under the card in every mode.
const releaseRecord = computed(() => ctx.value && sets.value[0] ? recordOf(sets.value[0].main, ctx.value) : null)
const emptyText = computed(() => {
  const t = text.value
  if (props.empty === 'none') return [t.none, t.noneB]
  return props.empty === 'live' ? [t.nothingLive, t.nothingLiveB] : props.empty === 'release' ? [t.noRelease, ''] : [t.noRuns, '']
})
</script>

<template>
  <div class="fl" :data-mode="mode">
    <div class="fl-top">
      <span class="seg fl-modes" role="radiogroup" :aria-label="text.modesLabel" data-testid="flow-modes" @keydown="modeKey">
        <button v-for="m in modes" :key="m.id" type="button" role="radio" :data-mode="m.id" :aria-checked="mode === m.id" :tabindex="mode === m.id ? 0 : -1"
          @click="mode !== m.id && emit('update:mode', m.id)">{{ m.label }}</button>
      </span>
      <div class="fl-legend" :aria-label="text.legend" role="group">
        <span><i class="sw work" />{{ text.lgWork }}</span>
        <span><i class="sw wait"><AppIcon name="clock" :size="9" /></i>{{ text.lgWait }}</span>
        <span><i class="sw rework" />{{ text.lgRework }}</span>
        <span><i class="sw fut" />{{ text.lgFut }}</span>
        <span><i class="sw inc"><AppIcon name="alert" :size="9" /></i>{{ text.lgInc }}</span>
        <span><i class="sw tgt" />{{ text.lgTgt }}</span>
        <span><i class="sw hand" />{{ text.lgHand }}</span>
      </div>
    </div>
    <div class="fl-head" data-testid="flow-head">
      <!-- A notice from the page (a refused save) replaces the headline in its fixed slot. -->
      <slot v-if="$slots.notice" name="notice" />
      <template v-else-if="head && data">
        <p class="big" :title="head.big.map(p => p.text).join('')"><template v-for="(part, i) in head.big" :key="i"><b v-if="part.strong">{{ part.text }}</b><template v-else>{{ part.text }}</template></template></p>
        <p v-if="head.small" class="small" :title="head.small">{{ head.small }}</p>
      </template>
    </div>

    <div v-if="data || retainBar" ref="card" class="fl-card">
      <div class="fl-bar" data-testid="flow-bar">
        <template v-if="live">
          <span class="fl-livechip" :class="{ hist: history }" data-testid="flow-chip">
            <AppIcon :name="history ? 'history' : 'pulse'" :size="14" />
            <span class="stack"><span class="shown">{{ chip }}</span><span aria-hidden="true">{{ text.live.replace('{t}', '00:00') }}</span><span aria-hidden="true">{{ text.viewing.replace('{t}', '00:00') }}</span></span>
          </span>
        </template>
        <template v-else>
          <select class="field fl-pick" :aria-label="mode === 'replay' ? text.pickRun : text.pickPair" :value="choice ?? ''" data-testid="flow-pick" @change="choose">
            <option v-for="c in choices" :key="c.id" :value="c.id">{{ c.label }}</option>
          </select>
          <button type="button" class="play" :aria-label="player.state.playing ? text.pause : text.play" :disabled="reduced || !data" :title="reduced ? text.rmNote : undefined" data-testid="flow-play" @click="toggle">
            <AppIcon :name="player.state.playing ? 'pause' : 'play'" :size="15" />
          </button>
          <span v-if="reduced" class="fl-rm" :title="text.rmNote" data-testid="flow-rm">{{ text.rmNote }}</span>
          <span v-else class="seg" role="radiogroup" :aria-label="text.speed" @keydown="speedKey">
            <button v-for="x in speeds" :key="x" type="button" role="radio" :data-speed="x" :aria-checked="player.state.speed === x" :tabindex="player.state.speed === x ? 0 : -1"
              :disabled="!data" @click="player.state.speed = x">{{ x }}{{ TIMES }}</button>
          </span>
        </template>
        <span class="fl-clock" :class="{ wide: !live }" :title="clock" data-testid="flow-clock">{{ clock }}</span>
        <span class="grow" />
        <span class="seg" role="radiogroup" :aria-label="text.zoom" @keydown="presetKey">
          <button v-for="p in presets" :key="p.id" type="button" role="radio" :data-zoom="p.id" :aria-checked="preset === p.id"
            :tabindex="preset === p.id || (preset === null && p.id === 'fit') ? 0 : -1" :disabled="!data" @click="zoom(p.id)">{{ p.label }}</button>
        </span>
        <button type="button" class="btn sm fl-follow" :disabled="!data || followOn" data-testid="flow-follow" @click="backToFollow(timeline)">
          <AppIcon :name="live ? 'pulse' : 'refresh'" :size="13" />
          <span class="stack"><span class="shown">{{ followOn ? followLabels[0] : followLabels[1] }}</span><span aria-hidden="true">{{ followLabels[0] }}</span><span aria-hidden="true">{{ followLabels[1] }}</span></span>
        </button>
      </div>
      <template v-if="data">
      <p class="fl-readout" aria-live="polite" data-testid="flow-readout">
        <template v-if="readout"><b>{{ readout.tag }}</b>{{ readout.rest }}</template>
        <template v-else>
          <span class="hint-narrow">{{ touchHint }}</span>
          <span class="hint-wide"><template v-for="(part, i) in hint" :key="i">
            <template v-if="'text' in part">{{ part.text }}</template>
            <template v-else-if="part.key === 'mod'">{{ mac ? '' : (lang === 'de' ? 'Strg' : 'Ctrl') }}<AppIcon v-if="mac" class="hk" name="command" :size="11" /><span v-if="mac" class="sr-only">Command</span></template>
            <template v-else><AppIcon class="hk" :name="({ left: 'arrow-left', right: 'arrow', up: 'arrow-up', down: 'arrow-down' } as const)[part.key]" :size="11" /></template>
          </template></span>
        </template>
      </p>
      <FlowOverview :data="data" :sets="sets" :timeline="timeline" :text="text" @scrub="player.stop()" />
      <FlowLanes v-model:focus-lane="focusLane" :data="data" :sets="sets" :timeline="timeline" :selected="selected?.step ?? null" :level="level" :lang="lang" :text="text"
        @select="select" @scrub="player.stop()" @focus="stageFocused = true" @blur="stageFocused = false" />
      <MomentPanel v-if="moment" :moment="moment" :text="text" :focus-key="focusKey" />
      </template>
      <div v-else-if="status === 'loading'" class="fl-state loading" aria-busy="true" data-testid="flow-loading">
        <span class="sr-only">{{ text.loading }}</span>
        <span class="sk ov-sk" /><span class="sk lanes-sk" /><span class="sk moment-sk" />
      </div>
      <div v-else-if="status === 'error'" class="fl-state banner err" role="alert" data-testid="flow-error">
        <AppIcon name="alert" :size="16" />
        <span class="grow"><b>{{ text.errT }}</b> {{ text.errB }}</span>
        <button type="button" class="btn sm" @click="emit('retry')"><AppIcon name="refresh" :size="14" />{{ text.retry }}</button>
      </div>
    </div>
    <div v-else-if="status === 'loading'" class="fl-card fl-state loading" aria-busy="true" data-testid="flow-loading">
      <span class="sr-only">{{ text.loading }}</span>
      <span class="sk bar-sk" /><span class="sk ov-sk" /><span class="sk lanes-sk" /><span class="sk moment-sk" />
    </div>
    <div v-else-if="status === 'error'" class="fl-card fl-state banner err" role="alert" data-testid="flow-error">
      <AppIcon name="alert" :size="16" />
      <span class="grow"><b>{{ text.errT }}</b> {{ text.errB }}</span>
      <button type="button" class="btn sm" @click="emit('retry')"><AppIcon name="refresh" :size="14" />{{ text.retry }}</button>
    </div>
    <div v-else class="fl-card fl-state banner" role="status" data-testid="flow-empty">
      <AppIcon name="info" :size="16" />
      <span class="grow"><b>{{ emptyText[0] }}</b> {{ emptyText[1] }}</span>
    </div>

    <p v-if="data && truncated" class="fl-note" role="status">{{ text.truncated }}</p>
    <ReleaseRecord v-if="data && releaseRecord" :record="releaseRecord" />
    <InFlightTable v-if="data && mode === 'live'" :rows="rows" :level="level" :text="text" />
    <TimeWent v-else-if="data && went.length" :runs="went" :text="text" />
  </div>
</template>

<style scoped>
.fl { margin-top: 14px; }
.fl-top { display: flex; align-items: center; gap: 10px 18px; flex-wrap: wrap; min-height: 32px; }
.fl-modes button { white-space: nowrap; }
.fl-legend { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 14px; margin-left: auto; font-size: 12px; color: var(--ink-2); }
.fl-legend > span { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.sw { display: inline-grid; place-items: center; width: 18px; height: 11px; border-radius: 3px; }
.sw.work { background: color-mix(in srgb, var(--teal) 22%, transparent); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--teal) 55%, transparent); }
.sw.wait { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); }
.sw.rework { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.sw.fut { box-shadow: inset 0 0 0 1px var(--ink-3); background: transparent; }
.sw.inc { background: color-mix(in srgb, var(--danger) 14%, transparent); box-shadow: inset 0 0 0 1px var(--danger); color: var(--danger); }
.sw.tgt { height: 5px; background: color-mix(in srgb, var(--ok) 55%, transparent); }
.sw.hand { width: 9px; height: 9px; border-radius: 50%; background: var(--surface-raised); box-shadow: inset 0 0 0 1.5px var(--ink-2); }
/* The headline keeps one height (two lines and one), so the card below never moves while the words change. */
.fl-head { height: 66px; margin-top: 12px; overflow: hidden; }
.fl-head p { margin: 0; }
.fl-head .big { font: 500 15.5px/22px var(--font); color: var(--ink); display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.fl-head .big b { font-weight: 650; }
.fl-head .small { margin-top: 2px; font-size: 13px; line-height: 20px; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fl-card { margin-top: 10px; padding: 6px 16px 12px; border-radius: 16px; background: var(--glass); -webkit-backdrop-filter: blur(14px) saturate(1.3); backdrop-filter: blur(14px) saturate(1.3); box-shadow: 0 0 0 1px var(--line), inset 0 1px 0 var(--glass-edge), 0 14px 34px -22px color-mix(in srgb, var(--primary-line) 35%, transparent); }
.fl-bar { display: flex; align-items: center; gap: 10px; height: 48px; border-bottom: 1px solid var(--line); }
.fl-bar .grow { flex: 1; }
.fl-bar .seg button { white-space: nowrap; }
.fl-pick { width: clamp(180px, 22vw, 300px); height: 32px; padding: 0 10px; font-size: 13px; }
.play { display: inline-grid; place-items: center; flex: none; width: 36px; height: 36px; padding: 0; border: 1px solid var(--glass-edge); border-radius: 999px; background: var(--btn-bg); box-shadow: var(--shadow-btn); color: var(--teal-ink); }
.play:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.play:disabled { opacity: .5; }
.fl-rm { flex: 0 1 auto; max-width: 340px; font-size: 12px; color: var(--ink-3); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
/* A label slot as wide as its longest wording: switching words never moves a neighbour. */
.stack { display: inline-grid; }
.stack > span { grid-area: 1 / 1; white-space: nowrap; }
.stack > span[aria-hidden] { visibility: hidden; }
.fl-livechip { display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 10px; border-radius: 999px; background: color-mix(in srgb, var(--teal) 10%, transparent); color: var(--teal-ink); font-size: 12.5px; font-weight: 600; white-space: nowrap; font-variant-numeric: tabular-nums; }
.fl-livechip.hist { background: var(--surface-sunken); color: var(--ink-2); }
.fl-clock { min-width: 0; font: 500 12px var(--mono); color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fl-clock.wide { flex: 0 1 200px; }
.fl-follow { flex: none; gap: 6px; }
.fl-readout { height: 20px; margin: 4px 0 0; font-size: 12px; line-height: 20px; color: var(--ink-3); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fl-readout b { color: var(--ink); font-weight: 600; }
.hk { display: inline-block; vertical-align: -1px; margin: 0 1px; }
.hint-narrow { display: none; }
.fl-state { display: grid; gap: 8px; }
.fl-state.loading { padding-top: 12px; }
.sk { display: block; border-radius: 8px; background: var(--skeleton); }
.bar-sk { height: 36px; }
.ov-sk { height: 64px; }
.lanes-sk { height: 330px; }
.moment-sk { height: 196px; }
@media (prefers-reduced-motion: no-preference) {
  .sk { background: linear-gradient(90deg, var(--skeleton) 0%, var(--skeleton-hi) 50%, var(--skeleton) 100%) 0 0 / 200% 100%; animation: fl-sk 1.4s ease-in-out infinite; }
}
@keyframes fl-sk { to { background-position: -200% 0; } }
.banner { display: flex; align-items: center; gap: 12px; padding: 12px 14px; font-size: 13px; color: var(--ink-2); }
.banner svg { flex: none; color: var(--ink-2); }
.banner b { font-weight: 600; color: var(--ink); }
.banner .grow { flex: 1; min-width: 0; }
.banner.err { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.banner.err svg { color: var(--danger); }
.banner .btn { flex: none; gap: 6px; }
.fl-note { margin: 8px 0 0; font-size: 12px; color: var(--ink-3); }
@container delivery (max-width: 640px) {
  .fl-top { gap: 10px; }
  .fl-modes { width: 100%; }
  .fl-modes button { flex: 1; min-height: 44px; }
  .fl-legend { margin-left: 0; gap: 4px 12px; }
  .fl-head { height: 108px; }
  .fl-head .big { font-size: 14.5px; line-height: 21px; -webkit-line-clamp: 3; }
  .fl-head .small { white-space: normal; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
  .fl-card { padding: 6px 10px 10px; }
  .fl-bar { flex-wrap: wrap; height: auto; gap: 8px; padding: 8px 0; }
  .fl-bar .grow { display: none; }
  .fl-bar .seg button { height: 44px; min-width: 44px; }
  .fl-pick { flex: 1 1 100%; width: auto; height: 44px; }
  .play { width: 44px; height: 44px; }
  .fl-rm { flex: 1 1 140px; width: auto; white-space: normal; }
  .fl-follow { min-height: 44px; flex: 1 1 auto; justify-content: center; }
  .fl-livechip { min-height: 32px; }
  .fl-clock, .fl-clock.wide { flex: 1 1 120px; }
  .fl-readout { height: 51px; white-space: normal; line-height: 17px; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; }
  .hint-narrow { display: inline; }
  .hint-wide { display: none; }
  .lanes-sk { height: 330px; }
  .moment-sk { height: 300px; }
  .banner { flex-wrap: wrap; }
  .banner .btn { min-height: 44px; }
}
</style>
