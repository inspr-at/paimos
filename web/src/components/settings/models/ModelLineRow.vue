<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import AppIcon from '../../AppIcon.vue'
import HarnessMark from '../../agents/HarnessMark.vue'
import { providerLabel } from '../../../lib/modelsSimple'
import {
  HARNESS_NAME, SOURCE_NAME, SOURCE_TIP, levelUnit, lifecycleSource, lifecycleText, olderWhy, pauseTag, routeOf, shortDate, takenText, untilText,
  type FactSource, type RegistryModel, type RegistryRoute, type RegistryRow,
} from '../../../lib/modelRegistry'

// Settings › Models › Model registry, one line (AEON-1163): a row that opens in place, folded by default. The fold
// shows the routes that run this version (harness, id, native levels, source), the provider, lifecycle and note facts
// with their source or "unknown", the older versions, and for admins Pause…, Remove… and Edit for routes added by
// hand. The fold grows below its row, so nothing above it moves (AEON-541); a remove question grows under the actions.
// A pause, a resume or its Undo never moves the actions either: Pause… keeps one box for every label it can show,
// and how each route is paused, when the routes differ, is said below the actions.
const props = defineProps<{ row: RegistryRow; open: boolean; manage: boolean; now: Date; editing: string | null; pausing: boolean; busy: boolean }>()
const emit = defineEmits<{ toggle: []; edit: [route: RegistryRoute, trigger: HTMLElement]; pause: [trigger: HTMLElement]; remove: [trigger: HTMLElement] }>()
const uid = useId()
const model = computed(() => props.row.model)
const first = computed(() => model.value.routes[0])
// The body stays rendered once opened, so closing can fold it away; a closed fold is inert.
const rendered = ref(props.open)
watch(() => props.open, open => { if (open) rendered.value = true })

const SOURCE_ICON: Record<FactSource, 'box' | 'server' | 'terminal' | 'edit'> = { shipped: 'box', provider: 'server', harness: 'terminal', manual: 'edit' }
const routeCount = computed(() => model.value.routes.length)
const meta = computed(() => [routeCount.value > 1 ? `${routeCount.value} routes` : first.value ? HARNESS_NAME[first.value.harness] : '', model.value.note].filter(Boolean).join(' · '))
const retiring = computed(() => model.value.retireAt ? `Retiring ${shortDate(model.value.retireAt, props.now)}` : '')
const paused = computed(() => pauseTag(model.value, props.now))
const older = computed(() => props.row.older.length)
const lifecycle = computed(() => lifecycleText(model.value.lifecycle, props.now))
const lifecycleFrom = computed(() => model.value.lifecycle ? lifecycleSource(model.value.lifecycle.source) : null)
const why = computed(() => model.value.lifecycle?.state === 'retiring' && model.value.lifecycle.reason ? model.value.lifecycle.reason : '')
const took = computed(() => takenText(model.value, props.now))
const hand = computed(() => model.value.routes.filter(route => route.hand))
const pauseLabel = computed(() => paused.value && model.value.routes.every(route => route.pause) ? paused.value : 'Pause…')
// The widest label a pause from the menu gives ("Paused until tomorrow, 04:00"), held invisibly in the same box.
const PAUSE_WIDEST = 'Paused until tomorrow, 00:00'
// Routes paused unlike the version as a whole. A version paused alike everywhere says so on its row and on Pause….
const routePauses = computed(() => {
  const routes = model.value.routes, until = routes[0]?.pause?.until
  return routes.every(route => route.pause && !route.partlyPaused && route.pause.until === until) ? [] : routes.filter(route => route.pause || route.partlyPaused)
})
const routePause = (route: RegistryRoute) => route.pause ? `${HARNESS_NAME[route.harness]} paused ${untilText(route.pause.until, props.now)}` : `Some ${HARNESS_NAME[route.harness]} levels are paused`
const via = (route: RegistryRoute) => route.via && route.via !== HARNESS_NAME[route.harness] ? route.via : ''
const routesOf = (version: RegistryModel) => version.routes.map(route => HARNESS_NAME[route.harness]).filter((name, index, all) => all.indexOf(name) === index).join(', ')
const olderSource = (version: RegistryModel) => version.lifecycle ? lifecycleSource(version.lifecycle.source) : null
</script>

