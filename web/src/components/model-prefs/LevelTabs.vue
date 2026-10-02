<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, useId } from 'vue'
import { LEVELS, type PrefLevel } from '../../lib/modelPrefs'
const props = defineProps<{ level: PrefLevel; project?: string; busy: boolean }>()
const emit = defineEmits<{ change: [level: PrefLevel] }>()
const id = useId()
const levels = computed(() => LEVELS.filter(l => l !== 'project' || props.project))
function keys(event: KeyboardEvent, index: number) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key) || event.metaKey || event.ctrlKey || event.altKey) return
  event.preventDefault()
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? levels.value.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + levels.value.length) % levels.value.length
  if (!props.busy) { emit('change', levels.value[next]!); (event.currentTarget as HTMLElement).parentElement?.querySelectorAll<HTMLButtonElement>('button')[next]?.focus() }
}
</script>
<template>
  <div class="editing"><span class="editing-label">You are editing</span><div class="level-tabs" role="tablist" aria-label="Preference level">
    <button v-for="(item, i) in levels" :id="`${id}-${item}`" :key="item" type="button" role="tab" :aria-selected="level === item" :tabindex="level === item ? 0 : -1" :disabled="busy" @click="emit('change', item)" @keydown="keys($event, i)"><span class="level-dot" :style="{ '--lv': `var(--level-${item})` }" />{{ item === 'default' ? 'Default' : item === 'person' ? 'You' : `Project ${project}` }}</button>
  </div></div>
</template>
<style scoped>
.editing { display: flex; align-items: center; gap: 12px; min-width: 0; }
.editing-label { flex: none; font-size: 12.5px; font-weight: 600; color: var(--ink-3); }
.level-tabs { display: flex; min-width: 0; gap: 2px; padding: 3px; border-radius: 12px; background: var(--seg-bg); }
button { display: flex; align-items: center; justify-content: center; gap: 7px; height: 38px; padding: 0 12px; border: 0; border-radius: 9px; background: transparent; font-weight: 600; color: var(--ink-2); min-width: 0; overflow-wrap: anywhere; }
button[aria-selected="true"] { background: var(--seg-on); color: var(--ink); box-shadow: var(--shadow-btn); }
@media (max-width: 600px) { .editing { display: grid; gap: 6px; } .level-tabs { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); } button { height: 44px; padding: 0 5px; font-size: 12px; } }
</style>
