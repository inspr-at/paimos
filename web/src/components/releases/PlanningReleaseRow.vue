<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { releaseName, releaseState, type PlanningRelease } from '../../lib/deliveryPlanning'
import AppIcon from '../AppIcon.vue'
const props = defineProps<{ release: PlanningRelease; expanded: boolean; scoped: boolean; disabled?: boolean; actionsAvailable?: boolean }>()
const emit = defineEmits<{ toggle: []; scope: []; menu: [anchor: HTMLElement] }>()
const name = computed(() => releaseName(props.release))
const progress = computed(() => props.release.rollup.units ? Math.round(100 * props.release.rollup.completed / props.release.rollup.units) : 0)
const outlook = computed(() => ({ fits: 'Fits the budget', does_not_fit: 'Over budget', unknown: 'Unknown' }[props.release.build_summary.budget_outlook ?? 'unknown'] ?? 'Unknown'))
const agents = computed(() => typeof props.release.build_summary.agents === 'number'
  ? `${props.release.build_summary.agents} agents · ${props.release.build_summary.waiting ?? 0} waiting` : '—')
</script>
<template>
  <div class="release-row" :class="{ expanded, scoped }" :data-release-id="release.release_id">
    <div class="release-title">
      <span class="handle-slot" aria-hidden="true" />
      <button type="button" class="chevron" :disabled="disabled" :aria-expanded="expanded" :aria-controls="`release-work-${release.release_id}`" :aria-label="`${expanded ? 'Collapse' : 'Expand'} ${name}`" @click="emit('toggle')"><AppIcon name="chevron-right" :size="14" /></button>
      <button type="button" class="release-name" :disabled="disabled" :aria-label="`Scope to ${name}`" :data-tip="`${name}${release.version ? `\n${release.version}` : ''}`" @click="emit('scope')">
        <span class="name-line"><AppIcon :name="release.visibility === 'internal' ? 'tag' : 'box'" :size="14" /><span class="name-text">{{ name }}</span></span>
        <span class="version" :aria-hidden="!release.version">{{ release.version || '\u00a0' }}</span>
      </button>
    </div>
    <span class="release-status" :data-state="release.state">{{ releaseState(release) }}</span>
    <span class="release-hours mono">{{ Number(release.rollup.open_hours.toFixed(1)) }}h<span class="phone-word"> open</span></span>
    <span class="release-progress" :data-tip="`${release.rollup.completed} of ${release.rollup.units} finished`"><span class="bar" aria-hidden="true"><i :style="{ width: `${progress}%` }" /></span><span class="mono">{{ release.rollup.completed }} of {{ release.rollup.units }}</span></span>
    <span class="release-outlook" :data-tip="outlook">{{ outlook }}</span>
    <span class="release-agents" :data-tip="agents === '—' ? 'Agent activity not reported' : agents">{{ agents }}</span>
    <button type="button" class="release-more" :disabled="!actionsAvailable || disabled" :aria-label="`Actions for ${name}`" :data-tip="actionsAvailable ? 'Release actions' : 'Release actions unavailable'" @click="emit('menu', $event.currentTarget as HTMLElement)"><AppIcon name="more" :size="15" /></button>
  </div>
</template>
<style scoped>
.release-row { display: grid; grid-template-columns: minmax(0, 1fr) 7rem 4.5rem 8rem 8rem 10rem 44px; align-items: center; gap: 8px; min-height: 64px; border-bottom: 1px solid var(--line); padding: 0 8px 0 0; font-size: 12.5px; }
.release-row.expanded { background: color-mix(in srgb, var(--surface-sunken) 55%, transparent); }
.release-row.scoped { background: var(--row-selected); }
.release-title { display: flex; min-width: 0; align-items: center; }
.handle-slot { width: 12px; flex-shrink: 0; }
.chevron, .release-more { display: grid; place-items: center; min-width: 32px; height: 44px; border: 0; border-radius: 6px; padding: 0; background: transparent; color: var(--ink-2); flex-shrink: 0; }
.chevron[aria-expanded="true"] svg { transform: rotate(90deg); }
button:hover:not(:disabled) { background: var(--row-hover); }
button:focus-visible { box-shadow: var(--focus-ring); outline: none; }
.release-name { flex: 1; min-width: 0; height: 64px; padding: 5px 4px; border: 0; background: transparent; color: var(--ink); text-align: left; }
.name-line { display: flex; gap: 8px; align-items: center; min-width: 0; }
.name-line svg { flex-shrink: 0; color: var(--ink-3); }
.name-text { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; font-weight: 650; line-height: 17px; }
.version { display: block; padding-left: 22px; font: 10px/14px var(--mono); opacity: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); }
.release-name:hover .version, .release-name:focus .version { opacity: 1; }
.release-progress { display: flex; align-items: center; gap: 8px; font-size: 11px; }
.bar { width: 40px; height: 4px; flex-shrink: 0; background: var(--line); border-radius: 4px; overflow: hidden; }
.bar i { display: block; height: 100%; background: var(--teal); }
.release-status { font-weight: 600; }
.release-outlook, .release-agents { color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.phone-word { display: none; }
@media (prefers-reduced-motion: no-preference) { .version { transition: opacity .15s; } .chevron svg { transition: transform .15s; } }
@container releases (max-width: 1100px) { .release-row { grid-template-columns: minmax(0, 1fr) 6rem 4rem 6.5rem 6rem 7rem 44px; gap: 6px; } .bar { display: none; } }
@container releases (max-width: 760px) {
  .release-row { grid-template-columns: minmax(0, 1fr) 6rem 44px; grid-template-rows: 64px 24px 24px; min-height: 112px; gap: 0 8px; padding-bottom: 0; }
  .release-title { grid-column: 1 / 3; grid-row: 1; padding-right: 4px; }
  .release-more { grid-column: 3; grid-row: 1; }
  .release-status { grid-column: 2; grid-row: 2; text-align: right; }
  .release-hours { grid-column: 1; grid-row: 2; padding-left: 44px; }
  .release-progress { grid-column: 1; grid-row: 3; padding-left: 44px; }
  .release-outlook { grid-column: 2 / 4; grid-row: 3; text-align: right; }
  .release-agents { grid-column: 1 / 4; grid-row: 2; justify-self: center; max-width: 40%; }
  .phone-word { display: inline; }
  .bar { display: inline-block; }
}
@media (pointer: coarse) { .chevron { min-width: 44px; } }
</style>