<template>
  <div class="ln" :data-reg-line="row.key">
    <button type="button" class="ln-row" :aria-expanded="open" :aria-controls="`${uid}-fold`" :data-reg-fold="row.key" @click="emit('toggle')">
      <span class="ln-mark" aria-hidden="true"><HarnessMark v-if="first" :harness="first.harness" :provider="routeOf(first.harness, first.models[0] ?? '')" :size="13" /></span>
      <span class="ln-main">
        <span class="ln-n">
          <span class="ln-name">{{ model.lineName }}</span>
          <span v-if="model.versionLabel" class="ln-v">{{ model.versionLabel }}</span>
          <span v-if="model.source" class="srcb sm" :data-tip="SOURCE_TIP[model.source]"><AppIcon :name="SOURCE_ICON[model.source]" :size="11" />{{ SOURCE_NAME[model.source] }}</span>
          <span v-else class="srcb sm unk" data-tip="No provider or harness has reported where this model comes from.">unknown</span>
          <template v-if="model.isNew"><span class="tag-new">New</span><span class="tag-off">off until used</span></template>
          <span v-else-if="retiring" class="tag-ret"><AppIcon name="clock" :size="12" />{{ retiring }}</span>
          <span v-if="older" class="tag-oldn">+{{ older }} older</span>
        </span>
        <!-- One line, like the design's: the pause tag leads it, so pausing never adds a line above the actions (AEON-541).
             The routes and the note it shortens are in the fold in full. -->
        <span v-if="meta || paused" class="ln-m"><span v-if="paused" class="tag-pause" data-reg-paused><AppIcon name="pause" :size="12" />{{ paused }}</span><span v-if="meta" class="ln-mt">{{ meta }}</span></span>
      </span>
      <span v-if="first" class="lv" :aria-label="`Levels: ${first.efforts.join(', ')}`"><span v-for="level in first.efforts" :key="level">{{ level }}</span></span>
      <AppIcon name="chevron" :size="14" class="ln-chev" />
    </button>
    <div :id="`${uid}-fold`" class="fold" :class="{ open }" role="region" :aria-label="model.name" :inert="!open || undefined" data-reg-body>
      <div class="fold-in">
        <div v-if="rendered" class="ln-body">
          <div class="rts" role="list" aria-label="Routes">
            <div v-for="route in model.routes" :key="route.key" class="rt-r" role="listitem" :data-reg-route="`${route.harness}:${route.id}`">
              <span class="rt-mark" aria-hidden="true"><HarnessMark :harness="route.harness" :provider="routeOf(route.harness, route.models[0] ?? '')" :size="12" /></span>
              <span class="rt-id"><span class="rt-h">{{ HARNESS_NAME[route.harness] }}<small v-if="via(route)"> via {{ via(route) }}</small></span><code class="rt-s">{{ route.id }}</code></span>
              <span class="lv" :aria-label="`Levels: ${route.efforts.join(', ')}`"><span v-for="level in route.efforts" :key="level">{{ level }}</span><small v-if="levelUnit(route)" class="lv-u">{{ levelUnit(route) }}</small></span>
              <span v-if="route.source" class="srcb sm" :data-tip="SOURCE_TIP[route.source]"><AppIcon :name="SOURCE_ICON[route.source]" :size="11" />{{ SOURCE_NAME[route.source] }}</span>
              <span v-else class="srcb sm unk" data-tip="No provider or harness has reported this route.">unknown</span>
              <span v-if="route.models.length > 1" class="rt-note">{{ route.models.length }} ids in {{ HARNESS_NAME[route.harness] }}, one per level</span>
            </div>
          </div>
          <div class="facts">
            <div class="fact"><span class="fact-k">Provider</span><span class="fact-v"><span v-if="model.provider === 'unknown'" class="unk">unknown</span><template v-else>{{ providerLabel(model.provider) }}</template></span></div>
            <div class="fact" data-reg-lifecycle>
              <span class="fact-k">Lifecycle</span>
              <span class="fact-v">
                <template v-if="lifecycle">{{ lifecycle }}<template v-if="why"> · {{ why }}</template>
                  <span v-if="lifecycleFrom" class="srcb sm" :data-tip="SOURCE_TIP[lifecycleFrom]"><AppIcon :name="SOURCE_ICON[lifecycleFrom]" :size="11" />{{ SOURCE_NAME[lifecycleFrom] }}</span>
                </template>
                <span v-else><span class="unk">unknown</span><small> · the provider reports none</small></span>
              </span>
            </div>
            <div v-if="took" class="fact"><span class="fact-k">Version</span><span class="fact-v">{{ model.versionLabel }} · {{ took }}</span></div>
            <div v-if="model.note" class="fact"><span class="fact-k">Note</span><span class="fact-v"><span>{{ model.note }}<small> · shown under its name in every picker</small></span></span></div>
            <div v-if="row.older.length" class="fact" data-reg-older>
              <span class="fact-k">Older versions</span>
              <span class="fact-v ov">
                <span v-for="version in row.older" :key="version.key" class="ov-i" :data-reg-older-version="version.key">
                  <span v-if="version.routes[0]" class="ov-mark" aria-hidden="true"><HarnessMark :harness="version.routes[0].harness" :provider="routeOf(version.routes[0].harness, version.routes[0].models[0] ?? '')" :size="11" /></span>
                  <b>{{ version.name }}</b>
                  <span class="ov-t">{{ routesOf(version) }} · {{ olderWhy(version, model.name, now) }}</span>
                  <span v-if="olderSource(version)" class="srcb sm" :data-tip="SOURCE_TIP[olderSource(version)!]"><AppIcon :name="SOURCE_ICON[olderSource(version)!]" :size="11" />{{ SOURCE_NAME[olderSource(version)!] }}</span>
                  <span v-if="version.routes[0]" class="lv ov-lv" :aria-label="`Levels: ${version.routes[0].efforts.join(', ')}`"><span v-for="level in version.routes[0].efforts" :key="level">{{ level }}</span></span>
                </span>
              </span>
            </div>
          </div>
          <div v-if="manage" class="ln-acts">
            <button v-for="route in hand" :key="route.key" type="button" class="btn sm ghost" data-reg-edit :aria-expanded="editing === route.lineKeys[0]" :disabled="busy" @click="emit('edit', route, $event.currentTarget as HTMLElement)"><AppIcon name="edit" :size="13" />Edit {{ HARNESS_NAME[route.harness] }} route</button>
            <button type="button" class="btn sm ghost ln-pause" data-reg-pause aria-haspopup="menu" :aria-expanded="pausing" :disabled="busy" @click="emit('pause', $event.currentTarget as HTMLElement)">
              <span class="btn-label">
                <span :aria-hidden="pauseLabel !== 'Pause…'"><AppIcon name="pause" :size="13" />Pause…</span>
                <span v-if="pauseLabel !== 'Pause…'"><AppIcon name="pause" :size="13" />{{ pauseLabel }}</span>
                <span aria-hidden="true"><AppIcon name="pause" :size="13" />{{ PAUSE_WIDEST }}</span>
              </span>
            </button>
            <span class="grow" />
            <button type="button" class="btn sm ghost danger-t" data-reg-remove :disabled="busy" @click="emit('remove', $event.currentTarget as HTMLElement)"><AppIcon name="trash" :size="13" />Remove…</button>
          </div>
          <slot />
          <ul v-if="routePauses.length" class="ln-pauses" aria-label="Paused routes" data-reg-route-pauses>
            <li v-for="route in routePauses" :key="route.key" :data-reg-route-pause="`${route.harness}:${route.id}`"><AppIcon name="pause" :size="12" />{{ routePause(route) }}</li>
          </ul>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ln { border-bottom: 1px solid var(--line); }
