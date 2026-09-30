<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { formatEta, type EtaInput, type EtaMode } from '../../lib/eta'
import { usePreference } from '../../lib/preferences'
import AppIcon from '../AppIcon.vue'

// One compact estimate: the headline time (ready, else live) flush to the column's
// edge, percent done as a quiet figure before it, grey with a clock when
// the report went stale, the warning tone once the time has passed. The tooltip
// carries both forms, who estimated and when. Working sessions without a time
// keep a quiet hint; other empty cells stay empty.
const props = withDefaults(defineProps<{ eta: EtaInput | null; now: number; align?: 'end' | 'start'; labelled?: boolean; missing?: boolean }>(), { align: 'end', labelled: false, missing: false })
const pref = usePreference<{ mode?: EtaMode }>('eta-display')
const mode = computed<EtaMode>(() => pref.value.value?.mode === 'clock' || pref.value.value?.mode === 'both' ? pref.value.value.mode : 'relative')
const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
const view = computed(() => formatEta(props.eta, mode.value, props.now, zone))
const tip = computed(() => [view.value?.tip, props.missing && !view.value?.text ? 'No ETA reported for this working session' : null].filter(Boolean).join('\n'))
// The kind is named when asked for, or when the headline is the live time (a
// column of ready times would otherwise read it as one).
const kind = computed(() => view.value?.text && (props.labelled || view.value.kind === 'Live') ? view.value.kind : null)
</script>

<template>
  <span v-if="view" class="eta-cell" :class="[align, { stale: view.stale, overdue: view.overdue && !view.stale }]" :data-tip="tip">
    <span class="sr-only">{{ tip.replace(/\n/g, '. ') }}</span>
    <span v-if="view.progress" class="pct" aria-hidden="true">{{ view.progress }}</span>
    <span class="main" aria-hidden="true">
      <AppIcon v-if="view.stale" name="clock" :size="12" class="stale-icon" />
      <span v-if="kind" class="kind">{{ kind }}</span>
      <span v-if="view.text" class="when">
        <span class="shown">{{ view.text }}</span>
        <span v-if="view.hover" class="hover">{{ view.hover }}</span>
      </span>
      <span v-else-if="missing" class="missing">no ETA</span>
    </span>
  </span>
  <span v-else-if="missing" class="eta-cell missing" :class="align" data-tip="No ETA reported for this working session">no ETA</span>
</template>

<style scoped>
.eta-cell { display: inline-flex; align-items: center; gap: 6px; min-width: 0; max-width: 100%; color: var(--ink); font-size: 12.5px; line-height: 18px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.eta-cell.end { justify-content: flex-end; }
.main { display: inline-flex; align-items: center; gap: 5px; min-width: 0; }
.kind { flex: none; font-size: 11.5px; color: var(--ink-3); }
.when { display: inline-grid; min-width: 0; }
.when > span { grid-area: 1 / 1; overflow: hidden; text-overflow: ellipsis; }
.eta-cell.end .when > span { text-align: right; }
.hover { visibility: hidden; }
.eta-cell:hover .when:has(.hover) .shown { visibility: hidden; }
.eta-cell:hover .hover { visibility: visible; }
.pct { flex: none; font: 500 11px/1 var(--mono); color: var(--ink-3); font-variant-ligatures: none; }
.missing { color: var(--ink-3); font-size: 11.5px; }
/* Start-aligned (inside another cell): the percent follows the time and is the
   first thing to go when the line is short; the tooltip still carries it. */
.eta-cell.start { flex-wrap: wrap; height: 18px; overflow: hidden; }
.eta-cell.start .pct { order: 3; }
.eta-cell.overdue .when { color: var(--warn); font-weight: 550; }
.eta-cell.stale, .eta-cell.stale .kind, .eta-cell.stale .pct { color: var(--ink-3); }
.stale-icon { flex: none; color: var(--ink-3); }
</style>
