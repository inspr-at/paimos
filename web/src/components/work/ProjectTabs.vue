<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
import type { ProjectTab } from './projectNavigation'

defineProps<{ items: readonly ProjectTab[]; selected: string; label: string; sections?: boolean }>()
const emit = defineEmits<{ select: [id: string] }>()
function move(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const buttons = Array.from((event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="tab"]'))
  const index = buttons.indexOf(event.target as HTMLButtonElement)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1
    : (index + (event.key === 'ArrowRight' ? 1 : -1) + buttons.length) % buttons.length
  buttons[next]?.focus()
  buttons[next]?.click()
}
</script>

<template>
  <div class="project-tabs" :class="{ sections }" role="tablist" :aria-label="label" @keydown.stop="move">
    <button v-for="item in items" :key="item.id" type="button" role="tab" :aria-selected="selected === item.id"
      :tabindex="selected === item.id ? 0 : -1" @click="emit('select', item.id)">
      <AppIcon :name="item.icon" :size="15" /><span>{{ item.label }}</span>
    </button>
  </div>
</template>

<style scoped>
.project-tabs { display: inline-flex; flex-shrink: 0; align-items: center; gap: 2px; padding: 3px; border-radius: 9px; background: var(--seg-bg); }
button { display: inline-flex; align-items: center; justify-content: center; gap: 6px; min-height: 28px; padding: 0 10px; border: 0; border-radius: 6px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
button svg { flex-shrink: 0; }
button:hover { background: var(--row-hover); color: var(--ink); }
button[aria-selected="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: 0 1px 2px rgba(32, 60, 61, .12), inset 0 0 0 1px var(--glass-edge); }
button:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; }
.sections { margin-top: 18px; gap: 4px; padding: 0; background: transparent; }
.sections button { min-height: 36px; padding: 0 14px; font-size: 13.5px; }
.sections button[aria-selected="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--line); }
@media (max-width: 600px) {
  .project-tabs { max-width: 100%; }
  button { flex: 1; min-height: 40px; padding: 0 9px; }
  .sections { display: flex; }
  .sections button { flex: 1; min-width: 0; min-height: 44px; padding: 0 8px; }
}
</style>