.ln-row { display: grid; grid-template-columns: 22px minmax(0, 1fr) auto 16px; align-items: center; column-gap: 14px; width: 100%; min-height: 60px; padding: 10px 16px; border: 0; border-radius: 10px; background: transparent; color: var(--ink); font: inherit; text-align: left; cursor: pointer; }
.ln-row:hover { background: var(--row-hover); }
.ln-row:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.ln-mark, .rt-mark, .ov-mark { display: grid; place-items: center; flex: none; width: 22px; height: 22px; border-radius: 7px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); }
.rt-mark { width: 20px; height: 20px; border-radius: 6px; }
.ov-mark { width: 18px; height: 18px; border-radius: 5px; }
.ln-main { display: grid; min-width: 0; }
.ln-n { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; font-size: 14px; font-weight: 600; }
.ln-name { overflow-wrap: anywhere; }
.ln-v { padding: 1px 7px; border-radius: 6px; background: var(--surface-sunken); font: 600 11.5px/1.5 var(--mono); color: var(--ink); }
.ln-m { display: flex; align-items: center; gap: 10px; min-width: 0; overflow: hidden; white-space: nowrap; font-size: 12.5px; line-height: 1.5; color: var(--ink-3); }
.ln-m .tag-pause { flex: none; font-weight: 500; }
.ln-mt { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.ln-chev { color: var(--ink-3); transition: transform .2s ease; }
.ln-row[aria-expanded="true"] .ln-chev { transform: rotate(180deg); }
.lv { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 4px; max-width: 240px; }
.lv span { padding: 2px 7px; border-radius: 999px; background: var(--surface-sunken); font: 500 11.5px/1.5 var(--mono); color: var(--ink-2); }
.lv-u { align-self: center; font-size: 11px; color: var(--ink-3); }
.tag-new { font: 600 9.5px/1 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); padding: 4px 5px; border-radius: 6px; }
.tag-off, .tag-oldn { font-size: 12px; font-weight: 500; color: var(--ink-3); }
.tag-ret, .tag-pause { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; font-weight: 500; color: var(--warn-ink); }
.tag-pause { color: var(--ink-2); }
/* Where a fact came from: an outlined word, never a coloured edge. "unknown" is plain muted text (no italics, GUI-27 #12). */
.srcb { display: inline-flex; align-items: center; gap: 4px; flex: none; height: 22px; padding: 0 8px; border-radius: 999px; box-shadow: inset 0 0 0 1px var(--line-2); font-size: 11.5px; font-weight: 500; color: var(--ink-2); white-space: nowrap; cursor: help; }
.srcb :deep(svg) { color: var(--ink-3); }
.srcb.sm { height: 20px; padding: 0 7px; font-size: 11px; }
.srcb.unk, .unk { color: var(--ink-3); }

