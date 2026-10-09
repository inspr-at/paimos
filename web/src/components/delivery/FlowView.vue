<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Delivery › Flow, A Lanes (AEON-994 draft 5, package 5): one card with a control
// bar (48 px), a hint line (20 px), the overview map (64 px) and the lanes (330 px),
// all fixed, so zooming, panning and moving the time never move a control. The
// moment panel, modes and the tables below follow in package 6.
import { computed, onMounted, ref, shallowRef, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import FlowLanes from './FlowLanes.vue'
import FlowOverview from './FlowOverview.vue'
import type { DeliveryLanguage } from '../../lib/delivery'
import {
  atNow, backToFollow, createTimeline, fitAll, following, laneModel, minutesText, pick, resetTimeline, timeLabel, zoomPreset, zoomTo, ZOOMS, LIVE_AT,
  type FlowData, type FlowLevel, type LaneItem,
} from '../../lib/deliveryFlow'
import { flowText, hintParts } from '../../lib/deliveryFlowText'

const props = defineProps<{ data: FlowData; level: FlowLevel; lang: DeliveryLanguage }>()
const text = computed(() => flowText(props.lang))
const sets = computed(() => laneModel(props.data))
const timeline = createTimeline()
const card = ref<HTMLElement>()
// The selection belongs to this data set; new data clears it.
const selected = shallowRef<LaneItem | null>(null)
function reset() {
  resetTimeline(timeline, props.data, { narrow: (card.value?.clientWidth ?? 1000) < 640 })
  selected.value = null
}
onMounted(reset)
watch(() => props.data, reset)

const live = computed(() => props.data.now != null)
const at = (m: number) => timeLabel(props.data, m)
const history = computed(() => live.value && !atNow(timeline))
const chip = computed(() => history.value ? text.value.viewing.replace('{t}', at(timeline.T)) : text.value.live.replace('{t}', at(props.data.now ?? 0)))
const clock = computed(() => {
  const t = text.value, base = t.at.replace('{t}', at(timeline.T))
  if (!live.value) return base
  if (atNow(timeline)) return `${base} (${t.now})`
  return timeline.T > props.data.now! ? `${base} · ${t.expected}` : base
})
const followOn = computed(() => following(timeline))
const followLabels = computed(() => live.value ? [text.value.followingNow, text.value.backNow] : [text.value.following, text.value.follow])

// ---------- Zoom presets: Fit all · 15 · 30 · 1 h ----------
const presets = computed(() => [{ id: 'fit' as const, label: text.value.fit }, ...ZOOMS.map(z => ({ id: z, label: text.value.zooms[z]! }))])
const preset = computed(() => zoomPreset(timeline))
function choose(id: 'fit' | typeof ZOOMS[number]) {
  if (id === 'fit') { fitAll(timeline); return }
  const keepNow = live.value && timeline.follow && atNow(timeline)
  zoomTo(timeline, id, keepNow ? props.data.now! : timeline.T, keepNow ? LIVE_AT : undefined)
}
function presetKey(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  const ids = presets.value.map(p => p.id), index = Math.max(0, ids.indexOf(preset.value ?? 'fit'))
  const next = { ArrowLeft: index - 1, ArrowUp: index - 1, ArrowRight: index + 1, ArrowDown: index + 1, Home: 0, End: ids.length - 1 }[event.key]
  if (next === undefined) return
  event.preventDefault()
  const id = ids[Math.max(0, Math.min(ids.length - 1, next))]!
  choose(id)
  const group = event.currentTarget as HTMLElement
  requestAnimationFrame(() => group.querySelector<HTMLButtonElement>(`[data-zoom="${id}"]`)?.focus())
}

// ---------- The hint line: the selected step, else how to use the lanes ----------
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const hint = computed(() => hintParts(text.value.hint))
// Phones have no wheel and no arrow keys: the first three parts of the same hint, whole.
const touchHint = computed(() => text.value.hint.split(' · ').slice(0, 3).join(' · '))
const readout = computed(() => {
  const item = selected.value
  if (!item) return null
  const s = item.step, t = text.value, run = item.run
  const kind = s.incident ? t.kinds.incident : t.kinds[s.kind]
  const actor = t.actors[props.level][s.lane]
  const fut = live.value && !run.isTarget && s.start >= props.data.now! ? ` · ${t.expected}` : ''
  return { tag: run.tag, rest: ` · ${pick(props.level === 'simple' ? s.simple : s.expert, props.lang)} · ${actor} · ${at(s.start)}–${at(s.end)} (${minutesText(s.end - s.start)}) · ${kind}${fut}` }
})
function select(item: LaneItem | null) { selected.value = item }
</script>

<template>
  <div class="fl">
    <div class="fl-top">
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
    <div ref="card" class="fl-card">
      <div class="fl-bar" data-testid="flow-bar">
        <span v-if="live" class="fl-livechip" :class="{ hist: history }" data-testid="flow-chip">
          <AppIcon :name="history ? 'history' : 'pulse'" :size="14" />
          <span class="stack"><span class="shown">{{ chip }}</span><span aria-hidden="true">{{ text.live.replace('{t}', '00:00') }}</span><span aria-hidden="true">{{ text.viewing.replace('{t}', '00:00') }}</span></span>
        </span>
        <span class="fl-clock" data-testid="flow-clock">{{ clock }}</span>
        <span class="grow" />
        <span class="seg" role="radiogroup" :aria-label="text.zoom" @keydown="presetKey">
          <button v-for="p in presets" :key="p.id" type="button" role="radio" :data-zoom="p.id" :aria-checked="preset === p.id"
            :tabindex="preset === p.id || (preset === null && p.id === 'fit') ? 0 : -1" @click="choose(p.id)">{{ p.label }}</button>
        </span>
        <button type="button" class="btn sm fl-follow" :disabled="followOn" data-testid="flow-follow" @click="backToFollow(timeline)">
          <AppIcon :name="live ? 'pulse' : 'refresh'" :size="13" />
          <span class="stack"><span class="shown">{{ followOn ? followLabels[0] : followLabels[1] }}</span><span aria-hidden="true">{{ followLabels[0] }}</span><span aria-hidden="true">{{ followLabels[1] }}</span></span>
        </button>
      </div>
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
      <FlowOverview :data="data" :sets="sets" :timeline="timeline" :text="text" />
      <FlowLanes :data="data" :sets="sets" :timeline="timeline" :selected="selected?.step ?? null" :level="level" :lang="lang" :text="text" @select="select" />
    </div>
  </div>
</template>

<style scoped>
.fl { margin-top: 14px; }
.fl-top { display: flex; align-items: center; gap: 10px 18px; flex-wrap: wrap; min-height: 32px; }
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
.fl-card { margin-top: 10px; padding: 6px 16px 12px; border-radius: 16px; background: var(--glass); -webkit-backdrop-filter: blur(14px) saturate(1.3); backdrop-filter: blur(14px) saturate(1.3); box-shadow: 0 0 0 1px var(--line), inset 0 1px 0 var(--glass-edge), 0 14px 34px -22px color-mix(in srgb, var(--primary-line) 35%, transparent); }
.fl-bar { display: flex; align-items: center; gap: 10px; height: 48px; border-bottom: 1px solid var(--line); }
.fl-bar .grow { flex: 1; }
.fl-bar .seg button { white-space: nowrap; }
/* A label slot as wide as its longest wording: switching words never moves a neighbour. */
.stack { display: inline-grid; }
.stack > span { grid-area: 1 / 1; white-space: nowrap; }
.stack > span[aria-hidden] { visibility: hidden; }
.fl-livechip { display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 10px; border-radius: 999px; background: color-mix(in srgb, var(--teal) 10%, transparent); color: var(--teal-ink); font-size: 12.5px; font-weight: 600; white-space: nowrap; font-variant-numeric: tabular-nums; }
.fl-livechip.hist { background: var(--surface-sunken); color: var(--ink-2); }
.fl-clock { min-width: 0; font: 500 12px var(--mono); color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fl-follow { flex: none; gap: 6px; }
.fl-readout { height: 20px; margin: 4px 0 0; font-size: 12px; line-height: 20px; color: var(--ink-3); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fl-readout b { color: var(--ink); font-weight: 600; }
.hk { display: inline-block; vertical-align: -1px; margin: 0 1px; }
.hint-narrow { display: none; }
@container delivery (max-width: 640px) {
  .fl-legend { margin-left: 0; gap: 4px 12px; }
  .fl-card { padding: 6px 10px 10px; }
  .fl-bar { flex-wrap: wrap; height: auto; gap: 8px; padding: 8px 0; }
  .fl-bar .grow { display: none; }
  .fl-bar .seg button { height: 44px; }
  .fl-follow { min-height: 44px; flex: 1 1 auto; justify-content: center; }
  .fl-livechip { min-height: 32px; }
  .fl-clock { flex: 1 1 120px; }
  .fl-readout { height: 51px; white-space: normal; line-height: 17px; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; }
  .hint-narrow { display: inline; }
  .hint-wide { display: none; }
}
</style>
