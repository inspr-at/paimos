<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useId } from 'vue'
import AppIcon from '../AppIcon.vue'
import { LEVEL_LABEL, lowerLevelLabel, providerDisabled, RESIDENCY_LABEL, type PrefLevel, type Residency, type ResidencyLockMode, type ResidencyView } from '../../lib/modelPrefs'
defineProps<{ level: PrefLevel; view: ResidencyView; mode: ResidencyLockMode; locked: boolean; editable: boolean; busy: boolean }>()
const emit = defineEmits<{ choose: [value: Residency]; lock: [value: boolean] }>()
const id = useId()
const values: Residency[] = ['any', 'eu', 'local']
const SUB = { any: 'All registered providers', eu: 'Only routes with EU-hosting evidence', local: 'Only verified local execution' }
function move(event: KeyboardEvent) {
  if (!['ArrowRight', 'ArrowLeft', 'ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) || event.metaKey || event.ctrlKey || event.altKey) return
  const buttons = [...(event.currentTarget as HTMLElement).parentElement!.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')]
  if (!buttons.length) return
  event.preventDefault()
  const index = buttons.indexOf(event.currentTarget as HTMLButtonElement)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (['ArrowRight', 'ArrowDown'].includes(event.key) ? 1 : -1) + buttons.length) % buttons.length
  buttons[next]?.focus(); buttons[next]?.click()
}
</script>
<template>
  <section class="providers" :aria-labelledby="`${id}-title`">
    <h3 :id="`${id}-title`" class="eyebrow">Allowed providers</h3><p class="hint">Which AI providers agents may use, for every kind of work.</p>
    <div class="providers-options" role="radiogroup" aria-label="Allowed providers">
      <button v-for="value in values" :key="value" type="button" role="radio" :aria-checked="view.value === value" :tabindex="view.value === value ? 0 : -1" :disabled="!editable || busy || providerDisabled(value, view, level, mode)" :data-tip="providerDisabled(value, view, level, mode) ? `Locked by ${LEVEL_LABEL[view.locked_by!]}` : undefined" @keydown="move" @click="emit('choose', value)"><span class="knob" /><span><b>{{ RESIDENCY_LABEL[value] }}</b><small>{{ SUB[value] }}</small></span></button>
    </div>
    <div class="provider-origin"><span class="origin-copy"><span class="level-dot" :style="{ '--lv': `var(--level-${view.set_by})` }" />{{ LEVEL_LABEL[view.set_by] }}<span v-if="view.locked_by"> · <AppIcon name="lock" :size="12" /> Locked by {{ LEVEL_LABEL[view.locked_by].toLowerCase() }}</span></span>
      <label v-if="level !== 'project'" class="switch"><input type="checkbox" :checked="!locked" :disabled="!editable || busy" :title="mode === 'warn' ? 'A lower level may choose a looser value, with a warning.' : undefined" @change="emit('lock', !($event.target as HTMLInputElement).checked)">{{ lowerLevelLabel(level) }}</label>
    </div>
    <p class="route-count">{{ view.qualifying_routes }} model {{ view.qualifying_routes === 1 ? 'route qualifies' : 'routes qualify' }} today<span v-if="view.value !== 'any' && !view.qualifying_routes">. Work waits; it never spills to another provider.</span></p>
  </section>
</template>
<style scoped>
.providers { display: grid; gap: 8px; } h3, p { margin: 0; } .route-count { min-height: 36px; } .provider-origin { min-height: 36px; }
.hint, .route-count { color: var(--ink-3); font-size: 12px; }
.providers-options { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; }
.providers-options button { display: flex; align-items: flex-start; gap: 9px; height: 88px; min-width: 0; padding: 12px; border: 0; border-radius: 12px; background: transparent; box-shadow: inset 0 0 0 1px var(--line); color: var(--ink); text-align: left; }
.providers-options button[aria-checked="true"] { background: color-mix(in srgb, var(--lv) 8%, var(--surface)); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--lv) 60%, transparent); }
.knob { width: 16px; height: 16px; margin-top: 2px; flex: none; border-radius: 50%; box-shadow: inset 0 0 0 1.5px var(--line-2); }
[aria-checked="true"] small { color: var(--ink-2); }
[aria-checked="true"] .knob { box-shadow: inset 0 0 0 5px var(--lv); }
b { font-size: 13px; } small { display: block; margin-top: 3px; color: var(--ink-3); font-size: 12px; line-height: 1.4; }
.provider-origin { display: flex; justify-content: space-between; gap: 10px; flex-wrap: wrap; font-size: 12px; color: var(--ink-2); }
.origin-copy, .origin-copy > span { display: inline-flex; align-items: center; gap: 5px; }
.switch { white-space: normal; font-size: 12px; }
.provider-warning { display: flex; align-items: flex-start; gap: 8px; padding: 10px 12px; background: var(--gold-wash); border-radius: 10px; color: var(--warn-ink); font-size: 12px; }
.provider-warning svg { flex: none; margin-top: 2px; }
@media (max-width: 600px) { .providers-options { grid-template-columns: 1fr; gap: 6px; } .providers-options button { height: 64px; padding: 8px 12px; } .provider-origin { display: grid; grid-template-rows: 18px 44px; gap: 8px; min-height: 70px; } .switch { min-height: 44px; } }
</style>