.fold { display: grid; grid-template-rows: 0fr; transition: grid-template-rows .24s ease; }
.fold.open { grid-template-rows: 1fr; }
.fold-in { min-height: 0; overflow: hidden; }
@media (prefers-reduced-motion: reduce) { .fold, .ln-chev { transition: none; } }
.ln-body { padding: 2px 16px 16px 52px; }
.rts { display: grid; border-radius: 12px; background: var(--surface-sunken); padding: 4px 10px; }
.rt-r { display: grid; grid-template-columns: 22px minmax(0, 1fr) auto auto; align-items: center; column-gap: 12px; row-gap: 2px; min-height: 48px; padding: 7px 0; }
.rt-r + .rt-r { border-top: 1px solid var(--line); }
.rt-id { display: grid; min-width: 0; }
.rt-h { font-size: 13px; font-weight: 600; color: var(--ink); white-space: nowrap; }
.rt-h small { font-weight: 500; color: var(--ink-3); font-size: 12px; }
.rt-s { min-width: 0; overflow-wrap: anywhere; font: 500 11.5px/1.4 var(--mono); color: var(--ink-2); }
.rt-r .lv span { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); }
.rt-note { grid-column: 2 / -1; display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--ink-3); }
.facts { display: grid; gap: 2px; margin-top: 10px; }
.fact { display: grid; grid-template-columns: 116px minmax(0, 1fr); column-gap: 12px; align-items: baseline; padding: 4px 0; font-size: 12.5px; }
.fact-k { color: var(--ink-3); }
.fact-v { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; min-width: 0; color: var(--ink); overflow-wrap: anywhere; }
.fact-v small { color: var(--ink-3); font-size: 12px; }
.ov { display: grid; gap: 6px; }
.ov-i { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; }
.ov-i b { font-weight: 600; }
.ov-t { color: var(--ink-3); }
.ov-lv { justify-content: flex-start; max-width: none; }
.ln-acts { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--line); }
.ln-acts .grow { flex: 1; }
.ln-acts .btn { white-space: normal; }
.ln-pause .btn-label { justify-items: start; font-variant-numeric: tabular-nums; }
.ln-pause .btn-label > span { display: inline-flex; align-items: center; gap: 7px; }
.ln-pauses { display: grid; gap: 4px; margin: 8px 0 0; padding: 0; list-style: none; }
.ln-pauses li { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--warn-ink); }
.btn.ghost.danger-t { color: var(--danger); }

@container (max-width: 600px) {
  .ln-row { grid-template-columns: 22px minmax(0, 1fr) 16px; padding: 10px 12px; }
  .ln-row .lv { display: none; }
  .ln-mark { align-self: start; margin-top: 1px; }
  .ln-body { padding: 2px 12px 14px; }
  .rts { padding: 2px 10px; }
  .rt-r { grid-template-columns: 22px minmax(0, 1fr) auto; }
  .rt-r .rt-mark { grid-row: 1; }
  .rt-r .lv { grid-column: 2 / -1; grid-row: 2; justify-content: flex-start; max-width: none; }
  .rt-r .srcb { grid-column: 2 / -1; grid-row: 3; justify-self: start; }
  .rt-note { grid-column: 2 / -1; }
  .ln-acts .grow { display: none; }
  .fact { grid-template-columns: minmax(0, 1fr); }
}
@media (pointer: coarse) { .ln-row { min-height: 44px; } }
</style>
<style scoped src="../../../styles/settingsButtons.css"></style>
